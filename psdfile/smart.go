package psdfile

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"slices"
	"unsafe"
)

// SmartObjectRecord associates a placed-layer descriptor with its physical source layer.
// SourceID links instances to LinkedFileRecord.ID; applications may treat these identifiers as private.
// Descriptor values are owned metadata, while original spans depend on the open source.
type SmartObjectRecord struct {
	LayerIndex, LayerSetIndex    int
	LayerSetKey                  string
	Block                        AdditionalBlock
	Prefix, DescriptorData, Tail Span
	Descriptor                   *Descriptor
	SourceID                     string
	SourceType                   int32
	Width, Height                float64
	Transform, NonAffine         []float64
	Warp                         *Descriptor
}

// SmartObjects decodes modern SoLd/SoLE placement metadata without opening linked contents.
// Ordered duplicate placement blocks remain separate records. Legacy PlLd blocks are retained by the source index.
func (d *Document) SmartObjects(ctx context.Context, options ...MetadataOptions) ([]SmartObjectRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := d.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	configuration, err := d.metadataLimits(options)
	if err != nil {
		return nil, err
	}
	var records []SmartObjectRecord
	metadataBytes, descriptorItems := int64(0), 0
	if len(options) == 1 && options[0].reservation != nil {
		metadataBytes = 64
		defer func() { *options[0].reservation = metadataBytes }()
		if metadataBytes > configuration.MaxMetadataBytes {
			return nil, fileError(ErrLimit, "smart", "smartObjects", 0, "view metadata exceeds budget")
		}
	}
	for setIndex, set := range d.LayerSets {
		for index, layer := range set.Layers {
			for _, block := range layer.AdditionalInfo {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if block.Key != "SoLd" && block.Key != "SoLE" {
					continue
				}
				if len(records) >= configuration.MaxBlocks {
					return nil, fileError(ErrLimit, "smart", "smartObjects", block.Data.Offset, "placement metadata exceeds limits")
				}
				if len(records) == cap(records) {
					capacity := max(1, cap(records)*2)
					added := int64(capacity-cap(records)) * int64(unsafe.Sizeof(SmartObjectRecord{}))
					if added > configuration.MaxMetadataBytes-metadataBytes {
						return nil, fileError(ErrLimit, "smart", "smartObjects", block.Data.Offset, "metadata budget exceeded")
					}
					metadataBytes += added
					owned := make([]SmartObjectRecord, len(records), capacity)
					copy(owned, records)
					records = owned
				}
				if block.Data.Length < 12 {
					return nil, fileError(ErrMalformed, "smart", "smartObjects.header", block.Data.Offset, "incomplete placement")
				}
				prefix := Span{Offset: block.Data.Offset, Length: 8}
				data, err := d.ReadSpan(prefix)
				if err != nil {
					return nil, err
				}
				version := binary.BigEndian.Uint32(data[4:])
				if string(data[:4]) != "soLD" || (version != 4 && version != 5) {
					return nil, fileError(ErrUnsupported, "smart", "smartObjects.version", prefix.Offset, "unknown placement schema")
				}
				span := Span{Offset: prefix.Offset + prefix.Length, Length: block.Data.Length - prefix.Length}
				limits := configuration
				limits.MaxMetadataBytes -= metadataBytes
				limits.MaxDescriptorItems -= descriptorItems
				descriptor, allocated, items, err := d.decodeDescriptorUsage(ctx, span, true, limits)
				if err != nil {
					return nil, err
				}
				metadataBytes += allocated
				descriptorItems += items
				id, ok := smartDescriptorValue(descriptor, "Idnt").(string)
				if !ok || id == "" {
					return nil, fileError(ErrMalformed, "smart", "smartObjects.id", span.Offset, "source reference is absent")
				}
				size, _ := smartDescriptorValue(descriptor, "Sz  ").(*Descriptor)
				width, widthOK := smartDescriptorNumber(smartDescriptorValue(size, "Wdth"))
				height, heightOK := smartDescriptorNumber(smartDescriptorValue(size, "Hght"))
				if !widthOK || !heightOK || width <= 0 || height <= 0 || !smartFinite(width) || !smartFinite(height) {
					return nil, fileError(ErrMalformed, "smart", "smartObjects.size", span.Offset, "source dimensions are invalid")
				}
				coordinatesBytes := int64(64)
				if smartDescriptorValue(descriptor, "nonAffineTransform") != nil {
					coordinatesBytes += 64
				}
				if coordinatesBytes > configuration.MaxMetadataBytes-metadataBytes {
					return nil, fileError(ErrLimit, "smart", "smartObjects.transform", span.Offset, "coordinate budget exceeded")
				}
				metadataBytes += coordinatesBytes
				transform, err := smartDescriptorNumbers(smartDescriptorValue(descriptor, "Trnf"))
				if err != nil {
					return nil, err
				}
				if len(transform) != 8 {
					return nil, fileError(ErrMalformed, "smart", "smartObjects.transform", span.Offset, "transform is absent")
				}
				nonAffine, err := smartDescriptorNumbers(smartDescriptorValue(descriptor, "nonAffineTransform"))
				if err != nil {
					return nil, err
				}
				var warp *Descriptor
				for _, key := range []string{"warp", "quiltWarp"} {
					if value := smartDescriptorValue(descriptor, key); value != nil {
						object, ok := value.(*Descriptor)
						if !ok || object == nil {
							return nil, fileError(ErrMalformed, "smart", "smartObjects.warp", span.Offset,
								"warp metadata is not a descriptor")
						}
						warp = object
					}
				}
				typeNumber, typeOK := smartDescriptorNumber(smartDescriptorValue(descriptor, "Type"))
				if !typeOK {
					return nil, fileError(ErrMalformed, "smart", "smartObjects.type", span.Offset,
						"placement type is absent or invalid")
				}
				if !smartFinite(typeNumber) || typeNumber < 0 || typeNumber > 3 || typeNumber != math.Trunc(typeNumber) {
					return nil, fileError(ErrUnsupported, "smart", "smartObjects.type", span.Offset, "unknown placement type")
				}
				end := descriptor.Span.Offset + descriptor.Span.Length
				records = append(records, SmartObjectRecord{
					LayerIndex: index, LayerSetIndex: setIndex, LayerSetKey: set.Key,
					Block: block, Prefix: prefix, DescriptorData: descriptor.Span,
					Tail:       Span{Offset: end, Length: block.Data.Offset + block.Data.Length - end},
					Descriptor: descriptor, SourceID: id, SourceType: int32(typeNumber), Width: width, Height: height,
					Transform: transform, NonAffine: nonAffine, Warp: warp,
				})
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slices.SortStableFunc(records, func(left, right SmartObjectRecord) int {
		if left.Block.Span.Offset < right.Block.Span.Offset {
			return -1
		}
		if left.Block.Span.Offset > right.Block.Span.Offset {
			return 1
		}
		return 0
	})
	return records, nil
}

// OpenLinkedDocument indexes an embedded or cached PSD/PSB without copying its encoded payload.
// The child borrows its parent's source: keep the parent open until the child is no longer used.
// No external path is opened; non-PSD image data requires an explicit image decoder.
func (d *Document) OpenLinkedDocument(
	ctx context.Context, record LinkedFileRecord, options ...LoadOptions,
) (*Document, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !record.HasData || record.Data.Length < 26 || !d.validSpan(record.Data) {
		return nil, fileError(ErrUnsupported, "smart", "linked.content", record.Data.Offset, "no indexed PSD/PSB content")
	}
	var signature [4]byte
	count, err := d.ReadAt(signature[:], record.Data.Offset)
	if cancellation := ctx.Err(); cancellation != nil {
		return nil, cancellation
	}
	if err != nil && err != io.EOF || count != len(signature) { //nolint:errorlint // Only bare EOF ends a read; joined errors retain source faults.
		return nil, &Error{Kind: ErrMalformed, Feature: "smart", Path: "linked.signature", Offset: record.Data.Offset,
			Detail: "embedded signature could not be read completely", Cause: err}
	}
	if string(signature[:]) != "8BPS" {
		return nil, fileError(ErrUnsupported, "smart", "linked.format", record.Data.Offset,
			"embedded content is not PSD/PSB; use an explicit supported raster decoder")
	}
	if len(options) == 0 {
		options = []LoadOptions{{Limits: d.Limits()}}
	}
	reader := &linkedDocumentReader{source: d, span: record.Data, indexingContext: ctx}
	child, err := Read(reader, record.Data.Length, options...)
	if err != nil {
		return nil, err
	}
	reader.indexingContext = nil
	return child, nil
}

type linkedDocumentReader struct {
	source          *Document
	span            Span
	indexingContext context.Context
}

func (r *linkedDocumentReader) ReadAt(data []byte, offset int64) (int, error) {
	if r.indexingContext != nil {
		if err := r.indexingContext.Err(); err != nil {
			return 0, err
		}
	}
	if offset < 0 || offset > r.span.Length {
		return 0, io.EOF
	}
	length := int64(len(data))
	available := r.span.Length - offset
	if length > available {
		count, err := r.source.ReadAt(data[:int(available)], r.span.Offset+offset)
		if err != nil {
			return count, err
		}
		return count, io.EOF
	}
	return r.source.ReadAt(data, r.span.Offset+offset)
}

func smartDescriptorValue(descriptor *Descriptor, key string) any {
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

func smartDescriptorNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case int32:
		return float64(number), true
	case int64:
		return float64(number), true
	case DescriptorUnit:
		switch unit := number.Number.(type) {
		case float64:
			return unit, true
		case float32:
			return float64(unit), true
		}
	}
	return 0, false
}

func smartDescriptorNumbers(value any) ([]float64, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]DescriptorValue)
	if !ok {
		return nil, fileError(ErrMalformed, "smart", "smartObjects.transform", 0, "transform is not a typed list")
	}
	if len(items) != 8 {
		return nil, fileError(ErrMalformed, "smart", "smartObjects.transform", 0, "transform needs eight coordinates")
	}
	numbers := make([]float64, len(items))
	for index, item := range items {
		number, ok := smartDescriptorNumber(item.Value)
		if !ok || !smartFinite(number) {
			return nil, fileError(ErrMalformed, "smart", "smartObjects.transform", 0, "coordinate is invalid")
		}
		numbers[index] = number
	}
	return numbers, nil
}

func smartFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
