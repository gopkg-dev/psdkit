package psdkit_test

import (
	"context"
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit"
)

func TestDisplayRegionKeepsAbsoluteCoordinatesAndSkipsFullCanvasBudget(t *testing.T) {
	data := displayFixtureBytes(t, displaySourceFixture{mode: 3, depth: 8, width: 4, height: 2,
		planes: [][]byte{{10, 20, 30, 40, 50, 60, 70, 80}, {11, 21, 31, 41, 51, 61, 71, 81}, {12, 22, 32, 42, 52, 62, 72, 82}}})
	source, err := psdkit.ParseDocument(data, psdkit.LoadOptions{Limits: psdkit.SourceLimits{MaxDecodePixels: 2}})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := psdkit.DecodeDisplayComposite(context.Background(), source); !errors.Is(err, psdkit.ErrColorLimit) {
		t.Fatalf("full canvas must exceed pixel limit: %v", err)
	}
	bounds := image.Rect(1, 1, 3, 2)
	tile, err := psdkit.DecodeDisplayRegion(context.Background(), source, bounds)
	if err != nil {
		t.Fatal(err)
	}
	if tile.Rect != bounds || tile.NRGBAAt(1, 1) != (color.NRGBA{R: 60, G: 61, B: 62, A: 255}) ||
		tile.NRGBAAt(2, 1) != (color.NRGBA{R: 70, G: 71, B: 72, A: 255}) {
		t.Fatalf("absolute tile pixels/geometry = %+v", tile)
	}
	tile.Pix[0] = 0
	again, err := psdkit.DecodeDisplayRegion(context.Background(), source, bounds)
	if err != nil || again.Pix[0] != 60 {
		t.Fatalf("display tiles alias source state: %v", err)
	}
}

func TestDisplayRegionBudgetsCancellationAndInvalidRectangles(t *testing.T) {
	data := displayFixtureBytes(t, displaySourceFixture{mode: 3, planes: [][]byte{{20}, {30}, {40}}})
	source, err := psdkit.ParseDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := psdkit.DecodeDisplayRegion(context.Background(), source, image.Rect(0, 0, 1, 1),
		psdkit.RegionDisplayOptions{Decode: psdkit.RegionOptions{MaxWorkingBytes: 6}}); !errors.Is(err, psdkit.ErrColorLimit) {
		t.Fatalf("plane plus RGBA storage exceeded budget: %v", err)
	}
	for _, bounds := range []image.Rectangle{{Min: image.Pt(-1, 0), Max: image.Pt(1, 1)},
		{Min: image.Pt(1, 0), Max: image.Pt(0, 1)}, image.Rect(0, 0, 2, 1)} {
		if _, err := psdkit.DecodeDisplayRegion(context.Background(), source, bounds); !errors.Is(err, psdkit.ErrColorInvalid) {
			t.Fatalf("invalid region %v accepted: %v", bounds, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := psdkit.DecodeDisplayRegion(ctx, source, image.Rect(0, 0, 1, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled region = %v", err)
	}
	_ = source.Close()
	if _, err := psdkit.DecodeDisplayRegion(context.Background(), source, image.Rect(0, 0, 1, 1)); !errors.Is(err, psdkit.ErrClosed) {
		t.Fatalf("closed region = %v", err)
	}
}

func TestDisplayRegionOptionalNativeColorContextMatchesStoredFullPreview(t *testing.T) {
	files, err := filepath.Glob("output/p6-native/*.psd")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Skip("optional Photoshop color fixtures are absent")
	}
	for _, filename := range files {
		t.Run(filepath.Base(filename), func(t *testing.T) {
			if _, err := os.Stat(filename); err != nil {
				t.Fatal(err)
			}
			source, err := psdkit.OpenDocument(filename)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			option := psdkit.DisplayOptions{}
			if filepath.Base(filename) == "cmyk8-unmanaged.psd" || filepath.Base(filename) == "cmyk16-unmanaged.psd" {
				option.IgnoreSourceProfile = true
				option.Color.AllowUnmanagedCMYK = true
			}
			full, err := psdkit.DecodeDisplayComposite(context.Background(), source, option)
			if err != nil {
				t.Fatal(err)
			}
			bounds := image.Rect(0, 0, min(3, full.Rect.Dx()), min(2, full.Rect.Dy()))
			tile, err := psdkit.DecodeDisplayRegion(context.Background(), source, bounds,
				psdkit.RegionDisplayOptions{Display: option})
			if err != nil {
				t.Fatal(err)
			}
			for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
				for x := bounds.Min.X; x < bounds.Max.X; x++ {
					if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
						t.Fatalf("tile/full color differs at %d,%d", x, y)
					}
				}
			}
		})
	}
}
