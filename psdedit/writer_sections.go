package psdedit

import (
	"context"
	"encoding/binary"
	"math"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func (e *Editor) prepareWriteLocked(ctx context.Context, option SaveOptions) (writePlan, error) {
	plan := writePlan{limit: option.MaxOutputBytes}
	for index, set := range e.source.LayerSets {
		if e.revision != 0 && index != e.source.ActiveLayerSet && len(set.Layers) != 0 {
			return plan, editError(ErrUnsupported, "save.layerSets", "multiple populated layer sets cannot be synchronized")
		}
	}
	fixed, reservation, err := e.writeWorkingBudgetLocked(ctx, option)
	if err != nil {
		return plan, err
	}
	option.reservedForRender = reservation
	option.writeFixedBytes = fixed
	composite, err := e.prepareCompositeLocked(ctx, option)
	if err != nil {
		return plan, err
	}
	resources, err := e.writeResourcesLocked(option.MaxOutputBytes, composite.data != nil)
	if err != nil {
		return plan, err
	}
	layers, err := e.prepareLayersLocked(ctx, option, composite.globalAlpha, int64(cap(composite.data)))
	if err != nil {
		return plan, err
	}
	layerMask, err := e.writeLayerMaskLocked(option.MaxOutputBytes, layers)
	if err != nil {
		return plan, err
	}
	if composite.header == e.source.Header {
		plan.original(e.source.HeaderData)
	} else {
		data, err := e.source.ReadSpan(e.source.HeaderData)
		if err != nil {
			return plan, err
		}
		binary.BigEndian.PutUint16(data[12:14], composite.header.Channels)
		plan.bytes(data)
	}
	plan.original(e.source.ColorModeData)
	plan.length(4, resources.size, "save.resources")
	plan.append(resources)
	plan.length(versionLengthWidth(e.source.Header.Version), layerMask.size, "save.layerMask")
	plan.append(layerMask)
	if composite.data == nil {
		plan.original(e.source.Composite.Span)
	} else {
		plan.bytes(composite.data)
	}
	if plan.err != nil {
		return plan, plan.err
	}
	if err := ctx.Err(); err != nil {
		return plan, err
	}
	return plan, nil
}

func (e *Editor) writeResourcesLocked(limit int64, mergedChanged bool) (writePlan, error) {
	plan := writePlan{limit: limit}
	seedSeen, versionSeen := false, false
	seed := e.nextPSDID
	if e.structureChanged {
		for _, resource := range e.source.Resources {
			if resource.ID != 1044 {
				continue
			}
			if resource.Data.Length < 4 {
				return plan, editError(ErrUnsupported, "save.resources.1044", "layer ID seed is incomplete")
			}
			data, err := e.source.ReadSpan(psdfile.Span{Offset: resource.Data.Offset, Length: 4})
			if err != nil {
				return plan, err
			}
			seed = max(seed, binary.BigEndian.Uint32(data))
		}
		for _, layer := range e.writeLayersLocked() {
			id := layer.node.psdID
			if layer.end {
				id = layer.node.endPSDID
			}
			if id == math.MaxUint32 {
				return plan, editError(ErrUnsupported, "save.resources.1044", "layer ID seed has no remaining values")
			}
			seed = max(seed, id+1)
		}
	}
	for _, resource := range e.source.Resources {
		if e.structureChanged && writerLayerUIResource(resource.ID) {
			continue
		}
		if mergedChanged && (resource.ID == 1033 || resource.ID == 1036) {
			continue
		}
		switch {
		case resource.ID == 1044 && e.structureChanged:
			seedSeen = true
			plan.original(psdfile.Span{
				Offset: resource.Span.Offset, Length: resource.Data.Offset - resource.Span.Offset,
			})
			plan.bytes(binary.BigEndian.AppendUint32(nil, seed))
			plan.original(psdfile.Span{Offset: resource.Data.Offset + 4, Length: resource.Data.Length - 4})
			plan.original(resource.Padding)
		case resource.ID == 1057 && mergedChanged:
			if err := e.validateVersionInfoLocked(resource.Data); err != nil {
				return plan, err
			}
			versionSeen = true
			plan.original(psdfile.Span{
				Offset: resource.Span.Offset, Length: resource.Data.Offset + 4 - resource.Span.Offset,
			})
			plan.bytes([]byte{1})
			plan.original(psdfile.Span{Offset: resource.Data.Offset + 5, Length: resource.Data.Length - 5})
			plan.original(resource.Padding)
		default:
			plan.original(resource.Span)
		}
	}
	if e.structureChanged && !seedSeen {
		plan.bytes(writerResource(1044, binary.BigEndian.AppendUint32(nil, seed)))
	}
	if mergedChanged && !versionSeen {
		data := binary.BigEndian.AppendUint32(nil, 1)
		data = append(data, 1)
		data = append(data, writerUnicode("github.com/gopkg-dev/psdkit")...)
		data = append(data, writerUnicode("Adobe Photoshop")...)
		data = binary.BigEndian.AppendUint32(data, 1)
		plan.bytes(writerResource(1057, data))
	}
	return plan, plan.err
}

func (e *Editor) validateVersionInfoLocked(span psdfile.Span) error {
	if span.Length < 17 {
		return editError(ErrUnsupported, "save.resources.1057", "version information is incomplete")
	}
	version, err := e.source.ReadSpan(psdfile.Span{Offset: span.Offset, Length: 4})
	if err != nil {
		return err
	}
	if binary.BigEndian.Uint32(version) != 1 {
		return editError(ErrUnsupported, "save.resources.1057", "unknown version information schema")
	}
	position, end := span.Offset+5, span.Offset+span.Length
	for range 2 {
		if end-position < 4 {
			return editError(ErrUnsupported, "save.resources.1057", "version information string is incomplete")
		}
		data, err := e.source.ReadSpan(psdfile.Span{Offset: position, Length: 4})
		if err != nil {
			return err
		}
		position += 4
		length := int64(binary.BigEndian.Uint32(data)) * 2
		if length > end-position {
			return editError(ErrUnsupported, "save.resources.1057", "version information string exceeds its block")
		}
		position += length
	}
	if end-position < 4 {
		return editError(ErrUnsupported, "save.resources.1057", "version information file version is incomplete")
	}
	return nil
}

func writerLayerUIResource(id uint16) bool {
	switch id {
	case 1024, 1026, 1069, 1072:
		return true
	default:
		return false
	}
}

func writerResource(id uint16, data []byte) []byte {
	result := append([]byte("8BIM"), byte(id>>8), byte(id), 0, 0)
	result = binary.BigEndian.AppendUint32(result, uint32(len(data)))
	result = append(result, data...)
	if len(data)%2 != 0 {
		result = append(result, 0)
	}
	return result
}

func (e *Editor) writeLayerMaskLocked(limit int64, layers writePlan) (writePlan, error) {
	plan := writePlan{limit: limit}
	width := versionLengthWidth(e.source.Header.Version)
	selectedKey := "main"
	selectedOffset := int64(-1)
	if e.source.ActiveLayerSet >= 0 {
		set := e.source.LayerSets[e.source.ActiveLayerSet]
		selectedKey, selectedOffset = set.Key, set.Span.Offset
	}
	if selectedKey == "main" {
		plan.length(width, layers.size, "save.layerInfo")
		plan.append(layers)
	} else if e.source.LayerInfo.Length != 0 {
		plan.original(e.source.LayerInfo)
	} else {
		plan.length(width, 0, "save.layerInfo")
	}
	if e.source.GlobalMask.Length != 0 {
		plan.original(e.source.GlobalMask)
	} else {
		plan.bytes([]byte{0, 0, 0, 0})
	}
	position := e.source.LayerMaskSection.Offset + int64(width)
	if e.source.LayerInfo.Length != 0 {
		position = e.source.LayerInfo.Offset + e.source.LayerInfo.Length
	}
	if e.source.GlobalMask.Length != 0 {
		position = e.source.GlobalMask.Offset + e.source.GlobalMask.Length
	}
	for _, block := range e.source.AdditionalInfo {
		if block.Span.Offset > position {
			plan.original(psdfile.Span{Offset: position, Length: block.Span.Offset - position})
		}
		if block.Data.Offset == selectedOffset && block.Key == selectedKey {
			plan.bytes([]byte(block.Signature + block.Key))
			plan.length(block.LengthWidth, layers.size, "save.additional."+block.Key)
			plan.append(layers)
			padding := writerReplacementPadding(layers.size, true)
			writerAppendPadding(&plan, block.Padding, padding)
		} else if data, changed := e.globalAdditional[block.Span.Offset]; changed {
			plan.bytes([]byte(block.Signature + block.Key))
			plan.length(block.LengthWidth, int64(len(data)), "save.additional."+block.Key)
			plan.bytes(data)
			writerAppendPadding(&plan, block.Padding, writerReplacementPadding(int64(len(data)), true))
		} else {
			linked, err := e.writeLinkedBlockLocked(block, true, limit)
			if err != nil {
				return plan, err
			}
			plan.append(linked)
		}
		position = block.Span.Offset + block.Span.Length
	}
	end := e.source.LayerMaskSection.Offset + e.source.LayerMaskSection.Length
	if end > position {
		plan.original(psdfile.Span{Offset: position, Length: end - position})
	}
	return plan, plan.err
}

func writerReplacementPadding(length int64, global bool) int64 {
	alignment := int64(2)
	// Document-level tagged blocks need four-byte alignment for Photoshop-compatible readers.
	if global {
		alignment = 4
	}
	return (alignment - length%alignment) % alignment
}

func writerAppendPadding(plan *writePlan, original psdfile.Span, length int64) {
	retained := min(length, original.Length)
	plan.original(psdfile.Span{Offset: original.Offset, Length: retained})
	if length > retained {
		plan.bytes(make([]byte, int(length-retained)))
	}
}
