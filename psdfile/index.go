package psdfile

import (
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"math"
	"strings"
	"unicode/utf16"
	"unsafe"
)

func (d *Document) index() error {
	budget := &indexBudget{document: d}
	if err := budget.reserve(int64(unsafe.Sizeof(*d))+4, "header", 0); err != nil {
		return err
	}
	cursor := &indexCursor{document: d, end: d.size, path: "document"}
	if err := d.indexHeader(cursor); err != nil {
		return err
	}
	d.ColorModeData, _ = cursor.section(4, "colorModeData")
	var resources Span
	d.ResourceSection, resources = cursor.section(4, "resources")
	if cursor.err != nil {
		return cursor.err
	}
	if err := d.indexResources(cursor.child(resources, "resources"), budget); err != nil {
		return err
	}
	lengthWidth := uint8(4)
	if d.Header.Version == 2 {
		lengthWidth = 8
	}
	var layerMask Span
	d.LayerMaskSection, layerMask = cursor.section(lengthWidth, "layerMask")
	if cursor.err != nil {
		return cursor.err
	}
	if layerMask.Length != 0 {
		if err := d.indexLayerMask(cursor.child(layerMask, "layerMask"), budget, lengthWidth); err != nil {
			return err
		}
	}
	d.selectLayers()
	if err := d.indexComposite(cursor); err != nil {
		return err
	}
	d.metadataBytes = budget.metadata
	return nil
}

func (d *Document) indexHeader(cursor *indexCursor) error {
	var data [26]byte
	cursor.path = "header"
	cursor.read(data[:], "data")
	if cursor.err != nil {
		return cursor.err
	}
	d.HeaderData = Span{Length: 26}
	d.Header = Header{
		Signature: string(data[:4]), Version: binary.BigEndian.Uint16(data[4:6]),
		Channels: binary.BigEndian.Uint16(data[12:14]), Height: binary.BigEndian.Uint32(data[14:18]),
		Width: binary.BigEndian.Uint32(data[18:22]), Depth: binary.BigEndian.Uint16(data[22:24]),
		ColorMode: binary.BigEndian.Uint16(data[24:26]),
	}
	copy(d.Header.Reserved[:], data[6:12])
	if d.Header.Signature != "8BPS" {
		return fileError(ErrMalformed, "index", "header.signature", 0, "expected Photoshop signature")
	}
	if d.Header.Version != 1 && d.Header.Version != 2 {
		return fileError(ErrMalformed, "index", "header.version", 4, "expected PSD or PSB version")
	}
	if d.Header.Reserved != [6]byte{} {
		return fileError(ErrMalformed, "index", "header.reserved", 6, "reserved bytes must be zero")
	}
	if d.Header.Channels == 0 || d.Header.Channels > 56 {
		return fileError(ErrMalformed, "index", "header.channels", 12, "channel count must be between 1 and 56")
	}
	dimensionLimit := uint32(30_000)
	if d.Header.Version == 2 {
		dimensionLimit = 300_000
	}
	if d.Header.Height == 0 || d.Header.Height > dimensionLimit {
		return fileError(ErrMalformed, "index", "header.height", 14, "height exceeds the format's dimension range")
	}
	if d.Header.Width == 0 || d.Header.Width > dimensionLimit {
		return fileError(ErrMalformed, "index", "header.width", 18, "width exceeds the format's dimension range")
	}
	switch d.Header.Depth {
	case 1, 8, 16, 32:
	default:
		return fileError(ErrMalformed, "index", "header.depth", 22, "invalid source sample depth")
	}
	switch d.Header.ColorMode {
	case 0, 1, 2, 3, 4, 7, 8, 9:
	default:
		return fileError(ErrMalformed, "index", "header.colorMode", 24, "invalid source color mode")
	}
	cursor.path = "document"
	return nil
}

