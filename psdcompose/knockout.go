package psdcompose

import "github.com/gopkg-dev/psdkit/psdmodel"

// knockoutScope carries pixel values only, so nested Knockout does not allocate intermediate canvases.
type knockoutScope struct {
	shallow composePixel
	deep    composePixel
}

func (plan *composePlan) checkAdvanced(layer *psdmodel.Layer, path string) error {
	if len(layer.ChannelRestrictions) > 56 {
		return composeError(ErrLimit, path+".channelRestrictions", "channel restrictions exceed the bounded channel count")
	}
	if layer.Background && (path != "document.layers[0]" || layer.Group || layer.Children != nil || layer.Clipping) {
		return composeError(ErrInvalid, path+".background", "Background must be the bottom document raster layer")
	}
	if layer.Knockout != psdmodel.KnockoutNone {
		plan.hasKnockout = true
	}
	return nil
}

func advancedCapability(layer *psdmodel.Layer, path string) error {
	if layer.Knockout > psdmodel.KnockoutDeep {
		return composeError(ErrUnsupported, path+".knockout", "unknown Knockout setting")
	}
	for _, channel := range layer.ChannelRestrictions {
		if channel > 2 {
			return composeError(ErrUnsupported, path+".channelRestrictions", "only RGB channel restrictions are supported")
		}
	}
	if len(layer.ChannelRestrictions) != 0 &&
		(layer.Knockout != psdmodel.KnockoutNone || advancedActiveEffects(layer) || dissolveMode(layer.BlendMode)) {
		return composeError(ErrUnsupported, path+".channelRestrictions",
			"channel restrictions with Knockout, effects or Dissolve are uncalibrated")
	}
	if layer.Knockout != psdmodel.KnockoutNone &&
		(layer.Adjustment != nil || advancedActiveEffects(layer) || dissolveMode(layer.BlendMode)) {
		return composeError(ErrUnsupported, path+".knockout",
			"Knockout with adjustments, effects or Dissolve is uncalibrated")
	}
	return nil
}

// ordinaryKnockoutShape excludes active semantics that change an isolated Normal subtree's intrinsic shape.
func ordinaryKnockoutShape(layer *psdmodel.Layer) bool {
	return !layer.Clipping && layer.Knockout == psdmodel.KnockoutNone &&
		normalizeBlend(layer.BlendMode) == "norm" && composeOpacity(layer.Opacity) == 1 &&
		composeOpacity(layer.FillOpacity) == 1 && layer.Mask == nil && layer.VectorMask == nil &&
		!composeCustomRanges(layer.BlendRanges) && len(layer.ChannelRestrictions) == 0 &&
		(layer.TransparencyShapes == nil || *layer.TransparencyShapes) && layer.Adjustment == nil &&
		layer.FillColor == nil && layer.Gradient == nil && layer.Pattern == nil && layer.Placed == nil &&
		!advancedActiveEffects(layer)
}

// checkKnockoutShape runs after descendant structural checks, using the existing seen map once per node.
// Membership still rejects cycles and shared nodes; a completed group's value stores its shape eligibility.
func (plan *composePlan) checkKnockoutShape(layer *psdmodel.Layer, path string) error {
	ordinary := ordinaryKnockoutShape(layer)
	childrenOrdinary := true
	for _, child := range layer.Children {
		if child.Hidden || composeOpacity(child.Opacity) == 0 {
			continue
		}
		if child.Group || child.Children != nil {
			childrenOrdinary = childrenOrdinary && plan.seen[child]
		} else {
			childrenOrdinary = childrenOrdinary && ordinaryKnockoutShape(child)
		}
	}
	if layer.Group || layer.Children != nil {
		plan.seen[layer] = ordinary && childrenOrdinary
	}
	if layer.Knockout != psdmodel.KnockoutNone && !childrenOrdinary {
		return composeError(ErrUnsupported, path+".knockout",
			"group-self Knockout requires ordinary Normal raster children or full isolated Normal subgroups")
	}
	return nil
}

