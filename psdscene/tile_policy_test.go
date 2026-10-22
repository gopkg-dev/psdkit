package psdscene

import (
	"bytes"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestTileSpecialCacheRequiresExplicitPolicyAndPreservesBlockers(t *testing.T) {
	for _, key := range []string{"TySh", "tySh", "vstk", "vscg"} {
		t.Run(key, func(t *testing.T) {
			source := cachedOpen(t, cachedTestSource(artboardTestBlock(key, []byte{0, 0})))
			if _, err := RenderTile(nil, source, image.Rect(0, 0, 2, 1)); !errors.Is(err, psdfile.ErrUnsupported) {
				t.Fatalf("implicit special layer cache masqueraded as live reconstruction: %v", err)
			}
			pixels, err := RenderTile(nil, source, image.Rect(1, 0, 2, 1), TileOptions{CachedSpecialLayers: true})
			if err != nil || !bytes.Equal(pixels.Pix, []byte{17, 31, 77, 128}) {
				t.Fatalf("explicit regional saved layer selection used wrong channel or merged footer: %v", err)
			}
		})
	}
	gradient := artboardTestBlock("GdFl", cachedTestGradient(true))
	other := artboardTestBlock("FEid", []byte{1, 2, 3, 4})
	data := cachedTestSource(append(gradient, other...))
	if _, err := RenderTile(nil, cachedOpen(t, data), image.Rect(0, 0, 1, 1),
		TileOptions{CachedSpecialLayers: true}); !cachedUnsupported(err) {
		t.Fatalf("explicit special cache erased an unrelated blocker: %v", err)
	}
}

func TestTileHiddenSpecialLayersDoNotRequireCacheOrLiveDecode(t *testing.T) {
	for _, key := range []string{"TySh", "vstk"} {
		data := cachedTestSource(artboardTestBlock(key, []byte{0, 0}))
		position := bytes.Index(data, []byte("8BIMnorm"))
		if position < 0 {
			t.Fatal("fixture blend metadata is absent")
		}
		data[position+10] |= 2
		result, err := RenderTile(nil, cachedOpen(t, data), image.Rect(0, 0, 1, 1))
		if err != nil || !bytes.Equal(result.Pix, []byte{0, 0, 0, 0}) {
			t.Fatalf("hidden special content required an implicit live/cache policy: %v", err)
		}
	}
}
