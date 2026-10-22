package psdcompose

import (
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdpattern"
)

func compareComposeRegions(t *testing.T, document *psdmodel.Document, options ...Options) {
	t.Helper()
	full, err := Render(nil, document, options...)
	if err != nil {
		t.Fatal(err)
	}
	for _, rectangle := range []image.Rectangle{
		image.Rect(1, 1, document.Width-1, document.Height-1),
		image.Rect(0, 0, 3, 3), image.Rect(document.Width-3, document.Height-3, document.Width, document.Height),
	} {
		window, err := RenderRegion(nil, document, rectangle, options...)
		if err != nil {
			t.Fatal(err)
		}
		if window.Rect != rectangle {
			t.Fatal("compose window lost its absolute document coordinates")
		}
		for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
			for x := rectangle.Min.X; x < rectangle.Max.X; x++ {
				if window.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
					t.Fatalf("ROI %v at (%d,%d) = %v, full %v", rectangle, x, y,
						window.NRGBAAt(x, y), full.NRGBAAt(x, y))
				}
			}
		}
	}
}

func TestComposeRegionKeepsProceduralOriginsAndFeatherHalo(t *testing.T) {
	t.Run("gradient", func(t *testing.T) {
		layer := &psdmodel.Layer{Bounds: image.Rect(2, 2, 7, 5), Gradient: liveGradientFixture(), BlendMode: "norm"}
		layer.Gradient.Align = true
		compareComposeRegions(t, &psdmodel.Document{Width: 11, Height: 9, Layers: []*psdmodel.Layer{layer}})
	})
	t.Run("pattern", func(t *testing.T) {
		tile := image.NewNRGBA(image.Rect(0, 0, 3, 2))
		for y := range 2 {
			for x := range 3 {
				tile.SetNRGBA(x, y, color.NRGBA{R: byte(x*73 + 11), G: byte(y*113 + 17), A: 255})
			}
		}
		fill := &psdpattern.Fill{Scale: 200}
		fill.Phase.X, fill.Phase.Y = 3, 1
		layer := &psdmodel.Layer{PatternTile: tile, Pattern: fill, BlendMode: "norm"}
		compareComposeRegions(t, &psdmodel.Document{Width: 11, Height: 9, Layers: []*psdmodel.Layer{layer}})
	})
	t.Run("pixel-feather", func(t *testing.T) {
		document, _ := featherTestDocument(1.5)
		compareComposeRegions(t, document)
	})
	t.Run("vector-feather", func(t *testing.T) {
		document, _ := vectorFeatherTestDocument(3)
		compareComposeRegions(t, document)
	})
	t.Run("dissolve", func(t *testing.T) {
		layer := composeTestLayer(image.Rect(0, 0, 11, 9), color.NRGBA{R: 201, G: 113, B: 73, A: 173})
		layer.BlendMode = "diss"
		seed := uint64(4917)
		compareComposeRegions(t, &psdmodel.Document{Width: 11, Height: 9, Layers: []*psdmodel.Layer{layer}},
			Options{DissolveSeed: &seed})
	})
}

func TestComposeRegionHugeCanvasBudgetsOnlyWindowAndCaches(t *testing.T) {
	document := &psdmodel.Document{Width: 300_000, Height: 300_000,
		Layers: []*psdmodel.Layer{{Gradient: liveGradientFixture(), BlendMode: "norm"}}}
	if _, err := Render(nil, document, Options{MaxWorkingBytes: 64 << 10}); !errors.Is(err, ErrLimit) {
		t.Fatalf("full output unexpectedly fit the small budget: %v", err)
	}
	rectangle := image.Rect(299_991, 299_992, 299_994, 299_994)
	result, err := RenderRegion(nil, document, rectangle, Options{MaxWorkingBytes: 64 << 10})
	if err != nil || result.Rect != rectangle || len(result.Pix) != 24 {
		t.Fatalf("regional gradient allocated or budgeted full canvas: %v", err)
	}
	for _, rectangle := range []image.Rectangle{
		image.Rect(-1, 0, 1, 1), image.Rect(0, 0, 300_001, 1),
		{Min: image.Pt(3, 3), Max: image.Pt(2, 4)},
	} {
		if _, err := RenderRegion(nil, document, rectangle); !errors.Is(err, ErrInvalid) {
			t.Fatalf("malformed ROI accepted: %v", err)
		}
	}
}

func TestComposeRegionMaskSamplesPreserveGeometryAndRequireHalo(t *testing.T) {
	rectangle := image.Rect(123_451, 299_998, 123_453, 300_000)
	layer := composeTestLayer(rectangle, color.NRGBA{R: 201, G: 73, A: 255})
	samples := composeTestLayer(rectangle, color.NRGBA{R: 128, A: 255}).Pixels
	layer.Mask = &psdmodel.Mask{Bounds: image.Rect(0, 0, 300_000, 300_000),
		SampleBounds: &rectangle, Pixels: samples, DefaultColor: new(uint8(255))}
	document := &psdmodel.Document{Width: 300_000, Height: 300_000, Layers: []*psdmodel.Layer{layer}}
	result, err := RenderRegion(nil, document, rectangle, Options{MaxWorkingBytes: 64 << 10})
	if err != nil || result.NRGBAAt(rectangle.Min.X, rectangle.Min.Y).A != 128 {
		t.Fatalf("large mask geometry was allocated or replaced by sample window: %v", err)
	}
	layer.Mask.Feather = new(1.5)
	if _, err := RenderRegion(nil, document, rectangle); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing mask neighborhood silently used default samples: %v", err)
	}
	document, _ = vectorFeatherTestDocument(1.5)
	if result, err := RenderRegion(nil, document, image.Rect(2, 2, 2, 3)); err != nil || len(result.Pix) != 0 {
		t.Fatalf("empty vector-feather region failed or allocated pixels: %v", err)
	}
}
