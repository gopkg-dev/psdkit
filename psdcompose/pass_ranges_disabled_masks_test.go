package psdcompose

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func attachPassRangeDisabledMasks(layer *psdmodel.Layer, kind string) {
	if kind != "vector" {
		bounds := image.Rect(0, 0, 11, 7)
		layer.Mask = &psdmodel.Mask{Bounds: bounds,
			Pixels:   composeTestLayer(bounds, color.NRGBA{R: 17, A: 255}).Pixels,
			Disabled: true, Invert: true, DefaultColor: new(uint8(31)),
			Density: new(uint8(193)), Feather: new(99.0)}
	}
	if kind != "pixel" {
		layer.VectorMask = &psdvector.Mask{Version: 3, Flags: 4, Disabled: true,
			Width: 11, Height: 7, HasInitialFill: true}
		layer.VectorDensity, layer.VectorFeather = new(uint8(137)), new(99.0)
		if layer.Mask != nil {
			layer.Mask.Unsupported = []string{"暂不支持矢量蒙版羽化"}
		}
	}
}

func TestSinglePassRangesDisabledMasksMatchUnmaskedCalibratedStages(t *testing.T) {
	modes := []string{"dark", "mul ", "idiv", "lbrn", "dkCl", "lite", "scrn", "div ", "lddg", "lgCl",
		"over", "sLit", "hLit", "vLit", "lLit", "pLit", "hMix", "diff", "smud", "fsub", "fdiv",
		"hue ", "sat ", "colr", "lum "}
	for _, mode := range modes {
		for _, kind := range []string{"pixel", "vector", "both"} {
			for _, location := range []string{"group", "child", "both"} {
				t.Run(mode+"/"+kind+"/"+location, func(t *testing.T) {
					group := singlePassNativeGroup(color.NRGBA{A: 255}, mode, 191, 161)
					child := composeTestLayer(image.Rect(0, 0, 11, 7), color.NRGBA{})
					child.BlendMode, child.Opacity = mode, new(191.0/255)
					group.Children[0] = child
					bottom := composeTestLayer(child.Bounds, color.NRGBA{})
					for index := range 77 {
						copy(child.Pixels.Pix[index*4:], []byte{byte(index*13 + 19), byte(index*37 + 53),
							byte(index*71 + 97), byte(index * 29)})
						copy(bottom.Pixels.Pix[index*4:], []byte{byte(index*17 + 31), byte(index*59 + 11),
							byte(index*47 + 23), byte(index*43 + 61)})
					}
					document := &psdmodel.Document{Width: 11, Height: 7, Layers: []*psdmodel.Layer{bottom, group}}
					baseline, err := Render(t.Context(), document)
					if err != nil {
						t.Fatal(err)
					}
					plan, err := preparePlan(document, nil)
					if err != nil {
						t.Fatal(err)
					}
					if location != "child" {
						attachPassRangeDisabledMasks(group, kind)
					}
					if location != "group" {
						attachPassRangeDisabledMasks(child, kind)
					}
					maskedPlan, err := preparePlan(document, []Options{{MaxFeatherRadius: 1}})
					if err != nil || maskedPlan.passRanges[group] != plan.passRanges[group] ||
						maskedPlan.workingBytes != plan.workingBytes {
						t.Fatalf("disabled masks changed stage or allocated coverage: %v", err)
					}
					actual, err := Render(t.Context(), document,
						Options{MaxWorkingBytes: plan.workingBytes, MaxFeatherRadius: 1})
					if err != nil || !bytes.Equal(actual.Pix, baseline.Pix) {
						t.Fatalf("disabled masks changed calibrated unmasked pixels: %v", err)
					}
					region := image.Rect(3, 2, 9, 6)
					partial, err := RenderRegion(t.Context(), document, region)
					if err != nil {
						t.Fatal(err)
					}
					for y := region.Min.Y; y < region.Max.Y; y++ {
						for x := region.Min.X; x < region.Max.X; x++ {
							if partial.NRGBAAt(x, y) != baseline.NRGBAAt(x, y) {
								t.Fatal("disabled masks changed regional source or gray range coordinates")
							}
						}
					}
				})
			}
		}
	}
}

