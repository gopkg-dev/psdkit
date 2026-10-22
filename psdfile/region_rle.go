package psdfile

import (
	"context"
	"io"
)

func (d *Document) decodeRegionRLE(
	ctx context.Context, span Span, region regionDecode, countWidth uint8,
	channels, channelIndex int, data []byte, path string,
) error {
	if int64(region.source.height) > span.Length/int64(countWidth)/int64(channels) {
		return fileError(ErrMalformed, "pixels.rle", path, span.Offset, "RLE row-count table is truncated")
	}
	rows := int64(region.source.height) * int64(channels)
	tableLength := rows * int64(countWidth)
	payloadLength := span.Length - tableLength
	firstRow := int64(channelIndex)*int64(region.source.height) + int64(region.rectangle.Min.Y)
	lastRow := firstRow + int64(region.shape.height)
	counts := make([]uint32, region.shape.height)
	table := d.regionCursor(ctx, Span{Offset: span.Offset, Length: tableLength})
	var total, firstOffset int64
	remainingWork := region.maxRLEWorkBytes - tableLength
	for row := range rows {
		count, err := decodeRLECount(table, countWidth, path)
		if err != nil {
			return err
		}
		if int64(count) > payloadLength-total {
			return fileError(ErrMalformed, "pixels.rle", path, table.position(), "RLE rows exceed the channel payload")
		}
		if row == firstRow {
			firstOffset = total
		}
		if row >= firstRow && row < lastRow {
			if int64(count) > remainingWork {
				return fileError(ErrLimit, "pixels.region", path, table.position(),
					"selected RLE rows exceed MaxRLEWorkBytes")
			}
			remainingWork -= int64(count)
			counts[row-firstRow] = count
		}
		total += int64(count)
	}
	if total != payloadLength && (total%2 != 1 || payloadLength-total != 1) {
		return fileError(ErrMalformed, "pixels.rle", path, table.position(), "RLE payload has unassigned bytes")
	}
	var window []byte
	if region.source.depth == 1 && region.rectangle.Min.X%8 != 0 {
		window = make([]byte, region.windowLen)
	}
	payloadOffset := span.Offset + tableLength + firstOffset
	for row, count := range counts {
		output := data[row*region.shape.rowBytes : (row+1)*region.shape.rowBytes]
		rowData := output
		if window != nil {
			rowData = window
		}
		cursor := d.regionCursor(ctx, Span{Offset: payloadOffset, Length: int64(count)})
		if err := decodePackBitsWindow(cursor, region.source.rowBytes, region.windowStart, rowData, path); err != nil {
			return err
		}
		region.copyWindow(output, rowData)
		payloadOffset += int64(count)
	}
	return nil
}

func decodePackBitsWindow(cursor *decodeCursor, rowBytes, windowStart int, output []byte, path string) error {
	position := 0
	windowEnd := windowStart + len(output)
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
		if length > rowBytes-position {
			return fileError(ErrMalformed, "pixels.rle", path, cursor.position(), "PackBits run exceeds the row width")
		}
		start, end := max(position, windowStart), min(position+length, windowEnd)
		if value >= 0 {
			if int64(length) > cursor.span.Length-cursor.read {
				return fileError(ErrMalformed, "pixels.rle", path, cursor.position(), "PackBits literal run is truncated")
			}
			if start < end {
				if err := cursor.skipRegionBytes(int64(start - position)); err != nil {
					return err
				}
				if _, err := io.ReadFull(cursor, output[start-windowStart:end-windowStart]); err != nil {
					return decodeReadError(err, "pixels.rle", path, cursor.position(), "PackBits region literal could not be read")
				}
				if err := cursor.skipRegionBytes(int64(position + length - end)); err != nil {
					return err
				}
			} else if err := cursor.skipRegionBytes(int64(length)); err != nil {
				return err
			}
		} else {
			repeated, err := cursor.ReadByte()
			if err != nil {
				return decodeReadError(err, "pixels.rle", path, cursor.position(), "PackBits repeated byte is missing")
			}
			for index := start; index < end; index++ {
				output[index-windowStart] = repeated
			}
		}
		position += length
	}
	if position != rowBytes {
		return fileError(ErrMalformed, "pixels.rle", path, cursor.position(), "PackBits row is shorter than its width")
	}
	return cursor.ctx.Err()
}

func (cursor *decodeCursor) skipRegionBytes(count int64) error {
	if err := cursor.ctx.Err(); err != nil {
		return err
	}
	if count < 0 || count > cursor.span.Length-cursor.read {
		return io.ErrUnexpectedEOF
	}
	if _, err := cursor.reader.Seek(count, io.SeekCurrent); err != nil {
		return err
	}
	cursor.read += count
	return nil
}
