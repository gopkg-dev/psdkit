package psdfile

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"slices"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdtext"
)

// TextLayerRecord associates a modern TySh text block with its physical source layer.
// Transform is xx, xy, yx, yy, tx, ty. Bounds is left, top, right, bottom,
// promoted from the native four float32 fields; BoundsBits preserves their exact IEEE bits.
// EngineData is an original lazy blob span, while TextDescriptor and Warp are owned metadata.
// This view does not resolve fonts, shape glyphs or render the text or warp.
type TextLayerRecord struct {
	LayerIndex, LayerSetIndex           int
	LayerSetKey                         string
	Block                               AdditionalBlock
	Version, TextVersion, WarpVersion   uint16
	Transform                           [6]float64
	TransformBits                       [6]uint64
	Bounds                              [4]float64
	BoundsBits                          [4]uint32
	Text                                string
	HasTextIndex                        bool
	TextIndex                           int32
	TextDescriptor, Warp                *Descriptor
	HasEngineData                       bool
	EngineData                          Span
	Prefix, TextDescriptorData          Span
	WarpVersionData, WarpDescriptorData Span
	BoundsData, Tail                    Span
}

// TextLayerView owns text records and reports the conservative decoder allocation reservation.
// EngineData blobs and raster channels remain lazy original spans.
type TextLayerView struct {
	Records       []TextLayerRecord
	MetadataBytes int64
}

// ReadTextLayerView returns text metadata with its reservation for compound rendering budgets.
func (d *Document) ReadTextLayerView(ctx context.Context, options ...MetadataOptions) (TextLayerView, error) {
	var result TextLayerView
	option, err := metadataViewOption(options, &result.MetadataBytes)
	if err != nil {
		return result, err
	}
	result.Records, err = d.TextLayers(ctx, option)
	return result, err
}

