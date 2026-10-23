package psdedit

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"image"
	"testing"
)

// smartDeflateTIFF writes two independent strips with horizontal prediction from the TIFF specification.
func smartDeflateTIFF(
	t *testing.T, pixels *image.NRGBA, order binary.ByteOrder, compression uint16, alpha bool,
) []byte {
	t.Helper()
	samples, count := 3, 10
	if alpha {
		samples, count = 4, 11
	}
	bitsOffset := 8 + 2 + count*12 + 4
	offsetsOffset := bitsOffset + samples*2
	countsOffset := offsetsOffset + 8
	data := make([]byte, countsOffset+8)
	copy(data, "II")
	if order == binary.BigEndian {
		copy(data, "MM")
	}
	order.PutUint16(data[2:], 42)
	order.PutUint32(data[4:], 8)
	order.PutUint16(data[8:], uint16(count))
	entries := [][4]uint32{
		{256, 4, 1, 2}, {257, 4, 1, 2}, {258, 3, uint32(samples), uint32(bitsOffset)},
		{259, 3, 1, uint32(compression)}, {262, 3, 1, 2}, {273, 4, 2, uint32(offsetsOffset)},
		{277, 3, 1, uint32(samples)}, {278, 4, 1, 1}, {279, 4, 2, uint32(countsOffset)}, {317, 3, 1, 2},
	}
	if alpha {
		entries = append(entries, [4]uint32{338, 3, 1, 2})
	}
	for index, entry := range entries {
		position := 10 + index*12
		order.PutUint16(data[position:], uint16(entry[0]))
		order.PutUint16(data[position+2:], uint16(entry[1]))
		order.PutUint32(data[position+4:], entry[2])
		if entry[1] == 3 && entry[2] == 1 {
			order.PutUint16(data[position+8:], uint16(entry[3]))
		} else {
			order.PutUint32(data[position+8:], entry[3])
		}
	}
	for component := range samples {
		order.PutUint16(data[bitsOffset+component*2:], 8)
	}
	for y := range 2 {
		var row []byte
		for x := range 2 {
			pixel := pixels.NRGBAAt(x, y)
			row = append(row, pixel.R, pixel.G, pixel.B)
			if alpha {
				row = append(row, pixel.A)
			}
		}
		for index := len(row) - 1; index >= samples; index-- {
			row[index] -= row[index-samples]
		}
		var compressed bytes.Buffer
		writer := zlib.NewWriter(&compressed)
		if _, err := writer.Write(row); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if len(data)%2 != 0 {
			data = append(data, 0)
		}
		order.PutUint32(data[offsetsOffset+y*4:], uint32(len(data)))
		order.PutUint32(data[countsOffset+y*4:], uint32(compressed.Len()))
		data = append(data, compressed.Bytes()...)
	}
	return data
}

func TestSmartContentsDeflateTIFFReplaceSaveReopenPreservesOriginalBytes(t *testing.T) {
	for _, parent := range []uint16{1, 2} {
		for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
			for _, compression := range []uint16{8, 32946} {
				for _, alpha := range []bool{false, true} {
					for savedCompression := range uint16(4) {
						t.Run(fmt.Sprintf("parent%d/%s/tiff%d/alpha%t/save%d",
							parent, order, compression, alpha, savedCompression), func(t *testing.T) {
							pixels := smartFormatsPixels(2, 2, alpha)
							encoded := smartDeflateTIFF(t, pixels, order, compression, alpha)
							immutable := bytes.Clone(encoded)
							editor, original := smartFormatsBaseline(t, parent, false)
							objects, err := editor.SmartObjects(t.Context())
							if err != nil {
								t.Fatal(err)
							}
							if err := editor.ReplaceContents(t.Context(), objects[1].ID, encoded); err != nil {
								t.Fatal(err)
							}
							clear(encoded)
							smartEditAssertCaches(t, editor, pixels)
							reopened, saved := smartFormatsSaveOpen(t, editor, savedCompression)
							record := smartFormatsAssertRecord(t, saved, "liFD", "TIFF", immutable)
							smartFormatsAssertSavedPixels(t, saved, pixels)
							smartEditAssertPreservedMetadata(t, original, saved)
							decoded, err := saved.DecodeLinkedRaster(t.Context(), record)
							if err != nil || !bytes.Equal(decoded.Pixels.Pix, pixels.Pix) {
								t.Fatalf("reopened TIFF content changed: %v", err)
							}
							views, err := reopened.SmartObjects(t.Context())
							if err != nil || len(views) != 2 || len(views[0].SharedInstances) != 2 {
								t.Fatalf("shared source did not survive reopen: %v", err)
							}
							if err := reopened.ReplaceContents(t.Context(), views[0].ID, immutable); err != nil {
								t.Fatalf("reopened TIFF source cannot be edited again: %v", err)
							}
							_, second := smartFormatsSaveOpen(t, reopened, savedCompression)
							smartFormatsAssertRecord(t, second, "liFD", "TIFF", immutable)
						})
					}
				}
			}
		}
	}
}
