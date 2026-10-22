package psdparse

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"strings"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

type psbWriter struct{ testWriter }

func (w *psbWriter) u64(value uint64) {
	_ = binary.Write(&w.Buffer, binary.BigEndian, value)
}

func (w *psbWriter) length(version uint16, length int) {
	if version == 2 {
		w.u64(uint64(length))
		return
	}
	w.u32(uint32(length))
}

func versionedPixels(version, compression uint16, planes [][]byte) []byte {
	w := &psbWriter{}
	w.u16(compression)
	switch compression {
	case 0:
		for _, plane := range planes {
			w.Write(plane)
		}
	case 1:
		for range planes {
			for range 2 {
				if version == 2 {
					w.u32(3)
				} else {
					w.u16(3)
				}
			}
		}
		for _, plane := range planes {
			w.Write([]byte{1, plane[0], plane[1], 1, plane[2], plane[3]})
		}
	case 2, 3:
		compressed := zlib.NewWriter(&w.Buffer)
		for _, plane := range planes {
			values := bytes.Clone(plane)
			if compression == 3 {
				values[1] -= values[0]
				values[3] -= values[2]
			}
			_, _ = compressed.Write(values)
		}
		_ = compressed.Close()
	}
	return w.Bytes()
}

func versionedDocument(version, layerCompression, compositeCompression uint16, globalAlpha bool) []byte {
	return versionedDocumentWithChannelTail(version, layerCompression, compositeCompression, globalAlpha, nil)
}

func versionedDocumentWithChannelTail(
	version, layerCompression, compositeCompression uint16,
	globalAlpha bool,
	channelTail []byte,
) []byte {
	planes := [][]byte{
		{20, 40, 60, 80}, {10, 30, 50, 70}, {1, 2, 3, 4}, {255, 128, 64, 0}, {0, 80, 160, 255},
	}
	channels := make([][]byte, len(planes))
	for index, values := range planes {
		channels[index] = versionedPixels(version, layerCompression, [][]byte{values})
		channels[index] = append(channels[index], channelTail...)
	}
	record := &psbWriter{}
	record.u32(0)
	record.u32(0)
	record.u32(2)
	record.u32(2)
	record.u16(uint16(len(channels)))
	ids := []uint16{0, 1, 2, 65535, 65534}
	for index, channel := range channels {
		record.u16(ids[index])
		record.length(version, len(channel))
	}
	record.WriteString("8BIMnorm")
	record.Write([]byte{128, 0, 0, 0})
	extra := &psbWriter{}
	extra.u32(20)
	extra.u32(0)
	extra.u32(0)
	extra.u32(2)
	extra.u32(2)
	extra.Write([]byte{128, 0, 0, 0})
	extra.block([]byte{0, 0, 255, 255, 0, 0, 255, 255})
	extra.Write([]byte{1, 'L', 0, 0})
	record.block(extra.Bytes())
	info := &psbWriter{}
	if globalAlpha {
		info.u16(65535)
	} else {
		info.u16(1)
	}
	info.Write(record.Bytes())
	for _, channel := range channels {
		info.Write(channel)
	}
	if info.Len()%2 != 0 {
		info.u8(0)
	}
	section := &psbWriter{}
	section.length(version, info.Len())
	section.Write(info.Bytes())
	section.u32(0)
	w := &psbWriter{}
	w.WriteString("8BPS")
	w.u16(version)
	w.Write(make([]byte, 6))
	w.u16(4)
	w.u32(2)
	w.u32(2)
	w.u16(8)
	w.u16(3)
	w.u32(0)
	w.u32(0)
	w.length(version, section.Len())
	w.Write(section.Bytes())
	w.Write(versionedPixels(version, compositeCompression, planes[:4]))
	return w.Bytes()
}

