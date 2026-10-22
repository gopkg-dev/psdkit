package psdvector

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
)

type vectorEncoder struct {
	ctx     context.Context
	mask    *Mask
	options EncodeOptions
	data    []byte
	records int
}

// EncodeMask writes an owned version-three vmsk/vsms payload from typed metadata and geometry.
// Unchanged records, unknown selectors and alignment bytes retain their original encoding.
// X/Y and RawX/RawY edits are resolved against retained source knots; conflicting edits are rejected.
// New subpaths and knots use RecordIndex -1; source record, header and span fields are provenance.
func EncodeMask(ctx context.Context, mask *Mask, options ...EncodeOptions) ([]byte, error) {
	return encodeVector(ctx, mask, true, options)
}

// EncodePath writes owned headerless saved/working path records with the same edit-preservation rules.
// A path resource cannot encode vector-mask flags; export geometry with those fields cleared.
func EncodePath(ctx context.Context, mask *Mask, options ...EncodeOptions) ([]byte, error) {
	return encodeVector(ctx, mask, false, options)
}

func encodeVector(ctx context.Context, mask *Mask, withHeader bool, options []EncodeOptions) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	configuration, err := encodeConfiguration(options)
	if err != nil {
		return nil, err
	}
	if mask == nil || mask.Width <= 0 || mask.Height <= 0 || mask.Width > 300_000 || mask.Height > 300_000 {
		return nil, vectorError(ErrMalformed, "vectorMask", 0, "mask dimensions are invalid")
	}
	if mask.FillRule != EvenOdd {
		return nil, vectorError(ErrUnsupported, "vectorMask.fillRule", mask.Span.Offset,
			"the path record format cannot encode a nonzero fill-rule override")
	}
	if len(mask.Subpaths) > configuration.MaxRecords || len(mask.Clipboards) > configuration.MaxRecords {
		return nil, vectorError(ErrLimit, "vectorMask.records", mask.Span.Offset,
			"typed path or clipboard counts exceed output record budgets")
	}
	remaining := configuration.MaxRecords
	for _, path := range mask.Subpaths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(path.Knots) >= remaining {
			return nil, vectorError(ErrLimit, "vectorMask.subpaths", path.Span.Offset,
				"typed knot counts exceed output record budgets")
		}
		remaining -= len(path.Knots) + 1
	}
	flags := resolvedFlags(mask)
	if !withHeader && flags != 0 {
		return nil, vectorError(ErrUnsupported, "vectorPath.flags", mask.Span.Offset,
			"headerless path resources have no vector-mask flags")
	}
	if withHeader && mask.Version != 0 && mask.Version != 3 {
		return nil, vectorError(ErrUnsupported, "vectorMask.version", mask.Span.Offset,
			"only a version-three vector-mask payload can be encoded")
	}
	encoder := vectorEncoder{ctx: ctx, mask: mask, options: configuration}
	if withHeader {
		if err := encoder.reserve(8); err != nil {
			return nil, err
		}
		encoder.data = binary.BigEndian.AppendUint32(encoder.data, 3)
		encoder.data = binary.BigEndian.AppendUint32(encoder.data, flags)
	}
	if err := encoder.pathRecords(); err != nil {
		return nil, err
	}
	padding := mask.Padding
	originalRecordCount := len(mask.Records)
	if encoder.records != originalRecordCount {
		if !zeroBytes(padding) {
			return nil, vectorError(ErrUnsupported, "vectorMask.padding", mask.PaddingSpan.Offset,
				"changed record counts cannot reinterpret retained nonzero alignment data")
		}
		padding = make([]byte, (4-len(encoder.data)%4)%4)
	}
	if len(padding) > 3 {
		return nil, vectorError(ErrMalformed, "vectorMask.padding", mask.PaddingSpan.Offset, "alignment exceeds three bytes")
	}
	if err := encoder.reserve(len(padding)); err != nil {
		return nil, err
	}
	encoder.data = append(encoder.data, padding...)
	return encoder.data, nil
}

