package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdvector"
)

type vectorMaskChange struct {
	data          []byte
	removed       bool
	dropGenerated bool
	promoteReal   bool
}

// AddVectorMask adds independently owned vector geometry to a layer or group without a vector mask.
// The vector canvas must match the document. An existing user pixel mask remains unchanged.
// Duplicate vector blocks and ambiguous pixel-mask layouts return ErrUnsupported without mutation.
func (e *Editor) AddVectorMask(ctx context.Context, id LayerID, mask *psdvector.Mask) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.checkMutableLocked(); err != nil {
		return err
	}
	if mask == nil || mask.Width != int(e.source.Header.Width) || mask.Height != int(e.source.Header.Height) {
		return editError(ErrInvalidEdit, "layers.vectorMask", "vector dimensions must match the existing document canvas")
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return err
	}
	_, exists, err := e.vectorMaskBlockLocked(node)
	if err != nil {
		return err
	}
	if exists {
		return editError(ErrInvalidEdit, "layers.vectorMask", "layer already has a vector mask")
	}
	state, err := e.pixelMaskStateLocked(ctx, node)
	if err != nil {
		return err
	}
	if state.generated || state.hasReal {
		return editError(ErrUnsupported, "layers.vectorMask", "mask layout cannot safely acquire vector geometry")
	}
	// A default-only pixel mask needs a real channel to remain a user mask beside vector geometry.
	if state.hasUser && len(e.pixelMaskChannelsLocked(node)) == 0 {
		return editError(ErrUnsupported, "layers.vectorMask", "default-only user mask has no independent sample channel")
	}
	scratch := int64(0)
	if node.maskData == nil {
		scratch = int64(len(state.data))
	}
	return e.setLifecycleVectorMaskLocked(ctx, node, mask, scratch)
}

// RemoveVectorMask removes vector geometry and its generated coverage while preserving a user pixel mask.
// Layers without vector geometry are unchanged. A real -3 user mask is retained as an ordinary -2 mask.
// Unchanged user samples retain their original encoded bytes while generated vector coverage is removed.
func (e *Editor) RemoveVectorMask(ctx context.Context, id LayerID) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.checkMutableLocked(); err != nil {
		return err
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return err
	}
	block, exists, err := e.vectorMaskBlockLocked(node)
	if err != nil {
		return err
	}
	if !exists {
		return ctx.Err()
	}
	state, err := e.pixelMaskStateLocked(ctx, node)
	if err != nil {
		return err
	}
	if state.hasReal && !state.generated {
		return editError(ErrUnsupported, "layers.vectorMask", "removing vector geometry would require real-mask migration")
	}
	if state.hasReal && state.data[state.layout.flagsOffset]&8 != 0 {
		return editError(ErrUnsupported, "layers.vectorMask", "real user mask has generated-coverage semantics")
	}
	settings := clonePixelMaskSettings(state.layout.settings)
	settings.VectorDensity, settings.VectorFeather = nil, nil
	length := int64(4)
	if state.hasUser {
		length = vectorRemovedMaskLength(state, settings)
	}
	change := node.pixelMaskChange
	extraBacking := int64(0)
	if change != nil && change.channelID == -3 {
		promoted := *change
		promoted.channelID = -2
		if change.plane != nil {
			plane := *change.plane
			plane.ID = -2
			promoted.plane = &plane
		}
		change = &promoted
	} else if state.generated && state.hasUser {
		if plane, replaced := node.planes[-3]; replaced {
			plane.ID = -2
			change = &pixelMaskChange{channelID: -2, plane: &plane,
				bounds: pixelMaskStoredBounds(state.data[24:40])}
			extraBacking = int64(cap(plane.Data) - len(plane.Data))
		}
	}
	if err := e.checkPixelMaskLifecycleBudgetLocked(state.data, length, extraBacking); err != nil {
		return err
	}
	data := []byte{0, 0, 0, 0}
	if state.hasUser {
		data = vectorRemovedMaskData(state, settings)
	}
	dropGenerated := state.generated
	promoteReal := state.generated && state.hasReal ||
		node.pixelMaskChange != nil && node.pixelMaskChange.channelID == -3
	if node.vectorMaskChange != nil {
		dropGenerated = dropGenerated || node.vectorMaskChange.dropGenerated
		promoteReal = promoteReal || node.vectorMaskChange.promoteReal
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	delete(node.additional, block.Span.Offset)
	node.vectorMask, node.maskData = nil, data
	node.vectorMaskChange = &vectorMaskChange{removed: true, dropGenerated: dropGenerated, promoteReal: promoteReal}
	node.pixelMaskChange = change
	if change != nil {
		node.setSemanticCost("pixelMaskChange", 128)
	}
	if state.hasUser {
		node.pixelMask = &settings
		node.setSemanticCost("pixelMask", 512)
	} else {
		node.pixelMask = nil
		node.setSemanticCost("pixelMask", 0)
	}
	if state.generated {
		delete(node.planes, -2)
	}
	if promoteReal {
		delete(node.planes, -3)
	}
	node.setSemanticCost("vector", 0)
	node.setSemanticCost("vectorLifecycle", 128)
	e.markChangedLocked(false)
	return nil
}

