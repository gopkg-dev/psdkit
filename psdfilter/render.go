package psdfilter

import (
	"context"
	"image"
	"math"
)

// WorkingEstimate validates a source layout and estimates Apply's peak allocation before any allocation.
// Image rectangles, nonzero origins and caller-owned row strides are preserved in the owned result.
func WorkingEstimate(source *image.NRGBA, stack Stack, options ...Options) (Estimate, error) {
	var estimate Estimate
	configuration, err := renderOptions(options)
	if err != nil {
		return estimate, err
	}
	if source == nil || source.Rect.Empty() || source.Rect.Dx() < 0 || source.Rect.Dy() < 0 {
		return estimate, filterError(ErrInvalid, "pixels", "provide a nonempty positive image rectangle")
	}
	width, height := int64(source.Rect.Dx()), int64(source.Rect.Dy())
	intLimit := int64(^uint(0) >> 1)
	if width > intLimit/4 || height > intLimit/4/width ||
		width > configuration.MaxPixels/height || source.Stride < int(width)*4 {
		return estimate, filterError(ErrLimit, "pixels", "pixel shape exceeds source layout or pixel limits")
	}
	if int64(source.Stride) > intLimit/max(1, height-1) ||
		int64(source.Stride)*(height-1)+width*4 > int64(len(source.Pix)) {
		return estimate, filterError(ErrInvalid, "pixels", "row stride or pixel buffer is incomplete")
	}
	if len(stack.Filters) > configuration.MaxFilters {
		return estimate, filterError(ErrLimit, "filters", "filter count exceeds the operation limit")
	}
	area := width * height
	estimate.RetainedBytes = area * 4
	estimate.WorkingBytes = estimate.RetainedBytes
	var largestKernel, sampleOperations int64
	gaussianActive, colorActive := false, false
	active := false
	for _, filter := range stack.Filters {
		if err := validateCommon(filter); err != nil {
			return Estimate{}, err
		}
		if !stack.Enabled || !filter.Enabled || filter.Opacity == 0 {
			continue
		}
		radius, err := validateFilter(filter, configuration)
		if err != nil {
			return Estimate{}, err
		}
		if identityFilter(filter) {
			continue
		}
		active = true
		var samples int64
		switch filter.Kind {
		case GaussianBlur:
			gaussianActive = true
			samples = int64(radius*2+1) * 2
			largestKernel = max(largestKernel, int64(radius*2+1)*8)
		case SurfaceBlur:
			samples = int64(radius*2+1) * int64(radius*2+1)
		case SmartSharpen:
			samples = int64(radius*2+1) * int64(radius*2+1)
			largestKernel = max(largestKernel, samples*24)
		case BrightnessContrast, HueSaturation:
			colorActive = true
		}
		if samples > 0 {
			if area > (configuration.MaxSampleOperations-sampleOperations)/samples {
				return Estimate{}, filterError(ErrLimit, "samples", "spatial neighborhood work exceeds its operation limit")
			}
			sampleOperations += area * samples
		}
	}
	if active {
		// Spatial point operators need only two RGBA8 buffers; Gaussian additionally retains float64 scratch.
		bytesPerPixel := int64(8)
		if gaussianActive {
			bytesPerPixel += 32
		}
		if colorActive {
			largestKernel += 2 * colorFilterWorkingBytes
		}
		if area > (math.MaxInt64-largestKernel)/bytesPerPixel {
			return Estimate{}, filterError(ErrLimit, "working", "filter allocation size overflows")
		}
		estimate.WorkingBytes = area*bytesPerPixel + largestKernel
	}
	if estimate.WorkingBytes > configuration.MaxWorkingBytes || estimate.WorkingBytes > intLimit {
		return Estimate{}, filterError(ErrLimit, "working", "output and filter scratch exceed the working limit")
	}
	return estimate, nil
}

