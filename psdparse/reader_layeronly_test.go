package psdparse

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"
)

var errLayerOnlyFooterRead = errors.New("saved composite must not be read")

type layerOnlyBoundaryReader struct {
	data                  []byte
	size, forbiddenOffset int64
	footerReads, calls    int
	completeError         error
	shortCount            bool
}

func (reader *layerOnlyBoundaryReader) ReadAt(target []byte, offset int64) (int, error) {
	reader.calls++
	if int64(len(target)) != 0 && offset+int64(len(target)) > reader.forbiddenOffset {
		reader.footerReads++
		return 0, errLayerOnlyFooterRead
	}
	if offset < 0 || offset >= reader.size {
		return 0, io.EOF
	}
	count := min(int64(len(target)), reader.size-offset)
	clear(target[:count])
	if offset < int64(len(reader.data)) {
		copy(target[:count], reader.data[offset:])
	}
	if reader.shortCount && count > 0 {
		return int(count) - 1, nil
	}
	if reader.completeError != nil {
		return int(count), reader.completeError
	}
	if count != int64(len(target)) {
		return int(count), io.EOF
	}
	return int(count), nil
}

func layerOnlyFooterOffset(data []byte) int64 {
	position := 26
	for range 2 {
		position += 4 + int(binary.BigEndian.Uint32(data[position:]))
	}
	if binary.BigEndian.Uint16(data[4:]) == 2 {
		return int64(position+8) + int64(binary.BigEndian.Uint64(data[position:]))
	}
	return int64(position+4) + int64(binary.BigEndian.Uint32(data[position:]))
}

func layerOnlyUnknownResource(data []byte) []byte {
	payload := bytes.Repeat([]byte{0x6d}, 65539)
	resource := append([]byte("8BIM"), 0x13, 0x87, 0, 0)
	resource = binary.BigEndian.AppendUint32(resource, uint32(len(payload)))
	resource = append(resource, payload...)
	resource = append(resource, 0)
	output := bytes.Clone(data[:30])
	output = binary.BigEndian.AppendUint32(output, uint32(len(resource)))
	output = append(output, resource...)
	return append(output, data[34:]...)
}

func TestParseLayerReaderNeverPrefetchesSavedCompositePSDPSB(t *testing.T) {
	expected := []byte{20, 10, 1, 255, 40, 30, 2, 128, 60, 50, 3, 64, 80, 70, 4, 0}
	for _, version := range []uint16{1, 2} {
		for _, largeResource := range []bool{false, true} {
			for _, largeFooter := range []bool{false, true} {
				name := fmt.Sprintf("version%d-resource%t-footer%t", version, largeResource, largeFooter)
				t.Run(name, func(t *testing.T) {
					data := semanticsDocument(version, nil, nil, semanticsBlock{"pRsv", []byte{0x31, 0x57, 0x99}})
					if largeResource {
						data = layerOnlyUnknownResource(data)
					}
					reader := &layerOnlyBoundaryReader{
						data: data, size: int64(len(data)), forbiddenOffset: layerOnlyFooterOffset(data),
					}
					if largeFooter {
						reader.size = 128 << 20
					}
					document, err := ParseLayerReader(reader, reader.size)
					if err != nil || reader.footerReads != 0 || len(document.Layers) != 1 || document.Composite != nil ||
						!bytes.Equal(document.Layers[0].Pixels.Pix, expected) {
						t.Fatalf("layer-only read crossed saved footer or changed layer bytes: %v hits=%d", err, reader.footerReads)
					}
				})
			}
		}
	}
}

func TestParseLayerReaderReadAtEOFAndOriginalErrorCause(t *testing.T) {
	data := testPSD(0, false)
	footer := layerOnlyFooterOffset(data)
	reader := &layerOnlyBoundaryReader{data: data, size: int64(len(data)), forbiddenOffset: footer, completeError: io.EOF}
	if _, err := ParseLayerReader(reader, reader.size); err != nil || reader.footerReads != 0 {
		t.Fatalf("full count plus EOF changed successful layer-only decode: %v", err)
	}
	probe := errors.New("private ReaderAt failure")
	reader = &layerOnlyBoundaryReader{data: data, size: int64(len(data)), forbiddenOffset: footer, completeError: probe}
	if _, err := ParseLayerReader(reader, reader.size); !errors.Is(err, probe) {
		t.Fatalf("full count plus non-EOF error lost the actual cause: %v", err)
	}
	reader = &layerOnlyBoundaryReader{data: data, size: int64(len(data)), forbiddenOffset: footer, shortCount: true}
	if _, err := ParseLayerReader(reader, reader.size); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short successful count became a partial document: %v", err)
	}
}

func TestParseLayerReaderTruncationAndPreviewPathRemainStrict(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		data := layerOnlyUnknownResource(semanticsDocument(version, nil, nil))
		footer := layerOnlyFooterOffset(data)
		for _, length := range []int{25, 29, 33, 47, int(footer) - 1} {
			if _, err := ParseLayerReader(bytes.NewReader(data[:length]), int64(length)); err == nil {
				t.Fatalf("version%d truncated resource/layer section length%d returned a partial document", version, length)
			}
		}
		reader := &layerOnlyBoundaryReader{data: data, size: int64(len(data)), forbiddenOffset: footer}
		if _, err := ParseReader(reader, reader.size); !errors.Is(err, errLayerOnlyFooterRead) || reader.footerReads == 0 {
			t.Fatalf("with-preview parser silently changed its existing footer path: %v", err)
		}
		full, err := ParseReader(bytes.NewReader(data), int64(len(data)))
		if err != nil || full.Composite == nil || len(full.Composite.Pix) != 32*32*4 {
			t.Fatalf("version%d ordinary with-preview decode changed: %v", version, err)
		}
	}
}
