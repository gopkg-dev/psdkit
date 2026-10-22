package psdfile

import "unsafe"

// PatternTileEstimate bounds one RGB8 tile decode's retained and peak working allocations.
// WorkingBytes includes RetainedBytes, all temporary plane samples and decoder scratch.
type PatternTileEstimate struct {
	RetainedBytes, WorkingBytes int64
}

// EstimatePatternTileRGB8 checks source records and estimates decode cost without reading pixel payloads.
// A combined renderer reserves WorkingBytes before DecodePatternTileRGB8, then retains only RetainedBytes.
func (d *Document) EstimatePatternTileRGB8(record PatternRecord) (PatternTileEstimate, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return PatternTileEstimate{}, ErrClosed
	}
	return d.estimatePatternTileRGB8(record, d.limits)
}

func (d *Document) estimatePatternTileRGB8(record PatternRecord, limits Limits) (PatternTileEstimate, error) {
	var estimate PatternTileEstimate
	if record.Version != 1 || record.VMVersion != 3 || len(record.Unsupported) != 0 ||
		record.ColorMode != 3 || record.VMChannels < 3 {
		return estimate, fileError(ErrUnsupported, "patterns", "patterns.rgb", record.Body.Offset,
			"pattern is not a recognized RGB8 tile")
	}
	if !d.validSpan(record.Body) || !d.validSpan(record.VMBody) ||
		uint64(len(record.Arrays)) != uint64(record.VMChannels)+2 || len(record.Arrays) > limits.MaxBlocks {
		return estimate, fileError(ErrMalformed, "patterns", "patterns.record", record.Body.Offset,
			"pattern spans or virtual array count are inconsistent")
	}
	view := &Document{limits: limits}
	shape, err := view.decodeBounds(record.VMBounds, 8, "patterns.rgb", record.Body.Offset)
	if err != nil {
		return estimate, err
	}
	if shape.width == 0 || shape.height == 0 || int64(shape.length) > limits.MaxDecodeBytes/4 {
		return estimate, fileError(ErrLimit, "patterns", "patterns.rgb", record.Body.Offset,
			"RGB output tile exceeds source decode budgets")
	}
	estimate.RetainedBytes = int64(shape.length) * 4
	estimate.WorkingBytes = estimate.RetainedBytes + (128 << 10) + 512 +
		int64(len(record.Arrays))*int64(unsafe.Sizeof(PatternPlane{})+unsafe.Sizeof(decodeShape{}))
	if estimate.WorkingBytes > limits.MaxDecodeBytes {
		return estimate, fileError(ErrLimit, "patterns", "patterns.rgb", record.Body.Offset,
			"tile output and scratch exceed source decode budgets")
	}
	found := [3]bool{}
	for _, array := range record.Arrays {
		if array.Written == 0 || array.DeclaredLength == 0 {
			continue
		}
		if array.Written != 1 || array.Depth != 8 || array.PixelDepth != 8 || array.Compression > 3 ||
			array.Bounds != record.VMBounds || array.Index < 0 ||
			array.Index > 2 && array.Index != int(record.VMChannels)+1 {
			return estimate, fileError(ErrUnsupported, "patterns", "patterns.rgb", array.Span.Offset,
				"written tile arrays have unsupported mask, depth or channel semantics")
		}
		if !d.validSpan(array.Data) || !d.validSpan(array.Body) || array.Data.Offset < array.Body.Offset ||
			array.Data.Offset+array.Data.Length > array.Body.Offset+array.Body.Length {
			return estimate, fileError(ErrMalformed, "patterns", "patterns.array", array.Span.Offset,
				"array pixels exceed the source channel body")
		}
		arrayShape, err := view.decodeBounds(array.Bounds, array.PixelDepth, "patterns.array", array.Span.Offset)
		if err != nil {
			return estimate, err
		}
		if int64(arrayShape.length) > limits.MaxDecodeBytes-estimate.WorkingBytes {
			return estimate, fileError(ErrLimit, "patterns", "patterns.samples", array.Span.Offset,
				"tile plane samples exceed the aggregate decode budget")
		}
		estimate.WorkingBytes += int64(arrayShape.length)
		if array.Index < 3 {
			if found[array.Index] {
				return estimate, fileError(ErrMalformed, "patterns", "patterns.rgb", array.Span.Offset,
					"duplicate RGB array ordinal")
			}
			found[array.Index] = true
		}
	}
	if !found[0] || !found[1] || !found[2] {
		return estimate, fileError(ErrUnsupported, "patterns", "patterns.rgb", record.Body.Offset,
			"RGB source channel arrays are incomplete")
	}
	return estimate, nil
}