func encodeConfiguration(options []EncodeOptions) (EncodeOptions, error) {
	configuration := EncodeOptions{MaxBytes: 32 << 20, MaxRecords: 100_000}
	if len(options) > 1 {
		return configuration, vectorError(ErrMalformed, "options", 0, "at most one encode option value is accepted")
	}
	if len(options) == 0 {
		return configuration, nil
	}
	provided := options[0]
	if provided.MaxBytes < 0 || provided.MaxRecords < 0 {
		return configuration, vectorError(ErrMalformed, "options", 0, "encode budgets must not be negative")
	}
	if provided.MaxBytes != 0 {
		configuration.MaxBytes = provided.MaxBytes
	}
	if provided.MaxRecords != 0 {
		configuration.MaxRecords = provided.MaxRecords
	}
	return configuration, nil
}

func resolvedFlags(mask *Mask) uint32 {
	original := uint32(0)
	if binary.BigEndian.Uint32(mask.Header[:4]) == 3 {
		original = binary.BigEndian.Uint32(mask.Header[4:])
	}
	flags := mask.Flags
	for index, value := range []bool{mask.Invert, mask.NotLinked, mask.Disabled} {
		bit := uint32(1) << index
		if value == (original&bit != 0) {
			continue
		}
		flags &^= bit
		if value {
			flags |= bit
		}
	}
	return flags
}

func (encoder *vectorEncoder) reserve(length int) error {
	if err := encoder.ctx.Err(); err != nil {
		return err
	}
	if length < 0 || int64(length) > encoder.options.MaxBytes-int64(len(encoder.data)) ||
		length > int(^uint(0)>>1)-len(encoder.data) {
		return vectorError(ErrLimit, "vectorMask.output", encoder.mask.Span.Offset, "encoded bytes exceed output budgets")
	}
	required := len(encoder.data) + length
	if cap(encoder.data) < required {
		limit := int(min(encoder.options.MaxBytes, int64(^uint(0)>>1)))
		capacity := limit
		if cap(encoder.data) <= limit/2 {
			capacity = min(limit, max(64, cap(encoder.data)*2))
		}
		capacity = max(required, capacity)
		buffer := make([]byte, len(encoder.data), capacity)
		copy(buffer, encoder.data)
		encoder.data = buffer
	}
	return nil
}

func (encoder *vectorEncoder) append(record [26]byte) error {
	if encoder.records >= encoder.options.MaxRecords {
		return vectorError(ErrLimit, "vectorMask.records", encoder.mask.Span.Offset, "record count exceeds output budgets")
	}
	if err := encoder.reserve(26); err != nil {
		return err
	}
	encoder.data = append(encoder.data, record[:]...)
	encoder.records++
	return nil
}

