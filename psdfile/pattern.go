package psdfile

import (
	"cmp"
	"context"
	"errors"
	"image"
	"slices"
)

// PatternArray indexes one virtual-memory channel without reading encoded samples.
// Written retains the original four-byte boolean; Depth and PixelDepth retain both depth fields.
// Span includes written/length words and Body excludes them. Data contains only encoded samples.
type PatternArray struct {
	Index                           int
	Written, DeclaredLength, Depth  uint32
	PixelDepth                      uint16
	Bounds                          image.Rectangle
	Compression                     uint8
	Span, Body, Data                Span
	WrittenData, LengthData         Span
	DepthData, BoundsData           Span
	PixelDepthData, CompressionData Span
}

// PatternRecord retains one ordered Patt/Pat2/Pat3 pattern and its virtual-memory array list.
// ID is an opaque local reference; duplicate IDs remain distinct records in source order.
// All source spans borrow the document lifetime and can be streamed through CopySpan.
// Unknown versions and tails remain available and are identified in Unsupported.
type PatternRecord struct {
	Block                                 AdditionalBlock
	Index                                 int
	Version, ColorMode                    uint32
	Vertical, Horizontal                  uint16
	Name, ID                              string
	VMVersion, VMChannels                 uint32
	VMBounds                              image.Rectangle
	Arrays                                []PatternArray
	Unsupported                           []string
	Span, Body, Header                    Span
	LengthData, VersionData               Span
	ColorModeData, PointData              Span
	NameData, IDData, Palette             Span
	VMSpan, VMBody, VMTail, Tail, Padding Span
}

// PatternView reports owned ordered pattern records and their bounded metadata reservation.
type PatternView struct {
	Records       []PatternRecord
	MetadataBytes int64
}

// ReadPatternView returns patterns together with a reservation for combined operation budgets.
func (d *Document) ReadPatternView(ctx context.Context, options ...MetadataOptions) (PatternView, error) {
	var result PatternView
	option, err := metadataViewOption(options, &result.MetadataBytes)
	if err != nil {
		return result, err
	}
	result.Records, err = d.Patterns(ctx, option)
	return result, err
}

