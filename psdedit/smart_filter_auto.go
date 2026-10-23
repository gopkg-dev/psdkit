package psdedit

import (
	"bytes"
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/internal/psdplaced"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdscene"
)

type smartFilterAutoEdit struct {
	node        *editNode
	source      *psdfile.Document
	revision    uint64
	block       psdfile.AdditionalBlock
	encoded     []byte
	placed      *psdmodel.Placed
	placedCost  int64
	prepared    psdplaced.Prepared
	retained    int64
	settings    psdscene.Options
	samePayload bool
}

func (e *Editor) setSmartFilterParametersAuto(
	ctx context.Context, id LayerID, stack psdfilter.Stack, provided *psdscene.Options,
) error {
	e.mu.Lock()
	stage, err := e.prepareSmartFilterAutoLocked(ctx, id, stack, provided)
	e.mu.Unlock()
	if err != nil {
		return err
	}
	defer func() { _ = stage.prepared.Candidate.Close() }()
	// Candidate metadata and edited contents are owned snapshots; all user resolvers run without the editor lock.
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
	if e.revision != stage.revision || e.source != stage.source {
		return ErrStaleLinkedEdit
	}
	if layer == nil || layer.Pixels == nil || layer.Bounds.Empty() || layer.Bounds.Size() != layer.Pixels.Rect.Size() {
		return editError(ErrUnsupported, "layers.smartFilters.pixels", "automatic artwork requires nonempty matching bounds")
	}
	limits := e.source.Limits()
	current := e.ownedBytesLocked(nil)
	remaining := limits.MaxDecodeBytes - current - stage.retained
	if remaining <= 0 || int64(cap(layer.Pixels.Pix)) > remaining {
		return editError(psdfile.ErrLimit, "layers.smartFilters.pixels", "automatic artwork exceeds the retained peak budget")
	}
	geometry := *layer.Pixels
	geometry.Rect = layer.Bounds
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
		return editError(psdfile.ErrLimit, "layers.smartFilters", "automatic parameter and pixel overlays exceed budget")
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

func (e *Editor) prepareSmartFilterAutoLocked(
	ctx context.Context, id LayerID, stack psdfilter.Stack, provided *psdscene.Options,
) (*smartFilterAutoEdit, error) {
	if err := e.checkMutableLocked(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.rgbCompositeSupportedLocked(); err != nil {
		return nil, err
	}
	settings := psdscene.Options{}
	if provided != nil {
		settings = *provided
	}
	if settings.OutputProfile != nil || settings.CachedSpecialLayers || settings.CachedTextLayers {
		return nil, editError(ErrUnsupported, "layers.smartFilters.render",
			"automatic editing requires live contents in the parent color encoding")
	}
	if settings.MaxWorkingBytes < 0 {
		return nil, editError(ErrInvalidEdit, "layers.smartFilters.render", "working budget must be nonnegative")
	}
	if settings.SmartFilters == nil {
		settings.SmartFilters = &psdfilter.Options{}
	} else {
		filters := *settings.SmartFilters
		settings.SmartFilters = &filters
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return nil, err
	}
	block, err := e.smartFilterEditBlock(node)
	if err != nil {
		return nil, err
	}
	limits := e.source.Limits()
	available := limits.MaxDecodeBytes - e.ownedBytesLocked(nil)
	if settings.MaxWorkingBytes != 0 {
		available = min(available, settings.MaxWorkingBytes)
	}
	metadataBudget := min(limits.MaxMetadataBytes, available/4)
	if metadataBudget <= 0 {
		return nil, editError(psdfile.ErrLimit, "layers.smartFilters", "no candidate metadata budget remains")
	}
	payload := node.additional[block.Span.Offset]
	if payload == nil {
		if block.Data.Length > metadataBudget/8 {
			return nil, editError(psdfile.ErrLimit, "layers.smartFilters", "placement payload exceeds candidate budget")
		}
		payload, err = readSmartPayload(ctx, e.source, block.Data)
		if err != nil {
			return nil, err
		}
	}
	bridge, bridgeBlock, err := smartFilterEditSource(ctx, payload, block.Key, limits, metadataBudget)
	if err != nil {
		return nil, err
	}
	encoded, encodeErr := bridge.EncodeSmartFilterParameters(ctx, bridgeBlock, stack,
		psdfile.MetadataOptions{MaxBytes: metadataBudget})
	_ = bridge.Close()
	if encodeErr != nil {
		return nil, encodeErr
	}
	placed, placedCost, err := smartFilterEditedPlacement(ctx, encoded, block.Key, limits, metadataBudget)
	if err != nil {
		return nil, err
	}
	candidate, _, err := smartFilterEditSource(ctx, encoded, block.Key, limits, metadataBudget)
	if err != nil {
		return nil, err
	}
	stage := &smartFilterAutoEdit{node: node, source: e.source, revision: e.revision, block: block,
		encoded: encoded, placed: placed, placedCost: placedCost, settings: settings,
		samePayload: bytes.Equal(payload, encoded),
		prepared:    psdplaced.Prepared{Source: e.source, Candidate: candidate, LayerIndex: node.sourceIndex}}
	// Preserve room for the independent candidate envelope, descriptor index and final current-mask qualification.
	maskBytes := e.source.Layers[node.sourceIndex].MaskData.Length
	if node.maskData != nil {
		maskBytes = int64(len(node.maskData))
	}
	stage.retained = int64(cap(encoded)+len(encoded)+len(encoded)%2+111) + candidate.IndexMetadataBytes() +
		placedCost + maskBytes + int64(len(e.source.Layers[node.sourceIndex].Channels))*256 + 4096
	if overlay := e.linkedOverlays[placed.ID]; overlay != nil {
		if !supportedSmartContentsType(overlay.fileType) || len(overlay.data) == 0 {
			_ = candidate.Close()
			return nil, editError(ErrUnsupported, "layers.smartFilters.source", "edited contents have no supported encoding")
		}
		if stage.retained >= available || int64(len(overlay.data)) > available-stage.retained {
			_ = candidate.Close()
			return nil, editError(psdfile.ErrLimit, "layers.smartFilters.source", "candidate contents exceed working budget")
		}
		stage.prepared.Contents = make([]byte, len(overlay.data))
		copy(stage.prepared.Contents, overlay.data)
		stage.prepared.FileType = overlay.fileType
		stage.retained += int64(len(overlay.data))
	}
	if stage.retained >= available {
		_ = candidate.Close()
		return nil, editError(psdfile.ErrLimit, "layers.smartFilters", "candidate consumes the source rendering budget")
	}
	stage.settings.MaxWorkingBytes = available - stage.retained
	return stage, nil
}
