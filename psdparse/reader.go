package psdparse

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf16"
)

type source struct {
	reader               io.ReaderAt
	size                 int64
	cache                [64 << 10]byte
	cacheStart, cacheEnd int64
	demandOnly           bool
}

type cursor struct {
	source        *source
	position, end int64
	err           error
}

func (r *cursor) remaining() int64 { return r.end - r.position }

func (r *cursor) fail(format string, args ...any) {
	if r.err == nil {
		r.err = fmt.Errorf(format, args...)
	}
}

func (r *cursor) read(target []byte) {
	if r.err != nil {
		return
	}
	if int64(len(target)) > r.remaining() {
		r.fail("truncated PSD structure at offset %d", r.position)
		return
	}
	for len(target) > 0 {
		if r.position < r.source.cacheStart || r.position >= r.source.cacheEnd {
			r.source.cacheStart = r.position
			length := min(int64(len(r.source.cache)), r.source.size-r.position)
			if r.source.demandOnly {
				length = min(length, int64(len(target)))
			}
			if length <= 0 {
				r.err = io.ErrUnexpectedEOF
				return
			}
			count, err := r.source.reader.ReadAt(r.source.cache[:length], r.position)
			if count < 0 || int64(count) > length {
				r.err = fmt.Errorf("invalid ReaderAt count at offset %d", r.position)
				return
			}
			if err != nil && !errors.Is(err, io.EOF) {
				r.err = err
				return
			}
			if int64(count) != length {
				r.err = errors.Join(io.ErrUnexpectedEOF, err)
				return
			}
			r.source.cacheEnd = r.position + int64(count)
			if count == 0 {
				if err == nil {
					err = io.ErrUnexpectedEOF
				}
				r.err = err
				return
			}
		}
		available := min(len(target), int(r.source.cacheEnd-r.position))
		copy(target[:available], r.source.cache[r.position-r.source.cacheStart:])
		r.position += int64(available)
		target = target[available:]
	}
}

func (r *cursor) skip(count int64) {
	if count < 0 || count > r.remaining() {
		r.fail("invalid PSD section length at offset %d", r.position)
		return
	}
	r.position += count
}

func (r *cursor) section(count int64) *cursor {
	child := &cursor{source: r.source, position: r.position, end: r.position, err: r.err}
	if count < 0 || count > r.remaining() {
		r.fail("invalid PSD section length %d at offset %d", count, r.position)
		child.err = r.err
		return child
	}
	child.end += count
	r.position += count
	return child
}

func (r *cursor) bytes(count int64) []byte {
	if count < 0 || count > 8<<20 || count > r.remaining() {
		r.fail("PSD metadata value exceeds its allocation limit")
		return nil
	}
	value := make([]byte, int(count))
	r.read(value)
	return value
}

func (r *cursor) u8() uint8    { var b [1]byte; r.read(b[:]); return b[0] }
func (r *cursor) u16() uint16  { var b [2]byte; r.read(b[:]); return binary.BigEndian.Uint16(b[:]) }
func (r *cursor) i16() int16   { return int16(r.u16()) }
func (r *cursor) u32() uint32  { var b [4]byte; r.read(b[:]); return binary.BigEndian.Uint32(b[:]) }
func (r *cursor) i32() int32   { return int32(r.u32()) }
func (r *cursor) u64() uint64  { var b [8]byte; r.read(b[:]); return binary.BigEndian.Uint64(b[:]) }
func (r *cursor) f64() float64 { return math.Float64frombits(r.u64()) }
func (r *cursor) f32() float64 { return float64(math.Float32frombits(r.u32())) }
func (r *cursor) sig() string  { var b [4]byte; r.read(b[:]); return string(b[:]) }

func (r *cursor) unicode() string {
	count := int64(r.u32())
	if count > 1<<20 || count*2 > r.remaining() {
		r.fail("invalid PSD Unicode string length")
		return ""
	}
	encoded := r.bytes(count * 2)
	words := make([]uint16, len(encoded)/2)
	for index := range words {
		words[index] = binary.BigEndian.Uint16(encoded[index*2:])
	}
	return strings.TrimRight(string(utf16.Decode(words)), "\x00")
}

func (r *cursor) pascal(alignment int64) string {
	count := int64(r.u8())
	value := string(r.bytes(count))
	consumed := count + 1
	r.skip((alignment - consumed%alignment) % alignment)
	return strings.TrimRight(value, "\x00")
}

func (r *cursor) classID() string {
	count := int64(r.u32())
	if count == 0 {
		count = 4
	}
	if count > 1<<20 {
		r.fail("invalid descriptor class identifier")
		return ""
	}
	return string(r.bytes(count))
}

func (r *cursor) absorb(child *cursor) {
	if r.err == nil && child.err != nil {
		r.err = child.err
	}
}
