package psdcompose

import (
	"fmt"
	"math"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

// passRangeVisibleKnockout checks the completed tree, including Knockout siblings visited after a range group.
func passRangeVisibleKnockout(layers []*psdmodel.Layer, hidden bool) bool {
	clippingHidden := true
	for _, layer := range layers {
		invisible := hidden || layer.Hidden || composeOpacity(layer.Opacity) == 0
		if layer.Clipping {
			invisible = invisible || clippingHidden
		} else {
			clippingHidden = invisible
		}
		if !invisible && layer.Knockout != psdmodel.KnockoutNone {
			return true
		}
		if passRangeVisibleKnockout(layer.Children, invisible) {
			return true
		}
	}
	return false
}

// restrictPassRangeKnockout retains legacy Normal behavior and rejects new stages in visible Knockout contexts.
func (plan *composePlan) restrictPassRangeKnockout(layers []*psdmodel.Layer, path string) error {
	for index, layer := range layers {
		layerPath := fmt.Sprintf("%s[%d]", path, index)
		stage := plan.passRanges[layer]
		if stage == passRangeSingleNormal {
			plan.passRanges[layer] = passRangeLegacyNormal
		} else if stage >= passRangeSingleIntrinsic {
			return composeError(ErrUnsupported, layerPath+".blendRanges",
				"single-raster pass-through group Blend If with visible Knockout is uncalibrated")
		}
		if err := plan.restrictPassRangeKnockout(layer.Children, layerPath+".children"); err != nil {
			return err
		}
	}
	return nil
}

// passRangeStage reuses the existing per-layer plan storage for calibrated source stages.
type passRangeStage uint8

const (
	passRangeNone passRangeStage = iota
	passRangeLegacyNormal
	passRangeSingleNormal
	passRangeSingleIntrinsic
	passRangeSingleNormalized
)

// passGroupRangeStage keeps legacy Normal groups and recognizes the measured single-raster gray subset.
// Without provenance on Layer, document warnings conservatively retain only the legacy path.
func passGroupRangeStage(layer *psdmodel.Layer, ordinaryProvenance bool) passRangeStage {
	if ordinaryProvenance && singlePassRangeSource(layer) {
		mode := normalizeBlend(layer.Children[0].BlendMode)
		if mode == "norm" {
			return passRangeSingleNormal
		}
		if blendUsesSpecialFill(mode) {
			return passRangeSingleIntrinsic
		}
		return passRangeSingleNormalized
	}
	if passGroupNormalRanges(layer) {
		return passRangeLegacyNormal
	}
	return passRangeNone
}

// singlePassRangeSource restricts the new stage to one saved raster child at full Fill.
// Non-Normal multi-child, nested, special-Fill, RGB-range and external-clip cases remain uncalibrated.
func singlePassRangeSource(layer *psdmodel.Layer) bool {
	if layer == nil || layer.BlendMode != "pass" || (!layer.Group && layer.Children == nil) ||
		len(layer.Children) != 1 || !composeCustomRanges(layer.BlendRanges) ||
		composeCustomRanges(layer.BlendRanges[1:]) || composeOpacity(layer.FillOpacity) != 1 ||
		!passRangeOrdinaryFlags(layer) || layer.VectorMask != nil || layer.VectorDensity != nil ||
		layer.VectorFeather != nil || !passRangeSavedMask(layer.Mask) {
		return false
	}
	child := layer.Children[0]
	return child != nil && !child.Group && child.Children == nil && child.Pixels != nil &&
		SupportedBlend(child.BlendMode) && composeOpacity(child.FillOpacity) == 1 &&
		!composeCustomRanges(child.BlendRanges) && passRangeOrdinaryFlags(child) &&
		child.Mask == nil && child.VectorMask == nil && child.VectorDensity == nil && child.VectorFeather == nil
}

// passRangeOrdinaryFlags excludes advanced source behavior absent from the native single-child matrix.
func passRangeOrdinaryFlags(layer *psdmodel.Layer) bool {
	return !layer.Clipping && !layer.Background && layer.Effects == nil && layer.Adjustment == nil &&
		layer.FillColor == nil && layer.Gradient == nil && layer.Pattern == nil && layer.Placed == nil &&
		layer.Knockout == psdmodel.KnockoutNone && len(layer.ChannelRestrictions) == 0 &&
		len(layer.Unsupported) == 0 && (layer.TransparencyShapes == nil || *layer.TransparencyShapes) &&
		(layer.BlendClipped == nil || *layer.BlendClipped) &&
		(layer.BlendInterior == nil || !*layer.BlendInterior) &&
		layer.MaskAsGlobal == nil && layer.VectorMaskAsGlobal == nil
}

// passRangeSavedMask accepts the ordinary saved raster mask used by the native fixtures.
// Coverage remains a sampled scalar; no additional full-canvas storage or scan is required.
func passRangeSavedMask(mask *psdmodel.Mask) bool {
	return mask == nil || !mask.Disabled && !mask.Invert && mask.Pixels != nil &&
		len(mask.Unsupported) == 0 && mask.DefaultColor != nil && *mask.DefaultColor == 0 &&
		(mask.Density == nil || *mask.Density == 255) && (mask.Feather == nil || *mask.Feather == 0)
}

// singlePassRangeSource evaluates child coverage before the group's own ranges, opacity and mask.
// Ordinary kernels use the normalized lifted color; the eight special-Fill kernels retain the intrinsic color.
func (plan *composePlan) singlePassRangeSource(
	layer *psdmodel.Layer, stage passRangeStage, backdrop composePixel, x, y int,
) composePixel {
	transparent := plan.stack(layer.Children, composePixel{}, x, y)
	if stage == passRangeSingleNormal {
		return transparent
	}
	full := plan.stack(layer.Children, backdrop, x, y)
	intrinsic := passGroupIntrinsic(full, backdrop, transparent.alpha)
	if stage == passRangeSingleIntrinsic {
		return intrinsic
	}
	amount := backdrop.alpha * (1 - transparent.alpha)
	for component := range 3 {
		transparent.color[component] = (intrinsic.color[component] + amount*transparent.color[component]) /
			(1 + amount)
	}
	return transparent
}

// singlePassGrayRanges applies the native source-byte gray stage and floating destination gray once.
func singlePassGrayRanges(
	pair psdmodel.BlendRange, source, backdrop [3]float64, backdropAlpha float64,
) float64 {
	sourceGray, backdropGray := float64(0), float64(0)
	for component, weight := range [3]float64{.3, .59, .11} {
		sourceGray += math.Round(composeClamp(source[component])*255) / 255 * weight
		backdropGray += backdrop[component] * weight
	}
	sourceGray = math.Round(sourceGray*255) / 255
	return composeSlider(pair.Source, sourceGray) *
		(1 - backdropAlpha + backdropAlpha*composeSlider(pair.Destination, backdropGray))
}
