package psdprecision

import (
	"context"
	"fmt"
	"image"

	"github.com/gopkg-dev/psdkit/internal/psdrgb16normal"
)

// RenderRGB16 applies the calibrated Normal/Difference subset to canonical encoded RGB16 rasters.
// Active rasters may have partial or negative-origin bounds; the first active mode must be Normal.
// The whole-scene MaxLayers limit bounds their count; there is no separate calibrated-layer-count ceiling.
// Active groups, masks, clipping and Blend If are unsupported; disabled masks do not affect eligibility.
// Hidden and zero-opacity layers are skipped.
// Source components must be exact uint16 codes divided by 65535 and opacity an exact byte divided by 255.
// Returned components are 0..32768 codes divided by 32768; zero alpha retains current straight RGB.
// The global active bounding rectangle starts with white RGB at alpha zero; the first active raster replaces
// it only inside that raster's bounds. Outside this rectangle, RGBA zero is a mathematical output policy.
// Native calibration includes representative 5/9/31-layer controls, not every possible sequence or layer count.
// Save storage codes with nearest(component*65535). RenderRGB16Normal retains its wider Normal geometry contract.
func RenderRGB16(ctx context.Context, scene *Scene, options ...Options) (*FloatImage, error) {
	if scene == nil {
		return nil, fmt.Errorf("%w: missing RGB16 source scene", ErrInvalid)
	}
	return renderRGB16(ctx, scene, image.Rect(0, 0, scene.Width, scene.Height), options)
}

// RenderRGB16Region applies the same whole-scene contract to an absolute output window.
// All original active rasters and samples are qualified before clipping to this window.
func RenderRGB16Region(
	ctx context.Context, scene *Scene, rectangle image.Rectangle, options ...Options,
) (*RegionImage, error) {
	result, err := renderRGB16(ctx, scene, rectangle, options)
	if err != nil {
		return nil, err
	}
	return &RegionImage{Bounds: rectangle, Image: result}, nil
}

func renderRGB16(
	ctx context.Context, scene *Scene, rectangle image.Rectangle, options []Options,
) (*FloatImage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	settings, err := renderOptions(options)
	if err != nil {
		return nil, err
	}
	if scene == nil || scene.Encoding != EncodedRGB || scene.Width <= 0 || scene.Height <= 0 ||
		rectangle.Min.X < 0 || rectangle.Min.Y < 0 || rectangle.Max.X < rectangle.Min.X ||
		rectangle.Max.Y < rectangle.Min.Y || rectangle.Max.X > scene.Width || rectangle.Max.Y > scene.Height {
		return nil, fmt.Errorf("%w: RGB16 window must lie inside a nonempty encoded scene", ErrInvalid)
	}
	area, err := checkedArea(rectangle.Dx(), rectangle.Dy())
	if err != nil {
		return nil, err
	}
	if int64(area) > settings.MaxPixels || int64(area) > settings.MaxWorkingBytes/32 {
		return nil, fmt.Errorf("%w: RGB16 output exceeds allocation budgets", ErrLimit)
	}
	plan := precisionPlan{scene: scene, options: settings, area: area, active: make(map[*Layer]bool)}
	if err := plan.validate(ctx, scene.Layers, 1, 0, true); err != nil {
		return nil, err
	}
	if err := validateRGB16(ctx, scene); err != nil {
		return nil, err
	}
	result := &FloatImage{Width: rectangle.Dx(), Height: rectangle.Dy(), Encoding: EncodedRGB,
		SourceDepth: 16, Pix: make([]float64, area*4)}
	var coverage image.Rectangle
	for _, layer := range scene.Layers {
		if !layer.Hidden && layerOpacity(layer) != 0 {
			coverage = coverage.Union(layer.Bounds)
		}
	}
	if err := initializeRGB16Normal(ctx, result, rectangle, coverage); err != nil {
		return nil, err
	}
	first := true
	for _, layer := range scene.Layers {
		if layer.Hidden || layerOpacity(layer) == 0 {
			continue
		}
		isBase := first
		first = false
		opacity, err := psdrgb16normal.ByteOpacity(layerOpacity(layer))
		if err != nil {
			return nil, normal16InputError(err)
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
					return nil, normal16InputError(err)
				}
				value := psdrgb16normal.Base(source, opacity)
				if !isBase {
					var backdrop psdrgb16normal.Pixel
					for component := range backdrop {
						backdrop[component] = uint32(result.Pix[targetOffset+component] * float64(psdrgb16normal.One))
					}
					if normalizedBlend(layer.BlendMode) == "diff" {
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

func validateRGB16(ctx context.Context, scene *Scene) error {
	active := 0
	for _, layer := range scene.Layers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if layer.Hidden || layerOpacity(layer) == 0 {
			continue
		}
		active++
		mode := normalizedBlend(layer.BlendMode)
		if active == 1 && mode != "norm" || mode != "norm" && mode != "diff" ||
			layer.Bounds.Empty() || layer.Children != nil || layer.Clipping || layer.Mask != nil && !layer.Mask.Disabled ||
			len(layer.BlendRanges) != 0 || layer.Pixels == nil {
			return fmt.Errorf("%w: RGB16 requires nonempty Normal/Difference rasters with first Normal",
				ErrUnsupported)
		}
		if _, err := psdrgb16normal.ByteOpacity(layerOpacity(layer)); err != nil {
			return normal16InputError(err)
		}
		for offset := 0; offset < len(layer.Pixels.Pix); offset += 4 {
			if offset&16383 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if _, err := psdrgb16normal.FromNormalized(layer.Pixels.Pix[offset : offset+4]); err != nil {
				return normal16InputError(err)
			}
		}
	}
	if active == 0 {
		return fmt.Errorf("%w: RGB16 requires an active ordinary raster", ErrUnsupported)
	}
	return nil
}
