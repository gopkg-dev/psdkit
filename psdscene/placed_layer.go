package psdscene

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/internal/psdplaced"
	"github.com/gopkg-dev/psdkit/internal/psdrendered"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// RenderPlacedLayer resolves one original RGB8 placed layer and projects its artwork into parent coordinates.
// Original placement, linked contents and color profiles supply the artwork; saved layer pixels are ignored.
// The returned owned layer retains effect and mask metadata separately from its unmasked, unfilled projected pixels.
// Effects, layer opacity, Fill and pixel masks are applied by composition after this operation.
// The complete projected raster and any recursively rendered prototype are charged to MaxWorkingBytes.
// Its runtime RenderedEffects identity permits approximate effect composition while retaining Placed metadata.
func RenderPlacedLayer(
	ctx context.Context, source *psdfile.Document, layerIndex int, options ...Options,
) (*psdmodel.Layer, error) {
	return renderPlacedLayer(ctx, source, layerIndex, nil, options...)
}

// RenderPreparedPlacedLayer renders private editor candidates through the original source-placement pipeline.
// Parent profiles and filter-mask records remain original; candidate filters run once on freshly projected contents.
// Returned artwork omits parent layer masks, effects, opacity and Fill, which the editing compositor applies later.
// Explicit candidate parent effects only determine the necessary source neighborhood and remain unpainted metadata.
// The private request's encoded storage is borrowed; MaxWorkingBytes bounds additional metadata and rendering storage.
func RenderPreparedPlacedLayer(
	ctx context.Context, prepared psdplaced.Prepared, options ...Options,
) (*psdmodel.Layer, error) {
	return renderPlacedLayer(ctx, prepared.Source, prepared.LayerIndex, &prepared, options...)
}

