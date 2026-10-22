package psdprecision

import (
	"context"
	"fmt"
	"image"
)

// RenderRGB16OverOpaque composites canonical raw RGB16 layers over a completely opaque Normal bottom layer.
// Later layers may have partial source alpha; opacity and alpha multiply with nearest Q15 rounding before mixing.
// The bottom layer must cover the canvas at full opacity and alpha. Other RenderRGB16Opaque restrictions apply.
// Source RGBA components are exact uint16 codes divided by 65535; returned colors use the 0..32768 internal domain.
// The result remains opaque. General floating-point scenes retain their independent Render contract.
func RenderRGB16OverOpaque(ctx context.Context, scene *Scene, options ...Options) (*FloatImage, error) {
	if scene == nil {
		return nil, fmt.Errorf("%w: missing RGB16 source scene", ErrInvalid)
	}
	return renderRGB16Integer(ctx, scene, image.Rect(0, 0, scene.Width, scene.Height), true, options)
}

// RenderRGB16OverOpaqueRegion composites canonical raw RGB16 source RGBA in an absolute window.
// Only window pixels contribute to output allocation budgets, and the complete window must have an opaque bottom.
// Bounds preserve document coordinates, and all RenderRGB16OverOpaque restrictions still apply.
func RenderRGB16OverOpaqueRegion(
	ctx context.Context, scene *Scene, rectangle image.Rectangle, options ...Options,
) (*RegionImage, error) {
	result, err := renderRGB16Integer(ctx, scene, rectangle, true, options)
	if err != nil {
		return nil, err
	}
	return &RegionImage{Bounds: rectangle, Image: result}, nil
}
