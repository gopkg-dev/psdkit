package psdparse

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image"
	"strings"
	"testing"

	"github.com/gopkg-dev/psdkit/psdrender"
)

type testWriter struct{ bytes.Buffer }

func (w *testWriter) u8(value byte)      { w.WriteByte(value) }
func (w *testWriter) u16(value uint16)   { _ = binary.Write(&w.Buffer, binary.BigEndian, value) }
func (w *testWriter) u32(value uint32)   { _ = binary.Write(&w.Buffer, binary.BigEndian, value) }
func (w *testWriter) block(value []byte) { w.u32(uint32(len(value))); w.Write(value) }

func testChannel(compression uint16, values []byte) []byte {
	output := &testWriter{}
	output.u16(compression)
	switch compression {
	case 0:
		output.Write(values)
	case 1:
		output.u16(3)
		output.u16(3)
		output.Write([]byte{1, values[0], values[1], 1, values[2], values[3]})
	case 2, 3:
		pixels := bytes.Clone(values)
		if compression == 3 {
			pixels[1] -= pixels[0]
			pixels[3] -= pixels[2]
		}
		compressed := zlib.NewWriter(&output.Buffer)
		_, _ = compressed.Write(pixels)
		_ = compressed.Close()
	}
	return output.Bytes()
}

func testPSD(compression uint16, mask bool) []byte {
	channels := [][]byte{
		testChannel(compression, []byte{20, 40, 60, 80}), testChannel(compression, []byte{10, 30, 50, 70}),
		testChannel(compression, []byte{1, 2, 3, 4}), testChannel(compression, []byte{255, 128, 64, 0})}
	if mask {
		channels = append(channels, testChannel(compression, []byte{0, 80, 160, 255}))
	}
	record := &testWriter{}
	record.u32(0)
	record.u32(0)
	record.u32(2)
	record.u32(2)
	record.u16(uint16(len(channels)))
	ids := []uint16{0, 1, 2, 65535, 65534}
	for index, data := range channels {
		record.u16(ids[index])
		record.u32(uint32(len(data)))
	}
	record.WriteString("8BIMnorm")
	record.Write([]byte{128, 0, 0, 0})
	extra := &testWriter{}
	if mask {
		extra.u32(20)
		extra.u32(0)
		extra.u32(0)
		extra.u32(2)
		extra.u32(2)
		extra.Write([]byte{128, 0, 0, 0})
	} else {
		extra.u32(0)
	}
	extra.block([]byte{0, 0, 255, 255, 0, 0, 255, 255})
	extra.Write([]byte{1, 'L', 0, 0})
	record.block(extra.Bytes())
	info := &testWriter{}
	info.u16(1)
	info.Write(record.Bytes())
	for _, data := range channels {
		info.Write(data)
	}
	if info.Len()%2 != 0 {
		info.u8(0)
	}
	section := &testWriter{}
	section.block(info.Bytes())
	section.u32(0)
	file := &testWriter{}
	file.WriteString("8BPS")
	file.u16(1)
	file.Write(make([]byte, 6))
	file.u16(3)
	file.u32(2)
	file.u32(2)
	file.u16(8)
	file.u16(3)
	file.u32(0)
	file.u32(0)
	file.block(section.Bytes())
	file.u16(0)
	file.Write([]byte{20, 40, 60, 80, 10, 30, 50, 70, 1, 2, 3, 4})
	return file.Bytes()
}

