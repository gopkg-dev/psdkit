package psdfont

import "unsafe"

type deltaSetIndex struct{ outer, inner uint16 }

type horizontalVariation struct {
	store                *itemVariationStore
	advance, left, right []deltaSetIndex
	source               Span
}

func readDeltaSetMap(data []byte, base int64, offset int, budget *fontMetadataBudget) ([]deltaSetIndex, error) {
	if offset < 0 || offset > len(data)-4 {
		return nil, fontError(ErrMalformed, "variation.map", base+int64(offset), "mapping header exceeds source")
	}
	format, entryFormat := data[offset], data[offset+1]
	header, count := 4, uint64(u16(data, offset+2))
	if format == 1 {
		if offset > len(data)-6 {
			return nil, fontError(ErrMalformed, "variation.map", base+int64(offset), "format-one header is truncated")
		}
		header, count = 6, uint64(u32(data, offset+2))
	} else if format != 0 {
		return nil, fontError(ErrUnsupported, "variation.map", base+int64(offset), "unsupported delta map format")
	}
	width, innerBits := int((entryFormat>>4)&3)+1, uint(entryFormat&15)+1
	if entryFormat&0xc0 != 0 || count == 0 || innerBits > uint(width*8) ||
		count > uint64((len(data)-offset-header)/width) {
		return nil, fontError(ErrMalformed, "variation.map", base+int64(offset), "invalid entry format or mapping count")
	}
	if err := budget.array(count, int64(unsafe.Sizeof(deltaSetIndex{})),
		"variation.map.memory", base+int64(offset)); err != nil {
		return nil, err
	}
	result := make([]deltaSetIndex, int(count))
	mask := uint32(1)<<innerBits - 1
	for index := range result {
		position := offset + header + index*width
		if err := budget.step("variation.map.entry", base+int64(position)); err != nil {
			return nil, err
		}
		var value uint32
		for _, octet := range data[position : position+width] {
			value = value<<8 | uint32(octet)
		}
		outer, inner := value>>innerBits, value&mask
		if outer > 65535 {
			return nil, fontError(ErrMalformed, "variation.map.index", base+int64(position), "outer delta index exceeds uint16")
		}
		result[index] = deltaSetIndex{outer: uint16(outer), inner: uint16(inner)}
	}
	if err := budget.ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (f *Face) readHorizontalVariations(budget *fontMetadataBudget) error {
	span, exists := f.tables["HVAR"]
	if !exists || f.variation == nil || len(f.variation.axes) == 0 {
		return nil
	}
	data, err := f.table("HVAR", 20)
	if err != nil {
		return err
	}
	if u16(data, 0) != 1 || u16(data, 2) != 0 {
		return fontError(ErrUnsupported, "HVAR.version", span.Offset, "unsupported HVAR version")
	}
	if err := budget.reserve(int64(unsafe.Sizeof(horizontalVariation{})), "HVAR.memory", span.Offset); err != nil {
		return err
	}
	storeOffset := uint64(u32(data, 4))
	if storeOffset == 0 || storeOffset > uint64(len(data)) {
		return fontError(ErrMalformed, "HVAR.store", span.Offset+4, "required item store offset exceeds HVAR")
	}
	store, err := readItemVariationStore(data, span.Offset, int(storeOffset), len(f.variation.axes), budget)
	if err != nil {
		return err
	}
	if err := store.prepareScalars(f.variation.normalized, budget); err != nil {
		return err
	}
	result := &horizontalVariation{store: store, source: span}
	for index, target := range []*[]deltaSetIndex{&result.advance, &result.left, &result.right} {
		offset := uint64(u32(data, 8+index*4))
		if offset == 0 {
			continue
		}
		if offset > uint64(len(data)) {
			return fontError(ErrMalformed, "HVAR.map", span.Offset+8+int64(index)*4, "mapping offset exceeds HVAR")
		}
		*target, err = readDeltaSetMap(data, span.Offset, int(offset), budget)
		if err != nil {
			return err
		}
		for _, deltaIndex := range *target {
			if err := budget.step("HVAR.mapIndex", span.Offset+int64(offset)); err != nil {
				return err
			}
			if err := store.validateIndex(deltaIndex, "HVAR.mapIndex", span.Offset+int64(offset)); err != nil {
				return err
			}
		}
	}
	if len(result.advance) == 0 {
		for glyph := uint32(0); glyph < uint32(f.NumGlyphs); glyph++ {
			if err := budget.step("HVAR.implicitAdvance", span.Offset); err != nil {
				return err
			}
			if err := store.validateIndex(deltaSetIndex{inner: uint16(glyph)}, "HVAR.implicitAdvance", span.Offset); err != nil {
				return err
			}
		}
	}
	if err := budget.ctx.Err(); err != nil {
		return err
	}
	f.hvar = result
	return nil
}

func mappedDeltaIndex(mapping []deltaSetIndex, glyph uint16) deltaSetIndex {
	if len(mapping) == 0 {
		return deltaSetIndex{inner: glyph}
	}
	return mapping[min(int(glyph), len(mapping)-1)]
}

func (f *Face) instanceAdvance(id uint16, step func(string, int64) error) (float64, error) {
	advance, _, err := f.metrics(id)
	if err != nil {
		return 0, err
	}
	if f.hvar == nil {
		return float64(advance), nil
	}
	index := mappedDeltaIndex(f.hvar.advance, id)
	delta, err := f.hvar.store.delta(index.outer, index.inner, step)
	if err != nil {
		return 0, err
	}
	return float64(advance) + delta, nil
}
