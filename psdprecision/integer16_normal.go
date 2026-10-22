package psdprecision

import (
	"context"
	"errors"
	"fmt"
	"image"

	"github.com/gopkg-dev/psdkit/internal/psdrgb16normal"
)

// RenderRGB16Normal composites canonical raw RGB16 RGBA using Photoshop's encoded Normal integer arithmetic.
// Active layers must be ordinary Normal rasters without masks, clipping, groups or Blend If.
// Source components must be exact uint16 codes divided by 65535, and opacity must be a byte divided by 255.
// Active rasters may have partial bounds, alpha and opacity. Hidden and zero-opacity layers are skipped.
// The global active bounds' bounding rectangle starts with white RGB and zero alpha; the first active raster
// replaces that background only within its own bounds. The first raster is selected before any ROI is considered.
// Returned RGB and alpha are 0..32768 codes divided by 32768; zero alpha retains the current straight RGB.
// Save source codes with nearest(component*65535). An empty active stack returns mathematical RGBA zero.
// Outside the active bounding rectangle, mathematical RGBA zero is an output policy, not a native stored-code claim.
// This contract does not establish native whole-canvas transparent save behavior.
// A first active raster with empty bounds is unsupported because its native initialization behavior is unproven.
func RenderRGB16Normal(ctx context.Context, scene *Scene, options ...Options) (*FloatImage, error) {
	if scene == nil {
		return nil, fmt.Errorf("%w: missing RGB16 Normal source scene", ErrInvalid)
	}
	return renderRGB16Normal(ctx, scene, image.Rect(0, 0, scene.Width, scene.Height), options)
}

// RenderRGB16NormalRegion applies the same whole-scene contract to an absolute output window.
// Active bounds and the first active raster are selected from the original whole scene before clipping to this window.
// Source and result bounds retain document coordinates; output allocation depends only on the window.
func RenderRGB16NormalRegion(
	ctx context.Context, scene *Scene, rectangle image.Rectangle, options ...Options,
) (*RegionImage, error) {
	result, err := renderRGB16Normal(ctx, scene, rectangle, options)
	if err != nil {
		return nil, err
	}
	return &RegionImage{Bounds: rectangle, Image: result}, nil
}

func renderRGB16Normal(
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
	if scene == nil || scene.Encoding != EncodedRGB || scene.Width < 0 || scene.Height < 0 ||
		rectangle.Min.X < 0 || rectangle.Min.Y < 0 || rectangle.Max.X < rectangle.Min.X ||
		rectangle.Max.Y < rectangle.Min.Y || rectangle.Max.X > scene.Width || rectangle.Max.Y > scene.Height {
		return nil, fmt.Errorf("%w: RGB16 Normal window must lie inside an encoded scene", ErrInvalid)
	}
	area, err := checkedArea(rectangle.Dx(), rectangle.Dy())
	if err != nil {
		return nil, err
	}
	if int64(area) > settings.MaxPixels || int64(area) > settings.MaxWorkingBytes/32 {
		return nil, fmt.Errorf("%w: RGB16 Normal output exceeds allocation budgets", ErrLimit)
	}
	plan := precisionPlan{scene: scene, options: settings, area: area, active: make(map[*Layer]bool)}
	if err := plan.validate(ctx, scene.Layers, 1, 0, true); err != nil {
		return nil, err
	}
	if err := validateRGB16Normal(ctx, scene); err != nil {
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
					value = psdrgb16normal.Over(backdrop, source, opacity)
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

func validateRGB16Normal(ctx context.Context, scene *Scene) error {
	first := true
	for _, layer := range scene.Layers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if layer.Hidden || layerOpacity(layer) == 0 {
			continue
		}
		if layer.Children != nil || layer.Clipping || layer.Mask != nil || len(layer.BlendRanges) != 0 ||
			normalizedBlend(layer.BlendMode) != "norm" {
			return fmt.Errorf("%w: RGB16 Normal requires ordinary unmasked Normal rasters", ErrUnsupported)
		}
		if _, err := psdrgb16normal.ByteOpacity(layerOpacity(layer)); err != nil {
			return normal16InputError(err)
		}
		if first && layer.Bounds.Empty() {
			return fmt.Errorf("%w: RGB16 Normal first active raster has empty bounds", ErrUnsupported)
		}
		first = false
		if layer.Pixels == nil {
			return fmt.Errorf("%w: RGB16 Normal raster has no source pixels", ErrUnsupported)
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
	return nil
}

// initializeRGB16Normal fills only the global active bounding rectangle intersecting the output window.
func initializeRGB16Normal(
	ctx context.Context, result *FloatImage, rectangle, coverage image.Rectangle,
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

func normal16InputError(err error) error {
	if errors.Is(err, psdrgb16normal.ErrNonFinite) {
		return ErrNonFinite
	}
	return fmt.Errorf("%w: RGB16 Normal requires exact source uint16 codes and byte opacity", ErrUnsupported)
}
