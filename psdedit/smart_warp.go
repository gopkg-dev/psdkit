package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"math"
	"slices"
	"strings"

	"github.com/gopkg-dev/psdkit/internal/psdplaced"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdscene"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

// SmartWarpEditOptions selects explicit live source and projection policies for an existing Warp edit.
// Nil Render uses strict supported embedded contents. Approximation and external resolvers are never inferred.
// Policies apply to this edit only; callers must independently select any approximate save-preview policy.
type SmartWarpEditOptions struct {
	// Render supplies explicit source resolvers, supported filters and independently selected approximation policies.
	// Cached-source fallbacks and output-profile conversion are unsupported; artwork retains the parent encoding.
	Render *psdscene.Options
}

type smartWarpEdit struct {
	node        *editNode
	source      *psdfile.Document
	revision    uint64
	block       psdfile.AdditionalBlock
	encoded     []byte
	placed      *psdmodel.Placed
	placedCost  int64
	prepared    psdplaced.Prepared
	settings    psdscene.Options
	retained    int64
	samePayload bool
}

// SetSmartObjectWarp replaces the control points of one existing supported custom raster placement.
// Points retain the existing row-major order and source-coordinate pixel units; bounds and placement corners remain.
// Current contents and supported filters rebuild unmasked source pixels; shared contents and peer instances remain.
// Existing uniform 10x10 and 13x13 grids require explicit Render.ApproximateCustomWarp; their slice schema remains.
// Presets, irregular quilts, legacy mirrors, folds and ambiguous descriptors are unsupported and commit nothing.
// Rendering and explicit resolver callbacks run without the editor lock; intervening edits return ErrStaleLinkedEdit.
func (e *Editor) SetSmartObjectWarp(
	ctx context.Context, id LayerID, points []psdmodel.Point, options SmartWarpEditOptions,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	stage, err := e.prepareSmartWarpLocked(ctx, id, points, options.Render)
	e.mu.Unlock()
	if err != nil {
		return err
	}
	return e.applySmartWarpStage(ctx, stage)
}

