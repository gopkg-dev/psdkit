package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"math"
	"reflect"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdgradient"
)

// SetGradientFill changes one existing classic RGB GdFl layer's live parameters and saved descriptor.
// Original descriptor entry order, encodings, known stop metadata and unrelated blocks are retained.
// Unsupported source semantics, duplicate fill families and failed peak-budget checks leave the editor unchanged.
func (e *Editor) SetGradientFill(ctx context.Context, id LayerID, fill psdgradient.Fill) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.checkMutableLocked(); err != nil {
		return err
	}
	if err := e.rgbCompositeSupportedLocked(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := psdgradient.Validate(fill); err != nil {
		return gradientEditError(err)
	}
	fill = normalizedGradient(fill)
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return err
	}
	if node.group || node.sourceIndex < 0 || node.sourceIndex >= len(e.source.Layers) {
		return editError(ErrUnsupported, "layers.gradient", "target is not an existing gradient fill layer")
	}
	var block psdfile.AdditionalBlock
	found := false
	for _, candidate := range e.source.Layers[node.sourceIndex].AdditionalInfo {
		if candidate.Key == "SoCo" || candidate.Key == "PtFl" || adjustmentFamilyKey(candidate.Key) ||
			unsupportedAdjustmentKey(candidate.Key) {
			return editError(ErrUnsupported, "layers.gradient", "multiple live layer families are ambiguous")
		}
		if candidate.Key == "GdFl" {
			if found {
				return editError(ErrUnsupported, "layers.gradient", "duplicate gradient fill blocks are ambiguous")
			}
			block, found = candidate, true
		}
	}
	if !found {
		return editError(ErrUnsupported, "layers.gradient", "layer has no original gradient descriptor")
	}
	if node.gradient != nil && reflect.DeepEqual(fill, *node.gradient) {
		return nil
	}
	limits := e.source.Limits()
	peakRetained := e.ownedBytesLocked(nil)
	available := limits.MaxDecodeBytes - peakRetained
	sourceBudget := min(limits.MaxMetadataBytes, available/4)
	if sourceBudget <= 0 || block.Data.Length > sourceBudget/8 || int64(len(fill.Name)) > available/32 {
		return editError(psdfile.ErrLimit, "layers.gradient", "source descriptor exceeds the peak working budget")
	}
	original, err := e.source.DecodeGradientFill(ctx, block, psdfile.MetadataOptions{MaxBytes: sourceBudget})
	if err != nil {
		return gradientEditError(err)
	}
	if node.gradient == nil && reflect.DeepEqual(fill, *original.Fill) {
		return nil
	}
	stopCount := int64(len(fill.ColorStops)) + int64(len(fill.OpacityStops))
	patchWorking := stopCount*2048 + int64(len(fill.Name))*4 + 4096
	remaining := available - sourceBudget - patchWorking
	if remaining <= 0 {
		return editError(psdfile.ErrLimit, "layers.gradient", "stop descriptors exceed the peak working budget")
	}
	canvas := image.Rect(0, 0, int(e.source.Header.Width), int(e.source.Header.Height))
	crop := image.Rect(0, 0, 1, 1)
	if _, err := psdgradient.Render(ctx, fill, canvas, psdgradient.Options{
		Crop: &crop, AlignBounds: &node.bounds, MaxPixels: 1, MaxWorkingBytes: remaining,
	}); err != nil {
		return gradientEditError(err)
	}
	if err := patchGradientDescriptor(ctx, original.Descriptor, fill); err != nil {
		return err
	}
	encodeScratch, outputLimit := min(limits.MaxMetadataBytes, remaining/4), min(limits.MaxMetadataBytes, remaining/8)
	if encodeScratch <= 0 || outputLimit <= 0 {
		return editError(psdfile.ErrLimit, "layers.gradient", "descriptor encoding exceeds the peak working budget")
	}
	encodeLimits := limits
	encodeLimits.MaxMetadataBytes = encodeScratch
	encoded, err := e.source.EncodeDescriptor(ctx, original.Descriptor, true,
		psdfile.DescriptorEncodeOptions{Limits: encodeLimits, MaxEncodedBytes: outputLimit})
	if err != nil {
		return gradientEditError(err)
	}
	if original.Tail.Length != 0 {
		tail, err := e.source.ReadSpan(original.Tail)
		if err != nil {
			return err
		}
		encoded = append(encoded, tail...)
	}
	previous := node.additional[block.Span.Offset]
	equal := bytes.Equal(previous, encoded)
	if previous == nil {
		equal, err = equalSourceVector(ctx, e.source, block.Data, encoded)
		if err != nil {
			return err
		}
	}
	if equal {
		return ctx.Err()
	}
	backing := int64(cap(encoded))
	bridgeBudget := min(limits.MaxMetadataBytes,
		available-sourceBudget-patchWorking-encodeScratch-2*backing-4096)
	if bridgeBudget <= 0 {
		return editError(psdfile.ErrLimit, "layers.gradient", "owned semantic copy exceeds the peak working budget")
	}
	owned, err := decodeGradientEditPayload(ctx, encoded, limits, bridgeBudget)
	if err != nil {
		return gradientEditError(err)
	}
	cost := ownedGradientBytes(owned) + backing - int64(len(encoded)) + 256
	finalRetained := peakRetained - node.semanticCosts["gradient"] - int64(len(previous))
	if finalRetained < 0 || cost+int64(len(encoded)) > limits.MaxDecodeBytes-finalRetained {
		return editError(psdfile.ErrLimit, "layers.gradient", "owned gradient overlay exceeds the retained budget")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if node.additional == nil {
		node.additional = make(map[int64][]byte)
	}
	node.additional[block.Span.Offset] = encoded
	node.gradient = owned
	node.setSemanticCost("gradient", cost)
	e.markChangedLocked(false)
	return nil
}

