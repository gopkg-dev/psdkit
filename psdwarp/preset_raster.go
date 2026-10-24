package psdwarp

import (
	"context"
	"image"
)

// ProjectMathematicalWithOptions samples certified preset geometry with the mathematical patch rasterizer.
// Callers must explicitly select this approximation; Photoshop-native pixel equivalence is unverified.
// The current operation's MaxBytes includes all retained preset geometry, output and raster scratch.
// Input pixels, compiled geometry and its original options remain unchanged.
func (g *PresetGeometry) ProjectMathematicalWithOptions(
	ctx context.Context, source *image.NRGBA, canvas image.Rectangle, options ...Options,
) (*image.NRGBA, image.Point, error) {
	ctx = warpContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, image.Point{}, err
	}
	if g == nil || g.patch == nil {
		return nil, image.Point{}, ErrInvalid
	}
	configuration, err := warpOptions(options)
	if err != nil {
		return nil, image.Point{}, err
	}
	if configuration.MaxBytes <= 16 {
		return nil, image.Point{}, ErrLimit
	}
	configuration.MaxBytes -= 16
	return g.patch.ProjectWithOptions(ctx, source, canvas, configuration)
}
