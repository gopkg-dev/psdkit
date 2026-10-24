package psdrender

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdwarp"
)

// WarpOptions bounds the single-patch warp renderer's output and inverse sampling.
type WarpOptions = psdwarp.Options

// CompileWarp copies and certifies supported custom warp geometry without allocating pixels.
func CompileWarp(ctx context.Context, layer *Layer, options ...WarpOptions) (*psdwarp.Patch, error) {
	if layer == nil {
		return nil, psdwarp.ErrInvalid
	}
	return psdwarp.Compile(ctx, layer.Placed, options...)
}

// CompilePresetWarpGeometry certifies supported native preset coordinates without allocating pixels.
// The returned geometry offers explicitly selected mathematical sampling, without native pixel equivalence.
// Scene rendering supports horizontal Arc Lower with explicit ApproximateCustomWarp; default projection is unchanged.
func CompilePresetWarpGeometry(
	ctx context.Context, layer *Layer, options ...WarpOptions,
) (*psdwarp.PresetGeometry, error) {
	if layer == nil {
		return nil, psdwarp.ErrInvalid
	}
	return psdwarp.CompilePresetGeometry(ctx, layer.Placed, options...)
}

// ProjectWarp projects source content through one supported custom 4x4 envelope.
// Opacity, masks, blend modes and effects are left to the document compositor.
func ProjectWarp(ctx context.Context, layer *Layer, source *image.NRGBA,
	canvas image.Rectangle, options ...WarpOptions,
) (*image.NRGBA, image.Point, error) {
	if layer == nil {
		return nil, image.Point{}, psdwarp.ErrInvalid
	}
	return psdwarp.Project(ctx, source, layer.Placed, canvas, options...)
}