func (e *Editor) vectorMaskBlockLocked(node *editNode) (psdfile.AdditionalBlock, bool, error) {
	var block psdfile.AdditionalBlock
	found := false
	if node.sourceIndex >= 0 && node.sourceIndex < len(e.source.Layers) {
		for _, candidate := range e.source.Layers[node.sourceIndex].AdditionalInfo {
			if candidate.Key != "vmsk" && candidate.Key != "vsms" {
				continue
			}
			if found {
				return block, false, editError(ErrUnsupported, "layers.vectorMask", "multiple vector-mask blocks are ambiguous")
			}
			block, found = candidate, true
		}
	}
	if node.vectorMaskChange != nil {
		return block, !node.vectorMaskChange.removed, nil
	}
	return block, found, nil
}

func (e *Editor) setLifecycleVectorMaskLocked(
	ctx context.Context, node *editNode, mask *psdvector.Mask, scratch int64,
) error {
	limits := e.source.Limits()
	available := limits.MaxDecodeBytes - e.ownedBytesLocked(nil) - scratch
	encodeLimit := min(limits.MaxMetadataBytes, (available-128)/2)
	if encodeLimit <= 0 {
		return editError(psdfile.ErrLimit, "layers.vectorMask", "retained overlays consume the working budget")
	}
	encoded, err := psdvector.EncodeMask(ctx, mask, psdvector.EncodeOptions{
		MaxBytes: encodeLimit, MaxRecords: limits.MaxDescriptorItems,
	})
	if err != nil {
		return vectorEditError(err)
	}
	if change := node.vectorMaskChange; change != nil && !change.removed && bytes.Equal(change.data, encoded) {
		return ctx.Err()
	}
	count, backingBytes := int64((len(encoded)-8)/26), int64(cap(encoded))
	if backingBytes > available || available-backingBytes < 640 || count > (available-backingBytes-640)/256 {
		return editError(psdfile.ErrLimit, "layers.vectorMask", "vector lifecycle edit exceeds the peak working budget")
	}
	owned, err := psdvector.DecodeMask(ctx, encoded, mask.Width, mask.Height, psdvector.DecodeOptions{
		MaxBytes:   min(limits.MaxMetadataBytes, available-128-backingBytes+int64(len(encoded))),
		MaxRecords: limits.MaxDescriptorItems,
	})
	if err != nil {
		return vectorEditError(err)
	}
	dropGenerated := node.vectorMaskChange != nil && node.vectorMaskChange.dropGenerated
	promoteReal := node.vectorMaskChange != nil && node.vectorMaskChange.promoteReal
	if err := ctx.Err(); err != nil {
		return err
	}
	node.vectorMask = owned
	node.vectorMaskChange = &vectorMaskChange{data: encoded, dropGenerated: dropGenerated, promoteReal: promoteReal}
	node.setSemanticCost("vector", 512+count*256)
	node.setSemanticCost("vectorLifecycle", 128)
	e.markChangedLocked(false)
	return nil
}

func vectorRemovedMaskLength(state pixelMaskState, settings PixelMaskSettings) int64 {
	parameterLength := len(pixelMaskParameters(settings))
	if parameterLength == 1 {
		parameterLength = 0
	}
	return max(24, int64(22+parameterLength+len(state.data)-state.layout.parametersEnd))
}

func vectorRemovedMaskData(state pixelMaskState, settings PixelMaskSettings) []byte {
	data := make([]byte, vectorRemovedMaskLength(state, settings))
	copy(data, state.data[:22])
	if state.hasReal {
		copy(data[4:20], state.data[24:40])
		data[20], data[21] = state.data[23], state.data[22]
	}
	parameters := pixelMaskParameters(settings)
	position := 22
	data[21] &^= 16
	if len(parameters) > 1 {
		data[21] |= 16
		position += copy(data[position:], parameters)
	}
	copy(data[position:], state.data[state.layout.parametersEnd:])
	binary.BigEndian.PutUint32(data, uint32(len(data)-4))
	return data
}
