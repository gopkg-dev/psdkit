// Package psdpattern repeats owned RGB8 pattern tiles in document coordinates without converting ICC profiles.
package psdpattern

import (
	"errors"
	"fmt"
	"image"
)

var (
	// ErrInvalid identifies malformed tile geometry or fill parameters.
	ErrInvalid = errors.New("invalid PSD pattern")
	// ErrUnsupported identifies unresolved pattern sources or unverified sampling semantics.
	ErrUnsupported = errors.New("unsupported PSD pattern")
	// ErrLimit identifies an exceeded output or working allocation budget.
	ErrLimit = errors.New("PSD pattern resource limit exceeded")
)

// Phase retains a pattern's displacement in document pixels.
type Phase struct{ X, Y float64 }

// Fill retains an opaque local pattern reference and its spatial controls.
// ID is kept for local matching and must not be used as an error-message payload.
// Scale is a percentage and Angle is counterclockwise degrees; Link controls layer-relative phase tracking.
type Fill struct {
	ID, Name     string
	Scale, Angle float64
	Link         bool
	Phase        Phase
	Unsupported  []string
}

// Options bounds rendering and optionally crops owned output without moving the tile origin.
// Zero budgets select 20 million pixels and 256 MiB of working allocation.
type Options struct {
	Crop                       *image.Rectangle
	MaxPixels, MaxWorkingBytes int64
}

// Error identifies a field without exposing opaque pattern references.
type Error struct {
	Kind         error
	Path, Detail string
}

// Error implements error.
func (e *Error) Error() string { return fmt.Sprintf("%v: %s: %s", e.Kind, e.Path, e.Detail) }

// Unwrap exposes the error category to errors.Is.
func (e *Error) Unwrap() error { return e.Kind }

func patternError(kind error, path, detail string) *Error {
	return &Error{Kind: kind, Path: path, Detail: detail}
}
