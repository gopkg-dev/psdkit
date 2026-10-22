package psdfile

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
)

// RegionOptions independently bounds sequential decompression and RLE scan work for one region request.
// Zero selects defaults; negative values are invalid. Raising work limits does not raise allocation limits.
type RegionOptions struct {
	// MaxWorkingBytes defaults to the source MaxDecodeBytes; a nonzero value may only lower that bound.
	MaxWorkingBytes int64
	// MaxInflatedBytes defaults to the source document's MaxDecodeBytes and counts every ZIP output byte.
	MaxInflatedBytes int64
	// MaxRLEWorkBytes defaults to 256 MiB and counts the full row table plus selected compressed rows.
	MaxRLEWorkBytes int64
}

// DecodeChannelRegion decodes a channel-local rectangle into owned source-precision samples.
// The origin is the channel's Bounds.Min, including for mask channels; rectangles must lie entirely inside it.
// MaxDecodePixels bounds the region, and MaxDecodeBytes bounds output plus row/table scratch, not the full image.
// Raw/RLE requests read only selected pixel segments; nonempty ZIP requests validate the entire compressed stream.
func (d *Document) DecodeChannelRegion(
	ctx context.Context, layerIndex, channelIndex int, rectangle image.Rectangle, options ...RegionOptions,
) (*Plane, error) {
	ctx = decodeContext(ctx)
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	layer, err := d.decodeLayerRecord(layerIndex)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("layers[%d].channels[%d].region", layerIndex, channelIndex)
	if channelIndex < 0 || channelIndex >= len(layer.Channels) {
		return nil, fileError(ErrMalformed, "pixels.region", path, layer.Span.Offset, "channel index is out of range")
	}
	channel := layer.Channels[channelIndex]
	source, err := regionGeometry(channel.Bounds, d.decodeLayerDepth(), path, channel.Data.Offset)
	if err != nil {
		return nil, err
	}
	return d.decodeRegion(ctx, source, channel.Data, channel.Compression, d.decodeRowCountWidth(),
		1, 0, channel.ID, rectangle, path, options)
}

// DecodeCompositeChannelRegion decodes a channel-local saved-image rectangle at its original bit depth.
// ZIP validates every channel in the shared stream, retaining only the requested channel region.
func (d *Document) DecodeCompositeChannelRegion(
	ctx context.Context, channelIndex int, rectangle image.Rectangle, options ...RegionOptions,
) (*Plane, error) {
	ctx = decodeContext(ctx)
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := fmt.Sprintf("composite.channels[%d].region", channelIndex)
	channels := int(d.Header.Channels)
	if channelIndex < 0 || channelIndex >= channels {
		return nil, fileError(ErrMalformed, "pixels.region", path, d.Composite.Data.Offset,
			"channel index is out of range")
	}
	source, err := regionDimensions(uint64(d.Header.Width), uint64(d.Header.Height), d.Header.Depth,
		path, d.Composite.Data.Offset)
	if err != nil {
		return nil, err
	}
	countWidth := d.Composite.RowCountWidth
	if countWidth == 0 {
		countWidth = d.decodeRowCountWidth()
	}
	return d.decodeRegion(ctx, source, d.Composite.Data, d.Composite.Compression, countWidth,
		channels, channelIndex, int16(channelIndex), rectangle, path, options)
}

type regionSource struct {
	width, height, rowBytes int
	length                  int64
	depth                   uint16
}

func regionGeometry(bounds image.Rectangle, depth uint16, path string, offset int64) (regionSource, error) {
	if bounds.Max.X < bounds.Min.X || bounds.Max.Y < bounds.Min.Y {
		return regionSource{}, fileError(ErrMalformed, "pixels.region", path, offset, "negative channel dimensions")
	}
	return regionDimensions(uint64(bounds.Max.X)-uint64(bounds.Min.X),
		uint64(bounds.Max.Y)-uint64(bounds.Min.Y), depth, path, offset)
}

func regionDimensions(width, height uint64, depth uint16, path string, offset int64) (regionSource, error) {
	if depth != 1 && depth != 8 && depth != 16 && depth != 32 {
		return regionSource{}, fileError(ErrUnsupported, "pixels.depth", path, offset, "unsupported sample depth")
	}
	intLimit := uint64(^uint(0) >> 1)
	if width > intLimit || height > intLimit || depth != 1 && width > intLimit/uint64(depth/8) {
		return regionSource{}, fileError(ErrLimit, "pixels.region", path, offset,
			"source row dimensions exceed platform integer range")
	}
	rowBytes := width/8 + (width%8+7)/8
	if depth != 1 {
		rowBytes = width * uint64(depth/8)
	}
	if height != 0 && rowBytes > math.MaxInt64/height {
		return regionSource{}, fileError(ErrLimit, "pixels.region", path, offset, "source sample length overflows")
	}
	return regionSource{width: int(width), height: int(height), rowBytes: int(rowBytes),
		length: int64(rowBytes * height), depth: depth}, nil
}

