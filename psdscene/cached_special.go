package psdscene

import (
	"context"
	"slices"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// selectCachedSpecial always selects saved gradient/pattern rasters under the explicit cache policy.
// Text keeps its explicit live rendering priority; all unrelated unsupported metadata remains active.
func selectCachedSpecial(ctx context.Context, record psdfile.LayerRecord, layer *psdmodel.Layer,
	liveText bool,
) error {
	gradient, pattern, text, stroke := false, false, false, false
	for _, block := range record.AdditionalInfo {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch block.Key {
		case "GdFl":
			gradient = true
		case "PtFl":
			pattern = true
		case "TySh", "tySh":
			text = true
		case "vscg", "vstk":
			stroke = true
		}
	}
	if !gradient && !pattern && (!text || liveText) && !stroke {
		return nil
	}
	if layer == nil || layer.Group || layer.Children != nil || layer.Pixels == nil || record.Bounds.Empty() {
		return sceneError(psdfile.ErrUnsupported, "layers.cachedSource", record.Span.Offset,
			"special-layer cache requires a saved raster leaf")
	}
	if (gradient || pattern) && (gradient && pattern || text || layer.FillColor != nil ||
		layer.Placed != nil || layer.Adjustment != nil) {
		return sceneError(psdfile.ErrUnsupported, "layers.cachedSource", record.Span.Offset,
			"multiple layer source kinds have ambiguous cached semantics")
	}
	if err := cachedSpecialRaster(record, layer); err != nil {
		return err
	}
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
	return nil
}

func cachedSpecialRaster(record psdfile.LayerRecord, layer *psdmodel.Layer) error {
	seen := [4]bool{}
	for _, channel := range record.Channels {
		if channel.ID == -2 || channel.ID == -3 {
			continue
		}
		if channel.ID < -1 || channel.ID > 2 || seen[channel.ID+1] {
			return sceneError(psdfile.ErrUnsupported, "layers.cachedSource.channels", channel.Span.Offset,
				"cached raster channel identifiers are unsupported or ambiguous")
		}
		seen[channel.ID+1] = true
	}
	if !seen[1] || !seen[2] || !seen[3] {
		return sceneError(psdfile.ErrUnsupported, "layers.cachedSource.channels", record.Span.Offset,
			"special-layer cache is missing saved RGB channels")
	}
	width, height := record.Bounds.Dx(), record.Bounds.Dy()
	pixels := layer.Pixels
	if pixels.Rect.Dx() != width || pixels.Rect.Dy() != height || width > int(^uint(0)>>1)/4 ||
		pixels.Stride < width*4 || len(pixels.Pix) < width*4 ||
		height-1 > (len(pixels.Pix)-width*4)/pixels.Stride {
		return sceneError(psdfile.ErrMalformed, "layers.cachedSource.pixels", record.Span.Offset,
			"saved special-layer raster dimensions or storage are inconsistent")
	}
	return nil
}
