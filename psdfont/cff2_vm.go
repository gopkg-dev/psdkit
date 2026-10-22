package psdfont

import (
	"context"
	"math"
	"slices"
	"unsafe"
)

type cff2VM struct {
	sink           type2Reader
	font           *cff2Font
	private        cff2Private
	stack          [513]float64
	encodedStack   [513]float64
	operandBlended [513]bool
	hintState      cff2HintState
	count, hints   int
	vsindex        uint16
	vsindexSet     bool
	blended        bool
	active         [11]Span
}

func (r *cff2VM) fail(kind error, offset int64, detail string) error {
	return fontError(kind, "cff2.charstring", offset, detail)
}

func (r *cff2VM) push(value float64, offset int64) error {
	if r.count == len(r.stack) {
		return r.fail(ErrLimit, offset, "CFF2 operand stack exceeds 513")
	}
	if !finite(value) || math.Abs(value) > 1<<40 {
		return r.fail(ErrLimit, offset, "CFF2 operand exceeds supported precision")
	}
	r.stack[r.count], r.encodedStack[r.count], r.operandBlended[r.count] = value, value, false
	r.count++
	return nil
}

func (r *cff2VM) execute(span Span, depth int) error {
	if depth > min(10, r.sink.limits.MaxDepth) || r.sink.visits >= r.sink.limits.MaxComponents {
		return r.fail(ErrLimit, span.Offset, "CFF2 subroutine depth or visits exceed limits")
	}
	if span.Length > 65535 {
		return r.fail(ErrLimit, span.Offset, "CFF2 program exceeds maximum CharString length 65535")
	}
	if span.Offset < 0 || span.Length < 0 || span.Offset > int64(len(r.sink.face.raw)) ||
		span.Length > int64(len(r.sink.face.raw))-span.Offset {
		return r.fail(ErrMalformed, span.Offset, "CFF2 program exceeds owned source")
	}
	if slices.Contains(r.active[:depth], span) {
		return r.fail(ErrMalformed, span.Offset, "cyclic CFF2 subroutine call")
	}
	r.active[depth], r.sink.visits = span, r.sink.visits+1
	data := r.sink.face.raw[span.Offset : span.Offset+span.Length]
	for position := 0; position < len(data); {
		offset := span.Offset + int64(position)
		if err := r.sink.step(offset); err != nil {
			return err
		}
		value, next, numeric, err := cffNumber(data, position, false)
		if err != nil {
			return r.fail(ErrMalformed, offset, "truncated CFF2 number")
		}
		if numeric {
			if err := r.push(value, offset); err != nil {
				return err
			}
			position = next
			continue
		}
		operator := int(data[position])
		position++
		if operator == 10 || operator == 29 {
			if r.count == 0 {
				return r.fail(ErrMalformed, offset, "CFF2 subroutine index is absent")
			}
			r.count--
			index, err := r.sink.integer(r.stack[r.count], offset)
			if err != nil {
				return err
			}
			subrs := r.private.subrs
			if operator == 29 {
				subrs = r.font.globalSubrs
			}
			index += subrBias(len(subrs))
			if index < 0 || index >= len(subrs) {
				return r.fail(ErrMalformed, offset, "CFF2 subroutine index is outside its directory")
			}
			if err := r.execute(subrs[index], depth+1); err != nil {
				return err
			}
			continue
		}
		if operator == 15 {
			if r.vsindexSet || r.blended || r.count != 1 || r.stack[0] < 0 || r.stack[0] > 65535 ||
				math.Trunc(r.stack[0]) != r.stack[0] || r.font.store == nil || int(r.stack[0]) >= len(r.font.store.data) {
				return r.fail(ErrMalformed, offset, "invalid CFF2 vsindex order or value")
			}
			r.vsindex, r.vsindexSet, r.count = uint16(r.stack[0]), true, 0
			continue
		}
		if operator == 16 {
			values := 0
			if r.count > 0 && finite(r.stack[r.count-1]) {
				values = int(r.stack[r.count-1])
			}
			r.count, err = cff2Blend(r.stack[:], r.count, r.font.store, r.vsindex, offset,
				func(_ string, at int64) error { return r.sink.step(at) })
			if err != nil {
				return err
			}
			for index := r.count - values; index < r.count; index++ {
				if err := r.sink.step(offset); err != nil {
					return err
				}
				r.operandBlended[index] = true
			}
			r.blended = true
			continue
		}
		if operator == 1 || operator == 3 || operator == 18 || operator == 23 || operator == 19 || operator == 20 {
			consumed, err := r.hintOperator(operator, data[position:], offset)
			if err != nil {
				return err
			}
			position += consumed
			continue
		}
		if operator == 12 {
			if position == len(data) {
				return r.fail(ErrMalformed, offset, "truncated CFF2 escaped operator")
			}
			operator = 256 + int(data[position])
			position++
			if operator >= 290 && operator <= 293 {
				if r.count > len(r.sink.stack) {
					return r.fail(ErrMalformed, offset, "wrong CFF2 flex operand count")
				}
				copy(r.sink.stack[:], r.stack[:r.count])
				r.sink.count = r.count
				if err := r.sink.flex(operator, offset); err != nil {
					return err
				}
			}
			r.count = 0
			continue
		}
		args := r.stack[:r.count]
		switch operator {
		case 4, 21, 22:
			if err := r.hintPath(offset); err != nil {
				return err
			}
			needed := 1
			if operator == 21 {
				needed = 2
			}
			if len(args) != needed {
				return r.fail(ErrMalformed, offset, "wrong CFF2 moveto operand count")
			}
			dx, dy := 0.0, args[0]
			switch operator {
			case 21:
				dx, dy = args[0], args[1]
			case 22:
				dx, dy = args[0], 0
			}
			err = r.sink.move(dx, dy)
		case 5, 6, 7, 8, 24, 25, 26, 27, 30, 31:
			err = r.sink.pathArguments(operator, args, offset)
		}
		if err != nil {
			return err
		}
		// Unrecognized CFF2 operators are ignored and clear their operand stack.
		r.count = 0
	}
	return r.sink.ctx.Err()
}

