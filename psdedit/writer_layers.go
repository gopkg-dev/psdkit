package psdedit

import (
	"context"
	"encoding/binary"
	"image"
	"maps"
	"math"
	"slices"
	"unicode/utf16"

	"github.com/gopkg-dev/psdkit/psdfile"
)

type writerChannel struct {
	id   int16
	part writePart
	size int64
}

func (e *Editor) prepareLayersLocked(
	ctx context.Context, option SaveOptions, globalAlpha bool, compositeBytes int64,
) (writePlan, error) {
	plan := writePlan{limit: option.MaxOutputBytes}
	physical := e.writeLayersLocked()
	if len(physical) > min(e.source.Limits().MaxLayers, math.MaxInt16) {
		return plan, editError(psdfile.ErrLimit, "save.layers", "layer count exceeds source budget or signed format count")
	}
	count := int16(len(physical))
	if globalAlpha {
		count = -count
	}
	plan.bytes(binary.BigEndian.AppendUint16(nil, uint16(count)))
	channels := writePlan{limit: option.MaxOutputBytes}
	encodedBytes, metadataBytes := compositeBytes, int64(0)
	if encodedBytes > e.source.Limits().MaxDecodeBytes {
		return plan, editError(psdfile.ErrLimit, "save.composite", "encoded preview exceeds working buffer budget")
	}
	for _, layer := range physical {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		record, payload, err := e.writeLayerLocked(ctx, option, layer, &encodedBytes)
		if err != nil {
			return plan, err
		}
		for _, part := range record.parts {
			if part.content {
				continue
			}
			if int64(len(part.data)) > e.source.Limits().MaxMetadataBytes-metadataBytes {
				return plan, editError(psdfile.ErrLimit, "save.metadata", "changed metadata exceeds MaxMetadataBytes")
			}
			metadataBytes += int64(len(part.data))
		}
		plan.append(record)
		channels.append(payload)
		if plan.err != nil || channels.err != nil {
			return plan, errorsForPlans(plan, channels)
		}
	}
	plan.append(channels)
	padding := psdfile.Span{}
	if e.source.ActiveLayerSet >= 0 {
		padding = e.source.LayerSets[e.source.ActiveLayerSet].Padding
	}
	// Keep existing trailing alignment bytes whenever they still produce an even section length.
	paddingLength := padding.Length
	if (plan.size+paddingLength)%2 != 0 {
		if paddingLength == 3 {
			paddingLength--
		} else {
			paddingLength++
		}
	}
	writerAppendPadding(&plan, padding, paddingLength)
	return plan, plan.err
}

func errorsForPlans(first, second writePlan) error {
	if first.err != nil {
		return first.err
	}
	return second.err
}