func (d *Document) indexResources(cursor *indexCursor, budget *indexBudget) error {
	for cursor.remaining() > 0 {
		start := cursor.position
		path := fmt.Sprintf("resources[%d]", len(d.Resources))
		cursor.path = path
		if err := budget.block(path, start); err != nil {
			return err
		}
		block := ResourceBlock{Signature: cursor.signature("signature"), ID: cursor.u16("id")}
		if cursor.err != nil {
			return cursor.err
		}
		if block.Signature != "8BIM" {
			return fileError(ErrMalformed, "index", path+".signature", start, "invalid image resource signature")
		}
		var err error
		block.Name, block.NameData, err = indexPascal(cursor, budget, 2, "name")
		if err != nil {
			return err
		}
		_, block.Data = cursor.section(4, "data")
		block.Padding = cursor.take(block.Data.Length%2, "padding")
		if cursor.err != nil {
			return cursor.err
		}
		block.Span = Span{Offset: start, Length: cursor.position - start}
		if err := budget.reserve(4, path, start); err != nil {
			return err
		}
		if err := indexAppend(budget, &d.Resources, block, path, start); err != nil {
			return err
		}
	}
	return nil
}

func indexPascal(
	cursor *indexCursor, budget *indexBudget, alignment int64, field string,
) (string, Span, error) {
	start := cursor.position
	length := int64(cursor.u8(field + ".length"))
	if cursor.err != nil {
		return "", Span{}, cursor.err
	}
	if err := budget.reserve(length, cursor.path+"."+field, start); err != nil {
		return "", Span{}, err
	}
	var data [255]byte
	cursor.read(data[:length], field)
	cursor.take((alignment-(length+1)%alignment)%alignment, field+".padding")
	return string(data[:length]), Span{Offset: start, Length: cursor.position - start}, cursor.err
}

func (d *Document) indexLayerMask(cursor *indexCursor, budget *indexBudget, lengthWidth uint8) error {
	var layers Span
	d.LayerInfo, layers = cursor.section(lengthWidth, "layerInfo")
	if cursor.err != nil {
		return cursor.err
	}
	if layers.Length != 0 {
		set, err := d.indexLayerSet(cursor.child(layers, "layerSets[0]"), budget, "main", d.Header.Depth)
		if err != nil {
			return err
		}
		if err := indexAppend(budget, &d.LayerSets, set, "layerSets[0]", layers.Offset); err != nil {
			return err
		}
	}
	if cursor.remaining() >= 4 {
		d.GlobalMask, _ = cursor.section(4, "globalMask")
		if cursor.err != nil {
			return cursor.err
		}
	}
	blocks, err := d.indexAdditional(cursor, budget, true)
	if err != nil {
		return err
	}
	d.AdditionalInfo = blocks
	for _, block := range blocks {
		depth := uint16(0)
		switch block.Key {
		case "Layr":
			depth = 8
		case "Lr16":
			depth = 16
		case "Lr32":
			depth = 32
		default:
			continue
		}
		if block.Data.Length == 0 {
			continue
		}
		path := fmt.Sprintf("layerSets[%d]", len(d.LayerSets))
		set, err := d.indexLayerSet(cursor.child(block.Data, path), budget, block.Key, depth)
		if err != nil {
			return err
		}
		if err := indexAppend(budget, &d.LayerSets, set, path, block.Data.Offset); err != nil {
			return err
		}
	}
	return nil
}

