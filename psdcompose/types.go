// Package psdcompose rebuilds RGB8 document pixels from decoded layer trees.
// It consumes saved raster pixels; it does not rasterize text, vectors or smart-object contents.
package psdcompose

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalid indicates malformed decoded data or invalid rendering options.
	ErrInvalid = errors.New("psdcompose: invalid data")
	// ErrUnsupported indicates visible rendering semantics that cannot be reproduced.
	ErrUnsupported = errors.New("psdcompose: unsupported rendering")
	// ErrLimit indicates that a configured working, layer or nesting budget was exceeded.
	ErrLimit = errors.New("psdcompose: limit exceeded")
)

// Error identifies a document field that prevents recomposition.
type Error struct {
	Kind   error
	Path   string
	Detail string
}

// Error implements error.
func (e *Error) Error() string {
	return fmt.Sprintf("%v at %s: %s", e.Kind, e.Path, e.Detail)
}

// Unwrap exposes the error category to errors.Is.
func (e *Error) Unwrap() error { return e.Kind }

// Options bounds recomposition's output, feather caches and tree working storage.
// Zero values select defaults. Caller-owned decoded pixel buffers are validated but
// are not charged to MaxWorkingBytes; callers decoding sources must budget those separately.
type Options struct {
	// Workers selects bounded row-block parallelism; zero uses up to four available Go processors.
	// The js/wasm target remains single-threaded. Negative values and values above 64 are invalid.
	Workers int
	// MaxWorkingBytes defaults to 256 MiB, including the owned output image.
	MaxWorkingBytes int64
	// MaxDepth defaults to 32, counting the outermost layer as depth one, and may not exceed 256.
	MaxDepth int
	// MaxLayers defaults to 500, counting all nodes including hidden ones.
	MaxLayers int
	// MaxFeatherRadius defaults to 1000 pixels and bounds Gaussian kernel construction.
	MaxFeatherRadius float64
	// DissolveSeed opts into psdkit's deterministic coordinate and layer-position noise.
	// Nil keeps Dissolve unsupported. This noise field does not match Adobe Photoshop's field.
	DissolveSeed *uint64
	// ApproximateHueSaturation explicitly selects the RGB8 HSL hue/range/colorize approximation.
	// Its native precision audit remains incomplete; default rendering supports neutral Hue/Saturation only.
	ApproximateHueSaturation bool
	// ApproximateDropShadow selects fractional spread, displacement and partial-alpha DropShadow coverage.
	// It enables Normal/Multiply layer and shadow blending and explicitly staged pixel masks.
	// The generic morphology and tent filter do not guarantee Photoshop pixel equivalence.
	// Other active effect families and unsupported advanced layer interactions remain rejected.
	ApproximateDropShadow bool
	// ApproximatePlacedColorOverlay permits pure Color Overlay on freshly rendered placed-layer pixels.
	// It reuses the existing Normal raster overlay stages while retaining the original placement identity.
	// Source projection may be approximate; saved placed-layer raster caches remain unsupported for effects.
	ApproximatePlacedColorOverlay bool
	// ApproximateClippedGroupBackdrop selects RGB reconstruction for a clipped Pass Through group base.
	// It preserves legacy alpha and shape; the zero value preserves legacy RGB as well.
	// Limited native initial/reopened stages calibrate this approximation; restored UI stages can differ.
	// Native alpha equality and complete Photoshop stage equivalence are not guaranteed.
	ApproximateClippedGroupBackdrop bool
	// MaxClippedGroupEvaluations bounds layer-source evaluation entries per output pixel when selected.
	// Zero selects 4096; ordinary and Knockout delegation entries each count once.
	// This bounds repeated isolated/backdrop evaluation without changing ROI-relative qualification.
	MaxClippedGroupEvaluations int64
}

func composeError(kind error, path, detail string) error {
	return &Error{Kind: kind, Path: path, Detail: detail}
}
