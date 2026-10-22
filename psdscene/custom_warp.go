package psdscene

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func compileSceneWarp(
	ctx context.Context, placed *psdmodel.Placed, approximate bool, options psdwarp.Options,
) (*psdwarp.Patch, error) {
	if approximate {
		return psdwarp.CompileMathematical(ctx, placed, options)
	}
	return psdwarp.Compile(ctx, placed, options)
}

// projectSceneWarp includes existing geometry in maxBytes and reuses the certified immutable patch.
func projectSceneWarp(
	ctx context.Context, patch *psdwarp.Patch, source *image.NRGBA, window image.Rectangle, maxBytes int64,
) (*image.NRGBA, image.Point, error) {
	if maxBytes <= 0 {
		return nil, image.Point{}, psdwarp.ErrLimit
	}
	return patch.ProjectWithOptions(ctx, source, window, psdwarp.Options{MaxBytes: maxBytes})
}
