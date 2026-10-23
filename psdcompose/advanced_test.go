package psdcompose

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdgradient"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdpattern"
)

func TestAdvancedTransparencyModulatesFill(t *testing.T) {
	layer := &psdmodel.Layer{
		Opacity: new(.5), FillOpacity: new(.75),
		TransparencyShapes: new(false),
	}
	coverage, fill := advancedCoverageFill(layer, .25, .5, .75)
	if coverage != .1875 || fill != .1875 {
		t.Fatalf("transparency should move from coverage into Fill: coverage=%g fill=%g", coverage, fill)
	}
	layer.TransparencyShapes = nil
	coverage, fill = advancedCoverageFill(layer, .25, .5, .75)
	if coverage != .046875 || fill != .75 {
		t.Fatalf("default transparency shape changed: coverage=%g fill=%g", coverage, fill)
	}
}

func TestAdvancedHardMixUsesNativeByteFill(t *testing.T) {
	layer := &psdmodel.Layer{TransparencyShapes: new(false)}
	backdrop := [3]float64{0, 1, 60.0 / 255}
	source := [3]float64{1, 0, 250.0 / 255}
	result, err := advancedBlendRGB(layer, "hMix", backdrop, source, 191.0/255)
	if err != nil {
		t.Fatal(err)
	}
	if result != ([3]float64{0, 1, 220.0 / 255}) {
		t.Fatalf("Hard Mix native effective Fill ramp = %v", result)
	}
	result, err = advancedBlendRGB(layer, "hMix", backdrop, source, 0)
	if err != nil || result != backdrop {
		t.Fatalf("zero effective Fill must leave the backdrop: %v, %v", result, err)
	}
	layer.TransparencyShapes = nil
	result, err = advancedBlendRGB(layer, "mul", backdrop, source, .25)
	if err != nil || result != ([3]float64{0, 0, 60.0 * 250 / (255 * 255)}) {
		t.Fatalf("ordinary blend kernel changed: %v, %v", result, err)
	}
}

func TestPassGroupIntrinsicKeepsChildBlendAndRemovesBackdrop(t *testing.T) {
	backdrop := composePixel{color: [3]float64{.8, .4, .2}, alpha: .5}
	groupAlpha := float64(.25)
	expected := [3]float64{.2, .3, .4}
	fullAlpha := groupAlpha + backdrop.alpha*(1-groupAlpha)
	full := composePixel{alpha: fullAlpha}
	for component := range 3 {
		full.color[component] = (groupAlpha*expected[component] +
			(1-groupAlpha)*backdrop.alpha*backdrop.color[component]) / fullAlpha
	}
	result := passGroupIntrinsic(full, backdrop, groupAlpha)
	if result.alpha != groupAlpha {
		t.Fatalf("group contribution grew its coverage: %g", result.alpha)
	}
	for component := range 3 {
		if math.Abs(result.color[component]-expected[component]) > 1e-14 {
			t.Fatalf("backdrop counted twice in group component %d: %v", component, result)
		}
	}
	if result := passGroupIntrinsic(full, backdrop, 0); result != (composePixel{}) {
		t.Fatalf("zero-contribution group should stay empty: %v", result)
	}
}

func passRangesTestGroup() *psdmodel.Layer {
	identity := psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 255}
	child := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 220, G: 40, B: 60, A: 128})
	child.Opacity, child.FillOpacity = new(.4), new(.7)
	return &psdmodel.Layer{Group: true, BlendMode: "pass", Children: []*psdmodel.Layer{child},
		Opacity: new(.5), BlendRanges: []psdmodel.BlendRange{{
			Source:      psdmodel.BlendSlider{BlackLow: 32, BlackHigh: 96, WhiteLow: 160, WhiteHigh: 224},
			Destination: identity,
		}}}
}

