// Package psdvector decodes retained Photoshop path records and rasterizes filled vector masks.
// It uses document-space cubic Bezier paths and does not rasterize strokes or text.
package psdvector

import (
	"errors"
	"fmt"
)

var (
	// ErrMalformed identifies invalid path structure or malformed decoded geometry.
	ErrMalformed = errors.New("malformed Photoshop vector path")
	// ErrUnsupported identifies vector semantics that cannot be reproduced safely.
	ErrUnsupported = errors.New("unsupported Photoshop vector path")
	// ErrLimit identifies an allocation, subdivision or scanline-work budget violation.
	ErrLimit = errors.New("photoshop vector resource limit exceeded")
)

// Error describes a field and original byte position without embedding source payloads.
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

// Span identifies original bytes; it does not retain a source reader.
type Span struct {
	Offset, Length int64
}

// FillRule determines how contours in one merged path component enclose pixels.
type FillRule uint8

const (
	// EvenOdd toggles coverage at every contour crossing, as specified for Photoshop path resources.
	EvenOdd FillRule = iota
	// NonZero uses the signed winding count of all contours in a component.
	NonZero
)

// Point retains signed 8.24 source coordinates and their document-pixel conversion.
// X and Y are pixel coordinates; RawX and RawY are the original horizontal and vertical integers.
type Point struct {
	X, Y       float64
	RawX, RawY int32
}

// Record preserves an entire 26-byte path record, including unrecognized selectors and reserved data.
type Record struct {
	Selector uint16
	Raw      [26]byte
	Span     Span
}

// Knot keeps incoming control, anchor and outgoing control points in that order.
// Linked describes the editing relationship of handles, without changing the saved curve geometry.
type Knot struct {
	Preceding, Anchor, Leaving Point
	Linked                     bool
	RecordIndex                int
}

// Subpath contains one ordered knot sequence and its retained length-record metadata.
// Operation is -1 for a continuation, 0 for exclude, 1 for combine, 2 for subtract and 3 for intersect.
// Index retains the originating shape identifier and does not affect fill geometry.
type Subpath struct {
	Closed      bool
	Operation   int16
	Index       uint32
	Unknown1    uint16
	Unknown2    uint32
	Reserved    [10]byte
	Metadata    [22]byte
	Knots       []Knot
	RecordIndex int
	Span        Span
}

// Clipboard retains a path clipboard rectangle and resolution as their original fixed-point integers.
type Clipboard struct {
	Top, Left, Bottom, Right, Resolution int32
	RecordIndex                          int
	Span                                 Span
}

// Mask is an independently owned typed view of a version-three vmsk/vsms payload or headerless path resource.
// Records, Header and Padding retain the original encoding; geometry is read-only during rasterization.
// Unknown record and reserved-field semantics are preserved and rejected by Rasterize.
type Mask struct {
	Width, Height                 int
	Version, Flags                uint32
	Invert, NotLinked, Disabled   bool
	FillRule                      FillRule
	HasFillRule                   bool
	InitialFill                   bool
	HasInitialFill                bool
	Subpaths                      []Subpath
	Clipboards                    []Clipboard
	Records                       []Record
	Header                        [8]byte
	Padding                       []byte
	Unsupported                   []string
	Span, HeaderSpan, PaddingSpan Span
}

// DecodeOptions bounds the owned metadata created by DecodeMask.
type DecodeOptions struct {
	// SourceOffset identifies the original payload start and defaults to zero.
	SourceOffset int64
	// MaxBytes defaults to 32 MiB and includes input bytes and conservatively reserved owned metadata.
	MaxBytes int64
	// MaxRecords defaults to 100,000 records, including knots and unknown selectors.
	MaxRecords int
}

// EncodeOptions bounds the independently owned wire bytes produced by EncodeMask and EncodePath.
type EncodeOptions struct {
	// MaxBytes defaults to 32 MiB and includes the header, records and alignment bytes.
	MaxBytes int64
	// MaxRecords defaults to 100,000 records, including preserved unknown records.
	MaxRecords int
}

// Options bounds filled-mask rasterization without charging the caller-owned typed path metadata.
type Options struct {
	// Samples selects a 4 by 4 or 8 by 8 sample grid; zero selects 8.
	Samples int
	// AreaCoverage integrates horizontal spans using fixed vertical midpoint samples per pixel.
	// It avoids horizontal grid quantization while retaining the same bounded flattened geometry and fill rules.
	// Samples still validates the legacy grid selection but AreaSamples selects the area sampling rate.
	AreaCoverage bool
	// AreaSamples selects 4, 8, 16 or 64 absolute vertical midpoint samples; zero selects 64.
	// A nonzero value requires AreaCoverage. It does not alter outline fitting or horizontal integration.
	AreaSamples int
	// CoverageLevels16 quantizes complete filled coverage to 16 alpha levels, from 0 to 255 in steps of 17.
	// The level is min(15, floor(coverage*16)); overlapping paths are combined before this quantization.
	// This optional transfer does not execute font hinting or reproduce a native antialias mode.
	CoverageLevels16 bool
	// NoAntialias uses one pixel-center membership sample and emits only 0 or 255.
	// It is an explicit unhinted outline model and is incompatible with AreaCoverage and CoverageLevels16.
	NoAntialias bool
	// Tolerance bounds cubic flattening in document pixels; zero selects 1/64 pixel.
	Tolerance float64
	// FillRule overrides the decoded even-odd default when nonnil.
	FillRule *FillRule
	// MaxWorkingBytes defaults to 256 MiB, including output, flattened edges and scanline storage.
	MaxWorkingBytes int64
	// MaxSegments defaults to 1,000,000 flattened edges.
	MaxSegments int
	// MaxDepth defaults to 24 cubic subdivisions and may not exceed 32.
	MaxDepth int
	// MaxScanlineWork defaults to 100,000,000 edge/component steps across supersampled scanlines.
	MaxScanlineWork int64
}

func vectorError(kind error, path string, offset int64, detail string) error {
	return &Error{Kind: kind, Path: path, Offset: offset, Detail: detail}
}
