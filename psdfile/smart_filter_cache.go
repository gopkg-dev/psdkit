package psdfile

import (
	"cmp"
	"context"
	"image"
	"slices"
	"unsafe"
)

// FilterCacheArray indexes one optional FEid/FXid array without reading its encoded pixels.
// Index is its stored ordinal; Data excludes the two-byte compression mode.
// Bounds and Depth are inherited from the cache record or its additional filter mask.
type FilterCacheArray struct {
	Index                   int
	Written                 uint32
	Bounds                  image.Rectangle
	Depth, Compression      uint16
	Span, Body, Data        Span
	WrittenData, LengthData Span
	CompressionData         Span
}

// FilterCacheRecord retains a source-ordered FEid/FXid input cache and its additional filter mask.
// ID associates this cache with SmartFilterStack.PlacementID, not the shared linked-content ID.
// Arrays retain every ordinal; the additional Mask's samples remain deferred until explicitly decoded.
// Cached arrays are retained data, not authoritative replacements for executing live filters.
type FilterCacheRecord struct {
	Block                              AdditionalBlock
	Index                              int
	ID                                 string
	BlockVersion, Version, MaxChannels uint32
	Depth                              uint16
	Bounds                             image.Rectangle
	Arrays                             []FilterCacheArray
	Mask                               *FilterCacheArray
	Span, Body, Header, ChannelBody    Span
	LengthData, IDData, VersionData    Span
	ChannelLengthData, MaskFlagData    Span
	ChannelTail, Tail, Padding         Span
}

// FilterCacheView reports owned cache indexes and their measured additional metadata allocation.
type FilterCacheView struct {
	Records       []FilterCacheRecord
	MetadataBytes int64
}

// ReadFilterCacheView indexes smart-filter cache blocks without reading any sample payload.
func (d *Document) ReadFilterCacheView(ctx context.Context, options ...MetadataOptions) (FilterCacheView, error) {
	var result FilterCacheView
	option, err := metadataViewOption(options, &result.MetadataBytes)
	if err != nil {
		return result, err
	}
	result.Records, err = d.FilterCaches(ctx, option)
	return result, err
}

