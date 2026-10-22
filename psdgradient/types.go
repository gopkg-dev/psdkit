// Package psdgradient renders bounded classic RGB gradient fills in document coordinates.
// Stops use the source profile's encoded RGB space; this package performs no ICC display conversion.
package psdgradient

import (
	"errors"
	"fmt"
	"image"
)

var (
	// ErrInvalid identifies malformed coordinates, stops or fill parameters.
	ErrInvalid = errors.New("invalid PSD gradient")
	// ErrUnsupported identifies gradient semantics outside the native-verified subset.
	ErrUnsupported = errors.New("unsupported PSD gradient")
	// ErrLimit identifies an exceeded pixel, stop or working allocation budget.
	ErrLimit = errors.New("PSD gradient resource limit exceeded")
)

// Kind identifies a gradient's spatial shape.
type Kind string

const (
	// Linear projects document coordinates onto one gradient axis.
	Linear Kind = "linear"
	// Radial maps distance from the gradient center onto its color ramp.
	Radial Kind = "radial"
)

// ColorStop retains a source encoded RGB color and its ramp position.
// Location ranges from zero to 4096; Midpoint is the percentage within the interval from the previous stop.
type ColorStop struct {
	Location uint32
	Midpoint uint16
	RGB      [3]float64
}

// OpacityStop retains a straight opacity and its separate midpoint-controlled ramp position.
// Opacity ranges from zero to one and is independent of the color ramp.
type OpacityStop struct {
	Location uint32
	Midpoint uint16
	Opacity  float64
}

// Offset is a gradient center displacement expressed in percentages of its alignment bounds.
type Offset struct{ X, Y float64 }

// Fill contains typed live gradient controls, excluding layer opacity and mask coverage.
// Scale is a percentage; Angle is counterclockwise in degrees and zero points toward increasing X.
// Smoothness retains the Photoshop zero-through-4096 interpolation setting.
// Unsupported retains semantically unrecognized descriptor fields; Render refuses those fills.
type Fill struct {
	Name, Interpolation, Form string
	Kind                      Kind
	ColorStops                []ColorStop
	OpacityStops              []OpacityStop
	Angle, Scale, Smoothness  float64
	Reverse, Align, Dither    bool
	Offset                    Offset
	Unsupported               []string
}

// Options bounds rendering resources and optionally supplies layer alignment bounds.
// AlignBounds is used only when Fill.Align is true; otherwise the full document bounds are used.
// Crop changes the owned output rectangle without changing document-based gradient geometry.
// Zero budgets select 20 million pixels, 256 MiB of working allocation and 4096 stops.
type Options struct {
	AlignBounds, Crop          *image.Rectangle
	MaxPixels, MaxWorkingBytes int64
	MaxStops                   int
}

// Error describes one gradient field without printing source descriptor content.
type Error struct {
	Kind         error
	Path, Detail string
}

// Error implements error.
func (e *Error) Error() string { return fmt.Sprintf("%v: %s: %s", e.Kind, e.Path, e.Detail) }

// Unwrap exposes the error category to errors.Is.
func (e *Error) Unwrap() error { return e.Kind }

func gradientError(kind error, path, detail string) *Error {
	return &Error{Kind: kind, Path: path, Detail: detail}
}
