package psdfile

import (
	"context"
	"encoding/binary"
	"math"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdtext"
)

func (builder *tiffPrivateBuilder) normalizeText(span Span) error {
	cursor := tiffPrivateCursor{builder: builder, position: span.Offset, end: span.Offset + span.Length,
		path: "private.TySh"}
	if cursor.u16() != 1 {
		cursor.fail(ErrUnsupported, "unknown mixed-order TIFF text version")
	}
	for range 6 {
		value := math.Float64frombits(cursor.u64())
		if math.IsNaN(value) || math.IsInf(value, 0) {
			cursor.fail(ErrMalformed, "TIFF text transform contains a non-finite coordinate")
		}
	}
	var textVersion [2]byte
	cursor.read(textVersion[:], 0)
	if binary.BigEndian.Uint16(textVersion[:]) != 50 {
		cursor.fail(ErrUnsupported, "mixed-order TIFF text descriptor version is not big-endian 50")
	}
	cursor.descriptorOrder(true, binary.BigEndian)
	if cursor.u16() != 1 {
		cursor.fail(ErrUnsupported, "unknown little-endian TIFF text warp version")
	}
	cursor.descriptor(true)
	var bounds [16]byte
	cursor.read(bounds[:], 0)
	if cursor.err != nil {
		return cursor.err
	}
	// The retained real-source TySh records contain sixteen zero bounds bytes.
	// They have identical float bits in both orders; other bounds require independent native evidence.
	for _, value := range bounds {
		if value != 0 {
			cursor.fail(ErrUnsupported, "nonzero mixed-order TIFF text bounds require an independently verified schema")
			break
		}
	}
	cursor.padding(3)
	return cursor.err
}

// ReadTextLayerView returns owned mixed-order TIFF TySh metadata without reading saved pixels or EngineData.
// It does not resolve fonts, render glyphs or reconstruct live private-layer contents.
func (source *TIFFLayerSource) ReadTextLayerView(
	ctx context.Context, options ...MetadataOptions,
) (TextLayerView, error) {
	ctx = decodeContext(ctx)
	if err := source.check(ctx); err != nil {
		return TextLayerView{}, err
	}
	return source.view.ReadTextLayerView(ctx, options...)
}

// DecodeTextEngine reads only a selected original TIFF EngineData blob and returns an owned typed engine tree.
// Byte order normalization never changes PostScript-like EngineData wire bytes or string escapes.
func (source *TIFFLayerSource) DecodeTextEngine(
	ctx context.Context, record TextLayerRecord, options ...psdtext.DecodeOptions,
) (*psdtext.EngineData, error) {
	ctx = decodeContext(ctx)
	if err := source.check(ctx); err != nil {
		return nil, err
	}
	if !record.HasEngineData {
		return nil, fileError(ErrUnsupported, "tiff.layers", "text.engineData", record.Block.Data.Offset,
			"selected TIFF text layer has no EngineData blob")
	}
	if !source.valid(record.EngineData) {
		return nil, fileError(ErrMalformed, "tiff.layers", "text.engineData", record.EngineData.Offset,
			"EngineData is outside this TIFF source")
	}
	limits := source.view.Limits()
	reservation := int64(unsafe.Sizeof(Document{})) + int64(unsafe.Sizeof(tiffPrivateOriginalReader{}))
	if reservation >= min(limits.MaxMetadataBytes, limits.MaxDecodeBytes) {
		return nil, fileError(ErrLimit, "tiff.layers", "text.engineData", record.EngineData.Offset,
			"original EngineData reader exhausts remaining working budgets")
	}
	limits.MaxMetadataBytes -= reservation
	limits.MaxDecodeBytes -= reservation
	original := &Document{reader: &tiffPrivateOriginalReader{source: source}, size: source.parent.size, limits: limits}
	return original.DecodeTextEngine(ctx, record, options...)
}
