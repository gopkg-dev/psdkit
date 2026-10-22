// Package psdfile indexes original PSD/PSB structures and decodes pixels on demand.
package psdfile

import (
	"errors"
	"fmt"
	"image"
	"io"
	"sync"
)

// Span identifies original bytes without copying or normalizing them.
type Span struct {
	Offset int64
	Length int64
}

// Header preserves the original PSD/PSB header fields.
type Header struct {
	Signature        string
	Version          uint16
	Reserved         [6]byte
	Channels         uint16
	Width, Height    uint32
	Depth, ColorMode uint16
}

// ResourceBlock preserves a resource's identifier, name, data and original alignment.
type ResourceBlock struct {
	Signature                     string
	ID                            uint16
	Name                          string
	Span, NameData, Data, Padding Span
}

// AdditionalBlock preserves an ordered tagged block, including unknown keys.
type AdditionalBlock struct {
	Signature, Key      string
	LengthWidth         uint8
	Span, Data, Padding Span
}

// ChannelRecord indexes a layer channel, including its compression word.
type ChannelRecord struct {
	ID             int16
	LengthWidth    uint8
	DeclaredLength uint64
	Compression    uint16
	Span, Data     Span
	Bounds         image.Rectangle
}

// MaskRecord preserves the mask's raw bytes and geometry used by its channels.
type MaskRecord struct {
	Span                                Span
	Bounds                              image.Rectangle
	DefaultColor, Flags, ParameterFlags uint8
	UserDensity, VectorDensity          *uint8
	UserFeather, VectorFeather          *float64
	HasReal                             bool
	RealBounds                          image.Rectangle
	RealFlags, RealDefaultColor         uint8
}

// LayerRecord keeps on-disk layer order and all original extra-data blocks.
// Span covers the record, not channel payloads; Extra is the payload after its length prefix.
// MaskData and BlendRanges include their length prefixes; NameData includes Pascal alignment.
type LayerRecord struct {
	Span                                   Span
	Bounds                                 image.Rectangle
	Name                                   string
	Channels                               []ChannelRecord
	BlendSignature, BlendMode              string
	Opacity, Clipping, Flags, Filler       uint8
	Extra, MaskData, BlendRanges, NameData Span
	Mask                                   *MaskRecord
	AdditionalInfo                         []AdditionalBlock
	Divider                                uint32
	ID                                     uint32
	HasID                                  bool
}

// LayerSet indexes the main or alternate Layr/Lr16/Lr32 layer-information payload.
// Span starts at the layer-count word and excludes any section length prefix.
type LayerSet struct {
	Key           string
	Depth         uint16
	Span, Padding Span
	GlobalAlpha   bool
	Layers        []LayerRecord
}

// CompositeRecord indexes the saved image without decoding its compressed payload.
type CompositeRecord struct {
	Span, Data Span
	// ReservedTail preserves bytes after a self-delimited Raw/RLE preview inside linked content.
	// Standalone sources retain this capacity only when VersionInfo explicitly marks the preview non-real.
	ReservedTail  Span
	Compression   uint16
	RowCountWidth uint8
}

// Limits bounds indexing, descriptor parsing and each explicit pixel decode.
// Zero values select defaults. Negative values are invalid.
type Limits struct {
	MaxFileBytes       int64
	MaxMetadataBytes   int64
	MaxReadBytes       int64
	MaxBlocks          int
	MaxLayers          int
	MaxDecodePixels    int64
	MaxDecodeBytes     int64
	MaxDescriptorDepth int
	MaxDescriptorItems int
}

// LoadOptions configures source indexing and on-demand allocation budgets.
type LoadOptions struct {
	Limits Limits
}

// DefaultLimits returns independent, bounded budgets without limiting PSB metadata to RGB8 images.
func DefaultLimits() Limits {
	return Limits{
		MaxFileBytes: 500 << 20, MaxMetadataBytes: 32 << 20, MaxReadBytes: 8 << 20,
		MaxBlocks: 100_000, MaxLayers: 10_000, MaxDecodePixels: 20_000_000,
		MaxDecodeBytes: 256 << 20, MaxDescriptorDepth: 32, MaxDescriptorItems: 100_000,
	}
}

var (
	// ErrClosed identifies use of a closed source document.
	ErrClosed = errors.New("PSD source is closed")
	// ErrMalformed identifies invalid or truncated original structures.
	ErrMalformed = errors.New("malformed PSD/PSB structure")
	// ErrLimit identifies a configured resource budget violation.
	ErrLimit = errors.New("PSD resource limit exceeded")
	// ErrUnsupported identifies an unsupported decode or descriptor feature.
	ErrUnsupported = errors.New("unsupported PSD feature")
)

// Error provides a machine-readable feature, field path and original file offset.
type Error struct {
	Kind          error
	Feature, Path string
	Offset        int64
	Detail        string
	Cause         error
}

// Error describes the failed operation without embedding raw identifiers or payloads.
func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s at offset %d: %s", e.Kind, e.Path, e.Offset, e.Detail)
}

// Unwrap supports errors.Is and errors.As for the category and underlying I/O error.
func (e *Error) Unwrap() []error {
	if e.Cause == nil {
		return []error{e.Kind}
	}
	return []error{e.Kind, e.Cause}
}

func fileError(kind error, feature, path string, offset int64, detail string) *Error {
	return &Error{Kind: kind, Feature: feature, Path: path, Offset: offset, Detail: detail}
}

// Document owns an indexed source until Close; exported metadata must be treated as read-only.
// Layers are in physical file order, while the legacy decoded model uses bottom-to-top order.
// ColorModeData, ResourceSection, LayerMaskSection, LayerInfo and GlobalMask include length prefixes.
type Document struct {
	Header                                                               Header
	Source, HeaderData, ColorModeData, ResourceSection, LayerMaskSection Span
	LayerInfo, GlobalMask                                                Span
	Resources                                                            []ResourceBlock
	AdditionalInfo                                                       []AdditionalBlock
	LayerSets                                                            []LayerSet
	ActiveLayerSet                                                       int
	Layers                                                               []LayerRecord
	Composite                                                            CompositeRecord
	GlobalAlpha                                                          bool
	Issues                                                               []Issue
	reader                                                               io.ReaderAt
	closer                                                               io.Closer
	size                                                                 int64
	limits                                                               Limits
	metadataBytes                                                        int64
	linkedContent                                                        bool
	mu                                                                   sync.RWMutex
	closed                                                               bool
}

// Issue reports a capability limitation separately from valid source indexing.
type Issue struct {
	Feature, Path string
	Offset        int64
	Detail        string
}

// Plane contains owned decoded source samples, not a color-converted RGBA8 image.
// Data uses big-endian 16/32-bit samples or packed MSB-first 1-bit samples.
type Plane struct {
	ID            int16
	Width, Height int
	Depth         uint16
	RowBytes      int
	Data          []byte
}

// LayerPixels contains independent channel planes with their original dimensions and depth.
type LayerPixels struct {
	Bounds   image.Rectangle
	Channels []Plane
}

// CompositePixels contains source-precision saved-image planes in their original order.
type CompositePixels struct {
	Width, Height    int
	Depth, ColorMode uint16
	Channels         []Plane
}
