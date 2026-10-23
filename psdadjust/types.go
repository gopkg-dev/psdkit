// Package psdadjust applies bounded RGB8 tonal adjustments without changing source pixels or alpha.
// RGB samples are encoded color values; exposure uses the native-verified RGB 2.2 power transfer.
package psdadjust

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalid identifies invalid image geometry or adjustment parameters.
	ErrInvalid = errors.New("invalid RGB8 adjustment")
	// ErrUnsupported identifies adjustment semantics outside the implemented RGB8 subset.
	ErrUnsupported = errors.New("unsupported RGB8 adjustment")
	// ErrLimit identifies a configured pixel, metadata or working-memory budget violation.
	ErrLimit = errors.New("RGB8 adjustment resource limit exceeded")
)

// Kind names an adjustment family recognized by the codec or RGB8 transform.
type Kind string

const (
	// Invert complements the three encoded color channels.
	Invert Kind = "invert"
	// Threshold replaces RGB with black or white according to encoded luminance.
	Threshold Kind = "threshold"
	// Posterize quantizes each encoded color channel to a fixed number of levels.
	Posterize Kind = "posterize"
	// Levels applies individual channel levels before composite levels.
	Levels Kind = "levels"
	// Curves applies individual channel curves before the composite curve.
	Curves Kind = "curves"
	// Exposure adjusts linearized RGB exposure, offset and gamma, then returns encoded RGB.
	Exposure Kind = "exposure"
	// BrightnessContrast retains legacy parameters pending native algorithm verification.
	BrightnessContrast Kind = "brightness-contrast"
	// ColorBalance retains the three tonal-range color offsets without assuming an unverified transfer curve.
	ColorBalance Kind = "color-balance"
	// ChannelMixer applies a fixed encoded-RGB channel matrix and constant.
	ChannelMixer Kind = "channel-mixer"
	// HueSaturation adjusts encoded RGB hue, saturation and lightness, optionally colorizing.
	HueSaturation Kind = "hue-saturation"
	// PhotoFilter retains a version-two or version-three native filter color, density and luminosity policy.
	PhotoFilter Kind = "photo-filter"
)

// Span identifies original bytes without retaining a source reader.
type Span struct{ Offset, Length int64 }

// Error identifies a field and original byte offset without exposing source payloads.
type Error struct {
	Kind         error
	Path, Detail string
	Offset       int64
}

// Error implements error.
func (e *Error) Error() string {
	return fmt.Sprintf("%v: %s at offset %d: %s", e.Kind, e.Path, e.Offset, e.Detail)
}

// Unwrap exposes the error category to errors.Is.
func (e *Error) Unwrap() error { return e.Kind }

// LevelChannel retains one channel's input, output and gamma controls.
// Channel zero is composite RGB; one, two and three refer to red, green and blue.
type LevelChannel struct {
	Channel                                          uint16
	InputBlack, InputWhite, OutputBlack, OutputWhite uint16
	Gamma                                            float64
}

// CurvePoint stores a curve control point with 0 to 255 input and output coordinates.
type CurvePoint struct{ Input, Output uint16 }

// CurveChannel contains either ordered control points or an explicit 256-byte lookup table.
// Curves use natural cubic interpolation and endpoint clamping; input coordinates must strictly increase.
// Control values must be monotone; explicit LUT values may use arbitrary mappings.
type CurveChannel struct {
	Channel uint16
	Points  []CurvePoint
	LUT     []uint8
}

// ExposureParams applies 2^Exposure, Offset and inverse Gamma after a 2.2 power transfer.
// Linear results are clamped to zero through one before gamma and inverse 2.2 encoding.
// This matches the native Photoshop RGB8 sRGB control fixture, rather than using the IEC piecewise transfer.
// This RGB8 entry point does not retain HDR samples or perform ICC profile conversion.
type ExposureParams struct{ Exposure, Offset, Gamma float64 }

// BrightnessContrastParams retains legacy brightness/contrast fields without approximating modern CgEd behavior.
type BrightnessContrastParams struct {
	Brightness, Contrast int16
	Mean                 uint16
	LabOnly, Legacy      bool
}

// ColorBalanceParams retains cyan/red, magenta/green and yellow/blue offsets for each tonal range.
// Each signed offset is between -100 and 100. PreserveLuminosity retains the source's luminosity policy.
type ColorBalanceParams struct {
	Shadows, Midtones, Highlights [3]int16
	PreserveLuminosity            bool
}

// MixerChannel retains four source-channel coefficients and a constant, expressed in signed percentages.
// Inputs zero through two are RGB; input three is the preserved CMYK black coefficient.
type MixerChannel struct {
	Inputs   [4]int16
	Constant int16
}

// ChannelMixerParams retains the four output rows from a version-one mixr block.
// Monochrome RGB8 uses row zero as Gray and retains the other rows without applying them.
// All rows are validated, and CMYK black inputs remain unsupported.
// Non-monochrome RGB8 uses the first three rows and rejects fourth-output changes.
type ChannelMixerParams struct {
	Monochrome bool
	Rows       [4]MixerChannel
}

// PhotoFilterParams preserves native filter words without assuming an unverified color conversion.
// Version-two colors use ColorSpace and four Components; version-three colors use three XYZWords.
// Signed Lab components remain their original uint16 bit patterns. XYZWords retain their original uint32 words.
// Density is zero through 100. Only zero-density RGB8 rendering is currently verified.
type PhotoFilterParams struct {
	ColorSpace         uint16
	Components         [4]uint16
	XYZWords           [3]uint32
	Density            uint32
	PreserveLuminosity bool
}

// Adjustment contains an owned typed view of an adjustment block or caller-provided parameters.
// Raw retains source encoding, while Span identifies its original location when decoded from a file.
// Curves contains the effective extra-marker channels when present; LegacyCurves retains the original base records.
type Adjustment struct {
	Kind                 Kind
	Key                  string
	Version              uint16
	ExtraVersion         uint16
	CountMap             uint32
	CurveMap             bool
	Threshold            uint16
	Posterize            uint16
	Levels               []LevelChannel
	Curves, LegacyCurves []CurveChannel
	Exposure             *ExposureParams
	BrightnessContrast   *BrightnessContrastParams
	ColorBalance         *ColorBalanceParams
	ChannelMixer         *ChannelMixerParams
	HueSaturation        *HueSaturationParams
	PhotoFilter          *PhotoFilterParams
	Raw                  []byte
	Span                 Span
	Unsupported          []string
}

// Options selects explicit approximations and bounds Apply's owned output and transform working storage.
// Borrowed input pixels are excluded from the storage budget.
// Zero values select 20 million pixels and 256 MiB of working memory.
type Options struct {
	MaxPixels, MaxWorkingBytes int64
	// ApproximateHueSaturation opts into the RGB8 HSL model whose native precision audit is incomplete.
	// Plain master Lightness is supported by default; other non-neutral controls require this policy.
	ApproximateHueSaturation bool
}

// CompileOptions selects explicitly approximate transform algorithms without allocating pixel output.
type CompileOptions struct {
	// ApproximateHueSaturation opts into the unverified RGB8 hue/range/colorize approximation.
	ApproximateHueSaturation bool
}

// DecodeOptions bounds original bytes and owned metadata created by Decode.
// Zero values select a 32 MiB budget and source offset zero.
type DecodeOptions struct {
	SourceOffset int64
	MaxBytes     int64
}

func adjustmentError(kind error, path string, offset int64, detail string) error {
	return &Error{Kind: kind, Path: path, Offset: offset, Detail: detail}
}