func advancedActiveEffects(layer *psdmodel.Layer) bool {
	if layer.Effects == nil || !layer.Effects.Enabled || !layer.Effects.EffectsVisible {
		return false
	}
	for _, effect := range layer.Effects.Items {
		if effect.Enabled && effect.Opacity != 0 {
			return true
		}
	}
	return false
}

func restrictChannels(layer *psdmodel.Layer, result, backdrop composePixel) composePixel {
	var disabled [3]bool
	for _, channel := range layer.ChannelRestrictions {
		disabled[channel] = true
	}
	if disabled[0] && disabled[1] && disabled[2] {
		return backdrop
	}
	for channel, blocked := range disabled {
		if blocked {
			// Restricted channels retain their premultiplied backdrop contribution under the shared output alpha.
			result.color[channel] = 0
			if result.alpha != 0 {
				result.color[channel] = composeClamp(backdrop.alpha * backdrop.color[channel] / result.alpha)
			}
		}
	}
	return result
}

func (plan *composePlan) knockoutBackdrop(x, y int) composePixel {
	if len(plan.document.Layers) == 0 {
		return composePixel{}
	}
	layer := plan.document.Layers[0]
	if !layer.Background || layer.Hidden || composeOpacity(layer.Opacity) == 0 {
		return composePixel{}
	}
	return plan.paint(layer, plan.source(layer, composePixel{}, x, y), composePixel{}, x, y, 1)
}

func (plan *composePlan) knockoutStack(
	layers []*psdmodel.Layer, backdrop composePixel, scope knockoutScope, x, y int,
) composePixel {
	if !plan.renderLive() {
		return composePixel{}
	}
	for index := 0; index < len(layers); {
		if !plan.renderLive() {
			return composePixel{}
		}
		base := layers[index]
		end := index + 1
		for end < len(layers) && layers[end].Clipping {
			if !plan.renderLive() {
				return composePixel{}
			}
			end++
		}
		if base.Clipping || base.Hidden || composeOpacity(base.Opacity) == 0 {
			index = end
			continue
		}
		if base.Adjustment != nil {
			backdrop = plan.adjust(base, backdrop, x, y, 1)
			index++
			continue
		}
		mode := base.BlendMode
		var source composePixel
		if end > index+1 && mode == "pass" && base.Knockout == psdmodel.KnockoutNone {
			tracked := backdrop
			tracked.residual = 1
			full := plan.knockoutSource(base, tracked, scope, x, y)
			shape := composeClamp(1 - full.residual)
			if plan.options.ApproximateClippedGroupBackdrop && shape != 0 && shape != 1 {
				isolated := plan.knockoutSource(base, composePixel{}, scope, x, y)
				source = passClipGroupIntrinsic(full, backdrop, isolated, shape)
			} else {
				source = passGroupIntrinsic(full, backdrop, shape)
			}
		} else {
			source = plan.knockoutSource(base, backdrop, scope, x, y)
		}
		if end > index+1 && source.alpha == 0 {
			index = end
			continue
		}
		if end > index+1 && source.alpha != 0 {
			// Clip chains share the base source as their shallow boundary.
			chain := source
			chain.alpha = 1
			if base.BlendClipped != nil && !*base.BlendClipped {
				blended := source.color
				if !dissolveMode(mode) {
					blendMode := mode
					if blendMode == "pass" {
						blendMode = "norm"
					}
					blended, _ = advancedBlendRGB(base, blendMode, backdrop.color, source.color,
						composeOpacity(base.FillOpacity))
				}
				for component := range 3 {
					chain.color[component] = (1-backdrop.alpha)*source.color[component] +
						backdrop.alpha*blended[component]
				}
				mode = "norm"
				if dissolveMode(base.BlendMode) {
					mode = "diss"
				}
			}
			// Blend Clipped Layers As Group is an isolation boundary for both native Knockout modes.
			clippingScope := knockoutScope{shallow: chain, deep: chain}
			if base.BlendClipped != nil && !*base.BlendClipped {
				clippingScope = scope
			}
			for _, clipped := range layers[index+1 : end] {
				if !plan.renderLive() {
					return composePixel{}
				}
				if clipped.Hidden || composeOpacity(clipped.Opacity) == 0 {
					continue
				}
				if clipped.Adjustment != nil {
					chain = plan.adjust(clipped, chain, x, y, 1)
					continue
				}
				clipSource := plan.knockoutSource(clipped, chain, clippingScope, x, y)
				chain = plan.knockoutPaint(clipped, clipped.BlendMode, clipSource, chain, clippingScope, x, y)
			}
			source.color = chain.color
			if mode == "pass" {
				mode = "norm"
			}
		}
		backdrop = plan.knockoutPaint(base, mode, source, backdrop, scope, x, y)
		index = end
	}
	return backdrop
}

