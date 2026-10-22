package psdfile

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"math"
	"slices"
	"unsafe"
)

// TIFFLayerSource indexes Photoshop-private saved layers from one embedded classic little-endian RGB8 TIFF.
// Channel samples are saved layer pixels, not live text, vector, effect or smart-object reconstruction.
// The source borrows its original document. Close only invalidates this view and its borrowed child documents.
// The internal big-endian structural view has no composite and is never exposed as a PSD or TIFF writer input.
type TIFFLayerSource struct {
	parent      *Document
	view        *Document
	record      LinkedFileRecord
	info        TIFFLayerSourceInfo
	swaps       []tiffPrivateSwap
	opaque      []TIFFOpaqueBlock
	metadata    int64
	limits      Limits
	profileSpan Span
}

// TIFFLayerSourceInfo describes the TIFF canvas and the provenance of its indexed private layers.
// All spans use absolute original document offsets. Normalized fields occupy those same address ranges.
type TIFFLayerSourceInfo struct {
	Width, Height                 uint32
	Depth                         uint16
	LayerCount                    int
	TIFFData, PrivateData, Layers Span
	SavedPreviewKind              string
	MetadataBytes                 int64
}

// TIFFOpaqueBlock retains a known block whose payload has not been normalized for semantic decoding.
// LayerIndex is -1 for a global block. Such blocks remain readable as original bytes only.
type TIFFOpaqueBlock struct {
	LayerIndex int
	Key        string
	Data       Span
}

// TIFFNormalizationRange identifies original fields whose units are reversed in the structural view.
// Original and Normalized have identical absolute offsets; each UnitBytes-sized element reverses independently.
// Returned ranges cover complete normalized fields, even when the queried span intersects only part of one.
type TIFFNormalizationRange struct {
	Original, Normalized Span
	UnitBytes            uint8
}

// TIFFSavedPreview contains the standard TIFF main IFD saved image, with explicit preview provenance.
// It does not represent private-layer transparency or live private-layer semantics.
type TIFFSavedPreview struct {
	Raster *LinkedRaster
	Kind   string
}

// OpenLinkedTIFFLayers indexes private Layr records without decoding saved pixels or embedded media.
// Limits may only lower the original source budgets. Unknown private block schemas return ErrUnsupported.
// Metadata, placement descriptors and RLE tables are normalized by bounded ReaderAt unit reversals.
func (d *Document) OpenLinkedTIFFLayers(
	ctx context.Context, record LinkedFileRecord, options ...LoadOptions,
) (*TIFFLayerSource, error) {
	ctx = decodeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d == nil {
		return nil, fileError(ErrMalformed, "tiff.layers", "source", 0, "original source is absent")
	}
	if _, err := d.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	if record.Kind != "liFD" || !record.HasData || !d.validSpan(record.Data) || record.Data.Length < 8 {
		return nil, fileError(ErrUnsupported, "tiff.layers", "content", record.Data.Offset,
			"an indexed embedded TIFF source is required")
	}
	limits, err := tiffPrivateLimits(d.Limits(), options)
	if err != nil {
		return nil, err
	}
	if record.Data.Length > limits.MaxFileBytes {
		return nil, fileError(ErrLimit, "tiff.layers", "content", record.Data.Offset, "TIFF exceeds MaxFileBytes")
	}
	builder := tiffPrivateBuilder{ctx: ctx, parent: d, span: record.Data, limits: limits}
	return builder.open(record)
}

func tiffPrivateLimits(parent Limits, options []LoadOptions) (Limits, error) {
	if len(options) == 0 {
		return parent, nil
	}
	provided, err := loadLimits(options)
	if err != nil {
		return Limits{}, err
	}
	provided.MaxFileBytes = min(parent.MaxFileBytes, provided.MaxFileBytes)
	provided.MaxMetadataBytes = min(parent.MaxMetadataBytes, provided.MaxMetadataBytes)
	provided.MaxReadBytes = min(parent.MaxReadBytes, provided.MaxReadBytes)
	provided.MaxDecodePixels = min(parent.MaxDecodePixels, provided.MaxDecodePixels)
	provided.MaxDecodeBytes = min(parent.MaxDecodeBytes, provided.MaxDecodeBytes)
	provided.MaxBlocks = min(parent.MaxBlocks, provided.MaxBlocks)
	provided.MaxLayers = min(parent.MaxLayers, provided.MaxLayers)
	provided.MaxDescriptorItems = min(parent.MaxDescriptorItems, provided.MaxDescriptorItems)
	provided.MaxDescriptorDepth = min(parent.MaxDescriptorDepth, provided.MaxDescriptorDepth)
	return provided, nil
}

