package psdfont

import (
	"context"
	"unsafe"
)

func readFvarMetadata(ctx context.Context, face *Face, budget *fontMetadataBudget) (*fontVariationData, error) {
	span, present := face.tables["fvar"]
	if !present {
		return nil, nil
	}
	data, err := face.table("fvar", 16)
	if err != nil {
		return nil, err
	}
	if u16(data, 0) != 1 || u16(data, 2) != 0 || u16(data, 6) != 2 {
		return nil, fontError(ErrUnsupported, "fvar.header", span.Offset, "unsupported fvar version or format")
	}
	offset, axisCount, axisSize := uint64(u16(data, 4)), uint64(u16(data, 8)), uint64(u16(data, 10))
	instanceCount, instanceSize := uint64(u16(data, 12)), uint64(u16(data, 14))
	if axisSize != 20 || offset < 16 || offset > uint64(len(data)) || axisCount > (uint64(len(data))-offset)/axisSize {
		return nil, fontError(ErrMalformed, "fvar.axes", span.Offset, "axis records exceed fvar source")
	}
	instanceStart := offset + axisCount*axisSize
	if instanceSize != 4+axisCount*4 && instanceSize != 6+axisCount*4 ||
		instanceCount > (uint64(len(data))-instanceStart)/instanceSize {
		return nil, fontError(ErrMalformed, "fvar.instances", span.Offset, "named instances exceed fvar source")
	}
	if err := budget.reserve(int64(unsafe.Sizeof(fontVariationData{})), "fvar.memory", span.Offset); err != nil {
		return nil, err
	}
	if err := budget.array(axisCount, int64(unsafe.Sizeof(variationAxisRecord{}))+80,
		"fvar.axes", span.Offset); err != nil {
		return nil, err
	}
	result := &fontVariationData{fvarSource: span, axes: make([]variationAxisRecord, int(axisCount)),
		copyLimit: budget.limits.MaxBytes, copyWork: budget.limits.MaxCFFOperations}
	seen := make(map[uint32]bool, int(axisCount))
	for index := range result.axes {
		at := int(offset + uint64(index)*axisSize)
		if err := budget.step("fvar.axes", span.Offset+int64(at)); err != nil {
			return nil, err
		}
		tag := u32(data, at)
		minimum, defaultValue, maximum := int32(u32(data, at+4)), int32(u32(data, at+8)), int32(u32(data, at+12))
		if seen[tag] || minimum > defaultValue || defaultValue > maximum {
			return nil, fontError(ErrMalformed, "fvar.axes", span.Offset+int64(at), "duplicate axis or unordered range")
		}
		seen[tag] = true
		result.axes[index] = variationAxisRecord{minimum: minimum, defaultValue: defaultValue, maximum: maximum,
			axis: VariationAxis{Tag: string(data[at : at+4]), Minimum: float64(minimum) / 65536,
				Default: float64(defaultValue) / 65536, Maximum: float64(maximum) / 65536,
				Flags: u16(data, at+16), NameID: u16(data, at+18),
				Source: Span{Offset: span.Offset + int64(at), Length: int64(axisSize)}}}
	}
	if err := budget.array(instanceCount, int64(unsafe.Sizeof(NamedVariationInstance{})),
		"fvar.instances", span.Offset+int64(instanceStart)); err != nil {
		return nil, err
	}
	result.named = make([]NamedVariationInstance, int(instanceCount))
	for index := range result.named {
		at := int(instanceStart + uint64(index)*instanceSize)
		if err := budget.step("fvar.instances", span.Offset+int64(at)); err != nil {
			return nil, err
		}
		if err := budget.array(axisCount, 8, "fvar.coordinates", span.Offset+int64(at)); err != nil {
			return nil, err
		}
		row := NamedVariationInstance{SubfamilyNameID: u16(data, at), Flags: u16(data, at+2),
			Coordinates: make([]float64, int(axisCount)),
			Source:      Span{Offset: span.Offset + int64(at), Length: int64(instanceSize)}}
		for coordinate := range row.Coordinates {
			if err := budget.step("fvar.coordinates", span.Offset+int64(at+4+coordinate*4)); err != nil {
				return nil, err
			}
			row.Coordinates[coordinate] = float64(int32(u32(data, at+4+coordinate*4))) / 65536
		}
		if instanceSize == 6+axisCount*4 {
			row.PostScriptNameID, row.HasPostScriptNameID = u16(data, at+4+int(axisCount)*4), true
		}
		result.named[index] = row
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func readAvarMetadata(face *Face, variation *fontVariationData, budget *fontMetadataBudget) error {
	span, present := face.tables["avar"]
	if !present {
		return nil
	}
	if len(variation.axes) == 0 {
		variation.avarSource = span
		return nil
	}
	data, err := face.table("avar", 8)
	if err != nil {
		return err
	}
	major, storedAxes := u16(data, 0), int(u16(data, 6))
	if major != 1 && major != 2 || u16(data, 2) != 0 {
		return fontError(ErrUnsupported, "avar.version", span.Offset, "avar version is not yet supported")
	}
	if u16(data, 4) != 0 || storedAxes != len(variation.axes) && (major != 2 || storedAxes != 0) {
		return fontError(ErrMalformed, "avar.axes", span.Offset, "avar axes differ from fvar")
	}
	if err := budget.array(uint64(storedAxes), int64(unsafe.Sizeof([]axisVariationMap{})),
		"avar.memory", span.Offset); err != nil {
		return err
	}
	variation.avar = make([][]axisVariationMap, storedAxes)
	position := 8
	for axis := range variation.avar {
		if err := budget.step("avar.axis", span.Offset+int64(position)); err != nil {
			return err
		}
		if position > len(data)-2 {
			return fontError(ErrMalformed, "avar.maps", span.Offset+int64(position), "missing axis map count")
		}
		count := int(u16(data, position))
		position += 2
		if count > (len(data)-position)/4 {
			return fontError(ErrMalformed, "avar.maps", span.Offset+int64(position), "axis maps exceed source")
		}
		if err := budget.array(uint64(count), int64(unsafe.Sizeof(axisVariationMap{})),
			"avar.memory", span.Offset+int64(position)); err != nil {
			return err
		}
		maps := make([]axisVariationMap, count)
		for index := range maps {
			if err := budget.step("avar.maps", span.Offset+int64(position)); err != nil {
				return err
			}
			maps[index] = axisVariationMap{from: i16(data, position), to: i16(data, position+2)}
			position += 4
		}
		variation.avar[axis] = maps
	}
	variation.avarSource = span
	if major == 2 {
		return readAvarCrossAxis(data, span, position, variation, budget)
	}
	return nil
}
