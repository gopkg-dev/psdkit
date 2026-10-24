package psdparse_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

// defaultCoverageVectorDocument writes mask metadata and vector records without any mask sample channel.
func defaultCoverageVectorDocument(version uint16, bounds image.Rectangle, flags byte) []byte {
	mask := &emptyDimensionWriter{}
	mask.rectangle(bounds)
	mask.Write([]byte{63, flags | 16, 15, 77})
	mask.Write(binary.BigEndian.AppendUint64(nil, math.Float64bits(1.5)))
	mask.WriteByte(169)
	mask.Write(binary.BigEndian.AppendUint64(nil, 0))
	vector := &emptyDimensionWriter{}
	vector.u32(3)
	vector.u32(0)
	vector.u16(8)
	vector.u16(1)
	vector.Write(make([]byte, 22))
	extra := &emptyDimensionWriter{}
	extra.block(mask.Bytes())
	extra.u32(0)
	extra.Write([]byte{1, 'V', 0, 0})
	extra.WriteString("8BIMvmsk")
	extra.block(vector.Bytes())
	record := &emptyDimensionWriter{}
	record.rectangle(image.Rect(0, 0, 1, 1))
	record.u16(4)
	for _, id := range []int16{0, 1, 2, -1} {
		record.u16(uint16(id))
		record.length(version, 3)
	}
	record.WriteString("8BIMnorm")
	record.Write([]byte{255, 0, 0, 0})
	record.block(extra.Bytes())
	info := &emptyDimensionWriter{}
	info.u16(1)
	info.Write(record.Bytes())
	for _, value := range []byte{17, 34, 51, 255} {
		info.Write([]byte{0, 0, value})
	}
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
	file.u32(1)
	file.u32(1)
	file.u16(8)
	file.u16(3)
	file.u32(0)
	file.u32(0)
	file.length(version, layers.Len())
	file.Write(layers.Bytes())
	file.Write([]byte{0, 0, 17, 34, 51})
	return file.Bytes()
}

func TestVectorDefaultCoverageRetainsOnlyOrdinaryUserMaskPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, bounds := range []image.Rectangle{
			image.Rect(-4, 3, 3, 5), {}, image.Rect(3, 4, 3, 7), image.Rect(3, 4, 7, 4),
		} {
			for _, flags := range []byte{1 | 4, 1 | 2 | 4, 1 | 4 | 8, 1 | 2 | 4 | 8} {
				t.Run(fmt.Sprintf("version%d/bounds%v/flags%d", version, bounds, flags), func(t *testing.T) {
					data := defaultCoverageVectorDocument(version, bounds, flags)
					original := bytes.Clone(data)
					readers, indexed := emptyDimensionReaders(t, data)
					view, err := indexed.ReadLayerMetadata(t.Context(), 0)
					if err != nil {
						t.Fatal(err)
					}
					channel := int16(-2)
					if flags&8 != 0 {
						channel = 0
					}
					if view.PixelMaskChannel != channel {
						t.Fatalf("mask metadata source channel = %d, want %d", view.PixelMaskChannel, channel)
					}
					assertDefaultCoverageVectorLayer(t, view.Layer, bounds, flags)
					for name, read := range readers {
						t.Run(name, func(t *testing.T) {
							document, err := read()
							if err != nil {
								t.Fatal(err)
							}
							layer := document.Layers[0]
							assertDefaultCoverageVectorLayer(t, layer, bounds, flags)
							if !reflect.DeepEqual(layer.Mask, view.Layer.Mask) {
								t.Fatal("eager and metadata-only parsing disagree about user-mask semantics")
							}
							if layer.Pixels == nil || layer.Pixels.NRGBAAt(0, 0).R != 17 {
								t.Fatal("default mask metadata consumed the following raster samples")
							}
						})
					}
					if !bytes.Equal(data, original) || len(indexed.Layers[0].Channels) != 4 {
						t.Fatal("parsing changed the source or invented a user-mask channel")
					}
				})
			}
		}
	}
}

func assertDefaultCoverageVectorLayer(t *testing.T, layer *psdmodel.Layer, bounds image.Rectangle, flags byte) {
	t.Helper()
	if layer.VectorMask == nil || layer.VectorDensity == nil || *layer.VectorDensity != 169 ||
		layer.VectorFeather == nil || *layer.VectorFeather != 0 || len(layer.Unsupported) != 0 {
		t.Fatalf("vector mask geometry or shared parameters changed: %+v", layer)
	}
	if flags&8 != 0 {
		if layer.Mask != nil {
			t.Fatal("generated vector coverage without a real user channel became a user mask")
		}
		return
	}
	mask := layer.Mask
	if mask == nil || mask.Bounds != bounds || mask.Pixels != nil || mask.SampleBounds != nil ||
		mask.DefaultColor == nil || *mask.DefaultColor != 63 || !mask.Relative || !mask.Invert ||
		mask.Disabled != (flags&2 != 0) || mask.Density == nil || *mask.Density != 77 ||
		mask.Feather == nil || *mask.Feather != 1.5 || len(mask.Unsupported) != 0 {
		t.Fatalf("ordinary default coverage did not retain its independent user-mask metadata: %+v", mask)
	}
}