func (d *Document) indexLayerSet(
	cursor *indexCursor, budget *indexBudget, key string, depth uint16,
) (LayerSet, error) {
	set := LayerSet{Key: key, Depth: depth, Span: Span{Offset: cursor.position, Length: cursor.remaining()}}
	count := int(int16(cursor.u16("count")))
	if cursor.err != nil {
		return set, cursor.err
	}
	if count < 0 {
		set.GlobalAlpha = true
		count = -count
	}
	if count > d.limits.MaxLayers-budget.layers {
		return set, fileError(ErrLimit, "index", cursor.path+".count", set.Span.Offset, "layers exceed MaxLayers")
	}
	budget.layers += count
	if err := budget.reserve(int64(count)*int64(unsafe.Sizeof(LayerRecord{})), cursor.path, cursor.position); err != nil {
		return set, err
	}
	set.Layers = make([]LayerRecord, count)
	setPath := cursor.path
	for index := range set.Layers {
		cursor.path = fmt.Sprintf("%s.layers[%d]", setPath, index)
		layer, err := d.indexLayerRecord(cursor, budget)
		if err != nil {
			return set, err
		}
		set.Layers[index] = layer
	}
	for index := range set.Layers {
		layer := &set.Layers[index]
		for channelIndex := range layer.Channels {
			cursor.path = fmt.Sprintf("%s.layers[%d].channels[%d]", setPath, index, channelIndex)
			if err := d.indexChannel(cursor, &layer.Channels[channelIndex], layer, depth); err != nil {
				return set, err
			}
		}
	}
	set.Padding = Span{Offset: cursor.position, Length: cursor.remaining()}
	if set.Padding.Length > 3 {
		return set, fileError(ErrMalformed, "index", setPath+".padding", cursor.position,
			"unconsumed layer information exceeds alignment padding")
	}
	return set, nil
}

func indexRectangle(cursor *indexCursor, field string) image.Rectangle {
	bounds := indexRectangleValues(cursor, field)
	if bounds.Max.Y < bounds.Min.Y || bounds.Max.X < bounds.Min.X {
		cursor.fail(cursor.path+"."+field, cursor.position-16, "rectangle has negative width or height")
	}
	return bounds
}

func indexRectangleValues(cursor *indexCursor, field string) image.Rectangle {
	top := int(int32(cursor.u32(field + ".top")))
	left := int(int32(cursor.u32(field + ".left")))
	bottom := int(int32(cursor.u32(field + ".bottom")))
	right := int(int32(cursor.u32(field + ".right")))
	return image.Rectangle{Min: image.Pt(left, top), Max: image.Pt(right, bottom)}
}

func (d *Document) indexLayerRecord(cursor *indexCursor, budget *indexBudget) (LayerRecord, error) {
	start := cursor.position
	layer := LayerRecord{Bounds: indexRectangle(cursor, "bounds")}
	count := int(cursor.u16("channelCount"))
	if cursor.err != nil {
		return layer, cursor.err
	}
	if count > 56 {
		return layer, fileError(ErrMalformed, "index", cursor.path+".channelCount", start+16, "too many layer channels")
	}
	if err := budget.reserve(int64(count)*int64(unsafe.Sizeof(ChannelRecord{}))+8, cursor.path, start); err != nil {
		return layer, err
	}
	layer.Channels = make([]ChannelRecord, count)
	lengthWidth := uint8(4)
	if d.Header.Version == 2 {
		lengthWidth = 8
	}
	hasRealMask := false
	for index := range layer.Channels {
		id := int16(cursor.u16(fmt.Sprintf("channels[%d].id", index)))
		hasRealMask = hasRealMask || id == -3
		length := cursor.length(lengthWidth, fmt.Sprintf("channels[%d].length", index))
		layer.Channels[index] = ChannelRecord{ID: id, LengthWidth: lengthWidth, DeclaredLength: uint64(length)}
	}
	layer.BlendSignature = cursor.signature("blendSignature")
	layer.BlendMode = cursor.signature("blendMode")
	layer.Opacity, layer.Clipping = cursor.u8("opacity"), cursor.u8("clipping")
	layer.Flags, layer.Filler = cursor.u8("flags"), cursor.u8("filler")
	_, layer.Extra = cursor.section(4, "extra")
	if cursor.err != nil {
		return layer, cursor.err
	}
	if layer.BlendSignature != "8BIM" {
		blendOffset := start + 18 + int64(count)*(2+int64(lengthWidth))
		return layer, fileError(ErrMalformed, "index", cursor.path+".blendSignature", blendOffset,
			"invalid layer blend signature")
	}
	layer.Span = Span{Offset: start, Length: cursor.position - start}
	extra := cursor.child(layer.Extra, cursor.path+".extra")
	var mask Span
	layer.MaskData, mask = extra.section(4, "mask")
	if extra.err != nil {
		return layer, extra.err
	}
	if mask.Length != 0 {
		var err error
		layer.Mask, err = indexMask(extra.child(mask, extra.path+".mask"), budget, hasRealMask)
		if err != nil {
			return layer, err
		}
	}
	layer.BlendRanges, _ = extra.section(4, "blendRanges")
	if extra.err != nil {
		return layer, extra.err
	}
	var err error
	layer.Name, layer.NameData, err = indexPascal(extra, budget, 4, "name")
	if err != nil {
		return layer, err
	}
	layer.AdditionalInfo, err = d.indexAdditional(extra, budget, false)
	if err != nil {
		return layer, err
	}
	for _, block := range layer.AdditionalInfo {
		metadata := extra.child(block.Data, cursor.path+".additionalInfo."+block.Key)
		switch block.Key {
		case "luni":
			layer.Name, err = indexUnicode(metadata, budget)
		case "lyid":
			layer.ID, layer.HasID = metadata.u32("id"), true
		case "lsct", "lsdk":
			layer.Divider = metadata.u32("divider")
		}
		if err != nil {
			return layer, err
		}
		if metadata.err != nil {
			return layer, metadata.err
		}
	}
	return layer, nil
}

