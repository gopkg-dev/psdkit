package psdscene

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/internal/psdrgb16normal"
	"github.com/gopkg-dev/psdkit/psdprecision"
)

// qualifyRGB16Difference uses whole original bounds before any source window is decoded.
// Wider Normal eligibility and older opaque eligibility remain separate contracts.
func (engine *precisionSourceRenderer) qualifyRGB16Difference(
	ctx context.Context, layers []*psdprecision.Layer,
) error {
	if !engine.nativeRGB16 || engine.depth != 16 || engine.source.Header.ColorMode != 3 {
		return nil
	}
	active, difference := 0, false
	var coverage image.Rectangle
	for _, layer := range layers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if layer.Hidden || *layer.Opacity == 0 {
			continue
		}
		active++
		if active == 1 && layer.BlendMode != "norm" ||
			layer.BlendMode != "norm" && layer.BlendMode != "diff" || layer.Bounds.Empty() ||
			layer.Children != nil || layer.Clipping || !engine.knownInactiveRGB16Mask(layer) || len(layer.BlendRanges) != 0 {
			return nil
		}
		if _, err := psdrgb16normal.ByteOpacity(*layer.Opacity); err != nil {
			return nil
		}
		difference = difference || layer.BlendMode == "diff"
		coverage = coverage.Union(layer.Bounds)
	}
	engine.nativeRGB16Difference = active != 0 && difference
	if engine.nativeRGB16Difference {
		engine.activeRGB16Bounds = coverage
	}
	return nil
}