func normalizedGradient(fill psdgradient.Fill) psdgradient.Fill {
	if fill.Interpolation == "" {
		fill.Interpolation = "classic"
	}
	if fill.Form == "" {
		fill.Form = "custom-stops"
	}
	return fill
}

func patchGradientDescriptor(ctx context.Context, descriptor *psdfile.Descriptor, fill psdgradient.Fill) error {
	gradient := gradientObject(descriptor, "Grad")
	if gradient == nil {
		return editError(ErrUnsupported, "layers.gradient", "source gradient object is absent")
	}
	gradientEntry(gradient, "Nm  ", "TEXT", fill.Name, "")
	gradientEntry(gradient, "Intr", "doub", fill.Smoothness, float64(4096))
	style := "Lnr "
	if fill.Kind == psdgradient.Radial {
		style = "Rdl "
	}
	gradientEntry(descriptor, "Type", "enum", psdfile.DescriptorEnum{TypeID: "GrdT", ValueID: style}, nil)
	gradientUnit(descriptor, "Angl", "#Ang", fill.Angle, 0)
	gradientUnit(descriptor, "Scl ", "#Prc", fill.Scale, 100)
	gradientEntry(descriptor, "Rvrs", "bool", fill.Reverse, false)
	gradientEntry(descriptor, "Algn", "bool", fill.Align, true)
	gradientEntry(descriptor, "Dthr", "bool", fill.Dither, false)
	if offset := gradientObject(descriptor, "Ofst"); offset != nil {
		gradientUnit(offset, "Hrzn", "#Prc", fill.Offset.X, 0)
		gradientUnit(offset, "Vrtc", "#Prc", fill.Offset.Y, 0)
	} else if fill.Offset.X != 0 || fill.Offset.Y != 0 {
		offset := &psdfile.Descriptor{ClassID: "Pnt "}
		gradientUnit(offset, "Hrzn", "#Prc", fill.Offset.X, math.NaN())
		gradientUnit(offset, "Vrtc", "#Prc", fill.Offset.Y, math.NaN())
		gradientEntry(descriptor, "Ofst", "Objc", offset, nil)
	}
	if err := patchGradientStops(ctx, gradient, fill); err != nil {
		return err
	}
	return ctx.Err()
}

