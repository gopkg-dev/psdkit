package psdcompose

import (
	"fmt"
	"math"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

// advancedCoverageFill separates shape coverage from fill modulation for tsly=false.
// Adobe specifies that layer transparency then modulates the blend kernel like Fill.
func advancedCoverageFill(
	layer *psdmodel.Layer, sourceAlpha, clip, maskCoverage float64,
) (float64, float64) {
	opacity := composeOpacity(layer.Opacity)
	fill := composeOpacity(layer.FillOpacity)
	coverage := sourceAlpha * clip * opacity * maskCoverage
	if layer.TransparencyShapes != nil && !*layer.TransparencyShapes {
		coverage = clip * opacity * maskCoverage
		fill *= sourceAlpha
	}
	return coverage, fill
}

// advancedBlendRGB preserves the standard kernels and applies byte-quantized Hard Mix modulation.
// The effective Fill includes pixel transparency for tsly=false, so its intermediate
// ramp must use the native byte scale rather than treating transparency as coverage.
func advancedBlendRGB(
	layer *psdmodel.Layer, mode string, backdrop, source [3]float64, fill float64,
) ([3]float64, error) {
	if normalizeBlend(mode) != "hMix" || layer.TransparencyShapes == nil || *layer.TransparencyShapes {
		return blendRGB(mode, backdrop, source, fill)
	}
	if fill == 0 {
		return backdrop, nil
	}
	fillByte := math.Round(composeClamp(fill) * 255)
	if fillByte >= 128 {
		fillByte--
	}
	var result [3]float64
	for component := range 3 {
		backdropByte := math.Round(composeClamp(backdrop[component]) * 255)
		sourceByte := math.Round(composeClamp(source[component]) * 255)
		anchor := math.Round((255 - sourceByte) * fillByte / 255)
		value := math.Round((backdropByte-anchor)*255/(255-fillByte)) / 255
		result[component] = composeClamp(value)
	}
	return result, nil
}

// passGroupIntrinsic removes only the group's original backdrop contribution.
// The retained color includes child blends evaluated against that backdrop, while
// groupAlpha retains the child coverage before the group's own opacity and mask.
func passGroupIntrinsic(full, backdrop composePixel, groupAlpha float64) composePixel {
	if groupAlpha == 0 {
		return composePixel{}
	}
	result := composePixel{alpha: groupAlpha}
	for component := range 3 {
		premultiplied := full.color[component]*full.alpha -
			(1-groupAlpha)*backdrop.alpha*backdrop.color[component]
		result.color[component] = composeClamp(premultiplied / groupAlpha)
	}
	return result
}

// passGroupNormalRanges preserves the legacy calibrated flat Normal Blend If subset.
// Normal children have no backdrop-dependent color kernel, so their source can be
// composed over transparency before the group's own ranges, opacity and mask are applied once.
// External clipping-base status is checked separately against the enclosing sibling list.
func passGroupNormalRanges(layer *psdmodel.Layer) bool {
	if layer == nil || layer.BlendMode != "pass" || (!layer.Group && layer.Children == nil) ||
		!composeCustomRanges(layer.BlendRanges) || layer.Clipping || len(layer.Children) == 0 ||
		layer.Effects != nil || layer.Adjustment != nil || layer.FillColor != nil ||
		layer.Gradient != nil || layer.Pattern != nil || layer.Placed != nil ||
		layer.Knockout != psdmodel.KnockoutNone || len(layer.ChannelRestrictions) != 0 ||
		layer.TransparencyShapes != nil && !*layer.TransparencyShapes {
		return false
	}
	for _, child := range layer.Children {
		if child == nil {
			return false
		}
		if child.Hidden || composeOpacity(child.Opacity) == 0 {
			continue
		}
		if child.Group || child.Children != nil || child.Clipping || normalizeBlend(child.BlendMode) != "norm" ||
			composeCustomRanges(child.BlendRanges) || child.Pixels == nil || child.Effects != nil ||
			child.Adjustment != nil || child.FillColor != nil || child.Gradient != nil ||
			child.Pattern != nil || child.Placed != nil ||
			child.Knockout != psdmodel.KnockoutNone || len(child.ChannelRestrictions) != 0 ||
			child.TransparencyShapes != nil && !*child.TransparencyShapes {
			return false
		}
	}
	return true
}

// passRangesClipBaseCheck rejects active pass groups with their own ranges as external clipping bases.
// Call after structural validation: node sharing, cycles, image shape and traversal depth are already bounded.
// Plain pass groups and isolated groups keep their existing clipping behavior.
func passRangesClipBaseCheck(layers []*psdmodel.Layer) error {
	var visit func([]*psdmodel.Layer, string, bool) error
	visit = func(siblings []*psdmodel.Layer, path string, hidden bool) error {
		for index, layer := range siblings {
			if layer == nil {
				continue
			}
			layerPath := fmt.Sprintf("%s[%d]", path, index)
			invisible := hidden || layer.Hidden || composeOpacity(layer.Opacity) == 0
			if !invisible && !layer.Clipping && layer.BlendMode == "pass" &&
				(layer.Group || layer.Children != nil) && composeCustomRanges(layer.BlendRanges) &&
				index+1 < len(siblings) && siblings[index+1] != nil && siblings[index+1].Clipping {
				return composeError(ErrUnsupported, layerPath+".blendRanges",
					"pass-through group Blend If on an external clipping base is uncalibrated")
			}
			if err := visit(layer.Children, layerPath+".children", invisible); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(layers, "document.layers", false)
}
