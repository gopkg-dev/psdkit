package psdcompose

import (
	"context"
	"encoding/json/v2"
	"errors"
	"image"
	"image/color"
	"math/big"
	"os"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/testfixture"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// nestedKnockoutRat uses exact premultiplied Normal source-over, independently of composePixel.
func nestedKnockoutRat(values []color.NRGBA, backdrop, target color.NRGBA) color.NRGBA {
	remaining := new(big.Rat).SetInt64(1)
	for _, value := range values {
		remaining.Mul(remaining, new(big.Rat).SetFrac64(int64(255-value.A), 255))
	}
	shape := new(big.Rat).Sub(new(big.Rat).SetInt64(1), remaining)
	backdropWeight := new(big.Rat).Mul(remaining, new(big.Rat).SetFrac64(int64(backdrop.A), 255))
	targetWeight := new(big.Rat).Mul(shape, new(big.Rat).SetFrac64(int64(target.A), 255))
	alpha := new(big.Rat).Add(backdropWeight, targetWeight)
	toByte := func(value *big.Rat) uint8 {
		value = new(big.Rat).Add(value, new(big.Rat).SetFrac64(1, 2))
		return uint8(new(big.Int).Quo(value.Num(), value.Denom()).Uint64())
	}
	result := color.NRGBA{A: toByte(new(big.Rat).Mul(alpha, new(big.Rat).SetInt64(255)))}
	if alpha.Sign() != 0 {
		channels := []*uint8{&result.R, &result.G, &result.B}
		for index, output := range channels {
			backdropComponent := []uint8{backdrop.R, backdrop.G, backdrop.B}[index]
			targetComponent := []uint8{target.R, target.G, target.B}[index]
			value := new(big.Rat).Mul(backdropWeight, new(big.Rat).SetInt64(int64(backdropComponent)))
			value.Add(value, new(big.Rat).Mul(targetWeight, new(big.Rat).SetInt64(int64(targetComponent))))
			*output = toByte(value.Quo(value, alpha))
		}
	}
	return result
}

func TestNestedNormalIndependentRationalOracle(t *testing.T) {
	data, err := os.ReadFile(testfixture.Path(t, "psdcompose/testdata/nested-knockout/oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		LeafRGB                                         [][3]uint16
		BackdropRGBA, ShallowTargetRGBA, DeepTargetRGBA [4]uint16
		Cases                                           []struct {
			AlphaBytes []uint16
			SourceRGBA [4]uint16
			Knockout   []struct {
				Mode, Opacity, Fill string
				RGBA                [4]uint16
			}
		}
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	if len(oracle.Cases) != 343 {
		t.Fatal("independent oracle case count mismatch")
	}
	toColor := func(rgba [4]uint16) color.NRGBA {
		return color.NRGBA{R: uint8(rgba[0]), G: uint8(rgba[1]), B: uint8(rgba[2]), A: uint8(rgba[3])}
	}
	for caseIndex, item := range oracle.Cases {
		leaves := make([]*psdmodel.Layer, 3)
		for index := range leaves {
			rgb := oracle.LeafRGB[index]
			leaves[index] = composeTestLayer(image.Rect(0, 0, 1, 1),
				color.NRGBA{R: uint8(rgb[0]), G: uint8(rgb[1]), B: uint8(rgb[2]), A: uint8(item.AlphaBytes[index])})
		}
		left := &psdmodel.Layer{Group: true, BlendMode: "norm", Children: leaves[:1]}
		right := &psdmodel.Layer{Group: true, BlendMode: "norm", Children: leaves[1:]}
		inner := &psdmodel.Layer{Group: true, BlendMode: "norm", Children: []*psdmodel.Layer{left, right}}
		group := &psdmodel.Layer{Group: true, BlendMode: "norm", Children: []*psdmodel.Layer{inner}}
		if actual := composeTestPixel(t, composeTestDocument(group)); actual != toColor(item.SourceRGBA) {
			t.Fatalf("independent source case %d actual=%v expected=%v", caseIndex, actual, item.SourceRGBA)
		}
		plan := &composePlan{}
		source := plan.knockoutSource(group, composePixel{}, knockoutScope{}, 0, 0)
		pixel := func(rgba [4]uint16) composePixel {
			return composePixel{color: [3]float64{float64(rgba[0]) / 255, float64(rgba[1]) / 255,
				float64(rgba[2]) / 255}, alpha: float64(rgba[3]) / 255}
		}
		backdrop := pixel(oracle.BackdropRGBA)
		for outputIndex, output := range item.Knockout {
			group.Knockout = psdmodel.KnockoutShallow
			if output.Mode == "deep" {
				group.Knockout = psdmodel.KnockoutDeep
			}
			opacity, validOpacity := new(big.Rat).SetString(output.Opacity)
			fill, validFill := new(big.Rat).SetString(output.Fill)
			if !validOpacity || !validFill {
				t.Fatal("invalid independent fraction")
			}
			opacityFloat, _ := opacity.Float64()
			fillFloat, _ := fill.Float64()
			group.Opacity, group.FillOpacity = &opacityFloat, &fillFloat
			scope := knockoutScope{shallow: pixel(oracle.ShallowTargetRGBA), deep: pixel(oracle.DeepTargetRGBA)}
			if output.Mode == "isolated" {
				scope.shallow = composePixel{}
			}
			actual := plan.knockoutPaint(group, "norm", source, backdrop, scope, 0, 0)
			rgba := color.NRGBA{R: composeByte(actual.color[0]), G: composeByte(actual.color[1]),
				B: composeByte(actual.color[2]), A: composeByte(actual.alpha)}
			if rgba != toColor(output.RGBA) {
				t.Fatalf("independent group paint case %d/%d actual=%v expected=%v", caseIndex, outputIndex, rgba, output.RGBA)
			}
		}
	}
}

func nestedKnockoutDocument(values []color.NRGBA, mode psdmodel.Knockout, background bool) *psdmodel.Document {
	bottom := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 21, G: 71, B: 131, A: 255})
	bottom.Background = background
	outside := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 61, G: 111, B: 171, A: 255})
	children := make([]*psdmodel.Layer, len(values))
	for index, value := range values {
		children[index] = composeTestLayer(image.Rect(0, 0, 1, 1), value)
	}
	inner := &psdmodel.Layer{Group: true, BlendMode: "norm", Children: children}
	middle := &psdmodel.Layer{Group: true, BlendMode: "norm", Children: []*psdmodel.Layer{inner}}
	knockout := &psdmodel.Layer{Group: true, BlendMode: "pass", Knockout: mode,
		FillOpacity: new(float64(0)), Children: []*psdmodel.Layer{middle}}
	outer := &psdmodel.Layer{Group: true, BlendMode: "pass", Children: []*psdmodel.Layer{outside, knockout}}
	return composeTestDocument(bottom, outer)
}