type regionDecode struct {
	source                 regionSource
	shape                  decodeShape
	rectangle              image.Rectangle
	windowStart, windowLen int
	streamBuffer           int
	maxRLEWorkBytes        int64
}

func (d *Document) regionOptions(options []RegionOptions) (RegionOptions, error) {
	settings := RegionOptions{MaxWorkingBytes: d.limits.MaxDecodeBytes,
		MaxInflatedBytes: d.limits.MaxDecodeBytes, MaxRLEWorkBytes: 256 << 20}
	if len(options) > 1 {
		return RegionOptions{}, fileError(ErrMalformed, "pixels.region", "region.options", 0,
			"provide at most one RegionOptions")
	}
	if len(options) == 0 {
		return settings, nil
	}
	if options[0].MaxWorkingBytes < 0 || options[0].MaxInflatedBytes < 0 || options[0].MaxRLEWorkBytes < 0 {
		return RegionOptions{}, fileError(ErrLimit, "pixels.region", "region.options", 0,
			"region work limits must not be negative")
	}
	if options[0].MaxWorkingBytes > d.limits.MaxDecodeBytes {
		return RegionOptions{}, fileError(ErrLimit, "pixels.region", "region.options", 0,
			"MaxWorkingBytes may only lower the source MaxDecodeBytes")
	}
	if options[0].MaxWorkingBytes != 0 {
		settings.MaxWorkingBytes = options[0].MaxWorkingBytes
	}
	if options[0].MaxInflatedBytes != 0 {
		settings.MaxInflatedBytes = options[0].MaxInflatedBytes
	}
	if options[0].MaxRLEWorkBytes != 0 {
		settings.MaxRLEWorkBytes = options[0].MaxRLEWorkBytes
	}
	return settings, nil
}

func (d *Document) decodeRegion(
	ctx context.Context, source regionSource, span Span, compression uint16, countWidth uint8,
	channels, channelIndex int, id int16, rectangle image.Rectangle, path string, options []RegionOptions,
) (*Plane, error) {
	settings, err := d.regionOptions(options)
	if err != nil {
		return nil, err
	}
	if !d.validSpan(span) {
		return nil, fileError(ErrMalformed, "pixels.region", path, span.Offset, "channel span is outside the source")
	}
	if rectangle.Min.X < 0 || rectangle.Min.Y < 0 || rectangle.Max.X < rectangle.Min.X ||
		rectangle.Max.Y < rectangle.Min.Y || rectangle.Max.X > source.width || rectangle.Max.Y > source.height {
		return nil, fileError(ErrMalformed, "pixels.region", path, span.Offset,
			"rectangle must lie inside the channel-local bounds")
	}
	shape, err := d.decodeDimensions(uint64(rectangle.Dx()), uint64(rectangle.Dy()), source.depth, path, span.Offset)
	if err != nil {
		return nil, err
	}
	if channels <= 0 || source.length != 0 && int64(channels) > math.MaxInt64/source.length {
		return nil, fileError(ErrLimit, "pixels.region", path, span.Offset, "source channel length overflows")
	}
	if compression > 3 || compression == 3 && source.depth == 1 {
		return nil, fileError(ErrUnsupported, "pixels.compression", path, span.Offset,
			"unsupported region compression or prediction depth")
	}
	if compression == 1 && countWidth != 2 && countWidth != 4 {
		return nil, fileError(ErrMalformed, "pixels.rle", path, span.Offset, "invalid RLE row-count width")
	}
	region := regionDecode{source: source, shape: shape, rectangle: rectangle,
		maxRLEWorkBytes: settings.MaxRLEWorkBytes}
	if source.depth == 1 {
		region.windowStart = rectangle.Min.X / 8
		region.windowLen = rectangle.Max.X/8 + (rectangle.Max.X%8+7)/8 - region.windowStart
	} else {
		region.windowStart = rectangle.Min.X * int(source.depth/8)
		region.windowLen = shape.rowBytes
	}
	if shape.length == 0 {
		return shape.plane(id, make([]byte, 0)), nil
	}
	if compression >= 2 && source.length*int64(channels) > settings.MaxInflatedBytes {
		return nil, fileError(ErrLimit, "pixels.region", path, span.Offset,
			"complete ZIP output exceeds MaxInflatedBytes; raise the work limit explicitly")
	}
	if compression == 1 {
		if int64(source.height) > span.Length/int64(countWidth)/int64(channels) {
			return nil, fileError(ErrMalformed, "pixels.rle", path, span.Offset, "RLE row-count table is truncated")
		}
		tableLength := int64(source.height) * int64(channels) * int64(countWidth)
		if tableLength > settings.MaxRLEWorkBytes {
			return nil, fileError(ErrLimit, "pixels.region", path, span.Offset,
				"RLE row-count table exceeds MaxRLEWorkBytes")
		}
	}
	if int64(shape.length) > settings.MaxWorkingBytes {
		return nil, fileError(ErrLimit, "pixels.region", path, span.Offset,
			"region output exceeds MaxWorkingBytes")
	}
	remaining := settings.MaxWorkingBytes - int64(shape.length)
	reserve := func(size int64) error {
		if size < 0 || size > remaining {
			return fileError(ErrLimit, "pixels.region", path, span.Offset,
				"region output and decoder scratch exceed MaxDecodeBytes")
		}
		remaining -= size
		return nil
	}
	if compression < 2 && rectangle.Min.X%8 != 0 && source.depth == 1 {
		if err := reserve(int64(region.windowLen)); err != nil {
			return nil, err
		}
	}
	if compression == 1 {
		// Retain only the requested row counts; the complete count table is scanned without storing it.
		if err := reserve(4); err != nil {
			return nil, err
		}
		if int64(shape.height) > remaining/4 {
			return nil, fileError(ErrLimit, "pixels.region", path, span.Offset,
				"requested RLE row table exceeds MaxDecodeBytes")
		}
		if err := reserve(int64(shape.height) * 4); err != nil {
			return nil, err
		}
	}
	if compression >= 2 {
		if err := reserve(int64(source.rowBytes)); err != nil {
			return nil, err
		}
		if compression == 3 && source.depth == 32 {
			if err := reserve(int64(source.rowBytes)); err != nil {
				return nil, err
			}
		}
		region.streamBuffer = int(min(int64(4096), max(int64(16), span.Length)))
		if err := reserve(int64(region.streamBuffer)); err != nil {
			return nil, err
		}
	}
	data := make([]byte, shape.length)
	switch compression {
	case 0:
		err = d.decodeRegionRaw(ctx, span, region, channels, channelIndex, data, path)
	case 1:
		err = d.decodeRegionRLE(ctx, span, region, countWidth, channels, channelIndex, data, path)
	case 2, 3:
		err = d.decodeRegionZIP(ctx, span, region, channels, channelIndex, compression == 3, data, path)
	}
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return shape.plane(id, data), nil
}

