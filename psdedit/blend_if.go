package psdedit

import (
	"context"
	"encoding/binary"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// BlendIf returns independently owned source and underlying-color Blend If sliders.
// Ranges start with composite gray, followed by the document's color channels; nil means full coverage.
func (editor *Editor) BlendIf(id LayerID) ([]psdmodel.BlendRange, error) {
	return editor.BlendIfContext(context.Background(), id)
}

// BlendIfContext adds cancellation and working-budget checks to BlendIf.
func (editor *Editor) BlendIfContext(ctx context.Context, id LayerID) ([]psdmodel.BlendRange, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	editor.mu.RLock()
	defer editor.mu.RUnlock()
	if err := editor.checkOpenLocked(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	node, _, _, err := editor.findNodeLocked(id)
	if err != nil {
		return nil, err
	}
	data, temporary, err := editor.blendIfDataLocked(ctx, node)
	if err != nil {
		return nil, err
	}
	if len(data) <= 4 {
		return nil, nil
	}
	count := (len(data) - 4) / 8
	available := editor.source.Limits().MaxDecodeBytes - editor.ownedBytesLocked(nil)
	if temporary > available || int64(count)*8 > available-temporary {
		return nil, editError(psdfile.ErrLimit, "layers.blendIf", "owned Blend If query exceeds the working budget")
	}
	ranges := make([]psdmodel.BlendRange, count)
	for index := range ranges {
		position := 4 + index*8
		ranges[index] = psdmodel.BlendRange{
			Source:      blendIfSlider(data[position:]),
			Destination: blendIfSlider(data[position+4:]),
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return ranges, nil
}

// SetBlendIf replaces all Blend If ranges; nil or empty removes them and restores full coverage.
// All four byte endpoints are retained exactly, including overlapping black and white transitions.
// At most 57 ranges are accepted, matching the reader's composite-plus-channel limit.
// Rendering keeps its existing color-mode, extra-channel and combination support gates.
func (editor *Editor) SetBlendIf(id LayerID, ranges []psdmodel.BlendRange) error {
	return editor.SetBlendIfContext(context.Background(), id, ranges)
}

// SetBlendIfContext adds cancellation to SetBlendIf and commits only after validation and budget checks.
// Caller-owned input is copied; an identical range list leaves revision and cached previews unchanged.
func (editor *Editor) SetBlendIfContext(ctx context.Context, id LayerID, ranges []psdmodel.BlendRange) error {
	if ctx == nil {
		ctx = context.Background()
	}
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if err := editor.checkMutableLocked(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	node, _, _, err := editor.findNodeLocked(id)
	if err != nil {
		return err
	}
	if len(ranges) > 57 {
		return editError(ErrInvalidEdit, "layers.blendIf", "Blend If cannot contain more than 57 ranges")
	}
	for _, value := range ranges {
		if !blendIfOrdered(value.Source) || !blendIfOrdered(value.Destination) {
			return editError(ErrInvalidEdit, "layers.blendIf", "each black or white split endpoint pair must be ordered")
		}
	}
	current, temporary, err := editor.blendIfDataLocked(ctx, node)
	if err != nil {
		return err
	}
	if blendIfEqual(current, ranges) {
		return nil
	}
	limits := editor.source.Limits()
	encodedBytes := int64(4 + len(ranges)*8)
	ownedBytes := encodedBytes + int64(len(ranges))*8
	available := limits.MaxDecodeBytes - editor.ownedBytesLocked(nil)
	if encodedBytes > limits.MaxMetadataBytes || temporary > available || ownedBytes > available-temporary {
		return editError(psdfile.ErrLimit, "layers.blendIf", "Blend If replacement exceeds the working budget")
	}
	encoded := make([]byte, int(encodedBytes))
	binary.BigEndian.PutUint32(encoded, uint32(len(ranges)*8))
	owned := make([]psdmodel.BlendRange, len(ranges))
	copy(owned, ranges)
	for index, value := range owned {
		position := 4 + index*8
		blendIfPutSlider(encoded[position:], value.Source)
		blendIfPutSlider(encoded[position+4:], value.Destination)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	node.blendRanges, node.blendRangesData, node.blendRangesChanged = owned, encoded, true
	node.setSemanticCost("blendIf", ownedBytes)
	editor.markChangedLocked(false)
	return nil
}

// blendIfDataLocked returns length-prefixed data and the newly allocated temporary bytes.
func (editor *Editor) blendIfDataLocked(ctx context.Context, node *editNode) ([]byte, int64, error) {
	if node.blendRangesChanged {
		return node.blendRangesData, 0, nil
	}
	if node.sourceIndex < 0 {
		return nil, 0, nil
	}
	span := editor.source.Layers[node.sourceIndex].BlendRanges
	if span.Length == 0 {
		return nil, 0, nil
	}
	if span.Length < 4 || (span.Length-4)%8 != 0 || span.Length > 4+57*8 {
		return nil, 0, editError(ErrUnsupported, "layers.blendIf", "source Blend If range length is not supported")
	}
	limits := editor.source.Limits()
	available := limits.MaxDecodeBytes - editor.ownedBytesLocked(nil)
	if span.Length > limits.MaxMetadataBytes || span.Length > available {
		return nil, 0, editError(psdfile.ErrLimit, "layers.blendIf", "source Blend If read exceeds the working budget")
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	data, err := editor.source.ReadSpan(span)
	if err != nil {
		return nil, 0, err
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if int64(binary.BigEndian.Uint32(data)) != span.Length-4 {
		return nil, 0, editError(ErrUnsupported, "layers.blendIf", "source Blend If length prefix is inconsistent")
	}
	for position := 4; position < len(data); position += 4 {
		if !blendIfOrdered(blendIfSlider(data[position:])) {
			return nil, 0, editError(ErrUnsupported, "layers.blendIf", "source Blend If split endpoints are unordered")
		}
	}
	return data, int64(cap(data)), nil
}

func blendIfEqual(data []byte, ranges []psdmodel.BlendRange) bool {
	if len(data) <= 4 {
		return len(ranges) == 0
	}
	if len(data) != 4+len(ranges)*8 {
		return false
	}
	for index, value := range ranges {
		position := 4 + index*8
		if blendIfSlider(data[position:]) != value.Source || blendIfSlider(data[position+4:]) != value.Destination {
			return false
		}
	}
	return true
}

func blendIfOrdered(value psdmodel.BlendSlider) bool {
	return value.BlackLow <= value.BlackHigh && value.WhiteLow <= value.WhiteHigh
}

func blendIfSlider(data []byte) psdmodel.BlendSlider {
	return psdmodel.BlendSlider{BlackLow: data[0], BlackHigh: data[1], WhiteLow: data[2], WhiteHigh: data[3]}
}

func blendIfPutSlider(data []byte, value psdmodel.BlendSlider) {
	data[0], data[1], data[2], data[3] = value.BlackLow, value.BlackHigh, value.WhiteLow, value.WhiteHigh
}
