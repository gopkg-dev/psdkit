package psdfile

import (
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"math"
	"unsafe"
)

type tiffPrivateCursor struct {
	builder       *tiffPrivateBuilder
	position, end int64
	path          string
	err           error
}

func (cursor *tiffPrivateCursor) fail(kind error, detail string) {
	if cursor.err == nil {
		cursor.err = fileError(kind, "tiff.layers", cursor.path, cursor.position, detail)
	}
}

func (cursor *tiffPrivateCursor) take(length int64) Span {
	span := Span{Offset: cursor.position, Length: length}
	if cursor.err != nil {
		return span
	}
	if err := cursor.builder.ctx.Err(); err != nil {
		cursor.err = err
		return span
	}
	if length < 0 || length > cursor.end-cursor.position {
		cursor.fail(ErrMalformed, "private structural field exceeds its enclosing interval")
		return span
	}
	cursor.position += length
	return span
}

func (cursor *tiffPrivateCursor) read(data []byte, swap uint8) {
	span := cursor.take(int64(len(data)))
	if cursor.err != nil {
		return
	}
	if int64(len(data)) > cursor.builder.limits.MaxReadBytes {
		cursor.err = fileError(ErrLimit, "tiff.layers", cursor.path, span.Offset,
			"private structural read exceeds MaxReadBytes")
		return
	}
	count, err := cursor.builder.parent.ReadAt(data, span.Offset)
	if cancellation := cursor.builder.ctx.Err(); cancellation != nil {
		cursor.err = cancellation
		return
	}
	if count != len(data) || err != nil && err != io.EOF { //nolint:errorlint // Only bare EOF ends a read; joined errors retain source faults.
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		cursor.err = &Error{Kind: ErrMalformed, Feature: "tiff.layers", Path: cursor.path, Offset: span.Offset,
			Detail: "private structural source could not be read", Cause: err}
		return
	}
	if swap != 0 {
		cursor.err = cursor.builder.addSwap(span, swap, cursor.path)
	}
}

func (cursor *tiffPrivateCursor) u8() uint8 {
	var value [1]byte
	cursor.read(value[:], 0)
	return value[0]
}

func (cursor *tiffPrivateCursor) u16() uint16 {
	var value [2]byte
	cursor.read(value[:], 2)
	return binary.LittleEndian.Uint16(value[:])
}

func (cursor *tiffPrivateCursor) u32() uint32 {
	var value [4]byte
	cursor.read(value[:], 4)
	return binary.LittleEndian.Uint32(value[:])
}

func (cursor *tiffPrivateCursor) u64() uint64 {
	var value [8]byte
	cursor.read(value[:], 8)
	return binary.LittleEndian.Uint64(value[:])
}

func (cursor *tiffPrivateCursor) signature() string {
	var value [4]byte
	cursor.read(value[:], 4)
	return string([]byte{value[3], value[2], value[1], value[0]})
}

func (cursor *tiffPrivateCursor) unicode() {
	count := int64(cursor.u32())
	span := cursor.take(count * 2)
	if cursor.err == nil {
		cursor.err = cursor.builder.addSwap(span, 2, cursor.path)
	}
}

func (cursor *tiffPrivateCursor) descriptor(versioned bool) {
	cursor.descriptorOrder(versioned, binary.LittleEndian)
}

func (cursor *tiffPrivateCursor) descriptorOrder(versioned bool, order binary.ByteOrder) {
	if cursor.err != nil {
		return
	}
	span, err := normalizeTIFFPrivateDescriptor(cursor.builder.ctx, cursor.builder.parent,
		Span{Offset: cursor.position, Length: cursor.end - cursor.position}, versioned,
		tiffPrivateDescriptorOptions{Order: order, Limits: cursor.builder.limits, Reserve: cursor.builder.reserve,
			Count: cursor.builder.count, Swap: cursor.builder.addSwap})
	if err != nil {
		cursor.err = err
		return
	}
	cursor.position += span.Length
}

func (cursor *tiffPrivateCursor) padding(limit int64) {
	if cursor.err != nil {
		return
	}
	if cursor.end-cursor.position > limit {
		cursor.fail(ErrUnsupported, "unrecognized bytes follow the normalized private schema")
		return
	}
	for cursor.position < cursor.end && cursor.err == nil {
		if cursor.u8() != 0 {
			cursor.fail(ErrMalformed, "private alignment bytes are not zero")
		}
	}
}