// Info returns an owned value describing indexed geometry and source provenance, including after Close.
func (source *TIFFLayerSource) Info() TIFFLayerSourceInfo {
	if source == nil {
		return TIFFLayerSourceInfo{}
	}
	return source.info
}

// Close invalidates private-layer operations without closing or modifying the original document.
func (source *TIFFLayerSource) Close() error {
	if source == nil || source.view == nil {
		return nil
	}
	return source.view.Close()
}

func (source *TIFFLayerSource) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if source == nil || source.view == nil || source.parent == nil {
		return fileError(ErrMalformed, "tiff.layers", "source", 0, "private layer source is absent")
	}
	if _, err := source.view.ReadAt(nil, 0); err != nil {
		return err
	}
	_, err := source.parent.ReadAt(nil, 0)
	return err
}

func (source *TIFFLayerSource) valid(span Span) bool {
	return span.Offset >= source.record.Data.Offset && span.Length >= 0 &&
		span.Offset <= source.record.Data.Offset+source.record.Data.Length &&
		span.Length <= source.record.Data.Offset+source.record.Data.Length-span.Offset
}

// OriginalSpan maps a normalized structural interval to its original TIFF address interval.
// ReadOriginalSpan returns wire bytes; ReadNormalizedSpan applies the separately reported unit reversals.
func (source *TIFFLayerSource) OriginalSpan(ctx context.Context, normalized Span) (Span, error) {
	ctx = decodeContext(ctx)
	if err := source.check(ctx); err != nil {
		return Span{}, err
	}
	if !source.valid(normalized) {
		return Span{}, fileError(ErrMalformed, "tiff.layers", "mapping", normalized.Offset,
			"normalized interval is outside this TIFF source")
	}
	return normalized, nil
}

// NormalizationRanges returns owned complete unit-reversal ranges intersecting the requested source interval.
func (source *TIFFLayerSource) NormalizationRanges(
	ctx context.Context, span Span,
) ([]TIFFNormalizationRange, error) {
	if _, err := source.OriginalSpan(ctx, span); err != nil {
		return nil, err
	}
	var ranges []TIFFNormalizationRange
	for _, swap := range source.swaps {
		if swap.span.Offset >= span.Offset+span.Length {
			break
		}
		if swap.span.Offset+swap.span.Length <= span.Offset {
			continue
		}
		if int64(len(ranges)+1)*int64(unsafe.Sizeof(TIFFNormalizationRange{}))*2 >
			min(source.limits.MaxMetadataBytes, source.limits.MaxDecodeBytes)-source.metadata {
			return nil, fileError(ErrLimit, "tiff.layers", "mapping", span.Offset, "mapping output exceeds metadata budget")
		}
		ranges = append(ranges, TIFFNormalizationRange{Original: swap.span, Normalized: swap.span, UnitBytes: swap.width})
	}
	return ranges, nil
}

// OpaqueBlocks returns known private payloads unavailable for normalized semantic decoding.
func (source *TIFFLayerSource) OpaqueBlocks(ctx context.Context) ([]TIFFOpaqueBlock, error) {
	ctx = decodeContext(ctx)
	if err := source.check(ctx); err != nil {
		return nil, err
	}
	if int64(len(source.opaque))*2*int64(unsafe.Sizeof(TIFFOpaqueBlock{})) >
		min(source.limits.MaxMetadataBytes, source.limits.MaxDecodeBytes)-source.metadata {
		return nil, fileError(ErrLimit, "tiff.layers", "opaque", 0, "owned block metadata exceeds its budget")
	}
	return slices.Clone(source.opaque), nil
}

