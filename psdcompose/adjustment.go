package psdcompose

import (
	"errors"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func (plan *composePlan) prepareAdjustments() error {
	const transformBytes = int64(16 << 10)
	if int64(len(plan.adjustmentLayers)) > (plan.options.MaxWorkingBytes-plan.workingBytes)/transformBytes {
		return composeError(ErrLimit, "document.adjustments", "adjustment transforms exceed the working budget")
	}
	plan.workingBytes += int64(len(plan.adjustmentLayers)) * transformBytes
	for _, layer := range plan.adjustmentLayers {
		if layer.Group || layer.Children != nil || layer.FillColor != nil || layer.Placed != nil {
			return composeError(ErrInvalid, "document.adjustments", "adjustment layer conflicts with another layer kind")
		}
		transform, err := psdadjust.Compile(*layer.Adjustment, psdadjust.CompileOptions{
			ApproximateHueSaturation: plan.options.ApproximateHueSaturation,
		})
		if err != nil {
			kind := ErrInvalid
			if errors.Is(err, psdadjust.ErrUnsupported) {
				kind = ErrUnsupported
			} else if errors.Is(err, psdadjust.ErrLimit) {
				kind = ErrLimit
			}
			return composeError(kind, "document.adjustments", err.Error())
		}
		plan.adjustments[layer] = transform
	}
	return nil
}

// An adjustment changes the existing backdrop's color while retaining its exact coverage.
func (plan *composePlan) adjust(
	layer *psdmodel.Layer, backdrop composePixel, x, y int, clip float64,
) composePixel {
	if backdrop.alpha == 0 {
		return backdrop
	}
	transform := plan.adjustments[layer]
	if transform == nil {
		return backdrop
	}
	adjusted := transform.RGB(backdrop.color)
	amount := clip * composeOpacity(layer.Opacity) * composeOpacity(layer.FillOpacity) *
		plan.mask(layer.Mask, x, y) * plan.vectorAlpha(layer, x, y)
	amount *= composeBlendIf(layer.BlendRanges, adjusted, backdrop.color, backdrop.alpha)
	blended, _ := blendRGB(layer.BlendMode, backdrop.color, adjusted, 1)
	original := backdrop
	for component := range 3 {
		backdrop.color[component] = composeClamp(backdrop.color[component]*(1-amount) + blended[component]*amount)
	}
	return restrictChannels(layer, backdrop, original)
}
