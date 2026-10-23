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

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdgradient"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func gradientComponentControls(rgbConstant, reverse bool) psdgradient.Fill {
	fill := gradientEditBase()
	constant := gradientConstantControls(psdgradient.Linear, false)
	fill.Reverse = reverse
	if rgbConstant {
		fill.ColorStops = constant.ColorStops
		fill.OpacityStops = []psdgradient.OpacityStop{
			{Location: 0, Midpoint: 50, Opacity: .25}, {Location: 4096, Midpoint: 50, Opacity: .75},
		}
	} else {
		fill.OpacityStops = constant.OpacityStops
		fill.ColorStops = []psdgradient.ColorStop{
			{Location: 0, Midpoint: 50}, {Location: 4096, Midpoint: 50, RGB: [3]float64{1, 1, 1}},
		}
	}
	return fill
}

func gradientComponentExpected(rgbConstant, reverse bool) *image.NRGBA {
	// The ordinary classic ramp has weights 0, 13/64, 1/2 and 51/64 at these four document columns.
	gray, opacity := [4]byte{0, 52, 128, 203}, [4]byte{64, 90, 128, 165}
	if reverse {
		gray, opacity = [4]byte{255, 203, 128, 52}, [4]byte{191, 165, 128, 90}
	}
	expected := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := range 4 {
		for x := range 4 {
			value := color.NRGBA{R: gray[x], G: gray[x], B: gray[x], A: 128}
			if rgbConstant {
				value = color.NRGBA{R: 64, G: 128, B: 191, A: opacity[x]}
			}
			expected.SetNRGBA(x, y, value)
		}
	}
	return expected
}

func gradientComponentSavedPlanes(pixels *image.NRGBA) []byte {
	planes := make([]byte, 0, len(pixels.Pix))
	for component := range 4 {
		for offset := 0; offset < len(pixels.Pix); offset += 4 {
			alpha := int(pixels.Pix[offset+3])
			value := alpha
			if component != 3 {
				value = (int(pixels.Pix[offset+component])*alpha + 255*(255-alpha) + 127) / 255
			}
			planes = append(planes, byte(value))
		}
	}
	return planes
}

func TestGradientComponentRealtimeEditSaveReopenPSDPSBFourCompressions(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, rgbConstant := range []bool{false, true} {
				t.Run(fmt.Sprintf("v%d/c%d/constantRGB%t", version, compression, rgbConstant), func(t *testing.T) {
					editor := gradientEditSource(t, version, compositeFixtureTag("pRsv", []byte{9, 77, 0, 31}))
					for _, reverse := range []bool{false, true} {
						original, err := editor.source.ReadSpan(editor.source.Source)
						if err != nil {
							t.Fatal(err)
						}
						fill := gradientComponentControls(rgbConstant, reverse)
						if err := editor.SetGradientFill(nil, 1, fill); err != nil {
							t.Fatalf("constant component rejected irregular inactive stops: %v", err)
						}
						expected := gradientComponentExpected(rgbConstant, reverse)
						live, err := editor.Composite(nil)
						if err != nil || !bytes.Equal(live.Pix, expected.Pix) {
							t.Fatalf("edited varying component differs from independent ramp values: %v, %v", err, live)
						}
						saved := compositeSave(t, editor, SaveOptions{Compression: compression})
						if saved.Header.Version != version || !reflect.DeepEqual(*gradientEditRead(t, saved).Fill, fill) {
							t.Fatal("save changed format or normalized original stop order and inactive controls")
						}
						compositeExpect(t, saved, gradientComponentSavedPlanes(expected))
						scene, err := psdscene.Render(nil, saved)
						if err != nil || !bytes.Equal(scene.Pix, expected.Pix) {
							t.Fatalf("reopened Scene did not regenerate component gradient: %v", err)
						}
						region := image.Rect(1, 1, 4, 3)
						tile, err := psdscene.RenderTile(nil, saved, region)
						if err != nil || tile.Rect != region {
							t.Fatalf("saved component-gradient ROI failed: %v", err)
						}
						for y := region.Min.Y; y < region.Max.Y; y++ {
							for x := region.Min.X; x < region.Max.X; x++ {
								if tile.NRGBAAt(x, y) != expected.NRGBAAt(x, y) {
									t.Fatal("saved component-gradient ROI rescaled active geometry")
								}
							}
						}
						preserved := false
						for _, block := range saved.Layers[0].AdditionalInfo {
							if block.Key == "pRsv" {
								data, err := saved.ReadSpan(block.Data)
								preserved = err == nil && bytes.Equal(data, []byte{9, 77, 0, 31})
							}
						}
						unchanged, err := editor.source.ReadSpan(editor.source.Source)
						if !preserved || err != nil || !bytes.Equal(unchanged, original) {
							t.Fatalf("component gradient changed unknown blocks or immutable source bytes: %v", err)
						}
						reopened, err := New(saved)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = reopened.Close() })
						preview, err := reopened.Composite(nil)
						if err != nil || !bytes.Equal(preview.Pix, expected.Pix) {
							t.Fatalf("reopened editor changed the live component gradient: %v", err)
						}
						editor = reopened
					}
				})
			}
		}
	}
}