func TestNestedNormalGroupKnockoutRationalShape(t *testing.T) {
	for _, alphaA := range []uint8{0, 1, 64, 128, 192, 254, 255} {
		for _, alphaB := range []uint8{0, 1, 64, 128, 192, 254, 255} {
			values := []color.NRGBA{{R: 240, G: 40, B: 80, A: alphaA}, {R: 40, G: 200, B: 160, A: alphaB}}
			for _, mode := range []psdmodel.Knockout{psdmodel.KnockoutShallow, psdmodel.KnockoutDeep} {
				for _, background := range []bool{false, true} {
					document := nestedKnockoutDocument(values, mode, background)
					target := color.NRGBA{R: 21, G: 71, B: 131, A: 255}
					if mode == psdmodel.KnockoutDeep && !background {
						target = color.NRGBA{}
					}
					expected := nestedKnockoutRat(values, color.NRGBA{R: 61, G: 111, B: 171, A: 255}, target)
					actual := composeTestPixel(t, document)
					if actual != expected {
						t.Fatalf("alpha=%d/%d mode=%d background=%t got=%v expected=%v",
							alphaA, alphaB, mode, background, actual, expected)
					}
				}
			}
		}
	}
}

func TestNestedNormalKnockoutEligibilityGuards(t *testing.T) {
	mutations := []struct {
		name string
		edit func(*psdmodel.Layer)
	}{
		{"pass", func(layer *psdmodel.Layer) { layer.BlendMode = "pass" }},
		{"opacity", func(layer *psdmodel.Layer) { layer.Opacity = new(.5) }},
		{"fill", func(layer *psdmodel.Layer) { layer.FillOpacity = new(.5) }},
		{"mask", func(layer *psdmodel.Layer) { layer.Mask = &psdmodel.Mask{} }},
		{"clip", func(layer *psdmodel.Layer) { layer.Clipping = true }},
		{"knockout", func(layer *psdmodel.Layer) { layer.Knockout = psdmodel.KnockoutDeep }},
		{"channels", func(layer *psdmodel.Layer) { layer.ChannelRestrictions = []uint32{0} }},
		{"transparency", func(layer *psdmodel.Layer) { layer.TransparencyShapes = new(false) }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			document := nestedKnockoutDocument([]color.NRGBA{{A: 128}}, psdmodel.KnockoutDeep, false)
			middle := document.Layers[1].Children[1].Children[0]
			mutation.edit(middle)
			if err := Validate(document); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("advanced nested group accepted: %v", err)
			}
			middle.Hidden = true
			if err := Validate(document); err != nil {
				t.Fatalf("hidden advanced nested group blocks shape: %v", err)
			}
		})
	}
}