func (builder *tiffPrivateBuilder) index(record LinkedFileRecord) (*TIFFLayerSource, error) {
	directorySource := &Document{reader: builder.parent, size: builder.parent.size, limits: builder.limits}
	base := linkedTIFF{document: directorySource, context: builder.ctx, span: builder.span,
		options: LinkedRasterOptions{MaxPixels: builder.limits.MaxDecodePixels,
			MaxWorkingBytes: min(builder.limits.MaxMetadataBytes, builder.limits.MaxDecodeBytes), MaxProfileBytes: 4 << 20}}
	if err := base.directory(); err != nil {
		return nil, err
	}
	if base.order != binary.LittleEndian {
		return nil, fileError(ErrUnsupported, "tiff.layers", "byteOrder", builder.span.Offset,
			"private TIFF layers currently require classic little-endian encoding")
	}
	owned := int64(unsafe.Sizeof(TIFFLayerSource{})) + int64(unsafe.Sizeof(tiffPrivateReader{}))
	if err := builder.reserve(base.working+owned,
		"directory", builder.span.Offset); err != nil {
		return nil, err
	}
	width, err := base.scalar(256, -1)
	if err != nil {
		return nil, err
	}
	height, err := base.scalar(257, -1)
	if err != nil {
		return nil, err
	}
	if width <= 0 || height <= 0 || width > 30_000 || height > 30_000 {
		return nil, fileError(ErrUnsupported, "tiff.layers", "dimensions", builder.span.Offset,
			"private classic TIFF layers require PSD-sized positive canvas dimensions")
	}
	photo, err := base.scalar(262, -1)
	if err != nil {
		return nil, err
	}
	samples, err := base.scalar(277, 1)
	if err != nil {
		return nil, err
	}
	if photo != 2 || samples != 3 && samples != 4 {
		return nil, fileError(ErrUnsupported, "tiff.layers", "color", builder.span.Offset,
			"private TIFF layer samples require an RGB8 main image")
	}
	if _, err := base.sampleEncoding(samples); err != nil {
		return nil, err
	}
	if err := builder.validateMainIFD(&base, width, height, samples); err != nil {
		return nil, err
	}
	private, exists := base.field(37724)
	if !exists {
		return nil, fileError(ErrUnsupported, "tiff.layers", "private", builder.span.Offset,
			"TIFF has no Photoshop-private layer source data")
	}
	if private.kind != 7 || private.data.Length < 48 {
		return nil, fileError(ErrMalformed, "tiff.layers", "private", builder.span.Offset+private.data.Offset,
			"private TIFF source data requires a complete UNDEFINED block")
	}
	privateSpan := Span{Offset: builder.span.Offset + private.data.Offset, Length: private.data.Length}
	cursor := tiffPrivateCursor{builder: builder, position: privateSpan.Offset,
		end: privateSpan.Offset + privateSpan.Length, path: "private.header"}
	var marker [36]byte
	cursor.read(marker[:], 0)
	if cursor.err != nil {
		return nil, cursor.err
	}
	if string(marker[:]) != "Adobe Photoshop Document Data Block\x00" {
		return nil, fileError(ErrUnsupported, "tiff.layers", "private.header", privateSpan.Offset,
			"unsupported private TIFF document data version")
	}
	var layerSpan Span
	var globals []AdditionalBlock
	for cursor.position < cursor.end && cursor.err == nil {
		block, err := builder.block(&cursor, -1, true)
		if err != nil {
			return nil, err
		}
		if block.Key == "Layr" {
			if layerSpan.Length != 0 {
				return nil, fileError(ErrMalformed, "tiff.layers", "private.Layr", block.Data.Offset,
					"private TIFF has multiple RGB8 layer sets")
			}
			layerSpan = block.Data
		}
		if len(globals) == cap(globals) {
			capacity := max(1, cap(globals)*2)
			if err := builder.reserve(int64(capacity-cap(globals))*int64(unsafe.Sizeof(AdditionalBlock{})),
				"private.blocks", block.Span.Offset); err != nil {
				return nil, err
			}
			owned := make([]AdditionalBlock, len(globals), capacity)
			copy(owned, globals)
			globals = owned
		}
		globals = append(globals, block)
	}
	if cursor.err != nil {
		return nil, cursor.err
	}
	if layerSpan.Length == 0 {
		return nil, fileError(ErrUnsupported, "tiff.layers", "private.Layr", privateSpan.Offset,
			"private TIFF contains no RGB8 Layr set")
	}
	reader, err := builder.orderedReader()
	if err != nil {
		return nil, err
	}
	remaining := builder.limits
	remaining.MaxMetadataBytes -= builder.metadata
	remaining.MaxDecodeBytes -= builder.metadata
	remaining.MaxMetadataBytes = min(remaining.MaxMetadataBytes, remaining.MaxDecodeBytes)
	if remaining.MaxMetadataBytes <= 0 || remaining.MaxDecodeBytes <= 0 {
		return nil, fileError(ErrLimit, "tiff.layers", "index", layerSpan.Offset, "no private indexing budget remains")
	}
	view := &Document{reader: reader, size: builder.parent.size, limits: remaining, Source: builder.span,
		Header: Header{Version: 1, Channels: uint16(samples), Width: uint32(width), Height: uint32(height),
			Depth: 8, ColorMode: 3}, ActiveLayerSet: -1, AdditionalInfo: globals}
	indexing := &indexBudget{document: view}
	if err := indexing.reserve(int64(unsafe.Sizeof(Document{})), "TIFF.view", layerSpan.Offset); err != nil {
		return nil, err
	}
	set, err := view.indexLayerSet(&indexCursor{document: view, position: layerSpan.Offset,
		end: layerSpan.Offset + layerSpan.Length, path: "TIFF.Layr"}, indexing, "TIFF.Layr", 8)
	if err != nil {
		return nil, err
	}
	if err := indexing.reserve(int64(unsafe.Sizeof(LayerSet{})), "TIFF.Layr", layerSpan.Offset); err != nil {
		return nil, err
	}
	view.LayerSets, view.ActiveLayerSet, view.Layers = []LayerSet{set}, 0, set.Layers
	if resource, exists := base.field(34377); exists {
		if resource.kind != 1 && resource.kind != 7 {
			return nil, fileError(ErrMalformed, "tiff.layers", "resources", builder.span.Offset+resource.data.Offset,
				"TIFF Photoshop image resources require a byte field")
		}
		resourceSpan := Span{Offset: builder.span.Offset + resource.data.Offset, Length: resource.data.Length}
		if err := indexing.reserve(int64(unsafe.Sizeof(Document{})),
			"TIFF.resources.decoder", resourceSpan.Offset); err != nil {
			return nil, err
		}
		original := &Document{reader: strictRasterReaderAt{context: builder.ctx, source: builder.parent},
			size: builder.parent.size, limits: view.limits}
		if err := original.indexResources(&indexCursor{document: original, position: resourceSpan.Offset,
			end: resourceSpan.Offset + resourceSpan.Length, path: "TIFF.resources"}, indexing); err != nil {
			return nil, err
		}
		view.Resources = original.Resources
	}
	profileSpan := Span{}
	if profile, exists := base.field(34675); exists {
		if profile.kind != 7 || profile.data.Length < 132 {
			return nil, fileError(ErrMalformed, "tiff.layers", "icc", builder.span.Offset+profile.data.Offset,
				"TIFF ICC profile requires a complete UNDEFINED field")
		}
		profileSpan = Span{Offset: builder.span.Offset + profile.data.Offset, Length: profile.data.Length}
	}
	metadata := builder.metadata + indexing.metadata
	view.limits.MaxMetadataBytes = min(builder.limits.MaxMetadataBytes, builder.limits.MaxDecodeBytes) - metadata
	view.limits.MaxDecodeBytes = builder.limits.MaxDecodeBytes - metadata
	if view.limits.MaxMetadataBytes <= 0 || view.limits.MaxDecodeBytes <= 0 {
		return nil, fileError(ErrLimit, "tiff.layers", "index", layerSpan.Offset, "indexed TIFF metadata exhausts budgets")
	}
	view.metadataBytes = indexing.metadata
	if err := builder.ctx.Err(); err != nil {
		return nil, err
	}
	info := TIFFLayerSourceInfo{Width: uint32(width), Height: uint32(height), Depth: 8,
		LayerCount: len(set.Layers), TIFFData: builder.span, PrivateData: privateSpan, Layers: layerSpan,
		SavedPreviewKind: "flattened TIFF main IFD saved preview", MetadataBytes: metadata}
	return &TIFFLayerSource{parent: builder.parent, view: view, record: record, info: info,
		swaps: builder.swaps, opaque: builder.opaque, metadata: metadata, limits: builder.limits,
		profileSpan: profileSpan}, nil
}

