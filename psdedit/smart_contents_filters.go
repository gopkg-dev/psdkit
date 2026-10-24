package psdedit

import (
	"context"
	"image"
	"maps"
	"math"

	"github.com/gopkg-dev/psdkit/internal/psdplaced"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdscene"
)

type smartContentsFilterStage struct {
	pixels *image.NRGBA
	bounds image.Rectangle
	placed *psdmodel.Placed
	blocks map[int64][]byte
	cost   int64
}

func smartContentsHasFilters(records map[int][]psdfile.SmartObjectRecord, identity string) bool {
	for _, placements := range records {
		for _, placement := range placements {
			if placement.SourceID != identity || placement.Descriptor == nil {
				continue
			}
			for _, entry := range placement.Descriptor.Entries {
				if entry.Key == "filterFX" {
					return true
				}
			}
		}
	}
	return false
}

// replaceFilteredSmartContentsLocked stages every shared instance before replacing any editor-owned state.
func (e *Editor) replaceFilteredSmartContentsLocked(
	ctx context.Context, nodes []*editNode, identity string, data []byte, linked psdfile.LinkedFileView,
	placementBytes int64, embedExternal bool, external *LinkedContentsUpdate, options SmartContentsOptions,
) error {
	limits := e.source.Limits()
	working := e.ownedBytesLocked(nil) + linked.MetadataBytes + placementBytes + int64(len(nodes))*256
	if external != nil && external.Timestamp != nil {
		working += 64
	}
	if working >= limits.MaxDecodeBytes || int64(len(data)) > limits.MaxFileBytes ||
		int64(len(data)) >= limits.MaxDecodeBytes-working {
		return editError(psdfile.ErrLimit, "smartObject.filters.source", "replacement exceeds the retained peak budget")
	}
	owned := make([]byte, len(data))
	copy(owned, data)
	working += int64(cap(owned))
	content, err := decodeSmartContentsSource(ctx, owned, limits, limits.MaxDecodeBytes-working)
	if err != nil {
		return err
	}
	dimensions, fileType := image.Pt(content.width, content.height), content.fileType
	if err := content.close(); err != nil {
		return err
	}
	filters := psdfilter.Options{}
	if options.SmartFilters != nil {
		filters = *options.SmartFilters
	}
	stages := make([]smartContentsFilterStage, len(nodes))
	for index, node := range nodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(node.planes) != 0 {
			return editError(ErrInvalidEdit, "smartObject.channels", "source updates cannot mix raw channel overlays")
		}
		stage, retained, err := e.renderSmartContentsFiltersLocked(ctx, node, owned, fileType,
			dimensions, options, filters, limits.MaxDecodeBytes-working)
		if err != nil {
			return err
		}
		stages[index] = stage
		working += retained
	}
	if _, err := e.source.ReadAt(nil, 0); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if previous := e.linkedOverlays[identity]; previous != nil && previous.embedExternal {
		embedExternal = true
	}
	replacement := &linkedReplacement{data: owned, fileType: fileType, embedExternal: embedExternal}
	if external != nil {
		replacement.retainExternal, replacement.timestamp = true, external.Timestamp
	}
	if e.linkedOverlays == nil {
		e.linkedOverlays = make(map[string]*linkedReplacement)
	}
	e.linkedRecords, e.linkedRecordsBytes = linked.Records, linked.MetadataBytes
	e.linkedOverlays[identity] = replacement
	for index, node := range nodes {
		stage := stages[index]
		if node.additional == nil {
			node.additional = make(map[int64][]byte, len(stage.blocks))
		}
		maps.Copy(node.additional, stage.blocks)
		node.pixels, node.bounds, node.placed = stage.pixels, stage.bounds, stage.placed
		node.setSemanticCost("placed", stage.cost)
	}
	e.markChangedLocked(false)
	return nil
}

