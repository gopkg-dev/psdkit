package psdkit

import (
	"context"

	"github.com/gopkg-dev/psdkit/psdwarp"
)

// PresetWarpGeometry owns validated forward and inverse coordinates for a supported Warp preset.
type PresetWarpGeometry = psdwarp.PresetGeometry

// WarpGeometryOptions bounds retained geometry and inverse traversal.
type WarpGeometryOptions = psdwarp.Options

// UniformGridWarpGeometry owns a certified 10x10 or 13x13 uniform piecewise-cubic Warp.
// Compilation and projection do not consume saved placed-layer or composite pixels.
type UniformGridWarpGeometry = psdwarp.UniformGrid

// CompileUniformGridWarpGeometry prepares a uniform split grid from preserved source metadata.
// Nonuniform slices, stale source dimensions and unqualified projective placement are rejected.
// The explicit raster model and Photoshop pixel acceptance remain separate from geometry certification.
func CompileUniformGridWarpGeometry(
	ctx context.Context, placed *Placed, options ...WarpGeometryOptions,
) (*UniformGridWarpGeometry, error) {
	return psdwarp.CompileUniformGrid(ctx, placed, options...)
}

// CompilePresetWarpGeometry prepares owned horizontal Arc Lower coordinates with validated source metadata.
// Coordinate certification and native pixel sampling have separate acceptance scopes.
func CompilePresetWarpGeometry(
	ctx context.Context, placed *Placed, options ...WarpGeometryOptions,
) (*PresetWarpGeometry, error) {
	return psdwarp.CompilePresetGeometry(ctx, placed, options...)
}
