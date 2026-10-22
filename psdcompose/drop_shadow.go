package psdcompose

import (
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// pureDropShadowEffects separates the approximation from previously qualified effect families.
func pureDropShadowEffects(set *psdeffects.Set) bool {
	if set == nil || !set.Enabled || !set.EffectsVisible {
		return false
	}
	active := false
	for _, effect := range set.Items {
		if !effect.Enabled || effect.Opacity == 0 {
			continue
		}
		if effect.Kind != psdeffects.DropShadow {
			return false
		}
		active = true
	}
	return active
}

func dropShadowBlendSupported(mode string) bool {
	mode = normalizeBlend(mode)
	return mode == "norm" || mode == "mul"
}

func (plan *composePlan) registerApproximateDropShadow(layer *psdmodel.Layer, path string) error {
	if layer.Group || layer.Children != nil || layer.Pixels == nil || layer.FillColor != nil ||
		layer.Pattern != nil || layer.PatternTile != nil || layer.Gradient != nil || layer.Adjustment != nil ||
		!dropShadowBlendSupported(layer.BlendMode) {
		return composeError(ErrUnsupported, path, "approximate drop shadows require a Normal or Multiply raster layer")
	}
	if layer.Placed != nil && !layer.RenderedEffects.Valid(layer.Pixels, layer.Bounds, layer.Placed) {
		return composeError(ErrUnsupported, path, "placed-layer effects require freshly rendered source pixels")
	}
	if layer.VectorMask != nil || layer.Clipping || composeCustomRanges(layer.BlendRanges) ||
		len(layer.ChannelRestrictions) != 0 || layer.Knockout != psdmodel.KnockoutNone ||
		layer.TransparencyShapes != nil && !*layer.TransparencyShapes {
		return composeError(ErrUnsupported, path, "advanced layer interactions with drop shadows are unsupported")
	}
	for _, effect := range layer.Effects.Items {
		if effect.Enabled && effect.Opacity != 0 && !dropShadowBlendSupported(effect.BlendMode) {
			return composeError(ErrUnsupported, path, "drop-shadow blend mode must be Normal or Multiply")
		}
	}
	if layer.Bounds.Empty() {
		return nil
	}
	plan.effects[layer] = &effectCache{path: path, set: layer.Effects, approximateDropShadow: true}
	return nil
}

func (plan *composePlan) dropShadowShapesWithMask(layer *psdmodel.Layer) bool {
	return plan.options.ApproximateDropShadow && pureDropShadowEffects(layer.Effects) &&
		layer.Mask != nil && !layer.Mask.Disabled && !shadowMaskGlobal(layer)
}

// shadowMaskGlobal follows explicit lmgm first and the source mask's legacy link state when lmgm is absent.
// Native source controls map the stored Relative bit to an unlinked user mask; its Bounds remain absolute.
func shadowMaskGlobal(layer *psdmodel.Layer) bool {
	if layer.MaskAsGlobal != nil {
		return *layer.MaskAsGlobal
	}
	return layer.Mask != nil && layer.Mask.Relative
}

// paintApproximateDropShadow treats source shape as a partition between outside and inside style branches.
// Concealed shadows paint only the outside branch; unconcealed shadows paint both branches.
// The body paints the inside branch at full shape before the original alpha mixes the two branches once.
func (plan *composePlan) paintApproximateDropShadow(
	layer *psdmodel.Layer, mode string, source, backdrop composePixel, cache *effectCache, x, y int, clip float64,
) composePixel {
	original := backdrop
	opacity := composeOpacity(layer.Opacity) * clip
	outside, inside := backdrop, backdrop
	for _, plane := range cache.planes.Background {
		coverage := float64(plane.Coverage.AlphaAt(x, y).A) / 255 * plane.Opacity * opacity
		outside = paintDropShadowBlend(outside, plane.Color, coverage, plane.BlendMode)
		if !plane.LayerConceals {
			inside = paintDropShadowBlend(inside, plane.Color, coverage, plane.BlendMode)
		}
	}
	if plan.dropShadowShapesWithMask(layer) {
		source.alpha = float64(cache.alpha.AlphaAt(x, y).A) / 255
	}
	inside = paintDropShadowBlend(inside, source.color, composeOpacity(layer.FillOpacity)*opacity, mode)
	backdrop = composeInterpolate(outside, inside, source.alpha)
	if layer.Mask != nil && !layer.Mask.Disabled && shadowMaskGlobal(layer) {
		return composeInterpolate(original, backdrop, plan.mask(layer.Mask, x, y))
	}
	return backdrop
}

func paintDropShadowBlend(backdrop composePixel, source [3]float64, alpha float64, mode string) composePixel {
	if alpha == 0 {
		return backdrop
	}
	outputAlpha := alpha + backdrop.alpha*(1-alpha)
	blended, _ := blendRGB(mode, backdrop.color, source, 1)
	result := composePixel{alpha: outputAlpha, residual: backdrop.residual * (1 - alpha)}
	for component := range result.color {
		premultiplied := backdrop.alpha*backdrop.color[component]*(1-alpha) +
			alpha*((1-backdrop.alpha)*source[component]+backdrop.alpha*blended[component])
		result.color[component] = composeClamp(premultiplied / outputAlpha)
	}
	return result
}