func (d *Document) decodeRegionRaw(
	ctx context.Context, span Span, region regionDecode, channels, channelIndex int, data []byte, path string,
) error {
	expected := region.source.length * int64(channels)
	if expected != span.Length && (expected%2 != 1 || span.Length-expected != 1) {
		return fileError(ErrMalformed, "pixels.raw", path, span.Offset, "raw sample length does not match dimensions")
	}
	var window []byte
	if region.source.depth == 1 && region.rectangle.Min.X%8 != 0 {
		window = make([]byte, region.windowLen)
	}
	for row := range region.shape.height {
		output := data[row*region.shape.rowBytes : (row+1)*region.shape.rowBytes]
		rowData := output
		if window != nil {
			rowData = window
		}
		offset := span.Offset + int64(channelIndex)*region.source.length +
			int64(region.rectangle.Min.Y+row)*int64(region.source.rowBytes) + int64(region.windowStart)
		cursor := d.regionCursor(ctx, Span{Offset: offset, Length: int64(region.windowLen)})
		if _, err := io.ReadFull(cursor, rowData); err != nil {
			return decodeReadError(err, "pixels.raw", path, cursor.position(), "raw region could not be read")
		}
		region.copyWindow(output, rowData)
	}
	return nil
}

func (d *Document) regionCursor(ctx context.Context, span Span) *decodeCursor {
	reader := regionReaderAt{reader: checkedReaderAt{reader: d.reader}}
	return &decodeCursor{ctx: ctx, reader: io.NewSectionReader(reader, span.Offset, span.Length), span: span}
}

type regionReaderAt struct {
	reader io.ReaderAt
}

func (reader regionReaderAt) ReadAt(data []byte, offset int64) (int, error) {
	count, err := reader.reader.ReadAt(data, offset)
	if count == len(data) && err != nil {
		if errors.Is(err, io.EOF) {
			return count, nil
		}
		// io.ReadFull otherwise discards an error returned with a complete buffer.
		return 0, err
	}
	return count, err
}

func (region regionDecode) copyWindow(output, window []byte) {
	if region.source.depth != 1 {
		copy(output, window)
		return
	}
	shift := region.rectangle.Min.X % 8
	if shift == 0 {
		copy(output, window)
	} else {
		for index := range output {
			output[index] = window[index] << shift
			if index+1 < len(window) {
				output[index] |= window[index+1] >> (8 - shift)
			}
		}
	}
	if remainder := region.shape.width % 8; remainder != 0 {
		output[len(output)-1] &= 255 << (8 - remainder)
	}
}
