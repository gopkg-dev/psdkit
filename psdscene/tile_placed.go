package psdscene

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/internal/psdplaced"
	"github.com/gopkg-dev/psdkit/internal/psdrendered"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func (engine *rgbTileRenderer) readProfile(ctx context.Context) (*sceneProfile, error) {
	if engine.profile != nil {
		return engine.profile, nil
	}
	engine.legacy.working = engine.working
	profile, err := engine.legacy.readProfile(ctx, engine.source)
	if err != nil {
		return nil, err
	}
	engine.profile, engine.working = profile, engine.legacy.working
	return profile, nil
}

func (engine *rgbTileRenderer) placement(
	ctx context.Context, record psdfile.LayerRecord, layer *psdmodel.Layer,
) (psdfile.SmartObjectRecord, error) {
	if engine.prepared != nil && engine.prepared.layer == layer {
		return engine.prepared.placement, nil
	}
	var placement psdfile.SmartObjectRecord
	if layer.Placed.Type != "raster" {
		return placement, sceneError(psdfile.ErrUnsupported, "tile.smartObject.contents", record.Span.Offset,
			"live placed content requires a raster source reference")
	}
	found := false
	for _, block := range record.AdditionalInfo {
		if block.Key != "SoLd" && block.Key != "SoLE" {
			continue
		}
		if found || block.Data.Length < 12 {
			return placement, sceneError(psdfile.ErrUnsupported, "tile.smartObject.reference", block.Data.Offset,
				"duplicate or incomplete modern placement metadata")
		}
		found = true
		available := engine.settings.MaxWorkingBytes - engine.working
		if available < 512 || block.Data.Length > (available-512)/32 {
			return placement, sceneError(psdfile.ErrLimit, "tile.smartObject.metadata", block.Data.Offset,
				"placement descriptor exceeds the remaining working budget")
		}
		reservation := block.Data.Length*32 + 512
		span := psdfile.Span{Offset: block.Data.Offset + 8, Length: block.Data.Length - 8}
		descriptor, err := engine.source.DecodeDescriptorContext(ctx, span, true,
			psdfile.MetadataOptions{MaxBytes: available})
		if err != nil {
			return placement, err
		}
		if err := engine.reserve(reservation, "tile.smartObject.metadata", block.Data.Offset); err != nil {
			return placement, err
		}
		placement = psdfile.SmartObjectRecord{LayerIndex: engine.nodes[layer],
			LayerSetIndex: engine.source.ActiveLayerSet, Block: block, Descriptor: descriptor,
			SourceID: layer.Placed.ID, SourceType: 2, Width: float64(layer.Placed.Width),
			Height: float64(layer.Placed.Height), Transform: layer.Placed.Transform, NonAffine: layer.Placed.NonAffineTransform}
	}
	if !found || placement.SourceID == "" {
		return placement, sceneError(psdfile.ErrUnsupported, "tile.smartObject.reference", record.Span.Offset,
			"placed layer has no modern original-source reference")
	}
	return placement, nil
}

func (engine *rgbTileRenderer) linkedRecords(ctx context.Context) error {
	if engine.linked != nil {
		return nil
	}
	available := engine.settings.MaxWorkingBytes - engine.working
	if available <= 0 {
		return sceneError(psdfile.ErrLimit, "tile.smartObject.links", 0, "no linked metadata budget remains")
	}
	view, err := engine.source.ReadLinkedFileView(ctx, psdfile.MetadataOptions{MaxBytes: available})
	if err != nil {
		return err
	}
	if err := engine.reserve(view.MetadataBytes+int64(len(view.Records))*128, "tile.smartObject.links", 0); err != nil {
		return err
	}
	engine.linked = make(map[string]psdfile.LinkedFileRecord, len(view.Records))
	for _, record := range view.Records {
		if previous, exists := engine.linked[record.ID]; exists && !sameLinkedContentRecord(previous, record) {
			return sceneError(psdfile.ErrUnsupported, "tile.smartObject.links", record.Span.Offset,
				"duplicate original content records are ambiguous")
		}
		engine.linked[record.ID] = record
	}
	return nil
}

