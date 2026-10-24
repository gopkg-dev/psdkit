package psdplaced

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

// WarpProjection provides immutable certified geometry for one placed source projection.
// WorkingBytes includes retained geometry in each operation's projection budget.
type WarpProjection interface {
	WorkingBytes() int64
	Bounds(image.Rectangle) (image.Rectangle, error)
	ProjectWithOptions(
		context.Context, *image.NRGBA, image.Rectangle, ...psdwarp.Options,
	) (*image.NRGBA, image.Point, error)
}

// CompileWarp selects the current operation's certified custom warp geometry.
// Uniform 10x10 and 13x13 quilts require explicit approximation, even for affine placement.
// Their strict affine placement, typed slices and global nonfolding certificates remain mandatory.
// Single patches retain Compile's default gate and CompileMathematical's explicit homography policy.
func CompileWarp(
	ctx context.Context, placed *psdmodel.Placed, approximate bool, options psdwarp.Options,
) (WarpProjection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var projection WarpProjection
	var err error
	if placed != nil && placed.Warp != nil &&
		(placed.Warp.DeformColumns == 10 || placed.Warp.DeformColumns == 13 ||
			placed.Warp.DeformRows == 10 || placed.Warp.DeformRows == 13) {
		if !approximate {
			return nil, psdwarp.ErrUnsupported
		}
		projection, err = psdwarp.CompileUniformGrid(ctx, placed, options)
	} else if approximate {
		projection, err = psdwarp.CompileMathematical(ctx, placed, options)
	} else {
		projection, err = psdwarp.Compile(ctx, placed, options)
	}
	if err != nil {
		return nil, err
	}
	return projection, nil
}

// CompilePresetWarp compiles only certified preset geometry after explicit mathematical sampling selection.
// It does not broaden CompileWarp or allow unverified preset types, projective placement or folded geometry.
func CompilePresetWarp(
	ctx context.Context, placed *psdmodel.Placed, approximate bool, options psdwarp.Options,
) (WarpProjection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !approximate {
		return nil, psdwarp.ErrUnsupported
	}
	if placed != nil && placed.Warp != nil {
		warp := placed.Warp
		if warp.HasDeformRows || warp.HasDeformColumns || warp.DeformRows != 0 || warp.DeformColumns != 0 ||
			len(warp.QuiltSlices) != 0 {
			return nil, psdwarp.ErrUnsupported
		}
	}
	geometry, err := psdwarp.CompilePresetGeometry(ctx, placed, options)
	if err != nil {
		return nil, err
	}
	return (*presetProjection)(geometry), nil
}

// presetProjection adapts the explicit mathematical sampler without allocating another geometry wrapper.
type presetProjection psdwarp.PresetGeometry

func (p *presetProjection) WorkingBytes() int64 {
	return (*psdwarp.PresetGeometry)(p).WorkingBytes()
}

func (p *presetProjection) Bounds(canvas image.Rectangle) (image.Rectangle, error) {
	return (*psdwarp.PresetGeometry)(p).Bounds(canvas)
}

func (p *presetProjection) ProjectWithOptions(
	ctx context.Context, source *image.NRGBA, canvas image.Rectangle, options ...psdwarp.Options,
) (*image.NRGBA, image.Point, error) {
	return (*psdwarp.PresetGeometry)(p).ProjectMathematicalWithOptions(ctx, source, canvas, options...)
}
