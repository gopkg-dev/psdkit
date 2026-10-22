package psdprecision

import (
	"fmt"
	"image"
)

func validateRanges(ranges []BlendRange) error {
	identity := BlendSlider{WhiteLow: 255, WhiteHigh: 255}
	for index, item := range ranges {
		for _, slider := range []BlendSlider{item.Source, item.Destination} {
			if slider.BlackLow > slider.BlackHigh || slider.WhiteLow > slider.WhiteHigh {
				return fmt.Errorf("%w: unordered Blend If split thresholds", ErrInvalid)
			}
			if index >= 4 && slider != identity {
				return fmt.Errorf("%w: Blend If channel beyond RGB", ErrUnsupported)
			}
		}
	}
	return nil
}

func rangeSlider(slider BlendSlider, value float64) float64 {
	value *= 255
	// Photoshop's high-depth split ramp retains a one-unit endpoint bias on its 15-bit control domain.
	step := 255.0 / 32768
	black, white := float64(1), float64(1)
	if slider.BlackLow != 0 || slider.BlackHigh != 0 {
		if value < float64(slider.BlackLow) {
			black = 0
		} else if slider.BlackHigh > slider.BlackLow && value < float64(slider.BlackHigh) {
			black = (value - float64(slider.BlackLow) + step) /
				(float64(int(slider.BlackHigh)-int(slider.BlackLow)) + step)
		}
	}
	if slider.WhiteLow != 255 || slider.WhiteHigh != 255 {
		if value > float64(slider.WhiteHigh) {
			white = 0
		} else if slider.WhiteHigh > slider.WhiteLow && value > float64(slider.WhiteLow) {
			white = (float64(slider.WhiteHigh) - value + step) /
				(float64(int(slider.WhiteHigh)-int(slider.WhiteLow)) + step)
		}
	}
	return min(black, white)
}

func rangeCoverage(ranges []BlendRange, source, backdrop [3]float64, backdropAlpha float64) float64 {
	coverage := float64(1)
	for index, item := range ranges[:min(len(ranges), 4)] {
		var sourceValue, backdropValue float64
		if index == 0 {
			sourceValue = .3*source[0] + .59*source[1] + .11*source[2]
			backdropValue = .3*backdrop[0] + .59*backdrop[1] + .11*backdrop[2]
		} else {
			sourceValue, backdropValue = source[index-1], backdrop[index-1]
		}
		coverage *= rangeSlider(item.Source, sourceValue) *
			(1 - backdropAlpha + backdropAlpha*rangeSlider(item.Destination, backdropValue))
	}
	return coverage
}

func (plan *precisionPlan) sourcePixel(layer *Layer, x, y int) ([3]float64, float64, error) {
	var color [3]float64
	if layer.Pixels == nil || !image.Pt(x, y).In(layer.Bounds) {
		return color, 0, nil
	}
	offset := ((y-layer.Bounds.Min.Y)*layer.Pixels.Width + x - layer.Bounds.Min.X) * 4
	alpha, err := plan.sample(layer.Pixels.Pix[offset+3], true)
	if err != nil {
		return color, 0, err
	}
	for component := range color {
		value, err := plan.sample(layer.Pixels.Pix[offset+component], false)
		if err != nil {
			return color, 0, err
		}
		color[component] = value
	}
	return color, alpha, nil
}

func straightBackdrop(target []float64, offset int) ([3]float64, float64) {
	var color [3]float64
	alpha := target[offset+3]
	if alpha != 0 {
		for component := range color {
			color[component] = target[offset+component] / alpha
		}
	}
	return color, alpha
}

func (plan *precisionPlan) applyPixel(
	layer *Layer, color [3]float64, alpha float64, target []float64, offset, x, y int, mode string,
) error {
	mask, err := plan.maskCoverage(layer.Mask, x, y)
	if err != nil {
		return err
	}
	backdrop, backdropAlpha := straightBackdrop(target, offset)
	alpha *= mask * layerOpacity(layer) * rangeCoverage(layer.BlendRanges, color, backdrop, backdropAlpha)
	return plan.blendPremultiplied(target, offset, color, alpha, mode)
}

func (plan *precisionPlan) clippingPixel(
	base *Layer, clipped []*Layer, target []float64, offset, x, y int,
) error {
	color, alpha, err := plan.sourcePixel(base, x, y)
	if err != nil || alpha == 0 {
		return err
	}
	grouped := base.BlendClipped == nil || *base.BlendClipped
	if !grouped {
		backdrop, backdropAlpha := straightBackdrop(target, offset)
		for component := range color {
			blended, err := plan.blendComponent(backdrop[component], color[component], base.BlendMode)
			if err != nil {
				return err
			}
			color[component] = color[component]*(1-backdropAlpha) + blended*backdropAlpha
		}
	}
	chain := [4]float64{color[0], color[1], color[2], 1}
	for _, layer := range clipped {
		if layer.Hidden || layerOpacity(layer) == 0 {
			continue
		}
		top, topAlpha, err := plan.sourcePixel(layer, x, y)
		if err != nil {
			return err
		}
		if err := plan.applyPixel(layer, top, topAlpha, chain[:], 0, x, y, layer.BlendMode); err != nil {
			return err
		}
	}
	color = [3]float64{chain[0], chain[1], chain[2]}
	mode := base.BlendMode
	if !grouped {
		mode = "norm"
	}
	return plan.applyPixel(base, color, alpha, target, offset, x, y, mode)
}