func patchGradientStops(ctx context.Context, gradient *psdfile.Descriptor, fill psdgradient.Fill) error {
	for _, colors := range []bool{true, false} {
		key, classID, count := "Trns", "TrnS", len(fill.OpacityStops)
		if colors {
			key, classID, count = "Clrs", "Clrt", len(fill.ColorStops)
		}
		entry := gradientFind(gradient, key)
		var original []psdfile.DescriptorValue
		if entry != nil {
			original, _ = entry.Value.([]psdfile.DescriptorValue)
		}
		values := make([]psdfile.DescriptorValue, count)
		for index := range count {
			if err := ctx.Err(); err != nil {
				return err
			}
			stop := &psdfile.Descriptor{ClassID: classID}
			item := psdfile.DescriptorValue{Type: "Objc", Value: stop}
			if index < len(original) {
				item = original[index]
				stop, _ = item.Value.(*psdfile.Descriptor)
				if stop == nil {
					return editError(ErrUnsupported, "layers.gradient.stops", "source stop representation is not editable")
				}
			}
			if colors {
				value := fill.ColorStops[index]
				rgb := gradientObject(stop, "Clr ")
				if rgb == nil {
					rgb = &psdfile.Descriptor{ClassID: "RGBC"}
					gradientEntry(stop, "Clr ", "Objc", rgb, nil)
				}
				for component, componentKey := range []string{"Rd  ", "Grn ", "Bl  "} {
					gradientEntry(rgb, componentKey, "doub", value.RGB[component]*255, nil)
				}
				gradientEntry(stop, "Type", "enum", psdfile.DescriptorEnum{TypeID: "Clry", ValueID: "UsrS"}, nil)
				gradientEntry(stop, "Lctn", "long", int32(value.Location), nil)
				gradientEntry(stop, "Mdpn", "long", int32(value.Midpoint), nil)
			} else {
				value := fill.OpacityStops[index]
				gradientUnit(stop, "Opct", "#Prc", value.Opacity*100, math.NaN())
				gradientEntry(stop, "Lctn", "long", int32(value.Location), nil)
				gradientEntry(stop, "Mdpn", "long", int32(value.Midpoint), nil)
			}
			values[index] = item
		}
		if entry != nil {
			entry.Value = values
		} else if count != 0 {
			gradientEntry(gradient, key, "VlLs", values, nil)
		}
	}
	return nil
}

func gradientFind(descriptor *psdfile.Descriptor, key string) *psdfile.DescriptorEntry {
	for index := range descriptor.Entries {
		if descriptor.Entries[index].Key == key {
			return &descriptor.Entries[index]
		}
	}
	return nil
}

func gradientObject(descriptor *psdfile.Descriptor, key string) *psdfile.Descriptor {
	entry := gradientFind(descriptor, key)
	if entry == nil {
		return nil
	}
	value, _ := entry.Value.(*psdfile.Descriptor)
	return value
}

func gradientEntry(descriptor *psdfile.Descriptor, key, kind string, value, defaultValue any) {
	if entry := gradientFind(descriptor, key); entry != nil {
		if previous, valid := entry.Value.(psdfile.DescriptorEnum); valid {
			next := value.(psdfile.DescriptorEnum)
			previous.TypeID, previous.ValueID = next.TypeID, next.ValueID
			value = previous
		}
		entry.Value = value
		return
	}
	if reflect.DeepEqual(value, defaultValue) {
		return
	}
	descriptor.Entries = append(descriptor.Entries, psdfile.DescriptorEntry{Key: key, Type: kind, Value: value})
}

