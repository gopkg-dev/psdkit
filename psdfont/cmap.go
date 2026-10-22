package psdfont

import (
	"context"
	"unicode/utf8"
)

type cmapTable struct {
	span   Span
	format uint16
	count  int
}

func (f *Face) decodeCmap(ctx context.Context, limits DecodeOptions) error {
	data, err := f.table("cmap", 4)
	if err != nil {
		return err
	}
	span := f.tables["cmap"]
	if u16(data, 0) != 0 {
		return fontError(ErrUnsupported, "cmap.version", span.Offset, "unsupported cmap version")
	}
	count := int(u16(data, 2))
	if count > limits.MaxCmapEntries {
		return fontError(ErrLimit, "cmap.records", span.Offset+2, "encoding count exceeds MaxCmapEntries")
	}
	if 4+count*8 > len(data) {
		return fontError(ErrMalformed, "cmap.records", span.Offset+4, "truncated encoding directory")
	}
	best := -1
	for index := range count {
		if err := ctx.Err(); err != nil {
			return err
		}
		position := 4 + index*8
		platform, encoding, offset := u16(data, position), u16(data, position+2), uint64(u32(data, position+4))
		if offset+2 > uint64(len(data)) {
			return fontError(ErrMalformed, "cmap.subtable", span.Offset+int64(position), "encoding subtable is outside cmap")
		}
		format := u16(data, int(offset))
		unicode := platform == 0 || platform == 3 && (encoding == 1 || encoding == 10)
		if !unicode || format != 4 && format != 12 {
			continue
		}
		score := 1
		if platform == 0 {
			score++
		}
		if format == 12 {
			score += 4
		}
		if platform == 3 && encoding == 10 {
			score++
		}
		if score <= best {
			continue
		}
		minimum := uint64(16)
		if offset+minimum > uint64(len(data)) {
			return fontError(ErrMalformed, "cmap.subtable", span.Offset+int64(offset), "truncated Unicode subtable")
		}
		length := uint64(u16(data, int(offset)+2))
		if format == 12 {
			length = uint64(u32(data, int(offset)+4))
		}
		if length < minimum || length > uint64(len(data))-offset {
			return fontError(ErrMalformed, "cmap.subtable", span.Offset+int64(offset), "Unicode subtable length is invalid")
		}
		selected := cmapTable{span: Span{Offset: span.Offset + int64(offset), Length: int64(length)}, format: format}
		subtable := data[int(offset):int(offset+length)]
		if err := f.validateCmap(ctx, subtable, &selected, limits); err != nil {
			return err
		}
		f.cmap, best = selected, score
	}
	if best < 0 {
		return fontError(ErrUnsupported, "cmap.encoding", span.Offset, "no supported Unicode cmap format 4 or 12")
	}
	return nil
}

