package psdfile

import (
	"context"
	"errors"
	"io"
	"slices"
	"sort"
	"unsafe"
)

type tiffPrivateSwap struct {
	span  Span
	width uint8
}

type tiffPrivateReader struct {
	parent *Document
	span   Span
	swaps  []tiffPrivateSwap
}

// tiffPrivateOriginalReader borrows source bytes while retaining the private view's closed-source semantics.
// It locks the view per source read; callers must not hold an outer view lock during embedded indexing.
type tiffPrivateOriginalReader struct {
	source *TIFFLayerSource
}

func (reader *tiffPrivateOriginalReader) ReadAt(data []byte, offset int64) (int, error) {
	reader.source.view.mu.RLock()
	defer reader.source.view.mu.RUnlock()
	if reader.source.view.closed {
		return 0, ErrClosed
	}
	count, err := reader.source.parent.ReadAt(data, offset)
	if err != nil && err != io.EOF { //nolint:errorlint // Only bare EOF ends a read; joined errors retain source faults.
		return 0, err
	}
	if count < 0 || count > len(data) {
		return 0, fileError(ErrMalformed, "tiff.layers", "source", offset, "invalid original ReaderAt count")
	}
	return count, err
}

func (reader *tiffPrivateReader) original(data []byte, offset int64) (int, error) {
	count, err := reader.parent.ReadAt(data, offset)
	if err != nil && err != io.EOF { //nolint:errorlint // Only bare EOF ends a read; joined errors retain source faults.
		return 0, err
	}
	if count < 0 || count > len(data) {
		return 0, fileError(ErrMalformed, "tiff.layers", "source", offset, "invalid original ReaderAt count")
	}
	return count, err
}

func (reader *tiffPrivateReader) ReadAt(data []byte, offset int64) (int, error) {
	if offset < reader.span.Offset || offset > reader.span.Offset+reader.span.Length {
		return 0, io.EOF
	}
	requested := len(data)
	available := reader.span.Offset + reader.span.Length - offset
	if int64(len(data)) > available {
		data = data[:available]
	}
	count, err := reader.original(data, offset)
	if err != nil && err != io.EOF || count != len(data) { //nolint:errorlint // Only bare EOF ends a read; joined errors retain source faults.
		return count, err
	}
	start := sort.Search(len(reader.swaps), func(index int) bool {
		return reader.swaps[index].span.Offset+reader.swaps[index].span.Length > offset
	})
	end := offset + int64(count)
	for index := start; index < len(reader.swaps); index++ {
		swap := reader.swaps[index]
		if swap.span.Offset >= end {
			break
		}
		width := int64(swap.width)
		first := swap.span.Offset + max(int64(0), (offset-swap.span.Offset)/width)*width
		for position := first; position < min(end, swap.span.Offset+swap.span.Length); position += width {
			left, right := max(position, offset), min(position+width, end)
			if left == position && right == position+width {
				value := data[position-offset : position-offset+width]
				for low, high := 0, len(value)-1; low < high; low, high = low+1, high-1 {
					value[low], value[high] = value[high], value[low]
				}
				continue
			}
			var original [8]byte
			read, readErr := reader.original(original[:width], position)
			if read != int(width) || readErr != nil && readErr != io.EOF { //nolint:errorlint // Only bare EOF ends a read; joined errors retain source faults.
				if readErr == nil || readErr == io.EOF { //nolint:errorlint // Only bare EOF ends a read; joined errors retain source faults.
					readErr = errors.Join(io.ErrUnexpectedEOF, readErr)
				}
				return 0, readErr
			}
			for selected := left; selected < right; selected++ {
				data[selected-offset] = original[width-1-(selected-position)]
			}
		}
	}
	if count != requested {
		return count, io.EOF
	}
	return count, nil
}

type tiffPrivateBuilder struct {
	ctx      context.Context
	parent   *Document
	span     Span
	limits   Limits
	swaps    []tiffPrivateSwap
	opaque   []TIFFOpaqueBlock
	metadata int64
	items    int
	blocks   int
}

func (builder *tiffPrivateBuilder) reserve(amount int64, path string, offset int64) error {
	if amount < 0 || amount > builder.limits.MaxMetadataBytes-builder.metadata ||
		amount > builder.limits.MaxDecodeBytes-builder.metadata {
		return fileError(ErrLimit, "tiff.layers", path, offset, "private layer metadata exceeds the working budget")
	}
	builder.metadata += amount
	return nil
}

func (builder *tiffPrivateBuilder) count(count uint32, path string, offset int64) error {
	if uint64(count) > uint64(builder.limits.MaxDescriptorItems-builder.items) {
		return fileError(ErrLimit, "tiff.layers", path, offset, "private metadata items exceed their traversal limit")
	}
	builder.items += int(count)
	return nil
}

func (builder *tiffPrivateBuilder) addSwap(span Span, width uint8, path string) error {
	if err := builder.ctx.Err(); err != nil {
		return err
	}
	if width != 2 && width != 4 && width != 8 || span.Offset < builder.span.Offset || span.Length < 0 ||
		span.Length%int64(width) != 0 || span.Offset > builder.span.Offset+builder.span.Length ||
		span.Length > builder.span.Offset+builder.span.Length-span.Offset {
		return fileError(ErrMalformed, "tiff.layers", path, span.Offset, "invalid private normalization interval")
	}
	if span.Length == 0 {
		return nil
	}
	if len(builder.swaps) == cap(builder.swaps) {
		capacity := max(1, cap(builder.swaps)*2)
		if err := builder.reserve(int64(capacity-cap(builder.swaps))*int64(unsafe.Sizeof(tiffPrivateSwap{})),
			path, span.Offset); err != nil {
			return err
		}
		owned := make([]tiffPrivateSwap, len(builder.swaps), capacity)
		copy(owned, builder.swaps)
		builder.swaps = owned
	}
	builder.swaps = append(builder.swaps, tiffPrivateSwap{span: span, width: width})
	return nil
}

func (builder *tiffPrivateBuilder) orderedReader() (*tiffPrivateReader, error) {
	slices.SortFunc(builder.swaps, func(left, right tiffPrivateSwap) int {
		if left.span.Offset < right.span.Offset {
			return -1
		}
		if left.span.Offset > right.span.Offset {
			return 1
		}
		return 0
	})
	for index := 1; index < len(builder.swaps); index++ {
		prior, current := builder.swaps[index-1], builder.swaps[index]
		if prior.span.Offset+prior.span.Length > current.span.Offset {
			return nil, fileError(ErrMalformed, "tiff.layers", "normalization", current.span.Offset,
				"private typed normalization intervals overlap")
		}
	}
	return &tiffPrivateReader{parent: builder.parent, span: builder.span, swaps: builder.swaps}, nil
}

func (builder *tiffPrivateBuilder) open(record LinkedFileRecord) (*TIFFLayerSource, error) {
	return builder.index(record)
}
