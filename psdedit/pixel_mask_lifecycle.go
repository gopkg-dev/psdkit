package psdedit

import (
	"context"
	"encoding/binary"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdfile"
)

type pixelMaskChange struct {
	channelID int16
	plane     *psdfile.Plane
	bounds    image.Rectangle
	removed   bool
}

type pixelMaskState struct {
	data      []byte
	layout    pixelMaskLayout
	channelID int16
	generated bool
	hasReal   bool
	hasUser   bool
}

// AddPixelMask adds an independently owned user pixel mask in document coordinates.
// The compact plane must match bounds and the document depth; its ID is ignored and selected automatically.
// Omitting settings uses default coverage 255. At most one settings value may be supplied.
// Nil vector density and feather inherit existing shared parameters; SetPixelMask can subsequently clear them.
// An existing user mask must be removed explicitly before adding its replacement.
func (e *Editor) AddPixelMask(
	ctx context.Context, id LayerID, bounds image.Rectangle, plane psdfile.Plane, settings ...PixelMaskSettings,
) error {
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
	if len(settings) > 1 {
		return editError(ErrInvalidEdit, "layers.pixelMask", "at most one mask settings value is allowed")
	}
	options := PixelMaskSettings{DefaultColor: 255}
	if len(settings) == 1 {
		options = settings[0]
	}
	if !validPixelMaskFeather(options.Feather) || !validPixelMaskFeather(options.VectorFeather) {
		return editError(ErrInvalidEdit, "layers.pixelMask", "mask feather must be finite and nonnegative")
	}
	if !validPixelMaskBounds(bounds) || plane.Depth != e.effectiveDepthLocked() ||
		plane.Width != bounds.Dx() || plane.Height != bounds.Dy() {
		return editError(ErrInvalidEdit, "layers.pixelMask", "mask bounds, plane dimensions or depth are invalid")
	}
	length, err := e.checkPlaneLocked(plane)
	if err != nil {
		return err
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return err
	}
	state, err := e.pixelMaskStateLocked(ctx, node)
	if err != nil {
		return err
	}
	if state.hasUser {
		return editError(ErrInvalidEdit, "layers.pixelMask", "layer already has a user pixel mask")
	}
	if options.VectorDensity == nil {
		options.VectorDensity = state.layout.settings.VectorDensity
	}
	if options.VectorFeather == nil {
		options.VectorFeather = state.layout.settings.VectorFeather
	}
	if node.pixelMaskChange != nil {
		if node.pixelMaskChange.channelID == -3 && e.pixelMaskVectorBlocksLocked(node) == 0 {
			return editError(ErrUnsupported, "layers.pixelMask", "re-adding a real user mask requires vector geometry")
		}
		// Keep suppressing the original user channel after a remove/add sequence.
		state.channelID = node.pixelMaskChange.channelID
		state.generated = state.channelID == -3
	}
	if err := e.checkPixelMaskChannelCountLocked(node, state.channelID); err != nil {
		return err
	}
	if err := e.checkPixelMaskLifecycleBudgetLocked(state.data, pixelMaskAddedLength(state, options), length); err != nil {
		return err
	}
	encoded := addPixelMaskData(state, bounds, options)
	owned := make([]byte, len(plane.Data))
	copy(owned, plane.Data)
	plane.ID, plane.Data = state.channelID, owned
	ownedSettings := clonePixelMaskSettings(options)
	if err := ctx.Err(); err != nil {
		return err
	}
	node.maskData, node.pixelMask = encoded, &ownedSettings
	node.pixelMaskChange = &pixelMaskChange{channelID: state.channelID, bounds: bounds, plane: &plane}
	delete(node.planes, state.channelID)
	node.setSemanticCost("pixelMask", 512)
	node.setSemanticCost("pixelMaskChange", 128)
	e.markChangedLocked(false)
	return nil
}