// LayerRecord returns one owned physical Layr record with normalized metadata spans.
// Its saved channel pixels are decoded only by an explicit channel or layer request.
func (source *TIFFLayerSource) LayerRecord(ctx context.Context, index int) (LayerRecord, error) {
	ctx = decodeContext(ctx)
	if err := source.check(ctx); err != nil {
		return LayerRecord{}, err
	}
	if index < 0 || index >= len(source.view.Layers) {
		return LayerRecord{}, fileError(ErrMalformed, "tiff.layers", "layer.index", 0, "layer index is outside Layr")
	}
	record := source.view.Layers[index]
	working := int64(unsafe.Sizeof(record)) + int64(len(record.Channels))*2*int64(unsafe.Sizeof(ChannelRecord{})) +
		int64(len(record.AdditionalInfo))*2*int64(unsafe.Sizeof(AdditionalBlock{})) + 256
	if working > min(source.limits.MaxMetadataBytes, source.limits.MaxDecodeBytes)-source.metadata {
		return LayerRecord{}, fileError(ErrLimit, "tiff.layers", "layer.record", record.Span.Offset,
			"owned layer record exceeds metadata budget")
	}
	record.Channels = slices.Clone(record.Channels)
	record.AdditionalInfo = slices.Clone(record.AdditionalInfo)
	if record.Mask != nil {
		mask := *record.Mask
		if mask.UserDensity != nil {
			value := *mask.UserDensity
			mask.UserDensity = &value
		}
		if mask.UserFeather != nil {
			value := *mask.UserFeather
			mask.UserFeather = &value
		}
		if mask.VectorDensity != nil {
			value := *mask.VectorDensity
			mask.VectorDensity = &value
		}
		if mask.VectorFeather != nil {
			value := *mask.VectorFeather
			mask.VectorFeather = &value
		}
		record.Mask = &mask
	}
	return record, nil
}

// DecodeLayer returns owned saved channel samples at their original RGB8 precision.
func (source *TIFFLayerSource) DecodeLayer(ctx context.Context, index int) (*LayerPixels, error) {
	ctx = decodeContext(ctx)
	if err := source.check(ctx); err != nil {
		return nil, err
	}
	return source.view.DecodeLayer(ctx, index)
}

// DecodeChannel returns one owned saved layer channel, including source masks or transparency.
func (source *TIFFLayerSource) DecodeChannel(ctx context.Context, layer, channel int) (*Plane, error) {
	ctx = decodeContext(ctx)
	if err := source.check(ctx); err != nil {
		return nil, err
	}
	return source.view.DecodeChannel(ctx, layer, channel)
}

// DecodeChannelRegion reads a channel-local region without retaining unrelated saved rows.
func (source *TIFFLayerSource) DecodeChannelRegion(
	ctx context.Context, layer, channel int, rectangle image.Rectangle, options ...RegionOptions,
) (*Plane, error) {
	ctx = decodeContext(ctx)
	if err := source.check(ctx); err != nil {
		return nil, err
	}
	return source.view.DecodeChannelRegion(ctx, layer, channel, rectangle, options...)
}

// ReadSmartObjectView returns owned modern placement descriptors without opening or rendering their contents.
func (source *TIFFLayerSource) ReadSmartObjectView(
	ctx context.Context, options ...MetadataOptions,
) (SmartObjectView, error) {
	ctx = decodeContext(ctx)
	if err := source.check(ctx); err != nil {
		return SmartObjectView{}, err
	}
	return source.view.ReadSmartObjectView(ctx, options...)
}

// ReadLinkedFileView indexes private linked entries without copying their embedded media payloads.
func (source *TIFFLayerSource) ReadLinkedFileView(
	ctx context.Context, options ...MetadataOptions,
) (LinkedFileView, error) {
	ctx = decodeContext(ctx)
	if err := source.check(ctx); err != nil {
		return LinkedFileView{}, err
	}
	return source.view.ReadLinkedFileView(ctx, options...)
}

