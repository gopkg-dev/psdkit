package psdedit

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// AdvancedBlendingOptions contains optional advanced layer-blending settings.
// Nil fields leave the corresponding setting unchanged in SetAdvancedBlending.
// A non-nil empty ChannelRestrictions clears the restriction list; channel IDs are preserved verbatim.
// MaskAsGlobal and VectorMaskAsGlobal correspond to Photoshop's mask-hides-effects controls.
// Missing BlendClipped and TransparencyShapes settings default to true, and missing Knockout to None.
type AdvancedBlendingOptions struct {
	Knockout            *psdmodel.Knockout
	BlendClipped        *bool
	BlendInterior       *bool
	MaskAsGlobal        *bool
	VectorMaskAsGlobal  *bool
	TransparencyShapes  *bool
	ChannelRestrictions []uint32
}

const (
	advancedKnockout = iota
	advancedBlendClipped
	advancedBlendInterior
	advancedMaskAsGlobal
	advancedVectorMaskAsGlobal
	advancedTransparencyShapes
	advancedRestrictions
	advancedTagCount
)

var advancedBlendingKeys = [advancedTagCount]string{"knko", "clbl", "infx", "lmgm", "vmgm", "tsly", "brst"}

type advancedBlendingState struct {
	flags        [advancedRestrictions]uint8
	present      uint8
	restrictions [56]uint32
	count        int
}

type advancedBlendingOverlay struct {
	state advancedBlendingState
	added [advancedTagCount][]byte
}

func (overlay *advancedBlendingOverlay) ownedBytes() int64 {
	total := int64(unsafe.Sizeof(*overlay))
	for _, data := range overlay.added {
		total += int64(len(data))
	}
	return total
}

// AdvancedBlending returns independently owned advanced settings, retaining nil for absent source tags.
// Ambiguous duplicate tags and unknown source layouts return ErrUnsupported without normalizing source bytes.
func (editor *Editor) AdvancedBlending(ctx context.Context, id LayerID) (AdvancedBlendingOptions, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	editor.mu.RLock()
	defer editor.mu.RUnlock()
	if err := editor.checkOpenLocked(); err != nil {
		return AdvancedBlendingOptions{}, err
	}
	if err := ctx.Err(); err != nil {
		return AdvancedBlendingOptions{}, err
	}
	node, _, _, err := editor.findNodeLocked(id)
	if err != nil {
		return AdvancedBlendingOptions{}, err
	}
	state, _, err := editor.advancedBlendingStateLocked(ctx, node)
	if err != nil {
		return AdvancedBlendingOptions{}, err
	}
	if int64(unsafe.Sizeof(AdvancedBlendingOptions{}))+int64(state.count)*4+16 >
		editor.source.Limits().MaxDecodeBytes-editor.ownedBytesLocked(nil) {
		return AdvancedBlendingOptions{}, editError(psdfile.ErrLimit, "layers.advancedBlending",
			"owned advanced-blending query exceeds the working budget")
	}
	return state.options(), nil
}

