package psdscene

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

type defaultMaskTileSettings struct {
	color   byte
	flags   byte
	density *byte
	feather *float64
	vector  bool
}

// defaultMaskTileSource writes raw RGB8 layer channels with an independent, sample-free user mask header.
func defaultMaskTileSource(canvas image.Point, bounds image.Rectangle, settings defaultMaskTileSettings) []byte {
	var mask, record, pixels bytes.Buffer
	for _, edge := range []int{bounds.Min.Y, bounds.Min.X, bounds.Max.Y, bounds.Max.X} {
		_ = binary.Write(&mask, binary.BigEndian, int32(edge))
	}
	flags, parameters := settings.flags, byte(0)
	if settings.density != nil {
		parameters |= 1
	}
	if settings.feather != nil {
		parameters |= 2
	}
	if parameters != 0 {
		flags |= 16
	}
	mask.Write([]byte{settings.color, flags})
	if parameters != 0 {
		mask.WriteByte(parameters)
		if settings.density != nil {
			mask.WriteByte(*settings.density)
		}
		if settings.feather != nil {
			_ = binary.Write(&mask, binary.BigEndian, *settings.feather)
		}
	} else {
		mask.Write([]byte{0, 0})
	}
	extra := append(artboardTestU32(uint32(mask.Len())), mask.Bytes()...)
	extra = append(extra, 0, 0, 0, 0, 1, 'D', 0, 0)
	if settings.vector {
		vector := binary.BigEndian.AppendUint32(nil, 3)
		vector = binary.BigEndian.AppendUint32(vector, 0)
		vector = append(vector, 0, 8, 0, 1)
		vector = append(vector, make([]byte, 22)...)
		extra = append(extra, artboardTestBlock("vmsk", vector)...)
	}
	for _, edge := range []int{0, 0, canvas.Y, canvas.X} {
		_ = binary.Write(&record, binary.BigEndian, int32(edge))
	}
	_ = binary.Write(&record, binary.BigEndian, uint16(4))
	for component, id := range []int16{0, 1, 2, -1} {
		_ = binary.Write(&record, binary.BigEndian, id)
		_ = binary.Write(&record, binary.BigEndian, uint32(canvas.X*canvas.Y+2))
		pixels.Write([]byte{0, 0})
		pixels.Write(bytes.Repeat([]byte{[]byte{180, 90, 30, 255}[component]}, canvas.X*canvas.Y))
	}
	record.WriteString("8BIMnorm")
	record.Write([]byte{255, 0, 0, 0})
	_ = binary.Write(&record, binary.BigEndian, uint32(len(extra)))
	record.Write(extra)
	info := append([]byte{0xff, 0xff}, record.Bytes()...)
	info = append(info, pixels.Bytes()...)
	if len(info)%2 != 0 {
		info = append(info, 0)
	}
	layers := append(artboardTestU32(uint32(len(info))), info...)
	layers = append(layers, 0, 0, 0, 0)
	header := []byte{'8', 'B', 'P', 'S', 0, 1, 0, 0, 0, 0, 0, 0, 0, 4}
	header = append(header, artboardTestU32(uint32(canvas.Y))...)
	header = append(header, artboardTestU32(uint32(canvas.X))...)
	header = append(header, 0, 8, 0, 3, 0, 0, 0, 0, 0, 0, 0, 0)
	header = append(header, artboardTestU32(uint32(len(layers)))...)
	header = append(header, layers...)
	header = append(header, 0, 0)
	return append(header, bytes.Repeat([]byte{13}, canvas.X*canvas.Y*4)...)
}