func TestPSBCompressionMatchesPSD(t *testing.T) {
	for compression := uint16(0); compression <= 3; compression++ {
		for _, globalAlpha := range []bool{false, true} {
			t.Run(fmt.Sprintf("compression_%d/alpha_%t", compression, globalAlpha), func(t *testing.T) {
				psd, err := ParseBytes(versionedDocument(1, compression, compression, globalAlpha))
				if err != nil {
					t.Fatal(err)
				}
				psb, err := ParseBytes(versionedDocument(2, compression, compression, globalAlpha))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(psd.Composite.Pix, psb.Composite.Pix) {
					t.Fatalf("composite PSD=%v PSB=%v", psd.Composite.Pix, psb.Composite.Pix)
				}
				if !bytes.Equal(psd.Layers[0].Pixels.Pix, psb.Layers[0].Pixels.Pix) {
					t.Fatal("PSD and PSB layer pixels differ")
				}
				if !bytes.Equal(psd.Layers[0].Mask.Pixels.Pix, psb.Layers[0].Mask.Pixels.Pix) {
					t.Fatal("PSD and PSB mask pixels differ")
				}
				oracle, err := ParseBytes(versionedDocument(1, 0, 0, globalAlpha))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(psb.Composite.Pix, oracle.Composite.Pix) {
					t.Fatalf("PSB composite differs from raw PSD: %v", psb.Composite.Pix)
				}
			})
		}
	}
}

func testPSBCursor(data []byte) *cursor {
	return &cursor{source: &source{reader: bytes.NewReader(data), size: int64(len(data))}, end: int64(len(data))}
}

func TestPSBLength64Validation(t *testing.T) {
	for _, data := range [][]byte{
		{0, 0, 0, 0, 0, 0, 0},
		{128, 0, 0, 0, 0, 0, 0, 0},
		{255, 255, 255, 255, 255, 255, 255, 255},
		{0, 0, 0, 0, 0, 0, 0, 1},
	} {
		r := testPSBCursor(data)
		checkedLength64(r)
		if r.err == nil {
			t.Fatalf("invalid 64-bit length accepted: %x", data)
		}
	}
	valid := versionedDocument(2, 0, 0, false)
	for _, offset := range []int{34, 42, 72} {
		for _, length := range []uint64{math.MaxUint64, math.MaxInt64, uint64(len(valid) + 1)} {
			t.Run(fmt.Sprintf("offset_%d/length_%d", offset, length), func(t *testing.T) {
				data := bytes.Clone(valid)
				binary.BigEndian.PutUint64(data[offset:], length)
				if _, err := ParseBytes(data); err == nil {
					t.Fatal("out-of-bounds PSB section accepted")
				}
			})
		}
	}
}

func TestPSBAdditionalLengthRules(t *testing.T) {
	keys := []string{
		"LMsk", "Lr16", "Lr32", "Layr", "Mt16", "Mt32", "Mtrn", "Alph", "FMsk", "lnk2",
		"FEid", "FXid", "PxSD",
	}
	for _, version := range []uint16{1, 2} {
		for _, signature := range []string{"8BIM", "8B64"} {
			for _, key := range append(keys, "zzzz") {
				t.Run(fmt.Sprintf("v%d/%s/%s", version, signature, key), func(t *testing.T) {
					w := &psbWriter{}
					w.WriteString(signature + key)
					if signature == "8B64" || (version == 2 && key != "zzzz") {
						w.u64(0)
					} else {
						w.u32(0)
					}
					w.WriteString("8BIMluni")
					w.u32(6)
					w.u32(1)
					w.u16('X')
					r := testPSBCursor(w.Bytes())
					record := &layerRecord{layer: &psdmodel.Layer{}}
					p := &parser{version: version, document: &psdmodel.Document{}}
					p.additional(r, record)
					if r.err != nil {
						t.Fatal(r.err)
					}
					if r.remaining() != 0 || record.layer.Name != "X" {
						t.Fatalf("additional blocks misaligned: remaining=%d name=%q", r.remaining(), record.layer.Name)
					}
				})
			}
		}
	}
	for _, length := range []uint64{math.MaxUint64, 1 << 40} {
		w := &psbWriter{}
		w.WriteString("8B64zzzz")
		w.u64(length)
		r := testPSBCursor(w.Bytes())
		p := &parser{version: 2, document: &psdmodel.Document{}}
		p.additional(r, nil)
		if r.err == nil {
			t.Fatal("invalid additional 64-bit length accepted")
		}
	}
	r := testPSBCursor([]byte("8B64zzzz\x00\x00\x00\x00"))
	(&parser{version: 2, document: &psdmodel.Document{}}).additional(r, nil)
	if r.err == nil {
		t.Fatal("truncated additional 64-bit length accepted")
	}
}

