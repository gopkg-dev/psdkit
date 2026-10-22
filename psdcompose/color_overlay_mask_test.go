package psdcompose

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func TestPixelMaskedColorOverlayAllModesAndGlobalStages(t *testing.T) {
	modes := []string{"norm", "dark", "mul ", "idiv", "lbrn", "dkCl", "lite", "scrn", "div ",
		"lddg", "lgCl", "over", "sLit", "hLit", "vLit", "lLit", "pLit", "hMix", "diff", "smud",
		"fsub", "fdiv", "hue ", "sat ", "colr", "lum "}
	bounds := image.Rect(0, 0, 32, 8)
	backdrop := composeTestLayer(bounds, color.NRGBA{R: 73, G: 119, B: 211, A: 255})
	maskPixels := image.NewNRGBA(image.Rect(-3, 1, 29, 7))
	for y := maskPixels.Rect.Min.Y; y < maskPixels.Rect.Max.Y; y++ {
		for x := maskPixels.Rect.Min.X; x < maskPixels.Rect.Max.X; x++ {
			maskPixels.SetNRGBA(x, y, color.NRGBA{R: byte((x*31 + y*67 + 257) % 256), A: 255})
		}
	}
	density, defaultColor := uint8(173), uint8(0)
	mask := &psdmodel.Mask{Bounds: maskPixels.Rect, Pixels: maskPixels, Density: &density,
		DefaultColor: &defaultColor, Invert: true}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			layer := composeTestLayer(bounds, color.NRGBA{})
			for pixel := range 256 {
				layer.Pixels.SetNRGBA(pixel%32, pixel/32, color.NRGBA{
					R: byte((pixel*73 + 19) % 256), G: byte((pixel*151 + 101) % 256),
					B: byte((pixel*43 + 7) % 256), A: byte(pixel),
				})
			}
			overlay := coreEffect(psdeffects.ColorOverlay)
			overlay.BlendMode, overlay.Opacity = mode, .63
			overlay.Color = [3]float64{173.0 / 255, 59.0 / 255, 211.0 / 255}
			second := coreEffect(psdeffects.ColorOverlay)
			second.BlendMode, second.Opacity = "mul ", .37
			second.Color = [3]float64{20.0 / 255, 220.0 / 255, 80.0 / 255}
			layer.Effects = coreEffectSet(overlay, second)
			layer.FillOpacity, layer.Opacity = new(74.0/255), new(186.0/255)
			original := bytes.Clone(layer.Pixels.Pix)
			for _, interior := range []bool{false, true} {
				layer.BlendInterior = &interior
				layer.Mask = nil
				document := &psdmodel.Document{Width: 32, Height: 8, Layers: []*psdmodel.Layer{backdrop, layer}}
				plain, err := Render(nil, document)
				if err != nil {
					t.Fatal(err)
				}
				layer.Mask = mask
				for _, global := range []*bool{nil, new(false), new(true)} {
					layer.MaskAsGlobal = global
					actual, err := Render(nil, document)
					if err != nil {
						t.Fatal(err)
					}
					for pixel := range 256 {
						x, y := pixel%32, pixel/32
						stored := float64(0)
						if image.Pt(x, y).In(maskPixels.Rect) {
							stored = float64(maskPixels.NRGBAAt(x, y).R) / 255
						}
						amount := 1 - stored*173/255
						for channel, background := range []byte{73, 119, 211} {
							expected := byte(math.Round(float64(background)*(1-amount) + float64(plain.Pix[pixel*4+channel])*amount))
							if effectByteDifference(actual.Pix[pixel*4+channel], expected) > 1 {
								t.Fatalf("mask/global stage differs at pixel %d channel %d", pixel, channel)
							}
						}
						if actual.Pix[pixel*4+3] != 255 {
							t.Fatal("masked Color Overlay changed opaque backdrop alpha")
						}
					}
				}
			}
			if !bytes.Equal(layer.Pixels.Pix, original) || maskPixels.Pix[3] != 255 {
				t.Fatal("effect paint modified caller source or mask")
			}
		})
	}
}

func TestPixelMaskedColorOverlayZeroWhiteDisabledAndExactQuota(t *testing.T) {
	layer := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 190, G: 27, B: 61, A: 93})
	overlay := coreEffect(psdeffects.ColorOverlay)
	overlay.Opacity = .47
	layer.Effects = coreEffectSet(overlay)
	document := &psdmodel.Document{Width: 1, Height: 1, Layers: []*psdmodel.Layer{layer}}
	plain, err := Render(nil, document, Options{MaxWorkingBytes: 1542})
	if err != nil {
		t.Fatal(err)
	}
	maskPixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	layer.Mask = &psdmodel.Mask{Bounds: maskPixels.Rect, Pixels: maskPixels}
	for _, global := range []*bool{nil, new(false), new(true)} {
		layer.MaskAsGlobal = global
		for _, state := range []string{"zero", "white", "disabled", "density-zero"} {
			layer.Mask.Disabled, layer.Mask.Density = false, nil
			maskPixels.Pix[0] = 0
			switch state {
			case "white":
				maskPixels.Pix[0] = 255
			case "disabled":
				layer.Mask.Disabled = true
			case "density-zero":
				value := uint8(0)
				layer.Mask.Density = &value
			}
			if pixels, err := Render(nil, document, Options{MaxWorkingBytes: 1541}); pixels != nil ||
				!errors.Is(err, ErrLimit) {
				t.Fatalf("masked overlay escaped exact allocation quota: %v", err)
			}
			pixels, err := Render(nil, document, Options{MaxWorkingBytes: 1542})
			if err != nil {
				t.Fatal(err)
			}
			if state == "zero" {
				if !bytes.Equal(pixels.Pix, []byte{0, 0, 0, 0}) {
					t.Fatal("zero mask left body or overlay contribution")
				}
			} else if !bytes.Equal(pixels.Pix, plain.Pix) {
				t.Fatalf("%s mask changed unmasked output", state)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if pixels, err := Render(ctx, document); pixels != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled masked effect returned partial output: %v", err)
	}
}

func TestPixelMaskedOverlayDoesNotAdmitOtherEffectContracts(t *testing.T) {
	for _, kind := range []psdeffects.Kind{psdeffects.DropShadow, psdeffects.InnerGlow, psdeffects.Stroke} {
		layer := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{A: 255})
		layer.Mask = &psdmodel.Mask{Bounds: layer.Bounds, Pixels: image.NewNRGBA(layer.Bounds)}
		overlay, other := coreEffect(psdeffects.ColorOverlay), coreEffect(kind)
		layer.Effects = coreEffectSet(overlay, other)
		if pixels, err := Render(nil, &psdmodel.Document{
			Width: 1, Height: 1, Layers: []*psdmodel.Layer{layer},
		}); pixels != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatalf("masked %s active set was admitted: %v", kind, err)
		}
	}
}
