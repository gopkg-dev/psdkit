package psdcompose

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

type effectCache struct {
	path                  string
	set                   *psdeffects.Set
	alpha                 *image.Alpha
	planes                *psdeffects.Result
	shapeBytes            int64
	outputBytes           int64
	scratchBytes          int64
	remainingWork         int64
	binaryShape           bool
	maskedColorOverlay    bool
	approximateDropShadow bool
}

func (plan *composePlan) registerEffects(layer *psdmodel.Layer, path string) error {
	set := layer.Effects
	if set == nil || !set.Enabled || !set.EffectsVisible {
		return nil
	}
	if err := psdeffects.Validate(set); err != nil {
		return composeEffectError(err, path)
	}
	if plan.options.ApproximateDropShadow && (pureDropShadowEffects(set) || dropShadowOverlayEffects(set)) {
		return plan.registerApproximateDropShadow(layer, path)
	}
	active, binary := false, false
	scale := set.Scale
	if scale == 0 {
		scale = 1
	}
	for _, effect := range set.Items {
		if !effect.Enabled || effect.Opacity == 0 {
			continue
		}
		active = true
		interior := effect.Kind == psdeffects.ColorOverlay || effect.Kind == psdeffects.InnerShadow ||
			effect.Kind == psdeffects.InnerGlow
		if interior && !SupportedBlend(effect.BlendMode) {
			return composeError(ErrUnsupported, path, "unsupported color-overlay blend mode")
		}
		if !interior && normalizeBlend(effect.BlendMode) != "norm" {
			return composeError(ErrUnsupported, path, "effect blend modes require additional native coverage validation")
		}
		switch effect.Kind {
		case psdeffects.DropShadow:
			if !integerEffectValue(effect.Size*scale*effect.Spread) ||
				!integerEffectValue(effect.Size*scale*(1-effect.Spread)) {
				return composeError(ErrUnsupported, path, "drop-shadow size and spread require integral native radii")
			}
			if effect.Size != 0 {
				binary = true
			}
			angle := effect.Angle
			if effect.UseGlobalLight {
				angle = set.GlobalAngle
			}
			angle = math.Mod(angle, 360) * math.Pi / 180
			distance := effect.Distance * scale
			if !integerEffectValue(distance*math.Cos(angle)) || !integerEffectValue(distance*math.Sin(angle)) {
				return composeError(ErrUnsupported, path, "fractional shadow displacement has not passed strict native pixels")
			}
		case psdeffects.OuterGlow:
			binary = true
			if effect.Spread != 1 || effect.Size*scale != 2 && effect.Size*scale != 3 {
				return composeError(ErrUnsupported, path, "only full-spread two/three-pixel glow radii are calibrated")
			}
		case psdeffects.InnerShadow:
			binary = true
			if !integerEffectValue(effect.Size*scale) || effect.Size != 0 && effect.Spread != 0 {
				return composeError(ErrUnsupported, path,
					"inner shadows require integral native blur size and zero choke")
			}
		case psdeffects.InnerGlow:
			binary = true
			if !integerEffectValue(effect.Size*scale) || effect.Size != 0 && effect.Spread != 0 && effect.Spread != 1 {
				return composeError(ErrUnsupported, path,
					"inner glows require integral native size and zero or full choke")
			}
		case psdeffects.Stroke:
			binary = true
			if effect.Position == psdeffects.Center ||
				effect.Size*scale != 2 && (effect.Position != psdeffects.Outside || effect.Size*scale != 3) {
				return composeError(ErrUnsupported, path,
					"only inside/outside two-pixel and outside three-pixel strokes are calibrated")
			}
		}
	}
	if !active {
		return nil
	}
	livePlacedOverlay := plan.options.ApproximatePlacedColorOverlay && layer.Placed != nil &&
		pureColorOverlayEffects(set)
	if livePlacedOverlay {
		if err := validatePlacedColorOverlay(layer, path); err != nil {
			return err
		}
	}
	if layer.Group || layer.Children != nil || layer.Pixels == nil || layer.FillColor != nil || layer.Pattern != nil ||
		layer.PatternTile != nil ||
		layer.Adjustment != nil || layer.Placed != nil && !livePlacedOverlay || normalizeBlend(layer.BlendMode) != "norm" {
		return composeError(ErrUnsupported, path, "effects currently require an ordinary Normal raster layer")
	}
	pixelMaskActive := layer.Mask != nil && !layer.Mask.Disabled
	vectorMaskActive := layer.VectorMask != nil && !layer.VectorMask.Disabled
	maskedColorOverlay := (pixelMaskActive || vectorMaskActive) && pureColorOverlayEffects(set)
	if vectorMaskActive && maskedColorOverlay {
		if err := validateUnfeatheredEffectVectorMask(layer, path); err != nil {
			return err
		}
	}
	if (pixelMaskActive || vectorMaskActive) && !maskedColorOverlay || layer.Clipping ||
		composeCustomRanges(layer.BlendRanges) ||
		(layer.TransparencyShapes != nil && !*layer.TransparencyShapes) {
		return composeError(ErrUnsupported, path,
			"effects with masks, clipping, Blend If or alternate transparency are uncalibrated")
	}
	if plan.effects == nil {
		plan.effects = make(map[*psdmodel.Layer]*effectCache)
	}
	plan.effects[layer] = &effectCache{path: path, set: set, binaryShape: binary,
		maskedColorOverlay: maskedColorOverlay}
	return nil
}

