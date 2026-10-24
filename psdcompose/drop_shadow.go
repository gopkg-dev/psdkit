package psdcompose

import (
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
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

// dropShadowOverlayEffects admits a raster's active shadows and interior color overlays only.
func dropShadowOverlayEffects(set *psdeffects.Set) bool {
	if set == nil || !set.Enabled || !set.EffectsVisible {
		return false
	}
	shadow, overlay := false, false
	for _, effect := range set.Items {
		if !effect.Enabled || effect.Opacity == 0 {
			continue
		}
		switch effect.Kind {
		case psdeffects.DropShadow:
			shadow = true
		case psdeffects.ColorOverlay:
			overlay = true
		default:
			return false
		}
	}
	return shadow && overlay
}

func dropShadowBlendSupported(mode string) bool {
	mode = normalizeBlend(mode)
	return mode == "norm" || mode == "mul"
}

func (plan *composePlan) registerApproximateDropShadow(layer *psdmodel.Layer, path string) error {
	mixed := dropShadowOverlayEffects(layer.Effects)
	if layer.Group || layer.Children != nil || layer.Pixels == nil || layer.FillColor != nil ||
		layer.Pattern != nil || layer.PatternTile != nil || layer.Gradient != nil || layer.Adjustment != nil ||
		!dropShadowBlendSupported(layer.BlendMode) {
		return composeError(ErrUnsupported, path, "approximate drop shadows require a Normal or Multiply raster layer")
	}
	if mixed && normalizeBlend(layer.BlendMode) != "norm" {
		return composeError(ErrUnsupported, path, "shadow/color-overlay combinations require a Normal raster")
	}
	if layer.Placed != nil && !layer.RenderedEffects.Valid(layer.Pixels, layer.Bounds, layer.Placed) {
		return composeError(ErrUnsupported, path, "placed-layer effects require freshly rendered source pixels")
	}
	if layer.VectorMask != nil && !layer.VectorMask.Disabled {
		if normalizeBlend(layer.BlendMode) != "norm" || layer.Placed != nil {
			return composeError(ErrUnsupported, path, "vector-masked shadows require an ordinary Normal raster")
		}
		if err := validateEffectVectorMaskMetadata(layer, path); err != nil {
			return err
		}
		if layer.VectorFeather != nil && *layer.VectorFeather != 0 && !calibratedShadowVectorFeather(layer) {
			return composeError(ErrUnsupported, path,
				"vector-masked shadows require calibrated .5/1.5/3 single integer rectangle feather")
		}
		if layer.Mask != nil && !layer.Mask.Disabled && layer.Mask.Feather != nil && *layer.Mask.Feather != 0 &&
			!calibratedShadowVectorGeometry(layer.VectorMask) {
			return composeError(ErrUnsupported, path,
				"feathered dual-mask shadows require a single integer rectangle vector mask")
		}
	}
	if layer.Clipping || composeCustomRanges(layer.BlendRanges) ||
		len(layer.ChannelRestrictions) != 0 || layer.Knockout != psdmodel.KnockoutNone ||
		layer.TransparencyShapes != nil && !*layer.TransparencyShapes {
		return composeError(ErrUnsupported, path, "advanced layer interactions with drop shadows are unsupported")
	}
	for _, effect := range layer.Effects.Items {
		if !effect.Enabled || effect.Opacity == 0 {
			continue
		}
		if effect.Kind == psdeffects.ColorOverlay && SupportedBlend(effect.BlendMode) {
			continue
		}
		if effect.Kind != psdeffects.DropShadow || !dropShadowBlendSupported(effect.BlendMode) {
			return composeError(ErrUnsupported, path, "drop-shadow blend mode must be Normal or Multiply")
		}
	}
	if layer.Bounds.Empty() {
		return nil
	}
	plan.effects[layer] = &effectCache{path: path, set: layer.Effects, approximateDropShadow: true}
	return nil
}

// calibratedShadowVectorFeather reuses the established vector field without broadening its geometry or radii.
// Its caller must separately require the explicit shadow policy and ordinary Normal raster source.
func calibratedShadowVectorFeather(layer *psdmodel.Layer) bool {
	if layer.VectorFeather == nil {
		return false
	}
	radius := *layer.VectorFeather
	return (radius == .5 || radius == 1.5 || radius == 3) && calibratedShadowVectorGeometry(layer.VectorMask)
}

func calibratedShadowVectorGeometry(mask *psdvector.Mask) bool {
	return mask != nil && len(mask.Subpaths) == 1 && vectorFeatherGeometry(mask)
}

func (plan *composePlan) dropShadowShapesWithMask(layer *psdmodel.Layer) bool {
	return plan.options.ApproximateDropShadow &&
		(pureDropShadowEffects(layer.Effects) || dropShadowOverlayEffects(layer.Effects)) &&
		layer.Mask != nil && !layer.Mask.Disabled && !shadowMaskGlobal(layer)
}

func (plan *composePlan) dropShadowShapesWithVector(layer *psdmodel.Layer) bool {
	return plan.options.ApproximateDropShadow &&
		(pureDropShadowEffects(layer.Effects) || dropShadowOverlayEffects(layer.Effects)) &&
		layer.VectorMask != nil && !layer.VectorMask.Disabled &&
		(layer.VectorDensity == nil || *layer.VectorDensity != 0) && !shadowVectorMaskGlobal(layer)
}

// shadowVectorMaskGlobal follows explicit vmgm, or the original vector-mask link state for older files.
// The PSD format gives vector global masking the same final-crossfade contract as lmgm.
func shadowVectorMaskGlobal(layer *psdmodel.Layer) bool {
	if layer.VectorMaskAsGlobal != nil {
		return *layer.VectorMaskAsGlobal
	}
	return layer.VectorMask != nil && layer.VectorMask.NotLinked
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
	if plan.dropShadowShapesWithMask(layer) || plan.dropShadowShapesWithVector(layer) {
		source.alpha = float64(cache.alpha.AlphaAt(x, y).A) / 255
	}
	if len(cache.planes.Interior) == 0 {
		inside = paintDropShadowBlend(inside, source.color, composeOpacity(layer.FillOpacity)*opacity, mode)
	} else {
		inside = paintShadowOverlayInterior(layer, source, inside, cache, opacity)
	}
	backdrop = composeInterpolate(outside, inside, source.alpha)
	globalCoverage := 1.0
	globallyMasked := false
	if layer.Mask != nil && !layer.Mask.Disabled && shadowMaskGlobal(layer) {
		globalCoverage *= plan.mask(layer.Mask, x, y)
		globallyMasked = true
	}
	if layer.VectorMask != nil && !layer.VectorMask.Disabled &&
		(layer.VectorDensity == nil || *layer.VectorDensity != 0) && shadowVectorMaskGlobal(layer) {
		globalCoverage *= plan.vectorAlpha(layer, x, y)
		globallyMasked = true
	}
	if !globallyMasked {
		return backdrop
	}
	return composeInterpolate(original, backdrop, globalCoverage)
}

// paintShadowOverlayInterior uses the same interior group and Fill stages as an unshadowed color overlay.
// The caller applies the source-shape partition once, after shadows and all interior planes are assembled.
func paintShadowOverlayInterior(
	layer *psdmodel.Layer, source, backdrop composePixel, cache *effectCache, opacity float64,
) composePixel {
	fill := composeOpacity(layer.FillOpacity)
	group := layer.BlendInterior != nil && *layer.BlendInterior
	interior := paintNormalEffect(backdrop, source.color, fill)
	if group {
		interior = composePixel{color: source.color, alpha: 1}
	}
	for _, plane := range cache.planes.Interior {
		interior = paintInteriorEffect(interior, plane)
	}
	if group {
		return paintNormalEffect(backdrop, interior.color, fill*opacity)
	}
	return composeInterpolate(backdrop, interior, opacity)
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
