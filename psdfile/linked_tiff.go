package psdfile

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
)

// linkedTIFF decodes one classic, stripped RGB8 image without owning the encoded payload.
// Unsupported TIFF organizations never fall back to Photoshop-private previews or layer data.
type linkedTIFF struct {
	document *Document
	context  context.Context
	span     Span
	order    binary.ByteOrder
	fields   []linkedTIFFField
	working  int64
	options  LinkedRasterOptions
}

type linkedTIFFField struct {
	tag, kind uint16
	count     uint32
	data      Span
}

func (d *Document) decodeLinkedTIFF(
	ctx context.Context, record LinkedFileRecord, options LinkedRasterOptions,
) (*LinkedRaster, error) {
	decoder := linkedTIFF{document: d, context: ctx, span: record.Data, options: options}
	if err := decoder.directory(); err != nil {
		return nil, err
	}
	return decoder.decode()
}

func (decoder *linkedTIFF) failure(kind error, path string, offset int64, detail string) error {
	return fileError(kind, "linkedRaster.tiff", path, decoder.span.Offset+offset, detail)
}

func (decoder *linkedTIFF) read(data []byte, offset int64, path string) error {
	if err := decoder.context.Err(); err != nil {
		return err
	}
	if offset < 0 || offset > decoder.span.Length || int64(len(data)) > decoder.span.Length-offset {
		return decoder.failure(ErrMalformed, path, offset, "TIFF field is outside the embedded payload")
	}
	reader := io.NewSectionReader(strictRasterReaderAt{context: decoder.context, source: decoder.document},
		decoder.span.Offset+offset, int64(len(data)))
	_, err := io.ReadFull(reader, data)
	if err == nil {
		return nil
	}
	if cancellation := decoder.context.Err(); cancellation != nil {
		return cancellation
	}
	if errors.Is(err, ErrClosed) {
		return err
	}
	return &Error{Kind: ErrMalformed, Feature: "linkedRaster.tiff", Path: path, Offset: decoder.span.Offset + offset,
		Detail: "TIFF original bytes could not be read", Cause: err}
}

func (decoder *linkedTIFF) charge(amount int64, path string, offset int64) error {
	if amount < 0 || amount > decoder.options.MaxWorkingBytes-decoder.working || amount > math.MaxInt {
		return decoder.failure(ErrLimit, path, offset, "TIFF decoder and owned output exceed the working limit")
	}
	decoder.working += amount
	return nil
}

