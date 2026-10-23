package psdfile

import (
	"bufio"
	"compress/zlib"
	"image"
	"io"
)

func (decoder *linkedTIFF) decodeStrip(
	strip Span, pixels *image.NRGBA, row []byte, first, end, compression, predictor, samples, alpha int64,
) error {
	section := io.NewSectionReader(strictRasterReaderAt{context: decoder.context, source: decoder.document},
		decoder.span.Offset+strip.Offset, strip.Length)
	var reader io.Reader = section
	var packed, compressed *bufio.Reader
	var inflated io.ReadCloser
	var lzw *linkedTIFFLZW
	if compression == 32773 {
		packed = bufio.NewReaderSize(section, 4096)
	}
	if compression == 5 {
		lzw = &linkedTIFFLZW{context: decoder.context, reader: bufio.NewReaderSize(section, 4096)}
		reader = lzw
	}
	if compression == 8 || compression == 32946 {
		// ByteReader prevents zlib from consuming unvalidated bytes after its single stream.
		compressed = bufio.NewReaderSize(section, 4096)
		header, err := compressed.Peek(2)
		if err != nil {
			return decoder.streamError(err, strip.Offset)
		}
		// Adobe TIFF Technical Notes forbids FDICT even when its dictionary happens to be empty.
		if header[1]&0x20 != 0 {
			return decoder.failure(ErrMalformed, "strip", strip.Offset,
				"Deflate TIFF strips cannot request a preset dictionary")
		}
		inflated, err = zlib.NewReader(compressed)
		if err != nil {
			return decoder.streamError(err, strip.Offset)
		}
		defer func() { _ = inflated.Close() }()
		reader = inflated
	}
	for y := first; y < end; y++ {
		if err := decoder.context.Err(); err != nil {
			return err
		}
		var err error
		if packed != nil {
			err = decodeLinkedTIFFPackBits(decoder.context, packed, row)
		} else {
			_, err = io.ReadFull(reader, row)
		}
		if err != nil {
			return decoder.streamError(err, strip.Offset)
		}
		if predictor == 2 {
			// TIFF6 section 14 differences each component against the previous pixel in the same row.
			for index := int(samples); index < len(row); index++ {
				if index%65536 == 0 {
					if err := decoder.context.Err(); err != nil {
						return err
					}
				}
				row[index] += row[index-int(samples)]
			}
		}
		if err := decoder.renderRow(pixels.Pix[int(y)*pixels.Stride:], row, int(samples), alpha); err != nil {
			return err
		}
	}
	if inflated != nil {
		var extra [1]byte
		count, err := io.ReadFull(inflated, extra[:])
		if count != 0 {
			return decoder.failure(ErrMalformed, "strip", strip.Offset,
				"Deflate strip contains output after its declared rows")
		}
		if err != io.EOF { //nolint:errorlint // Only bare EOF validates the terminator; source faults must survive.
			return decoder.streamError(err, strip.Offset)
		}
		position, err := section.Seek(0, io.SeekCurrent)
		if err != nil {
			return decoder.streamError(err, strip.Offset)
		}
		if position-int64(compressed.Buffered()) != strip.Length {
			return decoder.failure(ErrMalformed, "strip", strip.Offset,
				"bytes follow the complete Deflate strip stream")
		}
	}
	if lzw != nil {
		var extra [1]byte
		count, err := io.ReadFull(lzw, extra[:])
		if count != 0 {
			return decoder.failure(ErrMalformed, "strip", strip.Offset,
				"LZW strip contains output after its declared rows")
		}
		if err != io.EOF { //nolint:errorlint // Only bare EOF proves that the LZW EOI code was reached.
			return decoder.streamError(err, strip.Offset)
		}
		if err := lzw.finish(); err != nil {
			return decoder.streamError(err, strip.Offset)
		}
	}
	if packed != nil {
		return decoder.finishPackBits(packed, strip.Offset)
	}
	return nil
}

func (decoder *linkedTIFF) finishPackBits(reader *bufio.Reader, offset int64) error {
	for {
		if err := decoder.context.Err(); err != nil {
			return err
		}
		octet, err := reader.ReadByte()
		if err == io.EOF { //nolint:errorlint // A joined EOF may carry a source fault.
			return nil
		}
		if err != nil {
			return decoder.streamError(err, offset)
		}
		if octet != 128 {
			return decoder.failure(ErrMalformed, "strip", offset,
				"PackBits strip contains output after its declared rows")
		}
	}
}
