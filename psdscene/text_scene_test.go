package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
)

func TestNativeTextSceneUsesExplicitGlyphsAndPreservesSource(t *testing.T) {
	if os.Getenv("PSDKIT_TEXT_RENDER_NATIVE") != "1" || os.Getenv("PSDKIT_TEST_FONT") == "" {
		t.Skip("native source and explicitly selected font are required")
	}
	face, err := psdfont.Open(nil, os.Getenv("PSDKIT_TEST_FONT"))
	if err != nil {
		t.Fatal(err)
	}
	resolver := psdfont.ResolverFunc(func(_ context.Context, name string) (*psdfont.Face, error) {
		if name != face.PostScriptName {
			return nil, psdfont.ErrMissingFont
		}
		return face, nil
	})
	data, err := os.ReadFile(filepath.Join("..", "output", "text-plain-native", "text-plain-none.psd"))
	if err != nil {
		t.Fatal(err)
	}
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	before := sha256.Sum256(data)
	if _, err := Render(nil, source); !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("default scene silently used text cache: %v", err)
	}
	kerning := false
	textOptions := TextRenderOptions{Kerning: &kerning}
	settings := Options{ApproximateText: &textOptions, FontResolver: resolver}
	actual, err := Render(nil, source, settings)
	if err != nil {
		t.Fatal(err)
	}
	view, err := source.TextLayers(nil)
	if err != nil || len(view) != 1 {
		t.Fatalf("text view: %v", err)
	}
	pixels, err := RenderTextLayer(nil, source, view[0], resolver, textOptions)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.Decode(nil)
	if err != nil {
		t.Fatal(err)
	}
	layer := flatten(snapshot.Layers)[view[0].LayerIndex]
	layer.Pixels, layer.Bounds = pixels, pixels.Rect
	expected, err := psdcompose.Render(nil, snapshot)
	if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
		t.Fatalf("scene did not compose freshly rendered glyphs: %v", err)
	}
	settings.FontResolver = nil
	if _, err := Render(nil, source, settings); !errors.Is(err, psdfont.ErrMissingFont) {
		t.Fatalf("missing font silently used saved raster pixels: %v", err)
	}
	settings.FontResolver, settings.MaxWorkingBytes = resolver, 1024
	if _, err := Render(nil, source, settings); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("scene ignored aggregate glyph budget: %v", err)
	}
	if before != sha256.Sum256(data) {
		t.Fatal("text scene changed original bytes")
	}
	retained := sha256.Sum256(actual.Pix)
	_ = source.Close()
	if retained != sha256.Sum256(actual.Pix) {
		t.Fatal("scene output depended on an open source")
	}
}
