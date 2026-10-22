// Package psdfont reads supplied TrueType, CFF1 and CFF2 fonts and defaults to unhinted Latin layout.
// HintRun explicitly fits bounded CFF1 source hints; TrueType instructions remain unexecuted.
// It never searches font directories, substitutes fonts or shapes complex scripts.
package psdfont

import (
	"context"
	"errors"
	"fmt"
	"math"
)

var (
	// ErrMalformed identifies malformed font structures or invalid caller geometry.
	ErrMalformed = errors.New("psdfont: malformed font or geometry")
	// ErrUnsupported identifies unsupported font outlines, shaping or layout semantics.
	ErrUnsupported = errors.New("psdfont: unsupported font feature")
	// ErrLimit identifies a configured byte, count or nesting limit.
	ErrLimit = errors.New("psdfont: resource limit exceeded")
	// ErrMissingFont indicates that the requested font was not explicitly supplied.
	ErrMissingFont = errors.New("psdfont: requested font is unavailable")
	// ErrMissingGlyph indicates that the selected Unicode cmap has no glyph for a character.
	ErrMissingGlyph = errors.New("psdfont: requested character has no glyph")
)

// Error identifies a bounded field without embedding font paths or text contents.
type Error struct {
	Kind         error
	Path, Detail string
	Offset       int64
}

// Error implements error.
func (e *Error) Error() string {
	return fmt.Sprintf("%v at %s offset %d: %s", e.Kind, e.Path, e.Offset, e.Detail)
}

// Unwrap exposes the category for errors.Is.
func (e *Error) Unwrap() error { return e.Kind }

// Span identifies bytes in the originally supplied font file or collection.
type Span struct{ Offset, Length int64 }

// Table preserves one sfnt table record in original directory order.
type Table struct {
	Tag      string
	Checksum uint32
	Span     Span
}

// Face owns one selected TrueType, CFF1 or immutable CFF2 instance and its original font bytes.
// Public metadata must be treated as read-only; glyph calls return independent owned outlines.
type Face struct {
	FaceIndex                    int
	PostScriptName               string
	UnitsPerEm, NumGlyphs        uint16
	Ascender, Descender, LineGap int16
	Tables                       []Table
	raw                          []byte
	tables                       map[string]Span
	locaLong                     bool
	horizontalMetrics            uint16
	cmap                         cmapTable
	kerns                        []kernTable
	kernUnsupported              bool
	reservation                  int64
	cff                          *cffFont
	cff2                         *cff2Font
	variation                    *fontVariationData
	hvar                         *horizontalVariation
	mvar                         *metricVariation
	gdefStore                    *itemVariationStore
}

// DecodeOptions bounds retained font bytes and font-directory/cmap traversal.
// Zero selects 32 MiB, 512 tables, 256 faces, 65535 glyphs, 100000 cmap entries and 1000000 CFF operations.
// MaxCFFOperations is shared by CFF directories, instance requests and all supported variation metadata.
type DecodeOptions struct {
	FaceIndex        int
	MaxBytes         int64
	MaxTables        int
	MaxFaces         int
	MaxGlyphs        int
	MaxCmapEntries   int
	MaxCFFOperations int
	// Variations explicitly selects CFF2 user coordinates; unlisted axes use fvar defaults.
	// Requests snapshot before context callbacks or Open I/O, then clamp and use declared fixed rounding.
	Variations []VariationCoordinate
}

// GlyphOptions bounds one owned glyph decode, including Type2 subroutine interpretation.
// Zero selects 16 MiB, 65536 points, 8192 contours, 4096 component visits, depth 32 and 1000000 operations.
// A CFF1 endchar parent, both components, their subroutines and name lookups share working-byte and work limits.
// Type2 subroutine nesting remains limited to ten per program; MaxDepth also includes the parent/component call depth.
type GlyphOptions struct {
	MaxWorkingBytes int64
	MaxPoints       int
	MaxContours     int
	MaxComponents   int
	MaxDepth        int
	MaxOperations   int
	// PreserveCFFHints decodes owned CFF1 stem, mask and private hint metadata without executing it.
	// The default leaves CFFHints nil and preserves the existing unhinted outline path.
	PreserveCFFHints bool
}

// Point is an unhinted design-space coordinate, including quadratic or cubic controls.
type Point struct {
	X, Y    float64
	OnCurve bool
}

// PathOp identifies a design-space path operation.
type PathOp uint8