// RemovePixelMask removes only the effective user pixel mask and its channel.
// Vector geometry, generated vector coverage and shared vector density/feather remain intact.
// Layers without a user pixel mask are unchanged. Ambiguous dual user masks return ErrUnsupported.
func (e *Editor) RemovePixelMask(ctx context.Context, id LayerID) error {
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
	state, err := e.pixelMaskStateLocked(ctx, node)
	if err != nil {
		return err
	}
	if !state.hasUser {
		return ctx.Err()
	}
	settings := PixelMaskSettings{
		VectorDensity: state.layout.settings.VectorDensity, VectorFeather: state.layout.settings.VectorFeather,
	}
	if e.pixelMaskVectorBlocksLocked(node) == 0 {
		// Parameters without vector geometry have no remaining owner after deleting the user mask.
		settings.VectorDensity, settings.VectorFeather = nil, nil
	}
	if err := e.checkPixelMaskLifecycleBudgetLocked(state.data, pixelMaskRemovedLength(state, settings), 0); err != nil {
		return err
	}
	encoded := removePixelMaskData(state, settings)
	ownedSettings := clonePixelMaskSettings(settings)
	if err := ctx.Err(); err != nil {
		return err
	}
	node.maskData, node.pixelMask = encoded, &ownedSettings
	node.pixelMaskChange = &pixelMaskChange{channelID: state.channelID, removed: true}
	delete(node.planes, state.channelID)
	node.setSemanticCost("pixelMask", 512)
	node.setSemanticCost("pixelMaskChange", 128)
	e.markChangedLocked(false)
	return nil
}

func validPixelMaskBounds(bounds image.Rectangle) bool {
	return bounds.Min.X >= math.MinInt32 && bounds.Min.Y >= math.MinInt32 &&
		bounds.Max.X <= math.MaxInt32 && bounds.Max.Y <= math.MaxInt32 &&
		bounds.Min.X <= bounds.Max.X && bounds.Min.Y <= bounds.Max.Y
}

func (e *Editor) checkPixelMaskChannelCountLocked(node *editNode, channelID int16) error {
	count := 0
	seen := make(map[int16]bool)
	if node.sourceIndex >= 0 && node.sourceIndex < len(e.source.Layers) {
		for _, channel := range e.source.Layers[node.sourceIndex].Channels {
			var retained bool
			channel, retained = effectiveSourceChannel(node, channel)
			if !retained {
				continue
			}
			count++
			seen[channel.ID] = true
		}
	}
	add := func(id int16) {
		if !seen[id] {
			seen[id] = true
			count++
		}
	}
	if !node.group && (node.pixels != nil || node.virtualComposite) {
		for _, id := range []int16{0, 1, 2, -1} {
			add(id)
		}
	}
	for id := range node.planes {
		add(id)
	}
	add(channelID)
	if count > 56 {
		return editError(psdfile.ErrLimit, "layers.pixelMask", "adding the mask would exceed 56 layer channels")
	}
	return nil
}

func (e *Editor) checkPixelMaskLifecycleBudgetLocked(data []byte, encoded, samples int64) error {
	limits := e.source.Limits()
	available := limits.MaxDecodeBytes - e.ownedBytesLocked(nil)
	// Source metadata, old overlays, replacement samples and metadata coexist until the atomic commit.
	metadata := int64(len(data)) + encoded + 640
	if uint64(encoded-4) > math.MaxUint32 || encoded > limits.MaxMetadataBytes ||
		metadata > available || samples > available-metadata {
		return editError(psdfile.ErrLimit, "layers.pixelMask", "mask lifecycle edit exceeds the peak working budget")
	}
	return nil
}

func (e *Editor) readPixelMaskDataLocked(ctx context.Context, node *editNode) ([]byte, error) {
	if node.maskData != nil {
		return node.maskData, ctx.Err()
	}
	if node.sourceIndex < 0 || node.sourceIndex >= len(e.source.Layers) {
		return []byte{0, 0, 0, 0}, ctx.Err()
	}
	record := e.source.Layers[node.sourceIndex]
	if record.Mask == nil && record.MaskData.Length <= 4 {
		return []byte{0, 0, 0, 0}, ctx.Err()
	}
	limits := e.source.Limits()
	available := limits.MaxDecodeBytes - e.ownedBytesLocked(nil)
	if record.MaskData.Length > limits.MaxMetadataBytes || record.MaskData.Length+512 > available {
		return nil, editError(psdfile.ErrLimit, "layers.pixelMask", "mask metadata exceeds budget")
	}
	data, err := e.source.ReadSpan(record.MaskData)
	if err != nil {
		return nil, err
	}
	return data, ctx.Err()
}

