package psdparse

import (
	"bufio"
	"compress/zlib"
	"fmt"
	"image"
	"io"
	"math"
)

type zipPayload struct {
	reader   io.ReadCloser
	section  *io.SectionReader
	buffered *bufio.Reader
	length   int64
}

func openZIPPayload(r *cursor) (*zipPayload, error) {
	length := r.remaining()
	section := io.NewSectionReader(r.source.reader, r.position, length)
	buffered := bufio.NewReader(section)
	reader, err := zlib.NewReader(buffered)
	if err != nil {
		return nil, err
	}
	return &zipPayload{reader: reader, section: section, buffered: buffered, length: length}, nil
}

func (payload *zipPayload) finish() error {
	var extra [1]byte
	count, err := io.ReadFull(payload.reader, extra[:])
	if count != 0 {
		return fmt.Errorf("PSD ZIP payload exceeds declared pixel dimensions")
	}
	if err != io.EOF { //nolint:errorlint // Only bare EOF ends a read; joined errors retain source faults.
		return fmt.Errorf("validate PSD ZIP payload checksum: %w", err)
	}
	read, err := payload.section.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	consumed := read - int64(payload.buffered.Buffered())
	if consumed != payload.length {
		return fmt.Errorf("PSD ZIP payload contains %d trailing bytes", payload.length-consumed)
	}
	return nil
}

func writeRow(target *image.NRGBA, row []byte, y, component int) {
	if component < 0 || component > 3 {
		return
	}
	offset := y*target.Stride + component
	for _, value := range row {
		target.Pix[offset] = value
		offset += 4
	}
}

func decodePackBits(encoded, row []byte) error {
	position := 0
	for offset := 0; offset < len(encoded); {
		control := int(int8(encoded[offset]))
		offset++
		switch {
		case control >= 0:
			count := control + 1
			if offset+count > len(encoded) || position+count > len(row) {
				return fmt.Errorf("invalid PSD PackBits literal")
			}
			copy(row[position:], encoded[offset:offset+count])
			offset += count
			position += count
		case control >= -127:
			count := 1 - control
			if offset >= len(encoded) || position+count > len(row) {
				return fmt.Errorf("invalid PSD PackBits repeat")
			}
			for range count {
				row[position] = encoded[offset]
				position++
			}
			offset++
		}
	}
	if position != len(row) {
		return fmt.Errorf("PSD PackBits row has %d pixels; expected %d", position, len(row))
	}
	return nil
}

func (p *parser) decodePlane(r *cursor, target *image.NRGBA, component int) {
	compression := r.u16()
	width, height := target.Rect.Dx(), target.Rect.Dy()
	row := make([]byte, width)
	switch compression {
	case 0:
		if r.remaining() != int64(width)*int64(height) {
			r.fail("PSD raw channel length does not match dimensions")
			return
		}
		for y := 0; y < height && r.err == nil; y++ {
			r.read(row)
			writeRow(target, row, y, component)
		}
	case 1:
		lengths := p.rleRowLengths(r, height)
		for y, length := range lengths {
			encoded := r.bytes(length)
			if r.err != nil {
				return
			}
			if err := decodePackBits(encoded, row); err != nil {
				r.err = err
				return
			}
			writeRow(target, row, y, component)
		}
		if r.remaining() != 0 {
			r.fail("PSD RLE channel exceeds declared row lengths")
		}
	case 2, 3:
		payload, err := openZIPPayload(r)
		if err != nil {
			r.err = fmt.Errorf("read PSD ZIP channel: %w", err)
			return
		}
		defer func() { _ = payload.reader.Close() }()
		for y := range height {
			if _, err := io.ReadFull(payload.reader, row); err != nil {
				r.err = fmt.Errorf("decode PSD ZIP channel: %w", err)
				return
			}
			if compression == 3 {
				for x := 1; x < width; x++ {
					row[x] += row[x-1]
				}
			}
			writeRow(target, row, y, component)
		}
		if err := payload.finish(); err != nil {
			r.err = fmt.Errorf("finish PSD ZIP channel: %w", err)
			return
		}
		r.position = r.end
	default:
		r.fail("unsupported PSD channel compression %d", compression)
	}
}

func (p *parser) rleRowLengths(r *cursor, count int) []int64 {
	lengthWidth := int64(2)
	if p.version == 2 {
		lengthWidth = 4
	}
	if int64(count)*lengthWidth > r.remaining() {
		r.fail("truncated PSD/PSB RLE row lengths at offset %d", r.position)
		return nil
	}
	lengths := make([]int64, count)
	for index := range lengths {
		if p.version == 2 {
			lengths[index] = int64(r.u32())
		} else {
			lengths[index] = int64(r.u16())
		}
	}
	available := r.remaining()
	for _, length := range lengths {
		if length > available {
			r.fail("PSD/PSB RLE row lengths exceed channel data at offset %d", r.position)
			return nil
		}
		available -= length
	}
	return lengths
}

func (p *parser) composite(r *cursor) {
	compression := r.u16()
	width, height := p.document.Width, p.document.Height
	p.document.Composite = freshImage(image.Rect(0, 0, width, height))
	target := p.document.Composite
	row := make([]byte, width)
	switch compression {
	case 0:
		for component := 0; component < p.channels; component++ {
			for y := 0; y < height && r.err == nil; y++ {
				r.read(row)
				writeRow(target, row, y, component)
			}
		}
	case 1:
		lengths := p.rleRowLengths(r, height*p.channels)
		if r.err != nil {
			return
		}
		for component := 0; component < p.channels; component++ {
			for y := range height {
				encoded := r.bytes(lengths[component*height+y])
				if r.err != nil {
					return
				}
				if err := decodePackBits(encoded, row); err != nil {
					r.err = err
					return
				}
				writeRow(target, row, y, component)
			}
		}
	case 2, 3:
		payload, err := openZIPPayload(r)
		if err != nil {
			r.err = fmt.Errorf("read PSD ZIP composite: %w", err)
			return
		}
		defer func() { _ = payload.reader.Close() }()
		for component := 0; component < p.channels; component++ {
			for y := range height {
				if _, err := io.ReadFull(payload.reader, row); err != nil {
					r.err = fmt.Errorf("decode PSD ZIP composite: %w", err)
					return
				}
				if compression == 3 {
					for x := 1; x < width; x++ {
						row[x] += row[x-1]
					}
				}
				writeRow(target, row, y, component)
			}
		}
		if err := payload.finish(); err != nil {
			r.err = fmt.Errorf("finish PSD ZIP composite: %w", err)
			return
		}
		r.position = r.end
	default:
		r.fail("unsupported PSD composite compression %d", compression)
	}
	if p.globalAlpha {
		for offset := 0; offset < len(target.Pix); offset += 4 {
			alpha := target.Pix[offset+3]
			if alpha == 0 || alpha == 255 {
				continue
			}
			alphaValue := float64(alpha) / 255
			inverse := 1 / alphaValue
			inverseAlpha := 255 * (1 - inverse)
			for component := range 3 {
				value := float64(float64(target.Pix[offset+component])*inverse) + inverseAlpha
				target.Pix[offset+component] = uint8(max(0, min(255, math.RoundToEven(value))))
			}
		}
	}
}
