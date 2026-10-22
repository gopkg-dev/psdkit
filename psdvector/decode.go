package psdvector

import (
	"context"
	"encoding/binary"
	"fmt"
)

// DecodeMask decodes a complete vector-mask payload without retaining or changing the supplied bytes.
// Paths use signed 8.24 normalized coordinates, with vertical components preceding horizontal components.
// Supported version-three data may contain zero alignment bytes after its final complete path record.
func DecodeMask(
	ctx context.Context, data []byte, width, height int, options ...DecodeOptions,
) (*Mask, error) {
	return decodeVector(ctx, data, width, height, 8, options)
}

// DecodePath decodes headerless 26-byte records used by saved and working path image resources.
// The returned geometry has Version zero and no vector-mask header or flags.
func DecodePath(
	ctx context.Context, data []byte, width, height int, options ...DecodeOptions,
) (*Mask, error) {
	return decodeVector(ctx, data, width, height, 0, options)
}

func decodeVector(
	ctx context.Context, data []byte, width, height, prefix int, options []DecodeOptions,
) (*Mask, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	configuration, err := decodeConfiguration(options)
	if err != nil {
		return nil, err
	}
	if width <= 0 || height <= 0 || width > 300_000 || height > 300_000 || len(data) < prefix ||
		configuration.SourceOffset > int64(^uint64(0)>>1)-int64(len(data)) {
		return nil, vectorError(ErrMalformed, "vectorMask", configuration.SourceOffset, "invalid dimensions or header")
	}
	count, remainder := (len(data)-prefix)/26, (len(data)-prefix)%26
	if remainder > 3 {
		return nil, vectorError(ErrMalformed, "vectorMask.records", configuration.SourceOffset+int64(prefix),
			"path payload ends with an incomplete record")
	}
	if count > configuration.MaxRecords || int64(len(data)) > configuration.MaxBytes ||
		configuration.MaxBytes-int64(len(data)) < 512 ||
		int64(count) > (configuration.MaxBytes-int64(len(data))-512)/256 {
		return nil, vectorError(ErrLimit, "vectorMask.records", configuration.SourceOffset+int64(prefix),
			"path records exceed metadata budgets")
	}
	var version, flags uint32
	if prefix != 0 {
		version, flags = binary.BigEndian.Uint32(data), binary.BigEndian.Uint32(data[4:])
		if version != 3 {
			return nil, vectorError(ErrUnsupported, "vectorMask.version", configuration.SourceOffset,
				"vector-mask version is not three")
		}
	}
	mask := &Mask{
		Width: width, Height: height, Version: version, Flags: flags,
		Invert: flags&1 != 0, NotLinked: flags&2 != 0, Disabled: flags&4 != 0,
		Records: make([]Record, 0, count), FillRule: EvenOdd,
		Span:       Span{Offset: configuration.SourceOffset, Length: int64(len(data))},
		HeaderSpan: Span{Offset: configuration.SourceOffset, Length: int64(prefix)},
	}
	copy(mask.Header[:], data[:prefix])
	if flags&^uint32(7) != 0 {
		mask.Unsupported = append(mask.Unsupported, "unknown vector-mask flags")
	}
	pending, active := 0, -1
	for index := range count {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		offset := prefix + index*26
		record := Record{
			Selector: binary.BigEndian.Uint16(data[offset:]),
			Span:     Span{Offset: configuration.SourceOffset + int64(offset), Length: 26},
		}
		copy(record.Raw[:], data[offset:offset+26])
		mask.Records = append(mask.Records, record)
		path := fmt.Sprintf("vectorMask.records[%d]", index)
		selector := record.Selector
		if pending != 0 && selector != 1 && selector != 2 && selector != 4 && selector != 5 {
			return nil, vectorError(ErrMalformed, path, record.Span.Offset,
				"subpath knot sequence ended before its declared count")
		}
		switch selector {
		case 0, 3:
			pending = int(binary.BigEndian.Uint16(record.Raw[2:4]))
			if pending > count-index-1 {
				return nil, vectorError(ErrMalformed, path, record.Span.Offset, "subpath knot count exceeds payload")
			}
			subpath := Subpath{
				Closed: selector == 0, Operation: int16(binary.BigEndian.Uint16(record.Raw[4:6])),
				Unknown1: binary.BigEndian.Uint16(record.Raw[6:8]),
				Unknown2: binary.BigEndian.Uint32(record.Raw[8:12]),
				Index:    binary.BigEndian.Uint32(record.Raw[12:16]), RecordIndex: index,
				Span:  Span{Offset: record.Span.Offset, Length: int64(pending+1) * 26},
				Knots: make([]Knot, 0, pending),
			}
			copy(subpath.Metadata[:], record.Raw[4:])
			copy(subpath.Reserved[:], record.Raw[16:])
			mask.Subpaths = append(mask.Subpaths, subpath)
			active = len(mask.Subpaths) - 1
		case 1, 2, 4, 5:
			if pending == 0 || active < 0 || mask.Subpaths[active].Closed != (selector == 1 || selector == 2) {
				return nil, vectorError(ErrMalformed, path, record.Span.Offset, "orphan or mismatched subpath knot")
			}
			knot := Knot{
				Preceding: decodePoint(record.Raw[2:10], width, height),
				Anchor:    decodePoint(record.Raw[10:18], width, height),
				Leaving:   decodePoint(record.Raw[18:26], width, height),
				Linked:    selector == 1 || selector == 4, RecordIndex: index,
			}
			mask.Subpaths[active].Knots = append(mask.Subpaths[active].Knots, knot)
			pending--
		case 6:
			if mask.HasFillRule || !zeroBytes(record.Raw[2:]) {
				mask.Unsupported = append(mask.Unsupported, "nonstandard or repeated path fill-rule record")
			}
			mask.HasFillRule = true
		case 7:
			clipboard := Clipboard{
				Top:        int32(binary.BigEndian.Uint32(record.Raw[2:6])),
				Left:       int32(binary.BigEndian.Uint32(record.Raw[6:10])),
				Bottom:     int32(binary.BigEndian.Uint32(record.Raw[10:14])),
				Right:      int32(binary.BigEndian.Uint32(record.Raw[14:18])),
				Resolution: int32(binary.BigEndian.Uint32(record.Raw[18:22])), RecordIndex: index, Span: record.Span,
			}
			mask.Clipboards = append(mask.Clipboards, clipboard)
			if !zeroBytes(record.Raw[22:]) {
				mask.Unsupported = append(mask.Unsupported, "nonstandard clipboard record")
			}
		case 8:
			value := binary.BigEndian.Uint16(record.Raw[2:4])
			if value > 1 {
				return nil, vectorError(ErrMalformed, path, record.Span.Offset, "initial fill is not boolean")
			}
			if mask.HasInitialFill || !zeroBytes(record.Raw[4:]) {
				mask.Unsupported = append(mask.Unsupported, "nonstandard or repeated initial-fill record")
			}
			mask.InitialFill, mask.HasInitialFill = value == 1, true
		default:
			mask.Unsupported = append(mask.Unsupported, "unknown path-record selector")
		}
	}
	if pending != 0 {
		return nil, vectorError(ErrMalformed, "vectorMask.records", mask.Span.Offset+mask.Span.Length,
			"subpath has fewer knots than its declared count")
	}
	if remainder != 0 {
		start := prefix + count*26
		mask.Padding = append([]byte(nil), data[start:]...)
		mask.PaddingSpan = Span{Offset: configuration.SourceOffset + int64(start), Length: int64(remainder)}
		if !zeroBytes(mask.Padding) {
			mask.Unsupported = append(mask.Unsupported, "nonzero path alignment data")
		}
	}
	return mask, nil
}

