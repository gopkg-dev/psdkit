package psdscene

import (
	"context"
	"image"
	"math"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func openTypeTextOptionsBytes(options *OpenTypeTextRenderOptions) int64 {
	amount := int64(unsafe.Sizeof(OpenTypeTextRenderOptions{}))
	if options.Bounds != nil {
		amount += int64(unsafe.Sizeof(image.Rectangle{}))
	}
	return amount + int64(cap(options.Shape.Features))*int64(unsafe.Sizeof(psdfont.ShapeFeature{}))
}

func ownOpenTypeTextOptions(
	options *OpenTypeTextRenderOptions, budget int64,
) (*OpenTypeTextRenderOptions, int64, error) {
	if options.Kerning != OpenTypeTextNone && options.Kerning != OpenTypeTextMetrics {
		return nil, 0, sceneError(psdfile.ErrUnsupported, "text.policy", 0, "choose explicit None or Metrics")
	}
	if options.Samples != 0 && options.Samples != 4 && options.Samples != 8 {
		return nil, 0, sceneError(psdfile.ErrUnsupported, "text.policy", 0,
			"coverage supports only four or eight samples per axis")
	}
	if options.StyleBoundaries > OpenTypeCompatibleStyleRuns {
		return nil, 0, sceneError(psdfile.ErrUnsupported, "text.policy", 0, "unknown style boundary policy")
	}
	if options.ClusterPaint > OpenTypeFirstStyleClusterPaint ||
		options.ClusterPaint == OpenTypeFirstStyleClusterPaint && options.StyleBoundaries != OpenTypeCompatibleStyleRuns {
		return nil, 0, sceneError(psdfile.ErrUnsupported, "text.policy", 0,
			"choose a known cluster paint policy; FirstStyle requires compatible style runs")
	}
	if options.MaxWorkingBytes < 0 || options.MaxGlyphs < 0 {
		return nil, 0, sceneError(psdfile.ErrLimit, "text.policy", 0, "text limits must not be negative")
	}
	if err := validateOpenTypeTypographyPolicy(*options); err != nil {
		return nil, 0, err
	}
	amount := int64(unsafe.Sizeof(OpenTypeTextRenderOptions{}))
	if options.Bounds != nil {
		amount += int64(unsafe.Sizeof(image.Rectangle{}))
	}
	featureBytes := int64(unsafe.Sizeof(psdfont.ShapeFeature{}))
	if amount > budget || int64(len(options.Shape.Features)) > (budget-amount)/featureBytes {
		return nil, 0, sceneError(psdfile.ErrLimit, "text.policy", 0, "owned text options exceed working budget")
	}
	amount += int64(len(options.Shape.Features)) * featureBytes
	owned := *options
	if options.Bounds != nil {
		bounds := *options.Bounds
		owned.Bounds = &bounds
	}
	owned.Shape.Features = make([]psdfont.ShapeFeature, len(options.Shape.Features))
	copy(owned.Shape.Features, options.Shape.Features)
	return &owned, amount, nil
}

func (engine *rgbTileRenderer) prepareOpenTypeMetadata(ctx context.Context) error {
	width, height := int64(engine.window.Dx()), int64(engine.window.Dy())
	remaining := engine.settings.MaxWorkingBytes - engine.working
	optionsBytes := openTypeTextOptionsBytes(engine.settings.ApproximateOpenTypeText) +
		int64(cap(engine.settings.OutputProfile)-len(engine.settings.OutputProfile))
	indexBytes := int64(len(engine.source.Layers)) * int64(unsafe.Sizeof(int(0)))
	if optionsBytes > remaining || indexBytes > remaining-optionsBytes || height != 0 &&
		width > (remaining-optionsBytes-indexBytes)/4/height {
		return sceneError(psdfile.ErrLimit, "tile.text.output", 0,
			"owned options, physical text index and final tile output exceed working budget")
	}
	if err := engine.reserve(optionsBytes+indexBytes+width*height*4, "tile.text.output", 0); err != nil {
		return err
	}
	engine.textIDs = make([]int, len(engine.source.Layers))
	for index := range engine.textIDs {
		engine.textIDs[index] = -1
	}
	remaining = engine.settings.MaxWorkingBytes - engine.working
	if remaining <= 0 {
		return sceneError(psdfile.ErrLimit, "tile.text.metadata", 0, "no text metadata budget remains")
	}
	view, err := engine.source.ReadTextLayerView(ctx, psdfile.MetadataOptions{MaxBytes: remaining})
	if err != nil {
		return err
	}
	if err := engine.reserve(view.MetadataBytes, "tile.text.metadata", 0); err != nil {
		return err
	}
	engine.texts = view.Records
	for index, record := range view.Records {
		if record.LayerSetIndex != engine.source.ActiveLayerSet {
			continue
		}
		if record.LayerIndex < 0 || record.LayerIndex >= len(engine.textIDs) ||
			engine.textIDs[record.LayerIndex] != -1 {
			return sceneError(psdfile.ErrUnsupported, "tile.text.metadata", record.Block.Data.Offset,
				"text blocks do not identify unique physical source leaves")
		}
		engine.textIDs[record.LayerIndex] = index
	}
	return nil
}

func (engine *rgbTileRenderer) planOpenTypeLayers(
	ctx context.Context, layers []*psdmodel.Layer, ancestor bool,
) error {
	if engine.textPlan == nil {
		amount := int64(len(engine.source.Layers)) * int64(unsafe.Sizeof(int64(0)))
		if err := engine.reserve(amount, "tile.text.plan", 0); err != nil {
			return err
		}
		engine.textPlan = make([]int64, len(engine.source.Layers))
	}
	clippingVisible := false
	canvas := image.Rect(0, 0, int(engine.source.Header.Width), int(engine.source.Header.Height))
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
		index := engine.nodes[layer]
		if index < 0 {
			continue
		}
		record := engine.source.Layers[index]
		cached, err := engine.specialPolicy(ctx, record, layer)
		if err != nil {
			return err
		}
		halo, err := tileEffectHalo(layer)
		if err != nil {
			return err
		}
		if len(layer.Unsupported) != 0 {
			return sceneError(psdfile.ErrUnsupported, "tile.text.plan", record.Span.Offset,
				"visible source metadata has unsupported semantics")
		}
		if layer.Children != nil {
			if err := engine.planOpenTypeLayers(ctx, layer.Children, visible); err != nil {
				return err
			}
		} else if engine.textIDs[index] != -1 {
			bounds := engine.window.Inset(-halo).Intersect(canvas)
			if limit := engine.settings.ApproximateOpenTypeText.Bounds; limit != nil {
				bounds = bounds.Intersect(*limit)
			}
			if err := engine.planOpenTypeRectangle(index, bounds, record.Span.Offset); err != nil {
				return err
			}
		} else if layer.Placed != nil && !cached {
			if engine.placedPlans == nil {
				amount := int64(len(engine.source.Layers)) * int64(unsafe.Sizeof(placedTilePlan{}))
				if err := engine.reserve(amount, "tile.text.placementPlan", record.Span.Offset); err != nil {
					return err
				}
				engine.placedPlans = make([]placedTilePlan, len(engine.source.Layers))
			}
			compiled, err := engine.compilePlacedPlan(ctx, record, layer)
			if err != nil {
				return err
			}
			if !compiled.bounds.Empty() {
				if err := engine.linkedRecords(ctx); err != nil {
					return err
				}
				linked, exists := engine.linked[compiled.placement.SourceID]
				if !exists || linked.Kind != "liFD" && (linked.Kind != "liFE" ||
					engine.settings.ExternalResolver == nil && !engine.legacy.resolvesExternalRaster(linked)) {
					return sceneError(psdfile.ErrUnsupported, "tile.text.placedSource", record.Span.Offset,
						"original placed source is absent or requires an explicit resolver")
				}
			}
			engine.placedPlans[index] = compiled
			if err := engine.planOpenTypeRectangle(index, compiled.bounds, record.Span.Offset); err != nil {
				return err
			}
		} else if cached || layer.FillColor == nil && layer.Gradient == nil && layer.Pattern == nil &&
			layer.Adjustment == nil {
			bounds := record.Bounds.Intersect(engine.window.Inset(-halo))
			if !bounds.Empty() {
				if _, err := tileColorChannels(record); err != nil {
					return err
				}
			}
			if err := engine.planOpenTypeRectangle(index, bounds, record.Span.Offset); err != nil {
				return err
			}
		}
		if layer.Mask != nil && !layer.Mask.Disabled && engine.maskIDs[layer] != 0 {
			radius := 0
			if layer.Mask.Feather != nil && *layer.Mask.Feather != 0 {
				value := *layer.Mask.Feather
				if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1000 {
					return sceneError(psdfile.ErrLimit, "tile.mask.feather", 0,
						"mask feather exceeds bounded halo support")
				}
				radius = int(math.Ceil(value*1.1*3)) + 2
			}
			bounds := layer.Mask.Bounds.Intersect(engine.window.Inset(-radius))
			if !bounds.Empty() {
				if _, err := tileMaskChannel(record, engine.maskIDs[layer]); err != nil {
					return err
				}
			}
			if err := engine.planOpenTypeRectangle(index, bounds, record.Mask.Span.Offset); err != nil {
				return err
			}
		}
	}
	return nil
}

