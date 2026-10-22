package psdedit

import (
	"context"
	"maps"

	"github.com/gopkg-dev/psdkit/psdfile"
)

// SetFillOpacity changes the layer's byte fill opacity without changing source pixels or other tagged blocks.
// Missing fill defaults to 255. Existing iOpa records must all use four bytes with zero reserved bytes.
// A visual edit follows the existing automatic-preview gates; accurate explicit composites support other depths.
func (editor *Editor) SetFillOpacity(id LayerID, value uint8) error {
	return editor.SetFillOpacityContext(context.Background(), id, value)
}

// SetFillOpacityContext adds cancellation to the bounded source reads required by SetFillOpacity.
// Invalid source representations, read failures, budgets and cancellation leave all edit state unchanged.
func (editor *Editor) SetFillOpacityContext(ctx context.Context, id LayerID, value uint8) error {
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
	limits := editor.source.Limits()
	retained := editor.ownedBytesLocked(nil)
	current, count := uint8(255), int64(0)
	if node.sourceIndex >= 0 {
		for _, block := range editor.source.Layers[node.sourceIndex].AdditionalInfo {
			if block.Key != "iOpa" {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if block.Data.Length != 4 {
				return editError(ErrUnsupported, "layers.fillOpacity", "source iOpa is not a four-byte representation")
			}
			if retained > limits.MaxDecodeBytes || 4 > limits.MaxDecodeBytes-retained {
				return editError(psdfile.ErrLimit, "layers.fillOpacity", "source iOpa read exceeds the working budget")
			}
			data, err := editor.source.ReadSpan(block.Data)
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if data[1] != 0 || data[2] != 0 || data[3] != 0 {
				return editError(ErrUnsupported, "layers.fillOpacity", "source iOpa has unknown reserved bytes")
			}
			current = data[0]
			count++
		}
	}
	if node.fillOpacityChanged {
		current = node.fillOpacity
	}
	if current == value {
		return nil
	}
	// Each staged replacement retains four payload bytes; the conservative allowance includes map entries.
	payload := count * 4
	working := count * 68
	if count == 0 {
		payload = 4
		working = 16
	}
	if payload > limits.MaxMetadataBytes || retained > limits.MaxDecodeBytes ||
		working > limits.MaxDecodeBytes-retained {
		return editError(psdfile.ErrLimit, "layers.fillOpacity", "fill-opacity overlays exceed the working budget")
	}
	var updates map[int64][]byte
	if count != 0 {
		updates = make(map[int64][]byte)
		for _, block := range editor.source.Layers[node.sourceIndex].AdditionalInfo {
			if block.Key == "iOpa" {
				updates[block.Span.Offset] = []byte{value, 0, 0, 0}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(updates) != 0 && node.additional == nil {
		node.additional = make(map[int64][]byte)
	}
	maps.Copy(node.additional, updates)
	node.fillOpacity, node.fillOpacityChanged = value, true
	node.fillOpacityAdded = count == 0
	editor.markChangedLocked(false)
	return nil
}

// fillOpacityLocked queries the effective last-wins value without normalizing opaque original records.
// Legacy one-byte records remain readable and copyable even though they are not editable.
func (editor *Editor) fillOpacityLocked(node *editNode) (uint8, error) {
	if node.fillOpacityChanged {
		return node.fillOpacity, nil
	}
	value := uint8(255)
	if node.sourceIndex < 0 {
		return value, nil
	}
	for _, block := range editor.source.Layers[node.sourceIndex].AdditionalInfo {
		if block.Key != "iOpa" {
			continue
		}
		if block.Data.Length < 1 {
			return 0, editError(ErrUnsupported, "layers.fillOpacity", "source iOpa has no readable value byte")
		}
		data, err := editor.source.ReadSpan(psdfile.Span{Offset: block.Data.Offset, Length: 1})
		if err != nil {
			return 0, err
		}
		value = data[0]
	}
	return value, nil
}