func (builder *tiffPrivateBuilder) validateMainIFD(base *linkedTIFF, width, height, samples int64) error {
	for _, tag := range []uint16{301, 318, 319, 322, 323, 324, 325, 330, 532} {
		if field, present := base.field(tag); present {
			return fileError(ErrUnsupported, "tiff.layers", "mainIFD.layout", builder.span.Offset+field.data.Offset,
				"private source indexing requires an RGB8 stripped main image without alternate colorimetry")
		}
	}
	for _, field := range []struct {
		tag      uint16
		fallback int64
		value    int64
	}{{254, 0, 0}, {255, 1, 1}, {266, 1, 1}, {274, 1, 1}, {284, 1, 1}, {317, 1, 1}} {
		value, err := base.scalar(field.tag, field.fallback)
		if err != nil {
			return err
		}
		if value != field.value {
			return fileError(ErrUnsupported, "tiff.layers", "mainIFD.layout", builder.span.Offset,
				"private source indexing requires a full-resolution top-left chunky image without prediction")
		}
	}
	compression, err := base.scalar(259, 1)
	if err != nil {
		return err
	}
	if compression != 1 && compression != 32773 {
		return fileError(ErrUnsupported, "tiff.layers", "mainIFD.compression", builder.span.Offset,
			"private RGB8 source currently requires Raw or PackBits main image encoding")
	}
	rows, err := base.scalar(278, math.MaxUint32)
	if err != nil {
		return err
	}
	if rows <= 0 {
		return fileError(ErrMalformed, "tiff.layers", "mainIFD.rows", builder.span.Offset, "main image strip height is zero")
	}
	count := (height-1)/rows + 1
	offsets, present := base.field(273)
	counts, counted := base.field(279)
	if !present || !counted || int64(offsets.count) != count || int64(counts.count) != count {
		return fileError(ErrMalformed, "tiff.layers", "mainIFD.strips", builder.span.Offset,
			"main image strip tables do not match the canvas")
	}
	if err := builder.count(uint32(count), "mainIFD.strips", builder.span.Offset+offsets.data.Offset); err != nil {
		return err
	}
	for index := range count {
		offset, err := base.integer(offsets, uint32(index))
		if err != nil {
			return err
		}
		length, err := base.integer(counts, uint32(index))
		if err != nil {
			return err
		}
		if offset < 8 || length <= 0 || offset > builder.span.Length || length > builder.span.Length-offset {
			return fileError(ErrMalformed, "tiff.layers", "mainIFD.strip", builder.span.Offset+offset,
				"main image strip is outside the original TIFF")
		}
		if compression == 1 && length != width*samples*min(rows, height-index*rows) {
			return fileError(ErrMalformed, "tiff.layers", "mainIFD.strip", builder.span.Offset+offset,
				"main image raw strip byte count differs from its RGB8 rows")
		}
	}
	return nil
}

