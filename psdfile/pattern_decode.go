package psdfile

import (
	"context"
	"errors"
	"image"
	"io"
	"unsafe"
)

// PatternPlane owns one decoded array's original bit-depth samples and channel ordinal.
type PatternPlane struct {
	ArrayIndex int
	Bounds     image.Rectangle
	Plane      Plane
}

// PatternPixels owns decoded tile planes without converting source color mode or sample depth.
type PatternPixels struct {
	ColorMode uint32
	Bounds    image.Rectangle
	Channels  []PatternPlane
}

// PatternDecodeOptions lowers the source pixel and aggregate working allocation budgets.
// Zero uses source limits; larger values never raise the corresponding source limit.
type PatternDecodeOptions struct {
	MaxWorkingBytes, MaxPixels int64
}

// DecodePattern decodes every written virtual-memory array into owned original-depth samples.
// RGB, mask and other arrays retain their ordinals; unknown versions, flags and compression are rejected.
func (d *Document) DecodePattern(
	ctx context.Context, record PatternRecord, options ...PatternDecodeOptions,
) (*PatternPixels, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limits, err := d.patternDecodeLimits(options)
	if err != nil {
		return nil, err
	}
	return d.decodePatternLocked(ctx, record, limits)
}

func (d *Document) patternDecodeLimits(options []PatternDecodeOptions) (Limits, error) {
	limits := d.limits
	if len(options) > 1 || len(options) == 1 && (options[0].MaxWorkingBytes < 0 || options[0].MaxPixels < 0) {
		return limits, fileError(ErrMalformed, "patterns", "patterns.options", 0, "invalid pattern decode options")
	}
	if len(options) == 1 {
		if options[0].MaxWorkingBytes != 0 {
			limits.MaxDecodeBytes = min(limits.MaxDecodeBytes, options[0].MaxWorkingBytes)
		}
		if options[0].MaxPixels != 0 {
			limits.MaxDecodePixels = min(limits.MaxDecodePixels, options[0].MaxPixels)
		}
	}
	return limits, nil
}

func (d *Document) decodePatternLocked(
	ctx context.Context, record PatternRecord, limits Limits,
) (*PatternPixels, error) {
	if len(record.Unsupported) != 0 || record.Version != 1 || record.VMVersion != 3 {
		return nil, fileError(ErrUnsupported, "patterns", "patterns.record", record.Body.Offset,
			"pattern record has unrecognized structural semantics")
	}
	if !d.validSpan(record.Body) || !d.validSpan(record.VMBody) ||
		uint64(len(record.Arrays)) != uint64(record.VMChannels)+2 || len(record.Arrays) > limits.MaxBlocks {
		return nil, fileError(ErrMalformed, "patterns", "patterns.record", record.Body.Offset,
			"pattern spans or virtual array count are inconsistent")
	}
	view := &Document{reader: strictPatternReaderAt{ctx: ctx, reader: d.reader}, size: d.size,
		limits: limits, Header: Header{Version: 1}}
	const streamScratch = int64(128 << 10)
	predictionScratch := int64(0)
	metadata := int64(len(record.Arrays))*int64(unsafe.Sizeof(PatternPlane{})+unsafe.Sizeof(decodeShape{})) + 512
	if metadata > limits.MaxDecodeBytes {
		return nil, fileError(ErrLimit, "patterns", "patterns.samples", record.Body.Offset,
			"decoded array index exceeds the aggregate working budget")
	}
	working := metadata
	shapes := make([]decodeShape, len(record.Arrays))
	for index, array := range record.Arrays {
		if array.Index != index || array.Written > 1 {
			return nil, fileError(ErrUnsupported, "patterns", "patterns.array", array.Span.Offset,
				"array ordinal or written flag is unrecognized")
		}
		if array.Written == 0 || array.DeclaredLength == 0 {
			continue
		}
		if array.Written != 1 || uint32(array.PixelDepth) != array.Depth || array.Compression > 3 {
			return nil, fileError(ErrUnsupported, "patterns", "patterns.array", array.Span.Offset,
				"array flags, repeated depth or compression are unsupported")
		}
		if !d.validSpan(array.Data) || !d.validSpan(array.Body) || array.Data.Offset < array.Body.Offset ||
			array.Data.Offset+array.Data.Length > array.Body.Offset+array.Body.Length {
			return nil, fileError(ErrMalformed, "patterns", "patterns.array", array.Span.Offset,
				"array payload lies outside the retained source body")
		}
		shape, err := view.decodeBounds(array.Bounds, array.PixelDepth, "patterns.array", array.Span.Offset)
		if err != nil {
			return nil, err
		}
		if int64(shape.length) > limits.MaxDecodeBytes-working {
			return nil, fileError(ErrLimit, "patterns", "patterns.samples", array.Span.Offset,
				"owned pattern planes exceed the aggregate decode budget")
		}
		working += int64(shape.length)
		if array.Compression == 3 && array.PixelDepth == 32 {
			predictionScratch = max(predictionScratch, int64(shape.rowBytes))
		}
		shapes[index] = shape
	}
	if working > limits.MaxDecodeBytes || streamScratch > limits.MaxDecodeBytes-working ||
		predictionScratch > limits.MaxDecodeBytes-working-streamScratch {
		return nil, fileError(ErrLimit, "patterns", "patterns.samples", record.Body.Offset,
			"pattern decoder scratch exceeds the aggregate working budget")
	}
	result := &PatternPixels{ColorMode: record.ColorMode, Bounds: record.VMBounds,
		Channels: make([]PatternPlane, 0, len(record.Arrays))}
	for index, array := range record.Arrays {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if array.Written == 0 || array.DeclaredLength == 0 {
			continue
		}
		data, err := view.decodeLayerChannel(ctx, ChannelRecord{Data: array.Data, Compression: uint16(array.Compression)},
			shapes[index], "patterns.array")
		if err != nil {
			return nil, patternReadError(ctx, err)
		}
		shape := shapes[index]
		result.Channels = append(result.Channels, PatternPlane{ArrayIndex: array.Index, Bounds: array.Bounds,
			Plane: Plane{Width: shape.width, Height: shape.height, RowBytes: shape.rowBytes, Depth: shape.depth, Data: data}})
	}
	return result, nil
}

