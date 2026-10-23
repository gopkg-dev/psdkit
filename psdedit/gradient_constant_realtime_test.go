package psdedit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"reflect"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdgradient"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func gradientConstantControls(kind psdgradient.Kind, opaque bool) psdgradient.Fill {
	fill := gradientEditBase()
	fill.Kind, fill.Smoothness, fill.Scale, fill.Angle = kind, 0, .01, 37
	fill.Offset, fill.Reverse = psdgradient.Offset{X: 11, Y: -19}, true
	fill.ColorStops, fill.OpacityStops = nil, nil
	for index, location := range []uint32{4096, 2048, 0, 2048} {
		fill.ColorStops = append(fill.ColorStops, psdgradient.ColorStop{
			Location: location, Midpoint: uint16(index%2) * 100, RGB: [3]float64{.25, .5, .75},
		})
		if !opaque {
			fill.OpacityStops = append(fill.OpacityStops, psdgradient.OpacityStop{
				Location: location, Midpoint: uint16(index%2) * 100, Opacity: .5,
			})
		}
	}
	return fill
}

func assertGradientConstantLive(t *testing.T, pixels *image.NRGBA, alpha byte) {
	t.Helper()
	if pixels == nil || pixels.Rect != image.Rect(0, 0, 4, 4) {
		t.Fatal("constant live gradient geometry differs")
	}
	for y := range 4 {
		for x := range 4 {
			if pixels.NRGBAAt(x, y) != (color.NRGBA{64, 128, 191, alpha}) {
				t.Fatalf("constant live RGBA differs at (%d,%d): %v", x, y, pixels.NRGBAAt(x, y))
			}
		}
	}
}

func TestGradientConstantRealtimeEditAutoSaveReopenPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, kind := range []psdgradient.Kind{psdgradient.Linear, psdgradient.Radial} {
				for _, opaque := range []bool{false, true} {
					t.Run(fmt.Sprintf("v%d/c%d/%s/opaque%t", version, compression, kind, opaque), func(t *testing.T) {
						editor := gradientEditSource(t, version, compositeFixtureTag("pRsv", []byte{99, 7, 0, 1}))
						original, err := editor.source.ReadSpan(editor.source.Source)
						if err != nil {
							t.Fatal(err)
						}
						fill := gradientConstantControls(kind, opaque)
						alpha := byte(128)
						if opaque {
							alpha = 255
						}
						for _, smoothness := range []float64{0, 128.5, 4096} {
							fill.Smoothness = smoothness
							if err := editor.SetGradientFill(nil, 1, fill); err != nil {
								t.Fatalf("constant gradient edit rejected inactive controls: %v", err)
							}
							live, err := editor.Composite(nil)
							if err != nil {
								t.Fatal(err)
							}
							assertGradientConstantLive(t, live, alpha)
							saved := compositeSave(t, editor, SaveOptions{Compression: compression})
							if saved.Header.Version != version {
								t.Fatal("constant gradient changed the PSD/PSB format")
							}
							view := gradientEditRead(t, saved)
							retained := *view.Fill
							if len(retained.OpacityStops) == 0 {
								retained.OpacityStops = nil
							}
							if !reflect.DeepEqual(retained, fill) {
								t.Fatalf("constant rendering normalized the saved descriptor: got %+v, want %+v", *view.Fill, fill)
							}
							var planes []byte
							for _, channel := range []byte{64, 128, 191} {
								composite := byte((int(channel)*int(alpha) + 255*(255-int(alpha)) + 127) / 255)
								planes = append(planes, bytes.Repeat([]byte{composite}, 16)...)
							}
							if alpha != 255 {
								planes = append(planes, bytes.Repeat([]byte{alpha}, 16)...)
							}
							compositeExpect(t, saved, planes)
							scene, err := psdscene.Render(nil, saved)
							if err != nil {
								t.Fatal(err)
							}
							assertGradientConstantLive(t, scene, alpha)
							region := image.Rect(1, 1, 3, 4)
							tile, err := psdscene.RenderTile(nil, saved, region)
							if err != nil || tile.Rect != region {
								t.Fatalf("constant saved gradient tile failed: %v", err)
							}
							for y := region.Min.Y; y < region.Max.Y; y++ {
								for x := region.Min.X; x < region.Max.X; x++ {
									if tile.NRGBAAt(x, y) != scene.NRGBAAt(x, y) {
										t.Fatal("constant gradient tile disagrees with full rendering")
									}
								}
							}
							opaqueKept := false
							for _, block := range saved.Layers[0].AdditionalInfo {
								if block.Key == "pRsv" {
									data, err := saved.ReadSpan(block.Data)
									opaqueKept = err == nil && bytes.Equal(data, []byte{99, 7, 0, 1})
								}
							}
							if !opaqueKept {
								t.Fatal("constant save changed unrelated source metadata")
							}
							unchanged, err := editor.source.ReadSpan(editor.source.Source)
							if err != nil || !bytes.Equal(unchanged, original) {
								t.Fatalf("constant gradient mutated the immutable source: %v", err)
							}
							reopened, err := New(saved)
							if err != nil {
								t.Fatal(err)
							}
							t.Cleanup(func() { _ = reopened.Close() })
							editor = reopened
							original, err = saved.ReadSpan(saved.Source)
							if err != nil {
								t.Fatal(err)
							}
						}
					})
				}
			}
		}
	}
}

func TestGradientConstantRealtimeFailureAndReturnToOrdinaryRamp(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		editor := gradientEditSource(t, version, nil)
		constant := gradientConstantControls(psdgradient.Radial, false)
		if err := editor.SetGradientFill(nil, 1, constant); err != nil {
			t.Fatal(err)
		}
		saved := compositeSave(t, editor)
		reopened, err := New(saved)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		before, _ := reopened.Changes()
		for _, smoothness := range []float64{0, 4096} {
			invalid := constant
			invalid.ColorStops = slices.Clone(constant.ColorStops)
			invalid.ColorStops[0].RGB[0] = 1
			invalid.Smoothness = smoothness
			if err := reopened.SetGradientFill(nil, 1, invalid); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("nonconstant controls retained the constant-only qualification: %v", err)
			}
			if after, _ := reopened.Changes(); after != before {
				t.Fatal("unsupported ramp replacement changed the saved constant state")
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := reopened.SetGradientFill(ctx, 1, gradientEditBase()); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled ramp replacement succeeded: %v", err)
		}
		live, err := reopened.Composite(nil)
		if err != nil {
			t.Fatal(err)
		}
		assertGradientConstantLive(t, live, 128)
		if err := reopened.SetGradientFill(nil, 1, gradientEditBase()); err != nil {
			t.Fatal(err)
		}
		changed, err := reopened.Composite(nil)
		if err != nil || bytes.Equal(changed.Pix, live.Pix) {
			t.Fatalf("supported nonconstant ramp did not restore ordinary sampling: %v", err)
		}
		again := compositeSave(t, reopened)
		preview, err := psdscene.Render(nil, again)
		if err != nil || !bytes.Equal(preview.Pix, changed.Pix) {
			t.Fatalf("ordinary sampling failed after saving the repaired gradient: %v", err)
		}
	}
}
