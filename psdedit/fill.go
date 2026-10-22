package psdedit

import (
	"context"
	"maps"
	"math"

	"github.com/gopkg-dev/psdkit/psdfile"
)

// SetSolidFill changes an existing RGB solid-color layer's live fill and saved descriptor.
// RGB components are encoded values between zero and one; opacity remains a separate layer property.
// Vector geometry, original raster caches and unrelated descriptor entries are retained.
func (e *Editor) SetSolidFill(ctx context.Context, id LayerID, rgb [3]float64) error {
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
	for _, component := range rgb {
		if math.IsNaN(component) || math.IsInf(component, 0) || component < 0 || component > 1 {
			return editError(ErrInvalidEdit, "layers.fillColor", "encoded RGB components must be finite and between zero and one")
		}
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return err
	}
	if node.sourceIndex < 0 || node.group || node.sourceIndex >= len(e.source.Layers) {
		return editError(ErrUnsupported, "layers.fillColor", "layer is not an existing solid-color source")
	}
	if node.fillColor != nil && *node.fillColor == rgb {
		return nil
	}
	limits := e.source.Limits()
	retained := e.ownedBytesLocked(nil)
	updates := make(map[int64][]byte)
	total, found, changed := int64(0), false, node.fillColor != nil
	for _, block := range e.source.Layers[node.sourceIndex].AdditionalInfo {
		if block.Key != "SoCo" {
			continue
		}
		found = true
		if block.Data.Length > limits.MaxMetadataBytes || block.Data.Length > (limits.MaxDecodeBytes-retained-total)/32 {
			return editError(psdfile.ErrLimit, "layers.fillColor", "fill descriptor exceeds the working budget")
		}
		descriptor, err := e.source.DecodeDescriptorContext(ctx, block.Data, true)
		if err != nil {
			return err
		}
		colorFound := false
		for index := range descriptor.Entries {
			entry := &descriptor.Entries[index]
			if entry.Key != "Clr " {
				continue
			}
			colorDescriptor, ok := entry.Value.(*psdfile.Descriptor)
			if !ok || colorDescriptor == nil || colorDescriptor.ClassID != "RGBC" {
				return editError(ErrUnsupported, "layers.fillColor", "solid fill uses a non-RGB color descriptor")
			}
			seen := [3]bool{}
			for componentIndex := range colorDescriptor.Entries {
				component := &colorDescriptor.Entries[componentIndex]
				for channel, key := range []string{"Rd  ", "Grn ", "Bl  "} {
					if component.Key != key {
						continue
					}
					previous, valid := component.Value.(float64)
					if component.Type != "doub" || !valid {
						return editError(ErrUnsupported, "layers.fillColor", "RGB color component encoding is unsupported")
					}
					changed = changed || previous != rgb[channel]*255
					component.Value, seen[channel] = rgb[channel]*255, true
				}
			}
			if !seen[0] || !seen[1] || !seen[2] {
				return editError(ErrUnsupported, "layers.fillColor", "solid fill has incomplete RGB color parameters")
			}
			colorFound = true
		}
		if !colorFound {
			return editError(ErrUnsupported, "layers.fillColor", "solid fill has no RGB color descriptor")
		}
		encoded, err := e.source.EncodeDescriptor(ctx, descriptor, true, psdfile.DescriptorEncodeOptions{
			MaxEncodedBytes: min(limits.MaxMetadataBytes, limits.MaxDecodeBytes-retained-total),
		})
		if err != nil {
			return err
		}
		tailLength := block.Data.Length - descriptor.Span.Length
		if tailLength > 0 {
			tail, err := e.source.ReadSpan(psdfile.Span{Offset: descriptor.Span.Offset + descriptor.Span.Length,
				Length: tailLength})
			if err != nil {
				return err
			}
			encoded = append(encoded, tail...)
		}
		if int64(cap(encoded)) > limits.MaxDecodeBytes-retained-total {
			return editError(psdfile.ErrLimit, "layers.fillColor", "encoded fill overrides exceed the working budget")
		}
		total += int64(cap(encoded))
		updates[block.Span.Offset] = encoded
	}
	if !found {
		return editError(ErrUnsupported, "layers.fillColor", "layer has no solid-color source descriptor")
	}
	if !changed {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if 24 > limits.MaxDecodeBytes-retained-total {
		return editError(psdfile.ErrLimit, "layers.fillColor", "owned RGB color exceeds the working budget")
	}
	if node.additional == nil {
		node.additional = make(map[int64][]byte)
	}
	maps.Copy(node.additional, updates)
	owned := rgb
	node.fillColor = &owned
	ownedCost := int64(24)
	for _, data := range updates {
		ownedCost += int64(cap(data) - len(data))
	}
	node.setSemanticCost("fill", ownedCost)
	e.markChangedLocked(false)
	return nil
}