func decodeConfiguration(options []DecodeOptions) (DecodeOptions, error) {
	configuration := DecodeOptions{MaxBytes: 32 << 20, MaxRecords: 100_000}
	if len(options) > 1 {
		return configuration, vectorError(ErrMalformed, "options", 0, "at most one decode option value is accepted")
	}
	if len(options) == 0 {
		return configuration, nil
	}
	provided := options[0]
	if provided.SourceOffset < 0 || provided.MaxBytes < 0 || provided.MaxRecords < 0 {
		return configuration, vectorError(ErrMalformed, "options", 0, "offset and budgets must not be negative")
	}
	configuration.SourceOffset = provided.SourceOffset
	if provided.MaxBytes != 0 {
		configuration.MaxBytes = provided.MaxBytes
	}
	if provided.MaxRecords != 0 {
		configuration.MaxRecords = provided.MaxRecords
	}
	return configuration, nil
}

func decodePoint(data []byte, width, height int) Point {
	vertical, horizontal := int32(binary.BigEndian.Uint32(data)), int32(binary.BigEndian.Uint32(data[4:]))
	return Point{
		X: float64(horizontal) / (1 << 24) * float64(width),
		Y: float64(vertical) / (1 << 24) * float64(height), RawX: horizontal, RawY: vertical,
	}
}

func zeroBytes(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}
