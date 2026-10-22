package psdscene

import (
	"context"
	"slices"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func (engine *rgbTileRenderer) specialPolicy(
	ctx context.Context, record psdfile.LayerRecord, layer *psdmodel.Layer,
) (bool, error) {
	text, stroke, gradient, pattern := false, false, false, false
	for _, block := range record.AdditionalInfo {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		switch block.Key {
		case "TySh", "tySh":
			text = true
		case "vscg", "vstk":
			stroke = true
		case "GdFl":
			gradient = true
		case "PtFl":
			pattern = true
		}
	}
	if stroke && engine.settings.ApproximateVectorStroke {
		if text || gradient || pattern {
			return false, vectorStrokeUnsupported(record.Span.Offset, "multiple artwork sources on a vector leaf")
		}
		// Planning keeps the original source; prepareVectorStroke validates and renders before compositor preflight.
		return false, nil
	}
	if engine.settings.ApproximateOpenTypeText != nil {
		if text {
			if stroke || gradient || pattern || layer.Group || layer.Children != nil || layer.FillColor != nil ||
				layer.Adjustment != nil || layer.Pattern != nil || layer.Gradient != nil || layer.Placed != nil {
				return false, sceneError(psdfile.ErrUnsupported, "tile.text.source", record.Span.Offset,
					"one live text source must be the only artwork source on its physical leaf")
			}
			return false, nil
		}
		if layer.Placed != nil {
			return false, nil
		}
	}
	if (text && !engine.settings.CachedTextLayers || stroke) && !engine.settings.CachedSpecialLayers {
		return false, sceneError(psdfile.ErrUnsupported, "tile.layers.semantic", record.Span.Offset,
			"text or stroke layers require explicit CachedSpecialLayers selection")
	}
	cached := layer.Placed != nil && engine.settings.CachedPlacedLayers || text && engine.settings.CachedTextLayers ||
		engine.settings.CachedSpecialLayers && (text || stroke || gradient || pattern)
	if !cached {
		return false, nil
	}
	if layer.Group || layer.Children != nil || record.Bounds.Empty() ||
		(gradient || pattern) && (gradient && pattern || text || layer.FillColor != nil ||
			layer.Placed != nil || layer.Adjustment != nil) {
		return false, sceneError(psdfile.ErrUnsupported, "tile.layers.cachedSource", record.Span.Offset,
			"saved layer raster selection has ambiguous or absent source geometry")
	}
	if _, err := tileColorChannels(record); err != nil {
		return false, sceneError(psdfile.ErrUnsupported, "tile.layers.cachedSource", record.Span.Offset,
			"saved layer RGB channels are absent or ambiguous")
	}
	if engine.settings.CachedPlacedLayers {
		layer.Placed = nil
	}
	if engine.settings.CachedSpecialLayers {
		if gradient {
			layer.Gradient = nil
		}
		if pattern {
			layer.Pattern, layer.PatternTile = nil, nil
		}
		layer.Unsupported = slices.DeleteFunc(layer.Unsupported, func(reason string) bool {
			return gradient && reason == "unsupported gradient descriptor" ||
				pattern && reason == "unsupported pattern fill descriptor"
		})
	}
	return true, nil
}
