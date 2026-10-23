package psdedit

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"maps"
	"math"
	"slices"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
	"github.com/gopkg-dev/psdkit/psdscene"
)

// SmartTransformOptions selects explicit source-color and projection policies for a single instance.
type SmartTransformOptions struct {
	// AssumeUntaggedSRGB permits source-profile conversion with explicitly assumed sRGB endpoints.
	AssumeUntaggedSRGB bool
	// ApproximatePlacedProjection permits mathematical sampling of transformed transparent source borders.
	// Photoshop pixel equivalence is not guaranteed for this explicitly selected approximation.
	ApproximatePlacedProjection bool
}

type smartTransformUpdate struct {
	node      *editNode
	placed    *psdmodel.Placed
	blocks    map[int64][]byte
	plan      *psdrender.ProjectionPlan
	identity  string
	cost      int64
	working   int64
	unchanged bool
}

// SetSmartObjectTransform changes one existing raster smart object's destination corners and saved artwork.
// Corners are top-left, top-right, bottom-right and bottom-left in parent-document coordinates.
// Shared source contents and all other instances remain unchanged. Only absent or inactive Warp is supported.
func (e *Editor) SetSmartObjectTransform(ctx context.Context, id LayerID, corners [4]psdmodel.Point) error {
	return e.SetSmartObjectTransformWithOptions(ctx, id, corners, nil, SmartTransformOptions{})
}