func TestDefaultCoverageMaskFullAndTileIgnoreAbsentSamples(t *testing.T) {
	for _, canvas := range []image.Point{{1, 1}, {64, 48}} {
		for _, bounds := range []image.Rectangle{
			image.Rect(-3, 2, 4, 8), {}, image.Rect(3, 4, 3, 8), image.Rect(-512, -512, 512, 512),
		} {
			for index, settings := range []defaultMaskTileSettings{
				{color: 0}, {color: 255}, {color: 64, vector: true},
				{color: 64, flags: 1 | 4, density: new(byte(128)), feather: new(3.0), vector: true},
				{color: 64, flags: 2 | 4, density: new(byte(128)), feather: new(3.0), vector: true},
				{color: 64, flags: 8, vector: true},
			} {
				t.Run(fmt.Sprintf("canvas%v/bounds%v/case%d", canvas, bounds, index), func(t *testing.T) {
					data := defaultMaskTileSource(canvas, bounds, settings)
					original := bytes.Clone(data)
					source := cachedOpen(t, data)
					full, err := Render(t.Context(), source, Options{MaxWorkingBytes: 16 << 20})
					if err != nil {
						t.Fatal(err)
					}
					coverage := float64(settings.color) / 255
					if settings.flags&4 != 0 {
						coverage = 1 - coverage
					}
					if settings.density != nil {
						coverage = 1 - (1-coverage)*float64(*settings.density)/255
					}
					if settings.flags&(2|8) != 0 {
						coverage = 1
					}
					expected := color.NRGBA{R: 180, G: 90, B: 30, A: byte(math.Round(coverage * 255))}
					if expected.A == 0 {
						expected = color.NRGBA{}
					}
					for _, rectangle := range []image.Rectangle{
						{Max: canvas},
						image.Rect(canvas.X/2, canvas.Y/2, canvas.X, canvas.Y),
					} {
						tile, err := RenderTile(t.Context(), source, rectangle, TileOptions{MaxWorkingBytes: 1 << 20})
						if err != nil {
							t.Fatal(err)
						}
						for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
							for x := rectangle.Min.X; x < rectangle.Max.X; x++ {
								if tile.NRGBAAt(x, y) != expected || full.NRGBAAt(x, y) != expected {
									t.Fatalf("sample-free coverage at %d,%d: full=%v tile=%v want=%v",
										x, y, full.NRGBAAt(x, y), tile.NRGBAAt(x, y), expected)
								}
							}
						}
					}
					if !bytes.Equal(data, original) {
						t.Fatal("regional reconstruction changed original source bytes")
					}
				})
			}
		}
	}
}

func TestDefaultCoverageTileKeepsUnknownGeneratedAndAmbiguousChannelFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		record psdfile.LayerRecord
		id     int16
	}{
		{"missing metadata", psdfile.LayerRecord{}, -2},
		{"generated", psdfile.LayerRecord{Mask: &psdfile.MaskRecord{Flags: 8}}, -2},
		{"unknown flags", psdfile.LayerRecord{Mask: &psdfile.MaskRecord{Flags: 0x80}}, -2},
		{"unknown parameters", psdfile.LayerRecord{Mask: &psdfile.MaskRecord{ParameterFlags: 0x80}}, -2},
		{"real metadata", psdfile.LayerRecord{Mask: &psdfile.MaskRecord{HasReal: true}}, -2},
		{"real target", psdfile.LayerRecord{Mask: &psdfile.MaskRecord{}}, -3},
		{"primary samples", psdfile.LayerRecord{Mask: &psdfile.MaskRecord{},
			Channels: []psdfile.ChannelRecord{{ID: -2}}}, -2},
		{"real samples", psdfile.LayerRecord{Mask: &psdfile.MaskRecord{},
			Channels: []psdfile.ChannelRecord{{ID: -3}}}, -2},
		{"duplicate vectors", psdfile.LayerRecord{Mask: &psdfile.MaskRecord{},
			AdditionalInfo: []psdfile.AdditionalBlock{{Key: "vmsk"}, {Key: "vsms"}}}, -2},
	} {
		t.Run(test.name, func(t *testing.T) {
			if tileDefaultCoverageMask(test.record, test.id) {
				t.Fatal("unsupported source layout was treated as known sample-free default coverage")
			}
		})
	}
	if _, err := tileMaskChannel(psdfile.LayerRecord{}, -3); !errors.Is(err, psdfile.ErrMalformed) {
		t.Fatalf("missing real user samples stopped reporting malformed source: %v", err)
	}
	data := defaultMaskTileSource(image.Pt(64, 48), image.Rect(0, 0, 64, 48), defaultMaskTileSettings{color: 63})
	source := cachedOpen(t, data)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := RenderTile(ctx, source, image.Rect(0, 0, 1, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("sample-free mask bypassed cancellation: %v", err)
	}
	if _, err := RenderTile(t.Context(), source, image.Rect(0, 0, 1, 1),
		TileOptions{MaxWorkingBytes: 1024}); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("sample-free mask bypassed metadata budget: %v", err)
	}
}
