package psdvector

import (
	"bytes"
	"errors"
	"image"
	"testing"
)

func TestRasterizeCombineFastPathRetainsSampleUnion(t *testing.T) {
	for _, rule := range []FillRule{EvenOdd, NonZero} {
		for _, invert := range []bool{false, true} {
			outer := rectangleSubpath(-2.375, -1.25, 3.125, 4.625, 1)
			hole := rectangleSubpath(-.375, .75, 1.125, 2.625, -1)
			if rule == NonZero {
				slicesReverseKnots(hole.Knots)
			}
			second := rectangleSubpath(.625, -2.375, 4.375, 1.75, 1)
			mask := &Mask{Width: 1, Height: 1, FillRule: rule, Invert: invert,
				Subpaths: []Subpath{outer, hole, second}}
			bounds := image.Rect(-4, -4, 6, 6)
			actual, err := Rasterize(nil, mask, bounds, Options{Samples: 8})
			if err != nil {
				t.Fatal(err)
			}
			for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
				for x := bounds.Min.X; x < bounds.Max.X; x++ {
					count := 0
					for subY := range 8 {
						for subX := range 8 {
							xp, yp := float64(x)+(float64(subX)+.5)/8, float64(y)+(float64(subY)+.5)/8
							first := xp >= -2.375 && xp < 3.125 && yp >= -1.25 && yp < 4.625
							counter := xp >= -.375 && xp < 1.125 && yp >= .75 && yp < 2.625
							other := xp >= .625 && xp < 4.375 && yp >= -2.375 && yp < 1.75
							if ((first && !counter) || other) != invert {
								count++
							}
						}
					}
					expected := byte((count*255 + 32) / 64)
					if actual.AlphaAt(x, y).A != expected {
						t.Fatalf("rule=%d invert=%v (%d,%d): got %d; oracle %d", rule, invert, x, y,
							actual.AlphaAt(x, y).A, expected)
					}
				}
			}
		}
	}
}

func TestRasterizeCombineWorkRemainsBounded(t *testing.T) {
	mask := &Mask{Width: 1, Height: 1}
	for index := range 64 {
		mask.Subpaths = append(mask.Subpaths, rectangleSubpath(float64(index)/16, 0, float64(index)/16+2, 2, 1))
	}
	bounds := image.Rect(-1, -1, 8, 4)
	// 256 edges times five rows times four sample rows uses 5120 pure-union edge steps.
	actual, err := Rasterize(nil, mask, bounds, Options{Samples: 4, MaxScanlineWork: 5120})
	if err != nil {
		t.Fatal(err)
	}
	expected, err := Rasterize(nil, mask, bounds, Options{Samples: 4})
	if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
		t.Fatalf("bounded fast path differs from regular union: %v", err)
	}
	result, err := Rasterize(nil, mask, bounds, Options{Samples: 4, MaxScanlineWork: 5119})
	if result != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("pure union escaped work limit: output=%v error=%v", result != nil, err)
	}
	mask.Subpaths[len(mask.Subpaths)-1].Operation = 2
	result, err = Rasterize(nil, mask, bounds, Options{Samples: 4, MaxScanlineWork: 5120})
	if result != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("mixed Boolean component used the union quota: output=%v error=%v", result != nil, err)
	}
}
