package psdfile_test

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdparse"
)

type rgb8FormatFixture struct {
	version, compression uint16
	width                int
	negativeCount        bool
	noLayers             bool
	globalTag, layerTag  string
	layerTail            []byte
	compositeTail        []byte
	composite            [][]byte
}

func rgb8FormatWord(data []byte, value uint16) []byte {
	return binary.BigEndian.AppendUint16(data, value)
}
func rgb8FormatLong(data []byte, value uint32) []byte {
	return binary.BigEndian.AppendUint32(data, value)
}

func rgb8FormatLength(data []byte, value int, version uint16) []byte {
	if version == 2 {
		return binary.BigEndian.AppendUint64(data, uint64(value))
	}
	return rgb8FormatLong(data, uint32(value))
}

func rgb8FormatBlock(data, value []byte, version uint16) []byte {
	return append(rgb8FormatLength(data, len(value), version), value...)
}

func rgb8FormatTag(key string, version uint16) []byte {
	if key == "" {
		return nil
	}
	return rgb8FormatLength(append([]byte("8BIM"), key...), 0, version)
}

func rgb8FormatPayload(t *testing.T, version, compression uint16, planes [][]byte) []byte {
	t.Helper()
	data := rgb8FormatWord(nil, compression)
	if compression == 0 {
		for _, plane := range planes {
			data = append(data, plane...)
		}
		return data
	}
	if compression == 1 {
		var rows []byte
		for _, plane := range planes {
			row := make([]byte, 0, len(plane)*2+1)
			for _, sample := range plane {
				row = append(row, 0, sample)
			}
			if len(plane)%2 != 0 {
				row = append(row, 0x80)
			}
			if version == 2 {
				data = rgb8FormatLong(data, uint32(len(row)))
			} else {
				data = rgb8FormatWord(data, uint16(len(row)))
			}
			rows = append(rows, row...)
		}
		return append(data, rows...)
	}
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	for _, plane := range planes {
		row := bytes.Clone(plane)
		if compression == 3 {
			for index := len(row) - 1; index > 0; index-- {
				row[index] -= row[index-1]
			}
		}
		if _, err := writer.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return append(data, compressed.Bytes()...)
}

func (fixture rgb8FormatFixture) bytes(t *testing.T) []byte {
	t.Helper()
	width := fixture.width
	if width == 0 {
		width = 3
	}
	composite := fixture.composite
	if composite == nil {
		composite = [][]byte{
			bytes.Repeat([]byte{200}, width), bytes.Repeat([]byte{180}, width), bytes.Repeat([]byte{160}, width),
			bytes.Repeat([]byte{128}, width), bytes.Repeat([]byte{17}, width),
		}
	}
	var section []byte
	if !fixture.noLayers {
		planes := [][]byte{
			bytes.Repeat([]byte{10}, width), bytes.Repeat([]byte{20}, width), bytes.Repeat([]byte{30}, width),
			bytes.Repeat([]byte{64}, width),
		}
		record := rgb8FormatLong(rgb8FormatLong(rgb8FormatLong(rgb8FormatLong(nil, 0), 0), 1), uint32(width))
		record = rgb8FormatWord(record, uint16(len(planes)))
		var channels []byte
		for index, plane := range planes {
			payload := rgb8FormatPayload(t, fixture.version, fixture.compression, [][]byte{plane})
			payload = append(payload, fixture.layerTail...)
			id := uint16(index)
			if index == 3 {
				id = 65535
			}
			record = rgb8FormatLength(rgb8FormatWord(record, id), len(payload), fixture.version)
			channels = append(channels, payload...)
		}
		record = append(record, []byte("8BIMnorm")...)
		record = append(record, 255, 0, 0, 0)
		extra := append(rgb8FormatLong(rgb8FormatLong(nil, 0), 0), 1, 'L', 0, 0)
		extra = append(extra, rgb8FormatTag(fixture.layerTag, fixture.version)...)
		record = rgb8FormatBlock(record, extra, 1)
		count := uint16(1)
		if fixture.negativeCount {
			count = 65535
		}
		info := append(rgb8FormatWord(nil, count), record...)
		info = append(info, channels...)
		if len(info)%2 != 0 {
			info = append(info, 0)
		}
		section = rgb8FormatLong(rgb8FormatBlock(nil, info, fixture.version), 0)
	} else if fixture.globalTag != "" {
		section = rgb8FormatLong(rgb8FormatLength(nil, 0, fixture.version), 0)
	}
	section = append(section, rgb8FormatTag(fixture.globalTag, fixture.version)...)
	data := rgb8FormatWord([]byte("8BPS"), fixture.version)
	data = append(data, make([]byte, 6)...)
	data = rgb8FormatWord(data, uint16(len(composite)))
	data = rgb8FormatLong(rgb8FormatLong(data, 1), uint32(width))
	data = rgb8FormatWord(rgb8FormatWord(data, 8), 3)
	data = rgb8FormatLong(rgb8FormatLong(data, 0), 0)
	data = rgb8FormatBlock(data, section, fixture.version)
	data = append(data, rgb8FormatPayload(t, fixture.version, fixture.compression, composite)...)
	return append(data, fixture.compositeTail...)
}

func TestRGB8CompositeTransparencyMarkers(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := uint16(0); compression <= 3; compression++ {
			for _, test := range []struct {
				name                string
				negative, noLayers  bool
				globalTag, layerTag string
				transparent         bool
			}{
				{name: "saved-alpha-without-layers", noLayers: true},
				{name: "saved-alpha-with-positive-count"},
				{name: "negative-count", negative: true, transparent: true},
				{name: "global-Mtrn", globalTag: "Mtrn", transparent: true},
				{name: "global-Mtrn-without-layers", noLayers: true, globalTag: "Mtrn", transparent: true},
				{name: "layer-Mtrn", layerTag: "Mtrn"},
				{name: "layer-Mt16", layerTag: "Mt16"},
				{name: "layer-Mt32", layerTag: "Mt32"},
				{name: "global-Mt16", globalTag: "Mt16"},
				{name: "global-Mt32", globalTag: "Mt32"},
			} {
				t.Run(fmt.Sprintf("v%d/compression%d/%s", version, compression, test.name), func(t *testing.T) {
					fixture := rgb8FormatFixture{version: version, compression: compression, negativeCount: test.negative,
						noLayers: test.noLayers, globalTag: test.globalTag, layerTag: test.layerTag}
					data := fixture.bytes(t)
					source, err := psdfile.Parse(data)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = source.Close() })
					eager, err := psdparse.ParseBytes(data)
					if err != nil {
						t.Fatal(err)
					}
					legacy, err := source.Decode(nil)
					if err != nil {
						t.Fatal(err)
					}
					display, err := psdkit.DecodeDisplayComposite(nil, source)
					if err != nil {
						t.Fatal(err)
					}
					region, err := psdkit.DecodeDisplayRegion(nil, source, image.Rect(1, 0, 3, 1))
					if err != nil {
						t.Fatal(err)
					}
					expected := []byte{200, 180, 160, 255}
					if test.transparent {
						expected = []byte{145, 106, 66, 128}
					}
					for name, pixels := range map[string][]byte{
						"ParseBytes": eager.Composite.Pix, "Decode": legacy.Composite.Pix,
						"display": display.Pix, "region": region.Pix,
					} {
						if !bytes.Equal(pixels, bytes.Repeat(expected, len(pixels)/4)) {
							t.Errorf("%s pixels=%v; expected repeated %v", name, pixels, expected)
						}
					}
					if !test.noLayers {
						for name, pixels := range map[string][]byte{
							"ParseBytes layer": eager.Layers[0].Pixels.Pix, "Decode layer": legacy.Layers[0].Pixels.Pix,
						} {
							if !bytes.Equal(pixels, bytes.Repeat([]byte{10, 20, 30, 64}, 3)) {
								t.Errorf("%s transparency changed: %v", name, pixels)
							}
						}
					}
					planes, err := source.DecodeComposite(nil)
					if err != nil || len(planes.Channels) != 5 ||
						!bytes.Equal(planes.Channels[3].Data, []byte{128, 128, 128}) ||
						!bytes.Equal(planes.Channels[4].Data, []byte{17, 17, 17}) {
						t.Fatalf("source alpha planes were lost or altered: %v", err)
					}
				})
			}
		}
	}
}