func TestPassGroupNormalRangesIncludesOnlyCalibratedSources(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*psdmodel.Layer)
		valid  bool
	}{
		{name: "single normal source", valid: true},
		{name: "multiple normal sources", valid: true, change: func(group *psdmodel.Layer) {
			group.Children = append(group.Children,
				composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{B: 200, A: 64}))
		}},
		{name: "default extra ranges", valid: true, change: func(group *psdmodel.Layer) {
			identity := psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 255}
			group.Children[0].BlendRanges = make([]psdmodel.BlendRange, 5)
			for index := range group.Children[0].BlendRanges {
				group.Children[0].BlendRanges[index] = psdmodel.BlendRange{Source: identity, Destination: identity}
			}
		}},
		{name: "inactive unsupported child", valid: true, change: func(group *psdmodel.Layer) {
			group.Children = append(group.Children, &psdmodel.Layer{Hidden: true, BlendMode: "diss"})
		}},
		{name: "zero opacity child", valid: true, change: func(group *psdmodel.Layer) {
			group.Children = append(group.Children, &psdmodel.Layer{Opacity: new(float64(0)), BlendMode: "hMix"})
		}},
		{name: "group has no own ranges", change: func(group *psdmodel.Layer) { group.BlendRanges = nil }},
		{name: "isolated group", change: func(group *psdmodel.Layer) { group.BlendMode = "norm" }},
		{name: "group is clipped", change: func(group *psdmodel.Layer) { group.Clipping = true }},
		{name: "multiply child counterexample", change: func(group *psdmodel.Layer) {
			group.Children[0].BlendMode = "mul "
		}},
		{name: "multi Screen counterexample", change: func(group *psdmodel.Layer) {
			second := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{B: 200, A: 64})
			second.BlendMode, second.Opacity = "scrn", new(.4)
			group.Children = append(group.Children, second)
		}},
		{name: "nested Normal counterexample", change: func(group *psdmodel.Layer) {
			group.Children[0] = &psdmodel.Layer{Group: true, BlendMode: "norm", Opacity: new(.5),
				Children: []*psdmodel.Layer{group.Children[0]}}
		}},
		{name: "special Fill counterexample", change: func(group *psdmodel.Layer) {
			group.Children[0].BlendMode, group.Children[0].FillOpacity = "idiv", new(.5)
		}},
		{name: "own child ranges", change: func(group *psdmodel.Layer) {
			group.Children[0].BlendRanges = group.BlendRanges
		}},
		{name: "child clipping", change: func(group *psdmodel.Layer) { group.Children[0].Clipping = true }},
		{name: "child tsly false", change: func(group *psdmodel.Layer) {
			group.Children[0].TransparencyShapes = new(false)
		}},
		{name: "child effects", change: func(group *psdmodel.Layer) {
			group.Children[0].Effects = &psdeffects.Set{Enabled: true, EffectsVisible: true}
		}},
		{name: "group effects", change: func(group *psdmodel.Layer) {
			group.Effects = &psdeffects.Set{Enabled: true, EffectsVisible: true}
		}},
		{name: "adjustment", change: func(group *psdmodel.Layer) {
			group.Children[0].Adjustment = &psdadjust.Adjustment{}
		}},
		{name: "solid fill", change: func(group *psdmodel.Layer) { group.Children[0].FillColor = &[3]float64{} }},
		{name: "gradient", change: func(group *psdmodel.Layer) { group.Children[0].Gradient = &psdgradient.Fill{} }},
		{name: "pattern", change: func(group *psdmodel.Layer) { group.Children[0].Pattern = &psdpattern.Fill{} }},
		{name: "placed source", change: func(group *psdmodel.Layer) { group.Children[0].Placed = &psdmodel.Placed{} }},
		{name: "missing raster", change: func(group *psdmodel.Layer) { group.Children[0].Pixels = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			group := passRangesTestGroup()
			if test.change != nil {
				test.change(group)
			}
			if supported := passGroupNormalRanges(group); supported != test.valid {
				t.Fatalf("eligibility=%v, want %v", supported, test.valid)
			}
		})
	}
}

func TestPassRangesClipBaseCheckRejectsEveryActiveSiblingDepth(t *testing.T) {
	base := passRangesTestGroup()
	clipped := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{B: 200, A: 128})
	clipped.Clipping = true
	for _, test := range []struct {
		name   string
		layers []*psdmodel.Layer
		path   string
	}{
		{name: "top level", layers: []*psdmodel.Layer{base, clipped}, path: "document.layers[0].blendRanges"},
		{name: "inside isolated group", layers: []*psdmodel.Layer{{Group: true, BlendMode: "norm",
			Children: []*psdmodel.Layer{base, clipped}}}, path: "document.layers[0].children[0].blendRanges"},
		{name: "inside pass group", layers: []*psdmodel.Layer{{Group: true, BlendMode: "pass",
			Children: []*psdmodel.Layer{base, clipped}}}, path: "document.layers[0].children[0].blendRanges"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := passRangesClipBaseCheck(test.layers)
			var typed *Error
			if !errors.Is(err, ErrUnsupported) || !errors.As(err, &typed) || typed.Path != test.path {
				t.Fatalf("uncalibrated clipping was not rejected structurally: %v", err)
			}
		})
	}
	plain := *base
	plain.BlendRanges = nil
	if err := passRangesClipBaseCheck([]*psdmodel.Layer{&plain, clipped}); err != nil {
		t.Fatalf("existing ordinary pass clipping changed: %v", err)
	}
	isolated := *base
	isolated.BlendMode = "norm"
	if err := passRangesClipBaseCheck([]*psdmodel.Layer{&isolated, clipped}); err != nil {
		t.Fatalf("existing isolated clipping changed: %v", err)
	}
	hidden := &psdmodel.Layer{Group: true, Hidden: true, Children: []*psdmodel.Layer{base, clipped}}
	if err := passRangesClipBaseCheck([]*psdmodel.Layer{hidden}); err != nil {
		t.Fatalf("invisible uncalibrated content blocked rendering: %v", err)
	}
}

