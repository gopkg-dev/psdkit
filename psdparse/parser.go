// Package psdparse reads bounded RGB8 Photoshop documents without native libraries or browser processes.
package psdparse

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"math"
	"os"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

type parser struct {
	document        *psdmodel.Document
	version         uint16
	channels        int
	globalAlpha     bool
	pixels          int64
	vectorBytes     int64
	adjustmentBytes int64
	metadataOnly    bool
}

// ParseFile parses a PSD or PSB from disk while retaining decoded pixels and useful metadata.
func ParseFile(filename string) (*psdmodel.Document, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	return ParseReader(file, info.Size())
}

// ParseBytes parses an existing in-memory PSD or PSB without copying the compressed source.
func ParseBytes(data []byte) (*psdmodel.Document, error) {
	return ParseReader(bytes.NewReader(data), int64(len(data)))
}

// ParseReader parses a seekable source after checking file, canvas, layer and mask allocation budgets.
func ParseReader(reader io.ReaderAt, size int64) (*psdmodel.Document, error) {
	return parseReader(reader, size, true)
}

// ParseLayerReader decodes the bounded RGB8 layer tree without decoding a saved merged preview.
// A layered semantic renderer must not depend on the validity or currency of cached footer pixels.
func ParseLayerReader(reader io.ReaderAt, size int64) (*psdmodel.Document, error) {
	return parseReader(reader, size, false)
}

func parseReader(reader io.ReaderAt, size int64, withPreview bool) (*psdmodel.Document, error) {
	if size < 26 || size > psdmodel.MaxFileBytes {
		return nil, fmt.Errorf("PSD must contain 26 bytes to 500 MiB")
	}
	r := &cursor{source: &source{reader: reader, size: size, demandOnly: !withPreview}, end: size}
	signature := r.sig()
	if r.err != nil {
		return nil, r.err
	}
	if signature != "8BPS" {
		return nil, fmt.Errorf("invalid PSD signature")
	}
	version := r.u16()
	if version != 1 && version != 2 {
		return nil, fmt.Errorf("unsupported PSD/PSB version %d", version)
	}
	r.skip(6)
	channels := int(r.u16())
	height, width := int(r.u32()), int(r.u32())
	depth, mode := r.u16(), r.u16()
	if r.err != nil {
		return nil, r.err
	}
	if depth != 8 || mode != 3 {
		return nil, fmt.Errorf("only RGB 8-bit PSD documents are supported")
	}
	if channels < 3 || channels > 16 {
		return nil, fmt.Errorf("PSD channel count must be between 3 and 16")
	}
	if err := checkDimensions(width, height, false); err != nil {
		return nil, err
	}
	p := &parser{
		document: &psdmodel.Document{Width: width, Height: height, LinkedNames: make(map[string]string)},
		version:  version, channels: channels, pixels: int64(width) * int64(height),
	}
	if !withPreview {
		p.pixels = 0
	}
	r.skip(int64(r.u32()))
	resources := r.section(int64(r.u32()))
	p.resources(resources)
	r.absorb(resources)
	if r.err != nil {
		return nil, r.err
	}
	layers := r.section(p.structureLength(r))
	if !withPreview && r.err == nil {
		// Once the layer section is located, bounded read-ahead may not enter the saved composite.
		r.source.size = layers.end
		r.source.demandOnly = false
	}
	p.layerSection(layers)
	r.absorb(layers)
	if r.err != nil {
		return nil, r.err
	}
	if withPreview {
		p.composite(r)
	}
	if r.err != nil {
		return nil, r.err
	}
	return p.document, nil
}

// structureLength reads one of the document length fields widened by the PSB format.
func (p *parser) structureLength(r *cursor) int64 {
	if p.version == 2 {
		if p.metadataOnly {
			length := r.u64()
			if length > math.MaxInt64 {
				r.fail("PSB channel length exceeds signed source offsets")
				return 0
			}
			return int64(length)
		}
		return checkedLength64(r)
	}
	return int64(r.u32())
}

func checkedLength64(r *cursor) int64 {
	length := r.u64()
	if length > math.MaxInt64 {
		r.fail("PSD/PSB 64-bit length exceeds signed source offsets at offset %d", r.position-8)
		return 0
	}
	if length > uint64(r.remaining()) {
		r.fail("invalid PSD/PSB 64-bit section length %d at offset %d", length, r.position-8)
		return 0
	}
	return int64(length)
}

func checkDimensions(width, height int, allowEmpty bool) error {
	if allowEmpty && width == 0 && height == 0 {
		return nil
	}
	if width <= 0 || height <= 0 || width > psdmodel.MaxDimension || height > psdmodel.MaxDimension ||
		int64(width)*int64(height) > psdmodel.MaxPixels {
		return fmt.Errorf("PSD canvas, layer or mask exceeds 30000 pixels per side or 20 million pixels")
	}
	return nil
}

func (p *parser) account(bounds image.Rectangle, r *cursor) {
	if p.metadataOnly {
		if bounds.Max.X < bounds.Min.X || bounds.Max.Y < bounds.Min.Y {
			r.fail("metadata rectangle has reversed edges")
		}
		return
	}
	if err := checkDimensions(bounds.Dx(), bounds.Dy(), true); err != nil {
		r.err = err
		return
	}
	p.pixels += int64(bounds.Dx()) * int64(bounds.Dy())
	if p.pixels > psdmodel.MaxDecodedPixels {
		r.fail("PSD layers and masks exceed 60 million decoded pixels")
	}
}

func rectangle(r *cursor) image.Rectangle {
	top, left, bottom, right := int(r.i32()), int(r.i32()), int(r.i32()), int(r.i32())
	return image.Rectangle{Min: image.Pt(left, top), Max: image.Pt(right, bottom)}
}

func freshImage(bounds image.Rectangle) *image.NRGBA {
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return nil
	}
	pixels := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for offset := 3; offset < len(pixels.Pix); offset += 4 {
		pixels.Pix[offset] = 255
	}
	return pixels
}
