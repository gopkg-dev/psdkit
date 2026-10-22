package psdfile

import (
	"bufio"
	"compress/zlib"
	"context"
	"errors"
	"io"
)

func (d *Document) decodeRegionZIP(
	ctx context.Context, span Span, region regionDecode, channels, channelIndex int,
	prediction bool, data []byte, path string,
) error {
	cursor := d.regionCursor(ctx, span)
	buffered := bufio.NewReaderSize(cursor, region.streamBuffer)
	reader, err := zlib.NewReader(buffered)
	if err != nil {
		return decodeReadError(err, "pixels.zip", path, cursor.position(), "invalid zlib stream header")
	}
	defer func() { _ = reader.Close() }()
	rowData := make([]byte, region.source.rowBytes)
	var shuffle []byte
	if prediction && region.source.depth == 32 {
		shuffle = make([]byte, region.source.rowBytes)
	}
	for channel := range channels {
		for row := range region.source.height {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := io.ReadFull(reader, rowData); err != nil {
				return decodeReadError(err, "pixels.zip", path, cursor.position(), "compressed samples are truncated")
			}
			if prediction {
				decodePredictionRow(rowData, shuffle, region.source.depth)
			}
			if channel == channelIndex && row >= region.rectangle.Min.Y && row < region.rectangle.Max.Y {
				start := (row - region.rectangle.Min.Y) * region.shape.rowBytes
				output := data[start : start+region.shape.rowBytes]
				region.copyWindow(output, rowData[region.windowStart:region.windowStart+region.windowLen])
			}
		}
	}
	var extra [1]byte
	count, err := io.ReadFull(reader, extra[:])
	if count != 0 {
		return fileError(ErrMalformed, "pixels.zip", path, cursor.position(), "zlib output exceeds sample dimensions")
	}
	if !errors.Is(err, io.EOF) {
		return decodeReadError(err, "pixels.zip", path, cursor.position(), "zlib checksum or stream terminator is invalid")
	}
	consumed := cursor.read - int64(buffered.Buffered())
	if consumed != span.Length {
		return fileError(ErrMalformed, "pixels.zip", path, span.Offset+consumed, "extra bytes follow the zlib stream")
	}
	return nil
}
