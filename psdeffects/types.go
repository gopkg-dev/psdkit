// Package psdeffects interprets modern RGB layer-effect descriptors and rasterizes bounded effect coverage.
package psdeffects

import (
	"errors"
	"image"
)

// Span identifies unchanged descriptor bytes in the original source.
type Span struct{ Offset, Length int64 }

// Descriptor retains ordered duplicate entries and the original descriptor span.
type Descriptor struct {
	Name, ClassID string
	Version       uint32
	Entries       []Entry
	Span          Span
}

// Entry preserves the descriptor key, OSType and original encoded spans.
type Entry struct {
	Key, Type                     string
	Value                         any
	Span, KeyData, TypeData, Data Span
}

// Value retains a typed list item and its original spans.
type Value struct {
	Type                 string
	Value                any
	Span, TypeData, Data Span
}

// Enum preserves separate enumeration class and value identifiers.
type Enum struct{ TypeID, ValueID string }

// Unit preserves a unit scalar without changing its original IEEE bits.
type Unit struct {
	Unit   string
	Number float64
	Bits   uint64
	Width  uint8
}

// Raw retains unrecognized typed data without reading or normalizing its payload.
type Raw struct{ Span, Data Span }

// Kind identifies a supported effect family.
type Kind string

const (
	// DropShadow generates displaced coverage behind a layer.
	DropShadow Kind = "drop-shadow"
	// OuterGlow generates expanded coverage behind a layer.
	OuterGlow Kind = "outer-glow"
	// InnerShadow generates displaced shadow coverage inside a layer.
	InnerShadow Kind = "inner-shadow"
	// InnerGlow generates glow coverage from the edge or center inside a layer.
	InnerGlow Kind = "inner-glow"
	// Stroke generates an outline in front of a layer.
	Stroke Kind = "stroke"
	// ColorOverlay blends a constant RGB color with the unfilled interior of a layer.
	ColorOverlay Kind = "color-overlay"
)

// Position selects a stroke's placement relative to the source boundary.
type Position string

const (
	// Outside places a stroke outside the source coverage.
	Outside Position = "outside"
	// Inside places a stroke inside the source coverage.
	Inside Position = "inside"
	// Center splits stroke width across the source boundary.
	Center Position = "center"
)

// GlowSource selects the edge or center of inner-glow coverage.
type GlowSource string

const (
	// Edge places inner-glow coverage near the source boundary.
	Edge GlowSource = "edge"
	// CenterSource places inner-glow coverage away from the source boundary.
	CenterSource GlowSource = "center"
)

// Effect stores RGB parameters separately from layer opacity and Fill opacity.
// Opacity, Spread, Noise and Jitter are fractions; Size and Distance are pixels; Angle is degrees.
// Color components are encoded RGB fractions. Descriptor and Unsupported retain unimplemented semantics.
type Effect struct {
	Kind                          Kind
	Enabled, Present, Shown       bool
	Color                         [3]float64
	BlendMode                     string
	Opacity, Size, Spread         float64
	Distance, Angle               float64
	UseGlobalLight, LayerConceals bool
	AntiAlias                     bool
	Noise, Jitter, Range          float64
	Technique                     string
	Position                      Position
	GlowSource                    GlowSource
	Descriptor                    *Descriptor
	Unsupported                   []string
}

// Set owns decoded effects and the entire ordered descriptor, including unknown source spans.
// Scale is a fraction, while GlobalAngle is degrees. EffectsVisible combines the document resource and master switch.
type Set struct {
	ObjectVersion, DescriptorVersion uint32
	Enabled, EffectsVisible          bool
	Scale, GlobalAngle               float64
	ModifyingCount                   *int
	Items                            []Effect
	OrderedEntries                   []Entry
	Descriptor                       *Descriptor
	Span, Tail                       Span
	Unsupported                      []string
}

// DecodeOptions bounds owned descriptor data and supplies document effect settings.
type DecodeOptions struct {
	MaxBytes       int64
	MaxEntries     int
	MaxDepth       int
	GlobalAngle    *float64
	EffectsVisible *bool
}

// Options bounds output coverage, intermediate buffers and morphology/convolution work.
// Defaults are 256 MiB, 100 million sample visits and 128 enabled effects.
type Options struct {
	MaxWorkingBytes int64
	MaxSampleWork   int64
	MaxEffects      int
	// ApproximateDropShadow selects the explicitly defined generic spread/tent/subpixel model.
	// Spread is max(alpha*clamp(radius+1-distance)), with radius=size*scale*spread.
	// Blur uses normalized weights max(size*scale*(1-spread)-abs(integerOffset), 0).
	// Displacement is bilinear; zero extension and one final round-to-byte apply to coverage.
	// The zero value preserves existing primitive math, including finite native brush calibrations.
	// It does not claim Photoshop pixel parity and does not enable noise or nonlinear contours.
	ApproximateDropShadow bool
	// DeferDropShadowConceal returns raw shadow coverage and its conceal flag for compositing.
	// It requires ApproximateDropShadow; the compositor must apply the source-shape partition itself.
	DeferDropShadowConceal bool
}

// Plane owns effect coverage and carries its unbaked RGB, opacity and blend parameters.
type Plane struct {
	Kind      Kind
	Coverage  *image.Alpha
	Color     [3]float64
	Opacity   float64
	BlendMode string
	// LayerConceals preserves a DropShadow's source-shape knockout setting.
	LayerConceals bool
}

// Result keeps background, interior and foreground planes in rendering order and their expanded bounds.
type Result struct {
	Background, Interior, Foreground []Plane
	Bounds                           image.Rectangle
}

var (
	// ErrInvalid identifies malformed descriptor values or raster arguments.
	ErrInvalid = errors.New("invalid PSD layer effect")
	// ErrUnsupported identifies active effect semantics not implemented by the primitive rasterizer.
	ErrUnsupported = errors.New("unsupported PSD layer effect")
	// ErrLimit identifies an effect operation that exceeds an explicit resource budget.
	ErrLimit = errors.New("PSD layer effect budget exceeded")
)

// Error associates a classified effect error with the offending descriptor or raster field.
type Error struct {
	Kind         error
	Path, Detail string
}

func (e *Error) Error() string { return e.Kind.Error() + ": " + e.Path + ": " + e.Detail }

// Unwrap exposes the effect error category to errors.Is.
func (e *Error) Unwrap() error { return e.Kind }

func effectError(kind error, path, detail string) error {
	return &Error{Kind: kind, Path: path, Detail: detail}
}
