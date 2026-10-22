package psdpattern

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"testing"
)

func patternTestTile() *image.NRGBA {
	result := image.NewNRGBA(image.Rect(7, 9, 9, 11))
	copy(result.Pix, []byte{10, 20, 30, 255, 40, 50, 60, 128, 70, 80, 90, 0, 100, 110, 120, 64})
	return result
}

func TestPatternRepeatPreservesTileRGBAAndDocumentCropCoordinates(t *testing.T) {
	tile := patternTestTile()
	before := bytes.Clone(tile.Pix)
	fill := Fill{Scale: 100}
	bounds, crop := image.Rect(-3, -2, 5, 4), image.Rect(-1, -1, 3, 2)
	full, err := Render(nil, tile, fill, bounds)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Render(nil, tile, fill, bounds, Options{Crop: &crop})
	if err != nil || result.Rect != crop {
		t.Fatalf("owned cropped output differs: %v", err)
	}
	for y := crop.Min.Y; y < crop.Max.Y; y++ {
		for x := crop.Min.X; x < crop.Max.X; x++ {
			if result.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
				t.Fatal("crop moved or stretched the periodic tile origin")
			}
		}
	}
	fill.Phase = Phase{X: -1, Y: 1}
	shifted, err := Render(nil, tile, fill, bounds)
	if err != nil || shifted.NRGBAAt(0, 0) != tile.NRGBAAt(8, 10) {
		t.Fatalf("negative modulo or pixel phase differs: %v", err)
	}
	clear(full.Pix)
	clear(result.Pix)
	if !bytes.Equal(before, tile.Pix) {
		t.Fatal("pattern renderer mutated or borrowed the caller tile")
	}
}

func TestPatternLimitsUnknownSamplingStrideOverflowAndCancellation(t *testing.T) {
	tile, bounds, fill := patternTestTile(), image.Rect(0, 0, 2, 2), Fill{Scale: 100}
	for _, options := range []Options{{MaxPixels: 1}, {MaxWorkingBytes: 4}} {
		if _, err := Render(nil, tile, fill, bounds, options); !errors.Is(err, ErrLimit) {
			t.Fatalf("output budget ignored: %v", err)
		}
	}
	for _, invalid := range []Fill{{Scale: 0}, {Scale: math.NaN()}, {Scale: 100, Phase: Phase{X: math.Inf(1)}}} {
		if _, err := Render(nil, tile, invalid, bounds); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid pattern fill accepted: %v", err)
		}
	}
	for _, unsupported := range []Fill{{Scale: 80}, {Scale: 150}, {Scale: 100, Angle: 15},
		{Scale: 50, Angle: 90}, {Scale: 200, Angle: 180},
		{Scale: 100, Angle: 90}, {Scale: 100, Angle: 180}, {Scale: 100, Angle: 270},
		{Scale: 100, Phase: Phase{X: .5}}, {Scale: 100, Unsupported: []string{"unknown"}}} {
		if _, err := Render(nil, tile, unsupported, bounds); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unverified sampling semantics approximated: %v", err)
		}
	}
	broken := &image.NRGBA{Rect: image.Rect(0, 0, 1, 3), Stride: math.MaxInt, Pix: make([]byte, 12)}
	if _, err := Render(nil, broken, fill, bounds); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overflowing source stride accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Render(ctx, tile, fill, bounds); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled repeat rendering accepted: %v", err)
	}
}