type rgb8FormatPaddingCase struct {
	name  string
	width int
	tail  []byte
	valid bool
}

func rgb8FormatPaddingCases(compression uint16) []rgb8FormatPaddingCase {
	return []rgb8FormatPaddingCase{
		{name: "odd-unpadded", width: 3, valid: true},
		{name: "odd-single-pad", width: 3, tail: []byte{0xab}, valid: compression < 2},
		{name: "odd-two-pads", width: 3, tail: []byte{0, 0}},
		{name: "even-unpadded", width: 2, valid: true},
		{name: "even-single-pad", width: 2, tail: []byte{0}},
	}
}

func TestRGB8ChannelPaddingAgreement(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := uint16(0); compression <= 3; compression++ {
			for _, test := range rgb8FormatPaddingCases(compression) {
				t.Run(fmt.Sprintf("v%d/compression%d/%s", version, compression, test.name), func(t *testing.T) {
					data := (rgb8FormatFixture{version: version, compression: compression,
						width: test.width, layerTail: test.tail}).bytes(t)
					eager, eagerErr := psdparse.ParseBytes(data)
					source, err := psdfile.Parse(data)
					if err != nil {
						if test.valid || eagerErr == nil {
							t.Fatalf("index error=%v; eager error=%v; valid=%t", err, eagerErr, test.valid)
						}
						return
					}
					t.Cleanup(func() { _ = source.Close() })
					plane, planeErr := source.DecodeChannel(nil, 0, 0)
					region, regionErr := source.DecodeChannelRegion(nil, 0, 0, image.Rect(1, 0, test.width, 1))
					legacy, legacyErr := source.Decode(nil)
					for name, decodeErr := range map[string]error{
						"plane": planeErr, "region": regionErr, "eager": eagerErr, "legacy": legacyErr,
					} {
						if (decodeErr == nil) != test.valid {
							t.Errorf("%s: valid=%t error=%v", name, test.valid, decodeErr)
						}
					}
					if !test.valid || planeErr != nil || regionErr != nil || eagerErr != nil || legacyErr != nil {
						return
					}
					if !bytes.Equal(plane.Data, bytes.Repeat([]byte{10}, test.width)) ||
						!bytes.Equal(region.Data, bytes.Repeat([]byte{10}, test.width-1)) {
						t.Fatalf("padding contaminated samples: plane=%v region=%v", plane.Data, region.Data)
					}
					expected := bytes.Repeat([]byte{10, 20, 30, 64}, test.width)
					if !bytes.Equal(eager.Layers[0].Pixels.Pix, expected) ||
						!bytes.Equal(legacy.Layers[0].Pixels.Pix, expected) {
						t.Fatal("padding contaminated RGB8 layer pixels")
					}
				})
			}
		}
	}
}

func TestRGB8ZIPCompositeTrailingBytesRejected(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, compression := range []uint16{2, 3} {
			t.Run(fmt.Sprintf("v%d/compression%d", version, compression), func(t *testing.T) {
				data := (rgb8FormatFixture{version: version, compression: compression,
					compositeTail: []byte{0}}).bytes(t)
				source, err := psdfile.Parse(data)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = source.Close() })
				if _, err := psdparse.ParseBytes(data); err == nil {
					t.Error("eager parser accepted trailing ZIP composite bytes")
				}
				if _, err := source.Decode(nil); err == nil {
					t.Error("RGB8 snapshot accepted trailing ZIP composite bytes")
				}
				if _, err := psdkit.DecodeDisplayComposite(nil, source); err == nil {
					t.Error("display accepted trailing ZIP composite bytes")
				}
			})
		}
	}
}
