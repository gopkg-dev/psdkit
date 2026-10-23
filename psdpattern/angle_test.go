package psdpattern

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"testing"
)

func TestPatternWholeTurnsUseExactAxisSamplingAndCrop(t *testing.T) {
	tile := patternTestTile()
	original := bytes.Clone(tile.Pix)
	bounds, crop := image.Rect(-5, -3, 6, 7), image.Rect(-2, 1, 3, 4)
	for _, scale := range []float64{50, 100, 200} {
		fill := Fill{Scale: scale, Phase: Phase{X: -3, Y: 2}}
		expected, err := Render(nil, tile, fill, bounds)
		if err != nil {
			t.Fatal(err)
		}
		for _, angle := range []float64{-720, -360, math.Copysign(0, -1), 360, 720, 360 * (1 << 30)} {
			fill.Angle = angle
			actual, err := Render(nil, tile, fill, bounds)
			if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
				t.Fatalf("scale %g, angle %g changed exact axis samples: %v", scale, angle, err)
			}
			clipped, err := Render(nil, tile, fill, bounds,
				Options{Crop: &crop, MaxPixels: 15, MaxWorkingBytes: 60})
			if err != nil {
				t.Fatal(err)
			}
			for y := crop.Min.Y; y < crop.Max.Y; y++ {
				for x := crop.Min.X; x < crop.Max.X; x++ {
					if clipped.NRGBAAt(x, y) != expected.NRGBAAt(x, y) {
						t.Fatal("whole-turn angle shifted the cropped pattern origin")
					}
				}
			}
			clear(actual.Pix)
			clear(clipped.Pix)
		}
	}
	if !bytes.Equal(tile.Pix, original) {
		t.Fatal("whole-turn rendering borrowed or mutated the source tile")
	}
}

func TestPatternWholeTurnsKeepStrictAngleBudgetAndCancellationGuards(t *testing.T) {
	tile, bounds := patternTestTile(), image.Rect(0, 0, 4, 4)
	for _, scale := range []float64{50, 100, 200} {
		for _, angle := range []float64{math.Nextafter(360, 0), math.Nextafter(360, 720),
			math.Nextafter(-360, 0), 90, 180, 270, 450, -90} {
			result, err := Render(nil, tile, Fill{Scale: scale, Angle: angle}, bounds)
			if result != nil || !errors.Is(err, ErrUnsupported) {
				t.Fatalf("scale %g accepted nonintegral-turn angle %g: %v", scale, angle, err)
			}
		}
		fill := Fill{Scale: scale, Angle: 360}
		for _, options := range []Options{{MaxPixels: 15}, {MaxWorkingBytes: 63}} {
			if result, err := Render(nil, tile, fill, bounds, options); result != nil || !errors.Is(err, ErrLimit) {
				t.Fatalf("whole-turn angle bypassed output budget: %v", err)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if result, err := Render(ctx, tile, fill, bounds); result != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("whole-turn angle bypassed cancellation: %v", err)
		}
	}
}