// SetSmartObjectTransformWithOptions changes one placement using explicit source and sampling policies.
// Embedded and previously updated contents need no resolver; unedited external contents require one.
// Resolver runs without the editor lock. Concurrent edits return ErrStaleLinkedEdit without committing.
// Masks, effects and opacity remain separate from the refreshed artwork. Failed edits commit nothing.
func (e *Editor) SetSmartObjectTransformWithOptions(
	ctx context.Context, id LayerID, corners [4]psdmodel.Point,
	resolver LinkedContentsResolver, options SmartTransformOptions,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	update, err := e.prepareSmartTransformLocked(ctx, id, corners)
	if err != nil || update.unchanged {
		return err
	}
	available := e.source.Limits().MaxDecodeBytes - e.ownedBytesLocked(nil) - update.working
	if available <= 0 {
		return editError(psdfile.ErrLimit, "smartObject.transform", "no source metadata budget remains")
	}
	linked, err := e.source.ReadLinkedFileView(ctx, psdfile.MetadataOptions{MaxBytes: available})
	if err != nil {
		return err
	}
	var selected *psdfile.LinkedFileRecord
	for index := range linked.Records {
		if linked.Records[index].ID != update.identity {
			continue
		}
		if selected != nil {
			return editError(ErrUnsupported, "smartObject.transform.source", "source identity has duplicate records")
		}
		selected = &linked.Records[index]
	}
	if selected == nil || !supportedSmartContentsType(selected.FileType) {
		return editError(ErrUnsupported, "smartObject.transform.source", "a supported raster source is required")
	}
	available -= linked.MetadataBytes
	var encoded []byte
	encodedCost := int64(0)
	if overlay := e.linkedOverlays[update.identity]; overlay != nil {
		encoded = overlay.data
	} else if selected.Kind == "liFD" && selected.HasData {
		if selected.Data.Length > available {
			return editError(psdfile.ErrLimit, "smartObject.transform.source", "encoded source exceeds working budget")
		}
		encoded, err = readSmartPayload(ctx, e.source, selected.Data)
		if err != nil {
			return err
		}
		encodedCost = int64(len(encoded))
	} else {
		if selected.Kind != "liFE" || resolver == nil {
			return editError(ErrUnsupported, "smartObject.transform.source", "external contents require an explicit resolver")
		}
		record, revision, source := *selected, e.revision, e.source
		e.mu.Unlock()
		data, resolveErr := func() ([]byte, error) {
			// Restore lock ownership even when a caller's resolver panics.
			defer e.mu.Lock()
			return resolver(ctx, record)
		}()
		if resolveErr != nil {
			return resolveErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.checkMutableLocked(); err != nil {
			return err
		}
		if e.revision != revision || e.source != source {
			return ErrStaleLinkedEdit
		}
		if int64(len(data)) > e.source.Limits().MaxFileBytes || int64(len(data)) > available {
			return editError(psdfile.ErrLimit, "smartObject.transform.source", "resolved source exceeds working budget")
		}
		encoded = make([]byte, len(data))
		copy(encoded, data)
		encodedCost = int64(len(encoded))
	}
	linked.Records, selected = nil, nil
	return e.renderSmartTransformLocked(ctx, update, encoded, encodedCost, options)
}

func (e *Editor) prepareSmartTransformLocked(
	ctx context.Context, id LayerID, corners [4]psdmodel.Point,
) (smartTransformUpdate, error) {
	var update smartTransformUpdate
	if err := e.checkMutableLocked(); err != nil {
		return update, err
	}
	if err := ctx.Err(); err != nil {
		return update, err
	}
	if err := e.rgbCompositeSupportedLocked(); err != nil {
		return update, err
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return update, err
	}
	if node.group || node.sourceIndex < 0 || node.sourceIndex >= len(e.source.Layers) {
		return update, editError(ErrUnsupported, "smartObject.transform", "an existing placed raster layer is required")
	}
	for channel := range node.planes {
		if channel >= -1 || channel < -3 {
			return update, editError(ErrUnsupported, "smartObject.transform.channels", "raster channel overlays are unsupported")
		}
	}
	values := make([]float64, 8)
	for index, point := range corners {
		values[index*2], values[index*2+1] = point.X, point.Y
	}
	if _, err := psdrender.PlacedCorners(&psdmodel.Placed{Transform: values}); err != nil {
		return update, errors.Join(ErrInvalidEdit, err)
	}
	limits := e.source.Limits()
	available := limits.MaxDecodeBytes - e.ownedBytesLocked(nil)
	if available <= 0 {
		return update, editError(psdfile.ErrLimit, "smartObject.transform", "no placement metadata budget remains")
	}
	view, err := e.source.ReadSmartObjectView(ctx, psdfile.MetadataOptions{MaxBytes: available})
	if err != nil {
		return update, err
	}
	records := view.Records[:0]
	for _, record := range view.Records {
		if record.LayerSetIndex != e.source.ActiveLayerSet || record.LayerIndex != node.sourceIndex {
			continue
		}
		if record.SourceType != 2 || len(records) != 0 && !sameSmartTransformPlacement(records[0], record) {
			return update, editError(ErrUnsupported, "smartObject.transform", "placement blocks have ambiguous semantics")
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return update, editError(ErrUnsupported, "smartObject.transform", "a modern placement block is required")
	}
	available -= view.MetadataBytes
	if available <= 0 {
		return update, editError(psdfile.ErrLimit, "smartObject.transform", "placement descriptors exceed working budget")
	}
	metadata, err := e.source.ReadLayerMetadata(ctx, node.sourceIndex, psdfile.MetadataOptions{MaxBytes: available})
	if err != nil {
		return update, err
	}
	placed := metadata.Layer.Placed
	if placed == nil || placed.Type != "raster" || len(placed.Unsupported) != 0 ||
		placed.Warp != nil && (placed.Warp.Style != "" && placed.Warp.Style != "none" ||
			psdrender.RequiresWarpRendering(placed)) {
		return update, editError(ErrUnsupported, "smartObject.transform.warp", "active Warp or smart filters are unsupported")
	}
	if node.placed != nil {
		placed = node.placed
	}
	available -= metadata.MetadataBytes
	placementCost := smartProjectionMetadataBytes(placed) + 1024
	maskCost := int64(512)
	if node.maskData == nil {
		maskCost += e.source.Layers[node.sourceIndex].MaskData.Length
	}
	if placementCost > available || maskCost > available-placementCost {
		return update, editError(psdfile.ErrLimit, "smartObject.transform", "placement copies exceed working budget")
	}
	update.node, update.identity = node, records[0].SourceID
	update.unchanged = slices.Equal(placed.Transform, values) &&
		(len(placed.NonAffineTransform) == 0 || slices.Equal(placed.NonAffineTransform, values))
	update.placed, update.cost = cloneSmartPlacement(placed, placed.Width, placed.Height)
	update.placed.Transform, update.placed.NonAffineTransform = values, nil
	update.plan, err = psdrender.CompileProjection(&psdmodel.Layer{Placed: update.placed},
		int(e.source.Header.Width), int(e.source.Header.Height), false)
	if err != nil {
		return smartTransformUpdate{}, errors.Join(ErrInvalidEdit, err)
	}
	bounds := image.Rect(update.plan.Left, update.plan.Top,
		update.plan.Left+update.plan.Width, update.plan.Top+update.plan.Height)
	if err := e.smartTransformGeometryLocked(ctx, node, bounds, update.unchanged); err != nil {
		return smartTransformUpdate{}, err
	}
	update.working = update.cost + 1024
	available -= update.working
	if available <= 0 {
		return smartTransformUpdate{}, editError(psdfile.ErrLimit,
			"smartObject.transform", "placement copies exceed working budget")
	}
	update.blocks = make(map[int64][]byte, len(records))
	for _, record := range records {
		payload, err := e.smartPlacementPayloadLocked(ctx, node, record.Block, available)
		if err != nil {
			return smartTransformUpdate{}, err
		}
		if err := patchSmartTransformDescriptor(record.Descriptor, payload, record.Block.Data, values); err != nil {
			return smartTransformUpdate{}, err
		}
		update.blocks[record.Block.Span.Offset] = payload
		update.working += int64(len(payload))
		available -= int64(len(payload))
	}
	for _, block := range e.source.Layers[node.sourceIndex].AdditionalInfo {
		if block.Key != "PlLd" && block.Key != "plLd" {
			continue
		}
		if block.Data.Length > available/33 {
			return smartTransformUpdate{}, editError(psdfile.ErrLimit,
				"smartObject.transform.legacy", "legacy descriptor exceeds working budget")
		}
		payload, err := e.smartPlacementPayloadLocked(ctx, node, block, available)
		if err != nil {
			return smartTransformUpdate{}, err
		}
		if err := patchSmartTransformLegacy(ctx, e.source, block, payload, records[0].Transform, values); err != nil {
			return smartTransformUpdate{}, err
		}
		update.blocks[block.Span.Offset] = payload
		update.working += int64(len(payload))
		available -= int64(len(payload))
	}
	if err := ctx.Err(); err != nil {
		return smartTransformUpdate{}, err
	}
	return update, nil
}

func sameSmartTransformPlacement(first, second psdfile.SmartObjectRecord) bool {
	return first.SourceID == second.SourceID && first.SourceType == second.SourceType &&
		first.Width == second.Width && first.Height == second.Height &&
		slices.Equal(first.Transform, second.Transform) && slices.Equal(first.NonAffine, second.NonAffine)
}

func (e *Editor) smartPlacementPayloadLocked(
	ctx context.Context, node *editNode, block psdfile.AdditionalBlock, available int64,
) ([]byte, error) {
	if block.Data.Length > min(e.source.Limits().MaxMetadataBytes, available) {
		return nil, editError(psdfile.ErrLimit, "smartObject.transform.metadata", "placement payload exceeds working budget")
	}
	if payload, exists := node.additional[block.Span.Offset]; exists {
		if int64(len(payload)) != block.Data.Length {
			return nil, editError(ErrUnsupported, "smartObject.transform.metadata", "placement layout changed incompatibly")
		}
		owned := make([]byte, len(payload))
		copy(owned, payload)
		return owned, ctx.Err()
	}
	return readSmartPayload(ctx, e.source, block.Data)
}

func patchSmartTransformDescriptor(
	descriptor *psdfile.Descriptor, payload []byte, span psdfile.Span, values []float64,
) error {
	if descriptor == nil {
		return editError(ErrUnsupported, "smartObject.transform.metadata", "placement descriptor is absent")
	}
	if err := validateSmartDuplicates(descriptor.Entries, payload, span,
		"Trnf", "nonAffineTransform", "Idnt", "Type", "Sz  ", "warp"); err != nil {
		return err
	}
	found := false
	for _, entry := range descriptor.Entries {
		switch entry.Key {
		case "quiltWarp", "filterFX":
			return editError(ErrUnsupported, "smartObject.transform.metadata", "quilt and filter stacks are unsupported")
		case "warp":
			warp, ok := entry.Value.(*psdfile.Descriptor)
			if !ok || warp == nil {
				return editError(ErrUnsupported, "smartObject.transform.warp", "invalid warp descriptor")
			}
			if err := validateSmartTransformWarp(warp, payload, span); err != nil {
				return err
			}
		case "Trnf", "nonAffineTransform":
			items, ok := entry.Value.([]psdfile.DescriptorValue)
			if !ok || entry.Type != "VlLs" || len(items) != 8 {
				return editError(ErrUnsupported, "smartObject.transform.metadata", "eight transform coordinates are required")
			}
			for index, item := range items {
				field := psdfile.DescriptorEntry{Type: item.Type, Value: item.Value, Data: item.Data}
				if _, valid := smartScalar(field); !valid {
					return editError(ErrUnsupported, "smartObject.transform.metadata", "source coordinates are not finite")
				}
				if unit, ok := item.Value.(psdfile.DescriptorUnit); ok && unit.Unit != "#Pxl" {
					return editError(ErrUnsupported, "smartObject.transform.metadata", "coordinates require pixel units")
				}
				if item.Type == "UnFl" && float64(float32(values[index])) != values[index] {
					return editError(ErrUnsupported, "smartObject.transform.metadata",
						"requested coordinate cannot be represented exactly by the source float32 field")
				}
				if err := patchSmartScalar(payload, span, field, values[index]); err != nil {
					return err
				}
			}
			found = found || entry.Key == "Trnf"
		}
	}
	if !found {
		return editError(ErrUnsupported, "smartObject.transform.metadata", "transform coordinates are absent")
	}
	return nil
}

func validateSmartTransformWarp(warp *psdfile.Descriptor, payload []byte, span psdfile.Span) error {
	if err := validateSmartDuplicates(warp.Entries, payload, span,
		"warpStyle", "warpValue", "warpPerspective", "warpPerspectiveOther"); err != nil {
		return err
	}
	for _, entry := range warp.Entries {
		switch entry.Key {
		case "warpStyle":
			style, ok := entry.Value.(psdfile.DescriptorEnum)
			if !ok || style.TypeID != "warpStyle" || style.ValueID != "warpNone" {
				return editError(ErrUnsupported, "smartObject.transform.warp", "only inactive Warp is supported")
			}
		case "warpValue", "warpPerspective", "warpPerspectiveOther":
			value, ok := smartScalar(entry)
			if !ok || value != 0 {
				return editError(ErrUnsupported, "smartObject.transform.warp", "active Warp controls are unsupported")
			}
		case "customEnvelopeWarp", "quiltWarp", "deformNumRows", "deformNumCols":
			return editError(ErrUnsupported, "smartObject.transform.warp", "custom and quilt Warp are unsupported")
		}
	}
	return nil
}

func patchSmartTransformLegacy(ctx context.Context, source *psdfile.Document, block psdfile.AdditionalBlock,
	payload []byte, original, values []float64,
) error {
	header, err := smartLegacyHeader(payload)
	if err != nil {
		return err
	}
	warp, err := source.DecodeDescriptorContext(ctx, psdfile.Span{
		Offset: block.Data.Offset + int64(header), Length: block.Data.Length - int64(header),
	}, true)
	if err != nil {
		return err
	}
	if err := validateSmartTransformWarp(warp, payload, block.Data); err != nil {
		return err
	}
	start := header - 68
	// Validate the original legacy coordinates independently of a previous editor overlay.
	encoded, err := readSmartPayload(ctx, source, psdfile.Span{Offset: block.Data.Offset + int64(start), Length: 64})
	if err != nil {
		return err
	}
	for index, value := range values {
		if math.Float64frombits(binary.BigEndian.Uint64(encoded[index*8:])) != original[index] {
			return editError(ErrUnsupported, "smartObject.transform.legacy", "legacy and modern transforms disagree")
		}
		binary.BigEndian.PutUint64(payload[start+index*8:], math.Float64bits(value))
	}
	return nil
}

func (e *Editor) smartTransformGeometryLocked(
	ctx context.Context, node *editNode, bounds image.Rectangle, unchanged bool,
) error {
	layer := e.source.Layers[node.sourceIndex]
	for _, block := range layer.AdditionalInfo {
		switch block.Key {
		case "TySh", "tySh", "SoCo", "GdFl", "PtFl", "vscg", "vstk", "PxSc", "PxSD",
			"brit", "levl", "curv", "expA", "vibA", "hue ", "hue2", "blnc", "blwh", "phfl", "mixr",
			"clrL", "nvrt", "post", "thrs", "grdm", "selc", "CgEd":
			return editError(ErrUnsupported, "smartObject.transform", "incompatible semantic source block")
		}
	}
	var colors [3]int
	alpha := 0
	for _, channel := range layer.Channels {
		switch {
		case channel.ID >= 0 && channel.ID < 3:
			colors[channel.ID]++
		case channel.ID == -1:
			alpha++
		case channel.ID == -2 || channel.ID == -3:
		default:
			return editError(ErrUnsupported, "smartObject.transform.channels", "unknown raster channels are unsupported")
		}
	}
	if colors != [3]int{1, 1, 1} || alpha > 1 || layer.Divider != 0 {
		return editError(ErrUnsupported, "smartObject.transform.channels", "unambiguous RGB channels are required")
	}
	state, err := e.pixelMaskStateLocked(ctx, node)
	if err != nil {
		return err
	}
	if unchanged || bounds.Min == node.bounds.Min {
		return nil
	}
	if state.hasUser && (state.layout.settings.Relative ||
		len(state.data) > state.layout.flagsOffset && state.data[state.layout.flagsOffset]&1 != 0) {
		return editError(ErrUnsupported, "smartObject.transform.mask", "moving layer-relative user masks is unsupported")
	}
	// Generated vector coverage retains its own source-relative flag, separate from the edited user mask.
	if len(state.data) >= 22 && state.generated && state.data[21]&1 != 0 {
		return editError(ErrUnsupported, "smartObject.transform.mask", "moving layer-relative generated masks is unsupported")
	}
	return nil
}

func (e *Editor) renderSmartTransformLocked(ctx context.Context, update smartTransformUpdate,
	encoded []byte, encodedCost int64, options SmartTransformOptions,
) error {
	limits := e.source.Limits()
	working := e.ownedBytesLocked(nil) + update.working + encodedCost
	content, err := decodeSmartContentsSource(ctx, encoded, limits, limits.MaxDecodeBytes-working)
	if err != nil {
		return err
	}
	defer func() { _ = content.close() }()
	if content.width != update.placed.Width || content.height != update.placed.Height {
		return editError(ErrUnsupported, "smartObject.transform.source", "source dimensions differ; update contents first")
	}
	working += content.retainedBytes
	parentProfile, err := psdscene.ReadSourceRGBProfile(ctx, e.source, limits.MaxDecodeBytes-working)
	if err != nil {
		return err
	}
	working += int64(len(parentProfile))
	pixels, err := content.render(ctx, parentProfile, options.AssumeUntaggedSRGB, limits.MaxDecodeBytes-working)
	if err != nil {
		return err
	}
	working -= content.retainedBytes + int64(len(parentProfile))
	if err := content.close(); err != nil {
		return err
	}
	working += int64(len(pixels.Pix))
	area := int64(update.plan.Width) * int64(update.plan.Height)
	if area > limits.MaxDecodePixels || area > (limits.MaxDecodeBytes-working)/4 {
		return editError(psdfile.ErrLimit, "smartObject.transform.pixels", "projected artwork exceeds working budget")
	}
	projected, origin, err := psdrender.ProjectPlacedContentsWithOptions(ctx,
		&psdmodel.Layer{Placed: update.placed}, pixels, update.plan,
		psdrender.PlacedSamplingOptions{ApproximateTransparentBorder: options.ApproximatePlacedProjection})
	if err != nil {
		return smartProjectionError(err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := e.source.ReadAt(nil, 0); err != nil {
		return err
	}
	node := update.node
	if node.additional == nil {
		node.additional = make(map[int64][]byte, len(update.blocks))
	}
	maps.Copy(node.additional, update.blocks)
	node.placed, node.pixels = update.placed, projected
	node.bounds = image.Rectangle{Min: origin, Max: origin.Add(projected.Rect.Size())}
	node.setSemanticCost("placed", update.cost)
	e.markChangedLocked(false)
	return nil
}
