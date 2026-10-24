package psdscene

import (
	"context"
	"image"
	"math"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func artboardSourceConfiguration(
	ctx context.Context, source *psdfile.Document, configuration ArtboardRenderOptions,
) (TileOptions, psdcompose.Options, int64, error) {
	provided := Options{}
	if configuration.Render != nil {
		provided = *configuration.Render
	}
	if provided.CachedSpecialLayers || provided.CachedTextLayers || provided.OutputProfile != nil ||
		provided.ApproximateText != nil {
		return TileOptions{}, psdcompose.Options{}, 0,
			artboardRenderError(psdfile.ErrUnsupported, "artboard.source.policy", 0,
				"artboard sources require live content in parent encoding; use explicit OpenType source text")
	}
	compose := provided.Compose
	if compose.MaxWorkingBytes < 0 || compose.MaxDepth < 0 || compose.MaxLayers < 0 ||
		compose.MaxFeatherRadius < 0 || math.IsNaN(compose.MaxFeatherRadius) ||
		math.IsInf(compose.MaxFeatherRadius, 0) {
		return TileOptions{}, psdcompose.Options{}, 0,
			artboardRenderError(psdfile.ErrMalformed, "artboard.source.compose", 0,
				"source composition limits must be finite and nonnegative")
	}
	if provided.MaxWorkingBytes < 0 || provided.MaxDepth < 0 || provided.MaxDocuments < 0 {
		return TileOptions{}, psdcompose.Options{}, 0,
			artboardRenderError(psdfile.ErrMalformed, "artboard.source.options", 0, "source limits must be nonnegative")
	}
	if provided.MaxDepth == 0 {
		provided.MaxDepth = 16
	}
	if provided.MaxDocuments == 0 {
		provided.MaxDocuments = 64
	}
	canvas := image.Rect(0, 0, int(source.Header.Width), int(source.Header.Height))
	_, settings, err := tileConfiguration(ctx, source, canvas, []TileOptions{{
		MaxWorkingBytes: configuration.MaxWorkingBytes, MaxDepth: provided.MaxDepth,
		MaxDocuments: provided.MaxDocuments, Compose: configuration.Compose,
		ExternalResolver: provided.ExternalResolver, ExternalRasterResolver: provided.ExternalRasterResolver,
		ApproximatePlacedProjection: provided.ApproximatePlacedProjection,
		ApproximateCustomWarp:       provided.ApproximateCustomWarp,
		ApproximateVectorStroke:     provided.ApproximateVectorStroke,
		SmartFilters:                provided.SmartFilters, AssumeUntaggedSRGB: provided.AssumeUntaggedSRGB,
		CenteredJPEGChroma: provided.CenteredJPEGChroma, ApproximateOpenTypeText: provided.ApproximateOpenTypeText,
		FontResolver: provided.FontResolver,
	}}, false)
	return settings, compose, provided.MaxWorkingBytes, err
}

type artboardPlacedRenderer struct {
	settings     TileOptions
	childCompose psdcompose.Options
	limit        int64
	retained     int64
	engine       *rgbTileRenderer
	release      func()
}

func artboardSourcePolicyBytes(settings TileOptions) int64 {
	var cost int64
	if settings.ApproximateOpenTypeText != nil {
		cost += openTypeTextOptionsBytes(settings.ApproximateOpenTypeText)
	}
	if settings.SmartFilters != nil {
		cost += int64(unsafe.Sizeof(psdfilter.Options{}))
	}
	return cost
}

func (live *artboardPlacedRenderer) close() {
	if live.release != nil {
		live.release()
	}
}

func (live *artboardPlacedRenderer) prepare(
	ctx context.Context, source *psdfile.Document, physical []*psdmodel.Layer,
	layer *psdmodel.Layer, budget *renderer,
) error {
	remaining := budget.options.MaxWorkingBytes - budget.working
	if live.limit != 0 {
		remaining = min(remaining, live.limit-live.retained)
	}
	if remaining <= 0 {
		return artboardRenderError(psdfile.ErrLimit, "artboard.source.storage", 0,
			"no live source rendering storage remains")
	}
	maximum := budget.working + remaining
	if live.engine == nil {
		cost := int64(len(physical))*64 + 512
		if cost > remaining {
			return artboardRenderError(psdfile.ErrLimit, "artboard.source.metadata", 0,
				"live source maps and owned policies exceed remaining storage")
		}
		if err := budget.reserve(cost, "artboard.source.metadata", 0); err != nil {
			return err
		}
		live.retained += cost
		settings := live.settings
		settings.MaxWorkingBytes = maximum
		live.engine = &rgbTileRenderer{source: source, settings: settings,
			window:  image.Rect(0, 0, int(source.Header.Width), int(source.Header.Height)),
			working: budget.working, nodes: make(map[*psdmodel.Layer]int),
			contents: make(map[string]*image.NRGBA), sourceCompose: &live.childCompose,
			legacy: renderer{options: Options{MaxWorkingBytes: maximum, MaxDepth: settings.MaxDepth,
				MaxDocuments: settings.MaxDocuments, ExternalResolver: settings.ExternalResolver,
				ExternalRasterResolver: settings.ExternalRasterResolver, Compose: live.childCompose,
				ApproximatePlacedProjection: settings.ApproximatePlacedProjection,
				ApproximateCustomWarp:       settings.ApproximateCustomWarp,
				ApproximateVectorStroke:     settings.ApproximateVectorStroke,
				AssumeUntaggedSRGB:          settings.AssumeUntaggedSRGB, CenteredJPEGChroma: settings.CenteredJPEGChroma,
				SmartFilters: settings.SmartFilters}, active: map[*psdfile.Document]bool{source: true}, documents: 1}}
		for index, candidate := range physical {
			if candidate != nil {
				live.engine.nodes[candidate] = index
			}
		}
		release, err := live.engine.enterOpenTypeDocument()
		if err != nil {
			return err
		}
		live.release = release
		live.retained += live.engine.working - budget.working
		budget.working = live.engine.working
	}
	engine := live.engine
	engine.working = budget.working
	engine.settings.MaxWorkingBytes, engine.legacy.options.MaxWorkingBytes = maximum, maximum
	start := engine.working
	if err := engine.preparePlaced(ctx, source.Layers[engine.nodes[layer]], layer); err != nil {
		return err
	}
	if len(layer.Placed.Unsupported) != 0 {
		return artboardRenderError(psdfile.ErrUnsupported, "artboard.source.placement", 0, layer.Placed.Unsupported[0])
	}
	live.retained += engine.working - start
	budget.working = engine.working
	return ctx.Err()
}
