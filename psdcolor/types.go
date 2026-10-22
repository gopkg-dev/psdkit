package psdcolor

import "errors"

var (
	// ErrInvalid reports inconsistent samples, options, palette or malformed ICC data.
	ErrInvalid = errors.New("psdcolor: invalid color data")
	// ErrUnsupported reports an unsupported color space or ICC transform.
	ErrUnsupported = errors.New("psdcolor: unsupported color conversion")
	// ErrLimit reports a conversion allocation or profile budget violation.
	ErrLimit = errors.New("psdcolor: color conversion limit exceeded")
	// ErrNonFinite reports a NaN or infinity when no replacement policy was selected.
	ErrNonFinite = errors.New("psdcolor: non-finite sample")
	// ErrOutOfRange reports an HDR sample outside [0, 1] when tone mapping was not selected.
	ErrOutOfRange = errors.New("psdcolor: sample is outside display range")
)

// AlphaMode describes the source channel encoding, independently of color management.
type AlphaMode uint8

const (
	// AlphaNone produces opaque pixels and does not interpret extra channels as transparency.
	AlphaNone AlphaMode = iota
	// AlphaStraight reads an additional channel as straight coverage.
	AlphaStraight
	// AlphaWhiteMatte removes the source-space white matte used by saved PSD composite channels.
	AlphaWhiteMatte
)

// FloatEncoding identifies the color encoding of 32-bit floating point samples.
type FloatEncoding uint8

const (
	// FloatLinearRGB treats RGB and grayscale floats as linear-light source color values.
	// RGB ICC primaries still apply, while the profile's encoded-input TRCs are bypassed.
	FloatLinearRGB FloatEncoding = iota
	// FloatEncodedRGB applies the same source transfer curves as integer samples.
	FloatEncodedRGB
)

// NonFinitePolicy determines how NaN and infinity become display-only values.
type NonFinitePolicy uint8

const (
	// NonFiniteReject returns ErrNonFinite without changing the original sample bits.
	NonFiniteReject NonFinitePolicy = iota
	// NonFiniteZero displays every non-finite sample as zero.
	NonFiniteZero
	// NonFiniteClamp displays positive infinity as one and negative infinity or NaN as zero.
	NonFiniteClamp
)

// ToneMapPolicy selects a display-range mapping for finite floating point color samples.
type ToneMapPolicy uint8

const (
	// ToneMapReject returns ErrOutOfRange for finite color samples outside [0, 1].
	ToneMapReject ToneMapPolicy = iota
	// ToneMapClamp explicitly clips HDR colors to [0, 1]; it is not an exposure or highlight operator.
	ToneMapClamp
)

// RenderingIntent selects the source ICC forward transform.
type RenderingIntent uint8

const (
	// IntentRelativeColorimetric selects A2B1, with the ICC-defined A2B0 fallback when absent.
	IntentRelativeColorimetric RenderingIntent = iota
	// IntentPerceptual selects A2B0.
	IntentPerceptual
	// IntentSaturation selects A2B2, with A2B0 fallback when absent.
	IntentSaturation
	// IntentAbsoluteColorimetric is reserved and currently returns ErrUnsupported.
	IntentAbsoluteColorimetric
)

// Options selects explicit display conversion policies and allocation limits.
// A zero Options value assumes unmanaged encoded sRGB integers, linear sRGB floats and opaque coverage.
type Options struct {
	// ICCProfile is the complete embedded profile. An empty value selects no ICC transform.
	// Convert never ignores a supplied but unsupported profile.
	ICCProfile []byte
	// Intent defaults to relative colorimetric; absolute colorimetry is not implemented.
	Intent RenderingIntent
	// Palette contains indexed colors as 256 reds, 256 greens and 256 blues.
	Palette []byte
	// TransparencyIndex makes this indexed palette entry transparent when present.
	TransparencyIndex *uint8
	// Alpha selects none, straight coverage, or white-matted saved composite channels.
	Alpha AlphaMode
	// AlphaChannel is the plane index; zero selects the first plane after the color components.
	// Alpha planes use the same original bit depth as the color planes and are never gamma-corrected.
	AlphaChannel int
	// FloatEncoding describes 32-bit color samples independently of coverage.
	FloatEncoding FloatEncoding
	// NonFinite and ToneMap are applied to display values without changing source float bits.
	NonFinite NonFinitePolicy
	ToneMap   ToneMapPolicy
	// AllowUnmanagedCMYK permits the approximate (1-C)*(1-K) fallback only when no ICC was supplied.
	AllowUnmanagedCMYK bool
	// MaxPixels defaults to 64 million pixels when zero.
	MaxPixels int64
	// MaxOutputBytes defaults to 256 MiB and bounds the returned NRGBA buffer.
	MaxOutputBytes int64
	// MaxProfileBytes defaults to 4 MiB and bounds ICC parsing and one-dimensional curve storage.
	MaxProfileBytes int64
}
