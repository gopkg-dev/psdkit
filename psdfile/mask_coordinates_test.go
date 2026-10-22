package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"testing"
)

func maskCoordinatesFixture(version, depth uint16, flags byte, origin image.Point, real bool) []byte {
	maskBounds := image.Rect(2, 3, 5, 5)
	realBounds := image.Rect(-2, 1, 1, 3)
	var mask bytes.Buffer
	for _, edge := range []int{maskBounds.Min.Y, maskBounds.Min.X, maskBounds.Max.Y, maskBounds.Max.X} {
		indexTestNumber(&mask, int32(edge))
	}
	storedFlags := flags
	if real {
		storedFlags |= 8
	}
	mask.Write([]byte{0, storedFlags})
	if real {
		mask.Write([]byte{flags, 255})
		for _, edge := range []int{realBounds.Min.Y, realBounds.Min.X, realBounds.Max.Y, realBounds.Max.X} {
			indexTestNumber(&mask, int32(edge))
		}
	} else {
		mask.Write([]byte{0xA1, 0xB2})
	}
	encode := func(values []byte) []byte {
		var plane bytes.Buffer
		plane.Write([]byte{0, 0})
		for _, value := range values {
			switch depth {
			case 8:
				plane.WriteByte(value)
			case 16:
				indexTestNumber(&plane, uint16(value)*257)
			case 32:
				indexTestNumber(&plane, math.Float32bits(float32(value)/255))
			}
		}
		return plane.Bytes()
	}
	ids := []int16{0, 1, 2, -1, -2}
	channels := [][]byte{
		encode([]byte{10, 20, 30, 40, 50, 60}), encode([]byte{70, 80, 90, 100, 110, 120}),
		encode([]byte{130, 140, 150, 160, 170, 180}), encode(bytes.Repeat([]byte{255}, 6)),
		encode([]byte{0, 64, 128, 192, 255, 80}),
	}
	if real {
		ids = append(ids, -3)
		channels = append(channels, encode([]byte{255, 192, 128, 64, 0, 160}))
	}
	layers := indexTestLayerSet(version, image.Rectangle{Min: origin, Max: origin.Add(image.Pt(3, 2))},
		ids, channels, mask.Bytes(), nil)
	global := indexTestGlobal(version, layers, nil)
	var data bytes.Buffer
	data.WriteString("8BPS")
	indexTestNumber(&data, version)
	data.Write(make([]byte, 6))
	indexTestNumber(&data, uint16(3))
	indexTestNumber(&data, uint32(12))
	indexTestNumber(&data, uint32(12))
	indexTestNumber(&data, depth)
	indexTestNumber(&data, uint16(3))
	indexTestNumber(&data, uint32(0))
	indexTestNumber(&data, uint32(0))
	indexTestLength(&data, version, len(global))
	data.Write(global)
	data.Write([]byte{0, 0})
	data.Write(make([]byte, 12*12*3*int(depth/8)))
	return data.Bytes()
}

func TestMaskCoordinatesStoredDocumentDomainAcrossReadAPIs(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, flags := range []byte{0, 1} {
			for _, origin := range []image.Point{{}, {5, 7}, {-4, -3}} {
				name := fmt.Sprintf("v%d/flags%d/origin%d_%d", version, flags, origin.X, origin.Y)
				t.Run(name, func(t *testing.T) {
					data := maskCoordinatesFixture(version, 8, flags, origin, false)
					source, err := Parse(data)
					if err != nil {
						t.Fatal(err)
					}
					defer source.Close()
					wantBounds := image.Rect(2, 3, 5, 5)
					record := source.Layers[0]
					if record.Mask.Bounds != wantBounds || record.Mask.Flags != flags ||
						record.Channels[4].Bounds != wantBounds {
						t.Fatalf("raw indexed geometry changed: %+v", record.Mask)
					}
					var raw bytes.Buffer
					if _, err := source.CopySpan(nil, &raw, record.Mask.Span); err != nil {
						t.Fatal(err)
					}
					if raw.Bytes()[17] != flags || int32(binary.BigEndian.Uint32(raw.Bytes()[4:8])) != 2 {
						t.Fatalf("raw mask bytes changed: %x", raw.Bytes())
					}
					view, err := source.ReadLayerMetadata(nil, 0)
					if err != nil || view.Layer.Mask.Bounds != wantBounds || view.Layer.Mask.Relative != (flags == 1) ||
						view.Layer.Mask.Pixels != nil || view.PixelMaskChannel != -2 {
						t.Fatalf("metadata mask domain differs: %+v / %v", view, err)
					}
					for _, withPreview := range []bool{false, true} {
						decoded, err := source.decodeRGB8(nil, withPreview)
						if err != nil {
							t.Fatal(err)
						}
						mask := decoded.Layers[0].Mask
						if mask.Bounds != wantBounds || mask.Pixels.Bounds().Size() != wantBounds.Size() ||
							mask.Relative != (flags == 1) || mask.Pixels.Pix[4] != 64 {
							t.Fatalf("decoded mask shifted by layer origin: %+v", mask)
						}
						mask.Pixels.Pix[0] = 173
					}
					plane, err := source.DecodeChannel(nil, 0, 4)
					if err != nil || !bytes.Equal(plane.Data, []byte{0, 64, 128, 192, 255, 80}) {
						t.Fatalf("source mask pixels changed through decoded caller mutation: %+v / %v", plane, err)
					}
				})
			}
		}
	}
}

