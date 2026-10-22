package psdcompose_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestNativeOrdinaryRangesSavedDisabledEffectsAndSourceOwnership(t *testing.T) {
	// Photoshop serializes disabled default effect descriptors when setting these own ranges.
	for _, item := range []struct{ directory, id string }{
		{"p3-ordinary-gray-seed3-native", "ordinary-difference-source"},
		{"p3-ordinary-gray-seed3-native", "ordinary-difference-both"},
		{"p3-ordinary-special-stage-holdout-v2-native", "ordinary-hard-mix-source"},
		{"p3-ordinary-special-stage-holdout-v2-native", "ordinary-color-burn-both"},
	} {
		t.Run(item.id, func(t *testing.T) {
			path := filepath.Join("..", "output", item.directory, item.id+".psd")
			payload, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				t.Skip("optional Photoshop ordinary range fixture is absent")
			}
			if err != nil {
				t.Fatal(err)
			}
			beforeSource := sha256.Sum256(payload)
			source, err := psdfile.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			document, err := source.DecodeLayers(context.Background())
			if err != nil || document.Composite != nil {
				t.Fatalf("layer-only decoding borrowed a saved composite: %v", err)
			}
			before, err := json.Marshal(document, json.Deterministic(true))
			if err != nil {
				t.Fatal(err)
			}
			actual, err := psdcompose.Render(context.Background(), document)
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(filepath.Join("..", "output", item.directory, item.id+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			native, err := png.Decode(file)
			if err != nil {
				t.Fatal(err)
			}
			for y := 0; y < document.Height; y++ {
				for x := 0; x < document.Width; x++ {
					left := actual.NRGBAAt(x, y)
					right := color.NRGBAModel.Convert(native.At(x, y)).(color.NRGBA)
					difference := int(left.A) - int(right.A)
					if difference < -2 || difference > 2 {
						t.Fatalf("native alpha at %d,%d = %v, want %v", x, y, left, right)
					}
					for component, value := range []byte{left.R, left.G, left.B} {
						expected := []byte{right.R, right.G, right.B}[component]
						difference := int(value)*int(left.A) - int(expected)*int(right.A)
						if difference < -510 || difference > 510 {
							t.Fatalf("raw native PM at %d,%d component %d = %v, want %v", x, y, component, left, right)
						}
					}
				}
			}
			rectangle := image.Rect(1, 2, document.Width-1, document.Height-1)
			window, err := psdcompose.RenderRegion(context.Background(), document, rectangle)
			if err != nil || window.Rect != rectangle {
				t.Fatalf("ordinary range region lost its origin: %v", err)
			}
			for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
				for x := rectangle.Min.X; x < rectangle.Max.X; x++ {
					if actual.NRGBAAt(x, y) != window.NRGBAAt(x, y) {
						t.Fatalf("full and regional ordinary range pixels differ at %d,%d", x, y)
					}
				}
			}
			after, err := json.Marshal(document, json.Deterministic(true))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("rendering changed saved effect metadata, ranges, masks or pixels: %v", err)
			}
			payload, err = os.ReadFile(path)
			if err != nil || sha256.Sum256(payload) != beforeSource {
				t.Fatalf("rendering changed the source PSD: %v", err)
			}
		})
	}
}