// OpenLinkedDocument opens an actual borrowed embedded PSD/PSB from a private TIFF linked record.
// The returned child has its original PSD/PSB header and bytes; no external path is opened.
func (source *TIFFLayerSource) OpenLinkedDocument(
	ctx context.Context, record LinkedFileRecord, options ...LoadOptions,
) (*Document, error) {
	ctx = decodeContext(ctx)
	if err := source.check(ctx); err != nil {
		return nil, err
	}
	if !source.valid(record.Data) {
		return nil, fileError(ErrMalformed, "tiff.layers", "linked.content", record.Data.Offset,
			"linked media is outside this TIFF")
	}
	limits, err := tiffPrivateLimits(source.view.Limits(), options)
	if err != nil {
		return nil, err
	}
	// Embedded PSD/PSB contents keep their original wire encoding, including legal aliased source intervals.
	reservation := int64(unsafe.Sizeof(Document{})) + int64(unsafe.Sizeof(tiffPrivateOriginalReader{}))
	if reservation >= min(limits.MaxMetadataBytes, limits.MaxDecodeBytes) {
		return nil, fileError(ErrLimit, "tiff.layers", "linked.content", record.Data.Offset,
			"original embedded-content reader exhausts remaining working budgets")
	}
	limits.MaxMetadataBytes -= reservation
	limits.MaxDecodeBytes -= reservation
	original := &Document{reader: &tiffPrivateOriginalReader{source: source}, size: source.parent.size, limits: limits}
	return original.OpenLinkedDocument(ctx, record, LoadOptions{Limits: limits})
}

// Resources returns owned Photoshop image-resource records retained in their original big-endian TIFF encoding.
func (source *TIFFLayerSource) Resources(ctx context.Context) ([]ResourceBlock, error) {
	ctx = decodeContext(ctx)
	if err := source.check(ctx); err != nil {
		return nil, err
	}
	if int64(len(source.view.Resources))*2*int64(unsafe.Sizeof(ResourceBlock{})) >
		min(source.limits.MaxMetadataBytes, source.limits.MaxDecodeBytes)-source.metadata {
		return nil, fileError(ErrLimit, "tiff.layers", "resources", 0, "owned resource metadata exceeds its budget")
	}
	return slices.Clone(source.view.Resources), nil
}

// ICCProfile returns the original owned TIFF profile bytes without converting saved color encoding.
func (source *TIFFLayerSource) ICCProfile(ctx context.Context) ([]byte, error) {
	ctx = decodeContext(ctx)
	if err := source.check(ctx); err != nil {
		return nil, err
	}
	if source.profileSpan.Length == 0 {
		return nil, nil
	}
	profile, err := source.ReadOriginalSpan(ctx, source.profileSpan)
	if err != nil {
		return nil, err
	}
	if len(profile) < 132 || int64(binary.BigEndian.Uint32(profile[:4])) != source.profileSpan.Length {
		return nil, fileError(ErrMalformed, "tiff.layers", "icc", source.profileSpan.Offset,
			"TIFF ICC declared length differs from its field size")
	}
	return profile, nil
}

// ReadOriginalSpan returns owned original TIFF bytes, leaving byte order and payloads unchanged.
func (source *TIFFLayerSource) ReadOriginalSpan(ctx context.Context, span Span) ([]byte, error) {
	return source.readSpan(ctx, span, false)
}

// ReadNormalizedSpan returns owned structural bytes with known TIFF unit reversals applied.
// A span intersecting an opaque block payload returns ErrUnsupported instead of mislabeling its wire bytes.
func (source *TIFFLayerSource) ReadNormalizedSpan(ctx context.Context, span Span) ([]byte, error) {
	return source.readSpan(ctx, span, true)
}

