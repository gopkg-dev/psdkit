package psdparse_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdparse"
)

type emptyDimensionWriter struct{ bytes.Buffer }

func (w *emptyDimensionWriter) u16(value uint16) {
	w.Write(binary.BigEndian.AppendUint16(nil, value))
}

func (w *emptyDimensionWriter) u32(value uint32) {
	w.Write(binary.BigEndian.AppendUint32(nil, value))
}

func (w *emptyDimensionWriter) length(version uint16, length int) {
	if version == 2 {
		w.Write(binary.BigEndian.AppendUint64(nil, uint64(length)))
		return
	}
	w.u32(uint32(length))
}

func (w *emptyDimensionWriter) block(data []byte) {
	w.u32(uint32(len(data)))
	w.Write(data)
}

func (w *emptyDimensionWriter) rectangle(bounds image.Rectangle) {
	for _, edge := range []int{bounds.Min.Y, bounds.Min.X, bounds.Max.Y, bounds.Max.X} {
		w.u32(uint32(edge))
	}
}

// emptyDimensionDocument writes independent raw-channel fixtures with a normal layer after the tested record.
func emptyDimensionDocument(version uint16, canvas image.Point, bounds image.Rectangle, mask bool) []byte {
	records, pixels := &emptyDimensionWriter{}, &emptyDimensionWriter{}
	for index := range 2 {
		layerBounds := image.Rect(0, 0, 1, 1)
		if index == 0 && !mask {
			layerBounds = bounds
		}
		extra := &emptyDimensionWriter{}
		if index == 0 && mask {
			maskData := &emptyDimensionWriter{}
			maskData.rectangle(bounds)
			maskData.Write([]byte{255, 0, 0, 0})
			extra.block(maskData.Bytes())
		} else {
			extra.u32(0)
		}
		extra.u32(0)
		extra.Write([]byte{1, byte('A' + index), 0, 0})
		records.rectangle(layerBounds)
		ids := []int16{0, 1, 2, -1}
		if index == 0 && mask {
			ids = append(ids, -2)
		}
		records.u16(uint16(len(ids)))
		for component, id := range ids {
			channelBounds := layerBounds
			if id == -2 {
				channelBounds = bounds
			}
			channelData := []byte{0, 0}
			if channelBounds.Dx() == 1 && channelBounds.Dy() == 1 {
				channelData = append(channelData, []byte{17, 34, 51, 255, 255}[component])
			}
			records.u16(uint16(id))
			records.length(version, len(channelData))
			pixels.Write(channelData)
		}
		records.WriteString("8BIMnorm")
		records.Write([]byte{255, 0, 0, 0})
		records.block(extra.Bytes())
	}
	info := &emptyDimensionWriter{}
	info.u16(2)
	info.Write(records.Bytes())
	info.Write(pixels.Bytes())
	if info.Len()%2 != 0 {
		info.WriteByte(0)
	}
	layers := &emptyDimensionWriter{}
	layers.length(version, info.Len())
	layers.Write(info.Bytes())
	layers.u32(0)
	file := &emptyDimensionWriter{}
	file.WriteString("8BPS")
	file.u16(version)
	file.Write(make([]byte, 6))
	file.u16(3)
	file.u32(uint32(canvas.Y))
	file.u32(uint32(canvas.X))
	file.u16(8)
	file.u16(3)
	file.u32(0)
	file.u32(0)
	file.length(version, layers.Len())
	file.Write(layers.Bytes())
	file.u16(0)
	file.Write([]byte{17, 34, 51})
	return file.Bytes()
}

func emptyDimensionReaders(
	t *testing.T, data []byte,
) (map[string]func() (*psdmodel.Document, error), *psdfile.Document) {
	t.Helper()
	readers := map[string]func() (*psdmodel.Document, error){
		"ParseBytes": func() (*psdmodel.Document, error) { return psdparse.ParseBytes(data) },
		"ParseLayerReader": func() (*psdmodel.Document, error) {
			return psdparse.ParseLayerReader(bytes.NewReader(data), int64(len(data)))
		},
	}
	document, err := psdfile.Parse(data)
	if err != nil {
		t.Fatalf("index fixture: %v", err)
	}
	t.Cleanup(func() { _ = document.Close() })
	readers["Decode"] = func() (*psdmodel.Document, error) { return document.Decode(context.Background()) }
	readers["DecodeLayers"] = func() (*psdmodel.Document, error) { return document.DecodeLayers(context.Background()) }
	return readers, document
}

func TestEmptyLayerAndMaskDimensionsPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, size := range []image.Point{{0, 0}, {0, 1}, {1, 0}} {
			for _, mask := range []bool{false, true} {
				t.Run(fmt.Sprintf("version_%d/%dx%d/mask_%t", version, size.X, size.Y, mask), func(t *testing.T) {
					bounds := image.Rectangle{Min: image.Pt(3, 4), Max: image.Pt(3+size.X, 4+size.Y)}
					data := emptyDimensionDocument(version, image.Pt(1, 1), bounds, mask)
					readers, indexed := emptyDimensionReaders(t, data)
					t.Run("DecodeChannel", func(t *testing.T) {
						for channelIndex, channel := range indexed.Layers[0].Channels {
							if mask && channel.ID != -2 {
								continue
							}
							plane, err := indexed.DecodeChannel(context.Background(), 0, channelIndex)
							if err != nil {
								t.Fatal(err)
							}
							if plane.Width != size.X || plane.Height != size.Y || len(plane.Data) != 0 {
								t.Fatalf("empty channel dimensions or samples changed: %+v", plane)
							}
						}
					})
					for name, read := range readers {
						t.Run(name, func(t *testing.T) {
							document, err := read()
							if err != nil {
								t.Fatal(err)
							}
							if len(document.Layers) != 2 {
								t.Fatalf("layers=%d, want 2", len(document.Layers))
							}
							first, next := document.Layers[0], document.Layers[1]
							if mask {
								if first.Mask == nil || first.Mask.Bounds != bounds || first.Mask.Pixels != nil {
									t.Fatalf("empty mask geometry or pixels changed: %+v", first.Mask)
								}
							} else if first.Bounds != bounds || first.Pixels != nil {
								t.Fatalf("empty layer geometry or pixels changed: %+v", first)
							}
							if next.Name != "B" || next.Pixels == nil ||
								next.Pixels.NRGBAAt(0, 0) != (color.NRGBA{R: 17, G: 34, B: 51, A: 255}) {
								t.Fatalf("subsequent layer was not decoded correctly: %+v", next)
							}
						})
					}
				})
			}
		}
	}
}

func TestEmptyDimensionsDoNotBypassBoundsPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, size := range []image.Point{{-1, 0}, {0, -1}, {30001, 0}, {0, 30001}, {5000, 5000}} {
			for _, mask := range []bool{false, true} {
				t.Run(fmt.Sprintf("version_%d/%dx%d/mask_%t", version, size.X, size.Y, mask), func(t *testing.T) {
					data := emptyDimensionDocument(version, image.Pt(1, 1), image.Rectangle{Max: size}, mask)
					if _, err := psdparse.ParseBytes(data); err == nil {
						t.Fatal("invalid layer or mask dimensions accepted")
					}
					if _, err := psdparse.ParseLayerReader(bytes.NewReader(data), int64(len(data))); err == nil {
						t.Fatal("invalid layer-only dimensions accepted")
					}
				})
			}
		}
		for _, size := range []image.Point{{0, 0}, {0, 1}, {1, 0}} {
			t.Run(fmt.Sprintf("version_%d/canvas_%dx%d", version, size.X, size.Y), func(t *testing.T) {
				data := emptyDimensionDocument(version, size, image.Rectangle{}, false)
				if _, err := psdparse.ParseBytes(data); err == nil {
					t.Fatal("empty canvas accepted")
				}
				if _, err := psdparse.ParseLayerReader(bytes.NewReader(data), int64(len(data))); err == nil {
					t.Fatal("empty canvas accepted by layer-only reader")
				}
			})
		}
	}
}
