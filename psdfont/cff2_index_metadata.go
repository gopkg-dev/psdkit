package psdfont

import "unsafe"

// readCFF2Index reads table-relative CFF2 INDEX objects without narrowing their uint32 count.
func readCFF2Index(
	data []byte,
	base int64,
	offset int,
	budget *fontMetadataBudget,
) ([]Span, int, error) {
	const path = "cff2.index"
	if err := budget.ctx.Err(); err != nil {
		return nil, 0, err
	}
	if base < 0 || base > int64(^uint64(0)>>1)-int64(len(data)) {
		return nil, 0, fontError(ErrMalformed, path, base, "invalid CFF2 source span")
	}
	if offset < 0 || offset > len(data)-4 {
		return nil, 0, fontError(ErrMalformed, path, base, "truncated INDEX count")
	}
	if err := budget.step(path, base+int64(offset)); err != nil {
		return nil, 0, err
	}
	count := uint64(u32(data, offset))
	if count == 0 {
		if err := budget.ctx.Err(); err != nil {
			return nil, 0, err
		}
		return nil, offset + 4, nil
	}
	if len(data)-offset < 5 {
		return nil, 0, fontError(ErrMalformed, path, base+int64(offset), "truncated INDEX offsetSize")
	}
	width := int(data[offset+4])
	if width < 1 || width > 4 || count+1 > uint64((len(data)-offset-5)/width) {
		return nil, 0, fontError(ErrMalformed, path, base+int64(offset), "invalid INDEX offset array")
	}
	begin := offset + 5 + int(count+1)*width
	readOffset := func(index int) uint32 {
		position := offset + 5 + index*width
		value := uint32(0)
		for _, octet := range data[position : position+width] {
			value = value<<8 | uint32(octet)
		}
		return value
	}
	previous, last := readOffset(0), readOffset(int(count))
	if previous != 1 || last < 1 || uint64(last)-1 > uint64(len(data)-begin) {
		return nil, 0, fontError(ErrMalformed, path, base+int64(offset), "INDEX first or final offset differs")
	}
	if err := budget.array(count, int64(unsafe.Sizeof(Span{})), path, base+int64(offset)); err != nil {
		return nil, 0, err
	}
	result := make([]Span, int(count))
	for index := range result {
		at := offset + 5 + index*width
		if err := budget.step(path, base+int64(at)); err != nil {
			return nil, 0, err
		}
		value := readOffset(index + 1)
		if value < previous || uint64(value)-1 > uint64(len(data)-begin) {
			return nil, 0, fontError(ErrMalformed, path, base+int64(at), "INDEX offsets are unordered or out of bounds")
		}
		result[index] = Span{Offset: base + int64(begin) + int64(previous) - 1, Length: int64(value - previous)}
		previous = value
	}
	if err := budget.ctx.Err(); err != nil {
		return nil, 0, err
	}
	return result, begin + int(last) - 1, nil
}

// readCFF2FDSelect expands formats 0, 3 and 4 into one uint16 Font DICT index per glyph.
func readCFF2FDSelect(
	data []byte,
	base int64,
	offset int,
	glyphCount uint16,
	fontDictCount uint32,
	budget *fontMetadataBudget,
) ([]uint16, error) {
	const path = "cff2.fdSelect"
	if err := budget.ctx.Err(); err != nil {
		return nil, err
	}
	if base < 0 || base > int64(^uint64(0)>>1)-int64(len(data)) {
		return nil, fontError(ErrMalformed, path, base, "invalid CFF2 source span")
	}
	if offset < 0 || offset >= len(data) || glyphCount == 0 || fontDictCount == 0 {
		return nil, fontError(ErrMalformed, path, base, "missing FDSelect or glyph/Font DICT count")
	}
	if err := budget.step(path, base+int64(offset)); err != nil {
		return nil, err
	}
	format, position := data[offset], offset+1
	if format == 0 {
		if int(glyphCount) > len(data)-position {
			return nil, fontError(ErrMalformed, path, base+int64(position), "truncated FDSelect format zero")
		}
		if err := budget.array(uint64(glyphCount), 2, path, base+int64(offset)); err != nil {
			return nil, err
		}
		result := make([]uint16, int(glyphCount))
		for index, selector := range data[position : position+int(glyphCount)] {
			if err := budget.step(path, base+int64(position+index)); err != nil {
				return nil, err
			}
			if uint32(selector) >= fontDictCount {
				return nil, fontError(ErrMalformed, path, base+int64(position+index), "FDSelect refers to absent Font DICT")
			}
			result[index] = uint16(selector)
		}
		if err := budget.ctx.Err(); err != nil {
			return nil, err
		}
		return result, nil
	}
	countWidth, recordWidth := 2, 3
	if format == 4 {
		countWidth, recordWidth = 4, 6
	} else if format != 3 {
		return nil, fontError(ErrUnsupported, path, base+int64(offset), "unsupported FDSelect format")
	}
	if countWidth > len(data)-position {
		return nil, fontError(ErrMalformed, path, base+int64(position), "truncated FDSelect range count")
	}
	ranges := uint64(u16(data, position))
	if format == 4 {
		ranges = uint64(u32(data, position))
	}
	position += countWidth
	if len(data)-position < countWidth || ranges == 0 || ranges > uint64(glyphCount) ||
		ranges > uint64((len(data)-position-countWidth)/recordWidth) {
		return nil, fontError(ErrMalformed, path, base+int64(position), "invalid FDSelect range array")
	}
	if err := budget.array(uint64(glyphCount), 2, path, base+int64(offset)); err != nil {
		return nil, err
	}
	result := make([]uint16, int(glyphCount))
	for index := 0; index < int(ranges); index++ {
		at := position + index*recordWidth
		if err := budget.step(path, base+int64(at)); err != nil {
			return nil, err
		}
		first, next := uint32(u16(data, at)), uint32(u16(data, at+recordWidth))
		selector := uint16(data[at+2])
		if format == 4 {
			first, next, selector = u32(data, at), u32(data, at+recordWidth), u16(data, at+4)
		}
		if index == 0 && first != 0 || first >= next || next > uint32(glyphCount) ||
			uint32(selector) >= fontDictCount || index == int(ranges)-1 && next != uint32(glyphCount) {
			return nil, fontError(ErrMalformed, path, base+int64(at), "FDSelect range order, selector or sentinel differs")
		}
		for glyph := int(first); glyph < int(next); glyph++ {
			if err := budget.step(path, base+int64(at)); err != nil {
				return nil, err
			}
			result[glyph] = selector
		}
	}
	if err := budget.ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