func (engine *rgbTileRenderer) prototype(
	ctx context.Context, placement psdfile.SmartObjectRecord,
) (*image.NRGBA, error) {
	if pixels := engine.contents[placement.SourceID]; pixels != nil {
		return pixels, nil
	}
	if engine.prepared != nil && engine.prepared.contents != nil &&
		engine.prepared.placement.SourceID == placement.SourceID {
		return engine.preparedPrototype(ctx, placement)
	}
	if err := engine.linkedRecords(ctx); err != nil {
		return nil, err
	}
	record, exists := engine.linked[placement.SourceID]
	if !exists {
		return nil, sceneError(psdfile.ErrUnsupported, "tile.smartObject.contents", placement.Block.Data.Offset,
			"the referenced original placed source is absent")
	}
	profile, err := engine.readProfile(ctx)
	if err != nil {
		return nil, err
	}
	format := ""
	if record.Kind == "liFD" {
		format, err = engine.source.LinkedRasterFormat(ctx, record)
		if err != nil {
			return nil, err
		}
	}
	if engine.settings.ApproximateOpenTypeText != nil {
		pixels, err := engine.openTypePrototype(ctx, record, format, profile)
		if err != nil {
			return nil, err
		}
		engine.contents[placement.SourceID] = pixels
		return pixels, nil
	}
	engine.legacy.working = engine.working
	depth := 2
	if engine.settings.sourceDepth != 0 {
		depth = engine.settings.sourceDepth + 1
	}
	var pixels *image.NRGBA
	if record.Kind == "liFD" && format != "" {
		pixels, err = engine.legacy.renderLinkedRaster(ctx, engine.source, record, profile, depth)
	} else if engine.legacy.resolvesExternalRaster(record) {
		pixels, err = engine.legacy.renderExternalRaster(ctx, engine.source, record, profile, depth)
	} else {
		var child *psdfile.Document
		if record.Kind == "liFD" {
			limits := engine.source.Limits()
			available := engine.settings.MaxWorkingBytes - engine.working
			if available <= 0 {
				return nil, sceneError(psdfile.ErrLimit, "tile.smartObject.index", record.Data.Offset,
					"no original child indexing budget remains")
			}
			limits.MaxMetadataBytes = min(limits.MaxMetadataBytes, available)
			child, err = engine.source.OpenLinkedDocument(ctx, record, psdfile.LoadOptions{Limits: limits})
		} else {
			child, err = engine.source.ResolveLinkedDocument(ctx, record, engine.settings.ExternalResolver)
		}
		if err != nil {
			return nil, err
		}
		if engine.legacy.active[child] {
			return nil, sceneError(psdfile.ErrMalformed, "tile.smartObject.contents", record.Data.Offset,
				"the resolver returned an ancestor original source")
		}
		indexBytes := child.IndexMetadataBytes()
		if err := engine.legacy.reserve(indexBytes, "tile.smartObject.index", record.Data.Offset); err != nil {
			_ = child.Close()
			return nil, err
		}
		pixels, err = engine.legacy.render(ctx, child, depth, profile)
		closeErr := child.Close()
		engine.legacy.working -= indexBytes
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	if err != nil {
		return nil, err
	}
	engine.working = engine.legacy.working
	engine.contents[placement.SourceID] = pixels
	return pixels, nil
}

type placedTilePlan struct {
	placement              psdfile.SmartObjectRecord
	filters                *sceneSmartFilter
	projection             *psdrender.ProjectionPlan
	patch                  psdplaced.WarpProjection
	geometryBytes          int64
	canvas, window, bounds image.Rectangle
	ready                  bool
}

func (engine *rgbTileRenderer) compilePlacedPlan(
	ctx context.Context, record psdfile.LayerRecord, layer *psdmodel.Layer,
) (placedTilePlan, error) {
	var result placedTilePlan
	placement, err := engine.placement(ctx, record, layer)
	if err != nil {
		return result, err
	}
	if err := validateScenePresetDescriptor(layer.Placed, placement.Descriptor); err != nil {
		return result, sceneProjectionError(err, "tile.smartObject.warp", placement.Block.Data.Offset)
	}
	engine.legacy.working = engine.working
	filters, err := engine.legacy.prepareSmartFilters(ctx, placement, layer)
	if err != nil {
		return result, err
	}
	engine.working = engine.legacy.working
	filterHalo, err := tileFilterHalo(filters, engine.settings.SmartFilters)
	if err != nil {
		return result, err
	}
	effectWindow, err := engine.effectSourceWindow(layer)
	if err != nil {
		return result, err
	}
	canvas := image.Rect(0, 0, int(engine.source.Header.Width), int(engine.source.Header.Height))
	window := effectWindow.Inset(-filterHalo)
	if engine.approximateShadowSource(layer) {
		// Classify complete filter inputs against the same effect source domain for every requested tile.
		complete := *engine
		complete.window = canvas
		completeWindow, err := complete.effectSourceWindow(layer)
		if err != nil {
			return result, err
		}
		canvas = canvas.Union(completeWindow.Inset(-filterHalo))
	} else {
		window = window.Intersect(canvas)
	}
	var plan *psdrender.ProjectionPlan
	var patch psdplaced.WarpProjection
	var geometryBytes int64
	var bounds image.Rectangle
	if hasSceneWarp(layer.Placed) {
		available := engine.settings.MaxWorkingBytes - engine.working
		if available <= 0 {
			return result, sceneError(psdfile.ErrLimit, "tile.smartObject.warp", 0, "no warp geometry budget remains")
		}
		patch, err = engine.compilePlacedWarp(ctx, layer,
			psdwarp.Options{MaxBytes: available})
		if err != nil {
			return result, sceneProjectionError(err, "tile.smartObject.warp", record.Span.Offset)
		}
		geometryBytes = patch.WorkingBytes()
		if err := engine.reserve(geometryBytes, "tile.smartObject.warp", 0); err != nil {
			return result, err
		}
		if window.Empty() {
			// Warp bounds use an empty rectangle to mean no clipping; a regional empty window must stay empty.
			bounds = window
		} else {
			bounds, err = patch.Bounds(window)
		}
	} else {
		plan, err = psdrender.CompileProjectionRegion(layer, canvas, window)
		if err == nil {
			bounds = image.Rect(plan.Left, plan.Top, plan.Left+plan.Width, plan.Top+plan.Height)
		}
	}
	if err != nil {
		return result, err
	}
	result = placedTilePlan{placement: placement, filters: filters, projection: plan, patch: patch,
		geometryBytes: geometryBytes, canvas: canvas, window: window, bounds: bounds, ready: true}
	return result, nil
}

func (engine *rgbTileRenderer) preparePlaced(
	ctx context.Context, record psdfile.LayerRecord, layer *psdmodel.Layer,
) error {
	var compiled placedTilePlan
	var err error
	index := engine.nodes[layer]
	if index < len(engine.placedPlans) && engine.placedPlans[index].ready {
		compiled = engine.placedPlans[index]
		engine.placedPlans[index] = placedTilePlan{}
	} else {
		compiled, err = engine.compilePlacedPlan(ctx, record, layer)
		if err != nil {
			return err
		}
	}
	placement, filters, plan, patch := compiled.placement, compiled.filters, compiled.projection, compiled.patch
	geometryBytes, canvas, window, bounds := compiled.geometryBytes, compiled.canvas, compiled.window, compiled.bounds
	if bounds.Empty() {
		if engine.prepared != nil ||
			(engine.settings.Compose.ApproximateDropShadow || engine.settings.Compose.ApproximatePlacedColorOverlay) &&
				activeLayerEffects(layer) {
			// Resolve and qualify live contents even when the proven projection/source intersection is empty.
			prototype, err := engine.prototype(ctx, placement)
			if err != nil {
				return err
			}
			if prototype.Rect.Dx() != layer.Placed.Width || prototype.Rect.Dy() != layer.Placed.Height {
				return sceneError(psdfile.ErrUnsupported, "tile.smartObject.size", placement.Block.Data.Offset,
					"original source canvas differs from placement dimensions")
			}
		}
		layer.Pixels, layer.Bounds = image.NewNRGBA(bounds), bounds
		engine.working -= geometryBytes
		if filters != nil {
			engine.working -= filters.cost
		}
		if err := engine.bindEffectSource(layer, placement.Block.Data.Offset); err != nil {
			return err
		}
		return nil
	}
	prototype, err := engine.prototype(ctx, placement)
	if err != nil {
		return err
	}
	if prototype.Rect.Dx() != layer.Placed.Width || prototype.Rect.Dy() != layer.Placed.Height {
		return sceneError(psdfile.ErrUnsupported, "tile.smartObject.size", placement.Block.Data.Offset,
			"original source canvas differs from placement dimensions")
	}
	projectionBytes := int64(bounds.Dx()) * int64(bounds.Dy()) * 4
	engine.legacy.working = engine.working
	if err := engine.legacy.reserve(
		projectionBytes, "tile.smartObject.projection", placement.Block.Data.Offset,
	); err != nil {
		return err
	}
	var pixels *image.NRGBA
	var origin image.Point
	if patch != nil {
		available := engine.settings.MaxWorkingBytes - engine.working + geometryBytes
		pixels, origin, err = projectSceneWarp(ctx, patch, prototype, window, available)
	} else {
		pixels, origin, err = projectPlacedSource(ctx, layer, prototype, plan,
			engine.settings.ApproximatePlacedProjection)
	}
	if err != nil {
		return sceneProjectionError(err, "tile.smartObject.projection", placement.Block.Data.Offset)
	}
	layer.Pixels = pixels
	layer.Bounds = image.Rectangle{Min: origin, Max: origin.Add(pixels.Rect.Size())}
	if filters != nil {
		factBytes := int64(len(filters.operation.Filters))
		if err := engine.legacy.reserve(factBytes, "tile.smartObject.opacity", 0); err != nil {
			return err
		}
		filters.cost += factBytes
		filters.opaqueFacts, err = engine.filterOpacityFacts(ctx, layer, prototype, filters, canvas)
		if err != nil {
			return err
		}
	}
	if err := engine.legacy.applySmartFilters(ctx, engine.source, layer, filters, window); err != nil {
		return err
	}
	engine.working = engine.legacy.working
	engine.working -= geometryBytes
	if err := engine.bindEffectSource(layer, placement.Block.Data.Offset); err != nil {
		return err
	}
	return nil
}

func projectPlacedSource(
	ctx context.Context, layer *psdmodel.Layer, prototype *image.NRGBA, plan *psdrender.ProjectionPlan,
	approximate bool,
) (*image.NRGBA, image.Point, error) {
	return psdrender.ProjectPlacedContentsWithOptions(ctx, layer, prototype, plan,
		psdrender.PlacedSamplingOptions{ApproximateTransparentBorder: approximate})
}

func (engine *rgbTileRenderer) bindEffectSource(layer *psdmodel.Layer, offset int64) error {
	if (!engine.settings.Compose.ApproximateDropShadow && !engine.settings.Compose.ApproximatePlacedColorOverlay) ||
		!activeLayerEffects(layer) {
		return nil
	}
	if err := engine.reserve(psdrendered.WorkingBytes, "tile.smartObject.effectSource", offset); err != nil {
		return err
	}
	layer.RenderedEffects = psdrendered.New(layer.Pixels, layer.Bounds, layer.Placed)
	return nil
}

func tileFilterHalo(filters *sceneSmartFilter, options *psdfilter.Options) (int, error) {
	if filters == nil {
		return 0, nil
	}
	if options == nil {
		return 0, sceneError(psdfile.ErrUnsupported, "tile.smartObject.filters", 0,
			"live filters require an explicit rendering policy")
	}
	halo, err := psdfilter.RequiredHalo(filters.operation, *options)
	if err != nil {
		return 0, sceneFilterError(err)
	}
	return halo, nil
}