func indexMask(cursor *indexCursor, budget *indexBudget, hasReal bool) (*MaskRecord, error) {
	if err := budget.reserve(int64(unsafe.Sizeof(MaskRecord{})), cursor.path, cursor.position); err != nil {
		return nil, err
	}
	mask := &MaskRecord{Span: Span{Offset: cursor.position, Length: cursor.remaining()}}
	mask.Bounds = indexRectangleValues(cursor, "bounds")
	mask.DefaultColor, mask.Flags = cursor.u8("defaultColor"), cursor.u8("flags")
	// Photoshop uses this sentinel for an empty mask generated from vector geometry.
	if mask.Flags&8 != 0 && mask.Bounds == (image.Rectangle{Max: image.Pt(0, -1)}) {
		mask.Bounds = image.Rectangle{}
	} else if mask.Bounds.Max.Y < mask.Bounds.Min.Y || mask.Bounds.Max.X < mask.Bounds.Min.X {
		cursor.fail(cursor.path+".bounds", mask.Span.Offset, "rectangle has negative width or height")
	}
	if cursor.err != nil {
		return nil, cursor.err
	}
	// The -3 channel identifies the real header; parameter bytes alone can exceed its size.
	if hasReal {
		mask.HasReal = true
		mask.RealFlags, mask.RealDefaultColor = cursor.u8("realFlags"), cursor.u8("realDefaultColor")
		mask.RealBounds = indexRectangle(cursor, "realBounds")
	}
	if mask.Flags&16 != 0 {
		mask.ParameterFlags = cursor.u8("parameterFlags")
		if mask.ParameterFlags&1 != 0 {
			value := cursor.u8("userDensity")
			mask.UserDensity = &value
			if err := budget.reserve(1, cursor.path+".userDensity", cursor.position); err != nil {
				return nil, err
			}
		}
		if mask.ParameterFlags&2 != 0 {
			value := cursor.f64("userFeather")
			mask.UserFeather = &value
			if err := budget.reserve(8, cursor.path+".userFeather", cursor.position); err != nil {
				return nil, err
			}
		}
		if mask.ParameterFlags&4 != 0 {
			value := cursor.u8("vectorDensity")
			mask.VectorDensity = &value
			if err := budget.reserve(1, cursor.path+".vectorDensity", cursor.position); err != nil {
				return nil, err
			}
		}
		if mask.ParameterFlags&8 != 0 {
			value := cursor.f64("vectorFeather")
			mask.VectorFeather = &value
			if err := budget.reserve(8, cursor.path+".vectorFeather", cursor.position); err != nil {
				return nil, err
			}
		}
	}
	if cursor.err != nil {
		return nil, cursor.err
	}
	return mask, nil
}

