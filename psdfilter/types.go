// Package psdfilter executes bounded RGB8 smart-filter pipelines without saved raster caches.
package psdfilter

import (
	"errors"
	"fmt"

	"github.com/gopkg-dev/psdkit/psdadjust"
)

var (
	// ErrInvalid identifies invalid pixels or typed filter controls.
	ErrInvalid = errors.New("invalid PSD smart filter")
	// ErrUnsupported identifies filter semantics that this engine cannot execute.
	ErrUnsupported = errors.New("unsupported PSD smart filter")
	// ErrLimit identifies a exceeded pixel, filter-count or working allocation budget.
	ErrLimit = errors.New("PSD smart-filter resource limit exceeded")
)

// Kind identifies a live filter algorithm.
type Kind string

const (
	// GaussianBlur approximates Photoshop's private Gaussian using integrated pixels and finite box convolution.
	GaussianBlur Kind = "gaussian-blur"
	// SurfaceBlur applies an explicitly approximate range-weighted, edge-preserving blur.
	SurfaceBlur Kind = "surface-blur"
	// SmartSharpen applies an explicitly approximate lens-blur unsharp transform.
	SmartSharpen Kind = "smart-sharpen"
	// BrightnessContrast applies a typed encoded-RGB brightness/contrast adjustment.
	BrightnessContrast Kind = "brightness-contrast"
	// HueSaturation applies a typed encoded-RGB hue/saturation adjustment.
	HueSaturation Kind = "hue-saturation"
)

// Filter contains one source-ordered smart-filter operation.
// Radius is in source pixels; Opacity ranges from zero to one.
// Enabled false leaves the previous pipeline result untouched.
type Filter struct {
	Kind      Kind
	Enabled   bool
	Radius    float64
	Opacity   float64
	BlendMode string
	// Threshold is the Surface Blur maximum encoded-RGB channel difference, from zero to 255.
	Threshold float64
	// Amount is the Smart Sharpen factor, so 3.76 means 376 percent.
	Amount float64
	// NoiseReduction is the Smart Sharpen noise suppression fraction, from zero to one.
	NoiseReduction float64
	// BlurModel identifies the Smart Sharpen point-spread model; only "lens-blur" is implemented.
	BlurModel string
	// UseLegacy retains the original sharpen-generation policy; true is unsupported.
	UseLegacy bool
	// Adjustment contains typed color controls; spatial operations require it to be nil.
	Adjustment *psdadjust.Adjustment
}

// Stack retains the source execution order, from first applied to last applied.
type Stack struct {
	Enabled bool
	Filters []Filter
}

// Options bounds live operations; zero selects twenty million pixels, 256 MiB and 4096 filters.
// Approximation policies select declared RGB8 models rather than Photoshop's private algorithms.
type Options struct {
	MaxPixels, MaxWorkingBytes int64
	MaxFilters                 int
	// ApproximateGaussian opts into calibrated finite encoded-RGB premultiplied convolution.
	ApproximateGaussian bool
	// ApproximateSurfaceBlur opts into a finite tent footprint with a maximum-channel range gate.
	ApproximateSurfaceBlur bool
	// ApproximateSmartSharpen opts into an alpha-aware lens disk and noise-damped unsharp correction.
	ApproximateSmartSharpen bool
	// ApproximateBrightnessContrast opts into rational brightness and symmetric power contrast for modern controls.
	// Legacy controls use a linear brightness shift and the retained Mean contrast pivot.
	ApproximateBrightnessContrast bool
	// ApproximateHueSaturation opts into psdadjust's encoded-RGB HSL transform.
	// Plain master Lightness uses the verified RGB8 transfer without this option.
	ApproximateHueSaturation bool
	// MaxSampleOperations bounds spatial neighborhood samples across an active stack; zero selects 2^32.
	MaxSampleOperations int64
}

// Estimate describes a live operation's retained result and peak additional working storage.
// Caller-owned source pixels and stack controls are excluded; every allocated output, scratch and kernel is included.
type Estimate struct {
	RetainedBytes, WorkingBytes int64
}

// Error identifies one invalid filter field without printing source payloads.
type Error struct {
	Kind         error
	Path, Detail string
}

// Error describes the failed operation.
func (e *Error) Error() string { return fmt.Sprintf("%v: %s: %s", e.Kind, e.Path, e.Detail) }

// Unwrap exposes the error category to errors.Is.
func (e *Error) Unwrap() error { return e.Kind }

func filterError(kind error, path, detail string) *Error {
	return &Error{Kind: kind, Path: path, Detail: detail}
}
