package psdedit

import (
	"context"
	"encoding/binary"
	"errors"
	"math"

	"github.com/gopkg-dev/psdkit/psdfile"
)

// ErrStaleLinkedEdit identifies editor changes made while an external resolver was running.
var ErrStaleLinkedEdit = errors.New("PSD editor changed while resolving linked contents")

// LinkedContentsResolver supplies encoded PSD/PSB, PNG, JPEG or TIFF bytes for an explicitly selected external link.
// Metadata identifiers are opaque and should not be logged. The library never opens the retained path.
// Returned bytes remain caller-owned and are copied before committing the edit.
type LinkedContentsResolver func(context.Context, psdfile.LinkedFileRecord) ([]byte, error)

// EmbedLinkedContents resolves and embeds a supported external raster source for all shared instances.
// Destination corners remain fixed; the source and projected caches become owned editor overlays.
// The resolver runs without the editor lock. Concurrent edits cause ErrStaleLinkedEdit without a commit.
func (e *Editor) EmbedLinkedContents(ctx context.Context, id LayerID, resolver LinkedContentsResolver) error {
	return e.EmbedLinkedContentsWithOptions(ctx, id, resolver, SmartContentsOptions{})
}

// EmbedLinkedContentsWithOptions embeds an external source using explicit color, Warp and filter policies.
// The zero value preserves the strict rendering behavior and atomic guarantees of EmbedLinkedContents.
func (e *Editor) EmbedLinkedContentsWithOptions(
	ctx context.Context, id LayerID, resolver LinkedContentsResolver, options SmartContentsOptions,
) error {
	options = cloneSmartContentsOptions(options)
	if ctx == nil {
		ctx = context.Background()
	}
	if resolver == nil {
		return editError(ErrInvalidEdit, "linked.embed.resolver", "an explicit encoded-content resolver is required")
	}
	e.mu.RLock()
	if err := e.checkMutableLocked(); err != nil {
		e.mu.RUnlock()
		return err
	}
	if err := e.rgbCompositeSupportedLocked(); err != nil {
		e.mu.RUnlock()
		return err
	}
	if err := ctx.Err(); err != nil {
		e.mu.RUnlock()
		return err
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		e.mu.RUnlock()
		return err
	}
	limit := e.source.Limits().MaxDecodeBytes - e.ownedBytesLocked(nil)
	if limit <= 0 {
		e.mu.RUnlock()
		return editError(psdfile.ErrLimit, "linked.embed.metadata", "no working budget remains for link metadata")
	}
	placements, err := e.source.ReadSmartObjectView(ctx, psdfile.MetadataOptions{MaxBytes: limit})
	if err != nil {
		e.mu.RUnlock()
		return err
	}
	identity := ""
	for _, placement := range placements.Records {
		if placement.LayerSetIndex != e.source.ActiveLayerSet || placement.LayerIndex != node.sourceIndex {
			continue
		}
		if placement.SourceType != 2 || identity != "" && identity != placement.SourceID {
			e.mu.RUnlock()
			return editError(ErrUnsupported, "linked.embed.instance", "placement has unsupported or ambiguous contents")
		}
		identity = placement.SourceID
	}
	if identity == "" || node.group {
		e.mu.RUnlock()
		return editError(ErrUnsupported, "linked.embed.instance", "target is not a modern raster smart object")
	}
	if previous := e.linkedOverlays[identity]; previous != nil && previous.embedExternal {
		e.mu.RUnlock()
		return editError(ErrInvalidEdit, "linked.embed.instance", "source is already embedded")
	}
	limit -= placements.MetadataBytes
	if limit <= 0 {
		e.mu.RUnlock()
		return editError(psdfile.ErrLimit, "linked.embed.metadata", "linked metadata exceeds the working budget")
	}
	linked, err := e.source.ReadLinkedFileView(ctx, psdfile.MetadataOptions{MaxBytes: limit})
	if err != nil {
		e.mu.RUnlock()
		return err
	}
	if err := validateExternalEmbed(e.source, identity, linked.Records); err != nil {
		e.mu.RUnlock()
		return err
	}
	var selected psdfile.LinkedFileRecord
	for _, record := range linked.Records {
		if record.ID == identity {
			selected = record
			break
		}
	}
	revision, source := e.revision, e.source
	placements.Records, linked.Records = nil, nil
	e.mu.RUnlock()
	data, err := resolver(ctx, selected)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.checkMutableLocked(); err != nil {
		return err
	}
	if e.revision != revision || e.source != source {
		return ErrStaleLinkedEdit
	}
	return e.replaceSmartContentsWithOptionsLocked(ctx, id, data, true, nil, options)
}

func validateExternalEmbed(source *psdfile.Document, identity string, records []psdfile.LinkedFileRecord) error {
	var selected *psdfile.LinkedFileRecord
	for index := range records {
		if records[index].ID != identity {
			continue
		}
		if selected != nil {
			return editError(ErrUnsupported, "linked.embed.source", "source reference has duplicate linked records")
		}
		selected = &records[index]
	}
	if selected == nil || selected.Kind != "liFE" || selected.Version < 1 || selected.Version > 7 ||
		!supportedSmartContentsType(selected.FileType) {
		return editError(ErrUnsupported, "linked.embed.source", "a supported external raster record is required")
	}
	if selected.Block.Key != "lnkE" {
		return nil
	}
	scope, ok := linkedContainerScope(source, selected.Block)
	if !ok || scope.layerSet >= 0 && scope.layerSet != source.ActiveLayerSet {
		return editError(ErrUnsupported, "linked.embed.container", "external container is outside the active layer set")
	}
	return nil
}

