package psdcompose_test

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestNativeInnerEffectsFromLiveLayersIgnorePoisonedFooter(t *testing.T) {
	for _, id := range []string{
		"shadow-angle0", "shadow-angle45", "shadow-angle90", "shadow-angle135", "shadow-angle180",
		"shadow-angle225", "shadow-angle270", "shadow-angle315", "shadow-global", "shadow-fill-zero",
		"shadow-fill-half", "shadow-layer-half", "shadow-effect-half", "shadow-multiply",
		"glow-edge-hard-size0", "glow-edge-hard-size2", "glow-edge-hard-size4",
		"glow-center-hard-size0", "glow-center-hard-size2", "glow-center-hard-size4",
		"glow-edge-fill-zero", "glow-edge-fill-half", "glow-edge-screen",
		"shadow-soft-size2", "shadow-soft-size4", "shadow-soft-size8",
		"glow-edge-soft-size2", "glow-edge-soft-size4", "glow-edge-soft-size8",
		"glow-center-soft-size2", "glow-center-soft-size4", "glow-center-soft-size8",
	} {
		t.Run(id, func(t *testing.T) {
			path := filepath.Join("..", "output", "inner-effects-native", id+".psd")
			source, err := psdfile.Open(path)
			if os.IsNotExist(err) {
				t.Skip("optional Photoshop inner-effect fixture is absent")
			}
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			document, err := source.Decode(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if document.Composite != nil {
				for index := range document.Composite.Pix {
					document.Composite.Pix[index] = byte(index*73 + 17)
				}
			}
			pixels, err := psdcompose.Render(nil, document)
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(filepath.Join("..", "output", "inner-effects-native", id+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			native, err := png.Decode(file)
			if err != nil {
				t.Fatal(err)
			}
			for y := 0; y < pixels.Rect.Dy(); y++ {
				for x := 0; x < pixels.Rect.Dx(); x++ {
					left := pixels.NRGBAAt(x, y)
					right := color.NRGBAModel.Convert(native.At(x, y)).(color.NRGBA)
					for _, difference := range []int{int(left.R) - int(right.R), int(left.G) - int(right.G),
						int(left.B) - int(right.B), int(left.A) - int(right.A)} {
						if difference < -1 || difference > 1 {
							t.Fatalf("native pixel (%d,%d) differs: Go=%v PS=%v", x, y, left, right)
						}
					}
				}
			}
		})
	}
}

func TestNativeTentShadowAndDiscreteThreePixelBrushes(t *testing.T) {
	for _, item := range []struct{ directory, id, pngDirectory string }{
		{"effects-native", "shadow-soft-global", "effects-native"},
		{"effects-edit-size3", "stroke-parameters", "effects-edit-size3/native"},
		{"effects-edit-size3", "glow-parameters", "effects-edit-size3/native"},
	} {
		t.Run(item.id, func(t *testing.T) {
			source, err := psdfile.Open(filepath.Join("..", "output", item.directory, item.id+".psd"))
			if os.IsNotExist(err) {
				t.Skip("optional native effect fixture is absent")
			}
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			document, err := source.Decode(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			document.Composite = image.NewNRGBA(image.Rect(0, 0, document.Width, document.Height))
			pixels, err := psdcompose.Render(nil, document)
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(filepath.Join("..", "output", item.pngDirectory, item.id+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			native, err := png.Decode(file)
			if err != nil {
				t.Fatal(err)
			}
			for y := 0; y < pixels.Rect.Dy(); y++ {
				for x := 0; x < pixels.Rect.Dx(); x++ {
					left := pixels.NRGBAAt(x, y)
					right := color.NRGBAModel.Convert(native.At(x, y)).(color.NRGBA)
					if difference := int(left.A) - int(right.A); difference < -1 || difference > 1 {
						t.Fatalf("native alpha (%d,%d) differs: Go=%v PS=%v", x, y, left, right)
					}
					if left.A == 0 && right.A == 0 {
						continue
					}
					for _, difference := range []int{int(left.R) - int(right.R), int(left.G) - int(right.G),
						int(left.B) - int(right.B)} {
						if difference < -1 || difference > 1 {
							t.Fatalf("native RGB (%d,%d) differs: Go=%v PS=%v", x, y, left, right)
						}
					}
				}
			}
		})
	}
}
