package psdprecision

import (
	"context"
	"fmt"
	"image"
	"math"
)

const rgb16One uint32 = 32768

// RenderRGB16Opaque composites raw RGB16 source codes using Photoshop's encoded integer domain.
// Pixels must contain exact uint16 source codes divided by 65535 and have full alpha.
// Active layers must be flat, unmasked, unclipped and without Blend If; opacity must be a byte divided by 255.
// The bottom layer must be opaque Normal and cover the canvas. Normal, Multiply, Screen, Difference and
// Linear Dodge, Darken and Lighten are supported. Darken and Lighten select per-channel minimum or maximum.
// Returned colors are internal 0..32768 codes divided by 32768.
// Saving these colors as source uint16 codes uses nearest(color*65535), including 16384 mapping to 32768.
// General floating-point scenes retain their independent Render contract.
func RenderRGB16Opaque(ctx context.Context, scene *Scene, options ...Options) (*FloatImage, error) {
	if scene == nil {
		return nil, fmt.Errorf("%w: missing RGB16 source scene", ErrInvalid)
	}
	return renderRGB16Integer(ctx, scene, image.Rect(0, 0, scene.Width, scene.Height), false, options)
}

// RenderRGB16OpaqueRegion applies the calibrated opaque RGB16 integer compositor to an absolute window.
// Source bounds remain absolute, and only the selected window contributes to output allocation budgets.
// The active bottom layer must cover the complete window; all RenderRGB16Opaque restrictions still apply.
func RenderRGB16OpaqueRegion(
	ctx context.Context, scene *Scene, rectangle image.Rectangle, options ...Options,
) (*RegionImage, error) {
	result, err := renderRGB16Integer(ctx, scene, rectangle, false, options)
	if err != nil {
		return nil, err
	}
	return &RegionImage{Bounds: rectangle, Image: result}, nil
}

func renderRGB16Integer(
	ctx context.Context, scene *Scene, rectangle image.Rectangle, sourceAlpha bool, options []Options,
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
		return nil, fmt.Errorf("%w: RGB16 window must lie inside an encoded scene", ErrInvalid)
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
	if err := validateRGB16Integer(ctx, scene.Layers, rectangle, sourceAlpha); err != nil {
		return nil, err
	}
	result := &FloatImage{Width: rectangle.Dx(), Height: rectangle.Dy(), Encoding: EncodedRGB,
		SourceDepth: 16, Pix: make([]float64, area*4)}
	for _, layer := range scene.Layers {
		if layer.Hidden || layerOpacity(layer) == 0 || layer.Pixels == nil {
			continue
		}
		opacity := rgb16ByteOpacity(layerOpacity(layer))
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
				coverage := opacity
				if sourceAlpha {
					rawAlpha := uint32(math.Round(layer.Pixels.Pix[sourceOffset+3] * 65535))
					alpha := (rawAlpha + 1) / 2
					coverage = (alpha*opacity + rgb16One/2) / rgb16One
				}
				for component := range 3 {
					raw := uint32(math.Round(layer.Pixels.Pix[sourceOffset+component] * 65535))
					source := (raw + 1) / 2
					backdrop := uint32(result.Pix[targetOffset+component] * float64(rgb16One))
					mixed := rgb16Blend(backdrop, source, normalizedBlend(layer.BlendMode))
					value := (backdrop*(rgb16One-coverage) + mixed*coverage + rgb16One/2) / rgb16One
					result.Pix[targetOffset+component] = float64(value) / float64(rgb16One)
				}
				result.Pix[targetOffset+3] = 1
			}
		}
	}
	return result, ctx.Err()
}

func validateRGB16Integer(
	ctx context.Context, layers []*Layer, rectangle image.Rectangle, sourceAlpha bool,
) error {
	bottom := true
	for _, layer := range layers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if layer.Hidden || layerOpacity(layer) == 0 {
			continue
		}
		if layer.Children != nil || layer.Clipping || layer.Mask != nil && !layer.Mask.Disabled ||
			len(layer.BlendRanges) != 0 {
			return fmt.Errorf("%w: RGB16 integer composition requires ordinary opaque raster layers", ErrUnsupported)
		}
		if layer.Pixels == nil || layer.Bounds.Intersect(rectangle).Empty() {
			continue
		}
		mode := normalizedBlend(layer.BlendMode)
		if mode != "norm" && mode != "mul" && mode != "scrn" && mode != "diff" && mode != "lddg" &&
			mode != "dark" && mode != "lite" {
			return fmt.Errorf("%w: RGB16 integer blend mode %q", ErrUnsupported, mode)
		}
		opacity := layerOpacity(layer)
		if float64(uint32(math.Round(opacity*255)))/255 != opacity {
			return fmt.Errorf("%w: RGB16 integer opacity must come from a source byte", ErrUnsupported)
		}
		if bottom && (mode != "norm" || opacity != 1 || !rectangle.In(layer.Bounds)) {
			return fmt.Errorf("%w: RGB16 integer backdrop must be opaque Normal and cover the window", ErrUnsupported)
		}
		isBottom := bottom
		bottom = false
		for offset := 0; offset < len(layer.Pixels.Pix); offset += 4 {
			if offset&16383 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			alpha := layer.Pixels.Pix[offset+3]
			if math.IsNaN(alpha) || math.IsInf(alpha, 0) {
				return ErrNonFinite
			}
			if (!sourceAlpha || isBottom) && alpha != 1 {
				return fmt.Errorf("%w: RGB16 integer source alpha is not opaque", ErrUnsupported)
			}
			if alpha < 0 || alpha > 1 || float64(uint32(math.Round(alpha*65535)))/65535 != alpha {
				return fmt.Errorf("%w: RGB16 alpha must retain an exact source uint16 code value", ErrUnsupported)
			}
			for component := range 3 {
				value := layer.Pixels.Pix[offset+component]
				if math.IsNaN(value) || math.IsInf(value, 0) {
					return ErrNonFinite
				}
				if value < 0 || value > 1 || float64(uint32(math.Round(value*65535)))/65535 != value {
					return fmt.Errorf("%w: RGB16 colors must retain exact source uint16 code values", ErrUnsupported)
				}
			}
		}
	}
	if bottom && !rectangle.Empty() {
		return fmt.Errorf("%w: RGB16 integer scene has no opaque backdrop", ErrUnsupported)
	}
	return nil
}

func rgb16ByteOpacity(opacity float64) uint32 {
	value := uint32(math.Round(opacity * 255))
	return (value*rgb16One + 127) / 255
}

func rgb16Blend(backdrop, source uint32, mode string) uint32 {
	switch mode {
	case "dark":
		return min(backdrop, source)
	case "lite":
		return max(backdrop, source)
	case "mul":
		return (backdrop*source + rgb16One/2) / rgb16One
	case "scrn":
		return rgb16One - ((rgb16One-backdrop)*(rgb16One-source)+rgb16One/2)/rgb16One
	case "diff":
		if backdrop > source {
			return backdrop - source
		}
		return source - backdrop
	case "lddg":
		return min(rgb16One, backdrop+source)
	default:
		return source
	}
}