func (plan *composePlan) knockoutSource(
	layer *psdmodel.Layer, backdrop composePixel, scope knockoutScope, x, y int,
) composePixel {
	if !plan.enterLayerSource() {
		return composePixel{}
	}
	if !layer.Group && layer.Children == nil {
		return plan.source(layer, backdrop, x, y)
	}
	if stage := plan.passRanges[layer]; stage >= passRangeSingleNormal {
		return plan.singlePassRangeSource(layer, stage, backdrop, x, y)
	}
	if layer.BlendMode != "pass" || plan.passRanges[layer] != passRangeNone {
		// Native isolated groups establish their own transparency boundary for both modes.
		return plan.knockoutStack(layer.Children, composePixel{}, knockoutScope{}, x, y)
	}
	if layer.Knockout != psdmodel.KnockoutNone {
		target := scope.shallow
		if layer.Knockout == psdmodel.KnockoutDeep {
			target = scope.deep
		}
		tracked := target
		tracked.residual = 1
		full := plan.knockoutStack(layer.Children, tracked,
			knockoutScope{shallow: target, deep: scope.deep}, x, y)
		return passGroupIntrinsic(full, target, composeClamp(1-full.residual))
	}
	return plan.knockoutStack(layer.Children, backdrop,
		knockoutScope{shallow: backdrop, deep: scope.deep}, x, y)
}

func (plan *composePlan) knockoutPaint(
	layer *psdmodel.Layer, mode string, source, backdrop composePixel, scope knockoutScope, x, y int,
) composePixel {
	if layer.Knockout == psdmodel.KnockoutNone {
		return plan.paintWithMode(layer, mode, source, backdrop, x, y, 1)
	}
	if mode == "pass" {
		mode = "norm"
	}
	target := scope.shallow
	if layer.Knockout == psdmodel.KnockoutDeep {
		target = scope.deep
	}
	shape, fill := advancedCoverageFill(layer, source.alpha, 1,
		plan.mask(layer.Mask, x, y)*plan.vectorAlpha(layer, x, y))
	shape *= composeBlendIf(layer.BlendRanges, source.color, target.color, target.alpha)
	if shape == 0 {
		return backdrop
	}
	paintAlpha := shape * fill
	alpha := (1-shape)*backdrop.alpha + (shape-paintAlpha)*target.alpha + paintAlpha
	if alpha == 0 {
		return composePixel{}
	}
	blended, _ := advancedBlendRGB(layer, mode, target.color, source.color, fill)
	result := composePixel{alpha: alpha, residual: (1-shape)*backdrop.residual + (shape-paintAlpha)*target.residual}
	for component := range 3 {
		premultiplied := (1-shape)*backdrop.alpha*backdrop.color[component] +
			(shape-paintAlpha)*target.alpha*target.color[component]
		if blendUsesSpecialFill(mode) {
			premultiplied += paintAlpha * (1 - target.alpha) * source.color[component]
			premultiplied += shape * target.alpha * (blended[component] - target.color[component])
			premultiplied += paintAlpha * target.alpha * target.color[component]
		} else {
			premultiplied += paintAlpha * ((1-target.alpha)*source.color[component] + target.alpha*blended[component])
		}
		result.color[component] = composeClamp(premultiplied / alpha)
	}
	return result
}
