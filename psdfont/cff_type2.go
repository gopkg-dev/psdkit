package psdfont

import (
	"context"
	"math"
	"slices"
	"unsafe"
)

type type2Reader struct {
	face             *Face
	ctx              context.Context
	limits           GlyphOptions
	glyph            *Glyph
	private          cffPrivate
	stack            [48]float64
	transient        [32]float64
	stored           [32]bool
	count            int
	hints            int
	widthSeen        bool
	open             bool
	x, y             float64
	allocated        int64
	operations       int
	visits           int
	points           int
	active           [11]Span
	programDepth     int
	component        bool
	offsetX, offsetY float64
}

func (r *type2Reader) fail(kind error, offset int64, detail string) error {
	return fontError(kind, "cff.charstring", offset, detail)
}

func (r *type2Reader) step(offset int64) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if r.operations >= r.limits.MaxOperations {
		return r.fail(ErrLimit, offset, "Type2 interpretation exceeds MaxOperations")
	}
	r.operations++
	return nil
}

func (r *type2Reader) reserve(length int64) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if length < 0 || length > r.limits.MaxWorkingBytes-r.allocated {
		return r.fail(ErrLimit, r.glyph.Source.Offset, "owned Type2 outline exceeds MaxWorkingBytes")
	}
	r.allocated += length
	return nil
}

func (r *type2Reader) push(value float64, offset int64) error {
	if r.count == len(r.stack) {
		return r.fail(ErrLimit, offset, "Type2 argument stack exceeds 48 operands")
	}
	if !finite(value) || math.Abs(value) > 1<<40 {
		return r.fail(ErrLimit, offset, "Type2 arithmetic exceeds supported precision")
	}
	r.stack[r.count], r.count = value, r.count+1
	return nil
}

func (r *type2Reader) pop(offset int64) (float64, error) {
	if r.count == 0 {
		return 0, r.fail(ErrMalformed, offset, "Type2 argument stack underflow")
	}
	r.count--
	return r.stack[r.count], nil
}

func (r *type2Reader) integer(value float64, offset int64) (int, error) {
	if math.Trunc(value) != value || value < -1<<30 || value > 1<<30 {
		return 0, r.fail(ErrMalformed, offset, "Type2 index is not a supported integer")
	}
	return int(value), nil
}

func (r *type2Reader) width(extra bool) {
	if r.widthSeen {
		return
	}
	r.glyph.CharstringWidth = r.private.defaultWidth
	if extra {
		r.glyph.CharstringWidth = r.private.nominalWidth + r.stack[0]
		copy(r.stack[:], r.stack[1:r.count])
		r.count--
	}
	r.widthSeen = true
}

func (r *type2Reader) command(value Command) error {
	if len(r.glyph.Path) == cap(r.glyph.Path) {
		capacity := max(8, cap(r.glyph.Path)*2)
		if err := r.reserve(int64(capacity) * int64(unsafe.Sizeof(Command{}))); err != nil {
			return err
		}
		owned := make([]Command, len(r.glyph.Path), capacity)
		copy(owned, r.glyph.Path)
		r.glyph.Path = owned
	}
	r.glyph.Path = append(r.glyph.Path, value)
	return nil
}

func (r *type2Reader) point(x, y float64, onCurve bool) (Point, error) {
	matrix := r.private.matrix
	if r.offsetX != 0 || r.offsetY != 0 {
		x, y = x+r.offsetX, y+r.offsetY
	}
	value := Point{X: matrix[0]*x + matrix[2]*y + matrix[4],
		Y: matrix[1]*x + matrix[3]*y + matrix[5], OnCurve: onCurve}
	if !finite(value.X) || !finite(value.Y) || math.Abs(value.X) > 1<<30 || math.Abs(value.Y) > 1<<30 {
		return Point{}, r.fail(ErrLimit, r.glyph.Source.Offset, "Type2 coordinate exceeds supported precision")
	}
	if r.points >= r.limits.MaxPoints {
		return Point{}, r.fail(ErrLimit, r.glyph.Source.Offset, "Type2 points exceed MaxPoints")
	}
	index := len(r.glyph.Contours) - 1
	contour := r.glyph.Contours[index]
	if len(contour) == cap(contour) {
		capacity := max(8, cap(contour)*2)
		if err := r.reserve(int64(capacity) * int64(unsafe.Sizeof(Point{}))); err != nil {
			return Point{}, err
		}
		owned := make([]Point, len(contour), capacity)
		copy(owned, contour)
		contour = owned
	}
	r.glyph.Contours[index] = append(contour, value)
	r.points++
	return value, nil
}

