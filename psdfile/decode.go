package psdfile

import (
	"bufio"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
)

// DecodeChannel decodes one layer channel into owned samples at its original bit depth.
func (d *Document) DecodeChannel(ctx context.Context, layerIndex, channelIndex int) (*Plane, error) {
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
	path := fmt.Sprintf("layers[%d].channels[%d]", layerIndex, channelIndex)
	if channelIndex < 0 || channelIndex >= len(layer.Channels) {
		return nil, fileError(ErrMalformed, "pixels", path, layer.Span.Offset, "channel index is out of range")
	}
	channel := layer.Channels[channelIndex]
	shape, err := d.decodeBounds(channel.Bounds, d.decodeLayerDepth(), path, channel.Data.Offset)
	if err != nil {
		return nil, err
	}
	data, err := d.decodeLayerChannel(ctx, channel, shape, path)
	if err != nil {
		return nil, err
	}
	return shape.plane(channel.ID, data), nil
}

// DecodeLayer decodes all source channel planes, including masks, without color conversion.
func (d *Document) DecodeLayer(ctx context.Context, layerIndex int) (*LayerPixels, error) {
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
	shapes := make([]decodeShape, len(layer.Channels))
	var total int64
	for index, channel := range layer.Channels {
		path := fmt.Sprintf("layers[%d].channels[%d]", layerIndex, index)
		shape, err := d.decodeBounds(channel.Bounds, d.decodeLayerDepth(), path, channel.Data.Offset)
		if err != nil {
			return nil, err
		}
		if int64(shape.length) > d.limits.MaxDecodeBytes-total {
			return nil, fileError(ErrLimit, "pixels", path, channel.Data.Offset, "layer exceeds MaxDecodeBytes")
		}
		total += int64(shape.length)
		shapes[index] = shape
	}
	pixels := &LayerPixels{Bounds: layer.Bounds, Channels: make([]Plane, len(layer.Channels))}
	for index, channel := range layer.Channels {
		path := fmt.Sprintf("layers[%d].channels[%d]", layerIndex, index)
		data, err := d.decodeLayerChannel(ctx, channel, shapes[index], path)
		if err != nil {
			return nil, err
		}
		pixels.Channels[index] = *shapes[index].plane(channel.ID, data)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return pixels, nil
}

// DecodeComposite decodes the saved image into owned source-precision channel planes.
func (d *Document) DecodeComposite(ctx context.Context) (*CompositePixels, error) {
	ctx = decodeContext(ctx)
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	shape, err := d.decodeDimensions(
		uint64(d.Header.Width), uint64(d.Header.Height), d.Header.Depth, "composite", d.Composite.Data.Offset,
	)
	if err != nil {
		return nil, err
	}
	channels := int(d.Header.Channels)
	if err := d.decodeTotal(shape, channels, "composite", d.Composite.Data.Offset); err != nil {
		return nil, err
	}
	outputs := make([][]byte, channels)
	for index := range outputs {
		outputs[index] = make([]byte, shape.length)
	}
	if err := d.decodeCompositeData(ctx, shape, outputs); err != nil {
		return nil, err
	}
	pixels := &CompositePixels{
		Width: shape.width, Height: shape.height, Depth: shape.depth, ColorMode: d.Header.ColorMode,
		Channels: make([]Plane, channels),
	}
	for index, data := range outputs {
		pixels.Channels[index] = *shape.plane(int16(index), data)
	}
	return pixels, nil
}

// DecodeCompositeChannel decodes one saved-image channel, without retaining other channel samples.
// ZIP streams are validated in full; MaxDecodeBytes also bounds their complete decompressed size.
func (d *Document) DecodeCompositeChannel(ctx context.Context, channelIndex int) (*Plane, error) {
	ctx = decodeContext(ctx)
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := fmt.Sprintf("composite.channels[%d]", channelIndex)
	channels := int(d.Header.Channels)
	if channelIndex < 0 || channelIndex >= channels {
		return nil, fileError(ErrMalformed, "pixels", path, d.Composite.Data.Offset, "channel index is out of range")
	}
	shape, err := d.decodeDimensions(
		uint64(d.Header.Width), uint64(d.Header.Height), d.Header.Depth, path, d.Composite.Data.Offset,
	)
	if err != nil {
		return nil, err
	}
	if d.Composite.Compression == 2 || d.Composite.Compression == 3 {
		if err := d.decodeTotal(shape, channels, path, d.Composite.Data.Offset); err != nil {
			return nil, err
		}
	}
	outputs := make([][]byte, channels)
	outputs[channelIndex] = make([]byte, shape.length)
	if err := d.decodeCompositeData(ctx, shape, outputs); err != nil {
		return nil, err
	}
	return shape.plane(int16(channelIndex), outputs[channelIndex]), nil
}

type decodeShape struct {
	width, height, rowBytes, length int
	depth                           uint16
}

func (s decodeShape) plane(id int16, data []byte) *Plane {
	return &Plane{ID: id, Width: s.width, Height: s.height, Depth: s.depth, RowBytes: s.rowBytes, Data: data}
}

func decodeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (d *Document) decodeLayerRecord(index int) (LayerRecord, error) {
	if index < 0 || index >= len(d.Layers) {
		return LayerRecord{}, fileError(
			ErrMalformed, "pixels", fmt.Sprintf("layers[%d]", index), d.LayerInfo.Offset, "layer index is out of range",
		)
	}
	return d.Layers[index], nil
}

func (d *Document) decodeLayerDepth() uint16 {
	if d.ActiveLayerSet >= 0 && d.ActiveLayerSet < len(d.LayerSets) {
		return d.LayerSets[d.ActiveLayerSet].Depth
	}
	return d.Header.Depth
}

func (d *Document) decodeBounds(bounds image.Rectangle, depth uint16, path string, offset int64) (decodeShape, error) {
	if bounds.Max.X < bounds.Min.X || bounds.Max.Y < bounds.Min.Y {
		return decodeShape{}, fileError(ErrMalformed, "pixels", path, offset, "negative channel dimensions")
	}
	width := uint64(bounds.Max.X) - uint64(bounds.Min.X)
	height := uint64(bounds.Max.Y) - uint64(bounds.Min.Y)
	return d.decodeDimensions(width, height, depth, path, offset)
}

func (d *Document) decodeDimensions(
	width, height uint64, depth uint16, path string, offset int64,
) (decodeShape, error) {
	if depth != 1 && depth != 8 && depth != 16 && depth != 32 {
		return decodeShape{}, fileError(ErrUnsupported, "pixels.depth", path, offset, "unsupported sample depth")
	}
	intLimit := uint64(^uint(0) >> 1)
	if width > intLimit || height > intLimit {
		return decodeShape{}, fileError(ErrLimit, "pixels", path, offset, "dimensions exceed platform integer range")
	}
	pixelLimit := uint64(d.limits.MaxDecodePixels)
	if height != 0 && width > pixelLimit/height {
		return decodeShape{}, fileError(ErrLimit, "pixels", path, offset, "plane exceeds MaxDecodePixels")
	}
	var rowBytes uint64
	if depth == 1 {
		rowBytes = width/8 + (width%8+7)/8
	} else {
		bytesPerSample := uint64(depth / 8)
		if width > intLimit/bytesPerSample {
			return decodeShape{}, fileError(ErrLimit, "pixels", path, offset, "row size exceeds platform integer range")
		}
		rowBytes = width * bytesPerSample
	}
	byteLimit := min(uint64(d.limits.MaxDecodeBytes), intLimit)
	if rowBytes > byteLimit || height != 0 && rowBytes > byteLimit/height {
		return decodeShape{}, fileError(ErrLimit, "pixels", path, offset, "plane exceeds MaxDecodeBytes")
	}
	return decodeShape{
		width: int(width), height: int(height), rowBytes: int(rowBytes), length: int(rowBytes * height), depth: depth,
	}, nil
}

func (d *Document) decodeTotal(shape decodeShape, channels int, path string, offset int64) error {
	if channels <= 0 {
		return fileError(ErrMalformed, "pixels", path, offset, "missing image channels")
	}
	if int64(shape.length) > d.limits.MaxDecodeBytes/int64(channels) {
		return fileError(ErrLimit, "pixels", path, offset, "image channels exceed MaxDecodeBytes")
	}
	return nil
}

func (d *Document) decodeLayerChannel(
	ctx context.Context, channel ChannelRecord, shape decodeShape, path string,
) ([]byte, error) {
	if !d.validSpan(channel.Data) {
		return nil, fileError(ErrMalformed, "pixels", path, channel.Data.Offset, "channel span is outside the source")
	}
	data := make([]byte, shape.length)
	outputs := [][]byte{data}
	var err error
	switch channel.Compression {
	case 0:
		err = d.decodeRaw(ctx, channel.Data, shape, outputs, path)
	case 1:
		err = d.decodeRLE(ctx, channel.Data, shape, outputs, d.decodeRowCountWidth(), path)
	case 2, 3:
		err = d.decodeZIP(ctx, channel.Data, shape, outputs, channel.Compression == 3, path)
	default:
		err = fileError(ErrUnsupported, "pixels.compression", path, channel.Data.Offset, "unknown compression method")
	}
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func (d *Document) decodeCompositeData(ctx context.Context, shape decodeShape, outputs [][]byte) error {
	span := d.Composite.Data
	if !d.validSpan(span) {
		return fileError(ErrMalformed, "pixels", "composite", span.Offset, "image span is outside the source")
	}
	var err error
	switch d.Composite.Compression {
	case 0:
		err = d.decodeRaw(ctx, span, shape, outputs, "composite")
	case 1:
		countWidth := d.Composite.RowCountWidth
		if countWidth == 0 {
			countWidth = d.decodeRowCountWidth()
		}
		err = d.decodeRLE(ctx, span, shape, outputs, countWidth, "composite")
	case 2, 3:
		err = d.decodeZIP(ctx, span, shape, outputs, d.Composite.Compression == 3, "composite")
	default:
		err = fileError(ErrUnsupported, "pixels.compression", "composite", span.Offset, "unknown compression method")
	}
	if err != nil {
		return err
	}
	return ctx.Err()
}

func (d *Document) decodeRowCountWidth() uint8 {
	if d.Header.Version == 2 {
		return 4
	}
	return 2
}

func (d *Document) decodeRaw(
	ctx context.Context, span Span, shape decodeShape, outputs [][]byte, path string,
) error {
	if shape.length != 0 && int64(len(outputs)) > span.Length/int64(shape.length) {
		return fileError(ErrMalformed, "pixels.raw", path, span.Offset, "raw samples are truncated")
	}
	expected := int64(shape.length) * int64(len(outputs))
	if expected != span.Length && (expected%2 != 1 || span.Length-expected != 1) {
		return fileError(ErrMalformed, "pixels.raw", path, span.Offset, "raw sample length does not match dimensions")
	}
	if shape.length == 0 {
		return nil
	}
	for channel, data := range outputs {
		if data == nil {
			continue
		}
		start := span.Offset + int64(channel)*int64(shape.length)
		cursor := d.decodeCursor(ctx, Span{Offset: start, Length: int64(shape.length)})
		for row := range shape.height {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := io.ReadFull(cursor, data[row*shape.rowBytes:(row+1)*shape.rowBytes]); err != nil {
				return decodeReadError(err, "pixels.raw", path, cursor.position(), "raw samples could not be read")
			}
		}
	}
	return nil
}

// decodeCursor implements ByteReader so zlib cannot consume unvalidated trailing source bytes.
type decodeCursor struct {
	ctx    context.Context
	reader *io.SectionReader
	span   Span
	read   int64
}

func (d *Document) decodeCursor(ctx context.Context, span Span) *decodeCursor {
	return &decodeCursor{ctx: ctx, reader: io.NewSectionReader(d.reader, span.Offset, span.Length), span: span}
}

func (r *decodeCursor) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	count, err := r.reader.Read(data)
	r.read += int64(count)
	return count, err
}

func (r *decodeCursor) ReadByte() (byte, error) {
	var data [1]byte
	_, err := io.ReadFull(r, data[:])
	return data[0], err
}

func (r *decodeCursor) position() int64 { return r.span.Offset + r.read }

func decodeReadError(err error, feature, path string, offset int64, detail string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &Error{Kind: ErrMalformed, Feature: feature, Path: path, Offset: offset, Detail: detail, Cause: err}
}

func (d *Document) decodeZIP(
	ctx context.Context, span Span, shape decodeShape, outputs [][]byte, prediction bool, path string,
) error {
	if prediction && shape.depth == 1 {
		return fileError(ErrUnsupported, "pixels.prediction", path, span.Offset, "prediction requires 8/16/32-bit samples")
	}
	cursor := d.decodeCursor(ctx, span)
	buffered := bufio.NewReaderSize(cursor, 32<<10)
	reader, err := zlib.NewReader(buffered)
	if err != nil {
		return decodeReadError(err, "pixels.zip", path, cursor.position(), "invalid zlib stream header")
	}
	defer func() { _ = reader.Close() }()
	var scratch []byte
	for _, data := range outputs {
		if data == nil && scratch == nil {
			scratch = make([]byte, shape.rowBytes)
		}
	}
	var shuffle []byte
	if prediction && shape.depth == 32 {
		shuffle = make([]byte, shape.rowBytes)
	}
	for _, data := range outputs {
		if shape.rowBytes == 0 {
			continue
		}
		for row := range shape.height {
			if err := ctx.Err(); err != nil {
				return err
			}
			rowData := scratch
			if data != nil {
				rowData = data[row*shape.rowBytes : (row+1)*shape.rowBytes]
			}
			if _, err := io.ReadFull(reader, rowData); err != nil {
				return decodeReadError(err, "pixels.zip", path, cursor.position(), "compressed samples are truncated")
			}
			if prediction && data != nil {
				decodePredictionRow(rowData, shuffle, shape.depth)
			}
		}
	}
	var extra [1]byte
	count, err := io.ReadFull(reader, extra[:])
	if count != 0 {
		return fileError(ErrMalformed, "pixels.zip", path, cursor.position(), "zlib output exceeds sample dimensions")
	}
	if !errors.Is(err, io.EOF) {
		return decodeReadError(err, "pixels.zip", path, cursor.position(), "zlib checksum or stream terminator is invalid")
	}
	consumed := cursor.read - int64(buffered.Buffered())
	if consumed != span.Length {
		return fileError(ErrMalformed, "pixels.zip", path, span.Offset+consumed, "extra bytes follow the zlib stream")
	}
	return nil
}

func decodePredictionRow(data, shuffle []byte, depth uint16) {
	// PSD delta prediction operates on big-endian uint16 samples, but shuffled bytes for depth 32.
	// See psd-tools' compression.decode_prediction and Adobe's ZIP-with-prediction compression value.
	if depth == 16 {
		var previous uint16
		for index := 0; index < len(data); index += 2 {
			previous += binary.BigEndian.Uint16(data[index : index+2])
			binary.BigEndian.PutUint16(data[index:index+2], previous)
		}
		return
	}
	for index := 1; index < len(data); index++ {
		data[index] += data[index-1]
	}
	if depth != 32 {
		return
	}
	width := len(data) / 4
	for pixel := range width {
		for plane := range 4 {
			shuffle[4*pixel+plane] = data[plane*width+pixel]
		}
	}
	copy(data, shuffle)
}
