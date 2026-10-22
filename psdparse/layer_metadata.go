package psdparse

import (
	"context"
	"fmt"
	"io"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

// LayerMetadataOptions supplies the original document geometry and one indexed record interval.
// This entry point reads record metadata only; channel payloads and the merged preview are outside its interval.
type LayerMetadataOptions struct {
	Version       uint16
	Width, Height int
	Offset, Size  int64
}

// LayerMetadata holds owned semantics without raster pixels, plus the physical divider and live mask channel.
// PixelMaskChannel is -2 or -3 when the layer requires an ordinary source mask; zero means none.
type LayerMetadata struct {
	Layer            *psdmodel.Layer
	Divider          uint32
	PixelMaskChannel int16
	Warnings         []string
	BlockingIssues   []string
}

// ParseLayerMetadata decodes an indexed PSD/PSB layer record without eager pixel or canvas allocation.
// Callers validate and budget original metadata before this compatibility-semantic bridge.
func ParseLayerMetadata(
	ctx context.Context, reader io.ReaderAt, options LayerMetadataOptions,
) (*LayerMetadata, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if reader == nil || options.Version != 1 && options.Version != 2 || options.Width <= 0 || options.Height <= 0 ||
		options.Offset < 0 || options.Size < 34 || options.Offset > int64(^uint64(0)>>1)-options.Size {
		return nil, fmt.Errorf("invalid layer metadata interval or document geometry")
	}
	end := options.Offset + options.Size
	p := &parser{version: options.Version, metadataOnly: true,
		document: &psdmodel.Document{Width: options.Width, Height: options.Height, LinkedNames: make(map[string]string)}}
	r := &cursor{source: &source{reader: metadataContextReader{ctx: ctx, reader: reader}, size: end},
		position: options.Offset, end: end}
	record := p.layerRecord(r)
	if r.err != nil {
		return nil, r.err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.remaining() != 0 {
		return nil, fmt.Errorf("layer metadata interval contains unparsed trailing bytes")
	}
	channel := int16(0)
	if record.layer.Mask != nil {
		channel = -2
	}
	if record.renderedMask && record.userMask != nil {
		record.layer.Mask = record.userMask
		channel = -3
	} else if record.layer.VectorMask != nil && record.renderedMask {
		record.layer.Mask = record.userMask
		channel = 0
	}
	return &LayerMetadata{Layer: record.layer, Divider: uint32(record.divider), PixelMaskChannel: channel,
		Warnings: p.document.Warnings, BlockingIssues: p.document.BlockingIssues}, nil
}

type metadataContextReader struct {
	ctx    context.Context
	reader io.ReaderAt
}

func (reader metadataContextReader) ReadAt(data []byte, offset int64) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	count, err := reader.reader.ReadAt(data, offset)
	if cancellation := reader.ctx.Err(); cancellation != nil {
		return 0, cancellation
	}
	return count, err
}