func (e *Editor) writeLayerLocked(
	ctx context.Context, option SaveOptions, layer writeLayer, allocated *int64,
) (writePlan, writePlan, error) {
	record, payload := writePlan{limit: option.MaxOutputBytes}, writePlan{limit: option.MaxOutputBytes}
	node := layer.node
	original := psdfile.LayerRecord{
		Bounds: node.bounds, BlendSignature: "8BIM", BlendMode: "norm", Opacity: node.opacity, Flags: 8,
	}
	if layer.sourceIndex >= 0 {
		original = e.source.Layers[layer.sourceIndex]
	}
	bounds := original.Bounds
	if layer.sourceIndex < 0 || (!layer.end && node.pixels != nil) {
		bounds = node.bounds
	}
	if layer.sourceIndex < 0 && node.group {
		bounds = image.Rectangle{}
	}
	if err := writerBounds(bounds); err != nil {
		return record, payload, err
	}
	channels, err := e.writeChannelsLocked(ctx, option, layer, original, allocated)
	if err != nil {
		return record, payload, err
	}
	if len(channels) > 56 {
		return record, payload, editError(psdfile.ErrLimit, "save.layer.channels", "layer has more than 56 channels")
	}
	extra, err := e.writeExtraLocked(option.MaxOutputBytes, layer, original)
	if err != nil {
		return record, payload, err
	}
	header := binary.BigEndian.AppendUint32(nil, uint32(int32(bounds.Min.Y)))
	header = binary.BigEndian.AppendUint32(header, uint32(int32(bounds.Min.X)))
	header = binary.BigEndian.AppendUint32(header, uint32(int32(bounds.Max.Y)))
	header = binary.BigEndian.AppendUint32(header, uint32(int32(bounds.Max.X)))
	header = binary.BigEndian.AppendUint16(header, uint16(len(channels)))
	width := versionLengthWidth(e.source.Header.Version)
	for _, channel := range channels {
		header = binary.BigEndian.AppendUint16(header, uint16(channel.id))
		if width == 4 {
			if channel.size > math.MaxUint32 {
				return record, payload, editError(psdfile.ErrLimit, "save.channel.length", "channel length exceeds PSD width")
			}
			header = binary.BigEndian.AppendUint32(header, uint32(channel.size))
		} else {
			header = binary.BigEndian.AppendUint64(header, uint64(channel.size))
		}
		payload.addPart(channel.part)
	}
	blend := original.BlendMode
	opacity, flags := original.Opacity, original.Flags
	if layer.sourceIndex < 0 {
		if node.group && !layer.end {
			blend = "pass"
		}
		if !node.visible && !layer.end {
			flags |= 2
		}
		if layer.end {
			opacity = 255
		}
	} else if !layer.end {
		if node.opacityChanged {
			opacity = node.opacity
		}
		if node.visibleChanged {
			flags &^= 2
			if !node.visible {
				flags |= 2
			}
		}
	}
	header = append(header, original.BlendSignature...)
	header = append(header, blend...)
	header = append(header, opacity, original.Clipping, flags, original.Filler)
	record.bytes(header)
	record.length(4, extra.size, "save.layer.extra")
	record.append(extra)
	return record, payload, errorsForPlans(record, payload)
}

func writerBounds(bounds image.Rectangle) error {
	if bounds.Min.X < math.MinInt32 || bounds.Min.Y < math.MinInt32 ||
		bounds.Max.X > math.MaxInt32 || bounds.Max.Y > math.MaxInt32 || bounds.Dx() < 0 || bounds.Dy() < 0 {
		return editError(
			ErrInvalidEdit, "save.layer.bounds", "layer bounds cannot be represented by signed format coordinates",
		)
	}
	return nil
}

func (e *Editor) writeChannelsLocked(
	ctx context.Context, option SaveOptions, layer writeLayer, original psdfile.LayerRecord, allocated *int64,
) ([]writerChannel, error) {
	node := layer.node
	replacements := map[int16]psdfile.Plane{}
	workingSamples := int64(0)
	if !layer.end && !node.group {
		if node.pixels != nil || node.virtualComposite {
			if node.pixels == nil {
				width, height := int64(e.source.Header.Width), int64(e.source.Header.Height)
				if height <= 0 || width > e.source.Limits().MaxDecodeBytes/height/8 {
					return nil, editError(psdfile.ErrLimit, "save.working", "virtual source decode exceeds remaining budget")
				}
				if _, err := e.writerOutputLimitLocked(option, *allocated, width*height*8, 0, 0); err != nil {
					return nil, err
				}
			}
			pixels, err := e.nodePixelsLocked(ctx, node)
			if err != nil {
				return nil, err
			}
			width, height := int64(pixels.Rect.Dx()), int64(pixels.Rect.Dy())
			if height != 0 && width > e.source.Limits().MaxDecodeBytes/height/4 {
				return nil, editError(psdfile.ErrLimit, "save.working", "changed layer planes exceed MaxDecodeBytes")
			}
			workingSamples = width * height * 4
			if node.pixels == nil {
				workingSamples += int64(cap(pixels.Pix))
			}
			if _, err := e.writerOutputLimitLocked(option, *allocated, workingSamples, 0, 0); err != nil {
				return nil, err
			}
			planes, err := writerImagePlanes(ctx, pixels)
			if err != nil {
				return nil, err
			}
			for _, plane := range planes {
				replacements[plane.ID] = plane
			}
		}
		maps.Copy(replacements, node.planes)
	}
	seen := make(map[int16]bool)
	channels := make([]writerChannel, 0, len(original.Channels)+len(replacements))
	appendChanged := func(plane psdfile.Plane) error {
		remaining := min(option.MaxOutputBytes, e.source.Limits().MaxDecodeBytes-*allocated)
		if remaining <= 0 {
			return editError(psdfile.ErrLimit, "save.channels", "changed channel buffers exceed MaxDecodeBytes")
		}
		compression := option.Compression
		// Photoshop interprets 32-bit ZIP layer payloads using prediction, even with a ZIP-only request.
		if plane.Depth == 32 && compression == 2 {
			compression = 3
		}
		capacityLimit, err := e.writerOutputLimitLocked(option, *allocated, workingSamples, compression, int64(plane.RowBytes))
		if err != nil {
			return err
		}
		remaining = min(remaining, capacityLimit)
		encodedBound, err := writerPlaneEncodedBound(plane, compression, e.source.Header.Version)
		if err != nil {
			return err
		}
		if compression == 0 && encodedBound > capacityLimit {
			return editError(psdfile.ErrLimit, "save.working", "Raw layer capacity exceeds remaining working budget")
		}
		remaining = min(remaining, encodedBound)
		data, err := psdfile.EncodePlaneWithOptions(ctx, plane, compression, e.source.Header.Version,
			psdfile.EncoderOptions{Limits: e.source.Limits(), MaxEncodedBytes: remaining})
		if err != nil {
			return err
		}
		*allocated += int64(cap(data))
		channels = append(channels, writerChannel{id: plane.ID, part: writePart{data: data}, size: int64(len(data))})
		return nil
	}
	for _, channel := range original.Channels {
		seen[channel.ID] = true
		if plane, exists := replacements[channel.ID]; exists {
			if err := appendChanged(plane); err != nil {
				return nil, err
			}
			continue
		}
		channels = append(channels, writerChannel{
			id: channel.ID, part: writePart{span: channel.Span}, size: channel.Span.Length,
		})
	}
	ids := make([]int16, 0, len(replacements))
	for id := range replacements {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := appendChanged(replacements[id]); err != nil {
			return nil, err
		}
	}
	return channels, nil
}

