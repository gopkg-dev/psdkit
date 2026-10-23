package psdfile

import (
	"context"
	"encoding/binary"
	"math"
	"reflect"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdfilter"
)

// EncodeSmartFilterParameters changes existing filter controls in one original SoLd/SoLE payload.
// It returns owned block data and never mutates the source or caller metadata.
// Filter count, order, IDs, blending mode and all unrelated fields, encodings, prefix and tail are retained.
// Existing Gaussian, Surface Blur, Smart Sharpen, brightness/contrast and hue/saturation controls are editable.
// Sharpen blur/legacy modes, hue colorization and the existing hue range count/order are retained.
// Per-filter enabled/opacity and whole-stack enabled are editable; ambiguous edited fields are rejected.
// This metadata operation does not refresh raster caches or document previews; saving requires a chosen render policy.
func (d *Document) EncodeSmartFilterParameters(
	ctx context.Context, block AdditionalBlock, desired psdfilter.Stack, options ...MetadataOptions,
) ([]byte, error) {
	ctx = decodeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if block.Key != "SoLd" && block.Key != "SoLE" || block.Data.Length < 12 {
		return nil, fileError(ErrMalformed, "smartFilters", "placement", block.Data.Offset,
			"provide one complete original modern placement block")
	}
	limits, err := d.metadataLimits(options)
	if err != nil {
		return nil, err
	}
	budget := min(limits.MaxMetadataBytes, limits.MaxDecodeBytes)
	if budget <= 0 || block.Data.Length > budget/8 {
		return nil, fileError(ErrLimit, "smartFilters", "encode", block.Data.Offset,
			"source placement and owned output exceed the combined operation budget")
	}
	prefix, err := d.ReadSpan(Span{Offset: block.Data.Offset, Length: 8})
	if err != nil {
		return nil, err
	}
	version := binary.BigEndian.Uint32(prefix[4:])
	if string(prefix[:4]) != "soLD" || version != 4 && version != 5 {
		return nil, fileError(ErrUnsupported, "smartFilters", "placement.version", block.Data.Offset,
			"unknown modern placement schema")
	}
	descriptorBudget := budget / 8
	descriptor, err := d.DecodeDescriptorContext(ctx,
		Span{Offset: block.Data.Offset + 8, Length: block.Data.Length - 8}, true,
		MetadataOptions{MaxBytes: descriptorBudget})
	if err != nil {
		return nil, err
	}
	if err := smartFilterEditShape(descriptor, "filterFX", "Objc"); err != nil {
		return nil, err
	}
	view, err := DecodeSmartFilters(ctx, SmartObjectRecord{Descriptor: descriptor},
		MetadataOptions{MaxBytes: budget / 16})
	if err != nil {
		return nil, err
	}
	if view == nil || len(desired.Filters) != len(view.Filters) {
		return nil, fileError(ErrUnsupported, "smartFilters", "filters", descriptor.Span.Offset,
			"parameter edits require an existing unchanged filter count")
	}
	if err := smartFilterEditShape(view.Descriptor, "enab", "bool", "filterFXList", "VlLs",
		"validAtPosition", "bool", "filterMaskEnable", "bool", "filterMaskLinked", "bool",
		"filterMaskExtendWithWhite", "bool"); err != nil {
		return nil, err
	}
	if err := setSmartFilterValue(view.Descriptor, "enab", desired.Enabled); err != nil {
		return nil, err
	}
	for index, requested := range desired.Filters {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		original := view.Filters[index]
		if err := smartFilterEditShape(original.Descriptor,
			"filterID", "long", "enab", "bool", "Fltr", "Objc", "blendOptions", "Objc"); err != nil {
			return nil, err
		}
		if requested.Kind != original.Operation.Kind || requested.BlendMode != original.Operation.BlendMode {
			return nil, fileError(ErrUnsupported, "smartFilters", "filter.kind", original.Descriptor.Span.Offset,
				"parameter edits preserve filter families and blending modes")
		}
		if math.IsNaN(requested.Opacity) || math.IsInf(requested.Opacity, 0) ||
			requested.Opacity < 0 || requested.Opacity > 1 || math.IsNaN(requested.Radius) ||
			math.IsInf(requested.Radius, 0) || requested.Radius < 0 || requested.Radius > 1000 {
			return nil, fileError(ErrMalformed, "smartFilters", "filter.parameters", original.Descriptor.Span.Offset,
				"requested radius or opacity is invalid")
		}
		if err := setSmartFilterValue(original.Descriptor, "enab", requested.Enabled); err != nil {
			return nil, err
		}
		blending, _ := smartDescriptorValue(original.Descriptor, "blendOptions").(*Descriptor)
		if err := smartFilterEditShape(blending, "Opct", "UntF", "Md  ", "enum"); err != nil {
			return nil, err
		}
		if requested.Opacity != original.Operation.Opacity {
			if err := setSmartFilterUnit(blending, "Opct", "#Prc", requested.Opacity*100); err != nil {
				return nil, err
			}
			requested.Opacity = requested.Opacity * 100 / 100
		}
		if err := patchSmartFilterControls(original, requested); err != nil {
			return nil, err
		}
		// Decode the patched fields with the source schema before accepting any edit.
		patched, err := smartFilterItem(original.Index, original.Descriptor)
		if err != nil {
			return nil, err
		}
		if requested.Kind == psdfilter.SmartSharpen {
			if requested.Amount != original.Operation.Amount {
				requested.Amount = requested.Amount * 100 / 100
			}
			if requested.NoiseReduction != original.Operation.NoiseReduction {
				requested.NoiseReduction = requested.NoiseReduction * 100 / 100
			}
		}
		if !reflect.DeepEqual(smartFilterComparableControls(patched.Operation), smartFilterComparableControls(requested)) {
			return nil, fileError(ErrUnsupported, "smartFilters", "filter.controls", original.Descriptor.Span.Offset,
				"requested controls change fixed semantics or require absent source fields")
		}
	}
	encodeLimits := limits
	encodeLimits.MaxMetadataBytes = budget / 8
	encoded, err := d.EncodeDescriptor(ctx, descriptor, true,
		DescriptorEncodeOptions{Limits: encodeLimits, MaxEncodedBytes: budget / 8})
	if err != nil {
		return nil, err
	}
	tailSpan := Span{Offset: descriptor.Span.Offset + descriptor.Span.Length,
		Length: block.Data.Offset + block.Data.Length - descriptor.Span.Offset - descriptor.Span.Length}
	length := int64(len(encoded)) + 8 + tailSpan.Length
	if tailSpan.Length < 0 || length > budget/4 || length > int64(^uint(0)>>1) {
		return nil, fileError(ErrLimit, "smartFilters", "encode.output", block.Data.Offset,
			"encoded placement exceeds the combined operation budget")
	}
	tail, err := d.ReadSpan(tailSpan)
	if err != nil {
		return nil, err
	}
	result := make([]byte, int(length))
	copy(result, prefix)
	copy(result[8:], encoded)
	copy(result[8+len(encoded):], tail)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// smartFilterComparableControls treats absent and empty adjustment slices identically after JSON transport.
// Populated slices, optional controls and retained metadata still require exact equality.
func smartFilterComparableControls(filter psdfilter.Filter) psdfilter.Filter {
	if filter.Adjustment == nil {
		return filter
	}
	adjustment := *filter.Adjustment
	if len(adjustment.Levels) == 0 {
		adjustment.Levels = nil
	}
	if len(adjustment.Curves) == 0 {
		adjustment.Curves = nil
	}
	if len(adjustment.LegacyCurves) == 0 {
		adjustment.LegacyCurves = nil
	}
	if len(adjustment.Raw) == 0 {
		adjustment.Raw = nil
	}
	if len(adjustment.Unsupported) == 0 {
		adjustment.Unsupported = nil
	}
	if adjustment.HueSaturation != nil && len(adjustment.HueSaturation.Unknown) == 0 {
		hue := *adjustment.HueSaturation
		hue.Unknown = nil
		adjustment.HueSaturation = &hue
	}
	filter.Adjustment = &adjustment
	return filter
}

func setSmartFilterValue(descriptor *Descriptor, key string, value any) error {
	kind := ""
	switch value.(type) {
	case bool:
		kind = "bool"
	case int32:
		kind = "long"
	case DescriptorUnit:
		kind = "UntF"
	}
	if err := smartFilterEditShape(descriptor, key, kind); err != nil {
		return err
	}
	if descriptor != nil {
		for index := len(descriptor.Entries) - 1; index >= 0; index-- {
			if descriptor.Entries[index].Key == key {
				descriptor.Entries[index].Value = value
				return nil
			}
		}
	}
	return fileError(ErrUnsupported, "smartFilters", key, 0, "an editable source field is absent")
}

// smartFilterEditShape rejects duplicate or mistyped known fields without discarding unknown entries.
func smartFilterEditShape(descriptor *Descriptor, fields ...string) error {
	if descriptor == nil {
		return nil
	}
	for index := 0; index < len(fields); index += 2 {
		count := 0
		for _, entry := range descriptor.Entries {
			if entry.Key != fields[index] {
				continue
			}
			count++
			if count > 1 || entry.Type != fields[index+1] {
				return fileError(ErrUnsupported, "smartFilters", "filter."+entry.Key, descriptor.Span.Offset,
					"edited source fields must have one unambiguous known encoding")
			}
		}
	}
	return nil
}

func patchSmartFilterControls(original SmartFilterRecord, requested psdfilter.Filter) error {
	parameters := original.Parameters
	previous := original.Operation
	switch requested.Kind {
	case psdfilter.GaussianBlur, psdfilter.SurfaceBlur, psdfilter.SmartSharpen:
		if err := smartFilterEditShape(parameters, "Rds ", "UntF"); err != nil {
			return err
		}
		if requested.Radius != previous.Radius {
			if err := setSmartFilterUnit(parameters, "Rds ", "#Pxl", requested.Radius); err != nil {
				return err
			}
		}
	}
	switch requested.Kind {
	case psdfilter.SurfaceBlur:
		if err := smartFilterEditShape(parameters, "Thsh", "long"); err != nil {
			return err
		}
		if !smartFinite(requested.Threshold) || requested.Threshold < 0 || requested.Threshold > 255 ||
			math.Trunc(requested.Threshold) != requested.Threshold {
			return smartFilterParameterError(parameters, "Thsh", "threshold must be an integer from zero to 255")
		}
		if requested.Threshold != previous.Threshold {
			return setSmartFilterValue(parameters, "Thsh", int32(requested.Threshold))
		}
	case psdfilter.SmartSharpen:
		if err := smartFilterEditShape(parameters, "Amnt", "UntF", "noiseReduction", "UntF",
			"blur", "enum", "useLegacy", "bool", "presetKind", "enum"); err != nil {
			return err
		}
		if requested.Amount != previous.Amount {
			if err := setSmartFilterUnit(parameters, "Amnt", "#Prc", requested.Amount*100); err != nil {
				return err
			}
		}
		if requested.NoiseReduction != previous.NoiseReduction {
			return setSmartFilterUnit(parameters, "noiseReduction", "#Prc", requested.NoiseReduction*100)
		}
	case psdfilter.BrightnessContrast:
		return patchSmartFilterBrightness(original, requested)
	case psdfilter.HueSaturation:
		return patchSmartFilterHue(original, requested)
	}
	return nil
}

func patchSmartFilterBrightness(original SmartFilterRecord, requested psdfilter.Filter) error {
	if requested.Adjustment == nil || requested.Adjustment.Kind != psdadjust.BrightnessContrast ||
		requested.Adjustment.BrightnessContrast == nil {
		return smartFilterParameterError(original.Parameters, "adjustment", "brightness/contrast controls are required")
	}
	parameters := original.Parameters
	if err := smartFilterEditShape(parameters, "Brgh", "long", "Cntr", "long", "useLegacy", "bool"); err != nil {
		return err
	}
	previous := original.Operation.Adjustment.BrightnessContrast
	controls := requested.Adjustment.BrightnessContrast
	for _, field := range []struct {
		key      string
		oldValue int16
		value    int16
	}{{"Brgh", previous.Brightness, controls.Brightness}, {"Cntr", previous.Contrast, controls.Contrast}} {
		if field.value != field.oldValue {
			if err := setSmartFilterValue(parameters, field.key, int32(field.value)); err != nil {
				return err
			}
		}
	}
	if controls.Legacy != previous.Legacy {
		return setSmartFilterValue(parameters, "useLegacy", controls.Legacy)
	}
	return nil
}

func patchSmartFilterHue(original SmartFilterRecord, requested psdfilter.Filter) error {
	if requested.Adjustment == nil || requested.Adjustment.Kind != psdadjust.HueSaturation ||
		requested.Adjustment.HueSaturation == nil {
		return smartFilterParameterError(original.Parameters, "adjustment", "hue/saturation controls are required")
	}
	parameters := original.Parameters
	if err := smartFilterEditShape(parameters, "Clrz", "bool", "Adjs", "VlLs", "presetKind", "enum"); err != nil {
		return err
	}
	values, _ := smartDescriptorValue(parameters, "Adjs").([]DescriptorValue)
	controls := requested.Adjustment.HueSaturation
	previous := original.Operation.Adjustment.HueSaturation
	var present [7]bool
	for _, value := range values {
		local, _ := value.Value.(*Descriptor)
		if local == nil || local.ClassID != "Hst2" || value.Type != "Objc" {
			return fileError(ErrUnsupported, "smartFilters", "filter.Adjs", parameters.Span.Offset,
				"hue parameter edits require recognized existing range descriptors")
		}
		if err := smartFilterEditShape(local, "LclR", "long", "H   ", "long", "Strt", "long", "Lght", "long",
			"BgnR", "long", "BgnS", "long", "EndS", "long", "EndR", "long"); err != nil {
			return err
		}
		index, err := smartFilterInteger(local, "LclR", 0, 6)
		if err != nil {
			return err
		}
		if present[index] {
			return fileError(ErrUnsupported, "smartFilters", "filter.LclR", local.Span.Offset,
				"duplicate hue range identities are ambiguous")
		}
		present[index] = true
		desired, retained := controls.Master, previous.Master
		if index > 0 {
			desired, retained = controls.Ranges[index-1].Values, previous.Ranges[index-1].Values
			for position, key := range []string{"BgnR", "BgnS", "EndS", "EndR"} {
				bound := controls.Ranges[index-1].Bounds[position]
				if bound != previous.Ranges[index-1].Bounds[position] {
					if err := setSmartFilterValue(local, key, int32(bound)); err != nil {
						return err
					}
				}
			}
		}
		for _, field := range []struct {
			key      string
			oldValue int16
			value    int16
		}{{"H   ", retained.Hue, desired.Hue}, {"Strt", retained.Saturation, desired.Saturation},
			{"Lght", retained.Lightness, desired.Lightness}} {
			if field.value != field.oldValue {
				if err := setSmartFilterValue(local, field.key, int32(field.value)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func setSmartFilterUnit(descriptor *Descriptor, key, unit string, value float64) error {
	field, ok := smartDescriptorValue(descriptor, key).(DescriptorUnit)
	if !ok || field.Unit != unit || field.Width != 64 {
		return fileError(ErrUnsupported, "smartFilters", key, 0, "an editable scalar unit is absent or unrecognized")
	}
	field.Number = value
	return setSmartFilterValue(descriptor, key, field)
}