func (e *Editor) applySmartWarpStage(ctx context.Context, stage *smartWarpEdit) error {
	defer func() { _ = stage.prepared.Candidate.Close() }()
	layer, err := psdscene.RenderPreparedPlacedLayer(ctx, stage.prepared, stage.settings)
	if err != nil {
		return compositeRenderError(err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.checkMutableLocked(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.source != stage.source || e.revision != stage.revision {
		return ErrStaleLinkedEdit
	}
	if layer == nil || layer.Pixels == nil || layer.Bounds.Empty() ||
		layer.Bounds.Size() != layer.Pixels.Rect.Size() {
		return editError(ErrUnsupported, "smartObject.warp.pixels", "nonempty matching source artwork is required")
	}
	limits := e.source.Limits()
	current := e.ownedBytesLocked(nil)
	remaining := limits.MaxDecodeBytes - current - stage.retained
	geometry := *layer.Pixels
	geometry.Rect = layer.Bounds
	if remaining <= 0 || int64(cap(layer.Pixels.Pix)) > remaining {
		return editError(psdfile.ErrLimit, "smartObject.warp.pixels", "artwork exceeds the retained peak budget")
	}
	if _, err := textEditPixelBytes(&geometry, limits, remaining); err != nil {
		return err
	}
	if err := e.smartTransformGeometryLocked(ctx, stage.node, layer.Bounds, false); err != nil {
		return err
	}
	if stage.samePayload && stage.node.pixels != nil && stage.node.bounds == layer.Bounds &&
		textEditPixelsEqual(ctx, stage.node.pixels, layer.Pixels) {
		if _, err := e.source.ReadAt(nil, 0); err != nil {
			return err
		}
		return ctx.Err()
	}
	previous := stage.node.additional[stage.block.Span.Offset]
	retained := current - int64(len(previous)) - stage.node.semanticCosts["placed"]
	if stage.node.pixels != nil {
		retained -= int64(len(stage.node.pixels.Pix))
	}
	newCost := int64(cap(stage.encoded)) + stage.placedCost + int64(cap(layer.Pixels.Pix))
	if retained < 0 || newCost > limits.MaxDecodeBytes-retained {
		return editError(psdfile.ErrLimit, "smartObject.warp", "parameter and artwork overlays exceed budget")
	}
	if _, err := e.source.ReadAt(nil, 0); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	owned := *layer.Pixels
	owned.Rect = image.Rect(0, 0, layer.Bounds.Dx(), layer.Bounds.Dy())
	if stage.node.additional == nil {
		stage.node.additional = make(map[int64][]byte)
	}
	stage.node.additional[stage.block.Span.Offset] = stage.encoded
	stage.node.pixels, stage.node.bounds, stage.node.placed = &owned, layer.Bounds, stage.placed
	stage.node.setSemanticCost("placed", stage.placedCost+int64(cap(stage.encoded)-len(stage.encoded))+
		int64(cap(owned.Pix)-len(owned.Pix)))
	e.markChangedLocked(false)
	return nil
}

func (e *Editor) prepareSmartWarpLocked(
	ctx context.Context, id LayerID, points []psdmodel.Point, provided *psdscene.Options,
) (*smartWarpEdit, error) {
	if err := e.checkMutableLocked(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.rgbCompositeSupportedLocked(); err != nil {
		return nil, err
	}
	if len(points) != 16 && len(points) != 100 && len(points) != 169 {
		return nil, editError(ErrInvalidEdit, "smartObject.warp.mesh", "provide the existing 16, 100 or 169 control points")
	}
	for _, point := range points {
		if !smartFinite(point.X) || !smartFinite(point.Y) {
			return nil, editError(ErrInvalidEdit, "smartObject.warp.mesh", "control points must be finite")
		}
	}
	return e.prepareSmartWarpCandidateLocked(ctx, id, provided, false,
		func(descriptor *psdfile.Descriptor, payload []byte, span psdfile.Span) error {
			return patchSmartWarpMesh(descriptor, payload, span, points)
		})
}

func (e *Editor) prepareSmartWarpCandidateLocked(
	ctx context.Context, id LayerID, provided *psdscene.Options, preset bool,
	patch func(*psdfile.Descriptor, []byte, psdfile.Span) error,
) (*smartWarpEdit, error) {
	if err := e.checkMutableLocked(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.rgbCompositeSupportedLocked(); err != nil {
		return nil, err
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return nil, err
	}
	block, err := e.smartWarpEditBlock(node)
	if err != nil {
		return nil, err
	}
	limits := e.source.Limits()
	available := limits.MaxDecodeBytes - e.ownedBytesLocked(nil)
	settings, policyBytes, err := ownEffectsPlacedPolicy(provided, available)
	if err != nil {
		if classified, ok := errors.AsType[*psdfile.Error](err); ok {
			return nil, editError(classified.Kind,
				strings.Replace(classified.Path, "layers.effects", "smartObject.warp", 1), classified.Detail)
		}
		return nil, err
	}
	if settings.MaxWorkingBytes != 0 {
		available = min(available, settings.MaxWorkingBytes)
	}
	length := block.Data.Length
	current := node.additional[block.Span.Offset]
	if current != nil {
		length = int64(len(current))
	}
	if available <= policyBytes || length > min(limits.MaxMetadataBytes, (available-policyBytes)/32) {
		return nil, editError(psdfile.ErrLimit, "smartObject.warp.metadata", "current placement exceeds candidate budget")
	}
	var encoded []byte
	if current != nil {
		encoded = slices.Clone(current)
	} else {
		encoded, err = readSmartPayload(ctx, e.source, block.Data)
		if err != nil {
			return nil, err
		}
	}
	bridge, bridgeBlock, err := smartFilterEditSource(ctx, encoded, block.Key, limits, available-policyBytes)
	if err != nil {
		return nil, err
	}
	view, err := bridge.ReadSmartObjectView(ctx, psdfile.MetadataOptions{MaxBytes: (available - policyBytes) / 4})
	if err == nil && (len(view.Records) != 1 || view.Records[0].SourceType != 2) {
		err = editError(ErrUnsupported, "smartObject.warp", "one current modern raster placement is required")
	}
	if err == nil {
		err = patch(view.Records[0].Descriptor, encoded, bridgeBlock.Data)
	}
	_ = bridge.Close()
	if err != nil {
		return nil, err
	}
	placed, placedCost, err := smartFilterEditedPlacement(ctx, encoded, block.Key, limits, available/4)
	if err != nil {
		return nil, err
	}
	compile := psdplaced.CompileWarp
	if preset {
		compile = psdplaced.CompilePresetWarp
	}
	if _, err := compile(ctx, placed, settings.ApproximateCustomWarp, psdwarp.Options{
		MaxBytes: available / 4, MaxPixels: limits.MaxDecodePixels,
	}); err != nil {
		return nil, smartProjectionError(err)
	}
	candidate, _, err := smartFilterEditSource(ctx, encoded, block.Key, limits, available-policyBytes)
	if err != nil {
		return nil, err
	}
	stage := &smartWarpEdit{node: node, source: e.source, revision: e.revision, block: block,
		encoded: encoded, placed: placed, placedCost: placedCost, settings: settings,
		samePayload: bytes.Equal(current, encoded),
		prepared:    psdplaced.Prepared{Source: e.source, Candidate: candidate, LayerIndex: node.sourceIndex}}
	parent, err := e.source.ReadLayerMetadata(ctx, node.sourceIndex,
		psdfile.MetadataOptions{MaxBytes: available / 4})
	if err != nil {
		_ = candidate.Close()
		return nil, err
	}
	if parent.Layer == nil {
		_ = candidate.Close()
		return nil, editError(ErrUnsupported, "smartObject.warp.source", "original parent source metadata is absent")
	}
	stage.prepared.Effects = parent.Layer.Effects
	if node.effects != nil {
		stage.prepared.Effects = node.effects
	}
	activeEffects := false
	if effects := stage.prepared.Effects; effects != nil && effects.Enabled && effects.EffectsVisible {
		for _, effect := range effects.Items {
			activeEffects = activeEffects || effect.Enabled && effect.Opacity != 0
		}
	}
	if effects := stage.prepared.Effects; activeEffects &&
		!effectsPureColorOverlay(*effects) &&
		(!settings.Compose.ApproximateDropShadow ||
			!effectsPureDropShadow(*effects) && !effectsShadowOverlay(*effects)) {
		_ = candidate.Close()
		return nil, editError(ErrUnsupported, "smartObject.warp.effects",
			"source-neighborhood effects require a supported explicit shadow policy")
	}
	stage.prepared.EffectsApproximateDropShadow = settings.Compose.ApproximateDropShadow
	maskBytes := e.source.Layers[node.sourceIndex].MaskData.Length
	if node.maskData != nil {
		maskBytes = int64(len(node.maskData))
	}
	stage.retained = policyBytes + int64(cap(encoded)+len(encoded)+len(encoded)%2+111) +
		candidate.IndexMetadataBytes() + placedCost + parent.MetadataBytes + view.MetadataBytes + maskBytes +
		int64(len(e.source.Layers[node.sourceIndex].Channels))*256 + 4096
	if replacement := e.linkedOverlays[placed.ID]; replacement != nil {
		if len(replacement.data) == 0 || !supportedSmartContentsType(replacement.fileType) {
			_ = candidate.Close()
			return nil, editError(ErrUnsupported, "smartObject.warp.source", "current replacement source is unsupported")
		}
		if stage.retained >= available || int64(len(replacement.data)) > available-stage.retained {
			_ = candidate.Close()
			return nil, editError(psdfile.ErrLimit, "smartObject.warp.source", "replacement source exceeds candidate budget")
		}
		stage.prepared.Contents = slices.Clone(replacement.data)
		stage.prepared.FileType = replacement.fileType
		stage.retained += int64(cap(stage.prepared.Contents))
	}
	if stage.retained >= available {
		_ = candidate.Close()
		return nil, editError(psdfile.ErrLimit, "smartObject.warp", "candidate consumes the source rendering budget")
	}
	stage.settings.MaxWorkingBytes = available - stage.retained
	return stage, nil
}

func (e *Editor) smartWarpEditBlock(node *editNode) (psdfile.AdditionalBlock, error) {
	for channel := range node.planes {
		if channel != -2 && channel != -3 {
			return psdfile.AdditionalBlock{}, editError(ErrUnsupported, "smartObject.warp.channels",
				"raw source channel overlays cannot accompany a controlled Warp edit")
		}
	}
	// Mask channel edits retain independent geometry and do not replace the placed source artwork.
	qualified := *node
	qualified.planes = nil
	block, err := e.smartFilterEditBlock(&qualified)
	if err != nil {
		return block, err
	}
	for _, sourceBlock := range e.source.Layers[node.sourceIndex].AdditionalInfo {
		if sourceBlock.Key == "PlLd" || sourceBlock.Key == "plLd" {
			return block, editError(ErrUnsupported, "smartObject.warp.legacy",
				"legacy placement mirrors require a separately supported synchronized edit")
		}
	}
	return block, nil
}

// patchSmartWarpMesh changes only the existing float64 sample bytes, retaining every descriptor boundary and tail.
func patchSmartWarpMesh(
	descriptor *psdfile.Descriptor, payload []byte, span psdfile.Span, points []psdmodel.Point,
) error {
	if descriptor == nil || len(points) != 16 && len(points) != 100 && len(points) != 169 {
		return editError(ErrUnsupported, "smartObject.warp.mesh", "one existing supported control-point envelope is required")
	}
	var warp *psdfile.Descriptor
	for _, entry := range descriptor.Entries {
		if entry.Key != "warp" && entry.Key != "quiltWarp" {
			continue
		}
		candidate, valid := entry.Value.(*psdfile.Descriptor)
		if warp != nil || !valid || candidate == nil || entry.Type != "Objc" && entry.Type != "GlbO" {
			return editError(ErrUnsupported, "smartObject.warp.mesh", "one unambiguous custom Warp descriptor is required")
		}
		warp = candidate
	}
	if warp == nil {
		return editError(ErrUnsupported, "smartObject.warp.mesh", "an existing custom Warp descriptor is required")
	}
	var envelope *psdfile.Descriptor
	for _, entry := range warp.Entries {
		if entry.Key != "customEnvelopeWarp" {
			continue
		}
		candidate, valid := entry.Value.(*psdfile.Descriptor)
		if envelope != nil || !valid || candidate == nil || entry.Type != "Objc" && entry.Type != "GlbO" {
			return editError(ErrUnsupported, "smartObject.warp.mesh", "one custom envelope is required")
		}
		envelope = candidate
	}
	if envelope == nil {
		return editError(ErrUnsupported, "smartObject.warp.mesh", "custom envelope is absent")
	}
	var mesh *psdfile.DescriptorObjectArray
	for _, entry := range envelope.Entries {
		if entry.Key != "meshPoints" {
			continue
		}
		candidate, valid := entry.Value.(*psdfile.DescriptorObjectArray)
		if mesh != nil || !valid || candidate == nil || entry.Type != "ObAr" {
			return editError(ErrUnsupported, "smartObject.warp.mesh", "one typed control-point array is required")
		}
		mesh = candidate
	}
	if mesh == nil {
		return editError(ErrUnsupported, "smartObject.warp.mesh", "control-point array is absent")
	}
	var found [2]bool
	for _, column := range mesh.Entries {
		axis := 0
		switch column.Key {
		case "Hrzn":
		case "Vrtc":
			axis = 1
		default:
			continue
		}
		array, valid := column.Value.(psdfile.DescriptorUnitArray)
		if found[axis] || !valid || column.Type != "UnFl" || array.Unit != "#Pxl" ||
			len(array.Numbers) != len(points) {
			return editError(ErrUnsupported, "smartObject.warp.mesh", "each axis must retain its existing pixel sample count")
		}
		data, err := smartNumericSpan(payload, span, array.NumbersData, int64(len(points))*8)
		if err != nil {
			return err
		}
		for index, point := range points {
			value := point.X
			if axis == 1 {
				value = point.Y
			}
			if !smartFinite(value) {
				return editError(ErrInvalidEdit, "smartObject.warp.mesh", "control-point samples must be finite")
			}
			binary.BigEndian.PutUint64(data[index*8:], math.Float64bits(value))
		}
		found[axis] = true
	}
	if found != [2]bool{true, true} {
		return editError(ErrUnsupported, "smartObject.warp.mesh", "both pixel-coordinate axes are required")
	}
	return nil
}