func (builder *tiffPrivateBuilder) block(
	cursor *tiffPrivateCursor, layer int, global bool,
) (AdditionalBlock, error) {
	start := cursor.position
	if builder.blocks >= builder.limits.MaxBlocks {
		return AdditionalBlock{}, fileError(ErrLimit, "tiff.layers", "blocks", start, "private blocks exceed MaxBlocks")
	}
	builder.blocks++
	cursor.path = fmt.Sprintf("private.layer.%d.block", layer)
	signature, key := cursor.signature(), cursor.signature()
	length := int64(cursor.u32())
	if cursor.err != nil {
		return AdditionalBlock{}, cursor.err
	}
	if signature != "8BIM" {
		return AdditionalBlock{}, fileError(ErrUnsupported, "tiff.layers", "private.signature", start,
			"only classic private 8BIM blocks are supported")
	}
	data := cursor.take(length)
	alignment := int64(2)
	if global {
		alignment = 4
	}
	padding := cursor.take((alignment - length%alignment) % alignment)
	if cursor.err != nil {
		return AdditionalBlock{}, cursor.err
	}
	block := AdditionalBlock{Signature: signature, Key: key, LengthWidth: 4,
		Span: Span{Offset: start, Length: cursor.position - start}, Data: data, Padding: padding}
	if err := builder.normalizeBlock(block, layer, global); err != nil {
		return AdditionalBlock{}, err
	}
	return block, nil
}