func (r *type2Reader) close() error {
	if !r.open {
		return nil
	}
	r.open = false
	return r.command(Command{Op: Close})
}

func (r *type2Reader) move(dx, dy float64) error {
	if err := r.close(); err != nil {
		return err
	}
	if len(r.glyph.Contours) >= r.limits.MaxContours {
		return r.fail(ErrLimit, r.glyph.Source.Offset, "Type2 contours exceed MaxContours")
	}
	if len(r.glyph.Contours) == cap(r.glyph.Contours) {
		capacity := max(4, cap(r.glyph.Contours)*2)
		if err := r.reserve(int64(capacity) * int64(unsafe.Sizeof([]Point{}))); err != nil {
			return err
		}
		owned := make([][]Point, len(r.glyph.Contours), capacity)
		copy(owned, r.glyph.Contours)
		r.glyph.Contours = owned
	}
	r.glyph.Contours = append(r.glyph.Contours, nil)
	r.x, r.y, r.open = r.x+dx, r.y+dy, true
	value, err := r.point(r.x, r.y, true)
	if err != nil {
		return err
	}
	return r.command(Command{Op: Move, X: value.X, Y: value.Y})
}

func (r *type2Reader) line(dx, dy float64) error {
	if !r.open {
		return r.fail(ErrMalformed, r.glyph.Source.Offset, "Type2 line precedes moveto")
	}
	r.x, r.y = r.x+dx, r.y+dy
	value, err := r.point(r.x, r.y, true)
	if err != nil {
		return err
	}
	return r.command(Command{Op: Line, X: value.X, Y: value.Y})
}

func (r *type2Reader) curve(values [6]float64) error {
	if !r.open {
		return r.fail(ErrMalformed, r.glyph.Source.Offset, "Type2 curve precedes moveto")
	}
	x1, y1 := r.x+values[0], r.y+values[1]
	x2, y2 := x1+values[2], y1+values[3]
	r.x, r.y = x2+values[4], y2+values[5]
	first, err := r.point(x1, y1, false)
	if err != nil {
		return err
	}
	second, err := r.point(x2, y2, false)
	if err != nil {
		return err
	}
	target, err := r.point(r.x, r.y, true)
	if err != nil {
		return err
	}
	return r.command(Command{Op: Cubic, X: target.X, Y: target.Y, ControlX: first.X, ControlY: first.Y,
		Control2X: second.X, Control2Y: second.Y})
}

func subrBias(count int) int {
	if count < 1240 {
		return 107
	}
	if count < 33900 {
		return 1131
	}
	return 32768
}