func TestPSBRLELengthValidation(t *testing.T) {
	for _, data := range [][]byte{
		{0, 1, 0, 0, 0},
		{0, 1, 0, 0, 0, 3, 0, 0, 0, 3, 1, 20, 40},
		{0, 1, 255, 255, 255, 255, 0, 0, 0, 0},
	} {
		r := testPSBCursor(data)
		(&parser{version: 2}).decodePlane(r, freshImage(image.Rect(0, 0, 2, 2)), 0)
		if r.err == nil {
			t.Fatalf("malformed PSB RLE accepted: %x", data)
		}
	}
}

func TestPSBZIPDimensionAndChecksumValidation(t *testing.T) {
	for _, composite := range []bool{false, true} {
		expected := 4
		if composite {
			expected = 12
		}
		for _, kind := range []string{"short", "excess", "checksum", "truncated"} {
			t.Run(fmt.Sprintf("composite_%t/%s", composite, kind), func(t *testing.T) {
				count := expected
				switch kind {
				case "short":
					count--
				case "excess":
					count++
				}
				w := &psbWriter{}
				w.u16(2)
				compressed := zlib.NewWriter(&w.Buffer)
				_, _ = compressed.Write(make([]byte, count))
				_ = compressed.Close()
				data := bytes.Clone(w.Bytes())
				switch kind {
				case "checksum":
					data[len(data)-1] ^= 255
				case "truncated":
					data = data[:len(data)-1]
				}
				r := testPSBCursor(data)
				p := &parser{version: 2, channels: 3, document: &psdmodel.Document{Width: 2, Height: 2}}
				if composite {
					p.composite(r)
				} else {
					p.decodePlane(r, freshImage(image.Rect(0, 0, 2, 2)), 0)
				}
				if r.err == nil {
					t.Fatal("malformed PSB ZIP accepted")
				}
			})
		}
	}
}

func TestZIPTrailingBytesRejected(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, compression := range []uint16{2, 3} {
			for _, composite := range []bool{false, true} {
				t.Run(fmt.Sprintf("v%d/compression_%d/composite_%t", version, compression, composite), func(t *testing.T) {
					var data []byte
					if composite {
						data = append(versionedDocument(version, 0, compression, false), 0, 99)
					} else {
						data = versionedDocumentWithChannelTail(version, compression, 0, false, []byte{0, 99})
					}
					_, err := ParseBytes(data)
					if err == nil || !strings.Contains(err.Error(), "trailing bytes") {
						t.Fatalf("ZIP trailing garbage must be rejected, got %v", err)
					}
				})
			}
		}
	}
}

type interruptedZIPReaderAt struct{ data []byte }

func (reader interruptedZIPReaderAt) ReadAt(target []byte, offset int64) (int, error) {
	if offset >= 2 {
		return 0, context.Canceled
	}
	return bytes.NewReader(reader.data).ReadAt(target, offset)
}

func TestZIPReaderAtErrorsReachCaller(t *testing.T) {
	for _, composite := range []bool{false, true} {
		data := versionedPixels(1, 2, [][]byte{{1, 2, 3, 4}})
		r := &cursor{
			source: &source{reader: interruptedZIPReaderAt{data: data}, size: int64(len(data))},
			end:    int64(len(data)),
		}
		p := &parser{version: 1, channels: 1, document: &psdmodel.Document{Width: 2, Height: 2}}
		if composite {
			p.composite(r)
		} else {
			p.decodePlane(r, freshImage(image.Rect(0, 0, 2, 2)), 0)
		}
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("ReaderAt cancellation was hidden: %v", r.err)
		}
	}
}