func (decoder *linkedTIFF) directory() error {
	var header [8]byte
	if err := decoder.read(header[:], 0, "header"); err != nil {
		return err
	}
	switch string(header[:2]) {
	case "II":
		decoder.order = binary.LittleEndian
	case "MM":
		decoder.order = binary.BigEndian
	default:
		return decoder.failure(ErrMalformed, "header", 0, "invalid TIFF byte order")
	}
	if decoder.order.Uint16(header[2:4]) == 43 {
		return decoder.failure(ErrUnsupported, "version", 2, "BigTIFF requires a 64-bit directory decoder")
	}
	if decoder.order.Uint16(header[2:4]) != 42 {
		return decoder.failure(ErrMalformed, "version", 2, "invalid classic TIFF version")
	}
	position := int64(decoder.order.Uint32(header[4:]))
	if position < 8 || position%2 != 0 {
		return decoder.failure(ErrMalformed, "directory", position, "invalid first TIFF directory offset")
	}
	var countBytes [2]byte
	if err := decoder.read(countBytes[:], position, "directory.count"); err != nil {
		return err
	}
	count := int(decoder.order.Uint16(countBytes[:]))
	if count == 0 {
		return decoder.failure(ErrMalformed, "directory.count", position, "TIFF directory has no fields")
	}
	if count > decoder.document.Limits().MaxBlocks {
		return decoder.failure(ErrLimit, "directory.count", position, "TIFF directory exceeds the field limit")
	}
	if int64(count)*12+6 > decoder.span.Length-position {
		return decoder.failure(ErrMalformed, "directory", position, "TIFF directory is truncated")
	}
	if err := decoder.charge(int64(count)*32+1024, "directory", position); err != nil {
		return err
	}
	decoder.fields = make([]linkedTIFFField, count)
	for index := range decoder.fields {
		var encoded [12]byte
		offset := position + 2 + int64(index)*12
		if err := decoder.read(encoded[:], offset, "directory.field"); err != nil {
			return err
		}
		field := &decoder.fields[index]
		field.tag, field.kind = decoder.order.Uint16(encoded[:2]), decoder.order.Uint16(encoded[2:4])
		field.count = decoder.order.Uint32(encoded[4:8])
		if index > 0 && field.tag <= decoder.fields[index-1].tag {
			return decoder.failure(ErrMalformed, "directory.tags", offset, "TIFF fields are duplicated or unsorted")
		}
		unit := linkedTIFFTypeSize(field.kind)
		if unit == 0 {
			return decoder.failure(ErrUnsupported, "directory.type", offset+2, "unknown TIFF field type")
		}
		if field.count == 0 {
			return decoder.failure(ErrMalformed, "directory.count", offset+4, "TIFF field has no values")
		}
		field.data.Length = int64(field.count) * unit
		field.data.Offset = offset + 8
		if field.data.Length > 4 {
			field.data.Offset = int64(decoder.order.Uint32(encoded[8:]))
			if field.data.Offset < 8 || field.data.Offset%2 != 0 {
				return decoder.failure(ErrMalformed, "directory.value", offset+8, "invalid TIFF value offset")
			}
		}
		if field.data.Offset > decoder.span.Length || field.data.Length > decoder.span.Length-field.data.Offset {
			return decoder.failure(ErrMalformed, "directory.value", offset+8, "TIFF value exceeds the payload")
		}
	}
	var next [4]byte
	if err := decoder.read(next[:], position+2+int64(count)*12, "directory.next"); err != nil {
		return err
	}
	if nextOffset := int64(decoder.order.Uint32(next[:])); nextOffset != 0 {
		if nextOffset < 8 || nextOffset%2 != 0 || nextOffset == position || nextOffset > decoder.span.Length-2 {
			return decoder.failure(ErrMalformed, "directory.next", nextOffset, "invalid next TIFF directory offset")
		}
		return decoder.failure(ErrUnsupported, "directory.next", nextOffset, "multiple TIFF images are not supported")
	}
	return nil
}

func linkedTIFFTypeSize(kind uint16) int64 {
	switch kind {
	case 1, 2, 6, 7:
		return 1
	case 3, 8:
		return 2
	case 4, 9, 11:
		return 4
	case 5, 10, 12:
		return 8
	default:
		return 0
	}
}

func (decoder *linkedTIFF) field(tag uint16) (linkedTIFFField, bool) {
	for _, field := range decoder.fields {
		if field.tag == tag {
			return field, true
		}
		if field.tag > tag {
			break
		}
	}
	return linkedTIFFField{}, false
}

func (decoder *linkedTIFF) integer(field linkedTIFFField, index uint32) (int64, error) {
	if index >= field.count || field.kind != 1 && field.kind != 3 && field.kind != 4 {
		return 0, decoder.failure(ErrMalformed, "field.type", field.data.Offset,
			"TIFF numeric field requires an unsigned integer type and the expected count")
	}
	var encoded [4]byte
	unit := linkedTIFFTypeSize(field.kind)
	if err := decoder.read(encoded[:unit], field.data.Offset+int64(index)*unit, "field.value"); err != nil {
		return 0, err
	}
	switch field.kind {
	case 1:
		return int64(encoded[0]), nil
	case 3:
		return int64(decoder.order.Uint16(encoded[:2])), nil
	default:
		return int64(decoder.order.Uint32(encoded[:])), nil
	}
}

func (decoder *linkedTIFF) scalar(tag uint16, fallback int64) (int64, error) {
	field, exists := decoder.field(tag)
	if !exists {
		if fallback < 0 {
			return 0, decoder.failure(ErrMalformed, fmt.Sprintf("tag.%d", tag), 0, "required TIFF field is absent")
		}
		return fallback, nil
	}
	if field.count != 1 {
		return 0, decoder.failure(ErrMalformed, fmt.Sprintf("tag.%d", tag), field.data.Offset,
			"TIFF scalar field has multiple values")
	}
	return decoder.integer(field, 0)
}