// execute returns true only when an endchar terminates the complete glyph, including from a subroutine.
func (r *type2Reader) execute(span Span, depth int, subroutine bool) (bool, error) {
	if depth > 10 || depth+r.programDepth > r.limits.MaxDepth || r.visits >= r.limits.MaxComponents {
		return false, r.fail(ErrLimit, span.Offset, "Type2 subroutine depth or visits exceed limits")
	}
	if span.Length == 0 || span.Length > 65535 {
		return false, r.fail(ErrLimit, span.Offset, "Type2 charstring length is empty or exceeds 65535")
	}
	if slices.Contains(r.active[:depth], span) {
		return false, r.fail(ErrMalformed, span.Offset, "cyclic Type2 subroutine calls")
	}
	r.active[depth], r.visits = span, r.visits+1
	data := r.face.raw[int(span.Offset):int(span.Offset+span.Length)]
	for position := 0; position < len(data); {
		offset := span.Offset + int64(position)
		if err := r.step(offset); err != nil {
			return false, err
		}
		value, next, numeric, err := cffNumber(data, position, false)
		if err != nil {
			return false, r.fail(ErrMalformed, offset, "truncated Type2 number")
		}
		if numeric {
			if err := r.push(value, offset); err != nil {
				return false, err
			}
			position = next
			continue
		}
		operator := int(data[position])
		position++
		if operator == 10 || operator == 29 {
			value, err := r.pop(offset)
			if err != nil {
				return false, err
			}
			index, err := r.integer(value, offset)
			if err != nil {
				return false, err
			}
			subrs := r.private.subrs
			if operator == 29 {
				subrs = r.face.cff.globalSubrs
			}
			index += subrBias(len(subrs))
			if index < 0 || index >= len(subrs) {
				return false, r.fail(ErrMalformed, offset, "Type2 call refers to absent subroutine")
			}
			ended, err := r.execute(subrs[index], depth+1, true)
			if ended && position != len(data) {
				return false, r.fail(ErrMalformed, offset, "Type2 bytes follow terminating subroutine call")
			}
			if err != nil || ended {
				return ended, err
			}
			continue
		}
		if operator == 11 {
			if !subroutine {
				return false, r.fail(ErrMalformed, offset, "Type2 return outside subroutine")
			}
			if position != len(data) {
				return false, r.fail(ErrMalformed, offset, "Type2 bytes follow return")
			}
			return false, nil
		}
		if operator == 12 {
			if position == len(data) {
				return false, r.fail(ErrMalformed, offset, "truncated Type2 escaped operator")
			}
			operator, position = 256+int(data[position]), position+1
			if err := r.escaped(operator, offset); err != nil {
				return false, err
			}
			continue
		}
		if operator == 1 || operator == 3 || operator == 18 || operator == 23 || operator == 19 || operator == 20 {
			r.width(r.count%2 != 0)
			if r.count%2 != 0 || r.open && r.count != 0 ||
				r.count == 0 && operator != 19 && operator != 20 {
				return false, r.fail(ErrMalformed, offset, "invalid Type2 stem arguments or order")
			}
			r.hints += r.count / 2
			if r.hints > 96 {
				return false, r.fail(ErrLimit, offset, "Type2 stem hints exceed 96")
			}
			if r.glyph.CFFHints != nil {
				if err := r.preserveHintStems(operator, offset); err != nil {
					return false, err
				}
			}
			r.count = 0
			if operator == 19 || operator == 20 {
				length := (r.hints + 7) / 8
				if length == 0 || length > len(data)-position {
					return false, r.fail(ErrMalformed, offset, "Type2 mask is absent or truncated")
				}
				if r.hints%8 != 0 && data[position+length-1]&byte((1<<(8-r.hints%8))-1) != 0 {
					return false, r.fail(ErrMalformed, offset, "Type2 mask has nonzero unused bits")
				}
				if r.glyph.CFFHints != nil {
					if err := r.preserveHintMask(operator, offset, data[position:position+length]); err != nil {
						return false, err
					}
				}
				position += length
			}
			continue
		}
		if operator == 14 {
			if position != len(data) {
				return false, r.fail(ErrMalformed, offset, "Type2 bytes follow endchar")
			}
			r.width(r.count == 1 || r.count == 5)
			if r.count == 4 {
				if r.glyph.CFFHints != nil {
					return false, r.fail(ErrUnsupported, offset, "CFF1 component hints need independent program state")
				}
				if err := r.endcharComponents(offset, depth); err != nil {
					return false, err
				}
				return true, nil
			}
			if r.count != 0 {
				return false, r.fail(ErrMalformed, offset, "unexpected endchar arguments")
			}
			if err := r.close(); err != nil {
				return false, err
			}
			return true, nil
		}
		if err := r.path(operator, offset); err != nil {
			return false, err
		}
		r.count = 0
	}
	return false, r.fail(ErrMalformed, span.Offset+span.Length, "Type2 program lacks endchar or return")
}

func (r *type2Reader) path(operator int, offset int64) error {
	args := r.stack[:r.count]
	invalid := func() error { return r.fail(ErrMalformed, offset, "wrong Type2 path operand count") }
	if operator == 4 || operator == 21 || operator == 22 {
		needed := 1
		if operator == 21 {
			needed = 2
		}
		r.width(r.count == needed+1)
		args = r.stack[:r.count]
		if len(args) != needed {
			return invalid()
		}
		if operator == 4 {
			return r.move(0, args[0])
		}
		if operator == 22 {
			return r.move(args[0], 0)
		}
		return r.move(args[0], args[1])
	}
	if !r.widthSeen {
		if operator != 5 && operator != 6 && operator != 7 && operator != 8 && operator != 24 && operator != 25 &&
			operator != 26 && operator != 27 && operator != 30 && operator != 31 {
			return r.fail(ErrUnsupported, offset, "reserved or unsupported Type2 operator")
		}
		return r.fail(ErrMalformed, offset, "Type2 path precedes initial moveto or hints")
	}
	return r.pathArguments(operator, args, offset)
}