// SetAdvancedBlending patches selected advanced settings and preserves all unrelated source bytes.
// Existing known tags are replaced in place; missing tags are inserted with canonical payloads.
// Encoding and validation finish before mutation. Unsupported rendering combinations still require an explicit preview.
func (editor *Editor) SetAdvancedBlending(ctx context.Context, id LayerID, options AdvancedBlendingOptions) error {
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
	if err := validateAdvancedBlendingOptions(options); err != nil {
		return err
	}
	node, _, _, err := editor.findNodeLocked(id)
	if err != nil {
		return err
	}
	current, blocks, err := editor.advancedBlendingStateLocked(ctx, node)
	if err != nil {
		return err
	}
	updated := current.patched(options)
	var changed [advancedTagCount]bool
	var payloadBytes, updateCount int64
	for index := range advancedTagCount {
		changed[index] = !current.sameSetting(updated, index)
		if !changed[index] {
			updated.present = updated.present&^(1<<index) | current.present&(1<<index)
			if index != advancedRestrictions {
				updated.flags[index] = current.flags[index]
			}
			continue
		}
		updateCount++
		if index == advancedRestrictions {
			payloadBytes += int64(updated.count) * 4
		} else {
			payloadBytes += 4
		}
	}
	if updateCount == 0 {
		return ctx.Err()
	}
	limits := editor.source.Limits()
	working := payloadBytes + int64(unsafe.Sizeof(advancedBlendingOverlay{})) + updateCount*64
	if payloadBytes > limits.MaxMetadataBytes || working > limits.MaxDecodeBytes-editor.ownedBytesLocked(nil) {
		return editError(psdfile.ErrLimit, "layers.advancedBlending", "advanced-blending overlays exceed the working budget")
	}
	overlay := &advancedBlendingOverlay{state: updated}
	if node.advancedBlending != nil {
		overlay.added = node.advancedBlending.added
	}
	var replacements [advancedTagCount][]byte
	for index, change := range changed {
		if !change {
			continue
		}
		data := updated.payload(index)
		if blocks[index].Key == "" {
			overlay.added[index] = data
		} else {
			replacements[index] = data
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for index, data := range replacements {
		if data == nil {
			continue
		}
		if node.additional == nil {
			node.additional = make(map[int64][]byte)
		}
		node.additional[blocks[index].Span.Offset] = data
	}
	node.advancedBlending = overlay
	editor.markChangedLocked(false)
	return nil
}

func validateAdvancedBlendingOptions(options AdvancedBlendingOptions) error {
	if options.Knockout != nil && *options.Knockout > psdmodel.KnockoutDeep {
		return editError(ErrInvalidEdit, "layers.advancedBlending.knockout", "Knockout must be None, Shallow or Deep")
	}
	if len(options.ChannelRestrictions) > 56 {
		return editError(ErrInvalidEdit, "layers.advancedBlending.channelRestrictions",
			"at most 56 channels may be restricted")
	}
	for index, channel := range options.ChannelRestrictions {
		if slices.Contains(options.ChannelRestrictions[:index], channel) {
			return editError(ErrInvalidEdit, "layers.advancedBlending.channelRestrictions",
				"restricted channel IDs must be unique")
		}
	}
	return nil
}

func (editor *Editor) advancedBlendingStateLocked(
	ctx context.Context, node *editNode,
) (advancedBlendingState, [advancedTagCount]psdfile.AdditionalBlock, error) {
	state := advancedBlendingState{}
	var blocks [advancedTagCount]psdfile.AdditionalBlock
	if node.sourceIndex >= 0 {
		for _, block := range editor.source.Layers[node.sourceIndex].AdditionalInfo {
			if err := ctx.Err(); err != nil {
				return state, blocks, err
			}
			index := advancedBlendingKeyIndex(block.Key)
			if index < 0 {
				continue
			}
			if blocks[index].Key != "" {
				return state, blocks, editError(ErrUnsupported, "layers.advancedBlending",
					"duplicate advanced-blending tags are ambiguous")
			}
			blocks[index] = block
			length := block.Data.Length
			if index != advancedRestrictions && length != 4 ||
				index == advancedRestrictions && (length < 0 || length%4 != 0 || length > 56*4) {
				return state, blocks, editError(ErrUnsupported, "layers.advancedBlending",
					"unknown advanced-blending payload layout")
			}
			limits := editor.source.Limits()
			if length > limits.MaxMetadataBytes || length > limits.MaxDecodeBytes-editor.ownedBytesLocked(nil) {
				return state, blocks, editError(psdfile.ErrLimit, "layers.advancedBlending",
					"source advanced-blending read exceeds the working budget")
			}
			var buffer [56 * 4]byte
			data := buffer[:length]
			count, err := editor.source.ReadAt(data, block.Data.Offset)
			if count != len(data) || err != nil && !errors.Is(err, io.EOF) {
				if err == nil {
					err = io.ErrUnexpectedEOF
				}
				return state, blocks, err
			}
			if err := ctx.Err(); err != nil {
				return state, blocks, err
			}
			if err := state.decode(index, data); err != nil {
				return state, blocks, err
			}
		}
	}
	if node.advancedBlending != nil {
		state = node.advancedBlending.state
	}
	return state, blocks, ctx.Err()
}

func advancedBlendingKeyIndex(key string) int {
	for index, candidate := range advancedBlendingKeys {
		if key == candidate {
			return index
		}
	}
	return -1
}

func (state *advancedBlendingState) decode(index int, data []byte) error {
	state.present |= 1 << index
	if index == advancedRestrictions {
		state.count = len(data) / 4
		for item := range state.count {
			channel := binary.BigEndian.Uint32(data[item*4:])
			if slices.Contains(state.restrictions[:item], channel) {
				return editError(ErrUnsupported, "layers.advancedBlending", "duplicate restricted channel IDs are ambiguous")
			}
			state.restrictions[item] = channel
		}
		return nil
	}
	maximum := byte(1)
	if index == advancedKnockout {
		maximum = byte(psdmodel.KnockoutDeep)
	}
	if data[0] > maximum || data[1] != 0 || data[2] != 0 || data[3] != 0 {
		return editError(ErrUnsupported, "layers.advancedBlending", "unknown flag value or reserved advanced-blending bytes")
	}
	state.flags[index] = data[0]
	return nil
}

func (state advancedBlendingState) options() AdvancedBlendingOptions {
	result := AdvancedBlendingOptions{}
	if state.present&(1<<advancedKnockout) != 0 {
		result.Knockout = new(psdmodel.Knockout(state.flags[advancedKnockout]))
	}
	fields := [advancedRestrictions]**bool{nil, &result.BlendClipped, &result.BlendInterior,
		&result.MaskAsGlobal, &result.VectorMaskAsGlobal, &result.TransparencyShapes}
	for index := 1; index < advancedRestrictions; index++ {
		if state.present&(1<<index) != 0 {
			*fields[index] = new(state.flags[index] != 0)
		}
	}
	if state.present&(1<<advancedRestrictions) != 0 {
		result.ChannelRestrictions = make([]uint32, state.count)
		copy(result.ChannelRestrictions, state.restrictions[:state.count])
	}
	return result
}

func (state advancedBlendingState) patched(options AdvancedBlendingOptions) advancedBlendingState {
	if options.Knockout != nil {
		state.present |= 1 << advancedKnockout
		state.flags[advancedKnockout] = byte(*options.Knockout)
	}
	fields := [advancedRestrictions]*bool{nil, options.BlendClipped, options.BlendInterior,
		options.MaskAsGlobal, options.VectorMaskAsGlobal, options.TransparencyShapes}
	for index := 1; index < advancedRestrictions; index++ {
		if fields[index] == nil {
			continue
		}
		state.present |= 1 << index
		state.flags[index] = 0
		if *fields[index] {
			state.flags[index] = 1
		}
	}
	if options.ChannelRestrictions != nil {
		state.present |= 1 << advancedRestrictions
		state.count = len(options.ChannelRestrictions)
		copy(state.restrictions[:], options.ChannelRestrictions)
	}
	return state
}

func (state advancedBlendingState) sameSetting(other advancedBlendingState, index int) bool {
	if state.present&(1<<index) == 0 && other.present&(1<<index) == 0 {
		return true
	}
	if index == advancedRestrictions {
		if state.count != other.count {
			return false
		}
		for item := range state.count {
			if state.restrictions[item] != other.restrictions[item] {
				return false
			}
		}
		return true
	}
	value := state.flags[index]
	if state.present&(1<<index) == 0 {
		if index == advancedBlendClipped || index == advancedTransparencyShapes {
			value = 1
		} else if index != advancedKnockout {
			return other.present&(1<<index) == 0
		}
	}
	return value == other.flags[index]
}

func (state advancedBlendingState) payload(index int) []byte {
	if index != advancedRestrictions {
		return []byte{state.flags[index], 0, 0, 0}
	}
	data := make([]byte, state.count*4)
	for item, channel := range state.restrictions[:state.count] {
		binary.BigEndian.PutUint32(data[item*4:], channel)
	}
	return data
}

func appendAdvancedBlendingLocked(plan *writePlan, node *editNode) {
	if node.advancedBlending == nil {
		return
	}
	for index, data := range node.advancedBlending.added {
		if data != nil {
			plan.bytes(writerAdditional("8BIM", advancedBlendingKeys[index], 4, data))
		}
	}
}

func applyAdvancedBlending(layer *psdmodel.Layer, node *editNode) {
	if node.advancedBlending == nil {
		return
	}
	options := node.advancedBlending.state.options()
	if options.Knockout != nil {
		layer.Knockout = *options.Knockout
	}
	layer.BlendClipped, layer.BlendInterior = options.BlendClipped, options.BlendInterior
	layer.MaskAsGlobal, layer.VectorMaskAsGlobal = options.MaskAsGlobal, options.VectorMaskAsGlobal
	layer.TransparencyShapes = options.TransparencyShapes
	layer.ChannelRestrictions = options.ChannelRestrictions
}