func gradientUnit(descriptor *psdfile.Descriptor, key, unit string, number, defaultValue float64) {
	if entry := gradientFind(descriptor, key); entry != nil {
		value := entry.Value.(psdfile.DescriptorUnit)
		if value.Width == 32 {
			value.Number = float32(number)
		} else {
			value.Number = number
		}
		entry.Value = value
		return
	}
	if number == defaultValue {
		return
	}
	descriptor.Entries = append(descriptor.Entries, psdfile.DescriptorEntry{Key: key, Type: "UntF",
		Value: psdfile.DescriptorUnit{Unit: unit, Width: 64, Number: number}})
}

func decodeGradientEditPayload(
	ctx context.Context, payload []byte, limits psdfile.Limits, budget int64,
) (*psdgradient.Fill, error) {
	padding := (4 - len(payload)%4) % 4
	length := int64(len(payload)) + int64(padding) + 63
	if length > budget/4 || length > math.MaxUint32 || uint64(length) > uint64(^uint(0)>>1) {
		return nil, editError(psdfile.ErrLimit, "layers.gradient", "encoded semantic envelope exceeds the peak budget")
	}
	data := make([]byte, int(length))
	copy(data, "8BPS")
	binary.BigEndian.PutUint16(data[4:], 1)
	binary.BigEndian.PutUint16(data[12:], 3)
	binary.BigEndian.PutUint32(data[14:], 1)
	binary.BigEndian.PutUint32(data[18:], 1)
	binary.BigEndian.PutUint16(data[22:], 8)
	binary.BigEndian.PutUint16(data[24:], 3)
	binary.BigEndian.PutUint32(data[34:], uint32(20+len(payload)+padding))
	copy(data[46:], "8BIMGdFl")
	binary.BigEndian.PutUint32(data[54:], uint32(len(payload)))
	copy(data[58:], payload)
	limits.MaxMetadataBytes, limits.MaxDecodeBytes = min(limits.MaxMetadataBytes, budget), budget
	source, err := psdfile.Read(gradientEditReader{ctx: ctx, reader: bytes.NewReader(data)}, length,
		psdfile.LoadOptions{Limits: limits})
	if err != nil {
		return nil, err
	}
	defer func() { _ = source.Close() }()
	available := budget - length - source.IndexMetadataBytes()
	if available <= 0 || len(source.AdditionalInfo) != 1 {
		return nil, editError(psdfile.ErrLimit, "layers.gradient", "semantic view exceeds the remaining budget")
	}
	gradient, err := source.DecodeGradientFill(ctx, source.AdditionalInfo[0], psdfile.MetadataOptions{MaxBytes: available})
	if err != nil {
		return nil, err
	}
	return gradient.Fill, nil
}

type gradientEditReader struct {
	ctx    context.Context
	reader io.ReaderAt
}

func (r gradientEditReader) ReadAt(data []byte, offset int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.ReadAt(data, offset)
}

func ownedGradientBytes(fill *psdgradient.Fill) int64 {
	return int64(unsafe.Sizeof(*fill)) + int64(len(fill.Name))*4 + int64(len(fill.Form)+len(fill.Interpolation)) +
		int64(cap(fill.ColorStops))*int64(unsafe.Sizeof(psdgradient.ColorStop{})) +
		int64(cap(fill.OpacityStops))*int64(unsafe.Sizeof(psdgradient.OpacityStop{}))
}

func gradientEditError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	kind := ErrInvalidEdit
	if errors.Is(err, psdgradient.ErrUnsupported) || errors.Is(err, psdfile.ErrUnsupported) {
		kind = ErrUnsupported
	} else if errors.Is(err, psdgradient.ErrLimit) || errors.Is(err, psdfile.ErrLimit) {
		kind = psdfile.ErrLimit
	}
	return &psdfile.Error{Kind: kind, Feature: "edit", Path: "layers.gradient", Detail: err.Error(), Cause: err}
}
