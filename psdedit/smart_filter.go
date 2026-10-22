package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// SmartFilterEditOptions supplies accurate replacement pixels for the edited smart-filter layer.
// Pixels represents the projected, filtered artwork before layer opacity, pixel/vector masks and layer effects.
// Its rectangle is the new absolute parent-document layer rectangle, including any transparent padding.
// The caller ensures artwork agrees with the requested parameters; the library checks ownership and shape.
type SmartFilterEditOptions struct {
	Pixels *image.NRGBA
}

// SetSmartFilterParameters changes existing Gaussian controls and their saved source artwork atomically.
// Accurate caller pixels are required because changing metadata does not refresh Photoshop's saved layer raster.
// Filter families, count, order, blending, placement geometry, linked contents and unknown metadata are preserved.
// Radius, filter enabled/opacity and whole-stack enabled are editable; failed edits leave overlays unchanged.
func (e *Editor) SetSmartFilterParameters(
	ctx context.Context, id LayerID, stack psdfilter.Stack, options SmartFilterEditOptions,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.checkMutableLocked(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.checkRGBLocked(); err != nil {
		return err
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return err
	}
	if options.Pixels == nil {
		return editError(ErrUnsupported, "layers.smartFilters.pixels", "accurate replacement filter pixels are required")
	}
	block, err := e.smartFilterEditBlock(node)
	if err != nil {
		return err
	}
	if err := e.textEditGeometry(node, options.Pixels.Rect); err != nil {
		return err
	}
	limits := e.source.Limits()
	available := limits.MaxDecodeBytes - e.ownedBytesLocked(nil)
	pixelBytes, err := textEditPixelBytes(options.Pixels, limits, available)
	if err != nil {
		return err
	}
	work := available - pixelBytes - 4096
	if work <= 0 {
		return editError(psdfile.ErrLimit, "layers.smartFilters", "replacement pixels consume the working budget")
	}
	bridgeBudget := min(limits.MaxMetadataBytes, work/4)
	payload := node.additional[block.Span.Offset]
	if payload == nil {
		if block.Data.Length > bridgeBudget/8 {
			return editError(psdfile.ErrLimit, "layers.smartFilters", "placement payload exceeds the peak working budget")
		}
		payload, err = readSmartPayload(ctx, e.source, block.Data)
		if err != nil {
			return err
		}
	}
	bridge, bridgeBlock, err := smartFilterEditSource(ctx, payload, block.Key, limits, bridgeBudget)
	if err != nil {
		return err
	}
	defer func() { _ = bridge.Close() }()
	encoded, err := bridge.EncodeSmartFilterParameters(ctx, bridgeBlock, stack,
		psdfile.MetadataOptions{MaxBytes: min(limits.MaxMetadataBytes, work/4)})
	if err != nil {
		return err
	}
	if bytes.Equal(payload, encoded) && node.pixels != nil && node.bounds == options.Pixels.Rect &&
		textEditPixelsEqual(ctx, node.pixels, options.Pixels) {
		return ctx.Err()
	}
	placed, placedCost, err := smartFilterEditedPlacement(ctx, encoded, block.Key, limits, work/4)
	if err != nil {
		return err
	}
	previous := node.additional[block.Span.Offset]
	retained := e.ownedBytesLocked(nil) - int64(len(previous)) - node.semanticCosts["placed"]
	if node.pixels != nil {
		retained -= int64(cap(node.pixels.Pix))
	}
	if retained < 0 || pixelBytes+int64(cap(encoded))+placedCost > limits.MaxDecodeBytes-retained {
		return editError(psdfile.ErrLimit, "layers.smartFilters", "owned parameter and pixel overlays exceed the budget")
	}
	owned := image.NewNRGBA(image.Rect(0, 0, options.Pixels.Rect.Dx(), options.Pixels.Rect.Dy()))
	for row := range owned.Rect.Dy() {
		for column := 0; column < owned.Stride; column += 32 << 10 {
			if err := ctx.Err(); err != nil {
				return err
			}
			end := min(column+32<<10, owned.Stride)
			copy(owned.Pix[row*owned.Stride+column:row*owned.Stride+end],
				options.Pixels.Pix[row*options.Pixels.Stride+column:row*options.Pixels.Stride+end])
		}
	}
	if _, err := e.source.ReadAt(nil, 0); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if node.additional == nil {
		node.additional = make(map[int64][]byte)
	}
	node.additional[block.Span.Offset] = encoded
	node.pixels, node.bounds, node.placed = owned, options.Pixels.Rect, placed
	node.setSemanticCost("placed", placedCost)
	e.markChangedLocked(false)
	return nil
}

func (e *Editor) smartFilterEditBlock(node *editNode) (psdfile.AdditionalBlock, error) {
	var result psdfile.AdditionalBlock
	if node.group || node.sourceIndex < 0 || node.sourceIndex >= len(e.source.Layers) || len(node.planes) != 0 {
		return result, editError(ErrUnsupported, "layers.smartFilters",
			"an existing placed raster without channel overlays is required")
	}
	layer := e.source.Layers[node.sourceIndex]
	var colors [3]int
	alpha, count := 0, 0
	for _, channel := range layer.Channels {
		if channel.ID >= 0 && channel.ID < 3 {
			colors[channel.ID]++
		}
		if channel.ID == -1 {
			alpha++
		}
	}
	if colors != [3]int{1, 1, 1} || alpha > 1 || layer.Divider != 0 {
		return result, editError(ErrUnsupported, "layers.smartFilters.channels",
			"unambiguous original RGB channels are required")
	}
	for _, block := range layer.AdditionalInfo {
		switch block.Key {
		case "SoLd", "SoLE":
			result, count = block, count+1
		case "TySh", "tySh", "SoCo", "GdFl", "PtFl", "vscg", "vstk", "PxSc", "PxSD",
			"brit", "levl", "curv", "expA", "vibA", "hue ", "hue2", "blnc", "blwh", "phfl", "mixr",
			"clrL", "nvrt", "post", "thrs", "grdm", "selc", "CgEd":
			return result, editError(ErrUnsupported, "layers.smartFilters", "incompatible semantic source block")
		}
	}
	if count != 1 {
		return result, editError(ErrUnsupported, "layers.smartFilters", "one unambiguous modern placement block is required")
	}
	return result, nil
}

func smartFilterEditSource(
	ctx context.Context, payload []byte, key string, limits psdfile.Limits, budget int64,
) (*psdfile.Document, psdfile.AdditionalBlock, error) {
	var empty psdfile.AdditionalBlock
	padding := len(payload) % 2
	length := int64(len(payload)) + int64(padding) + 111
	if length > budget/8 || length > math.MaxUint32 || uint64(length) > uint64(^uint(0)>>1) {
		return nil, empty, editError(psdfile.ErrLimit, "layers.smartFilters", "placement envelope exceeds the working budget")
	}
	data := make([]byte, int(length))
	copy(data, "8BPS")
	binary.BigEndian.PutUint16(data[4:], 1)
	binary.BigEndian.PutUint16(data[12:], 3)
	binary.BigEndian.PutUint32(data[14:], 1)
	binary.BigEndian.PutUint32(data[18:], 1)
	binary.BigEndian.PutUint16(data[22:], 8)
	binary.BigEndian.PutUint16(data[24:], 3)
	binary.BigEndian.PutUint32(data[34:], uint32(68+len(payload)+padding))
	binary.BigEndian.PutUint32(data[38:], uint32(60+len(payload)+padding))
	binary.BigEndian.PutUint16(data[42:], 1)
	binary.BigEndian.PutUint32(data[52:], 1)
	binary.BigEndian.PutUint32(data[56:], 1)
	copy(data[62:], "8BIMnorm")
	data[70], data[72] = 255, 8
	binary.BigEndian.PutUint32(data[74:], uint32(24+len(payload)+padding))
	copy(data[90:], "8BIM"+key)
	binary.BigEndian.PutUint32(data[98:], uint32(len(payload)))
	copy(data[102:], payload)
	limits.MaxMetadataBytes = min(limits.MaxMetadataBytes, budget/8)
	limits.MaxDecodeBytes = min(limits.MaxDecodeBytes, budget)
	source, err := psdfile.Read(gradientEditReader{ctx: ctx, reader: bytes.NewReader(data)}, length,
		psdfile.LoadOptions{Limits: limits})
	if err != nil {
		return nil, empty, err
	}
	if len(source.Layers) != 1 || len(source.Layers[0].AdditionalInfo) != 1 {
		_ = source.Close()
		return nil, empty, editError(ErrInvalidEdit, "layers.smartFilters", "placement envelope is structurally inconsistent")
	}
	return source, source.Layers[0].AdditionalInfo[0], nil
}

func smartFilterEditedPlacement(
	ctx context.Context, payload []byte, key string, limits psdfile.Limits, budget int64,
) (*psdmodel.Placed, int64, error) {
	bridge, _, err := smartFilterEditSource(ctx, payload, key, limits, budget)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = bridge.Close() }()
	snapshot, err := bridge.Decode(ctx)
	if err != nil {
		return nil, 0, err
	}
	if len(snapshot.Layers) != 1 || snapshot.Layers[0].Placed == nil {
		return nil, 0, editError(ErrInvalidEdit, "layers.smartFilters", "updated placement has no owned semantic view")
	}
	placed := snapshot.Layers[0].Placed
	filtered := placed.Unsupported[:0]
	for _, reason := range placed.Unsupported {
		if reason != "暂不支持智能滤镜" {
			filtered = append(filtered, reason)
		}
	}
	placed.Unsupported = filtered
	cost := int64(1024+len(placed.ID)+len(placed.Type)) +
		int64(cap(placed.Transform)+cap(placed.NonAffineTransform))*8
	if placed.Warp != nil {
		cost += 512 + int64(cap(placed.Warp.Values))*8 + int64(cap(placed.Warp.MeshPoints))*16 +
			warpMetadataStringBytes(placed.Warp) + warpQuiltMetadataBytes(placed.Warp)
	}
	for _, reason := range placed.Unsupported {
		cost += int64(len(reason))*2 + 64
	}
	if cost > budget {
		return nil, 0, editError(psdfile.ErrLimit, "layers.smartFilters",
			"updated placement semantics exceed the working budget")
	}
	return placed, cost, nil
}
