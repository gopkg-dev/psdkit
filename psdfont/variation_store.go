package psdfont

import "unsafe"

type variationRegionAxis struct{ start, peak, end int16 }

type itemVariationData struct {
	present    bool
	itemCount  uint16
	wordCount  uint16
	longWords  bool
	regionIDs  []uint16
	rowSize    int
	deltaBytes []byte
	source     Span
}

type itemVariationStore struct {
	axisCount   int
	regionCount int
	regions     []variationRegionAxis
	data        []itemVariationData
	scalars     []float64
	source      Span
}

func readItemVariationStore(
	data []byte, base int64, offset int, axisCount int, budget *fontMetadataBudget,
) (*itemVariationStore, error) {
	if err := budget.step("variationStore.header", base+int64(offset)); err != nil {
		return nil, err
	}
	if offset < 0 || offset > len(data)-8 {
		return nil, fontError(ErrMalformed, "variationStore.header", base+int64(offset), "incomplete store header")
	}
	storeData := data[offset:]
	storeBase := base + int64(offset)
	if u16(storeData, 0) != 1 {
		return nil, fontError(ErrUnsupported, "variationStore.format", storeBase, "unsupported item variation store format")
	}
	count := uint64(u16(storeData, 6))
	if count > uint64((len(storeData)-8)/4) {
		return nil, fontError(ErrMalformed, "variationStore.offsets", storeBase+6, "data offset array exceeds source")
	}
	regionOffset := uint64(u32(storeData, 2))
	if regionOffset > uint64(len(storeData)) || uint64(len(storeData))-regionOffset < 4 {
		return nil, fontError(ErrMalformed, "variationStore.regions", storeBase+2, "region list offset exceeds source")
	}
	regionData := storeData[int(regionOffset):]
	storedAxes, regionCount := int(u16(regionData, 0)), int(u16(regionData, 2))
	if regionCount&0x8000 != 0 {
		return nil, fontError(ErrMalformed, "variationStore.regionCount", storeBase+int64(regionOffset)+2,
			"region count has a reserved high bit")
	}
	if storedAxes != axisCount {
		return nil, fontError(ErrMalformed, "variationStore.axes", storeBase+int64(regionOffset),
			"store axes differ from fvar")
	}
	axisRecords := uint64(storedAxes) * uint64(regionCount)
	if axisRecords > uint64((len(regionData)-4)/6) {
		return nil, fontError(ErrMalformed, "variationStore.regions", storeBase+int64(regionOffset),
			"region axes exceed source")
	}
	if err := budget.reserve(int64(unsafe.Sizeof(itemVariationStore{})), "variationStore.memory", storeBase); err != nil {
		return nil, err
	}
	if err := budget.array(axisRecords, int64(unsafe.Sizeof(variationRegionAxis{})),
		"variationStore.regions", storeBase+int64(regionOffset)); err != nil {
		return nil, err
	}
	if err := budget.array(count, int64(unsafe.Sizeof(itemVariationData{})),
		"variationStore.data", storeBase); err != nil {
		return nil, err
	}
	result := &itemVariationStore{axisCount: storedAxes, regionCount: regionCount,
		regions: make([]variationRegionAxis, int(axisRecords)), data: make([]itemVariationData, int(count)),
		source: Span{Offset: storeBase, Length: int64(len(storeData))}}
	for index := range result.regions {
		position := 4 + index*6
		if err := budget.step("variationStore.regions", storeBase+int64(regionOffset)+int64(position)); err != nil {
			return nil, err
		}
		result.regions[index] = variationRegionAxis{start: i16(regionData, position),
			peak: i16(regionData, position+2), end: i16(regionData, position+4)}
	}
	for index := range result.data {
		if err := budget.step("variationStore.data", storeBase+8+int64(index)*4); err != nil {
			return nil, err
		}
		dataOffset := uint64(u32(storeData, 8+index*4))
		if dataOffset == 0 {
			result.data[index] = itemVariationData{source: Span{Offset: storeBase + 8 + int64(index)*4, Length: 4}}
			continue
		}
		if dataOffset > uint64(len(storeData)) || uint64(len(storeData))-dataOffset < 6 {
			return nil, fontError(ErrMalformed, "variationStore.data", storeBase+8+int64(index)*4,
				"item variation data offset exceeds source")
		}
		subtable := storeData[int(dataOffset):]
		subtableBase := storeBase + int64(dataOffset)
		items, packedWords, regions := u16(subtable, 0), u16(subtable, 2), u16(subtable, 4)
		words := packedWords & 0x7fff
		if words > regions || uint64(regions) > uint64((len(subtable)-6)/2) {
			return nil, fontError(ErrMalformed, "variationStore.indices", subtableBase,
				"word count or region-index array exceeds source")
		}
		longWords := packedWords&0x8000 != 0
		rowSize := int(words)*2 + int(regions-words)
		if longWords {
			rowSize = int(words)*4 + int(regions-words)*2
		}
		begin := 6 + int(regions)*2
		if rowSize > 0 && uint64(items) > uint64((len(subtable)-begin)/rowSize) {
			return nil, fontError(ErrMalformed, "variationStore.deltas", subtableBase+int64(begin),
				"delta rows exceed source")
		}
		if err := budget.array(uint64(regions), 2, "variationStore.indices", subtableBase); err != nil {
			return nil, err
		}
		row := itemVariationData{present: true, itemCount: items, wordCount: words, longWords: longWords,
			regionIDs: make([]uint16, int(regions)), rowSize: rowSize,
			deltaBytes: subtable[begin : begin+int(items)*rowSize],
			source:     Span{Offset: subtableBase, Length: int64(begin + int(items)*rowSize)}}
		for region := range row.regionIDs {
			if err := budget.step("variationStore.indices", subtableBase+int64(6+region*2)); err != nil {
				return nil, err
			}
			id := u16(subtable, 6+region*2)
			if int(id) >= regionCount {
				return nil, fontError(ErrMalformed, "variationStore.index", subtableBase+int64(6+region*2),
					"region index is outside the region list")
			}
			row.regionIDs[region] = id
		}
		result.data[index] = row
	}
	return result, budget.ctx.Err()
}