func (e *Editor) renderSmartContentsFiltersLocked(
	ctx context.Context, node *editNode, contents []byte, fileType string, dimensions image.Point,
	options SmartContentsOptions, filters psdfilter.Options, available int64,
) (smartContentsFilterStage, int64, error) {
	var stage smartContentsFilterStage
	block, err := e.smartFilterEditBlock(node)
	if err != nil {
		return stage, 0, err
	}
	limits := e.source.Limits()
	// Current encoded placement is authoritative for both edited geometry and filter parameters.
	current := node.additional[block.Span.Offset]
	length := block.Data.Length
	if current != nil {
		length = int64(len(current))
	}
	if available <= 0 || length > min(limits.MaxMetadataBytes, available/16) {
		return stage, 0, editError(psdfile.ErrLimit, "smartObject.filters.metadata", "candidate payload exceeds budget")
	}
	var payload []byte
	if current == nil {
		payload, err = readSmartPayload(ctx, e.source, block.Data)
		if err != nil {
			return stage, 0, err
		}
	} else {
		payload = make([]byte, len(current))
		copy(payload, current)
	}
	candidate, candidateBlock, err := smartFilterEditSource(ctx, payload, block.Key, limits, available-int64(cap(payload)))
	if err != nil {
		return stage, 0, err
	}
	defer func() {
		if candidate != nil {
			_ = candidate.Close()
		}
	}()
	candidateBytes := int64(len(payload)+len(payload)%2+111) + candidate.IndexMetadataBytes()
	retained := int64(cap(payload))
	viewBudget := available - retained - candidateBytes
	if viewBudget <= 0 {
		return stage, 0, editError(psdfile.ErrLimit, "smartObject.filters.metadata", "no placement view budget remains")
	}
	view, err := candidate.ReadSmartObjectView(ctx, psdfile.MetadataOptions{MaxBytes: viewBudget})
	if err != nil {
		return stage, 0, err
	}
	if len(view.Records) != 1 {
		return stage, 0, editError(ErrUnsupported, "smartObject.filters.metadata", "one finite placement is required")
	}
	placement := view.Records[0]
	if !smartFinite(placement.Width) || !smartFinite(placement.Height) || placement.Width <= 0 || placement.Height <= 0 ||
		placement.Width > psdmodel.MaxDimension || placement.Height > psdmodel.MaxDimension ||
		placement.Width != math.Trunc(placement.Width) || placement.Height != math.Trunc(placement.Height) {
		return stage, 0, editError(ErrUnsupported, "smartObject.filters.metadata",
			"integral prototype dimensions are required")
	}
	resized := placement.Width != float64(dimensions.X) || placement.Height != float64(dimensions.Y)
	stage.blocks = map[int64][]byte{block.Span.Offset: payload}
	if resized {
		sx, sy := float64(dimensions.X)/placement.Width, float64(dimensions.Y)/placement.Height
		if err := patchSmartDescriptor(placement.Descriptor, payload, candidateBlock.Data,
			int(placement.Width), int(placement.Height), sx, sy); err != nil {
			return stage, 0, err
		}
		for _, legacy := range e.source.Layers[node.sourceIndex].AdditionalInfo {
			if legacy.Key != "PlLd" && legacy.Key != "plLd" {
				continue
			}
			remaining := available - retained - candidateBytes - view.MetadataBytes
			legacyPayload, err := e.smartContentsLegacySizeLocked(ctx, node, legacy,
				int(placement.Width), int(placement.Height), sx, sy, remaining)
			if err != nil {
				return stage, 0, err
			}
			stage.blocks[legacy.Span.Offset] = legacyPayload
			retained += int64(cap(legacyPayload))
		}
		// The first candidate indexed the unpatched geometry; rebuild only the private envelope.
		if err := candidate.Close(); err != nil {
			return stage, 0, err
		}
		candidate, _, err = smartFilterEditSource(ctx, payload, block.Key, limits,
			available-retained-view.MetadataBytes)
		if err != nil {
			return stage, 0, err
		}
		candidateBytes = int64(len(payload)+len(payload)%2+111) + candidate.IndexMetadataBytes()
	}
	// The view is retained until this call returns; charge it alongside the private envelope and output.
	maskBytes := e.source.Layers[node.sourceIndex].MaskData.Length
	if node.maskData != nil {
		maskBytes = int64(len(node.maskData))
	}
	qualification := maskBytes + int64(len(e.source.Layers[node.sourceIndex].Channels))*256 + 4096
	remaining := available - retained - candidateBytes - view.MetadataBytes - qualification
	if remaining <= 0 {
		return stage, 0, editError(psdfile.ErrLimit, "smartObject.filters.render", "no source rendering budget remains")
	}
	layer, err := psdscene.RenderPreparedPlacedLayer(ctx, psdplaced.Prepared{
		Source: e.source, Candidate: candidate, LayerIndex: node.sourceIndex, Contents: contents, FileType: fileType,
	}, psdscene.Options{MaxWorkingBytes: remaining, SmartFilters: &filters,
		ApproximateCustomWarp: options.ApproximateCustomWarp, AssumeUntaggedSRGB: options.AssumeUntaggedSRGB})
	if err != nil {
		return stage, 0, compositeRenderError(err)
	}
	if layer == nil || layer.Pixels == nil || layer.Placed == nil || layer.Bounds.Empty() ||
		layer.Bounds.Size() != layer.Pixels.Rect.Size() {
		return stage, 0, editError(ErrUnsupported, "smartObject.filters.pixels", "nonempty projected artwork is required")
	}
	if err := e.smartTransformGeometryLocked(ctx, node, layer.Bounds, false); err != nil {
		return stage, 0, err
	}
	stage.cost = smartProjectionMetadataBytes(layer.Placed) + 1024 + int64(cap(layer.Pixels.Pix)-len(layer.Pixels.Pix))
	for _, value := range stage.blocks {
		stage.cost += int64(cap(value) - len(value))
	}
	retained += int64(cap(layer.Pixels.Pix)) + smartProjectionMetadataBytes(layer.Placed) + 1024
	if retained > available-candidateBytes-view.MetadataBytes {
		return stage, 0, editError(psdfile.ErrLimit, "smartObject.filters.pixels", "candidate artwork exceeds peak budget")
	}
	owned := *layer.Pixels
	owned.Rect = image.Rect(0, 0, layer.Bounds.Dx(), layer.Bounds.Dy())
	stage.pixels, stage.bounds, stage.placed = &owned, layer.Bounds, layer.Placed
	return stage, retained, nil
}

func (e *Editor) smartContentsLegacySizeLocked(
	ctx context.Context, node *editNode, block psdfile.AdditionalBlock,
	width, height int, sx, sy float64, available int64,
) ([]byte, error) {
	limits := e.source.Limits()
	current := node.additional[block.Span.Offset]
	length := block.Data.Length
	if current != nil {
		length = int64(len(current))
	}
	if available <= 0 || length > min(limits.MaxMetadataBytes, available/33) {
		return nil, editError(psdfile.ErrLimit, "smartObject.filters.legacy", "legacy placement exceeds working budget")
	}
	var payload []byte
	var err error
	if current == nil {
		payload, err = readSmartPayload(ctx, e.source, block.Data)
		if err != nil {
			return nil, err
		}
	} else {
		payload = make([]byte, len(current))
		copy(payload, current)
	}
	bridge, bridgeBlock, err := smartFilterEditSource(ctx, payload, block.Key, limits, available-int64(cap(payload)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = bridge.Close() }()
	if err := patchSmartLegacy(ctx, bridge, payload, bridgeBlock.Data, width, height, sx, sy); err != nil {
		return nil, err
	}
	return payload, nil
}
