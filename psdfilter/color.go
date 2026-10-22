package psdfilter

import (
	"context"
	"errors"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdadjust"
)

// colorFilterWorkingBytes bounds one immutable transform and its temporary validation state.
const colorFilterWorkingBytes int64 = 8 << 10

type colorFilterPlan struct {
	transform  *psdadjust.Transform
	brightness *[256]float64
}

func colorFilterValidate(filter Filter, configuration Options) error {
	_, err := compileColorFilter(filter, configuration)
	return err
}

func compileColorFilter(filter Filter, configuration Options) (colorFilterPlan, error) {
	var plan colorFilterPlan
	if filter.Adjustment == nil || Kind(filter.Adjustment.Kind) != filter.Kind {
		return plan, filterError(ErrInvalid, "filter.adjustment", "provide matching typed color adjustment controls")
	}
	if len(filter.Adjustment.Unsupported) != 0 {
		return plan, filterError(ErrUnsupported, "filter.adjustment", "source color adjustment semantics are unavailable")
	}
	switch filter.Kind {
	case BrightnessContrast:
		parameters := filter.Adjustment.BrightnessContrast
		if parameters == nil || parameters.Mean > 255 {
			return plan, filterError(ErrInvalid, "brightnessContrast", "brightness/contrast controls are absent or invalid")
		}
		brightnessLimit, contrastMinimum := int16(150), int16(-50)
		if parameters.Legacy {
			brightnessLimit, contrastMinimum = 100, -100
		}
		if parameters.Brightness < -brightnessLimit || parameters.Brightness > brightnessLimit ||
			parameters.Contrast < contrastMinimum || parameters.Contrast > 100 {
			return plan, filterError(ErrInvalid, "brightnessContrast", "brightness or contrast exceeds its source mode range")
		}
		if parameters.LabOnly {
			return plan, filterError(ErrUnsupported, "brightnessContrast.labOnly", "Lab-only adjustment requires a Lab pipeline")
		}
		if !configuration.ApproximateBrightnessContrast &&
			(parameters.Brightness != 0 || parameters.Contrast != 0) {
			return plan, filterError(ErrUnsupported, "brightnessContrast",
				"non-neutral brightness/contrast requires an explicit approximation policy")
		}
		plan.brightness = new([256]float64)
		for value := range 256 {
			plan.brightness[value] = approximateBrightnessContrast(float64(value)/255, *parameters)
		}
	case HueSaturation:
		transform, err := psdadjust.Compile(*filter.Adjustment, psdadjust.CompileOptions{
			ApproximateHueSaturation: configuration.ApproximateHueSaturation,
		})
		if err != nil {
			kind := ErrInvalid
			if errors.Is(err, psdadjust.ErrUnsupported) {
				kind = ErrUnsupported
			} else if errors.Is(err, psdadjust.ErrLimit) {
				kind = ErrLimit
			}
			return plan, filterError(kind, "hueSaturation", "typed hue/saturation controls cannot be compiled")
		}
		plan.transform = transform
	default:
		return plan, filterError(ErrUnsupported, "filter.kind", "color filter kind is unavailable")
	}
	return plan, nil
}

func colorFilter(
	ctx context.Context, source, target *image.NRGBA, filter Filter, configuration Options,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	plan, err := compileColorFilter(filter, configuration)
	if err != nil {
		return err
	}
	for row := 0; row < source.Rect.Dy(); row++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for column := 0; column < source.Rect.Dx(); column++ {
			if column&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			input, output := row*source.Stride+column*4, row*target.Stride+column*4
			alpha := source.Pix[input+3]
			if alpha == 0 {
				copy(target.Pix[output:output+4], source.Pix[input:input+4])
				continue
			}
			original := [3]float64{
				float64(source.Pix[input]) / 255,
				float64(source.Pix[input+1]) / 255,
				float64(source.Pix[input+2]) / 255,
			}
			adjusted := original
			if plan.transform != nil {
				adjusted = plan.transform.RGB(original)
			} else {
				for component := range adjusted {
					adjusted[component] = plan.brightness[source.Pix[input+component]]
				}
			}
			for component := range adjusted {
				// Both samples have identical alpha, so premultiplied crossfade reduces to straight RGB interpolation.
				value := original[component] + filter.Opacity*(adjusted[component]-original[component])
				target.Pix[output+component] = roundedByte(value * 255)
			}
			target.Pix[output+3] = alpha
		}
	}
	return ctx.Err()
}

// approximateBrightnessContrast is a declared encoded-RGB model, not Photoshop's private transfer curve.
// Modern brightness uses a monotone rational midpoint shift, then symmetric power contrast about one half.
// Legacy brightness shifts linearly and scales contrast about the explicitly retained Mean pivot.
func approximateBrightnessContrast(value float64, parameters psdadjust.BrightnessContrastParams) float64 {
	if parameters.Legacy {
		value += float64(parameters.Brightness) / 100
		pivot := float64(parameters.Mean) / 255
		if parameters.Contrast == 100 {
			if value >= pivot {
				return 1
			}
			return 0
		}
		factor := 1 + float64(parameters.Contrast)/100
		if parameters.Contrast > 0 {
			factor = 100 / (100 - float64(parameters.Contrast))
		}
		return max(0, min(1, (value-pivot)*factor+pivot))
	}
	if parameters.Brightness != 0 {
		factor := math.Exp2(-float64(parameters.Brightness) / 75)
		value /= value + (1-value)*factor
	}
	if parameters.Contrast != 0 {
		exponent := math.Exp2(float64(parameters.Contrast) / 50)
		if value < .5 {
			value = .5 * math.Pow(2*value, exponent)
		} else {
			value = 1 - .5*math.Pow(2*(1-value), exponent)
		}
	}
	return max(0, min(1, value))
}