func (builder *tiffPrivateBuilder) normalizeLayers(span Span) error {
	cursor := tiffPrivateCursor{builder: builder, position: span.Offset, end: span.Offset + span.Length, path: "Layr"}
	count := int(int16(cursor.u16()))
	if count < 0 {
		count = -count
	}
	if cursor.err != nil {
		return cursor.err
	}
	if count > builder.limits.MaxLayers {
		return fileError(ErrLimit, "tiff.layers", "Layr.count", span.Offset, "private layers exceed MaxLayers")
	}
	type channel struct {
		length int64
		bounds image.Rectangle
	}
	channelScratch := int64(count) * 56 * (2*int64(unsafe.Sizeof(channel{})) + 2)
	if err := builder.reserve(channelScratch, "Layr.channels", span.Offset); err != nil {
		return err
	}
	var channels []channel
	for layer := 0; layer < count && cursor.err == nil; layer++ {
		cursor.path = fmt.Sprintf("Layr.layer.%d", layer)
		top, left, bottom, right := int32(cursor.u32()), int32(cursor.u32()), int32(cursor.u32()), int32(cursor.u32())
		if bottom < top || right < left {
			cursor.fail(ErrMalformed, "private layer bounds have reversed edges")
			break
		}
		bounds := image.Rect(int(left), int(top), int(right), int(bottom))
		componentCount := int(cursor.u16())
		if componentCount > 56 {
			cursor.fail(ErrMalformed, "private layer has too many channels")
			break
		}
		first := len(channels)
		ids := make([]int16, componentCount)
		for index := 0; index < componentCount && cursor.err == nil; index++ {
			ids[index] = int16(cursor.u16())
			length := int64(cursor.u32())
			channels = append(channels, channel{length: length, bounds: bounds})
		}
		if cursor.signature() != "8BIM" {
			cursor.fail(ErrMalformed, "invalid private layer blend signature")
			break
		}
		cursor.signature()
		cursor.take(4)
		extraLength := int64(cursor.u32())
		extraSpan := cursor.take(extraLength)
		if cursor.err != nil {
			break
		}
		extra := tiffPrivateCursor{builder: builder, position: extraSpan.Offset,
			end: extraSpan.Offset + extraSpan.Length, path: cursor.path + ".extra"}
		maskLength := int64(extra.u32())
		maskSpan := extra.take(maskLength)
		maskBounds, realBounds, hasReal, err := builder.normalizeMask(maskSpan)
		if err != nil {
			return err
		}
		for index, id := range ids {
			if id == -2 {
				channels[first+index].bounds = maskBounds
			}
			if id == -3 {
				channels[first+index].bounds = maskBounds
				if hasReal {
					channels[first+index].bounds = realBounds
				}
			}
		}
		ranges := int64(extra.u32())
		extra.take(ranges)
		nameLength := int64(extra.u8())
		extra.take(nameLength)
		extra.take((4 - (nameLength+1)%4) % 4)
		for extra.end-extra.position >= 12 && extra.err == nil {
			if _, err := builder.block(&extra, layer, false); err != nil {
				return err
			}
		}
		extra.padding(3)
		if extra.err != nil {
			return extra.err
		}
	}
	if cursor.err != nil {
		return cursor.err
	}
	for index, channel := range channels {
		cursor.path = fmt.Sprintf("Layr.channel.%d", index)
		data := cursor.take(channel.length)
		if cursor.err != nil {
			return cursor.err
		}
		encoded := tiffPrivateCursor{builder: builder, position: data.Offset,
			end: data.Offset + data.Length, path: cursor.path}
		if data.Length == 0 && channel.bounds.Empty() {
			continue
		}
		compression := encoded.u16()
		if encoded.err != nil {
			return encoded.err
		}
		switch compression {
		case 0:
			if int64(channel.bounds.Dx())*int64(channel.bounds.Dy()) != encoded.end-encoded.position {
				return fileError(ErrMalformed, "tiff.layers", cursor.path, data.Offset,
					"private raw channel length differs from its RGB8 bounds")
			}
		case 1:
			table := encoded.take(int64(channel.bounds.Dy()) * 2)
			if encoded.err != nil {
				return encoded.err
			}
			if err := builder.addSwap(table, 2, cursor.path+".rows"); err != nil {
				return err
			}
		default:
			return fileError(ErrUnsupported, "tiff.layers", cursor.path+".compression", data.Offset,
				"private layer channels currently require Raw or RLE encoding")
		}
	}
	cursor.padding(3)
	return cursor.err
}

