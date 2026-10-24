package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"slices"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// PixelMaskSettings contains editable metadata for a pixel mask without its geometry or samples.
// Nil density and feather fields remove the corresponding explicit parameter and restore its format default.
// Relative retains the stored position flag; changing it does not move the stored document-coordinate bounds.
// VectorDensity and VectorFeather share the pixel-mask metadata block and affect the layer's vector mask.
type PixelMaskSettings struct {
	Disabled, Relative, Invert bool
	DefaultColor               uint8
	Density                    *uint8
	Feather                    *float64
	VectorDensity              *uint8
	VectorFeather              *float64
}

type pixelMaskLayout struct {
	settings        PixelMaskSettings
	parametersStart int
	parametersEnd   int
	flagsOffset     int
	colorOffset     int
}

// PixelMask returns independently owned settings for the layer's effective existing user pixel mask.
// An ordinary mask without sample channels uses uniform default coverage, also beside a vector mask.
// Generated vector coverage without a real user-mask channel is not an editable pixel mask.
func (e *Editor) PixelMask(ctx context.Context, id LayerID) (PixelMaskSettings, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PixelMaskSettings{}, err
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if err := e.checkOpenLocked(); err != nil {
		return PixelMaskSettings{}, err
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return PixelMaskSettings{}, err
	}
	if node.pixelMaskChange != nil && node.pixelMaskChange.removed {
		return PixelMaskSettings{}, editError(ErrUnsupported, "layers.pixelMask", "layer has no user pixel mask")
	}
	if node.pixelMask != nil {
		return clonePixelMaskSettings(*node.pixelMask), ctx.Err()
	}
	_, layout, err := e.readPixelMaskLocked(ctx, node)
	return layout.settings, err
}

// SetPixelMask edits an existing user mask's flags, default coverage and density/feather parameters.
// Source channels, mask rectangles, reserved flag bits and opaque trailing bytes remain unchanged.
// Unknown parameter layouts and ambiguous live dual masks return ErrUnsupported without changing the editor.
// Saving unsupported rendering combinations requires an accurately supplied replacement composite.
func (e *Editor) SetPixelMask(ctx context.Context, id LayerID, settings PixelMaskSettings) error {
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
	if !validPixelMaskFeather(settings.Feather) || !validPixelMaskFeather(settings.VectorFeather) {
		return editError(ErrInvalidEdit, "layers.pixelMask", "mask feather must be finite and nonnegative")
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return err
	}
	data, layout, err := e.readPixelMaskLocked(ctx, node)
	if err != nil {
		return err
	}
	if equalPixelMaskSettings(layout.settings, settings) {
		return ctx.Err()
	}
	parameters := pixelMaskParameters(settings)
	// Preserve an existing explicit empty parameter set when no semantic field changed.
	hasParameters := len(parameters) > 1 || data[21]&16 != 0
	parameterLength := 0
	if hasParameters {
		parameterLength = len(parameters)
	}
	length := layout.parametersStart + parameterLength + len(data) - layout.parametersEnd
	limits := e.source.Limits()
	available := limits.MaxDecodeBytes - e.ownedBytesLocked(nil)
	// Source reads, the replacement block, and independently owned settings coexist until commit.
	if uint64(length-4) > math.MaxUint32 || int64(length) > limits.MaxMetadataBytes ||
		int64(len(data))+int64(length)+512 > available {
		return editError(psdfile.ErrLimit, "layers.pixelMask", "mask replacement exceeds the peak working budget")
	}
	encoded := make([]byte, length)
	copy(encoded, data[:layout.parametersStart])
	binary.BigEndian.PutUint32(encoded, uint32(length-4))
	encoded[21] &^= 16
	if hasParameters {
		encoded[21] |= 16
		copy(encoded[layout.parametersStart:], parameters)
	}
	copy(encoded[layout.parametersStart+parameterLength:], data[layout.parametersEnd:])
	flagsOffset, colorOffset := layout.flagsOffset, layout.colorOffset
	encoded[flagsOffset] &^= 7
	if settings.Relative {
		encoded[flagsOffset] |= 1
	}
	if settings.Disabled {
		encoded[flagsOffset] |= 2
	}
	if settings.Invert {
		encoded[flagsOffset] |= 4
	}
	encoded[colorOffset] = settings.DefaultColor
	if err := ctx.Err(); err != nil {
		return err
	}
	if bytes.Equal(encoded, data) {
		return nil
	}
	owned := clonePixelMaskSettings(settings)
	if err := ctx.Err(); err != nil {
		return err
	}
	node.maskData, node.pixelMask = encoded, &owned
	node.setSemanticCost("pixelMask", 512)
	e.markChangedLocked(false)
	return nil
}

