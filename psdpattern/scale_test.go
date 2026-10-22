package psdpattern

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"strconv"
	"testing"
)

func TestPatternScaleFiltersStraightHiddenRGBAndPreservesSource(t *testing.T) {
	tile := image.NewNRGBA(image.Rect(7, 9, 9, 11))
	copy(tile.Pix, []byte{200, 0, 0, 255, 0, 200, 0, 0, 0, 0, 200, 128, 200, 200, 200, 64})
	original := bytes.Clone(tile.Pix)
	half, err := Render(nil, tile, Fill{Scale: 50}, image.Rect(0, 0, 2, 2))
	if err != nil || half.NRGBAAt(0, 0) != (color.NRGBA{100, 100, 100, 112}) {
		t.Fatalf("wrapped straight-RGBA box filter lost hidden color samples: %v", err)
	}
	double, err := Render(nil, tile, Fill{Scale: 200}, image.Rect(0, 0, 4, 4))
	if err != nil || double.NRGBAAt(0, 0) != tile.NRGBAAt(7, 9) ||
		double.NRGBAAt(1, 0) != (color.NRGBA{100, 100, 0, 128}) ||
		double.NRGBAAt(1, 1) != (color.NRGBA{100, 100, 100, 112}) ||
		double.NRGBAAt(3, 0) != double.NRGBAAt(1, 0) {
		t.Fatalf("origin anchors, halfway interpolation, alpha or wrapped edge differs: %v", err)
	}
	clear(half.Pix)
	clear(double.Pix)
	if !bytes.Equal(original, tile.Pix) {
		t.Fatal("scaled output borrowed or mutated source samples")
	}
}

func TestPatternScaledOddPeriodPhaseCropAndConstantWorkingStorage(t *testing.T) {
	backing := image.NewNRGBA(image.Rect(0, 0, 6, 4))
	tile := backing.SubImage(image.Rect(1, 2, 4, 3)).(*image.NRGBA)
	for index, red := range []byte{0, 80, 200} {
		tile.SetNRGBA(index+1, 2, color.NRGBA{R: red, A: 255})
	}
	for _, control := range []struct {
		scale  float64
		period []byte
	}{{50, []byte{40, 100, 140}}, {200, []byte{0, 40, 80, 140, 200, 100}}} {
		fill := Fill{Scale: control.scale, Phase: Phase{X: 1, Y: -1}}
		bounds, crop := image.Rect(-3, -2, 6, 4), image.Rect(-1, -1, 2, 2)
		full, err := Render(nil, tile, fill, bounds)
		if err != nil {
			t.Fatal(err)
		}
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			expected := control.period[modulo(int64(x-1), int64(len(control.period)))]
			if full.NRGBAAt(x, 0).R != expected {
				t.Fatal("integer document phase, odd source period or negative wrapping differs")
			}
		}
		clipped, err := Render(nil, tile, fill, bounds, Options{Crop: &crop, MaxPixels: 9, MaxWorkingBytes: 36})
		if err != nil {
			t.Fatalf("lazy sampling required an intermediate resized tile allocation: %v", err)
		}
		for y := crop.Min.Y; y < crop.Max.Y; y++ {
			for x := crop.Min.X; x < crop.Max.X; x++ {
				if clipped.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
					t.Fatal("output cropping shifted the resized tile origin")
				}
			}
		}
		if _, err := Render(nil, tile, fill, bounds,
			Options{Crop: &crop, MaxWorkingBytes: 35}); !errors.Is(err, ErrLimit) {
			t.Fatalf("owned output escaped the scaled working budget: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Render(ctx, tile, fill, bounds); !errors.Is(err, context.Canceled) {
			t.Fatalf("scaled sampling ignored cancellation: %v", err)
		}
	}
}

func TestPatternScaledLargeDocumentCoordinatesKeepExactIntegerPhase(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("coordinate exceeds a 32-bit image.Rectangle")
	}
	var distance int64 = 1 << 40
	tile := patternTestTile()
	for _, scale := range []float64{50, 200} {
		fill := Fill{Scale: scale, Phase: Phase{X: float64(distance), Y: -float64(distance)}}
		bounds := image.Rect(int(distance)-1, -int(distance), int(distance), -int(distance)+1)
		local, err := Render(nil, tile, Fill{Scale: scale}, image.Rect(-1, 0, 0, 1))
		if err != nil {
			t.Fatal(err)
		}
		pixels, err := Render(nil, tile, fill, bounds, Options{MaxPixels: 1, MaxWorkingBytes: 4})
		if err != nil || pixels.NRGBAAt(bounds.Min.X, bounds.Min.Y) != local.NRGBAAt(-1, 0) {
			t.Fatalf("large coordinates lost integer wrapping precision: %v", err)
		}
	}
}