func (decoder *linkedTIFF) decode() (*LinkedRaster, error) {
	// These fields define a different image organization or pixel encoding.
	for _, tag := range []uint16{301, 318, 319, 322, 323, 324, 325, 330, 532, 37724} {
		if field, exists := decoder.field(tag); exists {
			return nil, decoder.failure(ErrUnsupported, fmt.Sprintf("tag.%d", tag), field.data.Offset,
				"TIFF colorimetry, tiles, subimages or Photoshop-private layers require a dedicated decoder")
		}
	}
	width, err := decoder.scalar(256, -1)
	if err != nil {
		return nil, err
	}
	height, err := decoder.scalar(257, -1)
	if err != nil {
		return nil, err
	}
	if width <= 0 || height <= 0 {
		return nil, decoder.failure(ErrMalformed, "dimensions", 0, "TIFF dimensions must be positive")
	}
	if width > decoder.options.MaxPixels/height || width > math.MaxInt/4/height {
		return nil, decoder.failure(ErrLimit, "dimensions", 0, "TIFF dimensions exceed the pixel or allocation limit")
	}
	photo, err := decoder.scalar(262, -1)
	if err != nil {
		return nil, err
	}
	if photo != 2 {
		return nil, decoder.failure(ErrUnsupported, "photometric", 0, "only RGB TIFF photometric encoding is supported")
	}
	samples, err := decoder.scalar(277, 1)
	if err != nil {
		return nil, err
	}
	if samples != 3 && samples != 4 {
		return nil, decoder.failure(ErrUnsupported, "samples", 0, "TIFF requires RGB or one explicit alpha component")
	}
	alpha, err := decoder.sampleEncoding(samples)
	if err != nil {
		return nil, err
	}
	for _, requirement := range []struct {
		tag      uint16
		fallback int64
		allowed  int64
	}{{254, 0, 0}, {255, 1, 1}, {266, 1, 1}, {274, 1, 1}, {284, 1, 1}, {317, 1, 1}} {
		value, err := decoder.scalar(requirement.tag, requirement.fallback)
		if err != nil {
			return nil, err
		}
		if value != requirement.allowed {
			return nil, decoder.failure(ErrUnsupported, fmt.Sprintf("tag.%d", requirement.tag), 0,
				"TIFF requires a full-resolution, top-left, chunky image without a predictor")
		}
	}
	compression, err := decoder.scalar(259, 1)
	if err != nil {
		return nil, err
	}
	if compression != 1 && compression != 32773 {
		return nil, decoder.failure(ErrUnsupported, "compression", 0,
			"only uncompressed and PackBits TIFF strips are supported")
	}
	rows, err := decoder.scalar(278, math.MaxUint32)
	if err != nil {
		return nil, err
	}
	if rows <= 0 {
		return nil, decoder.failure(ErrMalformed, "rowsPerStrip", 0, "TIFF strip height must be positive")
	}
	strips, err := decoder.strips(width*samples, height, rows, compression)
	if err != nil {
		return nil, err
	}
	if err := decoder.charge(width*height*4+width*samples+4096, "pixels", 0); err != nil {
		return nil, err
	}
	profile, err := decoder.profile()
	if err != nil {
		return nil, err
	}
	pixels := image.NewNRGBA(image.Rect(0, 0, int(width), int(height)))
	row := make([]byte, int(width*samples))
	for index, strip := range strips {
		reader := io.NewSectionReader(strictRasterReaderAt{context: decoder.context, source: decoder.document},
			decoder.span.Offset+strip.Offset, strip.Length)
		var packed *bufio.Reader
		if compression == 32773 {
			packed = bufio.NewReaderSize(reader, 4096)
		}
		first := int64(index) * rows
		for y := first; y < min(first+rows, height); y++ {
			if err := decoder.context.Err(); err != nil {
				return nil, err
			}
			if packed != nil {
				err = decodeLinkedTIFFPackBits(decoder.context, packed, row)
			} else {
				_, err = io.ReadFull(reader, row)
			}
			if err != nil {
				return nil, decoder.streamError(err, strip.Offset)
			}
			if err := decoder.renderRow(pixels.Pix[int(y)*pixels.Stride:], row, int(samples), alpha); err != nil {
				return nil, err
			}
		}
		if packed != nil {
			for {
				octet, err := packed.ReadByte()
				if err == io.EOF {
					break
				}
				if err != nil {
					return nil, decoder.streamError(err, strip.Offset)
				}
				if octet != 128 {
					return nil, decoder.failure(ErrMalformed, "strip", strip.Offset,
						"PackBits strip contains output after its declared rows")
				}
				if err := decoder.context.Err(); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := decoder.context.Err(); err != nil {
		return nil, err
	}
	return &LinkedRaster{Pixels: pixels, ICCProfile: profile, Format: "tiff", WorkingBytes: decoder.working}, nil
}

func (decoder *linkedTIFF) sampleEncoding(samples int64) (int64, error) {
	bits, exists := decoder.field(258)
	if !exists || int64(bits.count) != samples {
		return 0, decoder.failure(ErrMalformed, "bitsPerSample", bits.data.Offset,
			"TIFF BitsPerSample must describe every component")
	}
	for index := uint32(0); index < bits.count; index++ {
		value, err := decoder.integer(bits, index)
		if err != nil {
			return 0, err
		}
		if value != 8 {
			return 0, decoder.failure(ErrUnsupported, "bitsPerSample", bits.data.Offset,
				"only TIFF RGB8 component precision is supported")
		}
	}
	if format, exists := decoder.field(339); exists {
		if int64(format.count) != samples {
			return 0, decoder.failure(ErrMalformed, "sampleFormat", format.data.Offset,
				"TIFF SampleFormat must describe every component")
		}
		for index := uint32(0); index < format.count; index++ {
			value, err := decoder.integer(format, index)
			if err != nil {
				return 0, err
			}
			if value != 1 {
				return 0, decoder.failure(ErrUnsupported, "sampleFormat", format.data.Offset,
					"only unsigned TIFF samples are supported")
			}
		}
	}
	extra, exists := decoder.field(338)
	if samples == 3 {
		if exists {
			return 0, decoder.failure(ErrMalformed, "extraSamples", extra.data.Offset,
				"RGB TIFF has an unexpected extra component declaration")
		}
		return 0, nil
	}
	if !exists || extra.count != 1 {
		return 0, decoder.failure(ErrMalformed, "extraSamples", extra.data.Offset,
			"RGBA TIFF requires exactly one extra component declaration")
	}
	alpha, err := decoder.integer(extra, 0)
	if err != nil {
		return 0, err
	}
	if alpha != 1 && alpha != 2 {
		return 0, decoder.failure(ErrUnsupported, "extraSamples", extra.data.Offset,
			"unspecified TIFF extra samples cannot be treated as transparency")
	}
	return alpha, nil
}

func (decoder *linkedTIFF) strips(rowBytes, height, rows, compression int64) ([]Span, error) {
	offsets, present := decoder.field(273)
	counts, counted := decoder.field(279)
	count := (height-1)/rows + 1
	if !present || !counted || int64(offsets.count) != count || int64(counts.count) != count {
		return nil, decoder.failure(ErrMalformed, "strips", 0, "TIFF strip tables do not match the image height")
	}
	if err := decoder.charge(count*16, "strips", offsets.data.Offset); err != nil {
		return nil, err
	}
	strips := make([]Span, int(count))
	encodedWork := int64(0)
	for index := range strips {
		offset, err := decoder.integer(offsets, uint32(index))
		if err != nil {
			return nil, err
		}
		length, err := decoder.integer(counts, uint32(index))
		if err != nil {
			return nil, err
		}
		if offset < 8 || length <= 0 || offset > decoder.span.Length || length > decoder.span.Length-offset {
			return nil, decoder.failure(ErrMalformed, "strip", offset, "TIFF strip is outside the embedded payload")
		}
		if compression == 1 && length != rowBytes*min(rows, height-int64(index)*rows) {
			return nil, decoder.failure(ErrMalformed, "strip.byteCount", offset,
				"uncompressed TIFF strip length differs from its declared rows")
		}
		if length > decoder.options.MaxWorkingBytes-encodedWork {
			return nil, decoder.failure(ErrLimit, "strips.work", offset, "TIFF encoded strip work exceeds the byte limit")
		}
		encodedWork += length
		strips[index] = Span{Offset: offset, Length: length}
	}
	// The encoded work bound also limits arbitrarily long runs of PackBits no-op commands.
	if compression == 32773 {
		decoder.working = max(decoder.working, encodedWork)
	}
	return strips, nil
}

func (decoder *linkedTIFF) profile() ([]byte, error) {
	field, exists := decoder.field(34675)
	if !exists {
		return nil, nil
	}
	if field.kind != 7 || field.data.Length < 132 {
		return nil, decoder.failure(ErrMalformed, "icc", field.data.Offset,
			"TIFF ICC field requires a complete UNDEFINED profile")
	}
	if field.data.Length > decoder.options.MaxProfileBytes {
		return nil, decoder.failure(ErrLimit, "icc", field.data.Offset, "TIFF ICC profile exceeds its profile limit")
	}
	if err := decoder.charge(field.data.Length, "icc", field.data.Offset); err != nil {
		return nil, err
	}
	profile := make([]byte, int(field.data.Length))
	if err := decoder.read(profile, field.data.Offset, "icc"); err != nil {
		return nil, err
	}
	if int64(binary.BigEndian.Uint32(profile[:4])) != field.data.Length {
		return nil, decoder.failure(ErrMalformed, "icc", field.data.Offset,
			"TIFF ICC declared length differs from its field size")
	}
	return profile, nil
}

func (decoder *linkedTIFF) renderRow(destination, source []byte, samples int, alpha int64) error {
	for position := 0; position < len(source); position += samples {
		target := position / samples * 4
		if target%65536 == 0 {
			if err := decoder.context.Err(); err != nil {
				return err
			}
		}
		copy(destination[target:target+3], source[position:position+3])
		destination[target+3] = 255
		if samples == 3 {
			continue
		}
		opacity := uint32(source[position+3])
		destination[target+3] = byte(opacity)
		if alpha != 1 {
			continue
		}
		for component := range 3 {
			associated := uint32(source[position+component])
			if associated > opacity {
				return decoder.failure(ErrMalformed, "alpha", 0,
					"associated TIFF color exceeds its premultiplied opacity")
			}
			if opacity == 0 {
				destination[target+component] = 0
				continue
			}
			destination[target+component] = byte((associated*255 + opacity/2) / opacity)
		}
	}
	return nil
}

func (decoder *linkedTIFF) streamError(cause error, offset int64) error {
	if err := decoder.context.Err(); err != nil {
		return err
	}
	if errors.Is(cause, ErrClosed) {
		return cause
	}
	return &Error{Kind: ErrMalformed, Feature: "linkedRaster.tiff", Path: "strip", Offset: decoder.span.Offset + offset,
		Detail: "TIFF strip could not be decoded", Cause: cause}
}

func decodeLinkedTIFFPackBits(ctx context.Context, reader *bufio.Reader, output []byte) error {
	position := 0
	for position < len(output) {
		if err := ctx.Err(); err != nil {
			return err
		}
		octet, err := reader.ReadByte()
		if err != nil {
			return errors.Join(io.ErrUnexpectedEOF, err)
		}
		control := int(int8(octet))
		if control == -128 {
			continue
		}
		count := control + 1
		if control < 0 {
			count = 1 - control
		}
		if count > len(output)-position {
			return errors.New("TIFF PackBits packet crosses a row boundary")
		}
		if control >= 0 {
			if _, err := io.ReadFull(reader, output[position:position+count]); err != nil {
				return err
			}
		} else {
			value, err := reader.ReadByte()
			if err != nil {
				return errors.Join(io.ErrUnexpectedEOF, err)
			}
			for index := position; index < position+count; index++ {
				output[index] = value
			}
		}
		position += count
	}
	return nil
}
