package psdfile

import (
	"compress/zlib"
	"context"
	"encoding/binary"
	"fmt"
	"io"
)

// EncoderOptions configures bounded source-precision encoding.
// Limits.MaxDecodePixels bounds each plane and Limits.MaxDecodeBytes bounds all source samples.
// MaxEncodedBytes bounds the returned payload, including its compression word and RLE table.
// Zero values select DefaultLimits; encoding does not retain or mutate source samples.
type EncoderOptions struct {
	Limits          Limits
	MaxEncodedBytes int64
}

// EncodePlane encodes one channel, including its compression word, with default allocation limits.
// Version is 1 for PSD or 2 for PSB; compression is Raw (0), RLE (1), ZIP (2), or ZIP prediction (3).
func EncodePlane(ctx context.Context, plane Plane, compression, version uint16) ([]byte, error) {
	return EncodePlaneWithOptions(ctx, plane, compression, version, EncoderOptions{})
}

// EncodePlaneWithOptions encodes one channel with explicit sample and output budgets.
// Empty layer or mask channels are accepted; packed 1-bit row padding is preserved.
func EncodePlaneWithOptions(
	ctx context.Context, plane Plane, compression, version uint16, options EncoderOptions,
) ([]byte, error) {
	ctx = decodeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limits, outputLimit, err := encodeLimits(options)
	if err != nil {
		return nil, err
	}
	if err := encodeMethod(compression, version, plane.Depth, "plane"); err != nil {
		return nil, err
	}
	if _, err := encodePlaneShape(plane, limits, "plane"); err != nil {
		return nil, err
	}
	if plane.ID < -3 || plane.ID >= 56 {
		return nil, fileError(ErrMalformed, "encode.channel", "plane.ID", 0, "channel ID is out of range")
	}
	return encodePlanes(ctx, []Plane{plane}, compression, version, outputLimit, "plane")
}

// EncodeComposite encodes planar saved-image samples with default allocation limits.
// RLE has one shared row-count table and ZIP has one shared zlib stream for all channels.
func EncodeComposite(ctx context.Context, pixels CompositePixels, compression, version uint16) ([]byte, error) {
	return EncodeCompositeWithOptions(ctx, pixels, compression, version, EncoderOptions{})
}

// EncodeCompositeWithOptions encodes source-precision saved-image channels with explicit budgets.
// Channel IDs must match their planar positions, with equal dimensions, depth, and row bytes.
func EncodeCompositeWithOptions(
	ctx context.Context, pixels CompositePixels, compression, version uint16, options EncoderOptions,
) ([]byte, error) {
	ctx = decodeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limits, outputLimit, err := encodeLimits(options)
	if err != nil {
		return nil, err
	}
	if err := encodeMethod(compression, version, pixels.Depth, "composite"); err != nil {
		return nil, err
	}
	if len(pixels.Channels) == 0 || len(pixels.Channels) > 56 {
		return nil, fileError(ErrMalformed, "encode.channel", "composite.channels", 0, "expected 1 to 56 channels")
	}
	dimensionLimit := 30_000
	if version == 2 {
		dimensionLimit = 300_000
	}
	if pixels.Width <= 0 || pixels.Height <= 0 {
		return nil, fileError(ErrMalformed, "encode.shape", "composite", 0, "saved image dimensions must be positive")
	}
	if pixels.Width > dimensionLimit || pixels.Height > dimensionLimit {
		return nil, fileError(ErrLimit, "encode.shape", "composite", 0, "dimensions exceed the format limit")
	}
	var total int64
	for index, plane := range pixels.Channels {
		path := fmt.Sprintf("composite.channels[%d]", index)
		if plane.ID != int16(index) {
			return nil, fileError(ErrMalformed, "encode.channel", path, 0, "channel ID does not match planar position")
		}
		if plane.Width != pixels.Width || plane.Height != pixels.Height || plane.Depth != pixels.Depth {
			return nil, fileError(ErrMalformed, "encode.shape", path, 0, "channel shape differs from the saved image")
		}
		shape, err := encodePlaneShape(plane, limits, path)
		if err != nil {
			return nil, err
		}
		if int64(shape.length) > limits.MaxDecodeBytes-total {
			return nil, fileError(ErrLimit, "encode.samples", path, 0, "image channels exceed MaxDecodeBytes")
		}
		total += int64(shape.length)
	}
	return encodePlanes(ctx, pixels.Channels, compression, version, outputLimit, "composite")
}

