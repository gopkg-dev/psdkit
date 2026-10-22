package psdfile

import (
	"cmp"
	"context"
	"errors"
	"math"
	"slices"
	"unsafe"
)

// LinkedFileTimestamp preserves an external linked file's original timestamp fields.
// Seconds is an unmodified IEEE float64; no timezone or filesystem time is inferred.
type LinkedFileTimestamp struct {
	Year                     uint32
	Month, Day, Hour, Minute uint8
	Seconds                  float64
	Span                     Span
}

// LinkedFileRecord indexes one liFD, liFE or liFA entry without reading its content.
// ID is the original opaque Pascal identifier; repeated IDs and records remain in source order.
// Span includes the eight-byte length word; Body and Header exclude that word.
// Header ends before Data, or before versioned trailing fields when no content is present.
// Descriptor spans include their version word and may be explicitly decoded with DecodeDescriptor(span, true).
// Tail preserves unrecognized bytes inside the declared entry; Padding preserves its external four-byte alignment.
// Every Span borrows the source lifetime; CopySpan can stream Data while the document remains open.
type LinkedFileRecord struct {
	Block                                      AdditionalBlock
	Index                                      int
	Kind                                       string
	Version                                    uint32
	ID, Name, FileType, Creator                string
	DataSize, ExternalFileSize                 uint64
	HasData                                    bool
	Timestamp                                  *LinkedFileTimestamp
	ChildDocumentID                            string
	AssetModTime                               *float64
	AssetLocked                                *uint8
	Span, Body, Header, Data, Tail, Padding    Span
	LengthData, IDData, NameData, DataSizeData Span
	FileOpenFlagData, FileOpenDescriptor       Span
	ExternalDescriptor, ExternalFileSizeData   Span
	AliasReserved, ChildDocumentIDData         Span
	AssetModTimeData, AssetLockedData          Span
}