func (builder *tiffPrivateBuilder) normalizeMask(span Span) (image.Rectangle, image.Rectangle, bool, error) {
	if span.Length == 0 {
		return image.Rectangle{}, image.Rectangle{}, false, nil
	}
	cursor := tiffPrivateCursor{builder: builder, position: span.Offset, end: span.Offset + span.Length, path: "Layr.mask"}
	readBounds := func() image.Rectangle {
		top, left, bottom, right := int32(cursor.u32()), int32(cursor.u32()), int32(cursor.u32()), int32(cursor.u32())
		return image.Rectangle{Min: image.Pt(int(left), int(top)), Max: image.Pt(int(right), int(bottom))}
	}
	bounds := readBounds()
	cursor.u8()
	flags := cursor.u8()
	if flags&8 != 0 && bounds == (image.Rectangle{Max: image.Pt(0, -1)}) {
		bounds = image.Rectangle{}
	}
	if flags&16 != 0 {
		parameters := cursor.u8()
		if parameters&0xf0 != 0 {
			cursor.fail(ErrUnsupported, "unknown private mask parameters")
		}
		for _, entry := range []struct{ bit, width uint8 }{{1, 1}, {2, 8}, {4, 1}, {8, 8}} {
			if parameters&entry.bit != 0 {
				if entry.width == 1 {
					cursor.u8()
				} else {
					cursor.u64()
				}
			}
		}
	}
	hasReal := span.Length != 20 && cursor.end-cursor.position >= 18
	real := image.Rectangle{}
	if hasReal {
		cursor.take(2)
		real = readBounds()
	}
	cursor.padding(3)
	if bounds.Dx() < 0 || bounds.Dy() < 0 || real.Dx() < 0 || real.Dy() < 0 {
		cursor.fail(ErrMalformed, "private mask bounds have reversed edges")
	}
	return bounds, real, hasReal, cursor.err
}

func (builder *tiffPrivateBuilder) opaqueBlock(layer int, key string, span Span) error {
	if span.Length == 0 {
		return nil
	}
	if len(builder.opaque) == cap(builder.opaque) {
		capacity := max(1, cap(builder.opaque)*2)
		if err := builder.reserve(int64(capacity-cap(builder.opaque))*int64(unsafe.Sizeof(TIFFOpaqueBlock{})),
			"opaque", span.Offset); err != nil {
			return err
		}
		owned := make([]TIFFOpaqueBlock, len(builder.opaque), capacity)
		copy(owned, builder.opaque)
		builder.opaque = owned
	}
	builder.opaque = append(builder.opaque, TIFFOpaqueBlock{LayerIndex: layer, Key: key, Data: span})
	return nil
}

