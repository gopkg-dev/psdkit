package psdcompose

import (
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// pureColorOverlayEffects admits only active interior overlays, preserving all other effect contracts.
func pureColorOverlayEffects(set *psdeffects.Set) bool {
	active := false
	for _, effect := range set.Items {
		if !effect.Enabled || effect.Opacity == 0 {
			continue
		}
		if effect.Kind != psdeffects.ColorOverlay {
			return false
		}
		active = true
	}
	return active
}

func validateUnfeatheredEffectVectorMask(layer *psdmodel.Layer, path string) error {
	mask := layer.VectorMask
	if mask == nil || mask.Disabled {
		return nil
	}
	if len(mask.Unsupported) != 0 || mask.Flags&^uint32(7) != 0 || mask.Version != 0 && mask.Version != 3 {
		return composeError(ErrUnsupported, path, "masked effects require known vector-mask metadata")
	}
	if layer.VectorFeather != nil && *layer.VectorFeather != 0 ||
		layer.Mask != nil && !layer.Mask.Disabled && layer.Mask.Feather != nil && *layer.Mask.Feather != 0 {
		return composeError(ErrUnsupported, path, "effects with an active vector mask require unfeathered masks")
	}
	return nil
}

// paintMaskedColorOverlay uses the same pixel and vector coverage product as ordinary layer paint.
// Global-mask flags place coverage after the final premultiplied layer/effect result instead of in source alpha.
// Pure Color Overlay has no exterior plane and is affine in source alpha, so these stages are equivalent.
func (plan *composePlan) paintMaskedColorOverlay(
	layer *psdmodel.Layer, source, backdrop composePixel, cache *effectCache, x, y int, clip float64,
) composePixel {
	coverage := plan.mask(layer.Mask, x, y) * plan.vectorAlpha(layer, x, y)
	if coverage == 0 {
		return backdrop
	}
	if coverage == 1 {
		return plan.paintEffectPlanes(layer, source, backdrop, cache, x, y, clip)
	}
	vectorGlobal := layer.VectorMask != nil && !layer.VectorMask.Disabled &&
		(layer.VectorDensity == nil || *layer.VectorDensity != 0) &&
		layer.VectorMaskAsGlobal != nil && *layer.VectorMaskAsGlobal
	if layer.MaskAsGlobal != nil && *layer.MaskAsGlobal || vectorGlobal {
		painted := plan.paintEffectPlanes(layer, source, backdrop, cache, x, y, clip)
		return composeInterpolate(backdrop, painted, coverage)
	}
	source.alpha *= coverage
	return plan.paintEffectPlanes(layer, source, backdrop, cache, x, y, clip)
}