func (r *type2Reader) pathArguments(operator int, args []float64, offset int64) error {
	invalid := func() error { return r.fail(ErrMalformed, offset, "wrong Type2 path operand count") }
	switch operator {
	case 5:
		if len(args) < 2 || len(args)%2 != 0 {
			return invalid()
		}
		for index := 0; index < len(args); index += 2 {
			if err := r.line(args[index], args[index+1]); err != nil {
				return err
			}
		}
	case 6, 7:
		if len(args) == 0 {
			return invalid()
		}
		horizontal := operator == 6
		for _, value := range args {
			dx, dy := 0.0, value
			if horizontal {
				dx, dy = value, 0
			}
			if err := r.line(dx, dy); err != nil {
				return err
			}
			horizontal = !horizontal
		}
	case 8:
		if len(args) < 6 || len(args)%6 != 0 {
			return invalid()
		}
		for index := 0; index < len(args); index += 6 {
			if err := r.curve([6]float64(args[index : index+6])); err != nil {
				return err
			}
		}
	case 24:
		if len(args) < 8 || (len(args)-2)%6 != 0 {
			return invalid()
		}
		for index := 0; index < len(args)-2; index += 6 {
			if err := r.curve([6]float64(args[index : index+6])); err != nil {
				return err
			}
		}
		return r.line(args[len(args)-2], args[len(args)-1])
	case 25:
		if len(args) < 8 || (len(args)-6)%2 != 0 {
			return invalid()
		}
		for index := 0; index < len(args)-6; index += 2 {
			if err := r.line(args[index], args[index+1]); err != nil {
				return err
			}
		}
		return r.curve([6]float64(args[len(args)-6:]))
	case 26, 27:
		if len(args) < 4 || len(args)%4 > 1 {
			return invalid()
		}
		initial := 0.0
		if len(args)%4 == 1 {
			initial, args = args[0], args[1:]
		}
		for index := 0; index < len(args); index += 4 {
			values := [6]float64{initial, args[index], args[index+1], args[index+2], 0, args[index+3]}
			if operator == 27 {
				values = [6]float64{args[index], initial, args[index+1], args[index+2], args[index+3], 0}
			}
			if err := r.curve(values); err != nil {
				return err
			}
			initial = 0
		}
	case 30, 31:
		if len(args) < 4 || len(args)%4 > 1 {
			return invalid()
		}
		horizontal := operator == 31
		for len(args) >= 4 {
			values := [6]float64{0, args[0], args[1], args[2], args[3], 0}
			if horizontal {
				values = [6]float64{args[0], 0, args[1], args[2], 0, args[3]}
			}
			args = args[4:]
			if len(args) == 1 {
				if horizontal {
					values[4] = args[0]
				} else {
					values[5] = args[0]
				}
				args = nil
			}
			if err := r.curve(values); err != nil {
				return err
			}
			horizontal = !horizontal
		}
	default:
		return r.fail(ErrUnsupported, offset, "reserved or unsupported Type2 operator")
	}
	return nil
}