func (builder *tiffPrivateBuilder) normalizeBlock(block AdditionalBlock, layer int, global bool) error {
	cursor := tiffPrivateCursor{builder: builder, position: block.Data.Offset,
		end: block.Data.Offset + block.Data.Length, path: "private." + block.Key}
	if global {
		switch block.Key {
		case "Layr":
			return builder.normalizeLayers(block.Data)
		case "lnk2", "lnkD", "lnk3", "lnkE":
			return builder.normalizeLinked(block.Data)
		case "LMsk", "FMsk":
			length := block.Data.Length
			if length == 0 {
				return nil
			}
			if length != 12 && length != 14 {
				return fileError(ErrUnsupported, "tiff.layers", cursor.path, cursor.position,
					"unknown private global mask schema")
			}
			if err := builder.addSwap(Span{Offset: cursor.position, Length: 12}, 2, cursor.path); err != nil {
				return err
			}
			return nil
		case "cinf":
			cursor.descriptor(true)
			cursor.padding(3)
			return cursor.err
		case "Patt", "Txt2", "Anno":
			return builder.opaqueBlock(layer, block.Key, block.Data)
		default:
			return fileError(ErrUnsupported, "tiff.layers", cursor.path, block.Data.Offset,
				"unknown private global block semantics")
		}
	}
	switch block.Key {
	case "luni":
		cursor.unicode()
	case "lyid", "clbl", "infx", "knko", "lspf", "sn2P", "lyvr", "lsct", "lsdk":
		cursor.u32()
	case "lnsr":
		cursor.signature()
	case "lclr":
		if err := builder.addSwap(block.Data, 2, cursor.path); err != nil {
			return err
		}
		cursor.position = cursor.end
	case "fxrp":
		cursor.u64()
		cursor.u64()
	case "SoLd", "SoLE":
		if cursor.signature() != "soLD" {
			cursor.fail(ErrUnsupported, "unknown private placement signature")
		}
		version := cursor.u32()
		if version != 4 && version != 5 {
			cursor.fail(ErrUnsupported, "unknown private placement version")
		}
		cursor.descriptor(true)
	case "TySh":
		return builder.normalizeText(block.Data)
	case "lfx2", "lmfx":
		if cursor.u32() != 0 {
			cursor.fail(ErrUnsupported, "unknown private effects object version")
		}
		cursor.descriptor(true)
	case "vscg":
		cursor.signature()
		cursor.descriptor(true)
	case "SoCo", "GdFl", "PtFl", "vstk":
		cursor.descriptor(true)
	case "vogk":
		if cursor.u32() != 1 {
			cursor.fail(ErrUnsupported, "unknown private vector origination version")
		}
		cursor.descriptor(true)
	case "PlLd", "vsms", "vmsk", "lrFX", "shmd":
		return builder.opaqueBlock(layer, block.Key, block.Data)
	default:
		return fileError(ErrUnsupported, "tiff.layers", cursor.path, block.Data.Offset,
			"unknown private layer block semantics")
	}
	cursor.padding(3)
	return cursor.err
}

func (builder *tiffPrivateBuilder) normalizeLinked(span Span) error {
	cursor := tiffPrivateCursor{builder: builder, position: span.Offset,
		end: span.Offset + span.Length, path: "private.linked"}
	for cursor.position < cursor.end && cursor.err == nil {
		length := cursor.u64()
		if length > math.MaxInt64 {
			cursor.fail(ErrMalformed, "private linked record length exceeds signed offsets")
			break
		}
		body := cursor.take(int64(length))
		cursor.take((4 - int64(length)%4) % 4)
		if cursor.err != nil {
			return cursor.err
		}
		if err := builder.count(1, cursor.path, body.Offset); err != nil {
			return err
		}
		entry := tiffPrivateCursor{builder: builder, position: body.Offset, end: body.Offset + body.Length,
			path: cursor.path + ".record"}
		kind, version := entry.signature(), entry.u32()
		if kind != "liFD" && kind != "liFE" && kind != "liFA" || version < 1 || version > 7 {
			entry.fail(ErrUnsupported, "unknown private linked record kind or version")
		}
		idLength := int64(entry.u8())
		entry.take(idLength)
		entry.unicode()
		entry.signature()
		entry.signature()
		size := entry.u64()
		if size > math.MaxInt64 {
			entry.fail(ErrMalformed, "private linked media length exceeds signed offsets")
		}
		open := entry.u8()
		if open > 1 {
			entry.fail(ErrMalformed, "private linked file-open flag is not boolean")
		}
		if open == 1 {
			entry.descriptor(true)
		}
		switch kind {
		case "liFE":
			entry.descriptor(true)
			if version > 3 {
				entry.u32()
				entry.take(4)
				entry.u64()
			}
			entry.u64()
		case "liFA":
			entry.take(8)
		}
		if kind == "liFD" || kind == "liFE" && version > 2 {
			entry.take(int64(size))
		}
		if version >= 5 {
			entry.unicode()
		}
		if version >= 6 {
			entry.u64()
		}
		if version >= 7 {
			entry.u8()
		}
		if kind == "liFE" && version == 2 {
			entry.take(int64(size))
		}
		if entry.err != nil {
			return entry.err
		}
		if entry.position < entry.end {
			if err := builder.opaqueBlock(-1, "linked.tail", Span{Offset: entry.position,
				Length: entry.end - entry.position}); err != nil {
				return err
			}
		}
	}
	return cursor.err
}