func (f *Face) validateCmap(ctx context.Context, data []byte, table *cmapTable, limits DecodeOptions) error {
	if table.format == 12 {
		if u16(data, 2) != 0 {
			return fontError(ErrMalformed, "cmap.reserved", table.span.Offset+2, "nonzero format-twelve reserved field")
		}
		count := uint64(u32(data, 12))
		if count > uint64(limits.MaxCmapEntries) {
			return fontError(ErrLimit, "cmap.groups", table.span.Offset+12, "group count exceeds MaxCmapEntries")
		}
		if count*12+16 > uint64(len(data)) {
			return fontError(ErrMalformed, "cmap.groups", table.span.Offset+16, "truncated format-twelve groups")
		}
		previous := uint32(0)
		for index := 0; index < int(count); index++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			position := 16 + index*12
			start, end, glyph := u32(data, position), u32(data, position+4), u32(data, position+8)
			if start > end || end > utf8.MaxRune || index != 0 && start <= previous ||
				uint64(glyph)+uint64(end-start) >= uint64(f.NumGlyphs) {
				return fontError(ErrMalformed, "cmap.group", table.span.Offset+int64(position),
					"Unicode ranges or glyph indices are not valid and ordered")
			}
			previous = end
		}
		table.count = int(count)
		return nil
	}
	segments := int(u16(data, 6))
	if segments == 0 || segments%2 != 0 {
		return fontError(ErrMalformed, "cmap.segments", table.span.Offset+6, "invalid doubled segment count")
	}
	segments /= 2
	if segments > limits.MaxCmapEntries {
		return fontError(ErrLimit, "cmap.segments", table.span.Offset+6, "segment count exceeds MaxCmapEntries")
	}
	if 16+segments*8 > len(data) || u16(data, 14+segments*2) != 0 {
		return fontError(ErrMalformed, "cmap.segments", table.span.Offset+14, "truncated segments or nonzero reserved pad")
	}
	previous := uint16(0)
	for index := range segments {
		if index%256 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		start, end := u16(data, 16+segments*2+index*2), u16(data, 14+index*2)
		if start > end || index != 0 && start <= previous {
			return fontError(ErrMalformed, "cmap.segment", table.span.Offset+int64(14+index*2),
				"format-four ranges are not ordered or overlap")
		}
		previous = end
		for character := uint32(start); character <= uint32(end); character++ {
			if character%256 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			glyph, ok := cmapFourGlyph(data, segments, index, uint16(character))
			if !ok || glyph >= f.NumGlyphs {
				return fontError(ErrMalformed, "cmap.glyph", table.span.Offset,
					"glyph array offset or mapped glyph is outside the font")
			}
		}
	}
	if previous != 0xffff {
		return fontError(ErrMalformed, "cmap.sentinel", table.span.Offset, "format-four map lacks its final sentinel")
	}
	table.count = segments
	return nil
}

func cmapFourGlyph(data []byte, segments, index int, character uint16) (uint16, bool) {
	delta := u16(data, 16+segments*4+index*2)
	position := 16 + segments*6 + index*2
	offset := u16(data, position)
	if offset == 0 {
		return character + delta, true
	}
	start := u16(data, 16+segments*2+index*2)
	position += int(offset) + int(character-start)*2
	if offset%2 != 0 || position < 16+segments*8 || position+2 > len(data) {
		return 0, false
	}
	glyph := u16(data, position)
	if glyph != 0 {
		glyph += delta
	}
	return glyph, true
}

// LookupRune maps a Unicode scalar through the selected Unicode cmap without .notdef substitution.
// Format 12 takes precedence over format 4; a zero mapping returns ErrMissingGlyph.
func (f *Face) LookupRune(ctx context.Context, character rune) (uint16, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	if f == nil || f.raw == nil || character < 0 || character > utf8.MaxRune ||
		character >= 0xd800 && character <= 0xdfff {
		return 0, fontError(ErrMalformed, "cmap.character", 0, "missing face or invalid Unicode scalar")
	}
	data := f.raw[int(f.cmap.span.Offset):int(f.cmap.span.Offset+f.cmap.span.Length)]
	low, high := 0, f.cmap.count
	var glyph uint16
	if f.cmap.format == 12 {
		for low < high {
			middle := low + (high-low)/2
			position := 16 + middle*12
			start, end := u32(data, position), u32(data, position+4)
			if uint32(character) < start {
				high = middle
			} else if uint32(character) > end {
				low = middle + 1
			} else {
				glyph = uint16(u32(data, position+8) + uint32(character) - start)
				break
			}
		}
	} else if character <= 0xffff {
		for low < high {
			middle := low + (high-low)/2
			end := u16(data, 14+middle*2)
			if uint16(character) > end {
				low = middle + 1
			} else {
				high = middle
			}
		}
		if low < f.cmap.count && uint16(character) >= u16(data, 16+f.cmap.count*2+low*2) {
			glyph, _ = cmapFourGlyph(data, f.cmap.count, low, uint16(character))
		}
	}
	if glyph == 0 {
		return 0, ErrMissingGlyph
	}
	return glyph, nil
}
