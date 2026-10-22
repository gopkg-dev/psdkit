package psdeffects_test

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestDropShadowLegacyNativeSixRegressions(t *testing.T) {
	for _, name := range []string{
		"shadow-hard", "shadow-hard-fill-zero", "shadow-soft-global",
		"glow-hard", "stroke-outside", "glow-stroke-fill-half",
	} {
		t.Run(name, func(t *testing.T) {
			base := filepath.Join("..", "output", "effects-native", name)
			source, err := psdfile.Open(base + ".psd")
			if os.IsNotExist(err) {
				t.Skip("optional original Photoshop effect fixture is absent")
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := source.Close(); err != nil {
					t.Error(err)
				}
			})
			document, err := source.Decode(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			document.Composite = image.NewNRGBA(image.Rect(0, 0, document.Width, document.Height))
			for index := range document.Composite.Pix {
				document.Composite.Pix[index] = byte(index*37 + 11)
			}
			actual, err := psdcompose.Render(nil, document)
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(base + ".png")
			if err != nil {
				t.Fatal(err)
			}
			native, err := png.Decode(file)
			closeErr := file.Close()
			if err != nil {
				t.Fatal(err)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			if native.Bounds() != actual.Rect {
				t.Fatalf("native bounds differ: %v %v", native.Bounds(), actual.Rect)
			}
			for y := actual.Rect.Min.Y; y < actual.Rect.Max.Y; y++ {
				for x := actual.Rect.Min.X; x < actual.Rect.Max.X; x++ {
					got := actual.NRGBAAt(x, y)
					want := color.NRGBAModel.Convert(native.At(x, y)).(color.NRGBA)
					if difference := int(got.A) - int(want.A); difference < -2 || difference > 2 {
						t.Fatalf("native alpha at %d,%d: got %v, want %v", x, y, got, want)
					}
					gotRGB, wantRGB := [3]byte{got.R, got.G, got.B}, [3]byte{want.R, want.G, want.B}
					for channel := range 3 {
						premult := math.Round(float64(gotRGB[channel])*float64(got.A)/255) -
							math.Round(float64(wantRGB[channel])*float64(want.A)/255)
						if premult < -2 || premult > 2 {
							t.Fatalf("native premultiplied pixel at %d,%d: got %v, want %v", x, y, got, want)
						}
						if got.A != 0 && want.A != 0 {
							difference := int(gotRGB[channel]) - int(wantRGB[channel])
							if difference < -2 || difference > 2 {
								t.Fatalf("native visible pixel at %d,%d: got %v, want %v", x, y, got, want)
							}
						}
					}
				}
			}
		})
	}
}