func integerEffectValue(value float64) bool { return math.Abs(value-math.Round(value)) < 1e-9 }

// checkEffectClipping runs after all visible effect layers have been registered.
func (plan *composePlan) checkEffectClipping(layers []*psdmodel.Layer, path string) error {
	for index := 0; index < len(layers); {
		end := index + 1
		for end < len(layers) && layers[end].Clipping {
			end++
		}
		if end > index+1 {
			for position := index; position < end; position++ {
				if cache := plan.effects[layers[position]]; cache != nil {
					return composeError(ErrUnsupported, cache.path, "effects on a clipping base or chain are uncalibrated")
				}
			}
		}
		for position := index; position < end; position++ {
			if err := plan.checkEffectClipping(layers[position].Children,
				fmt.Sprintf("%s[%d].children", path, position)); err != nil {
				return err
			}
		}
		index = end
	}
	return nil
}

func (plan *composePlan) reserveEffectCaches() error {
	largestScratch, retained := int64(0), int64(0)
	for layer, cache := range plan.effects {
		width, height, err := composeDimensions(layer.Bounds, cache.path+".bounds")
		if err != nil {
			return err
		}
		available := plan.options.MaxWorkingBytes - plan.workingBytes - retained
		if available <= 0 || (height != 0 && int64(width) > available/int64(height)) {
			return composeError(ErrLimit, cache.path, "source alpha and effect caches exceed the working budget")
		}
		cache.shapeBytes = int64(width) * int64(height)
		available -= cache.shapeBytes
		if available <= 0 {
			return composeError(ErrLimit, cache.path, "source alpha consumes the effect working budget")
		}
		estimate, err := psdeffects.EstimateWorking(layer.Bounds, cache.set,
			psdeffects.Options{MaxWorkingBytes: available, ApproximateDropShadow: cache.approximateDropShadow,
				DeferDropShadowConceal: cache.approximateDropShadow})
		if err != nil {
			return composeEffectError(err, cache.path)
		}
		cache.outputBytes, cache.scratchBytes = estimate.OutputBytes, estimate.ScratchBytes
		cache.remainingWork = estimate.SampleWork
		if cache.binaryShape {
			for y := range height {
				for x := range width {
					value := layer.Pixels.Pix[y*layer.Pixels.Stride+x*4+3]
					if value != 0 && value != 255 {
						return composeError(ErrUnsupported, cache.path, "glow/stroke with partial source alpha is uncalibrated")
					}
				}
			}
		}
		retained += cache.shapeBytes + cache.outputBytes
		largestScratch = max(largestScratch, cache.scratchBytes)
	}
	if largestScratch > plan.options.MaxWorkingBytes-plan.workingBytes-retained {
		return composeError(ErrLimit, "document.effects", "retained alpha/coverage and effect scratch exceed working budget")
	}
	plan.workingBytes += retained
	return nil
}

func (plan *composePlan) prepareEffects(ctx context.Context) error {
	for layer, cache := range plan.effects {
		if err := ctx.Err(); err != nil {
			return err
		}
		alpha := image.NewAlpha(layer.Bounds)
		for y := 0; y < layer.Bounds.Dy(); y++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			for x := 0; x < layer.Bounds.Dx(); x++ {
				if x&1023 == 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
				}
				value := layer.Pixels.Pix[y*layer.Pixels.Stride+x*4+3]
				if cache.binaryShape && value != 0 && value != 255 {
					return composeError(ErrUnsupported, cache.path, "glow/stroke with partial source alpha is uncalibrated")
				}
				if cache.approximateDropShadow &&
					(plan.dropShadowShapesWithMask(layer) || plan.dropShadowShapesWithVector(layer)) {
					coverage := 1.0
					if plan.dropShadowShapesWithMask(layer) {
						coverage *= plan.shadowMask(layer.Mask, layer.Bounds.Min.X+x, layer.Bounds.Min.Y+y)
					}
					if plan.dropShadowShapesWithVector(layer) {
						coverage *= plan.vectorAlpha(layer, layer.Bounds.Min.X+x, layer.Bounds.Min.Y+y)
					}
					value = composeByte(float64(value) / 255 * coverage)
				}
				alpha.Pix[y*alpha.Stride+x] = value
			}
		}
		cache.alpha = alpha
		available := plan.options.MaxWorkingBytes - plan.workingBytes + cache.outputBytes
		if available <= 0 {
			return composeError(ErrLimit, cache.path, "effect scratch has no remaining working budget")
		}
		planes, err := psdeffects.Rasterize(ctx, alpha, cache.set,
			psdeffects.Options{MaxWorkingBytes: available, MaxSampleWork: cache.remainingWork,
				ApproximateDropShadow: cache.approximateDropShadow, DeferDropShadowConceal: cache.approximateDropShadow})
		if err != nil {
			return composeEffectError(err, cache.path)
		}
		cache.planes = planes
	}
	return nil
}