func TestGradientComponentFailuresKeepCommittedStateAndUnknownSourceFields(t *testing.T) {
	for _, rgbConstant := range []bool{false, true} {
		editor := gradientEditSource(t, 1, nil)
		fill := gradientComponentControls(rgbConstant, false)
		if err := editor.SetGradientFill(nil, 1, fill); err != nil {
			t.Fatal(err)
		}
		before, _ := editor.Changes()
		for _, smoothness := range []float64{0, 128.5} {
			invalid := fill
			invalid.Smoothness = smoothness
			if err := editor.SetGradientFill(nil, 1, invalid); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("one constant component allowed unverified smoothness: %v", err)
			}
		}
		invalid := fill
		invalid.ColorStops, invalid.OpacityStops = slices.Clone(fill.ColorStops), slices.Clone(fill.OpacityStops)
		if rgbConstant {
			invalid.OpacityStops[1].Location = 0
		} else {
			invalid.ColorStops[1].Location = 0
		}
		if err := editor.SetGradientFill(nil, 1, invalid); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("constant component hid ambiguous active stops: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := editor.SetGradientFill(ctx, 1, gradientComponentControls(rgbConstant, true)); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled component edit succeeded: %v", err)
		}
		after, _ := editor.Changes()
		pixels, err := editor.Composite(nil)
		if after != before || err != nil || !bytes.Equal(pixels.Pix, gradientComponentExpected(rgbConstant, false).Pix) {
			t.Fatalf("failed component-gradient edit changed committed live pixels: %v", err)
		}
	}
	descriptor := gradientEditDescriptor()
	fill := gradientComponentControls(true, false)
	if err := patchGradientDescriptor(context.Background(), descriptor, fill); err != nil {
		t.Fatal(err)
	}
	stops := gradientFind(gradientObject(descriptor, "Grad"), "Clrs").Value.([]psdfile.DescriptorValue)
	stop := stops[0].Value.(*psdfile.Descriptor)
	stop.Entries = append(stop.Entries, psdfile.DescriptorEntry{Key: "futureMetadata", Type: "bool", Value: true})
	editor := gradientEditDescriptorSource(t, 1, descriptor, nil)
	if err := editor.SetGradientFill(nil, 1, fill); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("constant component discarded unknown source stop semantics: %v", err)
	}
	state, _ := editor.Changes()
	node, _, _, _ := editor.findNodeLocked(1)
	if state.Dirty || node.gradient != nil || len(node.additional) != 0 {
		t.Fatal("rejected unknown source stop committed a partial gradient")
	}
}
