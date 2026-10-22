package psdedit

import (
	"cmp"
	"encoding/binary"
	"math"
	"slices"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func (e *Editor) linkedBlockChangedLocked(block psdfile.AdditionalBlock) bool {
	if len(e.movedExternalRecordsLocked(block)) != 0 {
		return true
	}
	for _, record := range e.linkedRecords {
		if record.Block.Span == block.Span && record.Block.Data == block.Data && e.linkedOverlays[record.ID] != nil {
			return true
		}
	}
	return false
}

// writeLinkedBlockLocked borrows unchanged spans and the editor-owned content overlay without reading old content.
// Global additional blocks have four-byte outer alignment; layer-local blocks use two bytes.
func (e *Editor) writeLinkedBlockLocked(
	block psdfile.AdditionalBlock, global bool, limit int64,
) (writePlan, error) {
	plan := writePlan{limit: limit}
	if moved := e.movedExternalRecordsLocked(block); len(moved) != 0 {
		return e.writeMovedExternalBlockLocked(block, moved, global, limit)
	}
	if block.Key == "lnkE" && e.externalBlockEmbeddedLocked(block) && e.emptyEmbeddedContainerLocked() != nil {
		return plan, nil
	}
	if !e.linkedBlockChangedLocked(block) {
		plan.original(block.Span)
		return plan, plan.err
	}
	if !writerLinkedSpan(e.source.Source, block.Span) || !writerLinkedSpan(block.Span, block.Data) ||
		block.Data.Offset-block.Span.Offset != 8+int64(block.LengthWidth) ||
		(block.LengthWidth != 4 && block.LengthWidth != 8) {
		return plan, editError(ErrInvalidEdit, "save.linked.block", "linked block spans or length width are inconsistent")
	}
	metadataBytes := int64(128)
	var ordered []int
	for index, record := range e.linkedRecords {
		if record.Block.Span != block.Span || record.Block.Data != block.Data {
			continue
		}
		charge := int64(128)
		if e.linkedOverlays[record.ID] != nil {
			charge = 512
		}
		if charge > e.source.Limits().MaxMetadataBytes-metadataBytes {
			return plan, editError(psdfile.ErrLimit, "save.linked.metadata", "linked write-plan metadata exceeds its budget")
		}
		metadataBytes += charge
		ordered = append(ordered, index)
	}
	slices.SortFunc(ordered, func(first, second int) int {
		return cmp.Compare(e.linkedRecords[first].Span.Offset, e.linkedRecords[second].Span.Offset)
	})
	payload := writePlan{limit: limit}
	position := block.Data.Offset
	for _, index := range ordered {
		record := e.linkedRecords[index]
		if record.Span.Offset < position || !writerLinkedSpan(block.Data, record.Span) ||
			record.Padding.Offset != record.Span.Offset+record.Span.Length ||
			record.Padding.Length > 3 || !writerLinkedSpan(block.Data, record.Padding) {
			return plan, editError(ErrInvalidEdit, "save.linked.records", "linked record spans overlap or exceed their block")
		}
		payload.original(psdfile.Span{Offset: position, Length: record.Span.Offset - position})
		replacement := e.linkedOverlays[record.ID]
		if replacement == nil {
			payload.original(record.Span)
			payload.original(record.Padding)
		} else {
			changed, err := writerLinkedReplacement(record, replacement, limit)
			if err != nil {
				return plan, err
			}
			payload.append(changed)
		}
		if payload.err != nil {
			return plan, payload.err
		}
		position = record.Padding.Offset + record.Padding.Length
	}
	payload.original(psdfile.Span{
		Offset: position, Length: block.Data.Offset + block.Data.Length - position,
	})
	if payload.err != nil {
		return plan, payload.err
	}
	lengthWidth := block.LengthWidth
	converted := block.Key == "lnkE" && e.externalBlockEmbeddedLocked(block)
	if converted {
		plan.bytes([]byte(block.Signature + "lnk2"))
		if e.source.Header.Version == 2 {
			lengthWidth = 8
		}
	} else {
		plan.original(psdfile.Span{Offset: block.Span.Offset, Length: 8})
	}
	plan.length(lengthWidth, payload.size, "save.linked.length")
	plan.append(payload)
	writerAppendPadding(&plan, block.Padding, writerReplacementPadding(payload.size, global))
	return plan, plan.err
}

func writerLinkedRecord(
	record psdfile.LinkedFileRecord, data []byte, fileType string, limit int64,
) (writePlan, error) {
	plan := writePlan{limit: limit}
	if record.Kind != "liFD" || !record.HasData || record.Version < 1 || record.Version > 7 {
		return plan, editError(ErrUnsupported, "save.linked.kind", "only embedded linked-file content can be replaced")
	}
	if record.LengthData != (psdfile.Span{Offset: record.Span.Offset, Length: 8}) ||
		record.Body.Offset != record.Span.Offset+8 || record.Body.Length != record.Span.Length-8 ||
		record.DataSizeData.Length != 8 || !writerLinkedSpan(record.Body, record.Data) ||
		!writerLinkedSpan(record.Body, record.DataSizeData) ||
		record.DataSizeData.Offset+8 > record.Data.Offset ||
		record.Header.Offset != record.Body.Offset ||
		record.Header.Offset+record.Header.Length != record.Data.Offset {
		return plan, editError(ErrInvalidEdit, "save.linked.record", "embedded content spans are inconsistent")
	}
	retained := record.Body.Length - record.Data.Length
	if retained < 0 || int64(len(data)) > math.MaxInt64-retained {
		return plan, editError(psdfile.ErrLimit, "save.linked.length", "replacement linked-file length overflows")
	}
	length := retained + int64(len(data))
	plan.length(8, length, "save.linked.record.length")
	if fileType == "" {
		plan.original(psdfile.Span{
			Offset: record.Body.Offset, Length: record.DataSizeData.Offset - record.Body.Offset,
		})
	} else {
		fileTypeOffset := record.NameData.Offset + record.NameData.Length
		if len(fileType) != 4 || !writerLinkedSpan(record.Header, record.NameData) ||
			fileTypeOffset > record.DataSizeData.Offset-4 {
			return plan, editError(ErrInvalidEdit, "save.linked.fileType", "source file type span is inconsistent")
		}
		plan.original(psdfile.Span{Offset: record.Body.Offset, Length: fileTypeOffset - record.Body.Offset})
		plan.bytes([]byte(fileType))
		plan.original(psdfile.Span{Offset: fileTypeOffset + 4, Length: record.DataSizeData.Offset - fileTypeOffset - 4})
	}
	plan.bytes(binary.BigEndian.AppendUint64(nil, uint64(len(data))))
	plan.original(psdfile.Span{
		Offset: record.DataSizeData.Offset + 8, Length: record.Data.Offset - record.DataSizeData.Offset - 8,
	})
	plan.addPart(writePart{data: data, content: true})
	position := record.Data.Offset + record.Data.Length
	plan.original(psdfile.Span{Offset: position, Length: record.Body.Offset + record.Body.Length - position})
	writerAppendPadding(&plan, record.Padding, (4-length%4)%4)
	return plan, plan.err
}

func writerLinkedSpan(parent, child psdfile.Span) bool {
	return parent.Offset >= 0 && parent.Length >= 0 && child.Offset >= parent.Offset && child.Length >= 0 &&
		child.Offset-parent.Offset <= parent.Length && child.Length <= parent.Length-(child.Offset-parent.Offset)
}