func (engine *rgbTileRenderer) planOpenTypeRectangle(index int, bounds image.Rectangle, at int64) error {
	width, height := int64(bounds.Dx()), int64(bounds.Dy())
	if bounds.Empty() {
		return nil
	}
	available := engine.settings.MaxWorkingBytes - engine.working
	if height <= 0 || width <= 0 || width > available/4/height {
		return sceneError(psdfile.ErrLimit, "tile.text.plan", at,
			"all retained layer samples exceed the working budget before font resolution")
	}
	amount := width * height * 4
	if err := engine.reserve(amount, "tile.text.plan", at); err != nil {
		return err
	}
	engine.textPlan[index] += amount
	return nil
}

func (engine *rgbTileRenderer) releaseOpenTypePlan(layer *psdmodel.Layer) {
	if engine.settings.ApproximateOpenTypeText == nil {
		return
	}
	index := engine.nodes[layer]
	engine.working -= engine.textPlan[index]
	engine.textPlan[index] = 0
}

func (engine *rgbTileRenderer) prepareOpenTypeLayer(
	ctx context.Context, record psdfile.LayerRecord, layer *psdmodel.Layer,
) (bool, error) {
	if engine.settings.ApproximateOpenTypeText == nil {
		return false, nil
	}
	physicalIndex := engine.nodes[layer]
	if physicalIndex < 0 || physicalIndex >= len(engine.textIDs) || engine.textIDs[physicalIndex] == -1 {
		return false, nil
	}
	if layer.Group || layer.Children != nil || layer.Placed != nil || layer.FillColor != nil ||
		layer.Gradient != nil || layer.Pattern != nil || layer.Adjustment != nil {
		return false, sceneError(psdfile.ErrUnsupported, "tile.text.source", record.Span.Offset,
			"text must be the only artwork source on its physical leaf")
	}
	halo, err := tileEffectHalo(layer)
	if err != nil {
		return false, err
	}
	canvas := image.Rect(0, 0, int(engine.source.Header.Width), int(engine.source.Header.Height))
	bounds := engine.window.Inset(-halo).Intersect(canvas)
	options := *engine.settings.ApproximateOpenTypeText
	if options.Bounds != nil {
		bounds = bounds.Intersect(*options.Bounds)
	}
	options.Bounds = &bounds
	available := engine.settings.MaxWorkingBytes - engine.working
	if available <= 0 {
		return false, sceneError(psdfile.ErrLimit, "tile.text.render", record.Span.Offset,
			"no OpenType glyph working budget remains")
	}
	if options.MaxWorkingBytes == 0 || options.MaxWorkingBytes > available {
		options.MaxWorkingBytes = available
	}
	text := engine.texts[engine.textIDs[physicalIndex]]
	pixels, err := RenderOpenTypeTextLayer(ctx, engine.source, text, engine.settings.FontResolver, options)
	if err != nil {
		return false, err
	}
	if err := engine.reserve(int64(cap(pixels.Pix)), "tile.text.pixels", record.Span.Offset); err != nil {
		return false, err
	}
	layer.Pixels, layer.Bounds = pixels, pixels.Rect
	return true, nil
}
