package psdcompose

import (
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdpattern"
)

func TestLivePatternIgnoresStaleRasterAndComposesCoverage(t *testing.T) {
	tile := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	tile.SetNRGBA(0, 0, color.NRGBA{R: 200, A: 255})
	tile.SetNRGBA(1, 0, color.NRGBA{B: 200, A: 128})
	stale := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	stale.SetNRGBA(0, 0, color.NRGBA{G: 255, A: 255})
	opacity := .5
	layer := &psdmodel.Layer{Bounds: stale.Rect, Pixels: stale, PatternTile: tile,
		Pattern: &psdpattern.Fill{Scale: 100}, BlendMode: "norm", Opacity: &opacity}
	document := &psdmodel.Document{Width: 4, Height: 2, Layers: []*psdmodel.Layer{layer}}
	actual, err := Render(nil, document)
	if err != nil {
		t.Fatal(err)
	}
	for row := range 2 {
		if actual.NRGBAAt(0, row) != (color.NRGBA{R: 200, A: 128}) ||
			actual.NRGBAAt(2, row) != (color.NRGBA{R: 200, A: 128}) ||
			actual.NRGBAAt(1, row) != (color.NRGBA{B: 200, A: 64}) {
			t.Fatal("live tile, tile alpha or layer opacity was replaced with cached pixels")
		}
	}
	if stale.NRGBAAt(0, 0).G != 255 || tile.NRGBAAt(0, 0).R != 200 {
		t.Fatal("pattern render changed caller pixels")
	}
	layer.PatternTile = nil
	if _, err := Render(nil, document); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unresolved pattern silently used stale pixels: %v", err)
	}
	layer.Hidden = true
	if _, err := Render(nil, document); err != nil {
		t.Fatalf("hidden unresolved pattern required payload: %v", err)
	}
	layer.PatternTile = &image.NRGBA{Rect: image.Rect(0, 0, 2, 2), Stride: 8, Pix: []byte{0}}
	if _, err := Render(nil, document); !errors.Is(err, ErrInvalid) {
		t.Fatalf("hidden malformed tile was not validated: %v", err)
	}
}

func TestLivePatternCountsItsCanvasCache(t *testing.T) {
	tile := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	layer := &psdmodel.Layer{PatternTile: tile, Pattern: &psdpattern.Fill{Scale: 100}, BlendMode: "norm"}
	document := &psdmodel.Document{Width: 8, Height: 4, Layers: []*psdmodel.Layer{layer}}
	if _, err := Render(nil, document, Options{MaxWorkingBytes: 1200}); !errors.Is(err, ErrLimit) {
		t.Fatalf("retained pattern canvas was not included in the budget: %v", err)
	}
	layer.Pattern.Scale = 75
	if _, err := Render(nil, document); err != nil {
		t.Fatalf("constant single-pixel sampling was rejected: %v", err)
	}
	layer.PatternTile = image.NewNRGBA(image.Rect(0, 0, 2, 1))
	if _, err := Render(nil, document); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("uncalibrated tile sampling was accepted: %v", err)
	}
}
