package psdscene_test

import (
	"errors"
	"image"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func TestPrecisionSourceClippingAndExtendedBlendClippedFlag(t *testing.T) {
	backdrop := precisionLayerFixture{bounds: image.Rect(0, 0, 1, 1), opacity: 255,
		channels: precisionRGBA(32, [4]float64{.2, .4, .6, 1})}
	grouped := byte(0)
	base := precisionLayerFixture{bounds: image.Rect(0, 0, 1, 1), opacity: 255, mode: "mul ", flags: 0x28,
		clbl: &grouped, channels: precisionRGBA(32, [4]float64{.8, .5, .25, .5})}
	top := precisionLayerFixture{bounds: image.Rect(0, 0, 1, 1), opacity: 255, clipping: 1,
		channels: precisionRGBA(32, [4]float64{1.5, -.25, .75, .5})}
	source, _ := precisionSource(t, precisionSourceFixture(32, 3, 1, 1,
		[]precisionLayerFixture{backdrop, base, top}))
	result, err := psdscene.RenderPrecision(nil, source)
	if err != nil {
		t.Fatal(err)
	}
	for index, expected := range []float64{.515, .1875, .525, 1} {
		if math.Abs(result.Pix[index]-expected) > 1e-7 {
			t.Fatalf("source ungrouped clipping sample %d = %.12g, want %.12g", index, result.Pix[index], expected)
		}
	}
	grouped = 1
	base.flags = 8
	source, _ = precisionSource(t, precisionSourceFixture(32, 3, 1, 1,
		[]precisionLayerFixture{backdrop, base, top}))
	result, err = psdscene.RenderPrecision(nil, source)
	if err != nil || math.Abs(result.Pix[0]-.215) > 1e-7 {
		t.Fatalf("source grouped clipping applied base blend before top: %v, %v", result, err)
	}
}

func TestPrecisionSourceCustomRangesRetainContinuousSamples(t *testing.T) {
	base := precisionLayerFixture{bounds: image.Rect(0, 0, 1, 1), opacity: 255, flags: 0x28,
		ranges:   []byte{0, 0, 255, 255, 0, 0, 255, 255, 128, 128, 255, 255, 0, 0, 255, 255},
		channels: precisionRGBA(32, [4]float64{127.99 / 255, 0, 0, .5})}
	source, _ := precisionSource(t, precisionSourceFixture(32, 3, 1, 1, []precisionLayerFixture{base}))
	result, err := psdscene.RenderPrecision(nil, source)
	if err != nil || result.Pix[3] != 0 {
		t.Fatalf("high-depth source range rounded fractional source to a byte: %v, %v", result, err)
	}
	base.channels = precisionRGBA(32, [4]float64{128.01 / 255, 0, 0, .5})
	source, _ = precisionSource(t, precisionSourceFixture(32, 3, 1, 1, []precisionLayerFixture{base}))
	result, err = psdscene.RenderPrecision(nil, source)
	if err != nil || result.Pix[3] != .5 {
		t.Fatalf("high-depth range did not retain values above threshold: %v, %v", result, err)
	}
}

func TestPrecisionSourceClippingMetadataPreflight(t *testing.T) {
	base := precisionLayerFixture{bounds: image.Rect(0, 0, 1, 1), opacity: 255,
		channels: precisionRGBA(32, [4]float64{1, 0, 0, 1})}
	for _, invalid := range []struct {
		name   string
		change func(*precisionLayerFixture)
		kind   error
	}{
		{"unknown clipping", func(layer *precisionLayerFixture) { layer.clipping = 2 }, psdfile.ErrUnsupported},
		{"unknown flag", func(layer *precisionLayerFixture) { layer.flags = 0x20 }, psdfile.ErrUnsupported},
		{"invalid clbl", func(layer *precisionLayerFixture) { value := byte(2); layer.clbl = &value },
			psdfile.ErrMalformed},
		{"unordered split", func(layer *precisionLayerFixture) {
			layer.ranges = []byte{100, 20, 255, 255, 0, 0, 255, 255}
		}, psdfile.ErrMalformed},
		{"non RGB channel", func(layer *precisionLayerFixture) {
			for range 5 {
				layer.ranges = append(layer.ranges, 0, 0, 255, 255, 0, 0, 255, 255)
			}
			layer.ranges[32] = 1
			layer.ranges[33] = 1
		}, psdfile.ErrUnsupported},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			fixture := base
			invalid.change(&fixture)
			source, reader := precisionSource(t,
				precisionSourceFixture(32, 3, 1, 1, []precisionLayerFixture{fixture}))
			reader.forbid = true
			if _, err := psdscene.RenderPrecision(nil, source); !errors.Is(err, invalid.kind) || reader.pixelReads != 0 {
				t.Fatalf("invalid clipping metadata reached sample decode: %v, reads=%d", err, reader.pixelReads)
			}
		})
	}
}
