package psdscene

import (
	"context"
	"errors"
	"image"
	"math"
	"slices"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

type rgbTileRenderer struct {
	source      *psdfile.Document
	settings    TileOptions
	window      image.Rectangle
	working     int64
	nodes       map[*psdmodel.Layer]int
	maskIDs     map[*psdmodel.Layer]int16
	patterns    patternSceneSources
	live        map[*psdmodel.Layer]bool
	legacy      renderer
	profile     *sceneProfile
	linked      map[string]psdfile.LinkedFileRecord
	contents    map[string]*image.NRGBA
	texts       []psdfile.TextLayerRecord
	textIDs     []int
	textPlan    []int64
	placedPlans []placedTilePlan
	prepared    *preparedPlacement
}

// RenderTile rebuilds an RGB8 source-layer window from independently decoded channel intersections.
// Groups, clipping, Blend If, pixel masks and supported live fills use the ordinary compositor's semantics.
// Source dimensions and encoded file length do not determine output allocations; Rect remains absolute.
func RenderTile(
	ctx context.Context, source *psdfile.Document, rectangle image.Rectangle, options ...TileOptions,
) (*image.NRGBA, error) {
	ctx, settings, err := tileConfiguration(ctx, source, rectangle, options, true)
	if err != nil {
		return nil, err
	}
	if source.Header.Depth != 8 || source.Header.ColorMode != 3 {
		return nil, sceneError(psdfile.ErrUnsupported, "tile.header", 0, "RGB8 source required")
	}
	if source.ActiveLayerSet >= 0 && source.ActiveLayerSet < len(source.LayerSets) &&
		source.LayerSets[source.ActiveLayerSet].Depth != 8 {
		return nil, sceneError(psdfile.ErrUnsupported, "tile.layerSets", 0, "active layer depth differs from header")
	}
	engine := rgbTileRenderer{source: source, settings: settings, window: rectangle,
		nodes: make(map[*psdmodel.Layer]int), maskIDs: make(map[*psdmodel.Layer]int16),
		patterns: patternSceneSources{tiles: make(map[string]*image.NRGBA)},
		live:     make(map[*psdmodel.Layer]bool), contents: make(map[string]*image.NRGBA),
		working: int64(len(settings.OutputProfile)), legacy: renderer{
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
	if settings.sourceRenderer != nil {
		engine.legacy.active = settings.sourceRenderer.active
		engine.legacy.documents = settings.sourceRenderer.documents
		defer func() { settings.sourceRenderer.documents = engine.legacy.documents }()
	}
	release, err := engine.enterOpenTypeDocument()
	if err != nil {
		return nil, err
	}
	defer release()
	if settings.ApproximateOpenTypeText != nil {
		if err := engine.prepareOpenTypeMetadata(ctx); err != nil {
			return nil, err
		}
	}
	layers, err := engine.tree(ctx)
	if err != nil {
		return nil, err
	}
	if err := engine.prepareSourceArtboards(ctx, layers); err != nil {
		return nil, err
	}
	if settings.ApproximateOpenTypeText != nil {
		if err := engine.planOpenTypeLayers(ctx, layers, true); err != nil {
			return nil, err
		}
	}
	if err := engine.prepare(ctx, layers, true); err != nil {
		return nil, err
	}
	if settings.ApproximateOpenTypeText != nil {
		// The output reservation was held during source rendering; the compositor now budgets that allocation itself.
		engine.working -= int64(rectangle.Dx()) * int64(rectangle.Dy()) * 4
	}
	document := &psdmodel.Document{Width: int(source.Header.Width), Height: int(source.Header.Height), Layers: layers}
	composeOptions := settings.Compose
	available := settings.MaxWorkingBytes - engine.working
	if available <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "tile.composite", 0, "no compositor storage remains")
	}
	if composeOptions.MaxWorkingBytes == 0 || composeOptions.MaxWorkingBytes > available {
		composeOptions.MaxWorkingBytes = available
	}
	if composeOptions.MaxDepth == 0 || composeOptions.MaxDepth > settings.MaxDepth {
		composeOptions.MaxDepth = settings.MaxDepth
	}
	if composeOptions.MaxLayers == 0 || composeOptions.MaxLayers > settings.MaxLayers {
		composeOptions.MaxLayers = settings.MaxLayers
	}
	// Zero-filled regional snapshots allow the complete semantic and compositor budget preflight before sample I/O.
	if err := psdcompose.ValidateRegion(document, rectangle, composeOptions); err != nil {
		return nil, tileComposeError(err)
	}
	if err := engine.load(ctx, layers, true); err != nil {
		return nil, err
	}
	result, err := psdcompose.RenderRegion(ctx, document, rectangle, composeOptions)
	if err != nil {
		return nil, tileComposeError(err)
	}
	if settings.OutputProfile != nil {
		profile, err := engine.readProfile(ctx)
		if err != nil {
			return nil, err
		}
		if err := engine.reserve(int64(len(result.Pix)), "tile.color.pixels", 0); err != nil {
			return nil, err
		}
		engine.legacy.working = engine.working
		result, err = engine.legacy.convertProfile(ctx, result, profile, &sceneProfile{data: settings.OutputProfile})
		if err != nil {
			return nil, err
		}
	}
	if settings.ApproximateOpenTypeText != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (engine *rgbTileRenderer) reserve(amount int64, path string, offset int64) error {
	if amount < 0 || amount > engine.settings.MaxWorkingBytes-engine.working {
		return sceneError(psdfile.ErrLimit, path, offset, "tile metadata and regional samples exceed working budget")
	}
	engine.working += amount
	return nil
}

func (engine *rgbTileRenderer) tree(ctx context.Context) ([]*psdmodel.Layer, error) {
	var root []*psdmodel.Layer
	stack := []*[]*psdmodel.Layer{&root}
	for index, v := range slices.Backward(engine.source.Layers) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		available := engine.settings.MaxWorkingBytes - engine.working
		if available <= 0 {
			return nil, sceneError(psdfile.ErrLimit, "tile.metadata", 0, "no layer metadata budget remains")
		}
		view, err := engine.source.ReadLayerMetadata(ctx, index, psdfile.MetadataOptions{MaxBytes: available})
		if err != nil {
			return nil, err
		}
		if err := engine.reserve(view.MetadataBytes, "tile.metadata", v.Span.Offset); err != nil {
			return nil, err
		}
		if view.Divider == 3 {
			if len(stack) == 1 {
				return nil, sceneError(psdfile.ErrMalformed, "tile.groups", 0, "unbalanced group end")
			}
			slices.Reverse(*stack[len(stack)-1])
			stack = stack[:len(stack)-1]
			continue
		}
		if view.Divider > 3 || view.Layer == nil {
			return nil, sceneError(psdfile.ErrUnsupported, "tile.groups", 0, "unknown group divider or layer metadata")
		}
		layer := view.Layer
		layer.Unsupported = append(layer.Unsupported, view.BlockingIssues...)
		parent := stack[len(stack)-1]
		*parent = append(*parent, layer)
		engine.nodes[layer], engine.maskIDs[layer] = index, view.PixelMaskChannel
		if view.Divider == 1 || view.Divider == 2 {
			layer.Group = true
			layer.Children = make([]*psdmodel.Layer, 0)
			stack = append(stack, &layer.Children)
			if len(stack) > engine.settings.MaxDepth {
				return nil, sceneError(psdfile.ErrLimit, "tile.groups", 0, "group nesting exceeds tile limit")
			}
		}
	}
	if len(stack) != 1 {
		return nil, sceneError(psdfile.ErrMalformed, "tile.groups", 0, "unbalanced group start")
	}
	slices.Reverse(root)
	if len(root) != 0 && !root[0].Group && root[0].Children == nil {
		record := engine.source.Layers[engine.nodes[root[0]]]
		alpha := false
		for _, channel := range record.Channels {
			alpha = alpha || channel.ID == -1
		}
		root[0].Background = !alpha && len(record.Channels) >= 3
	}
	return root, nil
}

func (engine *rgbTileRenderer) prepare(ctx context.Context, layers []*psdmodel.Layer, ancestor bool) error {
	clippingVisible := false
	for _, layer := range layers {
		visible := ancestor && !layer.Hidden && (layer.Opacity == nil || *layer.Opacity != 0)
		if layer.Clipping {
			visible = visible && clippingVisible
		} else {
			clippingVisible = visible
		}
		if !visible {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if engine.nodes[layer] < 0 {
			continue
		}
		record := engine.source.Layers[engine.nodes[layer]]
		vectorRendered, err := engine.prepareVectorStroke(ctx, record, layer)
		if err != nil {
			return err
		}
		cached, err := engine.specialPolicy(ctx, record, layer)
		if err != nil {
			return err
		}
		if layer.Children == nil {
			engine.releaseOpenTypePlan(layer)
		}
		textRendered, err := engine.prepareOpenTypeLayer(ctx, record, layer)
		if err != nil {
			return err
		}
		if vectorRendered {
			engine.live[layer] = true
		} else if textRendered {
			engine.live[layer] = true
		} else if layer.Children != nil {
			if err := engine.prepare(ctx, layer.Children, visible); err != nil {
				return err
			}
		} else if layer.Placed != nil && !cached {
			if err := engine.preparePlaced(ctx, record, layer); err != nil {
				return err
			}
			engine.live[layer] = true
		} else if layer.FillColor == nil && layer.Gradient == nil && layer.Pattern == nil && layer.Adjustment == nil {
			window, err := engine.effectSourceWindow(layer)
			if err != nil {
				return err
			}
			bounds := record.Bounds.Intersect(window)
			layer.Bounds = bounds
			if !bounds.Empty() {
				if _, err := tileColorChannels(record); err != nil {
					return err
				}
				if err := engine.reserve(int64(bounds.Dx())*int64(bounds.Dy())*4,
					"tile.layer.pixels", record.Span.Offset); err != nil {
					return err
				}
			}
			layer.Pixels = image.NewNRGBA(bounds)
		}
		if layer.Children != nil {
			engine.releaseOpenTypePlan(layer)
		}
		if err := engine.preparePixelMask(record, layer); err != nil {
			return err
		}
		if layer.Pattern != nil {
			resolver := renderer{options: Options{MaxWorkingBytes: engine.settings.MaxWorkingBytes},
				working: engine.working}
			if err := resolver.renderPattern(ctx, engine.source, layer, &engine.patterns); err != nil {
				return err
			}
			engine.working = resolver.working
		}
	}
	return nil
}

func (engine *rgbTileRenderer) load(ctx context.Context, layers []*psdmodel.Layer, ancestor bool) error {
	clippingVisible := false
	for _, layer := range layers {
		visible := ancestor && !layer.Hidden && (layer.Opacity == nil || *layer.Opacity != 0)
		if layer.Clipping {
			visible = visible && clippingVisible
		} else {
			clippingVisible = visible
		}
		if !visible {
			continue
		}
		if engine.nodes[layer] < 0 {
			continue
		}
		record := engine.source.Layers[engine.nodes[layer]]
		if layer.Children != nil {
			if err := engine.load(ctx, layer.Children, visible); err != nil {
				return err
			}
		} else if !engine.live[layer] && layer.Pixels != nil && !layer.Bounds.Empty() {
			channels, err := tileColorChannels(record)
			if err != nil {
				return err
			}
			for pixel := 0; pixel < len(layer.Pixels.Pix); pixel += 4 {
				layer.Pixels.Pix[pixel+3] = 255
			}
			for component, channelIndex := range channels {
				if channelIndex < 0 {
					continue
				}
				plane, err := engine.channel(ctx, engine.nodes[layer], channelIndex,
					layer.Bounds.Sub(record.Bounds.Min))
				if err != nil {
					return err
				}
				for pixel, value := range plane.Data {
					if pixel&4095 == 0 {
						if err := ctx.Err(); err != nil {
							return err
						}
					}
					layer.Pixels.Pix[pixel*4+component] = value
				}
			}
		}
		if err := engine.loadPixelMask(ctx, record, layer); err != nil {
			return err
		}
	}
	return nil
}

func (engine *rgbTileRenderer) preparePixelMask(record psdfile.LayerRecord, layer *psdmodel.Layer) error {
	if layer.Mask == nil || layer.Mask.Disabled || engine.maskIDs[layer] == 0 {
		return nil
	}
	radius := 0
	if layer.Mask.Feather != nil && *layer.Mask.Feather != 0 {
		value := *layer.Mask.Feather
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1000 {
			return sceneError(psdfile.ErrLimit, "tile.mask.feather", 0, "mask feather exceeds bounded halo support")
		}
		radius = int(math.Ceil(value*1.1*3)) + 2
	}
	window := engine.window
	if activeLayerEffects(layer) {
		// Exterior effects shape alpha before final paint, so masks must cover the effect source window too.
		window = window.Union(layer.Bounds)
	}
	bounds := layer.Mask.Bounds.Intersect(window.Inset(-radius))
	layer.Mask.SampleBounds = &bounds
	if bounds.Empty() {
		return nil
	}
	if _, err := tileMaskChannel(record, engine.maskIDs[layer]); err != nil {
		return err
	}
	if err := engine.reserve(int64(bounds.Dx())*int64(bounds.Dy())*4,
		"tile.mask.pixels", record.Mask.Span.Offset); err != nil {
		return err
	}
	layer.Mask.Pixels = image.NewNRGBA(bounds)
	return nil
}

func (engine *rgbTileRenderer) loadPixelMask(
	ctx context.Context, record psdfile.LayerRecord, layer *psdmodel.Layer,
) error {
	if layer.Mask == nil || layer.Mask.Disabled || layer.Mask.Pixels == nil || engine.maskIDs[layer] == 0 {
		return nil
	}
	channelIndex, err := tileMaskChannel(record, engine.maskIDs[layer])
	if err != nil {
		return err
	}
	plane, err := engine.channel(ctx, engine.nodes[layer], channelIndex,
		layer.Mask.SampleBounds.Sub(layer.Mask.Bounds.Min))
	if err != nil {
		return err
	}
	for pixel, value := range plane.Data {
		if pixel&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		layer.Mask.Pixels.Pix[pixel*4] = value
	}
	return nil
}

func (engine *rgbTileRenderer) channel(
	ctx context.Context, layerIndex, channelIndex int, rectangle image.Rectangle,
) (*psdfile.Plane, error) {
	available := min(engine.settings.MaxWorkingBytes-engine.working, engine.source.Limits().MaxDecodeBytes)
	if available <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "tile.decode", 0, "no channel scratch budget remains")
	}
	options := engine.settings.Region
	if options.MaxWorkingBytes == 0 || options.MaxWorkingBytes > available {
		options.MaxWorkingBytes = available
	}
	return engine.source.DecodeChannelRegion(ctx, layerIndex, channelIndex, rectangle, options)
}

