package psdfile

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdparse"
)

// Photoshop and ag-psd write the real mask header before the optional parameters.
func maskParametersPayload(real bool) []byte {
	var data bytes.Buffer
	for _, edge := range []int32{3, 2, 5, 4} {
		indexTestNumber(&data, edge)
	}
	flags := byte(16)
	if real {
		flags |= 8
	}
	data.Write([]byte{0, flags})
	if real {
		data.Write([]byte{1, 255})
		for _, edge := range []int32{1, -2, 3, 0} {
			indexTestNumber(&data, edge)
		}
	}
	data.Write([]byte{15, 128})
	indexTestNumber(&data, float64(2.5))
	data.WriteByte(211)
	indexTestNumber(&data, float64(0))
	return data.Bytes()
}

func maskParametersDocument(version uint16, real bool, payload []byte) []byte {
	ids := []int16{0, 1, 2, -1, -2}
	channels := [][]byte{
		{0, 0, 10, 20, 30, 40}, {0, 0, 50, 60, 70, 80}, {0, 0, 90, 100, 110, 120},
		{0, 0, 255, 255, 255, 255}, {0, 0, 0, 64, 128, 255},
	}
	if real {
		ids = append(ids, -3)
		channels = append(channels, []byte{0, 0, 255, 128, 64, 0})
	}
	var tags []byte
	if real {
		tags = indexTestTag(version, "8BIM", "vmsk", []byte{0, 0, 0, 3, 0, 0, 0, 0}, 2)
	}
	layers := indexTestLayerSet(version, image.Rect(0, 0, 2, 2), ids, channels, payload, tags)
	data := indexTestDocument(version, 8, 3, nil, indexTestGlobal(version, layers, nil), []byte{0, 0, 10, 20, 30})
	binary.BigEndian.PutUint16(data[12:14], 3)
	return data
}

func TestMaskParametersFollowRealHeaderAcrossAPIs(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, real := range []bool{false, true} {
			t.Run(fmt.Sprintf("version%d/real%t", version, real), func(t *testing.T) {
				data := maskParametersDocument(version, real, maskParametersPayload(real))
				document, err := Parse(data)
				if err != nil {
					t.Fatal(err)
				}
				defer document.Close()
				mask := document.Layers[0].Mask
				if mask.Bounds != image.Rect(2, 3, 4, 5) || mask.HasReal != real || mask.ParameterFlags != 15 ||
					mask.UserDensity == nil || *mask.UserDensity != 128 ||
					mask.UserFeather == nil || *mask.UserFeather != 2.5 ||
					mask.VectorDensity == nil || *mask.VectorDensity != 211 ||
					mask.VectorFeather == nil || *mask.VectorFeather != 0 {
					t.Fatalf("wrong indexed mask parameters: %+v", mask)
				}
				wantBounds, wantPixels, channelIndex := image.Rect(2, 3, 4, 5), []byte{0, 64, 128, 255}, 4
				if real {
					wantBounds, wantPixels, channelIndex = image.Rect(-2, 1, 0, 3), []byte{255, 128, 64, 0}, 5
					if mask.RealBounds != wantBounds || mask.RealFlags != 1 || mask.RealDefaultColor != 255 {
						t.Fatalf("wrong real mask header: %+v", mask)
					}
				}
				plane, err := document.DecodeChannel(nil, 0, channelIndex)
				if err != nil || !bytes.Equal(plane.Data, wantPixels) {
					t.Fatalf("wrong mask plane: %+v, %v", plane, err)
				}
				metadata, err := document.ReadLayerMetadata(nil, 0)
				if err != nil {
					t.Fatal(err)
				}
				checkMaskParameters(t, metadata.Layer, wantBounds, real, nil)
				decoded, err := document.decodeRGB8(nil, false)
				if err != nil {
					t.Fatal(err)
				}
				checkMaskParameters(t, decoded.Layers[0], wantBounds, real, wantPixels)
				eager, err := psdparse.ParseBytes(data)
				if err != nil {
					t.Fatal(err)
				}
				checkMaskParameters(t, eager.Layers[0], wantBounds, real, wantPixels)
			})
		}
	}
}

func checkMaskParameters(t *testing.T, layer *psdmodel.Layer, bounds image.Rectangle, relative bool, pixels []byte) {
	t.Helper()
	mask := layer.Mask
	if mask == nil || mask.Bounds != bounds || mask.Relative != relative ||
		mask.Density == nil || *mask.Density != 128 || mask.Feather == nil || *mask.Feather != 2.5 ||
		layer.VectorDensity == nil || *layer.VectorDensity != 211 ||
		layer.VectorFeather == nil || *layer.VectorFeather != 0 {
		t.Fatalf("wrong effective mask metadata: %+v", mask)
	}
	if pixels == nil {
		return
	}
	if mask.Pixels == nil {
		t.Fatal("missing mask pixels")
	}
	for index, sample := range pixels {
		if mask.Pixels.Pix[4*index] != sample {
			t.Fatalf("mask pixels changed: %v", mask.Pixels.Pix)
		}
	}
}

func TestMaskParametersRejectTruncatedRealHeaderAndParameters(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, real := range []bool{false, true} {
			payload := maskParametersPayload(real)
			cuts := []int{len(payload) - 1}
			if real {
				cuts = append(cuts, 0, 18, 35)
			}
			for _, cut := range cuts {
				t.Run(fmt.Sprintf("version%d/real%t/bytes%d", version, real, cut), func(t *testing.T) {
					data := maskParametersDocument(version, real, payload[:cut])
					if document, err := Parse(data); err == nil {
						document.Close()
						t.Fatal("indexed parser accepted a truncated mask")
					}
					if _, err := psdparse.ParseBytes(data); err == nil {
						t.Fatal("eager parser accepted a truncated mask")
					}
				})
			}
		}
	}
}