// FilterCaches indexes all FEid/FXid blocks, including duplicate and layer-local records, in physical order.
// All pixel data and unknown record tails remain source spans; no cached pixel is loaded by this operation.
func (d *Document) FilterCaches(ctx context.Context, options ...MetadataOptions) ([]FilterCacheRecord, error) {
	ctx = decodeContext(ctx)
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
		if err := budget.reserve(64, "filters.view", 0); err != nil {
			return nil, err
		}
	}
	blocks := []AdditionalBlock{}
	add := func(source []AdditionalBlock) error {
		for _, block := range source {
			if block.Key != "FEid" && block.Key != "FXid" {
				continue
			}
			if err := indexAppend(budget, &blocks, block, "filters.blocks", block.Data.Offset); err != nil {
				return err
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
	slices.SortStableFunc(blocks, func(left, right AdditionalBlock) int {
		return cmp.Compare(left.Span.Offset, right.Span.Offset)
	})
	var records []FilterCacheRecord
	for _, block := range blocks {
		cursor := &indexCursor{document: view, position: block.Data.Offset,
			end: block.Data.Offset + block.Data.Length, path: "filters.cache"}
		version := cursor.u32("version")
		if cursor.err != nil {
			return nil, patternReadError(ctx, cursor.err)
		}
		if version < 1 || version > 3 {
			return nil, fileError(ErrUnsupported, "smartFilters", "cache.version", block.Data.Offset,
				"unknown filter-cache block version")
		}
		for index := 0; cursor.remaining() != 0; index++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := budget.block("filters.cache.records", cursor.position); err != nil {
				return nil, err
			}
			start := cursor.position
			length := cursor.length(8, "length")
			body := cursor.take(length, "body")
			if cursor.err != nil {
				return nil, patternReadError(ctx, cursor.err)
			}
			entry := cursor.child(body, "filters.cache.record")
			record, err := readFilterCacheRecord(entry, budget, block, index, version)
			if err != nil {
				return nil, err
			}
			if entry.err != nil {
				return nil, patternReadError(ctx, entry.err)
			}
			record.LengthData = Span{Offset: start, Length: 8}
			record.Span = Span{Offset: start, Length: length + 8}
			record.Padding = cursor.take((4-length%4)%4, "padding")
			if cursor.err != nil {
				return nil, patternReadError(ctx, cursor.err)
			}
			if err := indexAppend(budget, &records, record, "filters.cache.records", start); err != nil {
				return nil, err
			}
		}
	}
	return records, nil
}

func readFilterCacheRecord(
	cursor *indexCursor, budget *indexBudget, block AdditionalBlock, index int, blockVersion uint32,
) (FilterCacheRecord, error) {
	record := FilterCacheRecord{Block: block, Index: index, BlockVersion: blockVersion,
		Body: Span{Offset: cursor.position, Length: cursor.remaining()}}
	start := cursor.position
	length := int(cursor.u8("identifier.length"))
	if err := budget.reserve(int64(length)*2, "filters.cache.identifier", start); err != nil {
		return record, err
	}
	identifier := make([]byte, length)
	cursor.read(identifier, "identifier")
	record.ID = string(identifier)
	record.IDData = Span{Offset: start, Length: int64(length + 1)}
	record.VersionData = Span{Offset: cursor.position, Length: 4}
	record.Version = cursor.u32("version")
	if cursor.err != nil {
		return record, nil
	}
	if record.Version != 1 {
		return record, fileError(ErrUnsupported, "smartFilters", "cache.record.version", record.VersionData.Offset,
			"unknown filter-cache record schema")
	}
	record.ChannelLengthData = Span{Offset: cursor.position, Length: 8}
	length64 := cursor.length(8, "channels.length")
	record.ChannelBody = cursor.take(length64, "channels.body")
	if cursor.err != nil {
		return record, nil
	}
	channels := cursor.child(record.ChannelBody, "filters.cache.channels")
	record.Bounds = filterCacheBounds(channels)
	depth := channels.u32("depth")
	if channels.err != nil {
		cursor.err = channels.err
		return record, nil
	}
	if depth != 1 && depth != 8 && depth != 16 && depth != 32 {
		return record, fileError(ErrUnsupported, "smartFilters", "cache.depth", channels.position-4,
			"unknown cache sample depth")
	}
	record.Depth = uint16(depth)
	record.MaxChannels = channels.u32("maxChannels")
	if uint64(record.MaxChannels)+2 > uint64(budget.document.limits.MaxBlocks) {
		return record, fileError(ErrLimit, "smartFilters", "cache.channels", channels.position-4,
			"cache array count exceeds the structural limit")
	}
	record.Header = Span{Offset: start, Length: channels.position - start}
	for arrayIndex := uint32(0); arrayIndex < record.MaxChannels+2; arrayIndex++ {
		if err := budget.block("filters.cache.arrays", channels.position); err != nil {
			return record, err
		}
		array := FilterCacheArray{Index: int(arrayIndex), Bounds: record.Bounds, Depth: record.Depth,
			WrittenData: Span{Offset: channels.position, Length: 4}}
		array.Written = channels.u32("written")
		if array.Written != 0 {
			readFilterCacheArray(channels, &array)
		}
		array.Span = Span{Offset: array.WrittenData.Offset, Length: channels.position - array.WrittenData.Offset}
		if channels.err != nil {
			cursor.err = channels.err
			return record, nil
		}
		if err := indexAppend(budget, &record.Arrays, array, "filters.cache.arrays", array.Span.Offset); err != nil {
			return record, err
		}
	}
	record.ChannelTail = channels.take(channels.remaining(), "tail")
	record.MaskFlagData = Span{Offset: cursor.position, Length: 1}
	if cursor.remaining() != 0 {
		flag := cursor.u8("mask.written")
		if flag != 0 {
			if err := budget.reserve(int64(unsafe.Sizeof(FilterCacheArray{})),
				"filters.cache.mask", record.MaskFlagData.Offset); err != nil {
				return record, err
			}
			mask := &FilterCacheArray{Index: -1, Written: uint32(flag), Depth: record.Depth,
				WrittenData: record.MaskFlagData}
			mask.Bounds = filterCacheBounds(cursor)
			readFilterCacheArray(cursor, mask)
			mask.Span = Span{Offset: record.MaskFlagData.Offset, Length: cursor.position - record.MaskFlagData.Offset}
			record.Mask = mask
		}
	}
	record.Tail = cursor.take(cursor.remaining(), "tail")
	return record, nil
}

func readFilterCacheArray(cursor *indexCursor, array *FilterCacheArray) {
	array.LengthData = Span{Offset: cursor.position, Length: 8}
	length := cursor.length(8, "array.length")
	array.Body = cursor.take(length, "array.body")
	if length == 0 || cursor.err != nil {
		return
	}
	body := cursor.child(array.Body, "filters.cache.array")
	array.CompressionData = Span{Offset: body.position, Length: 2}
	array.Compression = body.u16("compression")
	array.Data = body.take(body.remaining(), "data")
	if body.err != nil {
		cursor.err = body.err
	}
}

func filterCacheBounds(cursor *indexCursor) image.Rectangle {
	top, left := int32(cursor.u32("bounds.top")), int32(cursor.u32("bounds.left"))
	bottom, right := int32(cursor.u32("bounds.bottom")), int32(cursor.u32("bounds.right"))
	if right < left || bottom < top {
		cursor.fail("filters.cache.bounds", cursor.position-16, "negative cache rectangle")
	}
	return image.Rect(int(left), int(top), int(right), int(bottom))
}

// DecodeFilterCacheRegion decodes an explicitly selected source-precision cache array or additional mask.
// The rectangle is local to Array.Bounds.Min; filter-cache RLE tables always contain four-byte row lengths.
// Returned samples are owned and remain usable after source closure.
func (d *Document) DecodeFilterCacheRegion(
	ctx context.Context, array FilterCacheArray, rectangle image.Rectangle, options ...RegionOptions,
) (*Plane, error) {
	ctx = decodeContext(ctx)
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if array.Written != 1 || array.Body.Length < 2 || array.Compression > 3 {
		return nil, fileError(ErrUnsupported, "smartFilters", "cache.array", array.Span.Offset,
			"array written flag, payload or compression is unsupported")
	}
	if !d.validSpan(array.Data) || !d.validSpan(array.Body) ||
		array.Data.Offset != array.Body.Offset+2 || array.Data.Length != array.Body.Length-2 {
		return nil, fileError(ErrMalformed, "smartFilters", "cache.array", array.Span.Offset,
			"cache data is outside its declared array body")
	}
	source, err := regionGeometry(array.Bounds, array.Depth, "filters.cache.region", array.Data.Offset)
	if err != nil {
		return nil, err
	}
	return d.decodeRegion(ctx, source, array.Data, array.Compression, 4,
		1, 0, int16(array.Index), rectangle, "filters.cache.region", options)
}
