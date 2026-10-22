package psdwarp

import (
	"context"
	"image"
)

// ProjectWithOptions projects immutable compiled geometry using the current operation's resource limits.
// MaxBytes includes the already retained Patch.WorkingBytes as well as new raster scratch and output.
// The geometry and its original options remain unchanged; no second geometry or input pixel copy is made.
func (p *Patch) ProjectWithOptions(
	ctx context.Context, source *image.NRGBA, canvas image.Rectangle, options ...Options,
) (*image.NRGBA, image.Point, error) {
	ctx = warpContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, image.Point{}, err
	}
	if p == nil {
		return nil, image.Point{}, ErrInvalid
	}
	configuration, err := warpOptions(options)
	if err != nil {
		return nil, image.Point{}, err
	}
	operation := *p
	operation.options = configuration
	return operation.Project(ctx, source, canvas)
}
