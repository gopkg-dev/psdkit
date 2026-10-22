package psdkit_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"os"
	"testing"

	"github.com/gopkg-dev/psdkit"
	"github.com/gopkg-dev/psdkit/psdcompose"
)

func TestPhotoFilterPublicSourceTileVisitorAndTypedMetadata(t *testing.T) {
	data, err := os.ReadFile("psdadjust/testdata/photo-filter/neutral.psd")
	if err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(data)
	source, err := psdkit.ParseDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	var found bool
	for _, layer := range source.Layers {
		for _, block := range layer.AdditionalInfo {
			if block.Key != "phfl" {
				continue
			}
			adjustment, err := source.DecodeAdjustment(nil, block)
			if err != nil || adjustment.Kind != psdkit.AdjustmentPhotoFilter ||
				adjustment.PhotoFilter.Density != 0 || adjustment.PhotoFilter.ColorSpace != 7 {
				t.Fatalf("public typed parameters: %v %+v", err, adjustment)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no source filter metadata")
	}
	full, err := psdkit.RenderSource(nil, source)
	if err != nil {
		t.Fatal(err)
	}
	expected := []byte{0, 0, 0, 255, 1, 12, 254, 255, 30, 70, 130, 255, 213, 37, 83, 255, 255, 255, 255, 255}
	expected = append(expected, expected...)
	if !bytes.Equal(full.Pix, expected) {
		t.Fatalf("neutral Source changed literal pixels: %v", full.Pix)
	}
	window := image.Rect(1, 0, 5, 2)
	tile, err := psdkit.RenderSourceTile(nil, source, window)
	if err != nil {
		t.Fatal(err)
	}
	for y := window.Min.Y; y < window.Max.Y; y++ {
		for x := window.Min.X; x < window.Max.X; x++ {
			if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
				t.Fatal("neutral Tile changed absolute pixels")
			}
		}
	}
	visited := 0
	if err := psdkit.VisitSourceTiles(nil, source, full.Rect, image.Pt(2, 1),
		func(tile *image.NRGBA) error {
			for y := tile.Rect.Min.Y; y < tile.Rect.Max.Y; y++ {
				for x := tile.Rect.Min.X; x < tile.Rect.Max.X; x++ {
					if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
						t.Fatal("neutral visitor changed pixels")
					}
					visited++
				}
			}
			return nil
		}); err != nil || visited != 10 {
		t.Fatalf("neutral visitor: visited=%d err=%v", visited, err)
	}
	if !bytes.Equal(data, before) {
		t.Fatal("public metadata or rendering changed original source bytes")
	}
}

func TestPhotoFilterPublicNonzeroLiveGuardAndCancellation(t *testing.T) {
	source, err := psdkit.OpenDocument("psdadjust/testdata/photo-filter/nonzero.psd")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if output, err := psdkit.RenderSource(nil, source); output != nil || !errors.Is(err, psdcompose.ErrUnsupported) {
		t.Fatalf("nonzero Source silently used cached pixels or approximation: %v", err)
	}
	if output, err := psdkit.RenderSourceTile(nil, source, image.Rect(1, 0, 4, 1)); output != nil ||
		!errors.Is(err, psdcompose.ErrUnsupported) {
		t.Fatalf("nonzero ROI silently used cached pixels or approximation: %v", err)
	}
	called := false
	if err := psdkit.VisitSourceTiles(nil, source, image.Rect(0, 0, 5, 2), image.Pt(2, 1),
		func(_ *image.NRGBA) error { called = true; return nil }); !errors.Is(err, psdcompose.ErrUnsupported) || called {
		t.Fatalf("nonzero visitor returned unqualified pixels: %v callback=%t", err, called)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if output, err := psdkit.RenderSource(ctx, source); output != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Source cancellation lost cause or returned partial pixels: %v", err)
	}
}
