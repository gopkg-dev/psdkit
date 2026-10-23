package psdedit

import (
	"cmp"
	"context"
	"encoding/binary"
	"math"
	"slices"
	"time"

	"github.com/gopkg-dev/psdkit/psdfile"
)

// LinkedContentsUpdate provides a supported external raster source's exact contents and optional file timestamp.
// Data and Timestamp remain caller-owned and are copied before committing the edit.
// Timestamp uses the retained calendar fields without inferring a filesystem path or timezone.
// A nil Timestamp preserves the current editor value; a non-nil value requires linked-record version four or later.
type LinkedContentsUpdate struct {
	Data      []byte
	Timestamp *psdfile.LinkedFileTimestamp
}

// LinkedContentsUpdateResolver reads an explicitly selected external raster source through caller-owned access.
// The library does not open, create or overwrite any external path retained in the document.
// To persist a changed external file, the caller writes Data through its own authorized storage workflow.
// The supplied record and its spans describe the immutable input document, including on subsequent updates.
type LinkedContentsUpdateResolver func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error)

// UpdateLinkedContents refreshes all shared instances while retaining the external link and its path metadata.
// It refreshes file-size metadata, optionally its timestamp and existing nonempty linked content caches.
// Zero-length linked caches remain empty, matching native PSD/PSB external-link storage.
// The editor retains owned source bytes for live access and rebuilds every shared instance's raster cache.
// Destination corners remain fixed, including when the replacement canvas changes dimensions.
// The resolver runs without the editor lock; a concurrent edit causes ErrStaleLinkedEdit and commits nothing.
// Parent-document saving and external-file persistence are separate caller-controlled operations.
func (e *Editor) UpdateLinkedContents(
	ctx context.Context, id LayerID, resolver LinkedContentsUpdateResolver,
) error {
	return e.UpdateLinkedContentsWithOptions(ctx, id, resolver, SmartContentsOptions{})
}