func indexUnicode(cursor *indexCursor, budget *indexBudget) (string, error) {
	count := int64(cursor.u32("name.length"))
	if cursor.err != nil {
		return "", cursor.err
	}
	if count > cursor.remaining()/2 {
		return "", fileError(ErrMalformed, "index", cursor.path+".name", cursor.position,
			"Unicode string exceeds its enclosing block")
	}
	// Bound temporary UTF-16, rune and UTF-8 allocations as well as the retained string.
	if count > (budget.document.limits.MaxMetadataBytes-budget.metadata)/11 || count > int64(^uint(0)>>1) {
		return "", fileError(ErrLimit, "index", cursor.path+".name", cursor.position,
			"Unicode metadata allocation exceeds MaxMetadataBytes")
	}
	data := make([]byte, int(count*2))
	cursor.read(data, "name")
	if cursor.err != nil {
		return "", cursor.err
	}
	units := make([]uint16, int(count))
	for index := range units {
		units[index] = binary.BigEndian.Uint16(data[index*2:])
	}
	if len(units) > 0 && units[len(units)-1] == 0 {
		units = units[:len(units)-1]
	}
	value := string(utf16.Decode(units))
	if err := budget.reserve(int64(len(value)), cursor.path+".name", cursor.position); err != nil {
		return "", err
	}
	return value, nil
}

func (d *Document) indexAdditional(
	cursor *indexCursor, budget *indexBudget, global bool,
) ([]AdditionalBlock, error) {
	var blocks []AdditionalBlock
	path := cursor.path
	for cursor.remaining() >= 12 {
		// Some writers align global tags to four bytes, in addition to the required even padding.
		var first [4]byte
		probe := cursor.child(Span{Offset: cursor.position, Length: 4}, path)
		probe.read(first[:], "alignment")
		if probe.err != nil {
			return nil, probe.err
		}
		alignment := 0
		for alignment < len(first) && first[alignment] == 0 {
			alignment++
		}
		if alignment > 3 {
			return nil, fileError(ErrMalformed, "index", path+".additionalInfo.alignment", cursor.position,
				"additional information gap exceeds three alignment bytes")
		}
		cursor.take(int64(alignment), "alignment")
		if cursor.remaining() < 12 {
			break
		}
		start := cursor.position
		cursor.path = fmt.Sprintf("%s.additionalInfo[%d]", path, len(blocks))
		if err := budget.block(cursor.path, start); err != nil {
			return nil, err
		}
		block := AdditionalBlock{Signature: cursor.signature("signature"), Key: cursor.signature("key"), LengthWidth: 4}
		if cursor.err != nil {
			return nil, cursor.err
		}
		if block.Signature != "8BIM" && block.Signature != "8B64" {
			return nil, fileError(ErrMalformed, "index", cursor.path+".signature", start,
				"invalid additional information signature")
		}
		if block.Signature == "8B64" || (d.Header.Version == 2 && indexPSBLength64(block.Key)) {
			block.LengthWidth = 8
		}
		_, block.Data = cursor.section(block.LengthWidth, "data")
		block.Padding = cursor.take(block.Data.Length%2, "padding")
		if cursor.err != nil {
			return nil, cursor.err
		}
		if global && (block.Data.Length+block.Padding.Length)%4 != 0 && cursor.remaining() >= 2 {
			// Accept both even-aligned Adobe blocks and four-aligned Photoshop global blocks.
			signature := ""
			if cursor.remaining() >= 4 {
				probe := cursor.child(Span{Offset: cursor.position, Length: 4}, cursor.path)
				signature = probe.signature("alignment")
				if probe.err != nil {
					return nil, probe.err
				}
			}
			if signature != "8BIM" && signature != "8B64" {
				cursor.take(2, "padding")
				block.Padding.Length += 2
			}
		}
		block.Span = Span{Offset: start, Length: cursor.position - start}
		if err := budget.reserve(8, cursor.path, start); err != nil {
			return nil, err
		}
		if err := indexAppend(budget, &blocks, block, cursor.path, start); err != nil {
			return nil, err
		}
	}
	cursor.path = path
	if cursor.remaining() > 3 {
		return nil, &Error{Kind: ErrMalformed, Feature: "index", Path: path + ".additionalInfo",
			Offset: cursor.position, Detail: "truncated additional information header", Cause: io.ErrUnexpectedEOF}
	}
	if cursor.remaining() >= 2 {
		var trailing [3]byte
		probe := cursor.child(Span{Offset: cursor.position, Length: cursor.remaining()}, path)
		probe.read(trailing[:cursor.remaining()], "additionalInfo.padding")
		if probe.err != nil {
			return nil, probe.err
		}
		prefix := string(trailing[:cursor.remaining()])
		if strings.HasPrefix("8BIM", prefix) || strings.HasPrefix("8B64", prefix) {
			return nil, &Error{Kind: ErrMalformed, Feature: "index", Path: path + ".additionalInfo",
				Offset: cursor.position, Detail: "truncated additional information signature", Cause: io.ErrUnexpectedEOF}
		}
	}
	return blocks, nil
}

