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

// calibratedVectorFeatherOverlay admits only the established rectangle feather field with interior overlays.
func calibratedVectorFeatherOverlay(layer *psdmodel.Layer) bool {
	set := layer.Effects
	if set == nil || len(set.Unsupported) != 0 ||
		layer.Mask != nil && !layer.Mask.Disabled || layer.VectorFeather == nil || layer.VectorMask == nil ||
		len(layer.VectorMask.Subpaths) != 1 {
		return false
	}
	overlay := false
	for _, effect := range set.Items {
		if len(effect.Unsupported) != 0 {
			return false
		}
		if effect.Kind == psdeffects.ColorOverlay {
			overlay = true
			continue
		}
		if effect.Enabled && effect.Opacity != 0 {
			return false
		}
	}
	// Dormant overlay sets still need known metadata before their ordinary feather path can be reused.
	validation := *set
	validation.Enabled, validation.EffectsVisible = true, true
	if psdeffects.Validate(&validation) != nil {
		return false
	}
	radius := *layer.VectorFeather
	return overlay && (radius == .5 || radius == 1.5 || radius == 3) && vectorFeatherGeometry(layer.VectorMask)
}

func validateUnfeatheredEffectVectorMask(layer *psdmodel.Layer, path string) error {
	if err := validateEffectVectorMaskMetadata(layer, path); err != nil {
		return err
	}
	mask := layer.VectorMask
	if mask == nil || mask.Disabled {
		return nil
	}
	if layer.VectorFeather != nil && *layer.VectorFeather != 0 ||
		layer.Mask != nil && !layer.Mask.Disabled && layer.Mask.Feather != nil && *layer.Mask.Feather != 0 {
		return composeError(ErrUnsupported, path, "effects with an active vector mask require unfeathered masks")
	}
	return nil
}

func validateEffectVectorMaskMetadata(layer *psdmodel.Layer, path string) error {
	mask := layer.VectorMask
	if mask != nil && !mask.Disabled &&
		(len(mask.Unsupported) != 0 || mask.Flags&^uint32(7) != 0 || mask.Version != 0 && mask.Version != 3) {
		return composeError(ErrUnsupported, path, "masked effects require known vector-mask metadata")
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