func (e *Editor) readPixelMaskLocked(
	ctx context.Context, node *editNode,
) ([]byte, pixelMaskLayout, error) {
	data, err := e.readPixelMaskDataLocked(ctx, node)
	if err != nil {
		return nil, pixelMaskLayout{}, err
	}
	channels := e.pixelMaskChannelsLocked(node)
	vectorBlocks := e.pixelMaskVectorBlocksLocked(node)
	if len(channels) == 0 && vectorBlocks > 1 {
		return nil, pixelMaskLayout{}, editError(ErrUnsupported, "layers.pixelMask",
			"multiple vector-mask blocks have ambiguous mask ownership")
	}
	if len(data) >= 22 && data[21]&8 != 0 {
		if vectorBlocks != 1 {
			return nil, pixelMaskLayout{}, editError(ErrUnsupported, "layers.pixelMask",
				"generated coverage requires one unambiguous vector-mask block")
		}
	}
	layout, err := decodePixelMaskLayout(data, channels)
	return data, layout, err
}

func decodePixelMaskLayout(data []byte, channels []psdfile.ChannelRecord) (pixelMaskLayout, error) {
	hasReal := slices.ContainsFunc(channels, func(channel psdfile.ChannelRecord) bool { return channel.ID == -3 })
	layout, err := decodePixelMaskHeader(data, hasReal)
	if err != nil {
		return pixelMaskLayout{}, err
	}
	unsupported := func(detail string) (pixelMaskLayout, error) {
		return pixelMaskLayout{}, editError(ErrUnsupported, "layers.pixelMask", detail)
	}
	channelID := int16(-2)
	if data[21]&8 != 0 {
		if !hasReal {
			return unsupported("generated coverage has no real user-mask metadata")
		}
		channelID = -3
		layout.flagsOffset, layout.colorOffset = 22, 23
	} else if hasReal && data[22]&2 == 0 {
		return unsupported("two active user masks have ambiguous editing semantics")
	}
	count := 0
	for _, channel := range channels {
		if channel.ID == channelID {
			count++
		}
	}
	// A single ordinary mask can carry uniform default coverage without encoded sample channels.
	defaultCoverageOnly := len(channels) == 0 && channelID == -2 && !hasReal
	if count != 1 && !defaultCoverageOnly {
		return unsupported("effective user-mask channel is absent or duplicated")
	}
	flags := data[layout.flagsOffset]
	layout.settings.Disabled, layout.settings.Relative = flags&2 != 0, flags&1 != 0
	layout.settings.Invert, layout.settings.DefaultColor = flags&4 != 0, data[layout.colorOffset]
	return layout, nil
}

