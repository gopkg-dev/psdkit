package psdscene

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/internal/psdplaced"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func hasSceneWarp(placed *psdmodel.Placed) bool {
	return placed != nil && placed.Warp != nil &&
		(placed.Warp.Style == "custom" || psdrender.RequiresWarpRendering(placed))
}

func validateScenePresetDescriptor(placed *psdmodel.Placed, descriptor *psdfile.Descriptor) error {
	if !hasSceneWarp(placed) || placed.Warp.Style == "custom" {
		return nil
	}
	return psdplaced.ValidatePresetDescriptor(descriptor)
}

func compileSceneWarp(
	ctx context.Context, placed *psdmodel.Placed, approximate bool, options psdwarp.Options,
) (psdplaced.WarpProjection, error) {
	if placed != nil && placed.Warp != nil && placed.Warp.Style != "custom" {
		return psdplaced.CompilePresetWarp(ctx, placed, approximate, options)
	}
	return psdplaced.CompileWarp(ctx, placed, approximate, options)
}

func (engine *rgbTileRenderer) compilePlacedWarp(
	ctx context.Context, layer *psdmodel.Layer, options psdwarp.Options,
) (psdplaced.WarpProjection, error) {
	return compileSceneWarp(ctx, layer.Placed, engine.settings.ApproximateCustomWarp, options)
}

// projectSceneWarp includes existing geometry in maxBytes and reuses certified immutable geometry.
func projectSceneWarp(
	ctx context.Context, patch psdplaced.WarpProjection,
	source *image.NRGBA, window image.Rectangle, maxBytes int64,
) (*image.NRGBA, image.Point, error) {
	if maxBytes <= 0 {
		return nil, image.Point{}, psdwarp.ErrLimit
	}
	return patch.ProjectWithOptions(ctx, source, window, psdwarp.Options{MaxBytes: maxBytes})
}