// UpdateLinkedContentsWithOptions refreshes an external source with explicit source-color and Warp policies.
// Paths and external-file persistence remain caller-controlled, with the same stale-edit protection.
func (e *Editor) UpdateLinkedContentsWithOptions(
	ctx context.Context, id LayerID, resolver LinkedContentsUpdateResolver, options SmartContentsOptions,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if resolver == nil {
		return editError(ErrInvalidEdit, "linked.update.resolver", "an explicit external-content resolver is required")
	}
	e.mu.RLock()
	selected, revision, source, err := e.selectLinkedUpdateLocked(ctx, id)
	e.mu.RUnlock()
	if err != nil {
		return err
	}
	update, err := resolver(ctx, selected)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if update.Timestamp != nil {
		value := *update.Timestamp
		value.Span = psdfile.Span{}
		update.Timestamp = &value
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.checkMutableLocked(); err != nil {
		return err
	}
	if e.revision != revision || e.source != source {
		return ErrStaleLinkedEdit
	}
	return e.replaceSmartContentsWithOptionsLocked(ctx, id, update.Data, false, &update, options)
}

func (e *Editor) selectLinkedUpdateLocked(
	ctx context.Context, id LayerID,
) (psdfile.LinkedFileRecord, uint64, *psdfile.Document, error) {
	empty := psdfile.LinkedFileRecord{}
	if err := e.checkMutableLocked(); err != nil {
		return empty, 0, nil, err
	}
	if err := e.rgbCompositeSupportedLocked(); err != nil {
		return empty, 0, nil, err
	}
	if err := ctx.Err(); err != nil {
		return empty, 0, nil, err
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return empty, 0, nil, err
	}
	limit := e.source.Limits().MaxDecodeBytes - e.ownedBytesLocked(nil)
	if limit <= 0 {
		return empty, 0, nil, editError(psdfile.ErrLimit, "linked.update.metadata", "no metadata budget remains")
	}
	placements, err := e.source.ReadSmartObjectView(ctx, psdfile.MetadataOptions{MaxBytes: limit})
	if err != nil {
		return empty, 0, nil, err
	}
	identity := ""
	for _, placement := range placements.Records {
		if placement.LayerSetIndex != e.source.ActiveLayerSet || placement.LayerIndex != node.sourceIndex {
			continue
		}
		if placement.SourceType != 2 || identity != "" && identity != placement.SourceID {
			return empty, 0, nil, editError(ErrUnsupported, "linked.update.instance", "ambiguous contents or source type")
		}
		identity = placement.SourceID
	}
	if identity == "" || node.group {
		return empty, 0, nil, editError(ErrUnsupported, "linked.update.instance", "target is not a raster smart object")
	}
	if previous := e.linkedOverlays[identity]; previous != nil && previous.embedExternal {
		return empty, 0, nil, editError(ErrInvalidEdit, "linked.update.instance", "source is already embedded")
	}
	limit -= placements.MetadataBytes
	if limit <= 0 {
		return empty, 0, nil, editError(psdfile.ErrLimit, "linked.update.metadata", "linked metadata exceeds budget")
	}
	linked, err := e.source.ReadLinkedFileView(ctx, psdfile.MetadataOptions{MaxBytes: limit})
	if err != nil {
		return empty, 0, nil, err
	}
	selected, err := validateLinkedUpdate(identity, linked.Records, nil)
	return selected, e.revision, e.source, err
}

func validateLinkedUpdate(
	identity string, records []psdfile.LinkedFileRecord, timestamp *psdfile.LinkedFileTimestamp,
) (psdfile.LinkedFileRecord, error) {
	var selected *psdfile.LinkedFileRecord
	for index := range records {
		if records[index].ID != identity {
			continue
		}
		if selected != nil {
			return psdfile.LinkedFileRecord{}, editError(ErrUnsupported, "linked.update.source", "duplicate source records")
		}
		selected = &records[index]
	}
	if selected == nil || selected.Kind != "liFE" || selected.Version < 1 || selected.Version > 7 ||
		!supportedSmartContentsType(selected.FileType) {
		return psdfile.LinkedFileRecord{}, editError(
			ErrUnsupported, "linked.update.source", "supported external raster required",
		)
	}
	if timestamp != nil && (selected.Version < 4 || !validLinkedTimestamp(*timestamp)) {
		return psdfile.LinkedFileRecord{}, editError(
			ErrInvalidEdit, "linked.update.timestamp", "invalid timestamp or version",
		)
	}
	return *selected, nil
}

func validLinkedTimestamp(value psdfile.LinkedFileTimestamp) bool {
	if value.Year == 0 || value.Year > 9999 || value.Month == 0 || value.Month > 12 ||
		value.Day == 0 || value.Day > 31 || value.Hour > 23 || value.Minute > 59 ||
		math.IsNaN(value.Seconds) || math.IsInf(value.Seconds, 0) || value.Seconds < 0 || value.Seconds >= 60 {
		return false
	}
	calendar := time.Date(int(value.Year), time.Month(value.Month), int(value.Day), 0, 0, 0, 0, time.UTC)
	return calendar.Month() == time.Month(value.Month) && calendar.Day() == int(value.Day)
}

type externalLinkedPatch struct {
	span    psdfile.Span
	data    []byte
	content bool
}

func linkedReplacementOwnedBytes(replacement *linkedReplacement) int64 {
	length := int64(len(replacement.data))
	if replacement.timestamp != nil {
		length += 64
	}
	return length
}

func writerExternalLinkedRecord(
	record psdfile.LinkedFileRecord, replacement *linkedReplacement, limit int64,
) (writePlan, error) {
	plan := writePlan{limit: limit}
	if record.Kind != "liFE" || record.Version < 1 || record.Version > 7 || len(replacement.fileType) != 4 {
		return plan, editError(ErrUnsupported, "save.linked.external", "external record or file type is unsupported")
	}
	if record.LengthData != (psdfile.Span{Offset: record.Span.Offset, Length: 8}) ||
		record.Body.Offset != record.Span.Offset+8 || record.Body.Length != record.Span.Length-8 ||
		record.Header.Offset != record.Body.Offset || !writerLinkedSpan(record.Body, record.Header) ||
		record.DataSizeData.Length != 8 || record.ExternalFileSizeData.Length != 8 ||
		!writerLinkedSpan(record.Header, record.DataSizeData) ||
		!writerLinkedSpan(record.Header, record.ExternalFileSizeData) ||
		!writerLinkedSpan(record.Header, record.NameData) ||
		record.HasData != (record.Version >= 2) ||
		(record.HasData && (!writerLinkedSpan(record.Body, record.Data) || uint64(record.Data.Length) != record.DataSize)) {
		return plan, editError(ErrInvalidEdit, "save.linked.external", "external record spans are inconsistent")
	}
	fileTypeOffset := record.NameData.Offset + record.NameData.Length
	if fileTypeOffset+8 != record.DataSizeData.Offset || record.FileOpenFlagData.Length != 1 ||
		record.FileOpenFlagData.Offset != record.DataSizeData.Offset+8 ||
		!writerLinkedSpan(record.Header, record.ExternalDescriptor) || record.ExternalDescriptor.Length < 4 {
		return plan, editError(ErrInvalidEdit, "save.linked.external", "external format span is inconsistent")
	}
	externalStart := record.FileOpenFlagData.Offset + 1
	if record.FileOpenDescriptor.Length != 0 {
		if record.FileOpenDescriptor.Offset != externalStart ||
			!writerLinkedSpan(record.Header, record.FileOpenDescriptor) {
			return plan, editError(ErrInvalidEdit, "save.linked.external", "file-open descriptor is inconsistent")
		}
		externalStart += record.FileOpenDescriptor.Length
	}
	if record.ExternalDescriptor.Offset != externalStart {
		return plan, editError(ErrInvalidEdit, "save.linked.external", "external descriptor position is inconsistent")
	}
	externalEnd := record.ExternalDescriptor.Offset + record.ExternalDescriptor.Length
	if record.Version >= 4 {
		if record.Timestamp == nil || record.Timestamp.Span != (psdfile.Span{Offset: externalEnd, Length: 16}) {
			return plan, editError(ErrInvalidEdit, "save.linked.external", "external timestamp span is inconsistent")
		}
		externalEnd += 16
	}
	if record.ExternalFileSizeData.Offset != externalEnd ||
		externalEnd+8 != record.Header.Offset+record.Header.Length ||
		(record.HasData && record.Data.Offset != record.Header.Offset+record.Header.Length) {
		return plan, editError(ErrInvalidEdit, "save.linked.external", "external-only header spans are inconsistent")
	}
	cache := replacement.data
	if !record.HasData || record.Data.Length == 0 {
		cache = nil
	}
	patches := []externalLinkedPatch{
		{span: psdfile.Span{Offset: fileTypeOffset, Length: 4}, data: []byte(replacement.fileType)},
		{span: record.DataSizeData, data: binary.BigEndian.AppendUint64(nil, uint64(len(cache)))},
		{span: record.ExternalFileSizeData, data: binary.BigEndian.AppendUint64(nil, uint64(len(replacement.data)))},
	}
	if record.HasData {
		patches = append(patches, externalLinkedPatch{span: record.Data, data: cache, content: true})
	}
	if replacement.timestamp != nil {
		if record.Version < 4 || record.Timestamp == nil || record.Timestamp.Span.Length != 16 ||
			!validLinkedTimestamp(*replacement.timestamp) || !writerLinkedSpan(record.Header, record.Timestamp.Span) {
			return plan, editError(ErrInvalidEdit, "save.linked.timestamp", "external timestamp is inconsistent")
		}
		value := replacement.timestamp
		data := binary.BigEndian.AppendUint32(nil, value.Year)
		data = append(data, value.Month, value.Day, value.Hour, value.Minute)
		data = binary.BigEndian.AppendUint64(data, math.Float64bits(value.Seconds))
		patches = append(patches, externalLinkedPatch{span: record.Timestamp.Span, data: data})
	}
	slices.SortFunc(patches, func(first, second externalLinkedPatch) int {
		return cmp.Compare(first.span.Offset, second.span.Offset)
	})
	retained := record.Body.Length - record.Data.Length
	if !record.HasData {
		retained = record.Body.Length
	}
	if retained < 0 || int64(len(cache)) > math.MaxInt64-retained {
		return plan, editError(psdfile.ErrLimit, "save.linked.length", "external record length overflows")
	}
	length := retained + int64(len(cache))
	plan.length(8, length, "save.linked.length")
	position := record.Body.Offset
	for _, patch := range patches {
		if patch.span.Offset < position || !writerLinkedSpan(record.Body, patch.span) {
			return plan, editError(ErrInvalidEdit, "save.linked.external", "external patch spans overlap")
		}
		plan.original(psdfile.Span{Offset: position, Length: patch.span.Offset - position})
		plan.addPart(writePart{data: patch.data, content: patch.content})
		position = patch.span.Offset + patch.span.Length
	}
	plan.original(psdfile.Span{Offset: position, Length: record.Body.Offset + record.Body.Length - position})
	writerAppendPadding(&plan, record.Padding, (4-length%4)%4)
	return plan, plan.err
}