func writerImagePlanes(ctx context.Context, pixels *image.NRGBA) ([]psdfile.Plane, error) {
	width, height := pixels.Bounds().Dx(), pixels.Bounds().Dy()
	planes := make([]psdfile.Plane, 4)
	for index := range planes {
		id := int16(index)
		if index == 3 {
			id = -1
		}
		planes[index] = psdfile.Plane{ID: id, Width: width, Height: height, Depth: 8, RowBytes: width,
			Data: make([]byte, width*height)}
	}
	for row := range height {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for column := range width {
			offset := row*pixels.Stride + column*4
			for index := range planes {
				planes[index].Data[row*width+column] = pixels.Pix[offset+index]
			}
		}
	}
	return planes, nil
}

func (e *Editor) writeExtraLocked(limit int64, layer writeLayer, original psdfile.LayerRecord) (writePlan, error) {
	plan := writePlan{limit: limit}
	node := layer.node
	divider := layer.divider
	if layer.sourceIndex >= 0 && !node.group && !layer.end {
		divider = original.Divider
	}
	linkedChanged := false
	for _, block := range original.AdditionalInfo {
		linkedChanged = linkedChanged || e.linkedBlockChangedLocked(block)
	}
	if layer.sourceIndex >= 0 && (layer.end || !node.nameChanged) && original.Divider == divider && !linkedChanged &&
		(layer.end || len(node.additional) == 0) && (layer.end || !node.fillOpacityChanged) {
		plan.original(original.Extra)
		return plan, plan.err
	}
	name := node.name
	id := node.psdID
	if layer.end {
		name, id = "</Layer group>", node.endPSDID
	}
	if layer.sourceIndex < 0 {
		plan.bytes(make([]byte, 8))
		plan.bytes(writerPascal(name))
		plan.bytes(writerAdditional("8BIM", "luni", 4, writerUnicode(name)))
		plan.bytes(writerAdditional("8BIM", "lyid", 4, binary.BigEndian.AppendUint32(nil, id)))
		if divider != 0 {
			plan.bytes(writerAdditional("8BIM", "lsct", 4, binary.BigEndian.AppendUint32(nil, divider)))
		}
		if node.fillOpacityChanged && !layer.end {
			plan.bytes(writerAdditional("8BIM", "iOpa", 4, []byte{node.fillOpacity, 0, 0, 0}))
		}
		return plan, plan.err
	}
	plan.original(original.MaskData)
	plan.original(original.BlendRanges)
	if node.nameChanged && !layer.end {
		plan.bytes(writerPascal(name))
	} else {
		plan.original(original.NameData)
	}
	position := original.NameData.Offset + original.NameData.Length
	if node.fillOpacityChanged && !layer.end && node.fillOpacityAdded {
		// Keep the original final opaque block at the end; some sources use uncounted trailing alignment.
		plan.bytes(writerAdditional("8BIM", "iOpa", 4, []byte{node.fillOpacity, 0, 0, 0}))
	}
	unicodeSeen, fillSeen := false, false
	for _, block := range original.AdditionalInfo {
		fillSeen = fillSeen || block.Key == "iOpa"
		if block.Span.Offset > position {
			plan.original(psdfile.Span{Offset: position, Length: block.Span.Offset - position})
		}
		if data, changed := node.additional[block.Span.Offset]; changed && !layer.end {
			plan.bytes([]byte(block.Signature + block.Key))
			plan.length(block.LengthWidth, int64(len(data)), "save.layer.additional")
			plan.bytes(data)
			writerAppendPadding(&plan, block.Padding, writerReplacementPadding(int64(len(data)), false))
		} else if node.nameChanged && !layer.end && block.Key == "luni" {
			unicodeSeen = true
			data := writerUnicode(name)
			oldCount, err := e.source.ReadSpan(psdfile.Span{Offset: block.Data.Offset, Length: 4})
			if err != nil {
				return plan, err
			}
			oldStringLength := 4 + int64(binary.BigEndian.Uint32(oldCount))*2
			remaining := block.Data.Length - oldStringLength
			plan.bytes([]byte(block.Signature + block.Key))
			plan.length(block.LengthWidth, int64(len(data))+remaining, "save.layer.luni")
			plan.bytes(data)
			plan.original(psdfile.Span{Offset: block.Data.Offset + oldStringLength, Length: remaining})
			padding := writerReplacementPadding(int64(len(data))+remaining, false)
			writerAppendPadding(&plan, block.Padding, padding)
		} else if (block.Key == "lsct" || block.Key == "lsdk") && original.Divider != divider {
			plan.original(psdfile.Span{Offset: block.Span.Offset, Length: block.Data.Offset - block.Span.Offset})
			plan.bytes(binary.BigEndian.AppendUint32(nil, divider))
			plan.original(psdfile.Span{Offset: block.Data.Offset + 4, Length: block.Data.Length - 4})
			plan.original(block.Padding)
		} else {
			linked, err := e.writeLinkedBlockLocked(block, false, limit)
			if err != nil {
				return plan, err
			}
			plan.append(linked)
		}
		position = block.Span.Offset + block.Span.Length
	}
	if node.nameChanged && !layer.end && !unicodeSeen {
		plan.bytes(writerAdditional("8BIM", "luni", 4, writerUnicode(name)))
	}
	if node.fillOpacityChanged && !layer.end && !fillSeen && !node.fillOpacityAdded {
		plan.bytes(writerAdditional("8BIM", "iOpa", 4, []byte{node.fillOpacity, 0, 0, 0}))
	}
	end := original.Extra.Offset + original.Extra.Length
	if end > position {
		plan.original(psdfile.Span{Offset: position, Length: end - position})
	}
	return plan, plan.err
}

func writerUnicode(value string) []byte {
	units := utf16.Encode([]rune(value))
	data := binary.BigEndian.AppendUint32(nil, uint32(len(units)))
	for _, unit := range units {
		data = binary.BigEndian.AppendUint16(data, unit)
	}
	return data
}

func writerPascal(value string) []byte {
	data := []byte{0}
	for _, character := range value {
		if len(data) == 256 {
			break
		}
		if character > 0x7f {
			character = '?'
		}
		data = append(data, byte(character))
	}
	data[0] = byte(len(data) - 1)
	for len(data)%4 != 0 {
		data = append(data, 0)
	}
	return data
}

func writerAdditional(signature, key string, width uint8, data []byte) []byte {
	result := []byte(signature + key)
	if width == 8 {
		result = binary.BigEndian.AppendUint64(result, uint64(len(data)))
	} else {
		result = binary.BigEndian.AppendUint32(result, uint32(len(data)))
	}
	result = append(result, data...)
	if len(data)%2 != 0 {
		result = append(result, 0)
	}
	return result
}