func TestSinglePassDisabledMasksKeepActiveUnknownAndMalformedMasksRestricted(t *testing.T) {
	for _, item := range []struct {
		name   string
		change func(*psdmodel.Layer)
		want   error
	}{
		{"active pixel", func(layer *psdmodel.Layer) { layer.Mask.Disabled = false }, ErrUnsupported},
		{"active vector", func(layer *psdmodel.Layer) { layer.VectorMask.Disabled = false }, ErrUnsupported},
		{"active vector zero density", func(layer *psdmodel.Layer) {
			layer.VectorMask.Disabled, layer.VectorDensity = false, new(uint8(0))
		}, ErrUnsupported},
		{"unknown pixel", func(layer *psdmodel.Layer) { layer.Mask.Unsupported = []string{"future"} }, ErrUnsupported},
		{"unknown vector", func(layer *psdmodel.Layer) { layer.VectorMask.Unsupported = []string{"future"} }, ErrUnsupported},
		{"unknown vector version", func(layer *psdmodel.Layer) { layer.VectorMask.Version = 4 }, ErrUnsupported},
		{"unknown vector flags", func(layer *psdmodel.Layer) { layer.VectorMask.Flags |= 8 }, ErrUnsupported},
		{"orphan vector controls", func(layer *psdmodel.Layer) { layer.VectorMask = nil }, ErrUnsupported},
		{"orphan zero density", func(layer *psdmodel.Layer) {
			layer.Mask, layer.VectorMask, layer.VectorFeather, layer.VectorDensity = nil, nil, nil, new(uint8(0))
		}, ErrUnsupported},
		{"orphan zero feather", func(layer *psdmodel.Layer) {
			layer.Mask, layer.VectorMask, layer.VectorDensity, layer.VectorFeather = nil, nil, nil, new(0.0)
		}, ErrUnsupported},
		{"orphan feather marker", func(layer *psdmodel.Layer) { layer.VectorFeather = nil }, ErrUnsupported},
		{"NaN pixel feather", func(layer *psdmodel.Layer) { *layer.Mask.Feather = math.NaN() }, ErrInvalid},
		{"NaN vector feather", func(layer *psdmodel.Layer) { *layer.VectorFeather = math.NaN() }, ErrInvalid},
		{"short pixel samples", func(layer *psdmodel.Layer) { layer.Mask.Pixels.Pix = nil }, ErrInvalid},
	} {
		for _, onChild := range []bool{false, true} {
			t.Run(item.name+map[bool]string{false: "/group", true: "/child"}[onChild], func(t *testing.T) {
				group := singlePassNativeGroup(color.NRGBA{R: 203, A: 230}, "scrn", 191, 161)
				layer := group
				if onChild {
					layer = group.Children[0]
				}
				attachPassRangeDisabledMasks(layer, "both")
				item.change(layer)
				if output, err := Render(t.Context(), composeTestDocument(group)); output != nil || !errors.Is(err, item.want) {
					t.Fatalf("inactive coverage bypassed mask qualification: %v, want %v", err, item.want)
				}
			})
		}
	}
	group := singlePassNativeGroup(color.NRGBA{R: 203, A: 230}, "scrn", 191, 161)
	attachPassRangeDisabledMasks(group, "both")
	document := composeTestDocument(group)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if pixels, err := Render(ctx, document); pixels != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("disabled masks bypassed cancellation: %v", err)
	}
	pixels, err := Render(t.Context(), document, Options{MaxWorkingBytes: 1})
	if pixels != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("disabled masks bypassed output or metadata budgets: %v", err)
	}
}

func TestDisabledMasksDoNotPromoteNormalOrOrdinaryGrayRangeStages(t *testing.T) {
	for _, kind := range []string{"pixel", "vector", "both"} {
		group := singlePassNativeGroup(color.NRGBA{R: 203, A: 230}, "norm", 191, 161)
		attachPassRangeDisabledMasks(group, kind)
		attachPassRangeDisabledMasks(group.Children[0], kind)
		plan, err := preparePlan(composeTestDocument(group), nil)
		if err != nil || plan.passRanges[group] != passRangeLegacyNormal {
			t.Fatalf("disabled masks changed the existing Normal fallback stage: %v", err)
		}
		flat := group.Children[0]
		flat.BlendMode, flat.BlendRanges = "div ", group.BlendRanges
		plan, err = preparePlan(composeTestDocument(flat), nil)
		if err != nil || plan.ordinaryGrayRanges {
			t.Fatalf("disabled masks unexpectedly enabled the flat native gray stage: %v", err)
		}
	}
}
