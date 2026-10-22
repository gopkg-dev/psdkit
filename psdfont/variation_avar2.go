package psdfont

// readAvarCrossAxis uses avar-v2 table-relative offsets after the segment maps.
// The declared precision profile retains mapped 16.16 bases and snapshots F2DOT14 scalar inputs.
func readAvarCrossAxis(
	data []byte, span Span, position int, variation *fontVariationData, budget *fontMetadataBudget,
) error {
	if position > len(data)-8 {
		return fontError(ErrMalformed, "avar.crossAxis", span.Offset+int64(position), "missing v2 offsets")
	}
	mapOffset, storeOffset := uint64(u32(data, position)), uint64(u32(data, position+4))
	if mapOffset > uint64(len(data)) || storeOffset > uint64(len(data)) {
		return fontError(ErrMalformed, "avar.crossAxis", span.Offset+int64(position), "v2 offset exceeds avar")
	}
	if mapOffset != 0 {
		indices, err := readDeltaSetMap(data, span.Offset, int(mapOffset), budget)
		if err != nil {
			return err
		}
		variation.avarIndices = indices
	}
	if storeOffset == 0 {
		return nil
	}
	store, err := readItemVariationStore(data, span.Offset, int(storeOffset), len(variation.axes), budget)
	if err != nil {
		return err
	}
	for axis := range variation.axes {
		if err := budget.step("avar.axisIndex", span.Offset+int64(position)); err != nil {
			return err
		}
		index := mappedDeltaIndex(variation.avarIndices, uint16(axis))
		if err := store.validateIndex(index, "avar.axisIndex", span.Offset); err != nil {
			return err
		}
	}
	variation.avarStore = store
	return budget.ctx.Err()
}

func (s *itemVariationStore) validateIndex(index deltaSetIndex, path string, offset int64) error {
	if index.outer == 0xffff && index.inner == 0xffff {
		return nil
	}
	if int(index.outer) >= len(s.data) {
		return fontError(ErrMalformed, path, offset, "outer index exceeds variation store")
	}
	row := &s.data[index.outer]
	if row.present && index.inner >= row.itemCount {
		return fontError(ErrMalformed, path, offset, "inner index exceeds variation row")
	}
	return nil
}
