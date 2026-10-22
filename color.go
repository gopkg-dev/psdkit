package psdkit

import (
	"context"
	"fmt"
	"image"

	"github.com/gopkg-dev/psdkit/psdcolor"
)

// ColorOptions controls explicit conversion from original source samples to sRGB8 display pixels.
type ColorOptions = psdcolor.Options

// RenderingIntent selects the source ICC forward transform.
type RenderingIntent = psdcolor.RenderingIntent

// ProfileTransformOptions bounds explicit RGB matrix/TRC conversion between two ICC profiles.
type ProfileTransformOptions = psdcolor.ProfileTransformOptions

// TransformProfile returns owned RGB8 pixels in the destination profile while preserving alpha.
func TransformProfile(ctx context.Context, input *image.NRGBA, sourceICC, destinationICC []byte,
	options ...ProfileTransformOptions,
) (*image.NRGBA, error) {
	return psdcolor.TransformProfile(ctx, input, sourceICC, destinationICC, options...)
}

// ProfileTransformWorkingBytes estimates additional peak storage excluding borrowed inputs.
func ProfileTransformWorkingBytes(ctx context.Context, input *image.NRGBA, sourceICC, destinationICC []byte,
	options ...ProfileTransformOptions,
) (int64, error) {
	return psdcolor.ProfileTransformWorkingBytes(ctx, input, sourceICC, destinationICC, options...)
}

const (
	// IntentRelativeColorimetric selects relative colorimetry without black-point compensation.
	IntentRelativeColorimetric = psdcolor.IntentRelativeColorimetric
	// IntentPerceptual selects the profile's perceptual transform.
	IntentPerceptual = psdcolor.IntentPerceptual
	// IntentSaturation selects the profile's saturation transform.
	IntentSaturation = psdcolor.IntentSaturation
)

// DefaultColorOptions returns explicit default display policies and budgets.
func DefaultColorOptions() ColorOptions { return psdcolor.DefaultOptions() }

// DisplayOptions selects source color context for a saved-image display conversion.
// A non-nil Color.ICCProfile overrides the source profile; an empty non-nil slice explicitly disables it.
type DisplayOptions struct {
	Color               ColorOptions
	IgnoreSourceProfile bool
	IgnoreTransparency  bool
}

var (
	// ErrColorInvalid identifies malformed color samples, profiles or conversion options.
	ErrColorInvalid = psdcolor.ErrInvalid
	// ErrColorUnsupported identifies an unsupported color space or ICC transform.
	ErrColorUnsupported = psdcolor.ErrUnsupported
	// ErrColorLimit identifies a display conversion allocation budget violation.
	ErrColorLimit = psdcolor.ErrLimit
	// ErrColorNonFinite identifies rejected NaN or infinite floating samples.
	ErrColorNonFinite = psdcolor.ErrNonFinite
	// ErrColorOutOfRange identifies rejected HDR or negative floating samples.
	ErrColorOutOfRange = psdcolor.ErrOutOfRange
)

// ConvertSamples returns an owned sRGB8 display image without changing original sample bytes.
func ConvertSamples(ctx context.Context, pixels *CompositePixels, options ...ColorOptions) (*image.NRGBA, error) {
	return psdcolor.Convert(ctx, pixels, options...)
}

// DecodeDisplayComposite converts stored composite samples using their palette, ICC and transparency markers.
// It displays the saved preview, including any placeholder or quantization already present in that preview.
// This operation does not re-render high-precision document layers or change their source precision.
func DecodeDisplayComposite(
	ctx context.Context, source *SourceDocument, options ...DisplayOptions,
) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if source == nil || len(options) > 1 {
		return nil, fmt.Errorf("%w: source and at most one display option are required", psdcolor.ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := source.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	option := DisplayOptions{}
	if len(options) == 1 {
		option = options[0]
	}
	configuration := option.Color
	if configuration.MaxPixels < 0 || configuration.MaxOutputBytes < 0 || configuration.MaxProfileBytes < 0 {
		return nil, fmt.Errorf("%w: display budgets must not be negative", psdcolor.ErrInvalid)
	}
	limits := source.Limits()
	area := int64(source.Header.Width) * int64(source.Header.Height)
	maxPixels, maxOutput, maxProfile := configuration.MaxPixels, configuration.MaxOutputBytes, configuration.MaxProfileBytes
	if maxPixels == 0 {
		maxPixels = limits.MaxDecodePixels
		configuration.MaxPixels = maxPixels
	}
	if maxOutput == 0 {
		maxOutput = limits.MaxDecodeBytes
		configuration.MaxOutputBytes = maxOutput
	}
	if maxProfile == 0 {
		maxProfile = 4 << 20
		configuration.MaxProfileBytes = maxProfile
	}
	rowBytes := (int64(source.Header.Width)*int64(source.Header.Depth) + 7) / 8
	sampleBytes := rowBytes * int64(source.Header.Height) * int64(source.Header.Channels)
	if maxPixels < 0 || maxOutput < 0 || maxProfile < 0 || area > maxPixels || area > maxOutput/4 ||
		area > limits.MaxDecodeBytes/4 || sampleBytes > limits.MaxDecodeBytes-area*4 {
		return nil, fmt.Errorf("%w: source samples and display output exceed budget", psdcolor.ErrLimit)
	}
	configuration, err := displayMetadata(ctx, source, option, configuration)
	if err != nil {
		return nil, err
	}
	if err := validateDisplayMetadata(ctx, source, configuration); err != nil {
		return nil, err
	}
	samples, err := source.DecodeComposite(ctx)
	if err != nil {
		return nil, err
	}
	return psdcolor.Convert(ctx, samples, configuration)
}