func TestNestedNormalKnockoutStructuralBudgetsAndCancellation(t *testing.T) {
	document := nestedKnockoutDocument([]color.NRGBA{{A: 128}}, psdmodel.KnockoutDeep, false)
	inner := document.Layers[1].Children[1].Children[0].Children[0]
	inner.Children = append(inner.Children, inner)
	if err := Validate(document); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cycle did not terminate as structural invalid: %v", err)
	}
	inner.Children = inner.Children[:1]
	duplicate := &psdmodel.Layer{Group: true, BlendMode: "mul ", Children: []*psdmodel.Layer{inner}}
	document.Layers = append(document.Layers, duplicate)
	if err := Validate(document); !errors.Is(err, ErrInvalid) {
		t.Fatalf("completed unsafe group reused without structural rejection: %v", err)
	}
	document.Layers = document.Layers[:2]
	unsafe := &psdmodel.Layer{Group: true, BlendMode: "mul "}
	document.Layers = append(document.Layers, unsafe, unsafe)
	if err := Validate(document); !errors.Is(err, ErrInvalid) {
		t.Fatalf("false shape summary bypassed repeated group check: %v", err)
	}
	document.Layers = document.Layers[:2]
	for _, options := range []Options{{MaxDepth: 4}, {MaxLayers: 5}, {MaxWorkingBytes: 1000}} {
		if err := Validate(document, options); !errors.Is(err, ErrLimit) {
			t.Fatalf("nested tree exceeded configured budget: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if output, err := Render(ctx, document); output != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled nested Knockout returned partial output: %v %v", output, err)
	}
}

func TestNestedNormalKnockoutRegionAndSourceIsolation(t *testing.T) {
	document := nestedKnockoutDocument([]color.NRGBA{{R: 210, G: 30, B: 90, A: 64},
		{R: 30, G: 190, B: 50, A: 128}}, psdmodel.KnockoutDeep, false)
	document.Width, document.Height = 3, 2
	full, err := Render(nil, document)
	if err != nil {
		t.Fatal(err)
	}
	window := image.Rect(0, 0, 1, 1)
	regional, err := RenderRegion(nil, document, window)
	if err != nil {
		t.Fatal(err)
	}
	if regional.NRGBAAt(0, 0) != full.NRGBAAt(0, 0) {
		t.Fatalf("ROI differs from full nested Knockout: %v %v", regional.Pix, full.Pix)
	}
	leaf := document.Layers[1].Children[1].Children[0].Children[0].Children[0]
	if leaf.Pixels.NRGBAAt(0, 0) != (color.NRGBA{R: 210, G: 30, B: 90, A: 64}) {
		t.Fatal("nested Knockout modified caller-owned source pixels")
	}
}