func TestCompressionAndMaskPixels(t *testing.T) {
	for _, compression := range []uint16{0, 1, 2, 3} {
		t.Run([]string{"raw", "rle", "zip", "prediction"}[compression], func(t *testing.T) {
			document, err := ParseBytes(testPSD(compression, true))
			if err != nil {
				t.Fatal(err)
			}
			if len(document.Layers) != 1 {
				t.Fatalf("layer count=%d", len(document.Layers))
			}
			layer := document.Layers[0]
			expected := []byte{20, 10, 1, 255, 40, 30, 2, 128, 60, 50, 3, 64, 80, 70, 4, 0}
			if !bytes.Equal(layer.Pixels.Pix, expected) {
				t.Fatalf("decoded channels differ: %v", layer.Pixels.Pix)
			}
			if *layer.Opacity != 128.0/255 || *layer.Mask.DefaultColor != 128 {
				t.Fatal("opacity or default mask color lost")
			}
			mask := []byte{0, 0, 0, 255, 80, 80, 80, 255, 160, 160, 160, 255, 255, 255, 255, 255}
			if !bytes.Equal(layer.Mask.Pixels.Pix, mask) {
				t.Fatalf("decoded mask differs: %v", layer.Mask.Pixels.Pix)
			}
		})
	}
}

func TestMalformedInputLimits(t *testing.T) {
	valid := testPSD(0, false)
	for _, length := range []int{0, 25, 26, 37, len(valid) - 1} {
		if _, err := ParseBytes(valid[:length]); err == nil {
			t.Errorf("truncated %d-byte PSD accepted", length)
		}
	}
	for _, test := range []struct {
		name   string
		offset int
		value  uint32
		word   bool
	}{
		{"PSB", 4, 2, true}, {"16-bit", 22, 16, true}, {"CMYK", 24, 4, true}, {"wide", 18, 30001, false},
		{"pixels", 14, 20_000_000, false}, {"channels", 12, 57, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := bytes.Clone(valid)
			if test.word {
				binary.BigEndian.PutUint16(data[test.offset:], uint16(test.value))
			} else {
				binary.BigEndian.PutUint32(data[test.offset:], test.value)
			}
			if _, err := ParseBytes(data); err == nil {
				t.Fatal("unsupported or oversized PSD accepted")
			}
		})
	}
	if _, err := ParseReader(bytes.NewReader(valid), 500<<20+1); err == nil {
		t.Fatal("oversized compressed source accepted")
	}
	p := &parser{pixels: 20_000_000}
	r := &cursor{}
	p.account(image.Rect(0, 0, 4000, 4000), r)
	p.account(image.Rect(0, 0, 4000, 4000), r)
	p.account(image.Rect(0, 0, 4000, 4000), r)
	if r.err == nil || !strings.Contains(r.err.Error(), "60 million") {
		t.Fatal("total decoded pixel budget was not checked")
	}
}

func TestPackBitsValidation(t *testing.T) {
	row := make([]byte, 4)
	if err := decodePackBits([]byte{253, 9}, row); err != nil || !bytes.Equal(row, []byte{9, 9, 9, 9}) {
		t.Fatalf("valid repeat failed: %v %v", row, err)
	}
	for _, encoded := range [][]byte{{3, 1}, {252, 9}, {128}, {1, 1, 2}} {
		if err := decodePackBits(encoded, row); err == nil {
			t.Fatalf("invalid row accepted: %v", encoded)
		}
	}
}

func TestDescriptorsAndUnsupportedFeatures(t *testing.T) {
	data := []byte{1, 0, 0, 0}
	r := &cursor{source: &source{reader: bytes.NewReader(data), size: int64(len(data))}, end: int64(len(data))}
	record := &layerRecord{layer: &psdrender.Layer{}}
	(&parser{document: &psdrender.Document{}}).additionalEntry(r, "knko", record)
	if record.layer.Knockout != 1 || len(record.layer.Unsupported) != 0 {
		t.Fatal("typed Knockout setting was lost")
	}
	invalid := readWarp(descriptor{"warpStyle": "warpStyle.warpCustom", "deformNumRows": float64(0)})
	if !invalid.Split {
		t.Fatal("presence of a split-grid descriptor was lost")
	}
}

func FuzzParse(f *testing.F) {
	f.Add(testPSD(0, true))
	f.Add(testPSD(1, false))
	f.Add(testPSD(3, true))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = ParseBytes(data)
	})
}
