package psdcompose

import (
	"fmt"
	"math"
	"strings"
)

// SupportedBlend reports whether the RGB compositor implements a PSD blend key.
// Keys may include their format-defined trailing space. English names such as
// "normal", "multiply", "color-burn", and "colorBurn" are also accepted.
// Dissolve is unsupported
// because its coordinate-dependent coverage cannot be reconstructed from color alone.
func SupportedBlend(mode string) bool {
	switch normalizeBlend(mode) {
	case "norm", "dark", "mul", "idiv", "lbrn", "dkCl", "lite", "scrn", "div", "lddg", "lgCl",
		"over", "sLit", "hLit", "vLit", "lLit", "pLit", "hMix", "diff", "smud", "fsub", "fdiv",
		"hue", "sat", "col", "lum":
		return true
	}
	return false
}

func normalizeBlend(mode string) string {
	mode = strings.TrimRight(mode, " ")
	switch mode {
	case "", "normal":
		return "norm"
	case "darken":
		return "dark"
	case "multiply":
		return "mul"
	case "color-burn", "colorBurn":
		return "idiv"
	case "linear-burn", "linearBurn":
		return "lbrn"
	case "darker-color", "darkerColor":
		return "dkCl"
	case "lighten":
		return "lite"
	case "screen":
		return "scrn"
	case "color-dodge", "colorDodge":
		return "div"
	case "linear-dodge", "linearDodge", "add":
		return "lddg"
	case "lighter-color", "lighterColor":
		return "lgCl"
	case "overlay":
		return "over"
	case "soft-light", "softLight":
		return "sLit"
	case "hard-light", "hardLight":
		return "hLit"
	case "vivid-light", "vividLight":
		return "vLit"
	case "linear-light", "linearLight":
		return "lLit"
	case "pin-light", "pinLight":
		return "pLit"
	case "hard-mix", "hardMix":
		return "hMix"
	case "difference":
		return "diff"
	case "exclusion":
		return "smud"
	case "subtract":
		return "fsub"
	case "divide":
		return "fdiv"
	case "saturation":
		return "sat"
	case "color", "colr":
		return "col"
	case "luminosity":
		return "lum"
	}
	return mode
}

// blendUsesSpecialFill identifies modes whose Fill changes the blend kernel.
// For these modes the compositor also keeps coverage and paint alpha separate.
func blendUsesSpecialFill(mode string) bool {
	switch normalizeBlend(mode) {
	case "idiv", "lbrn", "div", "lddg", "vLit", "lLit", "hMix", "diff":
		return true
	}
	return false
}

// blendRGB evaluates a color function on straight, normalized RGB values.
// Ordinary modes leave Fill to the source alpha. Special modes apply Fill here;
// their transparent-backdrop contribution and alpha growth still use Fill outside.
// The component modes follow the PDF luminosity and saturation construction.
func blendRGB(mode string, backdrop, source [3]float64, fill float64) ([3]float64, error) {
	mode = normalizeBlend(mode)
	if !SupportedBlend(mode) {
		return [3]float64{}, fmt.Errorf("blend mode %q: %w", mode, ErrUnsupported)
	}
	fill = clampBlendUnit(fill)
	if blendUsesSpecialFill(mode) && fill == 0 {
		return backdrop, nil
	}
	var result [3]float64
	switch mode {
	case "dkCl":
		result = backdrop
		if blendChoiceGray(source, 50) < blendChoiceGray(backdrop, 50) {
			result = source
		}
	case "lgCl":
		result = backdrop
		if blendChoiceGray(source, 49) > blendChoiceGray(backdrop, 49) {
			result = source
		}
	case "hue":
		result = blendSetLum(blendSetSat(source, blendSat(backdrop)), blendLum(backdrop))
	case "sat":
		result = blendSetLum(blendSetSat(backdrop, blendSat(source)), blendLum(backdrop))
	case "col":
		result = blendSetLum(source, blendLum(backdrop))
	case "lum":
		result = blendSetLum(backdrop, blendLum(source))
	default:
		for channel := range result {
			result[channel] = blendChannel(mode, backdrop[channel], source[channel], fill)
		}
	}
	for channel := range result {
		result[channel] = clampBlendUnit(result[channel])
	}
	return result, nil
}

// blendChoiceGray evaluates the native RGB8 whole-color selection stage before alpha blending.
// Darker Color rounds half upward; Lighter Color rounds half downward. Equal gray keeps the backdrop.
func blendChoiceGray(color [3]float64, bias float64) float64 {
	amount := 30*math.Round(color[0]*255) + 59*math.Round(color[1]*255) + 11*math.Round(color[2]*255)
	return math.Floor((amount + bias) / 100)
}