// DecodePatternTileRGB8 returns a source encoded RGB8 NRGBA tile with exact preserved transparency.
// High-bit-depth and non-RGB samples remain available through DecodePattern and are not guessed into display RGB.
func (d *Document) DecodePatternTileRGB8(
	ctx context.Context, record PatternRecord, options ...PatternDecodeOptions,
) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if record.ColorMode != 3 || record.VMChannels < 3 || record.VMBounds.Empty() {
		return nil, fileError(ErrUnsupported, "patterns", "patterns.rgb", record.Body.Offset,
			"pattern does not identify a supported RGB tile")
	}
	limits, err := d.patternDecodeLimits(options)
	if err != nil {
		return nil, err
	}
	if _, err := d.estimatePatternTileRGB8(record, limits); err != nil {
		return nil, err
	}
	width := int64(record.VMBounds.Max.X) - int64(record.VMBounds.Min.X)
	height := int64(record.VMBounds.Max.Y) - int64(record.VMBounds.Min.Y)
	if width <= 0 || height <= 0 || width > limits.MaxDecodePixels/height ||
		width > limits.MaxDecodeBytes/4/height || width > int64(^uint(0)>>1)/4/height {
		return nil, fileError(ErrLimit, "patterns", "patterns.rgb", record.Body.Offset, "RGB tile exceeds decode budgets")
	}
	outputBytes := width * height * 4
	if outputBytes >= limits.MaxDecodeBytes {
		return nil, fileError(ErrLimit, "patterns", "patterns.rgb", record.Body.Offset, "RGB tile consumes the decode budget")
	}
	limits.MaxDecodeBytes -= outputBytes
	pixels, err := d.decodePatternLocked(ctx, record, limits)
	if err != nil {
		return nil, err
	}
	result := image.NewNRGBA(image.Rect(0, 0, int(width), int(height)))
	for index := 3; index < len(result.Pix); index += 4 {
		result.Pix[index] = 255
	}
	found := [3]bool{}
	for _, channel := range pixels.Channels {
		component := channel.ArrayIndex
		if component == int(record.VMChannels)+1 {
			component = 3
		} else if component == int(record.VMChannels) {
			return nil, fileError(ErrUnsupported, "patterns", "patterns.rgb", record.Body.Offset,
				"pattern user-mask sampling semantics require explicit support")
		}
		if component < 0 || component > 2 && channel.ArrayIndex != int(record.VMChannels)+1 {
			continue
		}
		if channel.Plane.Depth != 8 || channel.Bounds != record.VMBounds {
			return nil, fileError(ErrUnsupported, "patterns", "patterns.rgb", record.Body.Offset,
				"RGB or transparency array has a different depth or tile rectangle")
		}
		if component < 3 {
			found[component] = true
		}
		for row := 0; row < int(height); row++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for column := 0; column < int(width); column++ {
				result.Pix[row*result.Stride+column*4+component] = channel.Plane.Data[row*channel.Plane.RowBytes+column]
			}
		}
	}
	if !found[0] || !found[1] || !found[2] {
		return nil, fileError(ErrUnsupported, "patterns", "patterns.rgb", record.Body.Offset, "RGB channel arrays are incomplete")
	}
	return result, nil
}

type strictPatternReaderAt struct {
	ctx    context.Context
	reader io.ReaderAt
}

func (r strictPatternReaderAt) ReadAt(data []byte, offset int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	count, err := r.reader.ReadAt(data, offset)
	if r.ctx.Err() != nil {
		return 0, r.ctx.Err()
	}
	if count < 0 || count > len(data) || count < len(data) && err == nil {
		return 0, io.ErrUnexpectedEOF
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	return count, err
}