func tileColorChannels(record psdfile.LayerRecord) ([4]int, error) {
	indices := [4]int{-1, -1, -1, -1}
	for index, channel := range record.Channels {
		component := int(channel.ID)
		if component == -1 {
			component = 3
		} else if component < 0 || component > 2 {
			continue
		}
		if indices[component] != -1 {
			return indices, sceneError(psdfile.ErrMalformed, "tile.channels", channel.Data.Offset,
				"duplicate color or alpha channel")
		}
		indices[component] = index
	}
	for component := range 3 {
		if indices[component] < 0 {
			return indices, sceneError(psdfile.ErrMalformed, "tile.channels", record.Span.Offset,
				"required RGB channel missing")
		}
	}
	return indices, nil
}

func tileMaskChannel(record psdfile.LayerRecord, id int16) (int, error) {
	channelIndex := -1
	for index, channel := range record.Channels {
		if channel.ID != id {
			continue
		}
		if channelIndex != -1 {
			return 0, sceneError(psdfile.ErrMalformed, "tile.mask", channel.Data.Offset, "duplicate mask channel")
		}
		channelIndex = index
	}
	if channelIndex < 0 {
		return 0, sceneError(psdfile.ErrMalformed, "tile.mask", record.Span.Offset, "pixel mask channel missing")
	}
	return channelIndex, nil
}