func blendChannel(mode string, backdrop, source, fill float64) float64 {
	switch mode {
	case "norm":
		return source
	case "dark":
		return math.Min(backdrop, source)
	case "mul":
		return backdrop * source
	case "idiv":
		return blendBurn(backdrop, 1-fill+fill*source)
	case "lbrn":
		return backdrop + fill*(source-1)
	case "lite":
		return math.Max(backdrop, source)
	case "scrn":
		return backdrop + source - backdrop*source
	case "div":
		return blendDodge(backdrop, fill*source)
	case "lddg":
		return backdrop + fill*source
	case "over":
		return blendHardLight(source, backdrop)
	case "sLit":
		if source <= 0.5 {
			return backdrop - (1-2*source)*backdrop*(1-backdrop)
		}
		curve := math.Sqrt(backdrop)
		if backdrop <= 0.25 {
			curve = ((16*backdrop-12)*backdrop + 4) * backdrop
		}
		return backdrop + (2*source-1)*(curve-backdrop)
	case "hLit":
		return blendHardLight(backdrop, source)
	case "vLit":
		if source <= 0.5 {
			denominator := 1 - fill*(1-2*source)
			if denominator == 0 {
				return 0
			}
			return 1 - (1-backdrop)/denominator
		}
		denominator := 1 - fill*(2*source-1)
		if denominator == 0 {
			return 1
		}
		return backdrop / denominator
	case "lLit":
		return backdrop + fill*(2*source-1)
	case "pLit":
		if source <= 0.5 {
			return math.Min(backdrop, 2*source)
		}
		return math.Max(backdrop, 2*source-1)
	case "hMix":
		if fill < 1 {
			return (backdrop - fill*(1-source)) / (1 - fill)
		}
		total := backdrop + source
		if total > 1 || total == 1 && backdrop > 0.5 {
			return 1
		}
		return 0
	case "diff":
		return math.Abs(backdrop - fill*source)
	case "smud":
		return backdrop + source - 2*backdrop*source
	case "fsub":
		return backdrop - source
	case "fdiv":
		if backdrop == 0 {
			return 0
		}
		if source == 0 {
			return 1
		}
		return backdrop / source
	}
	return source
}

func blendBurn(backdrop, source float64) float64 {
	if backdrop == 1 {
		return 1
	}
	if source == 0 {
		return 0
	}
	return 1 - math.Min(1, (1-backdrop)/source)
}

func blendDodge(backdrop, source float64) float64 {
	if backdrop == 0 {
		return 0
	}
	if source == 1 {
		return 1
	}
	return math.Min(1, backdrop/(1-source))
}

func blendHardLight(backdrop, source float64) float64 {
	if source <= 0.5 {
		return 2 * backdrop * source
	}
	return 1 - 2*(1-backdrop)*(1-source)
}

func blendLum(color [3]float64) float64 {
	return 0.3*color[0] + 0.59*color[1] + 0.11*color[2]
}

func blendSat(color [3]float64) float64 {
	return max(color[0], color[1], color[2]) - min(color[0], color[1], color[2])
}

func blendSetLum(color [3]float64, luminosity float64) [3]float64 {
	delta := luminosity - blendLum(color)
	for channel := range color {
		color[channel] += delta
	}
	luminosity = blendLum(color)
	lowest := min(color[0], color[1], color[2])
	highest := max(color[0], color[1], color[2])
	if lowest < 0 {
		for channel := range color {
			color[channel] = luminosity + (color[channel]-luminosity)*luminosity/(luminosity-lowest)
		}
	}
	if highest > 1 {
		for channel := range color {
			color[channel] = luminosity + (color[channel]-luminosity)*(1-luminosity)/(highest-luminosity)
		}
	}
	return color
}

func blendSetSat(color [3]float64, saturation float64) [3]float64 {
	lowIndex, highIndex := 0, 0
	for channel := 1; channel < 3; channel++ {
		if color[channel] < color[lowIndex] {
			lowIndex = channel
		}
		if color[channel] > color[highIndex] {
			highIndex = channel
		}
	}
	span := color[highIndex] - color[lowIndex]
	if span == 0 {
		return [3]float64{}
	}
	middleIndex := 3 - lowIndex - highIndex
	var result [3]float64
	result[highIndex] = saturation
	result[middleIndex] = (color[middleIndex] - color[lowIndex]) * saturation / span
	return result
}

func clampBlendUnit(value float64) float64 {
	return max(0, min(1, value))
}
