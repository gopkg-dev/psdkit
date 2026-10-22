package psdfile

import (
	"context"
	"fmt"
	"math"
)

// ArtboardRect retains document-coordinate bounds without truncating fractional or negative values.
type ArtboardRect struct{ Left, Top, Right, Bottom float64 }

// ArtboardData is an owned metadata view, not a rendering capability declaration.
// BackgroundType remains the source integer because API/scripting representations may use different enumerations.
// Unknown entries and duplicate keys retain their full ordered descriptor and original source spans.
type ArtboardData struct {
	Block          AdditionalBlock
	Descriptor     *Descriptor
	OrderedEntries []DescriptorEntry
	Rect           *ArtboardRect
	BackgroundType *int32
	BackgroundRGB  *[3]float64
	PresetName     *string
	GuideIDs       []int32
	UnknownEntries []DescriptorEntry
	Tail           Span
	MetadataBytes  int64
	Unsupported    []string
}

// ArtboardRecord associates one retained artb/artd/abdd block with its physical source layer.
// LayerIndex is -1 for document-level blocks; multiple blocks retain source order without choosing precedence.
type ArtboardRecord struct {
	LayerIndex int
	LayerID    uint32
	LayerName  string
	Data       *ArtboardData
}

// DecodeArtboard reads artb/artd/abdd versioned descriptors into owned structural metadata.
// It does not composite a background, crop children, infer document coordinates or change source blocks.
func (d *Document) DecodeArtboard(
	ctx context.Context, block AdditionalBlock, options ...MetadataOptions,
) (*ArtboardData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := d.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	if !artboardKey(block.Key) {
		return nil, fileError(ErrUnsupported, "artboard", "artboard.key", block.Data.Offset,
			"additional block does not contain artboard metadata")
	}
	limits, err := d.metadataLimits(options)
	if err != nil {
		return nil, err
	}
	limits.MaxMetadataBytes = min(limits.MaxMetadataBytes, limits.MaxDecodeBytes)
	descriptor, allocated, _, err := d.decodeDescriptorUsage(ctx, block.Data, true, limits)
	if err != nil {
		return nil, err
	}
	viewBytes := int64(512) + int64(len(descriptor.Entries))*64
	if viewBytes > limits.MaxMetadataBytes-allocated {
		return nil, fileError(ErrLimit, "artboard", "artboard.metadata", block.Data.Offset,
			"owned artboard view exceeds metadata budget")
	}
	result := &ArtboardData{Block: block, Descriptor: descriptor, OrderedEntries: descriptor.Entries,
		MetadataBytes: allocated + viewBytes,
		Tail: Span{Offset: descriptor.Span.Offset + descriptor.Span.Length,
			Length: block.Data.Length - descriptor.Span.Length}}
	seen := make(map[string]bool)
	for _, entry := range descriptor.Entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if seen[entry.Key] {
			cost := int64(len(entry.Key)) + 128
			if cost > limits.MaxMetadataBytes-result.MetadataBytes {
				return nil, fileError(ErrLimit, "artboard", "artboard.duplicates", entry.Data.Offset,
					"duplicate-parameter diagnostics exceed metadata budget")
			}
			result.MetadataBytes += cost
			result.Unsupported = append(result.Unsupported, "duplicate artboard parameter "+entry.Key)
		}
		seen[entry.Key] = true
		switch entry.Key {
		case "artboardRect":
			object, valid := entry.Value.(*Descriptor)
			if !valid {
				return nil, artboardMalformed(entry.Data.Offset, "artboardRect", "rectangle is not a descriptor")
			}
			rectangle, err := artboardRectangle(object)
			if err != nil {
				return nil, err
			}
			if result.Rect == nil {
				result.Rect = rectangle
			}
		case "artboardBackgroundType":
			value, valid := artboardInteger(entry.Value)
			if !valid {
				return nil, artboardMalformed(entry.Data.Offset, entry.Key, "background type is not an integer")
			}
			if result.BackgroundType == nil {
				result.BackgroundType = &value
			}
		case "Clr ":
			color, valid := entry.Value.(*Descriptor)
			if !valid || color.ClassID != "RGBC" {
				result.Unsupported = append(result.Unsupported, "artboard background color is not RGB")
				continue
			}
			rgb, err := artboardRGB(color)
			if err != nil {
				return nil, err
			}
			if result.BackgroundRGB == nil {
				result.BackgroundRGB = &rgb
			}
		case "artboardPresetName":
			value, valid := entry.Value.(string)
			if !valid {
				return nil, artboardMalformed(entry.Data.Offset, entry.Key, "preset name is not a string")
			}
			if result.PresetName == nil {
				result.PresetName = &value
			}
		case "guideIDs":
			list, valid := entry.Value.([]DescriptorValue)
			if !valid {
				return nil, artboardMalformed(entry.Data.Offset, entry.Key, "guide IDs are not a list")
			}
			cost := int64(len(list)) * 8
			if cost > limits.MaxMetadataBytes-result.MetadataBytes {
				return nil, fileError(ErrLimit, "artboard", "artboard.guideIDs", entry.Data.Offset,
					"owned guide IDs exceed metadata budget")
			}
			result.MetadataBytes += cost
			ids := make([]int32, len(list))
			for index, item := range list {
				id, valid := artboardInteger(item.Value)
				if !valid {
					return nil, artboardMalformed(item.Data.Offset, entry.Key, "guide ID is not an integer")
				}
				ids[index] = id
			}
			if result.GuideIDs == nil {
				result.GuideIDs = ids
			}
		default:
			if 256 > limits.MaxMetadataBytes-result.MetadataBytes {
				return nil, fileError(ErrLimit, "artboard", "artboard.unknown", entry.Data.Offset,
					"unknown ordered entry view exceeds metadata budget")
			}
			result.MetadataBytes += 256
			result.UnknownEntries = append(result.UnknownEntries, entry)
		}
	}
	if result.Tail.Length != 0 {
		if result.Tail.Length > 3 || result.Tail.Length != (4-descriptor.Span.Length%4)%4 {
			result.Unsupported = append(result.Unsupported, "unrecognized bytes after artboard descriptor")
		} else {
			var padding [3]byte
			if err := d.readEffectsField(ctx, result.Tail, padding[:result.Tail.Length]); err != nil {
				return nil, err
			}
			for _, value := range padding[:result.Tail.Length] {
				if value != 0 {
					result.Unsupported = append(result.Unsupported, "nonzero artboard alignment bytes")
					break
				}
			}
		}
	}
	return result, nil
}