func (r *type2Reader) escaped(operator int, offset int64) error {
	if operator >= 290 && operator <= 293 {
		return r.flex(operator, offset)
	}
	if operator == 256 {
		return nil
	}
	if operator == 279 {
		return r.fail(ErrUnsupported, offset, "Type2 random operator has no calibrated deterministic behavior")
	}
	if operator == 283 {
		if r.count == 0 {
			return r.fail(ErrMalformed, offset, "Type2 dup underflow")
		}
		return r.push(r.stack[r.count-1], offset)
	}
	if operator == 284 {
		if r.count < 2 {
			return r.fail(ErrMalformed, offset, "Type2 exch underflow")
		}
		r.stack[r.count-1], r.stack[r.count-2] = r.stack[r.count-2], r.stack[r.count-1]
		return nil
	}
	if operator == 278 {
		if r.count < 4 {
			return r.fail(ErrMalformed, offset, "Type2 ifelse underflow")
		}
		args := r.stack[r.count-4 : r.count]
		value := args[0]
		if args[2] > args[3] {
			value = args[1]
		}
		r.count -= 4
		return r.push(value, offset)
	}
	value, err := r.pop(offset)
	if err != nil {
		return err
	}
	switch operator {
	case 265:
		return r.push(math.Abs(value), offset)
	case 270:
		return r.push(-value, offset)
	case 261:
		result := 0.0
		if value == 0 {
			result = 1
		}
		return r.push(result, offset)
	case 274:
		return nil
	case 282:
		if value < 0 {
			return r.fail(ErrMalformed, offset, "Type2 sqrt has a negative operand")
		}
		return r.push(math.Sqrt(value), offset)
	case 276, 277:
		index, err := r.integer(value, offset)
		if err != nil {
			return err
		}
		if index < 0 || index >= len(r.transient) {
			return r.fail(ErrMalformed, offset, "Type2 transient index is outside 0..31")
		}
		if operator == 277 {
			if !r.stored[index] {
				return r.fail(ErrMalformed, offset, "Type2 transient value has not been initialized")
			}
			return r.push(r.transient[index], offset)
		}
		stored, err := r.pop(offset)
		if err != nil {
			return err
		}
		r.transient[index], r.stored[index] = stored, true
		return nil
	case 285:
		index, err := r.integer(value, offset)
		if err != nil {
			return err
		}
		index = max(0, index)
		if index >= r.count {
			return r.fail(ErrMalformed, offset, "Type2 index exceeds argument stack")
		}
		return r.push(r.stack[r.count-1-index], offset)
	case 286:
		shift, err := r.integer(value, offset)
		if err != nil {
			return err
		}
		lengthValue, err := r.pop(offset)
		if err != nil {
			return err
		}
		length, err := r.integer(lengthValue, offset)
		if err != nil {
			return err
		}
		if length < 0 || length > r.count {
			return r.fail(ErrMalformed, offset, "Type2 roll count exceeds argument stack")
		}
		if length == 0 {
			return nil
		}
		shift = (shift%length + length) % length
		var rotated [48]float64
		for index := range length {
			rotated[(index+shift)%length] = r.stack[r.count-length+index]
		}
		copy(r.stack[r.count-length:r.count], rotated[:length])
		return nil
	}
	left, err := r.pop(offset)
	if err != nil {
		return err
	}
	result := 0.0
	switch operator {
	case 259:
		if left != 0 && value != 0 {
			result = 1
		}
	case 260:
		if left != 0 || value != 0 {
			result = 1
		}
	case 266:
		result = left + value
	case 267:
		result = left - value
	case 268:
		if value == 0 {
			return r.fail(ErrMalformed, offset, "Type2 division by zero")
		}
		result = left / value
	case 271:
		if left == value {
			result = 1
		}
	case 280:
		result = left * value
	default:
		return r.fail(ErrUnsupported, offset, "reserved Type2 escaped operator")
	}
	return r.push(result, offset)
}

func (r *type2Reader) flex(operator int, offset int64) error {
	args := r.stack[:r.count]
	lengths := [4]int{7, 13, 9, 11}
	if len(args) != lengths[operator-290] {
		return r.fail(ErrMalformed, offset, "wrong Type2 flex argument count")
	}
	var first, second [6]float64
	switch operator {
	case 290:
		first = [6]float64{args[0], 0, args[1], args[2], args[3], 0}
		second = [6]float64{args[4], 0, args[5], -args[2], args[6], 0}
	case 291:
		first, second = [6]float64(args[:6]), [6]float64(args[6:12])
	case 292:
		first = [6]float64{args[0], args[1], args[2], args[3], args[4], 0}
		second = [6]float64{args[5], 0, args[6], args[7], args[8], -(args[1] + args[3] + args[7])}
	case 293:
		first = [6]float64(args[:6])
		second = [6]float64{args[6], args[7], args[8], args[9], 0, 0}
		dx, dy := args[0]+args[2]+args[4]+args[6]+args[8], args[1]+args[3]+args[5]+args[7]+args[9]
		if math.Abs(dx) > math.Abs(dy) {
			second[4], second[5] = args[10], -dy
		} else {
			second[4], second[5] = -dx, args[10]
		}
	}
	if err := r.curve(first); err != nil {
		return err
	}
	if err := r.curve(second); err != nil {
		return err
	}
	r.count = 0
	return nil
}

