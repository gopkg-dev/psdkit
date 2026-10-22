package psdfont

import "math"

// variationAxisScalar applies the overview's per-axis recovery and interpolation rules.
func variationAxisScalar(axis variationRegionAxis, coordinate int16) float64 {
	if axis.start > axis.peak || axis.peak > axis.end ||
		axis.start < 0 && axis.end > 0 && axis.peak != 0 || axis.peak == 0 {
		return 1
	}
	if coordinate < axis.start || coordinate > axis.end {
		return 0
	}
	if coordinate == axis.peak {
		return 1
	}
	if coordinate < axis.peak {
		return float64(int32(coordinate)-int32(axis.start)) / float64(int32(axis.peak)-int32(axis.start))
	}
	return float64(int32(axis.end)-int32(coordinate)) / float64(int32(axis.end)-int32(axis.peak))
}

// prepareScalars computes one owned region scalar without publishing a partial array.
func (s *itemVariationStore) prepareScalars(normalized []int16, budget *fontMetadataBudget) error {
	const path = "variationStore.scalars"
	offset := int64(0)
	if s != nil {
		offset = s.source.Offset
	}
	if err := budget.step(path, offset); err != nil {
		return err
	}
	if s == nil || s.axisCount != len(normalized) || s.axisCount < 0 || s.regionCount < 0 ||
		s.axisCount > 0 && s.regionCount > len(s.regions)/s.axisCount ||
		len(s.regions) != s.axisCount*s.regionCount {
		return fontError(ErrMalformed, path, offset, "region dimensions differ from normalized axes")
	}
	for _, coordinate := range normalized {
		if err := budget.step("variationStore.coordinates", offset); err != nil {
			return err
		}
		if coordinate < -16384 || coordinate > 16384 {
			return fontError(ErrMalformed, "variationStore.coordinates", offset, "normalized coordinate exceeds [-1, 1]")
		}
	}
	if err := budget.array(uint64(s.regionCount), 8, path, offset); err != nil {
		return err
	}
	result := make([]float64, s.regionCount)
	for region := range result {
		if err := budget.step(path, offset); err != nil {
			return err
		}
		scalar := 1.0
		for axis, coordinate := range normalized {
			if err := budget.step("variationStore.axisScalar", offset); err != nil {
				return err
			}
			scalar *= variationAxisScalar(s.regions[region*s.axisCount+axis], coordinate)
		}
		result[region] = scalar
	}
	if err := budget.ctx.Err(); err != nil {
		return err
	}
	s.scalars = result
	return nil
}

// delta reads one delta-set row and charges the caller's shared execution budget.
func (s *itemVariationStore) delta(
	outer, inner uint16,
	step func(path string, offset int64) error,
) (float64, error) {
	const path = "variationStore.delta"
	offset := int64(0)
	if s != nil {
		offset = s.source.Offset
	}
	if step == nil {
		return 0, fontError(ErrMalformed, path, offset, "delta evaluation requires a work callback")
	}
	if err := step(path, offset); err != nil {
		return 0, err
	}
	if outer == 0xffff && inner == 0xffff {
		return 0, step(path, offset)
	}
	if s == nil || int(outer) >= len(s.data) {
		return 0, fontError(ErrMalformed, path, offset, "delta outer index is outside the data array")
	}
	row := &s.data[int(outer)]
	if !row.present {
		return 0, step(path, row.source.Offset)
	}
	if inner >= row.itemCount {
		return 0, fontError(ErrMalformed, path, row.source.Offset, "delta inner index is outside the row array")
	}
	if len(s.scalars) != s.regionCount || int(row.wordCount) > len(row.regionIDs) {
		return 0, fontError(ErrMalformed, path, row.source.Offset, "region scalars or word count differ")
	}
	rowSize := len(row.regionIDs) + int(row.wordCount)
	if row.longWords {
		if rowSize > int(^uint(0)>>1)/2 {
			return 0, fontError(ErrMalformed, path, row.source.Offset, "delta row size overflows")
		}
		rowSize *= 2
	}
	if row.rowSize != rowSize || rowSize > 0 && int(row.itemCount) > len(row.deltaBytes)/rowSize {
		return 0, fontError(ErrMalformed, path, row.source.Offset, "delta row size exceeds source")
	}
	begin := int(inner) * rowSize
	rowBytes := row.deltaBytes[begin : begin+rowSize]
	rowBase := row.source.Offset + int64(6+2*len(row.regionIDs)) + int64(begin)
	result, position := 0.0, 0
	for column, regionID := range row.regionIDs {
		if err := step(path, rowBase+int64(position)); err != nil {
			return 0, err
		}
		if int(regionID) >= len(s.scalars) {
			return 0, fontError(ErrMalformed, path, rowBase+int64(position), "delta region index exceeds the scalar list")
		}
		scalar := s.scalars[int(regionID)]
		if math.IsNaN(scalar) || math.IsInf(scalar, 0) || scalar < 0 || scalar > 1 {
			return 0, fontError(ErrMalformed, path, rowBase+int64(position), "invalid precomputed region scalar")
		}
		width := 1
		if row.longWords {
			width = 2
		}
		if column < int(row.wordCount) {
			width *= 2
		}
		value := int32(int8(rowBytes[position]))
		switch width {
		case 2:
			value = int32(i16(rowBytes, position))
		case 4:
			value = int32(u32(rowBytes, position))
		}
		result += float64(value) * scalar
		position += width
	}
	if err := step(path, rowBase+int64(position)); err != nil {
		return 0, err
	}
	return result, nil
}