// TextLayers decodes modern TySh descriptors without reading their EngineData blobs or raster channels.
// It preserves duplicate text blocks and descriptor entries in original source order.
// Legacy tySh and unknown header versions return ErrUnsupported, with original blocks still indexed.
func (d *Document) TextLayers(ctx context.Context, options ...MetadataOptions) ([]TextLayerRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := d.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limits, err := d.metadataLimits(options)
	if err != nil {
		return nil, err
	}
	var result []TextLayerRecord
	allocated, items := int64(0), 0
	if len(options) == 1 && options[0].reservation != nil {
		allocated = 64
		defer func() { *options[0].reservation = allocated }()
		if allocated > limits.MaxMetadataBytes {
			return nil, fileError(ErrLimit, "text", "textLayers", 0, "view metadata exceeds budget")
		}
	}
	for setIndex, set := range d.LayerSets {
		for layerIndex, layer := range set.Layers {
			for _, block := range layer.AdditionalInfo {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if block.Key == "tySh" {
					return nil, fileError(ErrUnsupported, "text", "textLayers.version", block.Data.Offset,
						"legacy Photoshop 5 text schema is retained but not decoded")
				}
				if block.Key != "TySh" {
					continue
				}
				if len(result) >= limits.MaxBlocks || limits.MaxMetadataBytes-allocated < 128 {
					return nil, fileError(ErrLimit, "text", "textLayers", block.Data.Offset, "text metadata exceeds limits")
				}
				allocated += 128
				if len(result) == cap(result) {
					capacity := max(1, cap(result)*2)
					reservation := int64(capacity) * int64(unsafe.Sizeof(TextLayerRecord{}))
					if reservation > limits.MaxMetadataBytes-allocated {
						return nil, fileError(ErrLimit, "text", "textLayers", block.Data.Offset, "record budget exceeded")
					}
					allocated += reservation
					owned := make([]TextLayerRecord, len(result), capacity)
					copy(owned, result)
					result = owned
				}
				if block.Data.Length < 52 {
					return nil, fileError(ErrMalformed, "text", "textLayers.header", block.Data.Offset, "incomplete TySh header")
				}
				record := TextLayerRecord{LayerIndex: layerIndex, LayerSetIndex: setIndex, LayerSetKey: set.Key,
					Block: block, Prefix: Span{Offset: block.Data.Offset, Length: 52}}
				data, err := d.textRead(ctx, record.Prefix)
				if err != nil {
					return nil, err
				}
				record.Version, record.TextVersion = binary.BigEndian.Uint16(data), binary.BigEndian.Uint16(data[50:])
				if record.Version != 1 || record.TextVersion != 50 {
					return nil, fileError(ErrUnsupported, "text", "textLayers.version", block.Data.Offset,
						"unknown TySh or text version")
				}
				for index := range 6 {
					record.TransformBits[index] = binary.BigEndian.Uint64(data[2+index*8:])
					record.Transform[index] = math.Float64frombits(record.TransformBits[index])
					if !textFinite(record.Transform[index]) {
						return nil, fileError(ErrMalformed, "text", "textLayers.transform", record.Prefix.Offset+2+int64(index*8),
							"affine transform contains a non-finite coordinate")
					}
				}
				end := block.Data.Offset + block.Data.Length
				span := Span{Offset: record.Prefix.Offset + record.Prefix.Length,
					Length: block.Data.Length - record.Prefix.Length}
				remaining := limits
				remaining.MaxMetadataBytes -= allocated
				remaining.MaxDescriptorItems -= items
				descriptor, used, count, err := d.decodeDescriptorUsage(ctx, span, true, remaining)
				if err != nil {
					return nil, err
				}
				allocated, items = allocated+used, items+count
				record.TextDescriptor, record.TextDescriptorData = descriptor, descriptor.Span
				value := textDescriptorValue(descriptor, "Txt ")
				if text, ok := value.(string); ok {
					record.Text = text
				} else {
					return nil, fileError(ErrMalformed, "text", "textLayers.text", descriptor.Span.Offset,
						"text descriptor lacks its Unicode text")
				}
				if value := textDescriptorValue(descriptor, "EngineData"); value != nil {
					blob, ok := value.(DescriptorBlob)
					if !ok {
						return nil, fileError(ErrMalformed, "text", "textLayers.engineData", descriptor.Span.Offset,
							"EngineData is not a lazy descriptor blob")
					}
					record.HasEngineData, record.EngineData = true, blob.Data
				}
				if value := textDescriptorValue(descriptor, "TextIndex"); value != nil {
					index, ok := value.(int32)
					if !ok || index < 0 {
						return nil, fileError(ErrMalformed, "text", "textLayers.TextIndex", descriptor.Span.Offset,
							"TextIndex is not a nonnegative native long")
					}
					record.HasTextIndex, record.TextIndex = true, index
				}
				position := descriptor.Span.Offset + descriptor.Span.Length
				if end-position < 2 {
					return nil, fileError(ErrMalformed, "text", "textLayers.warpVersion", position, "missing warp version")
				}
				record.WarpVersionData = Span{Offset: position, Length: 2}
				data, err = d.textRead(ctx, record.WarpVersionData)
				if err != nil {
					return nil, err
				}
				record.WarpVersion = binary.BigEndian.Uint16(data)
				if record.WarpVersion != 1 {
					return nil, fileError(ErrUnsupported, "text", "textLayers.warpVersion", position, "unknown text warp version")
				}
				position += 2
				remaining = limits
				remaining.MaxMetadataBytes -= allocated
				remaining.MaxDescriptorItems -= items
				warpSpan := Span{Offset: position, Length: end - position}
				warp, used, count, err := d.decodeDescriptorUsage(ctx, warpSpan, true, remaining)
				if err != nil {
					return nil, err
				}
				allocated, items = allocated+used, items+count
				record.Warp, record.WarpDescriptorData = warp, warp.Span
				position = warp.Span.Offset + warp.Span.Length
				if end-position < 16 {
					return nil, fileError(ErrMalformed, "text", "textLayers.bounds", position, "missing native float32 bounds")
				}
				record.BoundsData = Span{Offset: position, Length: 16}
				data, err = d.textRead(ctx, record.BoundsData)
				if err != nil {
					return nil, err
				}
				for index := range 4 {
					record.BoundsBits[index] = binary.BigEndian.Uint32(data[index*4:])
					record.Bounds[index] = float64(math.Float32frombits(record.BoundsBits[index]))
					if !textFinite(record.Bounds[index]) {
						return nil, fileError(ErrMalformed, "text", "textLayers.bounds", position+int64(index*4),
							"text bounds contain a non-finite coordinate")
					}
				}
				record.Tail = Span{Offset: position + 16, Length: end - position - 16}
				result = append(result, record)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slices.SortStableFunc(result, func(left, right TextLayerRecord) int {
		if left.Block.Span.Offset < right.Block.Span.Offset {
			return -1
		}
		if left.Block.Span.Offset > right.Block.Span.Offset {
			return 1
		}
		return 0
	})
	return result, nil
}

// DecodeTextEngine reads and parses only the selected record's retained EngineData blob.
// A text descriptor without EngineData returns ErrUnsupported rather than invented style metadata.
func (d *Document) DecodeTextEngine(
	ctx context.Context, record TextLayerRecord, options ...psdtext.DecodeOptions,
) (*psdtext.EngineData, error) {
	if _, err := d.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if !record.HasEngineData {
		return nil, fileError(ErrUnsupported, "text", "textLayers.engineData", record.Block.Data.Offset,
			"text layer has no saved EngineData blob")
	}
	return d.DecodeEngineData(ctx, record.EngineData, options...)
}

// DecodeEngineData parses an original EngineData span with source and caller budgets combined.
// SourceOffset is always the original span offset; caller limits may lower but never raise source limits.
func (d *Document) DecodeEngineData(
	ctx context.Context, span Span, options ...psdtext.DecodeOptions,
) (*psdtext.EngineData, error) {
	return d.decodeEngineData(ctx, span, false, options...)
}

func (d *Document) decodeEngineData(
	ctx context.Context, span Span, document bool, options ...psdtext.DecodeOptions,
) (*psdtext.EngineData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := d.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(options) > 1 {
		return nil, fileError(ErrMalformed, "text", "engineData.options", span.Offset, "provide at most one options value")
	}
	limits := psdtext.DecodeOptions{SourceOffset: span.Offset,
		MaxBytes: min(d.limits.MaxMetadataBytes, d.limits.MaxDecodeBytes),
		MaxDepth: min(256, d.limits.MaxDescriptorDepth), MaxNodes: d.limits.MaxDescriptorItems}
	if len(options) == 1 {
		provided := options[0]
		if provided.SourceOffset < 0 || provided.MaxBytes < 0 || provided.MaxDepth < 0 ||
			provided.MaxDepth > 256 || provided.MaxNodes < 0 {
			return nil, fileError(ErrLimit, "text", "engineData.options", span.Offset, "invalid EngineData limits")
		}
		if provided.MaxBytes != 0 {
			limits.MaxBytes = min(limits.MaxBytes, provided.MaxBytes)
		}
		if provided.MaxDepth != 0 {
			limits.MaxDepth = min(limits.MaxDepth, provided.MaxDepth)
		}
		if provided.MaxNodes != 0 {
			limits.MaxNodes = min(limits.MaxNodes, provided.MaxNodes)
		}
	}
	if !d.validSpan(span) {
		return nil, fileError(ErrMalformed, "text", "engineData.span", span.Offset, "EngineData is outside the source")
	}
	if span.Length > d.limits.MaxReadBytes || span.Length > limits.MaxBytes/2 {
		return nil, fileError(ErrLimit, "text", "engineData.span", span.Offset,
			"payload plus owned parse copy exceed source or caller budget")
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	data, err := d.textReadLocked(ctx, span)
	if err != nil {
		return nil, err
	}
	limits.MaxBytes -= span.Length
	var engine *psdtext.EngineData
	if document {
		engine, err = psdtext.DecodeDocument(ctx, data, limits)
	} else {
		engine, err = psdtext.Decode(ctx, data, limits)
	}
	if err != nil {
		return nil, textDecodeError(err, span.Offset)
	}
	return engine, nil
}

func (d *Document) textRead(ctx context.Context, span Span) ([]byte, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.textReadLocked(ctx, span)
}

func (d *Document) textReadLocked(ctx context.Context, span Span) ([]byte, error) {
	if d.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !d.validSpan(span) {
		return nil, fileError(ErrMalformed, "text", "text.span", span.Offset, "text field is outside the source")
	}
	if span.Length > d.limits.MaxReadBytes || uint64(span.Length) > uint64(^uint(0)>>1) {
		return nil, fileError(ErrLimit, "text", "text.span", span.Offset, "text field exceeds MaxReadBytes")
	}
	data := make([]byte, int(span.Length))
	reader := contextReaderAt{ctx: ctx, reader: d.reader}
	count, err := reader.ReadAt(data, span.Offset)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if count != len(data) || err != nil && !errors.Is(err, io.EOF) {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, &Error{Kind: ErrMalformed, Feature: "text", Path: "text.payload", Offset: span.Offset,
			Detail: "original text bytes could not be read", Cause: err}
	}
	return data, nil
}

func textDescriptorValue(descriptor *Descriptor, key string) any {
	if descriptor == nil {
		return nil
	}
	for _, v := range slices.Backward(descriptor.Entries) {
		if v.Key == key {
			return v.Value
		}
	}
	return nil
}

func textFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func textDecodeError(err error, offset int64) error {
	kind := ErrMalformed
	switch {
	case errors.Is(err, psdtext.ErrLimit):
		kind = ErrLimit
	case errors.Is(err, psdtext.ErrUnsupported):
		kind = ErrUnsupported
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	path, detail := "engineData", "retained EngineData decoding failed"
	if textErr, ok := errors.AsType[*psdtext.Error](err); ok {
		path, detail, offset = textErr.Path, textErr.Detail, textErr.Offset
	}
	return &Error{Kind: kind, Feature: "text", Path: path, Offset: offset, Detail: detail, Cause: err}
}
