package psdscene

import (
	"context"
	"errors"
	"fmt"
	"image"

	"github.com/gopkg-dev/psdkit/internal/psdrgb16normal"
	"github.com/gopkg-dev/psdkit/psdprecision"
)

// qualifyRGB16Normal checks the wider Normal and limited Difference contracts using original source metadata.
func (engine *precisionSourceRenderer) qualifyRGB16Normal(
	ctx context.Context, layers []*psdprecision.Layer,
) error {
	if err := engine.qualifyRGB16Difference(ctx, layers); err != nil {
		return err
	}
	if !engine.nativeRGB16 || engine.depth != 16 || engine.source.Header.ColorMode != 3 {
		return nil
	}
	first := true
	var coverage image.Rectangle
	for _, layer := range layers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if layer.Hidden || *layer.Opacity == 0 {
			continue
		}
		if layer.Children != nil || layer.Clipping || layer.Mask != nil || len(layer.BlendRanges) != 0 ||
			layer.BlendMode != "norm" {
			return nil
		}
		if _, err := psdrgb16normal.ByteOpacity(*layer.Opacity); err != nil {
			return nil
		}
		if first && layer.Bounds.Empty() {
			// Empty-first initialization is unproven; keep the mathematical float path in every window.
			engine.nativeRGB16 = false
			return nil
		}
		first = false
		coverage = coverage.Union(layer.Bounds)
	}
	// An empty active scene keeps the existing mathematical float behavior; native transparent save is unproven.
	engine.nativeRGB16Normal = !first
	if engine.nativeRGB16Normal {
		engine.activeRGB16Bounds = coverage
	}
	return nil
}

// composeRGB16Normal uses the private RGB16 decoder's absolute snapshots for qualified Normal/Difference.
// Active bounds and the first active raster come from the original document, not clipped window snapshots.
func (engine *precisionSourceRenderer) composeRGB16Normal(
	ctx context.Context, scene *psdprecision.Scene, rectangle image.Rectangle,
) (*psdprecision.FloatImage, error) {
	if !engine.nativeRGB16Normal && !engine.nativeRGB16Difference || engine.source == nil || engine.depth != 16 ||
		engine.source.Header.ColorMode != 3 {
		return nil, fmt.Errorf("%w: missing qualified RGB16 Normal source", psdprecision.ErrUnsupported)
	}
	area := rectangle.Dx() * rectangle.Dy()
	result := &psdprecision.FloatImage{Width: rectangle.Dx(), Height: rectangle.Dy(),
		Encoding: psdprecision.EncodedRGB, SourceDepth: 16, Pix: make([]float64, area*4)}
	if err := initializeRGB16NormalSource(ctx, result, rectangle, engine.activeRGB16Bounds); err != nil {
		return nil, err
	}
	first := true
	for _, layer := range scene.Layers {
		if layer.Hidden || *layer.Opacity == 0 {
			continue
		}
		isBase := first
		first = false
		if layer.Pixels == nil {
			continue
		}
		opacity, err := psdrgb16normal.ByteOpacity(*layer.Opacity)
		if err != nil {
			return nil, normal16SourceInputError(err)
		}
		bounds := layer.Bounds.Intersect(rectangle)
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				if (x-bounds.Min.X)&4095 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				sourceOffset := ((y-layer.Bounds.Min.Y)*layer.Pixels.Width + x - layer.Bounds.Min.X) * 4
				targetOffset := ((y-rectangle.Min.Y)*result.Width + x - rectangle.Min.X) * 4
				source, err := psdrgb16normal.FromNormalized(layer.Pixels.Pix[sourceOffset : sourceOffset+4])
				if err != nil {
					return nil, normal16SourceInputError(err)
				}
				value := psdrgb16normal.Base(source, opacity)
				if !isBase {
					var backdrop psdrgb16normal.Pixel
					for component := range backdrop {
						backdrop[component] = uint32(result.Pix[targetOffset+component] * float64(psdrgb16normal.One))
					}
					if engine.nativeRGB16Difference && layer.BlendMode == "diff" {
						value = psdrgb16normal.OverDifference(backdrop, source, opacity)
					} else {
						value = psdrgb16normal.Over(backdrop, source, opacity)
					}
				}
				for component := range value {
					result.Pix[targetOffset+component] = float64(value[component]) / float64(psdrgb16normal.One)
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// initializeRGB16NormalSource uses the whole original active bounds before intersecting the output window.
func initializeRGB16NormalSource(
	ctx context.Context, result *psdprecision.FloatImage, rectangle, coverage image.Rectangle,
) error {
	bounds := coverage.Intersect(rectangle)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if (x-bounds.Min.X)&4095 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			offset := ((y-rectangle.Min.Y)*result.Width + x - rectangle.Min.X) * 4
			result.Pix[offset], result.Pix[offset+1], result.Pix[offset+2] = 1, 1, 1
		}
	}
	return nil
}

func normal16SourceInputError(err error) error {
	if errors.Is(err, psdrgb16normal.ErrNonFinite) {
		return psdprecision.ErrNonFinite
	}
	return fmt.Errorf("%w: RGB16 Normal source decoder lost canonical codes", psdprecision.ErrUnsupported)
}