func renderPlacedLayer(
	ctx context.Context, source *psdfile.Document, layerIndex int, prepared *psdplaced.Prepared, options ...Options,
) (*psdmodel.Layer, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil || len(options) > 1 {
		return nil, sceneError(psdfile.ErrMalformed, "placedLayer.options", 0,
			"provide a source and at most one option value")
	}
	if layerIndex < 0 || layerIndex >= len(source.Layers) {
		return nil, sceneError(psdfile.ErrMalformed, "placedLayer.index", 0, "placed layer index is out of range")
	}
	provided := Options{}
	if len(options) != 0 {
		provided = options[0]
	}
	if prepared != nil {
		if err := preparedPlacementOptions(*prepared, provided); err != nil {
			return nil, err
		}
	}
	if provided.ApproximateText != nil {
		return nil, sceneError(psdfile.ErrUnsupported, "placedLayer.text", 0,
			"use the explicit OpenType source policy for placed-layer source text")
	}
	if provided.MaxWorkingBytes == 0 {
		provided.MaxWorkingBytes = 256 << 20
	}
	if provided.MaxDepth == 0 {
		provided.MaxDepth = 16
	}
	if provided.MaxDocuments == 0 {
		provided.MaxDocuments = 64
	}
	canvas := image.Rect(0, 0, int(source.Header.Width), int(source.Header.Height))
	ctx, settings, err := tileConfiguration(ctx, source, canvas, []TileOptions{{
		MaxWorkingBytes: provided.MaxWorkingBytes, MaxDepth: provided.MaxDepth, MaxDocuments: provided.MaxDocuments,
		ExternalResolver: provided.ExternalResolver, CachedSpecialLayers: provided.CachedSpecialLayers,
		ExternalRasterResolver: provided.ExternalRasterResolver,
		Compose:                provided.Compose, SmartFilters: provided.SmartFilters, AssumeUntaggedSRGB: provided.AssumeUntaggedSRGB,
		ApproximatePlacedProjection: provided.ApproximatePlacedProjection,
		CachedTextLayers:            provided.CachedTextLayers, ApproximateVectorStroke: provided.ApproximateVectorStroke,
		ApproximateCustomWarp: provided.ApproximateCustomWarp,
		CenteredJPEGChroma:    provided.CenteredJPEGChroma, ApproximateOpenTypeText: provided.ApproximateOpenTypeText,
		FontResolver: provided.FontResolver,
	}}, false)
	if err != nil {
		return nil, err
	}
	if source.Header.Depth != 8 || source.Header.ColorMode != 3 {
		return nil, sceneError(psdfile.ErrUnsupported, "placedLayer.header", 0, "original RGB8 source is required")
	}
	if provided.OutputProfile != nil {
		return nil, sceneError(psdfile.ErrUnsupported, "placedLayer.color", 0,
			"placed artwork must retain the parent document color encoding")
	}
	if source.ActiveLayerSet >= 0 && source.ActiveLayerSet < len(source.LayerSets) &&
		source.LayerSets[source.ActiveLayerSet].Depth != 8 {
		return nil, sceneError(psdfile.ErrUnsupported, "placedLayer.layerSets", 0,
			"active layer depth differs from the RGB8 source header")
	}
	view, err := source.ReadLayerMetadata(ctx, layerIndex,
		psdfile.MetadataOptions{MaxBytes: settings.MaxWorkingBytes})
	if err != nil {
		return nil, err
	}
	layer := view.Layer
	if layer == nil || layer.Placed == nil || view.Divider != 0 || layer.Group || layer.Children != nil {
		return nil, sceneError(psdfile.ErrUnsupported, "placedLayer.source", source.Layers[layerIndex].Span.Offset,
			"the selected physical layer must contain one original placed artwork source")
	}
	if len(view.BlockingIssues) != 0 || len(layer.Unsupported) != 0 {
		return nil, sceneError(psdfile.ErrUnsupported, "placedLayer.source", source.Layers[layerIndex].Span.Offset,
			"the selected source contains unsupported layer metadata")
	}
	engine := rgbTileRenderer{source: source, settings: settings, window: canvas,
		working: view.MetadataBytes, nodes: map[*psdmodel.Layer]int{layer: layerIndex},
		maskIDs:  map[*psdmodel.Layer]int16{layer: view.PixelMaskChannel},
		contents: make(map[string]*image.NRGBA), legacy: renderer{
			options: Options{MaxWorkingBytes: settings.MaxWorkingBytes, MaxDepth: settings.MaxDepth,
				MaxDocuments: settings.MaxDocuments, ExternalResolver: settings.ExternalResolver, Compose: settings.Compose,
				ExternalRasterResolver:      settings.ExternalRasterResolver,
				ApproximatePlacedProjection: settings.ApproximatePlacedProjection,
				CachedTextLayers:            settings.CachedTextLayers, ApproximateVectorStroke: settings.ApproximateVectorStroke,
				ApproximateCustomWarp: settings.ApproximateCustomWarp,
				CachedSpecialLayers:   settings.CachedSpecialLayers, AssumeUntaggedSRGB: settings.AssumeUntaggedSRGB,
				CenteredJPEGChroma: settings.CenteredJPEGChroma, SmartFilters: settings.SmartFilters},
			active: map[*psdfile.Document]bool{source: true}, documents: 1,
		}}
	if prepared != nil && prepared.Effects != nil {
		childCompose := settings.Compose
		engine.sourceCompose = &childCompose
		engine.settings.Compose.ApproximateDropShadow = prepared.EffectsApproximateDropShadow
	}
	release, err := engine.enterOpenTypeDocument()
	if err != nil {
		return nil, err
	}
	defer release()
	if prepared != nil {
		if err := engine.installPreparedPlacement(ctx, *prepared, layer); err != nil {
			return nil, err
		}
	}
	if err := engine.preparePlaced(ctx, source.Layers[layerIndex], layer); err != nil {
		return nil, err
	}
	if len(layer.Placed.Unsupported) != 0 {
		return nil, sceneError(psdfile.ErrUnsupported, "placedLayer.placement", source.Layers[layerIndex].Span.Offset,
			layer.Placed.Unsupported[0])
	}
	if prepared == nil {
		if err := engine.preparePixelMask(source.Layers[layerIndex], layer); err != nil {
			return nil, err
		}
		if err := engine.loadPixelMask(ctx, source.Layers[layerIndex], layer); err != nil {
			return nil, err
		}
	}
	if layer.RenderedEffects == nil {
		if err := engine.reserve(psdrendered.WorkingBytes,
			"placedLayer.effectSource", source.Layers[layerIndex].Span.Offset); err != nil {
			return nil, err
		}
		layer.RenderedEffects = psdrendered.New(layer.Pixels, layer.Bounds, layer.Placed)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if prepared != nil {
		if _, err := source.ReadAt(nil, 0); err != nil {
			return nil, err
		}
		if _, err := prepared.Candidate.ReadAt(nil, 0); err != nil {
			return nil, err
		}
	}
	return layer, nil
}