func tileEffectHalo(layer *psdmodel.Layer) (int, error) {
	if layer.Effects == nil || !layer.Effects.Enabled || !layer.Effects.EffectsVisible {
		return 0, nil
	}
	scale := layer.Effects.Scale
	if scale == 0 {
		scale = 1
	}
	halo := float64(0)
	for _, effect := range layer.Effects.Items {
		if !effect.Enabled || effect.Opacity == 0 {
			continue
		}
		if effect.Kind == psdeffects.ColorOverlay {
			continue
		}
		amount := (math.Abs(effect.Distance)+effect.Size*3)*scale + 4
		if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 || amount > 10_000 {
			return 0, sceneError(psdfile.ErrLimit, "tile.effects", 0, "effect neighborhood exceeds bounded halo support")
		}
		halo = max(halo, amount)
	}
	return int(math.Ceil(halo)), nil
}

func activeLayerEffects(layer *psdmodel.Layer) bool {
	if layer.Effects == nil || !layer.Effects.Enabled || !layer.Effects.EffectsVisible {
		return false
	}
	for _, effect := range layer.Effects.Items {
		if effect.Enabled && effect.Opacity != 0 {
			return true
		}
	}
	return false
}

func (engine *rgbTileRenderer) effectSourceWindow(layer *psdmodel.Layer) (image.Rectangle, error) {
	if !engine.approximateShadowSource(layer) {
		halo, err := tileEffectHalo(layer)
		if err != nil {
			return image.Rectangle{}, err
		}
		return engine.window.Inset(-halo), nil
	}
	window := engine.window
	for _, effect := range layer.Effects.Items {
		if !effect.Enabled || effect.Opacity == 0 || effect.Kind != psdeffects.DropShadow {
			continue
		}
		bounds, err := psdeffects.DropShadowSourceBounds(engine.window, effect,
			layer.Effects.Scale, layer.Effects.GlobalAngle, psdeffects.Options{ApproximateDropShadow: true})
		if err != nil {
			kind := psdfile.ErrMalformed
			if errors.Is(err, psdeffects.ErrLimit) {
				kind = psdfile.ErrLimit
			} else if errors.Is(err, psdeffects.ErrUnsupported) {
				kind = psdfile.ErrUnsupported
			}
			return image.Rectangle{}, &psdfile.Error{Kind: kind, Feature: "scene", Path: "tile.effects",
				Detail: "cannot determine the source neighborhood for the requested effect window", Cause: err}
		}
		window = window.Union(bounds)
	}
	return window, nil
}

func (engine *rgbTileRenderer) approximateShadowSource(layer *psdmodel.Layer) bool {
	if !engine.settings.Compose.ApproximateDropShadow || !activeLayerEffects(layer) {
		return false
	}
	shadow, overlay := false, false
	for _, effect := range layer.Effects.Items {
		if !effect.Enabled || effect.Opacity == 0 {
			continue
		}
		switch effect.Kind {
		case psdeffects.DropShadow:
			shadow = true
		case psdeffects.ColorOverlay:
			overlay = true
		default:
			return false
		}
	}
	return shadow && (!overlay || layer.Placed == nil &&
		(layer.BlendMode == "" || layer.BlendMode == "normal" || layer.BlendMode == "norm"))
}