func (f *Face) cffGlyph(ctx context.Context, id uint16, limits GlyphOptions) (*Glyph, error) {
	if int(id) >= len(f.cff.charstrings) || f.cff.cid && int(id) >= len(f.cff.fdSelect) {
		return nil, fontError(ErrMalformed, "cff.glyph.index", 0, "glyph is outside the owned CFF index")
	}
	advance, bearing, err := f.metrics(id)
	if err != nil {
		return nil, err
	}
	selector := 0
	if f.cff.cid {
		selector = int(f.cff.fdSelect[id])
	}
	glyph := &Glyph{ID: id, Source: f.cff.charstrings[id], AdvanceWidth: advance, LeftSideBearing: bearing}
	reader := type2Reader{face: f, ctx: ctx, limits: limits, glyph: glyph, private: f.cff.private[selector]}
	if err := reader.reserve(int64(unsafe.Sizeof(type2Reader{})) + int64(unsafe.Sizeof(Glyph{}))); err != nil {
		return nil, err
	}
	if limits.PreserveCFFHints {
		if err := reader.preservePrivateHints(); err != nil {
			return nil, err
		}
	}
	ended, err := reader.execute(glyph.Source, 0, false)
	if err != nil {
		return nil, err
	}
	if !ended {
		return nil, reader.fail(ErrMalformed, glyph.Source.Offset, "Type2 glyph did not end")
	}
	if err := cffGlyphBounds(ctx, glyph); err != nil {
		return nil, err
	}
	glyph.reservation = reader.allocated
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return glyph, nil
}

func cffGlyphPreciseBounds(ctx context.Context, glyph *Glyph) ([4]float64, bool, error) {
	bounds := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	include := func(x, y float64) {
		bounds[0], bounds[1] = min(bounds[0], x), min(bounds[1], y)
		bounds[2], bounds[3] = max(bounds[2], x), max(bounds[3], y)
	}
	x, y := 0.0, 0.0
	for _, command := range glyph.Path {
		if err := ctx.Err(); err != nil {
			return [4]float64{}, false, err
		}
		if command.Op == Close {
			continue
		}
		include(command.X, command.Y)
		if command.Op == Cubic {
			for axis := range 2 {
				start, first, second, end := x, command.ControlX, command.Control2X, command.X
				if axis == 1 {
					start, first, second, end = y, command.ControlY, command.Control2Y, command.Y
				}
				a, b, c := -start+3*first-3*second+end, 2*(start-2*first+second), first-start
				var roots [2]float64
				count := 0
				if a == 0 {
					if b != 0 {
						roots[0], count = -c/b, 1
					}
				} else if discriminant := b*b - 4*a*c; discriminant >= 0 {
					root := math.Sqrt(discriminant)
					roots, count = [2]float64{(-b + root) / (2 * a), (-b - root) / (2 * a)}, 2
				}
				for _, t := range roots[:count] {
					if t <= 0 || t >= 1 {
						continue
					}
					u := 1 - t
					px := u*u*u*x + 3*u*u*t*command.ControlX + 3*u*t*t*command.Control2X + t*t*t*command.X
					py := u*u*u*y + 3*u*u*t*command.ControlY + 3*u*t*t*command.Control2Y + t*t*t*command.Y
					include(px, py)
				}
			}
		}
		x, y = command.X, command.Y
	}
	if math.IsInf(bounds[0], 1) {
		return [4]float64{}, false, nil
	}
	return bounds, true, nil
}

func cffGlyphBounds(ctx context.Context, glyph *Glyph) error {
	bounds, hasInk, err := cffGlyphPreciseBounds(ctx, glyph)
	if err != nil {
		return err
	}
	if !hasInk {
		return nil
	}
	for index, value := range bounds {
		value = math.Floor(value)
		if index >= 2 {
			value = math.Ceil(bounds[index])
		}
		if value < -32768 || value > 32767 {
			return fontError(ErrLimit, "cff.glyph.bounds", glyph.Source.Offset, "outline bounds exceed FWORD metadata")
		}
		glyph.Bounds[index] = int16(value)
	}
	return nil
}
