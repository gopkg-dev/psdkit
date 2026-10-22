package psdfile

import (
	"context"
	"strings"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdtext"
)

// DocumentTextBlock retains a document-wide Txt2 block without reading its payload.
// EngineData identifies the tagged-block data directly; Txt2 has no additional inner length prefix.
type DocumentTextBlock struct {
	Block      AdditionalBlock
	EngineData Span
}

// DocumentTextView reports lazy document text blocks and their metadata allocation reservation.
type DocumentTextView struct {
	Records       []DocumentTextBlock
	MetadataBytes int64
}

// DocumentTextAssociation pairs a physical TySh layer with its checked document text record.
type DocumentTextAssociation struct {
	Layer TextLayerRecord
	Text  *psdtext.DocumentTextRecord
}

// DocumentTextAssociationView owns layer descriptors and borrows the supplied document engine records.
// MetadataBytes reserves its layer decoding, association slice and uniqueness-check storage.
type DocumentTextAssociationView struct {
	Records       []DocumentTextAssociation
	MetadataBytes int64
}

// ReadDocumentTextAssociations checks unique layer indices without reading EngineData or raster channels.
// Within each layer set, duplicate TySh entries and repeated TextIndex references return ErrUnsupported.
// Different physical layer sets may share an index only when their TySh text matches the document record.
func (d *Document) ReadDocumentTextAssociations(
	ctx context.Context, metadata *psdtext.DocumentMetadata, options ...MetadataOptions,
) (DocumentTextAssociationView, error) {
	result := DocumentTextAssociationView{}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := d.ReadAt(nil, 0); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	limits, err := d.metadataLimits(options)
	if err != nil {
		return result, err
	}
	if metadata == nil || metadata.Schema != 7 {
		return result, fileError(ErrMalformed, "text", "documentText.associations", 0,
			"schema-7 document text metadata is required")
	}
	layers, err := d.ReadTextLayerView(ctx, options...)
	if err != nil {
		return result, err
	}
	result.MetadataBytes = layers.MetadataBytes + int64(unsafe.Sizeof(DocumentTextAssociationView{})) + 64
	budget := min(limits.MaxMetadataBytes, limits.MaxDecodeBytes)
	remaining := budget - result.MetadataBytes
	cost := int64(unsafe.Sizeof(DocumentTextAssociation{})) + 64
	if remaining < 0 || int64(len(layers.Records)) > remaining/cost {
		return result, fileError(ErrLimit, "text", "documentText.associations", 0, "association view exceeds metadata budget")
	}
	result.MetadataBytes += int64(len(layers.Records)) * cost
	result.Records = make([]DocumentTextAssociation, len(layers.Records))
	type associationKey struct {
		setIndex  int
		textIndex int32
	}
	indices := make(map[associationKey]struct{}, len(layers.Records))
	for index, layer := range layers.Records {
		text, err := layer.ResolveDocumentText(ctx, metadata)
		if err != nil {
			return result, err
		}
		key := associationKey{setIndex: layer.LayerSetIndex, textIndex: layer.TextIndex}
		if _, exists := indices[key]; exists {
			return result, fileError(ErrUnsupported, "text", "documentText.associations", layer.Block.Data.Offset,
				"multiple physical text blocks in one layer set reference one document text record")
		}
		indices[key] = struct{}{}
		result.Records[index] = DocumentTextAssociation{Layer: layer, Text: text}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, nil
}

// ReadDocumentTextView indexes global Txt2 blocks in source order without reading text or raster data.
// Duplicate blocks remain separately available and are never silently collapsed or removed.
func (d *Document) ReadDocumentTextView(
	ctx context.Context, options ...MetadataOptions,
) (DocumentTextView, error) {
	result := DocumentTextView{}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := d.ReadAt(nil, 0); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	limits, err := d.metadataLimits(options)
	if err != nil {
		return result, err
	}
	result.MetadataBytes = int64(unsafe.Sizeof(DocumentTextView{}))
	budget := min(limits.MaxMetadataBytes, limits.MaxDecodeBytes)
	if result.MetadataBytes > budget {
		return result, fileError(ErrLimit, "text", "documentText.blocks", 0, "view exceeds metadata budget")
	}
	for _, block := range d.AdditionalInfo {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if block.Key != "Txt2" {
			continue
		}
		if len(result.Records) >= limits.MaxBlocks {
			return result, fileError(ErrLimit, "text", "documentText.blocks", block.Data.Offset, "block count exceeds limits")
		}
		if len(result.Records) == cap(result.Records) {
			capacity := max(1, cap(result.Records)*2)
			reservation := int64(capacity) * int64(unsafe.Sizeof(DocumentTextBlock{}))
			if reservation > budget-result.MetadataBytes {
				return result, fileError(ErrLimit, "text", "documentText.blocks", block.Data.Offset, "block view exceeds budget")
			}
			result.MetadataBytes += reservation
			owned := make([]DocumentTextBlock, len(result.Records), capacity)
			copy(owned, result.Records)
			result.Records = owned
		}
		result.Records = append(result.Records, DocumentTextBlock{Block: block, EngineData: block.Data})
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, nil
}

// DecodeDocumentTextEngine explicitly reads one retained Txt2 payload with source and caller limits combined.
// Parsing owns the exact original bytes and spans; it does not shape glyphs or infer private schema fields.
func (d *Document) DecodeDocumentTextEngine(
	ctx context.Context, record DocumentTextBlock, options ...psdtext.DecodeOptions,
) (*psdtext.EngineData, error) {
	return d.decodeEngineData(ctx, record.EngineData, true, options...)
}

// ResolveDocumentText checks one unambiguous TySh TextIndex and its matching schema-7 Txt2 record.
// Metadata borrows the supplied owned engine tree, which must remain read-only while this association is used.
// No absent index, terminal paragraph marker or differing text is invented to force a match.
func (record TextLayerRecord) ResolveDocumentText(
	ctx context.Context, metadata *psdtext.DocumentMetadata,
) (*psdtext.DocumentTextRecord, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	count, textCount := 0, 0
	var indexEntry, textEntry *DescriptorEntry
	if record.TextDescriptor != nil {
		for index, entry := range record.TextDescriptor.Entries {
			if index%1024 == 0 && ctx != nil {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			if entry.Key == "TextIndex" {
				count++
				indexEntry = &record.TextDescriptor.Entries[index]
			}
			if entry.Key == "Txt " {
				textCount++
				textEntry = &record.TextDescriptor.Entries[index]
			}
		}
	}
	if count != 1 || !record.HasTextIndex {
		return nil, fileError(ErrUnsupported, "text", "textLayers.TextIndex", record.Block.Data.Offset,
			"one explicit unambiguous TextIndex is required")
	}
	if textCount != 1 {
		return nil, fileError(ErrUnsupported, "text", "textLayers.text", record.Block.Data.Offset,
			"one explicit unambiguous Unicode text property is required")
	}
	currentIndex, valid := indexEntry.Value.(int32)
	if indexEntry.Type != "long" || !valid || currentIndex < 0 || currentIndex != record.TextIndex {
		return nil, fileError(ErrMalformed, "text", "textLayers.TextIndex", indexEntry.Span.Offset,
			"current TextIndex descriptor value disagrees with the typed record")
	}
	currentText, valid := textEntry.Value.(string)
	if textEntry.Type != "TEXT" || !valid || currentText != record.Text {
		return nil, fileError(ErrMalformed, "text", "textLayers.text", textEntry.Span.Offset,
			"current Unicode text descriptor value disagrees with the typed record")
	}
	if metadata == nil || metadata.Schema != 7 || record.TextIndex < 0 || int64(record.TextIndex) >= int64(len(metadata.Texts)) {
		return nil, fileError(ErrMalformed, "text", "textLayers.TextIndex", record.Block.Data.Offset,
			"TextIndex is outside the schema-7 document text records")
	}
	result := &metadata.Texts[record.TextIndex]
	if result.Index != int(record.TextIndex) || !strings.HasSuffix(result.Text, "\r") ||
		result.Text[:len(result.Text)-1] != record.Text {
		return nil, fileError(ErrMalformed, "text", "textLayers.TextIndex", record.Block.Data.Offset,
			"document text content does not match the indexed TySh text")
	}
	return result, nil
}
