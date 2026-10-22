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

// paintPixelMaskedColorOverlay samples the same coordinate/density/feather coverage as ordinary layer paint.
// lmgm=true masks the final premultiplied layer/effect result; false shapes the source before effect paint.
// Pure Color Overlay has no exterior plane, so legacy absent-link alternatives are algebraically equivalent.
func (plan *composePlan) paintPixelMaskedColorOverlay(
	layer *psdmodel.Layer, source, backdrop composePixel, cache *effectCache, x, y int, clip float64,
) composePixel {
	coverage := plan.mask(layer.Mask, x, y)
	if coverage == 0 {
		return backdrop
	}
	if coverage == 1 {
		return plan.paintEffectPlanes(layer, source, backdrop, cache, x, y, clip)
	}
	if layer.MaskAsGlobal != nil && *layer.MaskAsGlobal {
		painted := plan.paintEffectPlanes(layer, source, backdrop, cache, x, y, clip)
		return composeInterpolate(backdrop, painted, coverage)
	}
	source.alpha *= coverage
	return plan.paintEffectPlanes(layer, source, backdrop, cache, x, y, clip)
}