// LinkedFiles indexes every linked-file block, including duplicates and entries in alternate layer sets.
// It returns owned metadata in file order and never opens external paths or reads embedded payload bytes.
// Descriptor structures are bounded to identify their spans; their content remains available for explicit decoding.
// Limits apply to this complete operation, independently of source indexing and later content reads.
func (d *Document) LinkedFiles(ctx context.Context, options ...MetadataOptions) ([]LinkedFileRecord, error) {
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
	limits, err := d.metadataLimits(options)
	if err != nil {
		return nil, err
	}
	view := &Document{reader: contextReaderAt{ctx: ctx, reader: d.reader}, size: d.size, limits: limits}
	budget := &indexBudget{document: view}
	if len(options) == 1 && options[0].reservation != nil {
		defer func() { *options[0].reservation = budget.metadata }()
		if err := budget.reserve(64, "linked.view", 0); err != nil {
			return nil, err
		}
	}
	blocks, err := linkedBlocks(ctx, d, budget)
	if err != nil {
		return nil, linkedReadError(ctx, err)
	}
	var records []LinkedFileRecord
	descriptorItems := 0
	for _, block := range blocks {
		cursor := &indexCursor{
			document: view, position: block.Data.Offset, end: block.Data.Offset + block.Data.Length, path: "linked",
		}
		for index := 0; cursor.remaining() != 0 && cursor.err == nil; index++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := budget.block("linked.records", cursor.position); err != nil {
				return nil, linkedReadError(ctx, err)
			}
			start := cursor.position
			length := cursor.length(8, "length")
			body := cursor.take(length, "body")
			if cursor.err != nil {
				return nil, linkedReadError(ctx, cursor.err)
			}
			entry := cursor.child(body, "linked.record")
			record := readLinkedRecord(entry, block, index, budget, &descriptorItems)
			if entry.err != nil {
				return nil, linkedReadError(ctx, entry.err)
			}
			record.LengthData = Span{Offset: start, Length: 8}
			record.Span = Span{Offset: start, Length: 8 + length}
			record.Padding = cursor.take((4-length%4)%4, "padding")
			if cursor.err != nil {
				return nil, linkedReadError(ctx, cursor.err)
			}
			if err := indexAppend(budget, &records, record, "linked.records", start); err != nil {
				return nil, linkedReadError(ctx, err)
			}
		}
		if cursor.err != nil {
			return nil, linkedReadError(ctx, cursor.err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func linkedBlocks(ctx context.Context, d *Document, budget *indexBudget) ([]AdditionalBlock, error) {
	var blocks []AdditionalBlock
	add := func(source []AdditionalBlock) error {
		for _, block := range source {
			if err := ctx.Err(); err != nil {
				return err
			}
			switch block.Key {
			case "lnkD", "lnk2", "lnk3", "lnkE":
				if !d.validSpan(block.Data) || !d.validSpan(block.Span) {
					return fileError(ErrMalformed, "linked", "linked.block", block.Data.Offset, "block is outside the source")
				}
				if err := budget.block("linked.blocks", block.Span.Offset); err != nil {
					return err
				}
				if err := indexAppend(budget, &blocks, block, "linked.blocks", block.Span.Offset); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := add(d.AdditionalInfo); err != nil {
		return nil, err
	}
	for _, set := range d.LayerSets {
		for _, layer := range set.Layers {
			if err := add(layer.AdditionalInfo); err != nil {
				return nil, err
			}
		}
	}
	slices.SortFunc(blocks, func(first, second AdditionalBlock) int {
		return cmp.Compare(first.Span.Offset, second.Span.Offset)
	})
	return slices.CompactFunc(blocks, func(first, second AdditionalBlock) bool {
		return first.Span == second.Span && first.Data == second.Data
	}), nil
}

func readLinkedRecord(
	c *indexCursor, block AdditionalBlock, index int, budget *indexBudget, descriptorItems *int,
) LinkedFileRecord {
	result := LinkedFileRecord{Block: block, Index: index, Body: Span{Offset: c.position, Length: c.remaining()}}
	if err := budget.reserve(12, "linked.record", c.position); err != nil {
		c.err = err
		return result
	}
	result.Kind, result.Version = c.signature("kind"), c.u32("version")
	if c.err != nil {
		return result
	}
	if result.Kind != "liFD" && result.Kind != "liFE" && result.Kind != "liFA" ||
		result.Version < 1 || result.Version > 7 {
		c.err = fileError(ErrUnsupported, "linked", "linked.record", result.Body.Offset, "unsupported linked kind or version")
		return result
	}
	result.ID, result.IDData = linkedPascal(c, budget, "id")
	result.Name, result.NameData = linkedUnicode(c, budget, "name")
	result.FileType, result.Creator = c.signature("fileType"), c.signature("creator")
	result.DataSizeData = Span{Offset: c.position, Length: 8}
	result.DataSize = c.u64("dataSize")
	result.FileOpenFlagData = Span{Offset: c.position, Length: 1}
	openFile := c.u8("fileOpen")
	if openFile > 1 {
		c.fail("linked.record.fileOpen", result.FileOpenFlagData.Offset, "file-open flag must be zero or one")
	}
	if c.err != nil {
		return result
	}
	if openFile != 0 {
		result.FileOpenDescriptor = linkedDescriptor(c, budget, descriptorItems, "fileOpenDescriptor")
	}
	if c.err != nil {
		return result
	}
	switch result.Kind {
	case "liFE":
		result.ExternalDescriptor = linkedDescriptor(c, budget, descriptorItems, "externalDescriptor")
		if c.err != nil {
			return result
		}
		if result.Version > 3 {
			if err := budget.reserve(int64(unsafe.Sizeof(LinkedFileTimestamp{})), "linked.timestamp", c.position); err != nil {
				c.err = err
				return result
			}
			timestamp := &LinkedFileTimestamp{Span: Span{Offset: c.position, Length: 16}}
			timestamp.Year, timestamp.Month, timestamp.Day = c.u32("year"), c.u8("month"), c.u8("day")
			timestamp.Hour, timestamp.Minute, timestamp.Seconds = c.u8("hour"), c.u8("minute"), c.f64("seconds")
			result.Timestamp = timestamp
		}
		result.ExternalFileSizeData = Span{Offset: c.position, Length: 8}
		result.ExternalFileSize = c.u64("externalFileSize")
	case "liFA":
		result.AliasReserved = c.take(8, "aliasReserved")
	}
	result.Header = Span{Offset: result.Body.Offset, Length: c.position - result.Body.Offset}
	if result.Kind == "liFD" || result.Kind == "liFE" && result.Version > 2 {
		result.HasData = true
		result.Data = linkedPayload(c, result.DataSize)
	}
	if c.err != nil {
		return result
	}
	if result.Version >= 5 {
		result.ChildDocumentID, result.ChildDocumentIDData = linkedUnicode(c, budget, "childDocumentID")
	}
	if c.err != nil {
		return result
	}
	if result.Version >= 6 {
		if err := budget.reserve(8, "linked.assetModTime", c.position); err != nil {
			c.err = err
			return result
		}
		result.AssetModTimeData = Span{Offset: c.position, Length: 8}
		value := c.f64("assetModTime")
		result.AssetModTime = &value
	}
	if c.err != nil {
		return result
	}
	if result.Version >= 7 {
		if err := budget.reserve(1, "linked.assetLocked", c.position); err != nil {
			c.err = err
			return result
		}
		result.AssetLockedData = Span{Offset: c.position, Length: 1}
		value := c.u8("assetLocked")
		result.AssetLocked = &value
	}
	if result.Kind == "liFE" && result.Version == 2 {
		result.HasData = true
		result.Data = linkedPayload(c, result.DataSize)
	}
	result.Tail = c.take(c.remaining(), "tail")
	return result
}

func linkedPayload(c *indexCursor, size uint64) Span {
	if size > math.MaxInt64 {
		c.fail("linked.record.data", c.position, "content size exceeds signed source offsets")
		return Span{}
	}
	return c.take(int64(size), "data")
}

func linkedPascal(c *indexCursor, budget *indexBudget, field string) (string, Span) {
	if c.err != nil {
		return "", Span{}
	}
	start := c.position
	length := int64(c.u8(field + ".length"))
	if c.err != nil {
		return "", Span{}
	}
	if length > c.document.limits.MaxReadBytes {
		c.err = fileError(ErrLimit, "linked", "linked.record."+field, c.position, "identifier exceeds MaxReadBytes")
		return "", Span{}
	}
	if err := budget.reserve(length*2, "linked.record."+field, c.position); err != nil {
		c.err = err
		return "", Span{}
	}
	span := c.take(length, field)
	if c.err != nil {
		return "", Span{}
	}
	data := make([]byte, int(length))
	reader := c.child(span, "linked.record."+field)
	reader.read(data, "value")
	if reader.err != nil {
		c.err = reader.err
	}
	return string(data), Span{Offset: start, Length: c.position - start}
}

func linkedUnicode(c *indexCursor, budget *indexBudget, field string) (string, Span) {
	if c.err != nil {
		return "", Span{}
	}
	reader := descriptorCursor{document: c.document, offset: c.position, end: c.end, allocated: budget.metadata}
	value, span := reader.unicode("linked.record." + field)
	c.position, c.err, budget.metadata = reader.offset, reader.err, reader.allocated
	return value, span
}

func linkedDescriptor(c *indexCursor, budget *indexBudget, items *int, field string) Span {
	if c.err != nil {
		return Span{}
	}
	start := c.position
	reader := descriptorCursor{
		document: c.document, offset: start, end: c.end, allocated: budget.metadata, items: *items,
	}
	version := reader.u32("linked.record." + field + ".version")
	if reader.err == nil && version != 16 {
		c.err = fileError(ErrUnsupported, "linked", "linked.record."+field, start, "unsupported descriptor version")
		return Span{}
	}
	reader.descriptor(1, "linked.record."+field, false)
	c.position, c.err, budget.metadata, *items = reader.offset, reader.err, reader.allocated, reader.items
	return Span{Offset: start, Length: c.position - start}
}

func linkedReadError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if detail, ok := errors.AsType[*Error](err); ok {
		return &Error{Kind: detail.Kind, Feature: "linked", Path: detail.Path, Offset: detail.Offset,
			Detail: detail.Detail, Cause: err}
	}
	return err
}
