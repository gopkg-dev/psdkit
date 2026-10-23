package psdfilter

import "math"

// SupportRadius validates one filter and returns its finite input dependency radius in pixels.
// Color adjustments have zero support but remain active operations.
func SupportRadius(filter Filter, options ...Options) (int, error) {
	configuration, err := renderOptions(options)
	if err != nil {
		return 0, err
	}
	return validateFilter(filter, configuration)
}

// RequiredHalo validates a complete stack and sums its active spatial dependencies.
// Callers provide this halo around an output region before calling ApplyRegion.
func RequiredHalo(stack Stack, options ...Options) (int, error) {
	configuration, err := renderOptions(options)
	if err != nil {
		return 0, err
	}
	if len(stack.Filters) > configuration.MaxFilters {
		return 0, filterError(ErrLimit, "filters", "filter count exceeds the operation limit")
	}
	var total int
	for _, filter := range stack.Filters {
		if err := validateCommon(filter); err != nil {
			return 0, err
		}
		if !stack.Enabled || !filter.Enabled || filter.Opacity == 0 {
			continue
		}
		radius, err := validateFilter(filter, configuration)
		if err != nil {
			return 0, err
		}
		if total > int(^uint(0)>>1)-radius {
			return 0, filterError(ErrLimit, "halo", "combined input support overflows")
		}
		total += radius
	}
	return total, nil
}

func validateCommon(filter Filter) error {
	if !finite(filter.Radius) || filter.Radius < 0 || !finite(filter.Opacity) ||
		filter.Opacity < 0 || filter.Opacity > 1 {
		return filterError(ErrInvalid, "filter", "radius or opacity is invalid")
	}
	return nil
}

func validateFilter(filter Filter, configuration Options) (int, error) {
	if err := validateCommon(filter); err != nil {
		return 0, err
	}
	if !filter.Enabled || filter.Opacity == 0 {
		return 0, nil
	}
	if filter.BlendMode != "norm" && filter.BlendMode != "nrml" && filter.BlendMode != "Nrml" {
		return 0, filterError(ErrUnsupported, "filter.blendMode", "filter blending is unavailable")
	}
	switch filter.Kind {
	case GaussianBlur:
		if filter.Radius > 250 {
			return 0, filterError(ErrLimit, "gaussian.radius", "radius exceeds the bounded RGB8 filter limit")
		}
		if filter.Adjustment != nil {
			return 0, filterError(ErrInvalid, "gaussian", "color controls do not belong to this algorithm")
		}
		if filter.Radius == 0 {
			return 0, nil
		}
		if !configuration.ApproximateGaussian {
			return 0, filterError(ErrUnsupported, "gaussian", "an explicit Gaussian approximation policy is required")
		}
		return gaussianSupportRadius(filter.Radius), nil
	case SurfaceBlur:
		if !finite(filter.Threshold) || filter.Threshold < 0 || filter.Threshold > 255 {
			return 0, filterError(ErrInvalid, "surfaceBlur.threshold", "threshold must be zero through 255")
		}
		if filter.Radius > 32 {
			return 0, filterError(ErrLimit, "surfaceBlur.radius", "radius exceeds the bounded neighborhood limit")
		}
		if filter.Adjustment != nil {
			return 0, filterError(ErrInvalid, "surfaceBlur", "color controls do not belong to this algorithm")
		}
		if filter.Radius == 0 || filter.Threshold == 0 {
			return 0, nil
		}
		if !configuration.ApproximateSurfaceBlur {
			return 0, filterError(ErrUnsupported, "surfaceBlur", "an explicit Surface Blur approximation policy is required")
		}
		return int(math.Ceil(filter.Radius)), nil
	case SmartSharpen:
		if !finite(filter.Amount) || filter.Amount < 0 || filter.Amount > 5 ||
			!finite(filter.NoiseReduction) || filter.NoiseReduction < 0 || filter.NoiseReduction > 1 {
			return 0, filterError(ErrInvalid, "smartSharpen", "amount or noise reduction is outside its bounded range")
		}
		if filter.BlurModel != "lens-blur" || filter.UseLegacy {
			return 0, filterError(ErrUnsupported, "smartSharpen.model", "only the modern lens-blur approximation is available")
		}
		if filter.Radius > 32 {
			return 0, filterError(ErrLimit, "smartSharpen.radius", "radius exceeds the bounded lens support limit")
		}
		if filter.Adjustment != nil {
			return 0, filterError(ErrInvalid, "smartSharpen", "color controls do not belong to this algorithm")
		}
		if filter.Radius == 0 || filter.Amount == 0 {
			return 0, nil
		}
		if !configuration.ApproximateSmartSharpen {
			return 0, filterError(ErrUnsupported, "smartSharpen", "an explicit Smart Sharpen approximation policy is required")
		}
		return int(math.Ceil(filter.Radius + .5)), nil
	case BrightnessContrast, HueSaturation:
		return 0, colorFilterValidate(filter, configuration)
	default:
		return 0, filterError(ErrUnsupported, "filter.kind", "algorithm is unavailable")
	}
}

func gaussianSupportRadius(radius float64) int {
	if radius == 0 {
		return 0
	}
	// Preserve the established scene halo, which bounds both calibrated FIR and finite box kernels.
	return int(math.Ceil(radius*4*1.1)) + 4
}

func identityFilter(filter Filter) bool {
	switch filter.Kind {
	case GaussianBlur:
		return filter.Radius == 0
	case SurfaceBlur:
		return filter.Radius == 0 || filter.Threshold == 0
	case SmartSharpen:
		return filter.Radius == 0 || filter.Amount == 0
	default:
		return false
	}
}