func (e *Editor) pixelMaskVectorBlocksLocked(node *editNode) int {
	if node.vectorMaskChange != nil {
		if node.vectorMaskChange.removed {
			return 0
		}
		return 1
	}
	if node.sourceIndex < 0 || node.sourceIndex >= len(e.source.Layers) {
		return 0
	}
	count := 0
	for _, block := range e.source.Layers[node.sourceIndex].AdditionalInfo {
		if block.Key == "vmsk" || block.Key == "vsms" {
			count++
		}
	}
	return count
}

func (e *Editor) pixelMaskChannelsLocked(node *editNode) []psdfile.ChannelRecord {
	var channels []psdfile.ChannelRecord
	if node.sourceIndex >= 0 && node.sourceIndex < len(e.source.Layers) {
		for _, channel := range e.source.Layers[node.sourceIndex].Channels {
			var retained bool
			channel, retained = effectiveSourceChannel(node, channel)
			if !retained {
				continue
			}
			if channel.ID != -2 && channel.ID != -3 ||
				node.pixelMaskChange != nil && channel.ID == node.pixelMaskChange.channelID {
				continue
			}
			channels = append(channels, channel)
		}
	}
	if change := node.pixelMaskChange; change != nil && !change.removed {
		channels = append(channels, psdfile.ChannelRecord{ID: change.channelID, Bounds: change.bounds})
	}
	return channels
}

func (e *Editor) pixelMaskStateLocked(ctx context.Context, node *editNode) (pixelMaskState, error) {
	state := pixelMaskState{channelID: -2}
	unsupported := func(detail string) (pixelMaskState, error) {
		return pixelMaskState{}, editError(ErrUnsupported, "layers.pixelMask", detail)
	}
	data, err := e.readPixelMaskDataLocked(ctx, node)
	if err != nil {
		return state, err
	}
	state.data = data
	primary, real := 0, 0
	for _, channel := range e.pixelMaskChannelsLocked(node) {
		if channel.ID == -2 {
			primary++
		} else {
			real++
		}
	}
	if primary > 1 || real > 1 {
		return unsupported("duplicate mask channels are ambiguous")
	}
	vectorBlocks := e.pixelMaskVectorBlocksLocked(node)
	if vectorBlocks > 1 {
		return unsupported("multiple vector-mask blocks are ambiguous")
	}
	if len(data) == 4 && binary.BigEndian.Uint32(data) == 0 {
		if primary != 0 || real != 0 {
			return unsupported("mask channels have no matching metadata")
		}
		state.generated = vectorBlocks == 1
		if state.generated {
			state.channelID = -3
		}
		return state, nil
	}
	state.hasReal = real == 1
	state.layout, err = decodePixelMaskHeader(data, state.hasReal)
	if err != nil {
		return state, err
	}
	state.generated = data[21]&8 != 0
	if state.generated && vectorBlocks != 1 {
		// A zero-sized metadata carrier preserves shared parameters without creating a user mask.
		if primary != 0 || real != 0 || state.hasReal || pixelMaskStoredBounds(data[4:20]) != (image.Rectangle{}) {
			return unsupported("generated coverage requires one unambiguous vector-mask block")
		}
		state.generated = false
		return state, nil
	}
	if state.generated {
		state.channelID = -3
		if state.hasReal != (real == 1) {
			return unsupported("real user-mask metadata and channel disagree")
		}
		state.hasUser = real == 1
	} else {
		if real != 0 || state.hasReal {
			return unsupported("dual user masks have ambiguous lifecycle semantics")
		}
		// Ordinary metadata remains a default-coverage user mask even when vector geometry is present.
		// Generated vector coverage uses the explicit flag above and never acquires user-mask ownership here.
		state.hasUser = true
	}
	if state.hasUser {
		state.layout, err = decodePixelMaskLayout(data, e.pixelMaskChannelsLocked(node))
	}
	return state, err
}