func encodeLimits(options EncoderOptions) (Limits, int64, error) {
	limits, err := loadLimits([]LoadOptions{{Limits: options.Limits}})
	if err != nil {
		return Limits{}, 0, err
	}
	if options.MaxEncodedBytes < 0 {
		return Limits{}, 0, fileError(ErrLimit, "encode.options", "options.MaxEncodedBytes", 0, "limit must not be negative")
	}
	outputLimit := options.MaxEncodedBytes
	if outputLimit == 0 {
		outputLimit = DefaultLimits().MaxDecodeBytes
	}
	outputLimit = min(outputLimit, int64(^uint(0)>>1))
	return limits, outputLimit, nil
}

func encodeMethod(compression, version, depth uint16, path string) error {
	if version != 1 && version != 2 {
		return fileError(ErrUnsupported, "encode.version", path, 0, "version must be 1 (PSD) or 2 (PSB)")
	}
	if compression > 3 {
		return fileError(ErrUnsupported, "encode.compression", path, 0, "unknown compression method")
	}
	if compression == 3 && depth == 1 {
		return fileError(ErrUnsupported, "encode.prediction", path, 0, "prediction requires 8/16/32-bit samples")
	}
	return nil
}

func encodePlaneShape(plane Plane, limits Limits, path string) (decodeShape, error) {
	if plane.Width < 0 || plane.Height < 0 || plane.RowBytes < 0 {
		return decodeShape{}, fileError(ErrMalformed, "encode.shape", path, 0, "negative channel dimensions or row size")
	}
	document := Document{limits: limits}
	shape, err := document.decodeDimensions(uint64(plane.Width), uint64(plane.Height), plane.Depth, path, 0)
	if err != nil {
		return decodeShape{}, err
	}
	if shape.rowBytes != plane.RowBytes || shape.length != len(plane.Data) {
		return decodeShape{}, fileError(
			ErrMalformed, "encode.shape", path, 0, "row size or sample length differs from dimensions",
		)
	}
	return shape, nil
}

// encodeOutput owns a buffer whose capacity never exceeds its explicit output budget.
type encodeOutput struct {
	ctx   context.Context
	data  []byte
	limit int64
	path  string
}

func (output *encodeOutput) extend(length int) error {
	if err := output.ctx.Err(); err != nil {
		return err
	}
	if length < 0 || int64(length) > output.limit-int64(len(output.data)) {
		return fileError(ErrLimit, "encode.output", output.path, 0, "payload exceeds MaxEncodedBytes")
	}
	needed := len(output.data) + length
	if needed > cap(output.data) {
		current := int64(cap(output.data))
		growth := current + min(current, output.limit-current)
		capacity := max(int64(needed), min(max(growth, 4096), output.limit))
		data := make([]byte, len(output.data), int(capacity))
		copy(data, output.data)
		output.data = data
	}
	output.data = output.data[:needed]
	return nil
}

func (output *encodeOutput) Write(data []byte) (int, error) {
	start := len(output.data)
	if err := output.extend(len(data)); err != nil {
		return 0, err
	}
	copy(output.data[start:], data)
	return len(data), nil
}

func encodePlanes(
	ctx context.Context, planes []Plane, compression, version uint16, limit int64, path string,
) ([]byte, error) {
	output := &encodeOutput{ctx: ctx, limit: limit, path: path}
	if err := output.extend(2); err != nil {
		return nil, err
	}
	binary.BigEndian.PutUint16(output.data, compression)
	var err error
	switch compression {
	case 0:
		for _, plane := range planes {
			if err = encodeWriteChunks(ctx, output, plane.Data); err != nil {
				return nil, err
			}
		}
	case 1:
		err = encodeRLE(ctx, output, planes, version)
	case 2, 3:
		err = encodeZIP(ctx, output, planes, compression == 3)
	}
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return output.data, nil
}