// Patterns indexes all global and layer-local Patt/Pat2/Pat3 blocks in physical source order.
// It owns names and structural records, preserves duplicates and never reads array payloads.
func (d *Document) Patterns(ctx context.Context, options ...MetadataOptions) ([]PatternRecord, error) {
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
	limits.MaxMetadataBytes = min(limits.MaxMetadataBytes, limits.MaxDecodeBytes)
	view := &Document{reader: contextReaderAt{ctx: ctx, reader: d.reader}, size: d.size, limits: limits}
	budget := &indexBudget{document: view}
	blocks, err := patternBlocks(d, budget)
	if err != nil {
		return nil, err
	}
	var records []PatternRecord
	for _, block := range blocks {
		cursor := &indexCursor{document: view, position: block.Data.Offset,
			end: block.Data.Offset + block.Data.Length, path: "patterns"}
		for index := 0; cursor.remaining() != 0 && cursor.err == nil; index++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := budget.block("patterns.records", cursor.position); err != nil {
				return nil, err
			}
			start := cursor.position
			length := int64(cursor.u32("length"))
			body := cursor.take(length, "body")
			if cursor.err != nil {
				return nil, patternReadError(ctx, cursor.err)
			}
			entry := cursor.child(body, "patterns.record")
			record := readPatternRecord(entry, block, index, budget)
			if entry.err != nil {
				return nil, patternReadError(ctx, entry.err)
			}
			record.LengthData = Span{Offset: start, Length: 4}
			record.Span = Span{Offset: start, Length: 4 + length}
			record.Padding = cursor.take((4-length%4)%4, "padding")
			if cursor.err != nil {
				return nil, patternReadError(ctx, cursor.err)
			}
			if err := indexAppend(budget, &records, record, "patterns.records", start); err != nil {
				return nil, err
			}
		}
		if cursor.err != nil {
			return nil, patternReadError(ctx, cursor.err)
		}
	}
	if len(options) == 1 && options[0].reservation != nil {
		*options[0].reservation = budget.metadata
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func patternBlocks(d *Document, budget *indexBudget) ([]AdditionalBlock, error) {
	var blocks []AdditionalBlock
	add := func(values []AdditionalBlock) error {
		for _, block := range values {
			if block.Key != "Patt" && block.Key != "Pat2" && block.Key != "Pat3" {
				continue
			}
			if !d.validSpan(block.Data) {
				return fileError(ErrMalformed, "patterns", "patterns.block", block.Data.Offset, "block is outside source")
			}
			if err := budget.block("patterns.blocks", block.Span.Offset); err != nil {
				return err
			}
			if err := indexAppend(budget, &blocks, block, "patterns.blocks", block.Span.Offset); err != nil {
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
	slices.SortFunc(blocks, func(first, second AdditionalBlock) int {
		return cmp.Compare(first.Span.Offset, second.Span.Offset)
	})
	return slices.CompactFunc(blocks, func(first, second AdditionalBlock) bool {
		return first.Span == second.Span && first.Data == second.Data
	}), nil
}

func readPatternRecord(c *indexCursor, block AdditionalBlock, index int, budget *indexBudget) PatternRecord {
	result := PatternRecord{Block: block, Index: index, Body: Span{Offset: c.position, Length: c.remaining()}}
	result.VersionData = Span{Offset: c.position, Length: 4}
	result.Version = c.u32("version")
	if c.err != nil {
		return result
	}
	if result.Version != 1 {
		patternUnsupported(c, budget, &result, "patterns.version")
		result.Tail = c.take(c.remaining(), "unknownVersion")
		return result
	}
	result.ColorModeData = Span{Offset: c.position, Length: 4}
	result.ColorMode = c.u32("mode")
	if result.ColorMode != 0 && result.ColorMode != 1 && result.ColorMode != 2 && result.ColorMode != 3 &&
		result.ColorMode != 4 && result.ColorMode != 7 && result.ColorMode != 8 && result.ColorMode != 9 {
		patternUnsupported(c, budget, &result, "patterns.mode")
	}
	result.PointData = Span{Offset: c.position, Length: 4}
	result.Vertical, result.Horizontal = c.u16("vertical"), c.u16("horizontal")
	reader := descriptorCursor{document: c.document, offset: c.position, end: c.end, allocated: budget.metadata}
	result.Name, result.NameData = reader.unicode("patterns.name")
	c.position, c.err, budget.metadata = reader.offset, reader.err, reader.allocated
	result.ID, result.IDData = patternPascal(c, budget)
	if result.ColorMode == 2 {
		result.Palette = c.take(768, "palette")
	}
	result.Header = Span{Offset: result.Body.Offset, Length: c.position - result.Body.Offset}
	if c.err != nil {
		return result
	}
	start := c.position
	result.VMVersion = c.u32("vmVersion")
	length := int64(c.u32("vmLength"))
	result.VMBody = c.take(length, "vmBody")
	result.VMSpan = Span{Offset: start, Length: 8 + length}
	if c.err != nil {
		return result
	}
	if result.VMVersion != 3 {
		patternUnsupported(c, budget, &result, "patterns.vmVersion")
		result.VMTail = result.VMBody
	} else {
		vm := c.child(result.VMBody, "patterns.vm")
		result.VMBounds = patternRectangle(vm, "bounds")
		result.VMChannels = vm.u32("channels")
		count := uint64(result.VMChannels) + 2
		if count > uint64(c.document.limits.MaxBlocks-budget.blocks) || count > uint64(vm.remaining()/4) {
			vm.err = fileError(ErrLimit, "patterns", "patterns.arrays", vm.position,
				"virtual array count exceeds source or metadata bounds")
		}
		for index := uint64(0); index < count && vm.err == nil; index++ {
			if err := budget.block("patterns.arrays", vm.position); err != nil {
				vm.err = err
				break
			}
			array := readPatternArray(vm, int(index))
			if array.Written > 1 {
				patternUnsupported(c, budget, &result, "patterns.written")
			}
			if vm.err == nil {
				vm.err = indexAppend(budget, &result.Arrays, array, "patterns.arrays", array.Span.Offset)
			}
		}
		if vm.err != nil {
			c.err = vm.err
			return result
		}
		result.VMTail = vm.take(vm.remaining(), "tail")
		if result.VMTail.Length != 0 {
			patternUnsupported(c, budget, &result, "patterns.vmTail")
		}
	}
	result.Tail = c.take(c.remaining(), "tail")
	if result.Tail.Length != 0 {
		patternUnsupported(c, budget, &result, "patterns.tail")
	}
	return result
}

func readPatternArray(c *indexCursor, index int) PatternArray {
	start := c.position
	result := PatternArray{Index: index, WrittenData: Span{Offset: start, Length: 4}}
	result.Written = c.u32("written")
	if result.Written != 0 && c.err == nil {
		result.LengthData = Span{Offset: c.position, Length: 4}
		result.DeclaredLength = c.u32("length")
		result.Body = c.take(int64(result.DeclaredLength), "body")
		if result.DeclaredLength != 0 && c.err == nil {
			body := c.child(result.Body, "patterns.array")
			result.DepthData = Span{Offset: body.position, Length: 4}
			result.Depth = body.u32("depth")
			result.BoundsData = Span{Offset: body.position, Length: 16}
			result.Bounds = patternRectangle(body, "bounds")
			result.PixelDepthData = Span{Offset: body.position, Length: 2}
			result.PixelDepth = body.u16("pixelDepth")
			result.CompressionData = Span{Offset: body.position, Length: 1}
			result.Compression = body.u8("compression")
			result.Data = body.take(body.remaining(), "pixels")
			c.err = body.err
		}
	}
	result.Span = Span{Offset: start, Length: c.position - start}
	return result
}

func patternRectangle(c *indexCursor, field string) image.Rectangle {
	top, left, bottom, right := int32(c.u32(field)), int32(c.u32(field)), int32(c.u32(field)), int32(c.u32(field))
	return image.Rectangle{Min: image.Pt(int(left), int(top)), Max: image.Pt(int(right), int(bottom))}
}

func patternPascal(c *indexCursor, budget *indexBudget) (string, Span) {
	start := c.position
	length := int64(c.u8("idLength"))
	if c.err != nil {
		return "", Span{}
	}
	if length > c.document.limits.MaxReadBytes {
		c.err = fileError(ErrLimit, "patterns", "patterns.id", start, "identifier exceeds source read limit")
		return "", Span{}
	}
	if err := budget.reserve(length*2, "patterns.id", start); err != nil {
		c.err = err
		return "", Span{}
	}
	data := make([]byte, int(length))
	c.read(data, "id")
	return string(data), Span{Offset: start, Length: c.position - start}
}

func patternUnsupported(c *indexCursor, budget *indexBudget, record *PatternRecord, path string) {
	if err := budget.reserve(int64(len(path))+32, path, c.position); err != nil {
		c.err = err
		return
	}
	record.Unsupported = append(record.Unsupported, path)
}

func patternReadError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if detail, ok := errors.AsType[*Error](err); ok {
		return &Error{Kind: detail.Kind, Feature: "patterns", Path: detail.Path, Offset: detail.Offset,
			Detail: detail.Detail, Cause: err}
	}
	return err
}