const (
	// Move starts a contour.
	Move PathOp = iota + 1
	// Line adds a straight segment.
	Line
	// Quad adds a quadratic segment using ControlX and ControlY.
	Quad
	// Close closes the current contour.
	Close
	// Cubic adds a cubic segment using ControlX/ControlY and Control2X/Control2Y.
	Cubic
)

// Command describes one unhinted path operation; second controls are used only by Cubic.
type Command struct {
	Op                       PathOp
	X, Y, ControlX, ControlY float64
	Control2X, Control2Y     float64
}

// Glyph owns its contours and authoritative quadratic or cubic path in design units.
// CFF contours contain two off-curve points per cubic; use Path to distinguish cubic from quadratic segments.
// AdvanceWidth and LeftSideBearing retain original hmtx metrics, not hinted phantom-point adjustments.
// Instructions records original TrueType bytecode without executing it, including composite instruction spans.
// CFF Type2 charstring bytes are retained by Source and available through Face.ReadSpan.
type Glyph struct {
	ID              uint16
	AdvanceWidth    uint16
	LeftSideBearing int16
	// CharstringWidth retains CFF1's embedded width and is zero for CFF2; hmtx fields remain original.
	CharstringWidth float64
	Bounds          [4]int16
	Contours        [][]Point
	Path            []Command
	Source          Span
	// ComponentSources records original CFF1 endchar base and accent charstrings in that order.
	// Ordinary CFF1 and TrueType glyphs leave this owned slice empty; Instructions remains TrueType-only.
	ComponentSources []Span `json:",omitempty"`
	// InstanceMetrics retains precise selected CFF2 instance values; hmtx fields above remain raw.
	InstanceMetrics *InstanceGlyphMetrics `json:",omitempty"`
	// CFFHints owns CFF1 source hint metadata only when explicitly requested through GlyphOptions.
	CFFHints            *CFFHints `json:",omitempty"`
	Instructions        []Span
	HasComponentMetrics bool
	reservation         int64
}

// WorkingBytes reports the conservative allocation reservation used to produce this owned glyph.
func (g *Glyph) WorkingBytes() int64 {
	if g == nil {
		return 0
	}
	return g.reservation
}

// FontResolver resolves an exact requested PostScript name using caller-selected fonts only.
type FontResolver interface {
	ResolveFont(context.Context, string) (*Face, error)
}

// ResolverFunc adapts a caller function to FontResolver.
type ResolverFunc func(context.Context, string) (*Face, error)

// ResolveFont invokes the caller's explicit resolver.
func (f ResolverFunc) ResolveFont(ctx context.Context, name string) (*Face, error) {
	if f == nil {
		return nil, ErrMissingFont
	}
	return f(ctx, name)
}

func fontError(kind error, path string, offset int64, detail string) error {
	return &Error{Kind: kind, Path: path, Offset: offset, Detail: detail}
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func decodeConfiguration(options []DecodeOptions) (DecodeOptions, error) {
	result := DecodeOptions{MaxBytes: 32 << 20, MaxTables: 512, MaxFaces: 256,
		MaxGlyphs: 65535, MaxCmapEntries: 100_000, MaxCFFOperations: 1_000_000}
	if len(options) > 1 {
		return result, fontError(ErrMalformed, "options", 0, "provide at most one options value")
	}
	if len(options) == 0 {
		return result, nil
	}
	provided := options[0]
	if provided.FaceIndex < 0 || provided.MaxBytes < 0 || provided.MaxTables < 0 || provided.MaxFaces < 0 ||
		provided.MaxGlyphs < 0 || provided.MaxCmapEntries < 0 || provided.MaxCFFOperations < 0 {
		return result, fontError(ErrLimit, "options", 0, "limits and face index must not be negative")
	}
	result.FaceIndex = provided.FaceIndex
	result.Variations = provided.Variations
	if provided.MaxBytes != 0 {
		result.MaxBytes = provided.MaxBytes
	}
	for _, pair := range [][2]*int{
		{&result.MaxTables, &provided.MaxTables}, {&result.MaxFaces, &provided.MaxFaces},
		{&result.MaxGlyphs, &provided.MaxGlyphs}, {&result.MaxCmapEntries, &provided.MaxCmapEntries},
		{&result.MaxCFFOperations, &provided.MaxCFFOperations},
	} {
		if *pair[1] != 0 {
			*pair[0] = *pair[1]
		}
	}
	return result, nil
}