func indexPSBLength64(key string) bool {
	switch key {
	case "LMsk", "Lr16", "Lr32", "Layr", "Mt16", "Mt32", "Mtrn", "Alph", "FMsk", "lnk2", "FEid", "FXid", "PxSD":
		return true
	default:
		return false
	}
}

func (d *Document) selectLayers() {
	for index := range d.LayerSets {
		if d.LayerSets[index].Depth == d.Header.Depth {
			d.ActiveLayerSet = index
		}
	}
	if d.ActiveLayerSet >= 0 {
		set := &d.LayerSets[d.ActiveLayerSet]
		d.Layers, d.GlobalAlpha = set.Layers, set.GlobalAlpha
	}
}

func (d *Document) indexChannel(
	cursor *indexCursor, channel *ChannelRecord, layer *LayerRecord, depth uint16,
) error {
	channel.Bounds = layer.Bounds
	if channel.ID == -2 || channel.ID == -3 {
		if layer.Mask == nil {
			return fileError(ErrMalformed, "index", cursor.path+".bounds", cursor.position,
				"mask channel has no mask geometry")
		}
		channel.Bounds = layer.Mask.Bounds
		if channel.ID == -3 && layer.Mask.HasReal {
			channel.Bounds = layer.Mask.RealBounds
		}
	}
	channel.Span = cursor.take(int64(channel.DeclaredLength), "data")
	if cursor.err != nil {
		return cursor.err
	}
	data := cursor.child(channel.Span, cursor.path)
	if channel.Span.Length == 0 && channel.Bounds.Empty() {
		channel.Data = channel.Span
		return nil
	}
	channel.Compression = data.u16("compression")
	channel.Data = Span{Offset: data.position, Length: data.remaining()}
	if data.err != nil {
		return data.err
	}
	return d.checkIndexedPixels(data, channel.Compression, channel.Bounds.Dx(), channel.Bounds.Dy(), depth, 1, false)
}

func (d *Document) indexComposite(cursor *indexCursor) error {
	cursor.path = "composite"
	d.Composite.Span = Span{Offset: cursor.position, Length: cursor.remaining()}
	d.Composite.Compression = cursor.u16("compression")
	d.Composite.Data = Span{Offset: cursor.position, Length: cursor.remaining()}
	d.Composite.RowCountWidth = 2
	if d.Header.Version == 2 {
		d.Composite.RowCountWidth = 4
	}
	if cursor.err != nil {
		return cursor.err
	}
	placeholder, err := d.hasPlaceholderComposite()
	if err != nil {
		return err
	}
	return d.checkIndexedPixels(cursor, d.Composite.Compression, int(d.Header.Width), int(d.Header.Height),
		d.Header.Depth, int(d.Header.Channels), placeholder || d.linkedContent)
}

