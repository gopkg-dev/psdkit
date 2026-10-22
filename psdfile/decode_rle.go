package psdfile

import (
	"context"
	"encoding/binary"
	"io"
)

func (d *Document) decodeRLE(
	ctx context.Context, span Span, shape decodeShape, outputs [][]byte, countWidth uint8, path string,
) error {
	if countWidth != 2 && countWidth != 4 {
		return fileError(ErrMalformed, "pixels.rle", path, span.Offset, "invalid RLE row-count width")
	}
	rows := uint64(shape.height) * uint64(len(outputs))
	if rows > uint64(span.Length)/uint64(countWidth) {
		return fileError(ErrMalformed, "pixels.rle", path, span.Offset, "RLE row-count table is truncated")
	}
	tableLength := int64(rows * uint64(countWidth))
	payloadLength := span.Length - tableLength
	table := d.decodeCursor(ctx, Span{Offset: span.Offset, Length: tableLength})
	var total int64
	for range rows {
		count, err := decodeRLECount(table, countWidth, path)
		if err != nil {
			return err
		}
		if int64(count) > payloadLength-total {
			return fileError(ErrMalformed, "pixels.rle", path, table.position(), "RLE rows exceed the channel payload")
		}
		total += int64(count)
	}
	if total != payloadLength && (total%2 != 1 || payloadLength-total != 1) {
		return fileError(ErrMalformed, "pixels.rle", path, table.position(), "RLE payload has unassigned bytes")
	}
	table = d.decodeCursor(ctx, Span{Offset: span.Offset, Length: tableLength})
	payloadOffset := span.Offset + tableLength
	for _, data := range outputs {
		for row := range shape.height {
			if err := ctx.Err(); err != nil {
				return err
			}
			count, err := decodeRLECount(table, countWidth, path)
			if err != nil {
				return err
			}
			if data != nil {
				cursor := d.decodeCursor(ctx, Span{Offset: payloadOffset, Length: int64(count)})
				rowData := data[row*shape.rowBytes : (row+1)*shape.rowBytes]
				if err := decodePackBits(cursor, rowData, path); err != nil {
					return err
				}
			}
			payloadOffset += int64(count)
		}
	}
	return nil
}

func decodeRLECount(cursor *decodeCursor, width uint8, path string) (uint32, error) {
	var data [4]byte
	if _, err := io.ReadFull(cursor, data[:width]); err != nil {
		return 0, decodeReadError(err, "pixels.rle", path, cursor.position(), "RLE row length could not be read")
	}
	if width == 2 {
		return uint32(binary.BigEndian.Uint16(data[:2])), nil
	}
	return binary.BigEndian.Uint32(data[:]), nil
}

func decodePackBits(cursor *decodeCursor, output []byte, path string) error {
	position := 0
	for cursor.read < cursor.span.Length {
		control, err := cursor.ReadByte()
		if err != nil {
			return decodeReadError(err, "pixels.rle", path, cursor.position(), "PackBits control byte could not be read")
		}
		value := int(int8(control))
		if value == -128 {
			continue
		}
		length := value + 1
		if value < 0 {
			length = 1 - value
		}
		if length > len(output)-position {
			return fileError(ErrMalformed, "pixels.rle", path, cursor.position(), "PackBits run exceeds the row width")
		}
		if value >= 0 {
			if _, err := io.ReadFull(cursor, output[position:position+length]); err != nil {
				return decodeReadError(err, "pixels.rle", path, cursor.position(), "PackBits literal run is truncated")
			}
		} else {
			repeated, err := cursor.ReadByte()
			if err != nil {
				return decodeReadError(err, "pixels.rle", path, cursor.position(), "PackBits repeated byte is missing")
			}
			for index := range length {
				output[position+index] = repeated
			}
		}
		position += length
	}
	if position != len(output) {
		return fileError(ErrMalformed, "pixels.rle", path, cursor.position(), "PackBits row is shorter than its width")
	}
	return nil
}