func writerLinkedReplacement(record psdfile.LinkedFileRecord, replacement *linkedReplacement,
	limit int64,
) (writePlan, error) {
	if replacement.retainExternal {
		return writerExternalLinkedRecord(record, replacement, limit)
	}
	if replacement.embedExternal && record.Kind == "liFE" {
		return writerEmbeddedExternalRecord(record, replacement.data, replacement.fileType, limit)
	}
	return writerLinkedRecord(record, replacement.data, replacement.fileType, limit)
}

func writerEmbeddedExternalRecord(record psdfile.LinkedFileRecord, data []byte, fileType string,
	limit int64,
) (writePlan, error) {
	plan := writePlan{limit: limit}
	if record.Kind != "liFE" || record.Version < 1 || record.Version > 7 || len(fileType) != 4 {
		return plan, editError(ErrUnsupported, "save.linked.embed", "external record or target format is unsupported")
	}
	prefixEnd := record.FileOpenFlagData.Offset + 1
	if record.FileOpenDescriptor.Length != 0 {
		prefixEnd = record.FileOpenDescriptor.Offset + record.FileOpenDescriptor.Length
	}
	fileTypeOffset := record.NameData.Offset + record.NameData.Length
	tailStart := record.Header.Offset + record.Header.Length
	if record.HasData {
		tailStart = record.Data.Offset + record.Data.Length
	}
	bodyEnd := record.Body.Offset + record.Body.Length
	if record.Body.Offset != record.Span.Offset+8 || record.Body.Length != record.Span.Length-8 ||
		record.LengthData != (psdfile.Span{Offset: record.Span.Offset, Length: 8}) ||
		record.Header.Offset != record.Body.Offset || record.DataSizeData.Length != 8 ||
		!writerLinkedSpan(record.Body, record.Header) ||
		record.FileOpenFlagData.Length != 1 || prefixEnd != record.ExternalDescriptor.Offset ||
		!writerLinkedSpan(record.Header, record.ExternalDescriptor) ||
		!writerLinkedSpan(record.Header, record.DataSizeData) || !writerLinkedSpan(record.Header, record.NameData) ||
		fileTypeOffset < record.Body.Offset+8 || fileTypeOffset+8 != record.DataSizeData.Offset ||
		prefixEnd < record.DataSizeData.Offset+8 || tailStart < prefixEnd || tailStart > bodyEnd {
		return plan, editError(ErrInvalidEdit, "save.linked.embed", "external record spans are inconsistent")
	}
	if record.HasData && !writerLinkedSpan(record.Body, record.Data) {
		return plan, editError(ErrInvalidEdit, "save.linked.embed", "external cache span is inconsistent")
	}
	externalEnd := record.ExternalDescriptor.Offset + record.ExternalDescriptor.Length
	if record.Version > 3 {
		if record.Timestamp == nil || record.Timestamp.Span != (psdfile.Span{Offset: externalEnd, Length: 16}) {
			return plan, editError(ErrInvalidEdit, "save.linked.embed", "external timestamp span is inconsistent")
		}
		externalEnd += 16
	}
	if record.ExternalFileSizeData != (psdfile.Span{Offset: externalEnd, Length: 8}) ||
		externalEnd+8 != record.Header.Offset+record.Header.Length ||
		record.HasData && record.Data.Offset != record.Header.Offset+record.Header.Length {
		return plan, editError(ErrInvalidEdit, "save.linked.embed", "external-only header spans are inconsistent")
	}
	prefixBytes, tailBytes := prefixEnd-record.Body.Offset, bodyEnd-tailStart
	if prefixBytes > math.MaxInt64-tailBytes || int64(len(data)) > math.MaxInt64-prefixBytes-tailBytes {
		return plan, editError(psdfile.ErrLimit, "save.linked.embed", "embedded record size overflows")
	}
	length := prefixBytes + int64(len(data)) + tailBytes
	plan.length(8, length, "save.linked.record.length")
	plan.bytes([]byte("liFD"))
	plan.original(psdfile.Span{Offset: record.Body.Offset + 4, Length: fileTypeOffset - record.Body.Offset - 4})
	plan.bytes([]byte(fileType))
	plan.original(psdfile.Span{Offset: fileTypeOffset + 4, Length: record.DataSizeData.Offset - fileTypeOffset - 4})
	plan.bytes(binary.BigEndian.AppendUint64(nil, uint64(len(data))))
	plan.original(psdfile.Span{Offset: record.DataSizeData.Offset + 8, Length: prefixEnd - record.DataSizeData.Offset - 8})
	plan.addPart(writePart{data: data, content: true})
	plan.original(psdfile.Span{Offset: tailStart, Length: tailBytes})
	writerAppendPadding(&plan, record.Padding, (4-length%4)%4)
	return plan, plan.err
}
