package psdfile

import (
	"context"
	"errors"
	"io"
)

var (
	errTIFFLZWCode     = errors.New("invalid TIFF LZW dictionary code")
	errTIFFLZWClear    = errors.New("TIFF LZW strip must begin with a clear code")
	errTIFFLZWTrailing = errors.New("bytes follow TIFF LZW end-of-information code")
)

// linkedTIFFLZW implements TIFF6 section 13's MSB-first, early-width-change 9-to-12-bit coding.
// A strip owns one bounded dictionary; no decoded strip-sized or per-code allocations are made.
type linkedTIFFLZW struct {
	context         context.Context
	reader          io.ByteReader
	prefix          [4096]uint16
	suffix          [4096]byte
	stack           [4097]byte
	pending         int
	bits            uint32
	bitCount, width uint
	next, previous  uint16
	previousFirst   byte
	started, ended  bool
	failure         error
}

func (reader *linkedTIFFLZW) Read(output []byte) (int, error) {
	if err := reader.context.Err(); err != nil {
		return 0, err
	}
	written := 0
	for written < len(output) {
		if err := reader.context.Err(); err != nil {
			return written, err
		}
		if reader.pending != 0 {
			count := min(reader.pending, len(output)-written)
			for index := range count {
				reader.pending--
				output[written+index] = reader.stack[reader.pending]
			}
			written += count
			continue
		}
		if reader.failure != nil {
			return written, reader.failure
		}
		if reader.ended {
			return written, io.EOF
		}
		if err := reader.expandNext(); err != nil {
			reader.failure = err
			return written, err
		}
	}
	return written, nil
}

func (reader *linkedTIFFLZW) code() (uint16, error) {
	for reader.bitCount < reader.width {
		value, err := reader.reader.ReadByte()
		if err != nil {
			if err == io.EOF { //nolint:errorlint // Preserve source errors joined with EOF.
				return 0, io.ErrUnexpectedEOF
			}
			return 0, err
		}
		reader.bits = reader.bits<<8 | uint32(value)
		reader.bitCount += 8
	}
	reader.bitCount -= reader.width
	value := uint16(reader.bits >> reader.bitCount)
	reader.bits &= (1 << reader.bitCount) - 1
	return value, nil
}

func (reader *linkedTIFFLZW) expandNext() error {
	if !reader.started {
		reader.width = 9
	}
	code, err := reader.code()
	if err != nil {
		return err
	}
	if !reader.started && code != 256 {
		return errTIFFLZWClear
	}
	reader.started = true
	if code == 256 {
		reader.width, reader.next, reader.previous = 9, 258, 256
		return nil
	}
	if code == 257 {
		reader.ended = true
		return nil
	}
	if code > reader.next || reader.previous == 256 && code > 255 || reader.next > 4095 {
		return errTIFFLZWCode
	}
	current := code
	if code == reader.next {
		// The sole forward reference is the previous string followed by its own first byte.
		reader.stack[0], reader.pending = reader.previousFirst, 1
		current = reader.previous
	}
	for current >= 258 {
		if current >= reader.next || reader.pending >= len(reader.stack)-1 || reader.prefix[current] >= current {
			return errTIFFLZWCode
		}
		reader.stack[reader.pending] = reader.suffix[current]
		reader.pending++
		current = reader.prefix[current]
	}
	if current > 255 {
		return errTIFFLZWCode
	}
	first := byte(current)
	reader.stack[reader.pending] = first
	reader.pending++
	if reader.previous != 256 {
		reader.prefix[reader.next], reader.suffix[reader.next] = reader.previous, first
		reader.next++
		// TIFF6 switches after storing entries 510, 1022 and 2046, before the next code is read.
		if reader.width < 12 && reader.next == uint16(1<<reader.width)-1 {
			reader.width++
		}
	}
	reader.previous, reader.previousFirst = code, first
	return nil
}

func (reader *linkedTIFFLZW) finish() error {
	if err := reader.context.Err(); err != nil {
		return err
	}
	_, err := reader.reader.ReadByte()
	if err == io.EOF { //nolint:errorlint // Remaining bits in the EOI byte are padding; no later bytes are allowed.
		return nil
	}
	if err != nil {
		return err
	}
	return errTIFFLZWTrailing
}