func (encoder *vectorEncoder) pathRecords() error {
	mask := encoder.mask
	if len(mask.Records) == 0 {
		if err := encoder.fillRule(); err != nil {
			return err
		}
		if err := encoder.initialFill(); err != nil {
			return err
		}
		for _, path := range mask.Subpaths {
			if err := encoder.subpath(path); err != nil {
				return err
			}
		}
		for _, clipboard := range mask.Clipboards {
			if err := encoder.clipboard(clipboard); err != nil {
				return err
			}
		}
		return nil
	}
	originalFill, originalInitial, originalInitialValue := false, false, false
	lastPath, lastClipboard := -1, -1
	for index, record := range mask.Records {
		if err := encoder.ctx.Err(); err != nil {
			return err
		}
		if record.Selector != binary.BigEndian.Uint16(record.Raw[:2]) {
			return vectorError(ErrMalformed, "vectorMask.records", record.Span.Offset,
				"retained record selector differs from its provenance bytes")
		}
		originalFill = originalFill || record.Selector == 6
		if record.Selector == 8 {
			originalInitial, originalInitialValue = true, binary.BigEndian.Uint16(record.Raw[2:4]) != 0
		}
		if record.Selector == 0 || record.Selector == 3 {
			lastPath = index
		}
		if record.Selector == 7 {
			lastClipboard = index
		}
	}
	if !originalFill && mask.HasFillRule {
		if err := encoder.fillRule(); err != nil {
			return err
		}
	}
	if !originalInitial && (mask.HasInitialFill || mask.InitialFill) {
		if err := encoder.initialFill(); err != nil {
			return err
		}
	}
	pathCursor, clipboardCursor := 0, 0
	for index := 0; index < len(mask.Records); index++ {
		record := mask.Records[index]
		switch record.Selector {
		case 0, 3:
			count := int(binary.BigEndian.Uint16(record.Raw[2:4]))
			if count > len(mask.Records)-index-1 {
				return vectorError(ErrMalformed, "vectorMask.records", record.Span.Offset, "retained knot count is truncated")
			}
			for knotIndex := index + 1; knotIndex <= index+count; knotIndex++ {
				selector := mask.Records[knotIndex].Selector
				if selector != 1 && selector != 2 && selector != 4 && selector != 5 {
					return vectorError(ErrMalformed, "vectorMask.records", record.Span.Offset,
						"retained subpath count includes a non-knot record")
				}
			}
			if pathCursor < len(mask.Subpaths) {
				if err := encoder.subpath(mask.Subpaths[pathCursor]); err != nil {
					return err
				}
				pathCursor++
			}
			if index == lastPath {
				for pathCursor < len(mask.Subpaths) {
					if err := encoder.subpath(mask.Subpaths[pathCursor]); err != nil {
						return err
					}
					pathCursor++
				}
			}
			index += count
		case 1, 2, 4, 5:
			return vectorError(ErrMalformed, "vectorMask.records", record.Span.Offset, "retained knot has no length record")
		case 6:
			if mask.HasFillRule {
				if err := encoder.append(record.Raw); err != nil {
					return err
				}
			}
		case 8:
			if mask.HasInitialFill || mask.InitialFill {
				data := record.Raw
				if mask.InitialFill != originalInitialValue {
					binary.BigEndian.PutUint16(data[2:4], boolWord(mask.InitialFill))
				}
				if err := encoder.append(data); err != nil {
					return err
				}
			}
		case 7:
			if clipboardCursor < len(mask.Clipboards) {
				if err := encoder.clipboard(mask.Clipboards[clipboardCursor]); err != nil {
					return err
				}
				clipboardCursor++
			}
			if index == lastClipboard {
				for clipboardCursor < len(mask.Clipboards) {
					if err := encoder.clipboard(mask.Clipboards[clipboardCursor]); err != nil {
						return err
					}
					clipboardCursor++
				}
			}
		default:
			if err := encoder.append(record.Raw); err != nil {
				return err
			}
		}
	}
	for pathCursor < len(mask.Subpaths) {
		if err := encoder.subpath(mask.Subpaths[pathCursor]); err != nil {
			return err
		}
		pathCursor++
	}
	for clipboardCursor < len(mask.Clipboards) {
		if err := encoder.clipboard(mask.Clipboards[clipboardCursor]); err != nil {
			return err
		}
		clipboardCursor++
	}
	return nil
}

func (encoder *vectorEncoder) sourceLengthRecord(index int) bool {
	if index < 0 || index >= len(encoder.mask.Records) {
		return false
	}
	selector := encoder.mask.Records[index].Selector
	return selector == 0 || selector == 3
}

func (encoder *vectorEncoder) fillRule() error {
	var record [26]byte
	binary.BigEndian.PutUint16(record[:2], 6)
	return encoder.append(record)
}

func (encoder *vectorEncoder) initialFill() error {
	var record [26]byte
	binary.BigEndian.PutUint16(record[:2], 8)
	binary.BigEndian.PutUint16(record[2:4], boolWord(encoder.mask.InitialFill))
	return encoder.append(record)
}

func boolWord(value bool) uint16 {
	if value {
		return 1
	}
	return 0
}

func (encoder *vectorEncoder) subpath(path Subpath) error {
	if len(path.Knots) > math.MaxUint16 {
		return vectorError(ErrLimit, "vectorMask.subpaths", path.Span.Offset, "subpath exceeds its 16-bit knot count")
	}
	var record [26]byte
	if encoder.sourceLengthRecord(path.RecordIndex) {
		record = encoder.mask.Records[path.RecordIndex].Raw
	}
	selector := uint16(3)
	if path.Closed {
		selector = 0
	}
	binary.BigEndian.PutUint16(record[:2], selector)
	binary.BigEndian.PutUint16(record[2:4], uint16(len(path.Knots)))
	binary.BigEndian.PutUint16(record[4:6], uint16(path.Operation))
	binary.BigEndian.PutUint16(record[6:8], path.Unknown1)
	binary.BigEndian.PutUint32(record[8:12], path.Unknown2)
	binary.BigEndian.PutUint32(record[12:16], path.Index)
	copy(record[16:], path.Reserved[:])
	if err := encoder.append(record); err != nil {
		return err
	}
	for _, knot := range path.Knots {
		if err := encoder.knot(knot, path.Closed); err != nil {
			return err
		}
	}
	return nil
}

