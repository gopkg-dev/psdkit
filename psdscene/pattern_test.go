package psdscene

import (
	"bytes"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestNativePatternSceneUsesLocalTilesAndPreservesOwnership(t *testing.T) {
	if os.Getenv("PSDKIT_PATTERN_NATIVE") != "1" {
		t.Skip("native pattern controls are required")
	}
	for _, name := range []string{"rgb-grid-2x2", "alpha-grid-4x3"} {
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join("..", "output", "pattern-native")
			source, err := psdfile.Open(filepath.Join(directory, name+".psd"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = source.Close() })
			snapshot, err := source.Decode(nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := psdcompose.Render(nil, snapshot); !errors.Is(err, psdcompose.ErrUnsupported) {
				t.Fatalf("unresolved tile used cached pixels: %v", err)
			}
			actual, err := Render(nil, source)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := os.ReadFile(filepath.Join(directory, name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			expected, err := png.Decode(bytes.NewReader(encoded))
			if err != nil || actual.Bounds() != expected.Bounds() {
				t.Fatalf("native image geometry: %v", err)
			}
			for row := range actual.Rect.Dy() {
				for column := range actual.Rect.Dx() {
					red, green, blue, alpha := expected.At(column, row).RGBA()
					actualRed, actualGreen, actualBlue, actualAlpha := actual.At(column, row).RGBA()
					if red != actualRed || green != actualGreen || blue != actualBlue || alpha != actualAlpha {
						t.Fatalf("native pattern differs at (%d,%d)", column, row)
					}
				}
			}
			retained := append([]byte(nil), actual.Pix...)
			_ = source.Close()
			if !bytes.Equal(retained, actual.Pix) {
				t.Fatal("returned pattern scene depends on source lifetime")
			}
		})
	}
}