// Apply returns an owned filtered image, preserves input pixels and uses first-applied-to-last-applied order.
// Gaussian samples premultiplied encoded-RGB and alpha; Surface Blur and Smart Sharpen retain center alpha.
// Spatial sampling clamps image edges and excludes hidden transparent colors; the source rectangle is retained.
// Each approximation requires its explicit policy; Photoshop pixel parity is not claimed.
// Validated Normal-blend spatial identity controls and plain master Lightness need no approximation policy.
func Apply(ctx context.Context, source *image.NRGBA, stack Stack, options ...Options) (*image.NRGBA, error) {
	return apply(ctx, source, stack, nil, options)
}

func apply(
	ctx context.Context, source *image.NRGBA, stack Stack, opacityFacts []bool, options []Options,
) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	configuration, err := renderOptions(options)
	if err != nil {
		return nil, err
	}
	_, err = WorkingEstimate(source, stack, configuration)
	if err != nil {
		return nil, err
	}
	if opacityFacts != nil && len(opacityFacts) != len(stack.Filters) {
		return nil, filterError(ErrInvalid, "region.opacity", "provide one full-image input fact for each filter")
	}
	current := image.NewNRGBA(source.Rect)
	for row := 0; row < source.Rect.Dy(); row++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		copy(current.Pix[row*current.Stride:row*current.Stride+source.Rect.Dx()*4],
			source.Pix[row*source.Stride:row*source.Stride+source.Rect.Dx()*4])
	}
	if !stack.Enabled {
		return current, nil
	}
	var target *image.NRGBA
	var horizontal []float64
	for index, filter := range stack.Filters {
		if !filter.Enabled || filter.Opacity == 0 || identityFilter(filter) {
			continue
		}
		if target == nil {
			target = image.NewNRGBA(source.Rect)
		}
		var fullOpaque *bool
		if opacityFacts != nil {
			fullOpaque = &opacityFacts[index]
		}
		var operationErr error
		switch filter.Kind {
		case GaussianBlur:
			if horizontal == nil {
				horizontal = make([]float64, source.Rect.Dx()*source.Rect.Dy()*4)
			}
			operationErr = gaussian(ctx, current, target, horizontal, filter, fullOpaque)
		case SurfaceBlur:
			operationErr = surfaceBlur(ctx, current, target, filter)
		case SmartSharpen:
			operationErr = smartSharpen(ctx, current, target, filter)
		case BrightnessContrast, HueSaturation:
			operationErr = colorFilter(ctx, current, target, filter, configuration)
		}
		if operationErr != nil {
			return nil, operationErr
		}
		current, target = target, current
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return current, nil
}

func renderOptions(options []Options) (Options, error) {
	configuration := Options{MaxPixels: 20_000_000, MaxWorkingBytes: 256 << 20, MaxFilters: 4096,
		MaxSampleOperations: 1 << 32}
	if len(options) > 1 {
		return configuration, filterError(ErrInvalid, "options", "provide at most one option value")
	}
	if len(options) == 0 {
		return configuration, nil
	}
	provided := options[0]
	if provided.MaxPixels < 0 || provided.MaxWorkingBytes < 0 || provided.MaxFilters < 0 ||
		provided.MaxSampleOperations < 0 {
		return configuration, filterError(ErrInvalid, "options", "limits must be nonnegative")
	}
	if provided.MaxPixels != 0 {
		configuration.MaxPixels = provided.MaxPixels
	}
	if provided.MaxWorkingBytes != 0 {
		configuration.MaxWorkingBytes = provided.MaxWorkingBytes
	}
	if provided.MaxFilters != 0 {
		configuration.MaxFilters = provided.MaxFilters
	}
	configuration.ApproximateGaussian = provided.ApproximateGaussian
	configuration.ApproximateSurfaceBlur = provided.ApproximateSurfaceBlur
	configuration.ApproximateSmartSharpen = provided.ApproximateSmartSharpen
	configuration.ApproximateBrightnessContrast = provided.ApproximateBrightnessContrast
	configuration.ApproximateHueSaturation = provided.ApproximateHueSaturation
	if provided.MaxSampleOperations != 0 {
		configuration.MaxSampleOperations = provided.MaxSampleOperations
	}
	return configuration, nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
