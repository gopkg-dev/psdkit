package psdscene_test

import (
	"encoding/binary"
	"image"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdscene"
)

func TestPrecisionDisabledMaskKeepsCalibratedNormalDifferenceDomain(t *testing.T) {
	for _, mode := range []string{"norm", "diff"} {
		bounds := image.Rect(0, 0, 2, 1)
		layers := []precisionLayerFixture{
			{bounds: bounds, opacity: 255, channels: integer16TwoPixelChannels(
				[2]uint16{32767, 32767}, [2]uint16{0, 32768})},
			{bounds: bounds, opacity: 161, mode: mode, channels: integer16TwoPixelChannels(
				[2]uint16{49151, 49151}, [2]uint16{0, 49151})},
		}
		plain, _ := precisionSource(t, precisionSourceFixture(16, 3, 2, 1, layers))
		expected, err := psdscene.RenderPrecision(nil, plain)
		if err != nil {
			t.Fatal(err)
		}
		for index := range layers {
			layers[index].mask = precisionMask(bounds, 0, 2, nil, false)
			layers[index].channels = append(layers[index].channels, precisionChannel{id: -2,
				data: []byte{0, 0, 0, 0}})
		}
		source, reader := precisionSource(t, precisionSourceFixture(16, 3, 2, 1, layers))
		reader.pixelSpans = nil
		reader.forbid = true
		for _, layer := range source.Layers {
			for _, channel := range layer.Channels {
				if channel.ID == -2 {
					reader.pixelSpans = append(reader.pixelSpans, channel.Data)
				}
			}
		}
		full, err := psdscene.RenderPrecision(nil, source)
		if err != nil || !reflect.DeepEqual(full.Pix, expected.Pix) || reader.pixelReads != 0 {
			t.Fatalf("disabled %s mask changed Q15 output or decoded mask samples: %v", mode, err)
		}
		tile, err := psdscene.RenderPrecisionTile(nil, source, image.Rect(1, 0, 2, 1))
		if err != nil || !reflect.DeepEqual(tile.Image.Pix, expected.Pix[4:]) || reader.pixelReads != 0 {
			t.Fatalf("disabled mask changed source domain in ROI: %v", err)
		}
	}
}

func TestPrecisionUnknownDisabledMaskDoesNotUpgradeIntegerQualification(t *testing.T) {
	bounds := image.Rect(0, 0, 1, 1)
	for _, flags := range []byte{0x22, 0x42, 0x82} {
		layer := precisionLayerFixture{bounds: bounds, opacity: 255,
			channels: precisionRGBA(16, [4]float64{32767, 32767, 32767, 32768}),
			mask:     precisionMask(bounds, 0, flags, nil, false)}
		layer.channels = append(layer.channels, precisionChannel{id: -2, data: binary.BigEndian.AppendUint16(nil, 0)})
		source, _ := precisionSource(t, precisionSourceFixture(16, 3, 1, 1, []precisionLayerFixture{layer}))
		pixels, err := psdscene.RenderPrecision(nil, source)
		if err != nil || pixels.Pix[0] != float64(32767)/65535 {
			t.Fatalf("unknown disabled mask unexpectedly upgraded the established float fallback: %v", err)
		}
		if source.Layers[0].Mask.Flags != flags {
			t.Fatal("qualification changed original unknown mask flags")
		}
	}
}