func (source *TIFFLayerSource) readSpan(ctx context.Context, span Span, normalized bool) ([]byte, error) {
	ctx = decodeContext(ctx)
	if err := source.check(ctx); err != nil {
		return nil, err
	}
	if !source.valid(span) {
		return nil, fileError(ErrMalformed, "tiff.layers", "span", span.Offset, "interval is outside this TIFF")
	}
	if span.Length > source.limits.MaxReadBytes || span.Length > source.limits.MaxMetadataBytes-source.metadata ||
		span.Length > source.limits.MaxDecodeBytes-source.metadata || span.Length > math.MaxInt {
		return nil, fileError(ErrLimit, "tiff.layers", "span", span.Offset, "owned span exceeds source budgets")
	}
	reader := io.ReaderAt(source.parent)
	if normalized {
		for _, block := range source.opaque {
			if span.Length != 0 && span.Offset < block.Data.Offset+block.Data.Length &&
				span.Offset+span.Length > block.Data.Offset {
				return nil, fileError(ErrUnsupported, "tiff.layers", "opaque."+block.Key, block.Data.Offset,
					"private payload has not been normalized for semantic decoding")
			}
		}
		reader = source.view
	}
	data := make([]byte, int(span.Length))
	count, err := reader.ReadAt(data, span.Offset)
	if cancellation := ctx.Err(); cancellation != nil {
		return nil, cancellation
	}
	if count != len(data) || err != nil && err != io.EOF { //nolint:errorlint // Only bare EOF ends a read; joined errors retain source faults.
		if err == nil || err == io.EOF { //nolint:errorlint // Only bare EOF ends a read; joined errors retain source faults.
			err = errors.Join(io.ErrUnexpectedEOF, err)
		}
		return nil, &Error{Kind: ErrMalformed, Feature: "tiff.layers", Path: "span", Offset: span.Offset,
			Detail: "original TIFF interval could not be read", Cause: err}
	}
	return data, nil
}

// SavedPreview decodes only the standard TIFF main IFD saved preview.
// This explicit preview operation retains the source's white matte when it was stored in the TIFF main image.
func (source *TIFFLayerSource) SavedPreview(
	ctx context.Context, options ...LinkedRasterOptions,
) (*TIFFSavedPreview, error) {
	ctx = decodeContext(ctx)
	if err := source.check(ctx); err != nil {
		return nil, err
	}
	source.view.mu.RLock()
	defer source.view.mu.RUnlock()
	if source.view.closed {
		return nil, ErrClosed
	}
	// Bind the operation to this view's lifetime while borrowing only original TIFF bytes.
	// The independent document lock avoids recursively locking the normalized view during reads.
	original := &Document{reader: source.parent, size: source.parent.size, limits: source.view.limits}
	settings := LinkedRasterOptions{MaxPixels: original.limits.MaxDecodePixels,
		MaxWorkingBytes: original.limits.MaxDecodeBytes, MaxProfileBytes: 4 << 20}
	if len(options) > 1 {
		return nil, fileError(ErrMalformed, "tiff.layers", "preview.options", 0, "at most one options value is accepted")
	}
	if len(options) == 1 {
		provided := options[0]
		if provided.MaxPixels < 0 || provided.MaxWorkingBytes < 0 || provided.MaxProfileBytes < 0 {
			return nil, fileError(ErrMalformed, "tiff.layers", "preview.options", 0, "limits must be nonnegative")
		}
		if provided.MaxPixels != 0 {
			settings.MaxPixels = min(settings.MaxPixels, provided.MaxPixels)
		}
		if provided.MaxWorkingBytes != 0 {
			settings.MaxWorkingBytes = min(settings.MaxWorkingBytes, provided.MaxWorkingBytes)
		}
		if provided.MaxProfileBytes != 0 {
			settings.MaxProfileBytes = min(settings.MaxProfileBytes, provided.MaxProfileBytes)
		}
	}
	decoder := linkedTIFF{document: original, context: ctx, span: source.record.Data, options: settings}
	if err := decoder.directory(); err != nil {
		return nil, err
	}
	decoder.fields = slices.DeleteFunc(decoder.fields, func(field linkedTIFFField) bool { return field.tag == 37724 })
	raster, err := decoder.decode()
	if err != nil {
		return nil, err
	}
	return &TIFFSavedPreview{Raster: raster, Kind: source.info.SavedPreviewKind}, nil
}
