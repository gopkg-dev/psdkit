package psdedit

import (
	"bytes"
	"context"
	"maps"
	"strings"

	"github.com/gopkg-dev/psdkit/psdfile"
)

// SetBlendMode changes a layer or group's blend mode, including all retained group-divider overrides.
// PSD keys and common English names are accepted; LayerInfo reports the canonical four-byte PSD key.
// Pass-through is limited to groups. Dissolve can be saved with an accurate explicit composite;
// automatic previews still reject modes or combinations outside the compositor's supported scope.
func (editor *Editor) SetBlendMode(id LayerID, mode string) error {
	return editor.SetBlendModeContext(context.Background(), id, mode)
}

// SetBlendModeContext adds cancellation to the bounded metadata reads required by SetBlendMode.
// Malformed group overrides, read failures and budgets leave the editor unchanged.
func (editor *Editor) SetBlendModeContext(ctx context.Context, id LayerID, mode string) error {
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
	key, valid := editBlendKey(mode)
	if !valid || key == "pass" && !node.group {
		return editError(ErrInvalidEdit, "layers.blendMode", "use a known PSD blend mode; pass-through requires a group")
	}
	current, err := editor.blendModeLocked(node)
	if err != nil {
		return err
	}
	if current == key {
		return ctx.Err()
	}
	limits := editor.source.Limits()
	retained := editor.ownedBytesLocked(nil)
	updates := make(map[int64][]byte)
	working, payload := int64(0), int64(0)
	if node.sourceIndex >= 0 {
		record := editor.source.Layers[node.sourceIndex]
		if record.Divider > 3 {
			return editError(ErrUnsupported, "layers.blendMode", "unknown group divider cannot be edited")
		}
		for _, block := range record.AdditionalInfo {
			if block.Key != "lsct" && block.Key != "lsdk" {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			previous, replaced := node.additional[block.Span.Offset]
			length := block.Data.Length
			if replaced {
				length = int64(len(previous))
			}
			if length != 4 && length < 12 {
				return editError(ErrUnsupported, "layers.blendMode", "group blend override has an ambiguous layout")
			}
			encodedLength := max(length, 12)
			if encodedLength > limits.MaxMetadataBytes-payload || retained > limits.MaxDecodeBytes ||
				encodedLength+64 > limits.MaxDecodeBytes-retained-working {
				return editError(psdfile.ErrLimit, "layers.blendMode", "group blend overlays exceed the working budget")
			}
			data := make([]byte, int(encodedLength))
			if replaced {
				copy(data, previous)
			} else if _, err := editor.source.ReadAt(data[:int(length)], block.Data.Offset); err != nil {
				return err
			}
			if length >= 12 && !bytes.Equal(data[4:8], []byte("8BIM")) {
				return editError(ErrUnsupported, "layers.blendMode", "group blend signature is unknown")
			}
			copy(data[4:8], "8BIM")
			copy(data[8:12], key)
			updates[block.Span.Offset] = data
			working += encodedLength + 64
			payload += encodedLength
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(updates) != 0 && node.additional == nil {
		node.additional = make(map[int64][]byte)
	}
	maps.Copy(node.additional, updates)
	node.blendMode, node.blendModeChanged = key, true
	editor.markChangedLocked(false)
	return nil
}

// SetClipping changes whether a layer is clipped to the preceding base in bottom-to-top sibling order.
// A missing base or unsupported group combination is rejected by automatic composition, not silently flattened.
func (editor *Editor) SetClipping(id LayerID, clipped bool) error {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if err := editor.checkMutableLocked(); err != nil {
		return err
	}
	node, _, _, err := editor.findNodeLocked(id)
	if err != nil {
		return err
	}
	if node.sourceIndex >= 0 && editor.source.Layers[node.sourceIndex].Clipping > 1 {
		return editError(ErrUnsupported, "layers.clipping", "source clipping byte has unknown semantics")
	}
	if editor.clippingLocked(node) == clipped {
		return nil
	}
	node.clipping, node.clippingChanged = clipped, true
	editor.markChangedLocked(false)
	return nil
}

func (editor *Editor) clippingLocked(node *editNode) bool {
	if node.clippingChanged {
		return node.clipping
	}
	return node.sourceIndex >= 0 && editor.source.Layers[node.sourceIndex].Clipping != 0
}

// blendModeLocked follows the parser's last group-divider override while leaving unknown keys intact.
func (editor *Editor) blendModeLocked(node *editNode) (string, error) {
	if node.blendModeChanged {
		return node.blendMode, nil
	}
	if node.sourceIndex < 0 {
		if node.group {
			return "pass", nil
		}
		return "norm", nil
	}
	record := editor.source.Layers[node.sourceIndex]
	mode := record.BlendMode
	for _, block := range record.AdditionalInfo {
		if block.Key != "lsct" && block.Key != "lsdk" {
			continue
		}
		if block.Data.Length == 4 {
			continue
		}
		if block.Data.Length < 12 {
			return "", editError(ErrUnsupported, "layers.blendMode", "group blend override has an ambiguous layout")
		}
		var data [8]byte
		if _, err := editor.source.ReadAt(data[:], block.Data.Offset+4); err != nil {
			return "", err
		}
		if string(data[:4]) != "8BIM" {
			return "", editError(ErrUnsupported, "layers.blendMode", "group blend signature is unknown")
		}
		mode = string(data[4:])
	}
	return mode, nil
}

func editBlendKey(mode string) (string, bool) {
	switch strings.TrimRight(mode, " ") {
	case "norm", "normal":
		return "norm", true
	case "diss", "dissolve":
		return "diss", true
	case "pass", "pass-through", "passThrough":
		return "pass", true
	case "dark", "darken":
		return "dark", true
	case "mul", "multiply":
		return "mul ", true
	case "idiv", "color-burn", "colorBurn":
		return "idiv", true
	case "lbrn", "linear-burn", "linearBurn":
		return "lbrn", true
	case "dkCl", "darker-color", "darkerColor":
		return "dkCl", true
	case "lite", "lighten":
		return "lite", true
	case "scrn", "screen":
		return "scrn", true
	case "div", "color-dodge", "colorDodge":
		return "div ", true
	case "lddg", "linear-dodge", "linearDodge", "add":
		return "lddg", true
	case "lgCl", "lighter-color", "lighterColor":
		return "lgCl", true
	case "over", "overlay":
		return "over", true
	case "sLit", "soft-light", "softLight":
		return "sLit", true
	case "hLit", "hard-light", "hardLight":
		return "hLit", true
	case "vLit", "vivid-light", "vividLight":
		return "vLit", true
	case "lLit", "linear-light", "linearLight":
		return "lLit", true
	case "pLit", "pin-light", "pinLight":
		return "pLit", true
	case "hMix", "hard-mix", "hardMix":
		return "hMix", true
	case "diff", "difference":
		return "diff", true
	case "smud", "exclusion":
		return "smud", true
	case "fsub", "subtract":
		return "fsub", true
	case "fdiv", "divide":
		return "fdiv", true
	case "hue":
		return "hue ", true
	case "sat", "saturation":
		return "sat ", true
	case "col", "color", "colr":
		return "colr", true
	case "lum", "luminosity":
		return "lum ", true
	}
	return "", false
}