func (plan *composePlan) paintEffects(
	layer *psdmodel.Layer, mode string, source, backdrop composePixel, x, y int, clip float64,
) composePixel {
	cache := plan.effects[layer]
	if cache == nil || cache.planes == nil {
		return backdrop
	}
	if cache.approximateDropShadow {
		return plan.paintApproximateDropShadow(layer, mode, source, backdrop, cache, x, y, clip)
	}
	if cache.maskedColorOverlay {
		return plan.paintMaskedColorOverlay(layer, source, backdrop, cache, x, y, clip)
	}
	return plan.paintEffectPlanes(layer, source, backdrop, cache, x, y, clip)
}

func (plan *composePlan) paintEffectPlanes(
	layer *psdmodel.Layer, source, backdrop composePixel, cache *effectCache, x, y int, clip float64,
) composePixel {
	opacity := composeOpacity(layer.Opacity) * clip
	for _, plane := range cache.planes.Background {
		alpha := float64(plane.Coverage.AlphaAt(x, y).A) / 255 * plane.Opacity * opacity
		backdrop = paintNormalEffect(backdrop, plane.Color, alpha)
	}
	bodyAlpha := source.alpha * composeOpacity(layer.FillOpacity) * opacity
	interiorShape := source.alpha * opacity
	for _, plane := range cache.planes.Foreground {
		if plane.Kind == psdeffects.Stroke {
			cutout := 1 - float64(plane.Coverage.AlphaAt(x, y).A)/255
			bodyAlpha *= cutout
			interiorShape *= cutout
		}
	}
	if len(cache.planes.Interior) == 0 {
		backdrop = paintNormalEffect(backdrop, source.color, bodyAlpha)
	} else {
		groupInterior := layer.BlendInterior != nil && *layer.BlendInterior
		interior := paintNormalEffect(backdrop, source.color, composeOpacity(layer.FillOpacity))
		if groupInterior {
			interior = composePixel{color: source.color, alpha: 1}
		}
		for _, plane := range cache.planes.Interior {
			if plane.Kind != psdeffects.ColorOverlay {
				coverage := float64(plane.Coverage.AlphaAt(x, y).A) / 255
				if source.alpha == 0 {
					coverage = 0
				} else {
					coverage /= source.alpha
				}
				plane.Opacity *= composeClamp(coverage)
			}
			interior = paintInteriorEffect(interior, plane)
		}
		if groupInterior {
			backdrop = paintNormalEffect(backdrop, interior.color, bodyAlpha)
		} else {
			backdrop = composeInterpolate(backdrop, interior, interiorShape)
		}
	}
	for _, plane := range cache.planes.Foreground {
		alpha := float64(plane.Coverage.AlphaAt(x, y).A) / 255 * plane.Opacity * opacity
		backdrop = paintNormalEffect(backdrop, plane.Color, alpha)
	}
	return backdrop
}

func paintInteriorEffect(backdrop composePixel, plane psdeffects.Plane) composePixel {
	outputAlpha := plane.Opacity + backdrop.alpha*(1-plane.Opacity)
	if outputAlpha == 0 {
		return backdrop
	}
	blended, _ := blendRGB(plane.BlendMode, backdrop.color, plane.Color, plane.Opacity)
	result := composePixel{alpha: outputAlpha, residual: backdrop.residual * (1 - plane.Opacity)}
	for channel := range result.color {
		premultiplied := backdrop.alpha*backdrop.color[channel]*(1-plane.Opacity) +
			plane.Opacity*(1-backdrop.alpha)*plane.Color[channel]
		if blendUsesSpecialFill(plane.BlendMode) {
			premultiplied += backdrop.alpha*(blended[channel]-backdrop.color[channel]) +
				plane.Opacity*backdrop.alpha*backdrop.color[channel]
		} else {
			premultiplied += plane.Opacity * backdrop.alpha * blended[channel]
		}
		result.color[channel] = composeClamp(premultiplied / outputAlpha)
	}
	return result
}

func paintNormalEffect(backdrop composePixel, color [3]float64, alpha float64) composePixel {
	if alpha == 0 {
		return backdrop
	}
	outputAlpha := alpha + backdrop.alpha*(1-alpha)
	if outputAlpha == 0 {
		return composePixel{}
	}
	result := composePixel{alpha: outputAlpha, residual: backdrop.residual * (1 - alpha)}
	for component := range 3 {
		result.color[component] = composeClamp((color[component]*alpha +
			backdrop.color[component]*backdrop.alpha*(1-alpha)) / outputAlpha)
	}
	return result
}

func composeEffectError(err error, path string) error {
	kind := ErrInvalid
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, psdeffects.ErrUnsupported):
		kind = ErrUnsupported
	case errors.Is(err, psdeffects.ErrLimit):
		kind = ErrLimit
	}
	return composeError(kind, path, err.Error())
}
