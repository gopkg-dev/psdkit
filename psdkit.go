// Package psdkit reads and renders a bounded subset of Photoshop PSD documents in pure Go.
//
// OpenDocument indexes PSD/PSB sources while preserving original blocks and encoded channels.
// It decodes source samples on demand without reducing their bit depth. Open/Parse/Read remain
// eager RGB8 compatibility APIs. OpenEditor provides controlled edits and PSD/PSB saving.
// Encode exports rendered PNG or JPEG images.
//
// Treat documents, input images and returned images as read-only while a renderer uses them.
// Some rendering and output operations borrow existing pixel buffers; copy pixels before editing.
package psdkit

import (
	"context"
	"errors"
	"image"
	"io"
	"os"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
)

// Document contains decoded RGB8 data, with layers ordered bottom to top.
// A renderer borrows its document; callers must not modify it until all renderers stop using it.
type Document = psdmodel.Document

// Layer describes saved raster pixels, placement, opacity and supported smart-object metadata.
type Layer = psdmodel.Layer

// Mask describes a raster mask and its default coverage outside the saved mask bounds.
type Mask = psdmodel.Mask

// BlendSlider describes the split black and white endpoints of a Blend If slider.
type BlendSlider = psdmodel.BlendSlider

// BlendRange pairs source and underlying sliders; the first range is gray, followed by RGB.
type BlendRange = psdmodel.BlendRange

// Placed describes a smart-object instance and its saved transform metadata.
type Placed = psdmodel.Placed

// Point is a double-precision canvas coordinate.
type Point = psdmodel.Point

// WarpBounds describes the bounds and coordinate units of a Photoshop warp descriptor.
type WarpBounds = psdmodel.WarpBounds

// Warp retains metadata used to detect unsupported Photoshop warp transforms.
type Warp = psdmodel.Warp

// WarpSlice preserves the source coordinate domain of one native quilt slice column.
type WarpSlice = psdmodel.WarpSlice

// Slice describes a rectangular document crop.
type Slice = psdmodel.Slice

// Renderer renders a borrowed immutable document and serializes concurrent calls.
// Render can return the document's composite buffer; treat all returned pixels as read-only.
type Renderer = psdrender.Engine

// Metadata reports supported objects, crops, warnings and blocking rendering issues.
type Metadata = psdrender.Metadata

// Object groups all visible instances of a linked smart object.
type Object = psdrender.Object

// Instance describes one visible smart-object occurrence.
type Instance = psdrender.Instance

// BackgroundLayer describes a saved raster available for background replacement.
type BackgroundLayer = psdrender.BackgroundLayer

// ObjectConfig maps a smart object to a replacement image tag.
type ObjectConfig = psdrender.ObjectConfig

// Candidate describes an external image available for random background selection.
type Candidate = psdrender.Candidate

// RandomRow configures independent random replacements for one saved raster.
type RandomRow = psdrender.RandomRow

// Config controls replacement targets, fitting and alignment.
type Config = psdrender.Config

// Options controls input transparency trimming, output crops, format and longest-edge limits.
type Options = psdrender.Options

// RenderOptions is an alternative name for Options.
type RenderOptions = psdrender.RenderOptions

const (
	// MaxPixels bounds each decoded canvas, layer or mask.
	MaxPixels = psdmodel.MaxPixels
	// MaxDimension bounds each side of a decoded image.
	MaxDimension = psdmodel.MaxDimension
	// MaxDecodedPixels bounds the combined canvas, layer and mask pixel count.
	MaxDecodedPixels = psdmodel.MaxDecodedPixels
	// MaxInputPixels bounds the combined unique replacement and background pixel count.
	MaxInputPixels = psdrender.MaxInputPixels
	// MaxLayers bounds the number of decoded layer records.
	MaxLayers = psdmodel.MaxLayers
	// MaxFileBytes bounds the encoded PSD source size.
	MaxFileBytes = psdmodel.MaxFileBytes
)

var (
	// ErrUnsupported identifies features that prevent rendering; use errors.Is to check it.
	ErrUnsupported = psdrender.ErrUnsupported
	// ErrImageSize identifies invalid or unsupported image dimensions; use errors.Is to check it.
	ErrImageSize = psdrender.ErrImageSize
)

// Open reads an RGB8 PSD/PSB file and closes it before returning decoded pixels and metadata.
// Use OpenDocument for original-block retention and on-demand source-precision decoding.
func Open(filename string) (document *Document, err error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	return Read(file, info.Size())
}

// Parse eagerly reads an in-memory RGB8 PSD/PSB without retaining or copying its encoded source.
func Parse(data []byte) (document *Document, err error) {
	source, err := ParseDocument(data)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	return source.Decode(context.Background())
}

// Read eagerly reads a bounded RGB8 PSD/PSB from a random-access source of the supplied size.
// It does not close or retain reader; the caller owns the source's lifetime.
func Read(reader io.ReaderAt, size int64) (document *Document, err error) {
	if reader == nil {
		return nil, errors.New("missing PSD reader")
	}
	source, err := ReadDocument(reader, size)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	return source.Decode(context.Background())
}

// Inspect reports rendering capabilities without creating a renderer's caches.
func Inspect(document *Document) (Metadata, error) {
	return psdrender.Inspect(document)
}

// NewRenderer validates the supported RGB8 subset and borrows an immutable document.
// Unsupported features return an error wrapping ErrUnsupported.
func NewRenderer(document *Document) (*Renderer, error) {
	return psdrender.New(document)
}

// DefaultConfig enables each visible smart object using its source filename as the image tag.
func DefaultConfig(metadata Metadata) Config {
	return psdrender.DefaultConfig(metadata)
}

// ValidateConfig rejects invalid fitting, stale targets and conflicting replacements.
func ValidateConfig(config Config, metadata Metadata) error {
	return psdrender.ValidateConfig(config, metadata)
}

// ValidateOptions checks the PNG/JPEG output format and longest-edge limit.
func ValidateOptions(options Options) error {
	return psdrender.ValidateOptions(options)
}

// AssertImageSize checks dimensions before allocating or decoding an image.
func AssertImageSize(width, height int) error {
	return psdrender.AssertImageSize(width, height)
}

// EmptyImage allocates a bounded transparent straight-alpha image.
func EmptyImage(width, height int) (*image.NRGBA, error) {
	return psdrender.EmptyImage(width, height)
}

// PrepareOutput shrinks an image to Options.MaxSize and otherwise borrows the original pixels.
// The returned image must be treated as read-only. Format must be "png" or "jpeg".
func PrepareOutput(source *image.NRGBA, options Options) (*image.NRGBA, error) {
	return psdrender.PrepareOutput(source, options)
}

// Encode writes PNG or quality-95 JPEG pixels without modifying source or closing writer.
// It exports a raster image, not a PSD file. JPEG transparency is flattened against white.
func Encode(writer io.Writer, source *image.NRGBA, format string) error {
	return psdrender.Encode(writer, source, format)
}