func encodeWriteChunks(ctx context.Context, writer io.Writer, data []byte) error {
	for len(data) != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		length := min(len(data), 32<<10)
		count, err := writer.Write(data[:length])
		if err != nil {
			return err
		}
		if count != length {
			return io.ErrShortWrite
		}
		data = data[length:]
	}
	return nil
}

func encodeRLE(ctx context.Context, output *encodeOutput, planes []Plane, version uint16) error {
	countWidth := 2
	rowLimit := uint64(0xffff)
	if version == 2 {
		countWidth = 4
		rowLimit = 0xffffffff
	}
	var rows uint64
	for _, plane := range planes {
		if uint64(plane.Height) > uint64(output.limit)/uint64(countWidth)-rows {
			return fileError(ErrLimit, "encode.rle", output.path, 0, "RLE table exceeds MaxEncodedBytes")
		}
		rows += uint64(plane.Height)
	}
	if err := output.extend(int(rows * uint64(countWidth))); err != nil {
		return err
	}
	tableOffset := 2
	for _, plane := range planes {
		for row := range plane.Height {
			if err := ctx.Err(); err != nil {
				return err
			}
			start := len(output.data)
			data := plane.Data[row*plane.RowBytes : (row+1)*plane.RowBytes]
			if err := encodePackBits(ctx, output, data); err != nil {
				return err
			}
			length := uint64(len(output.data) - start)
			if length > rowLimit {
				return fileError(ErrLimit, "encode.rle", output.path, 0, "compressed row exceeds its format count width")
			}
			if countWidth == 2 {
				binary.BigEndian.PutUint16(output.data[tableOffset:], uint16(length))
			} else {
				binary.BigEndian.PutUint32(output.data[tableOffset:], uint32(length))
			}
			tableOffset += countWidth
		}
	}
	return nil
}

func encodePackBits(ctx context.Context, output *encodeOutput, data []byte) error {
	for position := 0; position < len(data); {
		if err := ctx.Err(); err != nil {
			return err
		}
		run := encodeRepeatLength(data[position:])
		if run >= 3 {
			packet := [2]byte{byte(1 - run), data[position]}
			if _, err := output.Write(packet[:]); err != nil {
				return err
			}
			position += run
			continue
		}
		start := position
		position += run
		for position < len(data) && position-start < 128 {
			run = encodeRepeatLength(data[position:])
			if run >= 3 {
				break
			}
			position += min(run, 128-(position-start))
		}
		control := [1]byte{byte(position - start - 1)}
		if _, err := output.Write(control[:]); err != nil {
			return err
		}
		if _, err := output.Write(data[start:position]); err != nil {
			return err
		}
	}
	return nil
}

func encodeRepeatLength(data []byte) int {
	length := 1
	for length < min(len(data), 128) && data[length] == data[0] {
		length++
	}
	return length
}

func encodeZIP(ctx context.Context, output *encodeOutput, planes []Plane, prediction bool) error {
	writer := zlib.NewWriter(output)
	var scratch []byte
	if prediction && len(planes[0].Data) != 0 {
		scratch = make([]byte, planes[0].RowBytes)
	}
	for _, plane := range planes {
		if plane.RowBytes == 0 {
			continue
		}
		for row := range plane.Height {
			if err := ctx.Err(); err != nil {
				return err
			}
			data := plane.Data[row*plane.RowBytes : (row+1)*plane.RowBytes]
			if prediction {
				if err := encodePredictionRow(ctx, data, scratch, plane.Depth); err != nil {
					return err
				}
				data = scratch
			}
			if err := encodeWriteChunks(ctx, writer, data); err != nil {
				return err
			}
		}
	}
	return writer.Close()
}

func encodePredictionRow(ctx context.Context, data, scratch []byte, depth uint16) error {
	if depth == 16 {
		var previous uint16
		for index := 0; index < len(data); index += 2 {
			if index%(32<<10) == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			value := binary.BigEndian.Uint16(data[index:])
			binary.BigEndian.PutUint16(scratch[index:], value-previous)
			previous = value
		}
		return nil
	}
	var previous byte
	width := len(data) / 4
	for index := range data {
		if index%(32<<10) == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		value := data[index]
		if depth == 32 {
			value = data[4*(index%width)+index/width]
		}
		scratch[index] = value - previous
		previous = value
	}
	return nil
}
