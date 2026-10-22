package psdprecision

import (
	"context"
	"fmt"
	"image"
)

// RegionImage owns a precise image and preserves the window's absolute document placement.
// Image samples are local row-major RGBA; Bounds.Min maps Image's first pixel into the document.
type RegionImage struct {
	Bounds image.Rectangle
	Image  *FloatImage
}

// RenderRegion composites only an absolute document-coordinate window.
// It borrows source samples and translates owned tree metadata, preserving all supported blend semantics.
// Allocation budgets apply to the window and compositor scratch, independently of the complete canvas size.
func RenderRegion(
	ctx context.Context, scene *Scene, rectangle image.Rectangle, options ...Options,
) (*RegionImage, error) {
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
	if scene == nil || scene.Width < 0 || scene.Height < 0 || rectangle.Min.X < 0 || rectangle.Min.Y < 0 ||
		rectangle.Max.X < rectangle.Min.X || rectangle.Max.Y < rectangle.Min.Y ||
		rectangle.Max.X > scene.Width || rectangle.Max.Y > scene.Height {
		return nil, fmt.Errorf("%w: window must lie inside the scene canvas", ErrInvalid)
	}
	count := 0
	seen := make(map[*Layer]bool)
	var translate func([]*Layer, int) ([]*Layer, error)
	translate = func(layers []*Layer, depth int) ([]*Layer, error) {
		if len(layers) != 0 && depth > settings.MaxDepth {
			return nil, fmt.Errorf("%w: tree exceeds MaxDepth", ErrLimit)
		}
		if len(layers) > settings.MaxLayers-count || int64(len(layers)) > settings.MaxWorkingBytes/512-int64(count) {
			return nil, fmt.Errorf("%w: translated tree exceeds metadata budget", ErrLimit)
		}
		result := make([]*Layer, 0, len(layers))
		for _, layer := range layers {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if layer == nil || seen[layer] {
				return nil, fmt.Errorf("%w: nil, cyclic or shared layer", ErrInvalid)
			}
			seen[layer] = true
			count++
			if count > settings.MaxLayers || int64(count) > settings.MaxWorkingBytes/512 {
				return nil, fmt.Errorf("%w: translated tree exceeds metadata budget", ErrLimit)
			}
			owned := *layer
			owned.Bounds = owned.Bounds.Sub(rectangle.Min)
			if layer.Mask != nil {
				mask := *layer.Mask
				mask.Bounds = mask.Bounds.Sub(rectangle.Min)
				owned.Mask = &mask
			}
			if layer.Children != nil {
				children, err := translate(layer.Children, depth+1)
				if err != nil {
					return nil, err
				}
				owned.Children = children
			}
			result = append(result, &owned)
		}
		return result, nil
	}
	layers, err := translate(scene.Layers, 1)
	if err != nil {
		return nil, err
	}
	settings.MaxWorkingBytes -= int64(count) * 512
	if settings.MaxWorkingBytes <= 0 {
		return nil, fmt.Errorf("%w: no compositor budget remains", ErrLimit)
	}
	window := &Scene{Width: rectangle.Dx(), Height: rectangle.Dy(), Encoding: scene.Encoding, Layers: layers}
	pixels, err := Render(ctx, window, settings)
	if err != nil {
		return nil, err
	}
	return &RegionImage{Bounds: rectangle, Image: pixels}, nil
}
