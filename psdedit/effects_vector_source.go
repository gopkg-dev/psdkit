package psdedit

import (
	"context"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

// effectsVectorSource qualifies current mask geometry without retaining rasterized mask samples.
func (e *Editor) effectsVectorSource(
	ctx context.Context, node *editNode, layer *psdmodel.Layer, working, budget int64,
	pureOverlay, approximateShadow bool,
) (int64, error) {
	record := e.source.Layers[node.sourceIndex]
	if metadata := record.Mask; metadata != nil {
		if metadata.Flags&0xe0 != 0 || metadata.ParameterFlags&0xf0 != 0 ||
			metadata.HasReal && metadata.RealFlags&0xe0 != 0 {
			return 0, editError(ErrUnsupported, "layers.effects.mask", "source mask flags or parameter layout are unknown")
		}
		for _, feather := range []*float64{metadata.UserFeather, metadata.VectorFeather} {
			if feather != nil && (math.IsNaN(*feather) || math.IsInf(*feather, 0) || *feather < 0) {
				return 0, editError(ErrUnsupported, "layers.effects.mask", "source mask feather is not a known finite value")
			}
		}
	}
	state, err := e.pixelMaskStateLocked(ctx, node)
	if err != nil {
		return 0, err
	}
	working += int64(len(state.data)) + 512
	if working >= budget {
		return 0, editError(psdfile.ErrLimit, "layers.effects.mask", "current mask metadata exceeds the working budget")
	}
	if len(state.data) >= 22 && (state.data[21]&0xe0 != 0 ||
		state.hasReal && state.data[state.layout.flagsOffset]&0xe0 != 0) {
		return 0, editError(ErrUnsupported, "layers.effects.mask", "current mask flags are unknown")
	}
	mask := layer.VectorMask
	if node.vectorMask != nil {
		mask = node.vectorMask
	}
	if node.vectorMaskChange != nil && node.vectorMaskChange.removed {
		mask = nil
	}
	if mask != nil && !mask.Disabled {
		mode := record.BlendMode
		if node.blendModeChanged {
			mode = node.blendMode
		}
		if layer.Placed != nil || node.placed != nil || record.BlendMode != "norm" || mode != "norm" {
			return 0, editError(ErrUnsupported, "layers.effects.mask", "vector-masked effects require an ordinary Normal raster")
		}
	}
	if layer.VectorMask != nil {
		if err := validateEffectsVectorSource(ctx, layer.VectorMask, budget-working); err != nil {
			return 0, err
		}
	}
	if mask == nil || mask.Disabled {
		return working, ctx.Err()
	}
	settings := state.layout.settings
	activePixel := state.hasUser && !settings.Disabled
	if settings.VectorFeather != nil && *settings.VectorFeather != 0 &&
		(!pureOverlay && !approximateShadow || activePixel && !approximateShadow ||
			!effectsCalibratedVectorFeather(mask, *settings.VectorFeather)) ||
		activePixel && settings.Feather != nil && *settings.Feather != 0 &&
			(!approximateShadow || !effectsCalibratedVectorGeometry(mask)) {
		return 0, editError(ErrUnsupported, "layers.effects.mask",
			"active vector effects require known mask feather and calibrated rectangle overlay or explicit shadow policy")
	}
	if mask != layer.VectorMask {
		if err := validateEffectsVectorSource(ctx, mask, budget-working); err != nil {
			return 0, err
		}
	}
	return working + 1, ctx.Err()
}

// effectsCalibratedVectorFeather mirrors the compositor's existing single integer rectangle qualification.
func effectsCalibratedVectorFeather(mask *psdvector.Mask, radius float64) bool {
	return (radius == .5 || radius == 1.5 || radius == 3) && effectsCalibratedVectorGeometry(mask)
}

func effectsCalibratedVectorGeometry(mask *psdvector.Mask) bool {
	if mask == nil || mask.Version != 3 ||
		mask.Flags&^uint32(7) != 0 || len(mask.Unsupported) != 0 || len(mask.Subpaths) != 1 {
		return false
	}
	path := mask.Subpaths[0]
	if !path.Closed || path.Operation != 1 || len(path.Knots) != 4 || path.Unknown1 > 1 || path.Unknown2 != 0 {
		return false
	}
	for _, value := range path.Reserved {
		if value != 0 {
			return false
		}
	}
	for index, knot := range path.Knots {
		point := knot.Anchor
		if point.X != knot.Preceding.X || point.Y != knot.Preceding.Y ||
			point.X != knot.Leaving.X || point.Y != knot.Leaving.Y ||
			math.IsNaN(point.X) || math.IsNaN(point.Y) || math.IsInf(point.X, 0) || math.IsInf(point.Y, 0) ||
			math.Trunc(point.X) != point.X || math.Trunc(point.Y) != point.Y ||
			math.Abs(point.X) > 1<<40 || math.Abs(point.Y) > 1<<40 {
			return false
		}
		next, opposite := path.Knots[(index+1)%4].Anchor, path.Knots[(index+2)%4].Anchor
		if (point.X == next.X) == (point.Y == next.Y) || point.X == opposite.X || point.Y == opposite.Y {
			return false
		}
	}
	return true
}

func validateEffectsVectorSource(ctx context.Context, mask *psdvector.Mask, budget int64) error {
	if len(mask.Unsupported) != 0 || mask.Flags&^uint32(7) != 0 || mask.Version != 0 && mask.Version != 3 {
		return editError(ErrUnsupported, "layers.effects.vectorMask", "source vector metadata is unknown")
	}
	// A one-pixel probe validates the complete path and supported flags under a bounded geometry budget.
	if _, err := psdvector.Rasterize(ctx, mask, image.Rect(0, 0, 1, 1),
		psdvector.Options{MaxWorkingBytes: budget}); err != nil {
		return vectorEditError(err)
	}
	return ctx.Err()
}

func effectsKnownPixelMaskIssues(mask *psdmodel.Mask, allowVectorMask bool) bool {
	for _, issue := range mask.Unsupported {
		// Shared vector feather is qualified from the current owned mask metadata, so explicit clearing is valid.
		if issue != "暂不支持矢量蒙版羽化" || !allowVectorMask {
			return false
		}
	}
	return true
}