func decodePixelMaskHeader(data []byte, hasReal bool) (pixelMaskLayout, error) {
	layout := pixelMaskLayout{parametersStart: 22, parametersEnd: 22, flagsOffset: 21, colorOffset: 20}
	unsupported := func(detail string) (pixelMaskLayout, error) {
		return pixelMaskLayout{}, editError(ErrUnsupported, "layers.pixelMask", detail)
	}
	if len(data) < 22 || uint64(binary.BigEndian.Uint32(data)) != uint64(len(data)-4) {
		return unsupported("mask length or fixed metadata is malformed")
	}
	if hasReal {
		if len(data) < 40 {
			return unsupported("real mask header is truncated")
		}
		layout.parametersStart, layout.parametersEnd = 40, 40
	}
	start := layout.parametersStart
	if data[21]&16 != 0 {
		if len(data) <= start || data[start]&0xf0 != 0 {
			return unsupported("unknown or truncated mask parameters")
		}
		position := start + 1
		for index, flag := range []byte{1, 2, 4, 8} {
			if data[start]&flag == 0 {
				continue
			}
			length := 1
			if flag == 2 || flag == 8 {
				length = 8
			}
			if length > len(data)-position {
				return unsupported("mask parameters are truncated")
			}
			switch index {
			case 0:
				layout.settings.Density = new(data[position])
			case 1:
				layout.settings.Feather = new(math.Float64frombits(binary.BigEndian.Uint64(data[position:])))
			case 2:
				layout.settings.VectorDensity = new(data[position])
			case 3:
				layout.settings.VectorFeather = new(math.Float64frombits(binary.BigEndian.Uint64(data[position:])))
			}
			position += length
		}
		layout.parametersEnd = position
	}
	if !validPixelMaskFeather(layout.settings.Feather) || !validPixelMaskFeather(layout.settings.VectorFeather) {
		return unsupported("source mask feather is not finite and nonnegative")
	}
	return layout, nil
}

func pixelMaskParameters(settings PixelMaskSettings) []byte {
	data := make([]byte, 1, 19)
	if settings.Density != nil {
		data[0] |= 1
		data = append(data, *settings.Density)
	}
	if settings.Feather != nil {
		data[0] |= 2
		data = binary.BigEndian.AppendUint64(data, math.Float64bits(*settings.Feather))
	}
	if settings.VectorDensity != nil {
		data[0] |= 4
		data = append(data, *settings.VectorDensity)
	}
	if settings.VectorFeather != nil {
		data[0] |= 8
		data = binary.BigEndian.AppendUint64(data, math.Float64bits(*settings.VectorFeather))
	}
	return data
}

func validPixelMaskFeather(value *float64) bool {
	return value == nil || *value >= 0 && !math.IsNaN(*value) && !math.IsInf(*value, 0)
}

func equalPixelMaskSettings(first, second PixelMaskSettings) bool {
	return first.Disabled == second.Disabled && first.Relative == second.Relative && first.Invert == second.Invert &&
		first.DefaultColor == second.DefaultColor && equalPixelMaskValue(first.Density, second.Density) &&
		equalPixelMaskValue(first.Feather, second.Feather) &&
		equalPixelMaskValue(first.VectorDensity, second.VectorDensity) &&
		equalPixelMaskValue(first.VectorFeather, second.VectorFeather)
}

func equalPixelMaskValue[T comparable](first, second *T) bool {
	return first == nil && second == nil || first != nil && second != nil && *first == *second
}

func clonePixelMaskSettings(settings PixelMaskSettings) PixelMaskSettings {
	if settings.Density != nil {
		settings.Density = new(*settings.Density)
	}
	if settings.Feather != nil {
		settings.Feather = new(*settings.Feather)
	}
	if settings.VectorDensity != nil {
		settings.VectorDensity = new(*settings.VectorDensity)
	}
	if settings.VectorFeather != nil {
		settings.VectorFeather = new(*settings.VectorFeather)
	}
	return settings
}

func applyPixelMaskSettings(layer *psdmodel.Layer, settings *PixelMaskSettings) {
	if settings == nil {
		return
	}
	owned := clonePixelMaskSettings(*settings)
	layer.VectorDensity, layer.VectorFeather = owned.VectorDensity, owned.VectorFeather
	if layer.Mask == nil {
		return
	}
	mask := *layer.Mask
	mask.Disabled, mask.Relative, mask.Invert = owned.Disabled, owned.Relative, owned.Invert
	mask.DefaultColor, mask.Density, mask.Feather = new(owned.DefaultColor), owned.Density, owned.Feather
	mask.Unsupported = slices.DeleteFunc(slices.Clone(mask.Unsupported), func(issue string) bool {
		return issue == "暂不支持矢量蒙版羽化"
	})
	if owned.VectorFeather != nil && *owned.VectorFeather != 0 {
		mask.Unsupported = append(mask.Unsupported, "暂不支持矢量蒙版羽化")
	}
	layer.Mask = &mask
}
