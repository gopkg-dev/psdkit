package psdfile

import (
	"context"
	"math"
	"strings"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdfilter"
)

// SmartFilterRecord retains one source-ordered smart-filter descriptor and its live controls.
// Descriptor and Parameters refer to the already owned placement metadata; their original spans borrow its source.
// Unknown filter IDs remain available as typed records with an empty Operation.Kind.
// ParametersKnown is false when recognized IDs include additional or unrecognized algorithm controls.
type SmartFilterRecord struct {
	Index                  int
	FilterID               uint32
	Name                   string
	Descriptor, Parameters *Descriptor
	Operation              psdfilter.Filter
	ParametersKnown        bool
}

// SmartFilterStack decodes filterFX without flattening or removing unrecognized descriptor fields.
// PlacementID matches FilterCacheRecord.ID, independently of the linked-content SourceID.
// Filters retain first-applied-to-last-applied source order and duplicate operations.
// MaskEnable/MaskLinked/MaskExtendWithWhite retain the parent's filter-mask behavior.
type SmartFilterStack struct {
	PlacementID                                 string
	Enabled, ValidAtPosition                    bool
	MaskEnable, MaskLinked, MaskExtendWithWhite bool
	Descriptor                                  *Descriptor
	Filters                                     []SmartFilterRecord
	MetadataBytes                               int64
}

// DecodeSmartFilters returns live smart-filter metadata from an already decoded placement.
// A placement without filterFX returns nil; no raster cache or original source payload is read.
// MaxBytes bounds only this additional typed view; the placement's owned descriptors are borrowed unchanged.
func DecodeSmartFilters(
	ctx context.Context, placement SmartObjectRecord, options ...MetadataOptions,
) (*SmartFilterStack, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(options) > 1 || len(options) == 1 && options[0].MaxBytes < 0 {
		return nil, fileError(ErrMalformed, "smartFilters", "options", 0, "invalid metadata options")
	}
	budget := int64(32 << 20)
	if len(options) == 1 && options[0].MaxBytes != 0 {
		budget = min(budget, options[0].MaxBytes)
	}
	value := smartDescriptorValue(placement.Descriptor, "filterFX")
	if value == nil {
		return nil, nil
	}
	descriptor, ok := value.(*Descriptor)
	if !ok || descriptor == nil {
		return nil, fileError(ErrMalformed, "smartFilters", "filterFX", placement.DescriptorData.Offset,
			"filter stack is not a descriptor")
	}
	metadataBytes := int64(unsafe.Sizeof(SmartFilterStack{}))
	if metadataBytes > budget {
		return nil, fileError(ErrLimit, "smartFilters", "metadata", descriptor.Span.Offset,
			"typed filter stack exceeds the allocation limit")
	}
	result := &SmartFilterStack{Descriptor: descriptor, MetadataBytes: metadataBytes}
	var err error
	if result.Enabled, err = smartFilterBoolean(descriptor, "enab", false); err != nil {
		return nil, err
	}
	if result.ValidAtPosition, err = smartFilterBoolean(descriptor, "validAtPosition", true); err != nil {
		return nil, err
	}
	if result.MaskEnable, err = smartFilterBoolean(descriptor, "filterMaskEnable", false); err != nil {
		return nil, err
	}
	if result.MaskLinked, err = smartFilterBoolean(descriptor, "filterMaskLinked", false); err != nil {
		return nil, err
	}
	if result.MaskExtendWithWhite, err = smartFilterBoolean(descriptor, "filterMaskExtendWithWhite", true); err != nil {
		return nil, err
	}
	if identifier, ok := smartDescriptorValue(placement.Descriptor, "placed").(string); ok {
		result.PlacementID = strings.TrimRight(identifier, "\x00")
	}
	listValue := smartDescriptorValue(descriptor, "filterFXList")
	if listValue == nil {
		if result.Enabled {
			return nil, fileError(ErrMalformed, "smartFilters", "filterFXList", descriptor.Span.Offset,
				"an enabled stack has no filter list")
		}
		if result.MetadataBytes > budget {
			return nil, fileError(ErrLimit, "smartFilters", "metadata", descriptor.Span.Offset,
				"typed filter metadata exceeds the allocation limit")
		}
		return result, nil
	}
	values, ok := listValue.([]DescriptorValue)
	if !ok {
		return nil, fileError(ErrMalformed, "smartFilters", "filterFXList", descriptor.Span.Offset,
			"filter list has an invalid descriptor type")
	}
	if len(values) > 4096 {
		return nil, fileError(ErrLimit, "smartFilters", "filterFXList", descriptor.Span.Offset,
			"filter count exceeds the typed metadata limit")
	}
	result.MetadataBytes += int64(len(values)) * int64(unsafe.Sizeof(SmartFilterRecord{}))
	if result.MetadataBytes > budget {
		return nil, fileError(ErrLimit, "smartFilters", "metadata", descriptor.Span.Offset,
			"typed filter records exceed the allocation limit")
	}
	result.Filters = make([]SmartFilterRecord, len(values))
	for index, value := range values {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		item, ok := value.Value.(*Descriptor)
		if !ok || item == nil {
			return nil, fileError(ErrMalformed, "smartFilters", "filterFXList.item", value.Span.Offset,
				"filter item is not a descriptor")
		}
		result.MetadataBytes += smartFilterAdjustmentCost(item)
		if result.MetadataBytes > budget {
			return nil, fileError(ErrLimit, "smartFilters", "metadata", item.Span.Offset,
				"typed color-filter controls exceed the allocation limit")
		}
		record, err := smartFilterItem(index, item)
		if err != nil {
			return nil, err
		}
		for _, key := range []string{"filterID", "enab", "Fltr", "blendOptions"} {
			count := 0
			for _, entry := range item.Entries {
				if entry.Key == key {
					count++
				}
			}
			if count > 1 {
				record.ParametersKnown = false
			}
		}
		if blending, ok := smartDescriptorValue(item, "blendOptions").(*Descriptor); ok {
			record.ParametersKnown = record.ParametersKnown &&
				smartFilterSchemaKnown(blending, "Opct", "UntF", "Md  ", "enum")
		}
		result.Filters[index] = record
	}
	return result, nil
}

