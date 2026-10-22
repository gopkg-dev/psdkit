package psdfont

import "context"

type kernTable struct {
	span     Span
	count    int
	override bool
}

func (f *Face) decodeKern(ctx context.Context, limits DecodeOptions, reserved int64) error {
	span, found := f.tables["kern"]
	if !found {
		return nil
	}
	data, err := f.table("kern", 4)
	if err != nil {
		return err
	}
	if u16(data, 0) != 0 {
		f.kernUnsupported = true
		return nil
	}
	count := int(u16(data, 2))
	if count > limits.MaxTables || int64(count)*64 > limits.MaxBytes-reserved {
		return fontError(ErrLimit, "kern.subtables", span.Offset+2, "kerning metadata exceeds configured limits")
	}
	f.kerns = make([]kernTable, 0, count)
	f.reservation += int64(count) * 64
	position := 4
	for range count {
		if err := ctx.Err(); err != nil {
			return err
		}
		if position+6 > len(data) {
			return fontError(ErrMalformed, "kern.subtable", span.Offset+int64(position), "truncated kerning subtable")
		}
		length, coverage := int(u16(data, position+2)), u16(data, position+4)
		if length < 6 || length > len(data)-position {
			return fontError(ErrMalformed, "kern.subtable", span.Offset+int64(position), "invalid kerning subtable length")
		}
		if coverage&1 == 0 {
			position += length
			continue
		}
		if u16(data, position) != 0 || coverage>>8 != 0 || coverage&0xf6 != 0 {
			f.kernUnsupported = true
			position += length
			continue
		}
		if length < 14 {
			return fontError(ErrMalformed, "kern.pairs", span.Offset+int64(position), "incomplete format-zero pair header")
		}
		pairs := int(u16(data, position+6))
		if pairs > limits.MaxCmapEntries {
			return fontError(ErrLimit, "kern.pairs", span.Offset+int64(position+6), "pair count exceeds traversal limit")
		}
		if pairs*6+14 > length {
			return fontError(ErrMalformed, "kern.pairs", span.Offset+int64(position+14), "truncated kerning pair array")
		}
		previous := uint32(0)
		for pair := range pairs {
			if pair%256 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			at := position + 14 + pair*6
			key := u32(data, at)
			if pair != 0 && key <= previous || u16(data, at) >= f.NumGlyphs || u16(data, at+2) >= f.NumGlyphs {
				return fontError(ErrMalformed, "kern.pair", span.Offset+int64(at), "pair keys are unordered or outside glyphs")
			}
			previous = key
		}
		f.kerns = append(f.kerns, kernTable{span: Span{Offset: span.Offset + int64(position+14), Length: int64(pairs * 6)},
			count: pairs, override: coverage&8 != 0})
		position += length
	}
	return nil
}

// Kerning returns legacy horizontal kern-format-zero adjustment in design units.
// It does not apply GPOS positioning. Unsupported legacy kern subtables return ErrUnsupported.
func (f *Face) Kerning(ctx context.Context, left, right uint16) (int32, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	if f == nil || left >= f.NumGlyphs || right >= f.NumGlyphs {
		return 0, fontError(ErrMalformed, "kern.glyph", 0, "missing face or glyph index outside the font")
	}
	if f.kernUnsupported {
		return 0, fontError(ErrUnsupported, "kern.format", 0, "non-horizontal or nonzero kerning format is unsupported")
	}
	key, result := uint32(left)<<16|uint32(right), int32(0)
	for _, table := range f.kerns {
		data := f.raw[int(table.span.Offset):int(table.span.Offset+table.span.Length)]
		low, high := 0, table.count
		for low < high {
			middle := low + (high-low)/2
			value := u32(data, middle*6)
			if key < value {
				high = middle
			} else if key > value {
				low = middle + 1
			} else {
				adjustment := int32(i16(data, middle*6+4))
				if table.override {
					result = adjustment
				} else {
					result += adjustment
				}
				break
			}
		}
	}
	return result, nil
}
