package psdfont

import (
	"math"
	"unsafe"
)

// fixedNearest chooses a declared intermediate 16.16 profile: nearest with ties toward positive infinity.
// OpenType mandates 16.16 calculation but does not prescribe every intermediate division rounding rule.
func fixedNearest(numerator, denominator int64) int64 {
	quotient, remainder := numerator/denominator, numerator%denominator
	if remainder < 0 {
		quotient--
		remainder += denominator
	}
	if remainder*2 >= denominator {
		quotient++
	}
	return quotient
}

func normalizeDefaultAxis(value int32, axis variationAxisRecord) int32 {
	if value < axis.defaultValue {
		return int32(fixedNearest((int64(value)-int64(axis.defaultValue))*65536,
			int64(axis.defaultValue)-int64(axis.minimum)))
	}
	if value > axis.defaultValue {
		return int32(fixedNearest((int64(value)-int64(axis.defaultValue))*65536,
			int64(axis.maximum)-int64(axis.defaultValue)))
	}
	return 0
}

func normalizedAvar(value int32, maps []axisVariationMap, budget *fontMetadataBudget) (int32, error) {
	anchors := [3]bool{}
	previousFrom, previousTo := int32(-32769), int32(-32769)
	start, end := axisVariationMap{}, axisVariationMap{}
	segment := false
	for _, row := range maps {
		if err := budget.step("avar.normalize", 0); err != nil {
			return 0, err
		}
		if row.from < -16384 || row.from > 16384 || row.to < -16384 || row.to > 16384 ||
			int32(row.from) <= previousFrom || int32(row.to) < previousTo {
			continue
		}
		previousFrom, previousTo = int32(row.from), int32(row.to)
		for index, anchor := range [3]int16{-16384, 0, 16384} {
			if row.from == anchor && row.to == anchor {
				anchors[index] = true
			}
		}
		coordinate := int32(row.from) * 4
		if coordinate < value {
			start = row
		} else if !segment {
			end, segment = row, true
		}
	}
	if !anchors[0] || !anchors[1] || !anchors[2] || !segment {
		return value, nil
	}
	if value == int32(end.from)*4 {
		return int32(end.to) * 4, nil
	}
	ratio := fixedNearest((int64(value)-int64(start.from)*4)*65536,
		(int64(end.from)-int64(start.from))*4)
	return int32(int64(start.to)*4 + fixedNearest(ratio*(int64(end.to)-int64(start.to))*4, 65536)), nil
}

func selectVariationCoordinates(
	variation *fontVariationData, requests []VariationCoordinate, budget *fontMetadataBudget,
) error {
	if err := budget.array(uint64(len(variation.axes)), int64(unsafe.Sizeof(VariationInstanceCoordinate{}))+82,
		"variation.coordinates", variation.fvarSource.Offset); err != nil {
		return err
	}
	coordinates := make([]VariationInstanceCoordinate, len(variation.axes))
	normalized := make([]int16, len(variation.axes))
	indices := make(map[string]int, len(variation.axes))
	for index, axis := range variation.axes {
		if err := budget.step("variation.defaults", axis.axis.Source.Offset); err != nil {
			return err
		}
		indices[axis.axis.Tag] = index
		coordinates[index] = VariationInstanceCoordinate{Tag: axis.axis.Tag, Requested: axis.axis.Default}
	}
	if err := budget.array(uint64(len(requests)), 64, "variation.requests", 0); err != nil {
		return err
	}
	seen := make(map[string]bool, len(requests))
	for _, request := range requests {
		if err := budget.step("variation.requests", 0); err != nil {
			return err
		}
		index, exists := indices[request.Tag]
		if len(request.Tag) != 4 || !finite(request.Value) || !exists || seen[request.Tag] {
			return fontError(ErrMalformed, "variation.request", 0, "unknown, duplicate or invalid axis request")
		}
		seen[request.Tag] = true
		coordinates[index].Requested = request.Value
	}
	for index, axis := range variation.axes {
		if err := budget.step("variation.normalize", axis.axis.Source.Offset); err != nil {
			return err
		}
		row := &coordinates[index]
		clamped := max(axis.axis.Minimum, min(axis.axis.Maximum, row.Requested))
		row.ClampedFixed = int32(math.Floor(clamped*65536 + .5))
		row.DefaultNormalizedFixed = normalizeDefaultAxis(row.ClampedFixed, axis)
		value := max(int32(-65536), min(int32(65536), row.DefaultNormalizedFixed))
		if len(variation.avar) != 0 {
			var err error
			value, err = normalizedAvar(value, variation.avar[index], budget)
			if err != nil {
				return err
			}
		}
		value = max(int32(-65536), min(int32(65536), value))
		row.AvarMappedFixed, row.FinalNormalizedFixed = value, value
		row.Normalized = int16((value + 2) >> 2)
		normalized[index] = row.Normalized
	}
	if variation.avarStore != nil {
		if err := variation.avarStore.prepareScalars(normalized, budget); err != nil {
			return err
		}
		for axis := range coordinates {
			index := mappedDeltaIndex(variation.avarIndices, uint16(axis))
			delta, err := variation.avarStore.delta(index.outer, index.inner, budget.step)
			if err != nil {
				return err
			}
			// All scalars use the complete v1-mapped snapshot; results publish only after every axis.
			adjusted := float64(coordinates[axis].AvarMappedFixed) + math.Floor(delta*4+.5)
			coordinates[axis].FinalNormalizedFixed = int32(max(-65536., min(65536., adjusted)))
		}
		for axis := range coordinates {
			if err := budget.step("avar.finalCoordinates", variation.avarSource.Offset); err != nil {
				return err
			}
			coordinates[axis].Normalized = int16((coordinates[axis].FinalNormalizedFixed + 2) >> 2)
			normalized[axis] = coordinates[axis].Normalized
		}
	}
	if err := budget.ctx.Err(); err != nil {
		return err
	}
	variation.coordinates, variation.normalized = coordinates, normalized
	return nil
}
