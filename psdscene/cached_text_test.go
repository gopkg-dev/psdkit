package psdscene

import (
	"bytes"
	"context"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestCachedTextPolicySelectsOnlyTypographyAndPreservesLiveGradient(t *testing.T) {
	text := cachedOpen(t, cachedTestSource(artboardTestBlock("TySh", []byte{0, 0})))
	if _, err := Render(nil, text); !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("default silently selected saved text: %v", err)
	}
	selected, err := Render(nil, text, Options{CachedTextLayers: true})
	if err != nil || !bytes.Equal(selected.Pix, []byte{201, 21, 39, 255, 17, 31, 77, 128}) {
		t.Fatalf("explicit typography pixels were not selected: %v", err)
	}
	tile, err := RenderTile(nil, text, image.Rect(1, 0, 2, 1), TileOptions{CachedTextLayers: true})
	if err != nil || !bytes.Equal(tile.Pix, selected.Pix[4:8]) {
		t.Fatalf("text source region differs: %v", err)
	}
	gradient := cachedOpen(t, cachedTestSource(artboardTestBlock("GdFl", cachedTestGradient(false))))
	live, err := Render(nil, gradient, Options{CachedTextLayers: true})
	if err != nil || !bytes.Equal(live.Pix, []byte{20, 80, 200, 255, 20, 80, 200, 255}) {
		t.Fatalf("typography policy selected a saved gradient raster: %v", err)
	}
	stroke := cachedOpen(t, cachedTestSource(artboardTestBlock("vstk", []byte{0, 0})))
	if _, err := Render(nil, stroke, Options{CachedTextLayers: true}); !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("typography policy selected a saved stroke raster: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if output, err := Render(ctx, text, Options{CachedTextLayers: true}); output != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("text source selection lost cancellation: %v", err)
	}
}
