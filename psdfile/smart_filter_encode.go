package psdfile

import (
	"context"
	"encoding/binary"
	"math"
	"reflect"

	"github.com/gopkg-dev/psdkit/psdfilter"
)

// EncodeSmartFilterParameters changes existing filter controls in one original SoLd/SoLE payload.
// It returns owned block data and never mutates the source or caller metadata.
// Filter count, order, IDs, blending mode and all unrelated fields, encodings, prefix and tail are retained.
// Existing Gaussian radius, per-filter enabled/opacity and whole-stack enabled are editable.
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
	view, err := DecodeSmartFilters(ctx, SmartObjectRecord{Descriptor: descriptor},
		MetadataOptions{MaxBytes: budget / 16})
	if err != nil {
		return nil, err
	}
	if view == nil || len(desired.Filters) != len(view.Filters) {
		return nil, fileError(ErrUnsupported, "smartFilters", "filters", descriptor.Span.Offset,
			"parameter edits require an existing unchanged filter count")
	}
	if err := setSmartFilterValue(view.Descriptor, "enab", desired.Enabled); err != nil {
		return nil, err
	}
	for index, requested := range desired.Filters {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		original := view.Filters[index]
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
		originalControls, requestedControls := original.Operation, requested
		originalControls.Enabled, requestedControls.Enabled = false, false
		originalControls.Opacity, requestedControls.Opacity = 0, 0
		if original.Operation.Kind == psdfilter.GaussianBlur {
			originalControls.Radius, requestedControls.Radius = 0, 0
		}
		if !reflect.DeepEqual(originalControls, requestedControls) {
			return nil, fileError(ErrUnsupported, "smartFilters", "filter.controls", original.Descriptor.Span.Offset,
				"only existing Gaussian radius and filter enabled/opacity controls are editable")
		}
		if err := setSmartFilterValue(original.Descriptor, "enab", requested.Enabled); err != nil {
			return nil, err
		}
		if requested.Opacity != original.Operation.Opacity {
			blending, _ := smartDescriptorValue(original.Descriptor, "blendOptions").(*Descriptor)
			if err := setSmartFilterUnit(blending, "Opct", "#Prc", requested.Opacity*100); err != nil {
				return nil, err
			}
		}
		if requested.Kind == psdfilter.GaussianBlur && requested.Radius != original.Operation.Radius {
			if err := setSmartFilterUnit(original.Parameters, "Rds ", "#Pxl", requested.Radius); err != nil {
				return nil, err
			}
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

func setSmartFilterValue(descriptor *Descriptor, key string, value any) error {
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

func setSmartFilterUnit(descriptor *Descriptor, key, unit string, value float64) error {
	field, ok := smartDescriptorValue(descriptor, key).(DescriptorUnit)
	if !ok || field.Unit != unit || field.Width != 64 {
		return fileError(ErrUnsupported, "smartFilters", key, 0, "an editable scalar unit is absent or unrecognized")
	}
	field.Number = value
	return setSmartFilterValue(descriptor, key, field)
}