func addPixelMaskData(state pixelMaskState, bounds image.Rectangle, settings PixelMaskSettings) []byte {
	parameters := pixelMaskParameters(settings)
	length := int(pixelMaskAddedLength(state, settings))
	var data []byte
	if state.generated {
		data = pixelMaskPrimaryData(state, parameters, length)
		data = append(data, pixelMaskSettingFlags(settings), settings.DefaultColor)
		data = appendPixelMaskBounds(data, bounds)
		if len(parameters) > 1 {
			data = append(data, parameters...)
		}
		if len(state.data) >= 22 {
			data = append(data, state.data[state.layout.parametersEnd:]...)
		}
	} else {
		data = make([]byte, 4, length)
		data = appendPixelMaskBounds(data, bounds)
		data = append(data, settings.DefaultColor, pixelMaskSettingFlags(settings))
		if len(parameters) > 1 {
			data[21] |= 16
			data = append(data, parameters...)
		} else {
			data = append(data, 0, 0)
		}
	}
	binary.BigEndian.PutUint32(data, uint32(len(data)-4))
	return data
}

func removePixelMaskData(state pixelMaskState, settings PixelMaskSettings) []byte {
	parameters := pixelMaskParameters(settings)
	if !state.generated && len(parameters) == 1 {
		return []byte{0, 0, 0, 0}
	}
	data := pixelMaskPrimaryData(state, parameters, int(pixelMaskRemovedLength(state, settings)))
	if len(parameters) > 1 {
		data = append(data, parameters...)
	}
	if state.generated {
		data = append(data, state.data[state.layout.parametersEnd:]...)
	}
	if len(data) == 22 {
		data = append(data, 0, 0)
	}
	binary.BigEndian.PutUint32(data, uint32(len(data)-4))
	return data
}

func pixelMaskPrimaryData(state pixelMaskState, parameters []byte, length int) []byte {
	data := make([]byte, 22, length)
	if state.generated && len(state.data) >= 22 {
		copy(data, state.data[:22])
	} else {
		data[20], data[21] = 255, 8
	}
	data[21] &^= 16
	if len(parameters) > 1 {
		data[21] |= 16
	}
	return data
}

func pixelMaskAddedLength(state pixelMaskState, settings PixelMaskSettings) int64 {
	parameters := len(pixelMaskParameters(settings))
	if parameters == 1 {
		parameters = 0
	}
	if state.generated {
		tail := 0
		if len(state.data) >= 22 {
			tail = len(state.data) - state.layout.parametersEnd
		}
		return int64(40 + parameters + tail)
	}
	if parameters == 0 {
		return 24
	}
	return int64(22 + parameters)
}

func pixelMaskRemovedLength(state pixelMaskState, settings PixelMaskSettings) int64 {
	parameters := len(pixelMaskParameters(settings))
	if !state.generated && parameters == 1 {
		return 4
	}
	if parameters == 1 {
		parameters = 0
	}
	length := 22 + parameters
	if state.generated {
		length += len(state.data) - state.layout.parametersEnd
	}
	if length == 22 {
		length += 2
	}
	return int64(length)
}

func pixelMaskSettingFlags(settings PixelMaskSettings) byte {
	var flags byte
	if settings.Relative {
		flags |= 1
	}
	if settings.Disabled {
		flags |= 2
	}
	if settings.Invert {
		flags |= 4
	}
	return flags
}

func appendPixelMaskBounds(data []byte, bounds image.Rectangle) []byte {
	for _, coordinate := range []int{bounds.Min.Y, bounds.Min.X, bounds.Max.Y, bounds.Max.X} {
		data = binary.BigEndian.AppendUint32(data, uint32(coordinate))
	}
	return data
}

func pixelMaskStoredBounds(data []byte) image.Rectangle {
	return image.Rectangle{
		Min: image.Pt(int(int32(binary.BigEndian.Uint32(data[4:]))), int(int32(binary.BigEndian.Uint32(data)))),
		Max: image.Pt(int(int32(binary.BigEndian.Uint32(data[12:]))), int(int32(binary.BigEndian.Uint32(data[8:])))),
	}
}