func TestComposePassGroupNormalRangesAppliesOwnedSourceAndDestinationGatesOnce(t *testing.T) {
	group := passRangesTestGroup()
	child := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 128, G: 128, B: 128, A: 128})
	child.Opacity, child.FillOpacity = new(.5), new(.5)
	group.Children = []*psdmodel.Layer{child}
	group.BlendRanges = []psdmodel.BlendRange{{
		Source:      psdmodel.BlendSlider{BlackLow: 64, BlackHigh: 192, WhiteLow: 255, WhiteHigh: 255},
		Destination: psdmodel.BlendSlider{BlackLow: 32, BlackHigh: 96, WhiteLow: 255, WhiteHigh: 255},
	}}
	group.Mask = &psdmodel.Mask{Bounds: image.Rect(0, 0, 1, 1),
		Pixels: composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 128, A: 255}).Pixels}
	backdrop := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 64, G: 64, B: 64, A: 128})
	beforeChild, beforeMask := bytes.Clone(child.Pixels.Pix), bytes.Clone(group.Mask.Pixels.Pix)
	beforeRange := group.BlendRanges[0]
	document := composeTestDocument(backdrop, group)
	document.Composite = composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{G: 255, A: 255}).Pixels
	// The group gates multiply coverage by 65/129 and (1-128/255)+(128/255)*(33/65).
	// Child opacity/Fill and group opacity/mask are independent factors, each applied once.
	if actual := composeTestPixel(t, document); actual != (color.NRGBA{R: 66, G: 66, B: 66, A: 130}) {
		t.Fatalf("group gates, mask or opacity were evaluated against the backdrop twice: %v", actual)
	}
	if !bytes.Equal(child.Pixels.Pix, beforeChild) || !bytes.Equal(group.Mask.Pixels.Pix, beforeMask) ||
		group.BlendRanges[0] != beforeRange {
		t.Fatal("rendering changed borrowed source pixels, coverage or ranges")
	}
}

func TestComposePassGroupNormalRangesUsesMergedChildColor(t *testing.T) {
	group := passRangesTestGroup()
	group.Children = []*psdmodel.Layer{
		composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 255, A: 128}),
		composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{B: 255, A: 128}),
	}
	identity := psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 255}
	group.BlendRanges = []psdmodel.BlendRange{
		{Source: identity, Destination: identity},
		{Source: psdmodel.BlendSlider{BlackHigh: 128, WhiteLow: 255, WhiteHigh: 255}, Destination: identity},
	}
	backdrop := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{A: 128})
	// The red slider sees the merged red component near 85, before group opacity or backdrop insertion.
	if actual := composeTestPixel(t, composeTestDocument(backdrop, group)); actual != (color.NRGBA{R: 34, B: 68, A: 160}) {
		t.Fatalf("group source range tested a child or a backdrop-contaminated color: %v", actual)
	}
}

func TestComposePassGroupRangesRejectsUncalibratedHierarchyAndExternalClips(t *testing.T) {
	group := passRangesTestGroup()
	nested := &psdmodel.Layer{Group: true, BlendMode: "norm", Opacity: new(.5),
		Children: group.Children}
	group.Children = []*psdmodel.Layer{nested}
	if err := Validate(composeTestDocument(group)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("nested own-range group was approximated: %v", err)
	}
	group = passRangesTestGroup()
	clipped := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{B: 255, A: 128})
	clipped.Clipping = true
	parent := &psdmodel.Layer{Group: true, BlendMode: "norm", Children: []*psdmodel.Layer{group, clipped}}
	err := Validate(composeTestDocument(parent))
	var typed *Error
	if !errors.Is(err, ErrUnsupported) || !errors.As(err, &typed) ||
		typed.Path != "document.layers[0].children[0].blendRanges" {
		t.Fatalf("nested external clip base was not rejected structurally: %v", err)
	}
}