func (encoder *vectorEncoder) knot(knot Knot, closed bool) error {
	var record [26]byte
	retained := false
	if knot.RecordIndex >= 0 && knot.RecordIndex < len(encoder.mask.Records) {
		source := encoder.mask.Records[knot.RecordIndex]
		if source.Selector == 1 || source.Selector == 2 || source.Selector == 4 || source.Selector == 5 {
			record, retained = source.Raw, true
		}
	}
	selector := uint16(5)
	if closed {
		selector = 2
	}
	if knot.Linked {
		selector--
	}
	binary.BigEndian.PutUint16(record[:2], selector)
	for index, point := range []Point{knot.Preceding, knot.Anchor, knot.Leaving} {
		offset := 2 + index*8
		originalY, originalX := int32(binary.BigEndian.Uint32(record[offset:])),
			int32(binary.BigEndian.Uint32(record[offset+4:]))
		x, err := resolveCoordinate(point.X, point.RawX, originalX, encoder.mask.Width, retained)
		if err != nil {
			return err
		}
		y, err := resolveCoordinate(point.Y, point.RawY, originalY, encoder.mask.Height, retained)
		if err != nil {
			return err
		}
		binary.BigEndian.PutUint32(record[offset:], uint32(y))
		binary.BigEndian.PutUint32(record[offset+4:], uint32(x))
	}
	return encoder.append(record)
}

func resolveCoordinate(pixel float64, raw, original int32, size int, retained bool) (int32, error) {
	if math.IsNaN(pixel) || math.IsInf(pixel, 0) {
		return 0, vectorError(ErrMalformed, "vectorMask.points", 0, "coordinate is not finite")
	}
	value := math.Round(pixel / float64(size) * (1 << 24))
	if value < math.MinInt32 || value > math.MaxInt32 {
		return 0, vectorError(ErrLimit, "vectorMask.points", 0, "coordinate does not fit signed 8.24 encoding")
	}
	normalized := int32(value)
	if !retained {
		if raw != 0 && normalized != 0 && raw != normalized {
			return 0, vectorError(ErrMalformed, "vectorMask.points", 0, "new raw and pixel coordinates disagree")
		}
		if normalized == 0 && raw != 0 {
			return raw, nil
		}
		return normalized, nil
	}
	originalPixel := float64(original) / (1 << 24) * float64(size)
	rawChanged, pixelChanged := raw != original, pixel != originalPixel
	if rawChanged && pixelChanged && normalized != raw {
		return 0, vectorError(ErrMalformed, "vectorMask.points", 0, "raw and pixel coordinate edits disagree")
	}
	if rawChanged {
		return raw, nil
	}
	if pixelChanged {
		return normalized, nil
	}
	return original, nil
}

func (encoder *vectorEncoder) clipboard(clipboard Clipboard) error {
	var record [26]byte
	if clipboard.RecordIndex >= 0 && clipboard.RecordIndex < len(encoder.mask.Records) &&
		encoder.mask.Records[clipboard.RecordIndex].Selector == 7 {
		record = encoder.mask.Records[clipboard.RecordIndex].Raw
	}
	binary.BigEndian.PutUint16(record[:2], 7)
	for index, value := range []int32{
		clipboard.Top, clipboard.Left, clipboard.Bottom, clipboard.Right, clipboard.Resolution,
	} {
		binary.BigEndian.PutUint32(record[2+index*4:], uint32(value))
	}
	return encoder.append(record)
}

// EqualEncoding reports whether a complete mask payload is unchanged after typed round-trip encoding.
// It is useful for callers choosing a raw-block preservation path before a writer rebuild.
func EqualEncoding(ctx context.Context, mask *Mask, data []byte, options ...EncodeOptions) (bool, error) {
	encoded, err := EncodeMask(ctx, mask, options...)
	if err != nil {
		return false, err
	}
	return bytes.Equal(encoded, data), nil
}
