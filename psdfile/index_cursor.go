package psdfile

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"unsafe"
)

// indexCursor reads only requested structural bytes; it never reads ahead into payloads.
type indexCursor struct {
	document *Document
	position int64
	end      int64
	path     string
	err      error
}

func (c *indexCursor) remaining() int64 { return c.end - c.position }

func (c *indexCursor) fail(path string, offset int64, detail string) {
	if c.err == nil {
		c.err = fileError(ErrMalformed, "index", path, offset, detail)
	}
}

func (c *indexCursor) read(data []byte, field string) {
	if c.err != nil {
		return
	}
	if int64(len(data)) > c.remaining() {
		c.err = &Error{Kind: ErrMalformed, Feature: "index", Path: c.path + "." + field,
			Offset: c.position, Detail: "structural field exceeds its enclosing section", Cause: io.ErrUnexpectedEOF}
		return
	}
	count, err := c.document.reader.ReadAt(data, c.position)
	if count != len(data) || err != nil && !errors.Is(err, io.EOF) {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		c.err = &Error{Kind: ErrMalformed, Feature: "index", Path: c.path + "." + field,
			Offset: c.position, Detail: "structural field could not be read", Cause: err}
		return
	}
	c.position += int64(len(data))
}

func (c *indexCursor) take(length int64, field string) Span {
	span := Span{Offset: c.position, Length: length}
	if c.err != nil {
		return span
	}
	if length < 0 || length > c.remaining() {
		c.err = &Error{Kind: ErrMalformed, Feature: "index", Path: c.path + "." + field,
			Offset: c.position, Detail: "declared length exceeds its enclosing section", Cause: io.ErrUnexpectedEOF}
		return span
	}
	c.position += length
	return span
}

func (c *indexCursor) child(span Span, path string) *indexCursor {
	return &indexCursor{document: c.document, position: span.Offset, end: span.Offset + span.Length, path: path}
}

func (c *indexCursor) u8(field string) uint8 {
	var data [1]byte
	c.read(data[:], field)
	return data[0]
}

func (c *indexCursor) u16(field string) uint16 {
	var data [2]byte
	c.read(data[:], field)
	return binary.BigEndian.Uint16(data[:])
}

func (c *indexCursor) u32(field string) uint32 {
	var data [4]byte
	c.read(data[:], field)
	return binary.BigEndian.Uint32(data[:])
}

func (c *indexCursor) u64(field string) uint64 {
	var data [8]byte
	c.read(data[:], field)
	return binary.BigEndian.Uint64(data[:])
}

func (c *indexCursor) f64(field string) float64 { return math.Float64frombits(c.u64(field)) }

func (c *indexCursor) signature(field string) string {
	var data [4]byte
	c.read(data[:], field)
	return string(data[:])
}

func (c *indexCursor) length(width uint8, field string) int64 {
	if width == 4 {
		return int64(c.u32(field))
	}
	offset := c.position
	value := c.u64(field)
	if value > math.MaxInt64 {
		c.fail(c.path+"."+field, offset, "unsigned length cannot be represented as a source offset")
		return 0
	}
	return int64(value)
}

func (c *indexCursor) section(width uint8, field string) (Span, Span) {
	start := c.position
	length := c.length(width, field+".length")
	data := c.take(length, field)
	return Span{Offset: start, Length: int64(width) + length}, data
}

type indexBudget struct {
	document *Document
	metadata int64
	blocks   int
	layers   int
}

func (b *indexBudget) reserve(length int64, path string, offset int64) error {
	if length < 0 || length > b.document.limits.MaxMetadataBytes-b.metadata {
		return fileError(ErrLimit, "index", path, offset, "indexed metadata exceeds MaxMetadataBytes")
	}
	b.metadata += length
	return nil
}

func (b *indexBudget) block(path string, offset int64) error {
	if b.blocks >= b.document.limits.MaxBlocks {
		return fileError(ErrLimit, "index", path, offset, "indexed blocks exceed MaxBlocks")
	}
	b.blocks++
	return nil
}

// indexAppend charges the retained backing array capacity before allocating it.
func indexAppend[T any](b *indexBudget, target *[]T, item T, path string, offset int64) error {
	if len(*target) == cap(*target) {
		capacity := max(1, cap(*target)*2)
		length := int64(capacity-cap(*target)) * int64(unsafe.Sizeof(item))
		if err := b.reserve(length, path, offset); err != nil {
			return err
		}
		values := make([]T, len(*target), capacity)
		copy(values, *target)
		*target = values
	}
	*target = append(*target, item)
	return nil
}