// Artboards reads ordered layer and document-level artboard metadata with an aggregate owned-view budget.
// A descriptor's presence does not promise that a renderer understands artboard clipping or backgrounds.
func (d *Document) Artboards(ctx context.Context, options ...MetadataOptions) ([]ArtboardRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := d.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	limits, err := d.metadataLimits(options)
	if err != nil {
		return nil, err
	}
	remaining := min(limits.MaxMetadataBytes, limits.MaxDecodeBytes)
	var result []ArtboardRecord
	read := func(index int, layerID uint32, name string, blocks []AdditionalBlock) error {
		for _, block := range blocks {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !artboardKey(block.Key) {
				continue
			}
			if remaining <= 256 || len(result) >= limits.MaxBlocks {
				return fileError(ErrLimit, "artboard", "artboards", block.Data.Offset,
					"artboard records exceed the aggregate metadata budget")
			}
			view, err := d.DecodeArtboard(ctx, block, MetadataOptions{MaxBytes: remaining - 256})
			if err != nil {
				return err
			}
			remaining -= view.MetadataBytes + 256
			result = append(result, ArtboardRecord{LayerIndex: index, LayerID: layerID, LayerName: name, Data: view})
		}
		return nil
	}
	for index, layer := range d.Layers {
		if err := read(index, layer.ID, layer.Name, layer.AdditionalInfo); err != nil {
			return nil, err
		}
	}
	if err := read(-1, 0, "", d.AdditionalInfo); err != nil {
		return nil, err
	}
	return result, nil
}

func artboardKey(key string) bool { return key == "artb" || key == "artd" || key == "abdd" }

func artboardNumber(value any) (float64, bool) {
	var number float64
	switch typed := value.(type) {
	case int32:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case float64:
		number = typed
	case DescriptorUnit:
		if typed.Unit != "#Pxl" {
			return 0, false
		}
		switch scalar := typed.Number.(type) {
		case float64:
			number = scalar
		case float32:
			number = float64(scalar)
		default:
			return 0, false
		}
	default:
		return 0, false
	}
	return number, !math.IsNaN(number) && !math.IsInf(number, 0)
}

func artboardInteger(value any) (int32, bool) {
	number, valid := artboardNumber(value)
	if !valid || math.Trunc(number) != number || number < math.MinInt32 || number > math.MaxInt32 {
		return 0, false
	}
	return int32(number), true
}

func artboardRectangle(descriptor *Descriptor) (*ArtboardRect, error) {
	if descriptor == nil {
		return nil, artboardMalformed(0, "artboardRect", "rectangle descriptor is nil")
	}
	coordinates := [4]float64{}
	seen := [4]bool{}
	for _, entry := range descriptor.Entries {
		for index, key := range []string{"Left", "Top ", "Rght", "Btom"} {
			if entry.Key != key {
				continue
			}
			value, valid := artboardNumber(entry.Value)
			if !valid || seen[index] {
				return nil, artboardMalformed(entry.Data.Offset, "artboardRect", "coordinate is nonfinite or duplicated")
			}
			seen[index], coordinates[index] = true, value
		}
	}
	if !seen[0] || !seen[1] || !seen[2] || !seen[3] || coordinates[2] < coordinates[0] ||
		coordinates[3] < coordinates[1] || math.IsInf(coordinates[2]-coordinates[0], 0) ||
		math.IsInf(coordinates[3]-coordinates[1], 0) {
		return nil, artboardMalformed(descriptor.Span.Offset, "artboardRect", "rectangle is incomplete or has invalid edges")
	}
	return &ArtboardRect{Left: coordinates[0], Top: coordinates[1], Right: coordinates[2], Bottom: coordinates[3]}, nil
}

func artboardRGB(descriptor *Descriptor) ([3]float64, error) {
	result, seen := [3]float64{}, [3]bool{}
	for _, entry := range descriptor.Entries {
		for index, key := range []string{"Rd  ", "Grn ", "Bl  "} {
			if entry.Key != key {
				continue
			}
			value, valid := artboardNumber(entry.Value)
			if !valid || value < 0 || value > 255 || seen[index] {
				return result, artboardMalformed(entry.Data.Offset, "artboard.color", "RGB component is invalid or duplicated")
			}
			result[index], seen[index] = value/255, true
		}
	}
	if !seen[0] || !seen[1] || !seen[2] {
		return result, artboardMalformed(descriptor.Span.Offset, "artboard.color", "RGB color is incomplete")
	}
	return result, nil
}

func artboardMalformed(offset int64, field, detail string) error {
	return fileError(ErrMalformed, "artboard", fmt.Sprintf("artboard.%s", field), offset, detail)
}
