// Package psdprecision composites finite source colors without eight-bit quantization or HDR clipping.
package psdprecision

import (
	"errors"
	"image"
)

// Encoding states the color domain used by ordinary source-over blending.
type Encoding uint8

const (
	// EncodedRGB blends normalized encoded colors, as used by ordinary RGB16 PSD sources.
	EncodedRGB Encoding = iota
	// LinearRGB blends linear-light values, as used by RGB32 PSD sources.
	LinearRGB
)

// NonFinitePolicy explicitly handles NaN and infinity in display-independent rendering inputs.
type NonFinitePolicy uint8

const (
	// NonFiniteReject returns ErrNonFinite without changing source sample bits.
	NonFiniteReject NonFinitePolicy = iota
	// NonFiniteZero substitutes zero only in the rendered result.
	NonFiniteZero
	// NonFiniteClamp substitutes positive infinity with one and negative infinity or NaN with zero.
	NonFiniteClamp
)

// FloatImage owns contiguous straight RGBA float64 samples in its declared color domain.
// Finite color components may be negative or greater than one; alpha must remain in [0,1].
// SourceDepth and ICCProfile preserve source provenance; rendering does not apply an ICC conversion.
type FloatImage struct {
	Width, Height int
	Encoding      Encoding
	SourceDepth   uint16
	ICCProfile    []byte
	Pix           []float64
}

// Mask contains normalized coverage on its document-coordinate rectangle.
// Default applies outside Bounds; Invert precedes density, and a nil Density means full strength.
type Mask struct {
	Bounds   image.Rectangle
	Values   []float64
	Default  float64
	Density  *float64
	Invert   bool
	Disabled bool
}

// BlendSlider stores Photoshop's split black and white thresholds on the 0..255 control scale.
type BlendSlider struct {
	BlackLow, BlackHigh uint8
	WhiteLow, WhiteHigh uint8
}

// BlendRange contains source and backdrop thresholds for gray, then red, green and blue.
type BlendRange struct {
	Source, Destination BlendSlider
}

// Layer is a raster or group in bottom-to-top painting order.
// A nonnil Children slice identifies a group. Nil opacity means one; zero opacity is invisible.
// Normal groups are isolated. Pass-through groups evaluate children against their backdrop.
// Group opacity and masks interpolate premultiplied changes into the original backdrop.
// Raster clipping chains keep the base transparency; nil BlendClipped selects Photoshop's grouped setting.
// Blend If evaluates the continuous source domain using high-depth split thresholds, without byte quantization.
type Layer struct {
	Name         string
	Bounds       image.Rectangle
	Pixels       *FloatImage
	Children     []*Layer
	Mask         *Mask
	Opacity      *float64
	BlendMode    string
	Hidden       bool
	Clipping     bool
	BlendClipped *bool
	BlendRanges  []BlendRange
	Unsupported  []string
}

// Scene holds one color domain and an immutable bottom-to-top layer tree.
type Scene struct {
	Width, Height int
	Encoding      Encoding
	Layers        []*Layer
}

// Options bounds new rendering buffers, traversal and non-finite substitutions.
// Defaults are 256 MiB, 20 million canvas pixels, ten thousand layers and sixty-four nesting levels.
// Borrowed scene samples are not included in MaxWorkingBytes; source adapters budget their owned input separately.
type Options struct {
	MaxWorkingBytes int64
	MaxPixels       int64
	MaxLayers       int
	MaxDepth        int
	NonFinite       NonFinitePolicy
}

var (
	// ErrInvalid identifies inconsistent geometry, alpha, color encoding or scene structure.
	ErrInvalid = errors.New("invalid high-precision PSD scene")
	// ErrUnsupported identifies active blending or semantic features outside the precise compositor.
	ErrUnsupported = errors.New("unsupported high-precision PSD feature")
	// ErrLimit identifies a rendering allocation or traversal budget violation.
	ErrLimit = errors.New("high-precision PSD resource limit exceeded")
	// ErrNonFinite identifies a NaN or infinite sample rejected by the selected policy.
	ErrNonFinite = errors.New("non-finite high-precision PSD sample")
)