func smartFilterItem(index int, item *Descriptor) (SmartFilterRecord, error) {
	record := SmartFilterRecord{Index: index, Descriptor: item,
		Operation: psdfilter.Filter{Opacity: 1, BlendMode: "norm"}}
	id, ok := smartDescriptorValue(item, "filterID").(int32)
	if !ok {
		return record, fileError(ErrMalformed, "smartFilters", "filterID", item.Span.Offset,
			"filter identifier is not an integer")
	}
	record.FilterID = uint32(id)
	if name, ok := smartDescriptorValue(item, "Nm  ").(string); ok {
		record.Name = strings.TrimRight(name, "\x00")
	}
	var err error
	if record.Operation.Enabled, err = smartFilterBoolean(item, "enab", false); err != nil {
		return record, err
	}
	if blending := smartDescriptorValue(item, "blendOptions"); blending != nil {
		options, ok := blending.(*Descriptor)
		if !ok || options == nil {
			return record, fileError(ErrMalformed, "smartFilters", "blendOptions", item.Span.Offset,
				"filter blending is not a descriptor")
		}
		if opacity := smartDescriptorValue(options, "Opct"); opacity != nil {
			unit, ok := opacity.(DescriptorUnit)
			value, numberOK := smartDescriptorNumber(opacity)
			if !ok || unit.Unit != "#Prc" || !numberOK || !smartFinite(value) || value < 0 || value > 100 {
				return record, fileError(ErrMalformed, "smartFilters", "blendOptions.opacity", options.Span.Offset,
					"filter opacity is not a valid percentage")
			}
			record.Operation.Opacity = value / 100
		}
		if mode := smartDescriptorValue(options, "Md  "); mode != nil {
			enumeration, ok := mode.(DescriptorEnum)
			if !ok || enumeration.TypeID != "BlnM" {
				return record, fileError(ErrMalformed, "smartFilters", "blendOptions.mode", options.Span.Offset,
					"filter blend mode has an invalid enumeration")
			}
			record.Operation.BlendMode = enumeration.ValueID
		}
	}
	parameters := smartDescriptorValue(item, "Fltr")
	if parameters != nil {
		var ok bool
		record.Parameters, ok = parameters.(*Descriptor)
		if !ok || record.Parameters == nil {
			return record, fileError(ErrMalformed, "smartFilters", "Fltr", item.Span.Offset,
				"filter parameters are not a descriptor")
		}
	}
	if record.FilterID != 0x47736e42 {
		return smartFilterExtended(record)
	}
	radius := smartDescriptorValue(record.Parameters, "Rds ")
	unit, ok := radius.(DescriptorUnit)
	value, numberOK := smartDescriptorNumber(radius)
	if !ok || unit.Unit != "#Pxl" || !numberOK || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return record, fileError(ErrMalformed, "smartFilters", "gaussian.radius", item.Span.Offset,
			"Gaussian radius is not a finite nonnegative pixel value")
	}
	record.Operation.Kind, record.Operation.Radius = psdfilter.GaussianBlur, value
	record.ParametersKnown = (record.Parameters.ClassID == "" || record.Parameters.ClassID == "GsnB") &&
		smartFilterSchemaKnown(record.Parameters, "Rds ", "UntF")
	return record, nil
}

func smartFilterAdjustmentCost(item *Descriptor) int64 {
	parameters, _ := smartDescriptorValue(item, "Fltr").(*Descriptor)
	if parameters == nil {
		return 0
	}
	id, _ := smartDescriptorValue(item, "filterID").(int32)
	if uint32(id) == 0x42726743 && parameters.ClassID == "BrgC" {
		return int64(unsafe.Sizeof(psdadjust.Adjustment{})) +
			int64(unsafe.Sizeof(psdadjust.BrightnessContrastParams{}))
	}
	if uint32(id) == 0x48537472 && parameters.ClassID == "HStr" {
		return int64(unsafe.Sizeof(psdadjust.Adjustment{})) +
			int64(unsafe.Sizeof(psdadjust.HueSaturationParams{}))
	}
	return 0
}

func smartFilterBoolean(descriptor *Descriptor, key string, fallback bool) (bool, error) {
	value := smartDescriptorValue(descriptor, key)
	if value == nil {
		return fallback, nil
	}
	boolean, ok := value.(bool)
	if !ok {
		return false, fileError(ErrMalformed, "smartFilters", key, descriptor.Span.Offset,
			"filter flag is not a boolean")
	}
	return boolean, nil
}