func (f *Face) cff2Glyph(ctx context.Context, id uint16, limits GlyphOptions) (*Glyph, error) {
	if int(id) >= len(f.cff2.charstrings) {
		return nil, fontError(ErrMalformed, "cff2.glyph", 0, "glyph is outside CFF2 directory")
	}
	advance, bearing, err := f.metrics(id)
	if err != nil {
		return nil, err
	}
	selector := 0
	if len(f.cff2.fdSelect) != 0 {
		selector = int(f.cff2.fdSelect[id])
	}
	private := f.cff2.private[selector]
	glyph := &Glyph{ID: id, Source: f.cff2.charstrings[id], AdvanceWidth: advance, LeftSideBearing: bearing}
	reader := cff2VM{font: f.cff2, private: private, vsindex: private.vsindex,
		sink: type2Reader{face: f, ctx: ctx, limits: limits, glyph: glyph, widthSeen: true,
			private: cffPrivate{matrix: cffIdentityMatrix}}}
	if err := reader.sink.reserve(int64(unsafe.Sizeof(cff2VM{})) + int64(unsafe.Sizeof(Glyph{}))); err != nil {
		return nil, err
	}
	if err := reader.execute(glyph.Source, 0); err != nil {
		return nil, err
	}
	if reader.count != 0 {
		return nil, reader.fail(ErrMalformed, glyph.Source.Offset, "unused operands at root CFF2 EOF")
	}
	if err := reader.sink.close(); err != nil {
		return nil, err
	}
	if err := cffGlyphBounds(ctx, glyph); err != nil {
		return nil, err
	}
	if err := reader.applyInstanceMetrics(id); err != nil {
		return nil, err
	}
	glyph.reservation = reader.sink.allocated
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return glyph, nil
}
