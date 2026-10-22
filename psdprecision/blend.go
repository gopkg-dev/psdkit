package psdprecision

import (
	"fmt"
	"math"
)

func normalizedBlend(mode string) string {
	switch mode {
	case "", "norm", "normal":
		return "norm"
	case "mul ", "mul", "multiply":
		return "mul"
	case "scrn", "screen":
		return "scrn"
	case "lddg", "linear-dodge", "linearDodge":
		return "lddg"
	case "dark", "darken":
		return "dark"
	case "lite", "lighten":
		return "lite"
	case "diff", "difference":
		return "diff"
	case "smud", "exclusion":
		return "smud"
	case "pass":
		return "pass"
	default:
		return mode
	}
}

// SupportsBlend reports the calibrated high-depth modes in the declared color domain.
// Screen and Exclusion are unavailable in Photoshop's RGB32 domain; Difference32 is not yet calibrated here.
// Pass-through applies only to groups.
func SupportsBlend(mode string, encoding Encoding) bool {
	if encoding > LinearRGB {
		return false
	}
	switch normalizedBlend(mode) {
	case "norm", "mul", "lddg", "dark", "lite":
		return true
	case "scrn", "diff", "smud":
		return encoding == EncodedRGB
	default:
		return false
	}
}

func groupNeedsBuffer(layer *Layer) bool {
	return normalizedBlend(layer.BlendMode) != "pass" || layerOpacity(layer) != 1 ||
		layer.Mask != nil && !layer.Mask.Disabled
}

func (plan *precisionPlan) blendComponent(backdrop, source float64, mode string) (float64, error) {
	var blended float64
	switch normalizedBlend(mode) {
	case "norm":
		blended = source
	case "mul":
		blended = backdrop * source
	case "scrn":
		blended = backdrop + source - backdrop*source
	case "lddg":
		blended = backdrop + source
		if plan.scene.Encoding == EncodedRGB {
			// RGB16 Linear Dodge saturates the overlap operation; RGB32 retains unbounded sums.
			blended = math.Min(1, blended)
		}
	case "dark":
		blended = math.Min(backdrop, source)
	case "lite":
		blended = math.Max(backdrop, source)
	case "diff":
		blended = math.Abs(backdrop - source)
	case "smud":
		blended = backdrop + source - 2*backdrop*source
	default:
		return 0, fmt.Errorf("%w: blend mode %q", ErrUnsupported, mode)
	}
	return plan.sample(blended, false)
}

func (plan *precisionPlan) blendPremultiplied(
	target []float64, offset int, source [3]float64, alpha float64, mode string,
) error {
	backdropAlpha := target[offset+3]
	if alpha == 0 {
		return nil
	}
	for component := range 3 {
		premultiplied := source[component]*alpha + target[offset+component]*(1-alpha)
		if normalizedBlend(mode) != "norm" && backdropAlpha != 0 {
			backdrop := target[offset+component] / backdropAlpha
			blended, err := plan.blendComponent(backdrop, source[component], mode)
			if err != nil {
				return err
			}
			premultiplied = target[offset+component]*(1-alpha) +
				source[component]*alpha*(1-backdropAlpha) + blended*alpha*backdropAlpha
		}
		value, err := plan.sample(premultiplied, false)
		if err != nil {
			return err
		}
		target[offset+component] = value
	}
	target[offset+3] = alpha + backdropAlpha*(1-alpha)
	return nil
}