func TestMaskCoordinatesRealMaskAndSourcePrecision(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, depth := range []uint16{8, 16, 32} {
			for _, flags := range []byte{0, 1} {
				name := fmt.Sprintf("v%d/depth%d/flags%d", version, depth, flags)
				t.Run(name, func(t *testing.T) {
					source, err := Parse(maskCoordinatesFixture(version, depth, flags, image.Pt(-4, -3), true))
					if err != nil {
						t.Fatal(err)
					}
					defer source.Close()
					view, err := source.ReadLayerMetadata(nil, 0)
					if err != nil || view.PixelMaskChannel != -3 || view.Layer.Mask.Bounds != image.Rect(-2, 1, 1, 3) ||
						view.Layer.Mask.Relative != (flags == 1) || *view.Layer.Mask.DefaultColor != 255 {
						t.Fatalf("real source-mask metadata changed: %+v / %v", view, err)
					}
					for _, channelIndex := range []int{4, 5} {
						plane, err := source.DecodeChannel(nil, 0, channelIndex)
						if err != nil || plane.Width != 3 || plane.Height != 2 || plane.Depth != depth {
							t.Fatalf("mask dimensions/depth changed: %+v / %v", plane, err)
						}
						region, err := source.DecodeChannelRegion(nil, 0, channelIndex, image.Rect(1, 0, 3, 2))
						if err != nil {
							t.Fatal(err)
						}
						stride := int(depth / 8)
						want := append(bytes.Clone(plane.Data[stride:3*stride]), plane.Data[4*stride:6*stride]...)
						if !bytes.Equal(region.Data, want) {
							t.Fatalf("mask-local ROI offset changed: %x != %x", region.Data, want)
						}
					}
					pixels, err := source.DecodeLayer(nil, 0)
					if err != nil || len(pixels.Channels) != 6 {
						t.Fatalf("source-precision complete layer failed: %+v / %v", pixels, err)
					}
				})
			}
		}
	}
}

type maskCoordinatesGuard struct {
	data      []byte
	forbidden []Span
	cause     error
}

func (reader maskCoordinatesGuard) ReadAt(data []byte, offset int64) (int, error) {
	for _, span := range reader.forbidden {
		if offset < span.Offset+span.Length && offset+int64(len(data)) > span.Offset {
			return 0, errors.New("mask metadata read attempted deferred source pixels")
		}
	}
	count, err := bytes.NewReader(reader.data).ReadAt(data, offset)
	if reader.cause != nil && count != 0 {
		return count, reader.cause
	}
	return count, err
}

func TestMaskCoordinatesMetadataReadOnlyAndErrors(t *testing.T) {
	data := maskCoordinatesFixture(2, 8, 1, image.Pt(-4, -3), false)
	source, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	guard := maskCoordinatesGuard{data: data, forbidden: []Span{source.Composite.Span}}
	for _, channel := range source.Layers[0].Channels {
		guard.forbidden = append(guard.forbidden, channel.Span)
	}
	source.reader = guard
	view, err := source.ReadLayerMetadata(nil, 0)
	if err != nil || view.Layer.Mask.Bounds != image.Rect(2, 3, 5, 5) {
		t.Fatalf("read-only mask view failed: %+v / %v", view, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.ReadLayerMetadata(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled source cause lost: %v", err)
	}
	guard.cause = io.ErrUnexpectedEOF
	source.reader = guard
	if _, err := source.ReadLayerMetadata(nil, 0); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("full-read source error cause lost: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if view.Layer.Mask.Bounds != image.Rect(2, 3, 5, 5) {
		t.Fatal("closed source invalidated owned mask metadata")
	}
	if _, err := source.ReadLayerMetadata(nil, 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed source accepted: %v", err)
	}
}