func (d *Document) hasPlaceholderComposite() (bool, error) {
	seen := false
	for _, resource := range d.Resources {
		if resource.ID != 1057 {
			continue
		}
		if resource.Data.Length < 5 {
			return false, nil
		}
		probe := &indexCursor{document: d, position: resource.Data.Offset,
			end: resource.Data.Offset + resource.Data.Length, path: "resources.versionInfo"}
		version, real := probe.u32("version"), probe.u8("hasRealMergedData")
		if probe.err != nil {
			return false, probe.err
		}
		if version != 1 || real != 0 {
			return false, nil
		}
		seen = true
	}
	return seen, nil
}

func (d *Document) retainCompositeTail(offset, length int64) {
	d.Composite.ReservedTail = Span{Offset: offset, Length: length}
	d.Composite.Data.Length -= length
}

func (d *Document) checkIndexedPixels(
	cursor *indexCursor, compression uint16, width, height int, depth uint16, channels int, retainTail bool,
) error {
	if compression > 3 {
		return fileError(ErrMalformed, "index", cursor.path+".compression", cursor.position-2,
			"invalid pixel compression method")
	}
	rowBytes := (int64(width)*int64(depth) + 7) / 8
	if height != 0 && rowBytes > math.MaxInt64/int64(height) ||
		channels != 0 && rowBytes*int64(height) > math.MaxInt64/int64(channels) {
		return fileError(ErrMalformed, "index", cursor.path+".data", cursor.position,
			"pixel byte count cannot be represented as a source offset")
	}
	expected := rowBytes * int64(height) * int64(channels)
	if compression == 0 {
		if cursor.remaining() != expected && (expected%2 != 1 || cursor.remaining() != expected+1) {
			if retainTail && cursor.remaining() > expected {
				d.retainCompositeTail(cursor.position+expected, cursor.remaining()-expected)
				return nil
			}
			return fileError(ErrMalformed, "index", cursor.path+".data", cursor.position,
				"raw pixel byte count does not match channel dimensions and depth")
		}
		return nil
	}
	if compression != 1 {
		return nil
	}
	countWidth := int64(2)
	if d.Header.Version == 2 {
		countWidth = 4
	}
	rows := int64(height) * int64(channels)
	tableLength := rows * countWidth
	if tableLength > cursor.remaining() {
		return &Error{Kind: ErrMalformed, Feature: "index", Path: cursor.path + ".rleTable", Offset: cursor.position,
			Detail: "RLE row-count table exceeds channel data", Cause: io.ErrUnexpectedEOF}
	}
	var counts [4096]byte
	var total int64
	for remaining := tableLength; remaining > 0; {
		length := min(remaining, int64(len(counts)))
		cursor.read(counts[:length], "rleTable")
		if cursor.err != nil {
			return cursor.err
		}
		for offset := int64(0); offset < length; offset += countWidth {
			value := int64(binary.BigEndian.Uint16(counts[offset:]))
			if countWidth == 4 {
				value = int64(binary.BigEndian.Uint32(counts[offset:]))
			}
			if rowBytes != 0 && value < 2 {
				return fileError(ErrMalformed, "index", cursor.path+".rleTable", cursor.position-length+offset,
					"RLE row cannot encode a nonempty scan line")
			}
			if value > math.MaxInt64-total {
				return fileError(ErrMalformed, "index", cursor.path+".rleTable", cursor.position-length+offset,
					"RLE row-count total overflows source offsets")
			}
			total += value
		}
		remaining -= length
	}
	if total != cursor.remaining() && (total%2 != 1 || cursor.remaining() != total+1) {
		if retainTail && cursor.remaining() > total {
			d.retainCompositeTail(cursor.position+total, cursor.remaining()-total)
			return nil
		}
		return fileError(ErrMalformed, "index", cursor.path+".rleTable", cursor.position-tableLength,
			"RLE row counts do not match compressed channel size")
	}
	return nil
}
