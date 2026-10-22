package psdeffects

import (
	"bytes"
	"context"
	"errors"
	"image"
	"testing"
)

func TestInnerFamiliesDecodeOrderedSourceAndOwnMetadata(t *testing.T) {
	for _, family := range []struct {
		key  string
		kind Kind
	}{{"IrSh", InnerShadow}, {"innerShadowMulti", InnerShadow}, {"IrGl", InnerGlow}, {"innerGlow", InnerGlow}} {
		descriptor := &Descriptor{ClassID: family.key, Entries: []Entry{
			{Key: "enab", Type: "bool", Value: true},
			{Key: "Clr ", Type: "Objc", Value: effectRGB()},
			{Key: "blur", Type: "UntF", Value: Unit{Unit: "#Pxl", Number: 2}},
			{Key: "Ckmt", Type: "UntF", Value: Unit{Unit: "#Pxl", Number: 100}},
			{Key: "glwS", Type: "enum", Value: Enum{TypeID: "IGSr", ValueID: "SrcC"}},
		}}
		set, err := Decode(nil, &Descriptor{Entries: []Entry{{Key: family.key, Type: "Objc", Value: descriptor}}})
		if err != nil || len(set.Items) != 1 || set.Items[0].Kind != family.kind ||
			set.Items[0].GlowSource != CenterSource || set.Items[0].Size != 2 || set.Items[0].Spread != 1 {
			t.Fatalf("inner family typed parameters differ: %#v, %v", set, err)
		}
		if err := Validate(set); err != nil {
			t.Fatal(err)
		}
		descriptor.Entries[2].Value = Unit{Unit: "#Pxl", Number: 9}
		if set.Items[0].Size != 2 {
			t.Fatal("inner effect decoder borrowed caller metadata")
		}
	}
}

func TestInnerShadowComplementDisplacementAndOwnedCoverage(t *testing.T) {
	alpha := rectangleAlpha(image.Rect(2, 3, 8, 9), 255)
	before := bytes.Clone(alpha.Pix)
	shadow := primitive(InnerShadow)
	shadow.Distance = 2
	result, err := Rasterize(nil, alpha, primitiveSet(shadow))
	if err != nil || len(result.Interior) != 1 || len(result.Background) != 0 || result.Bounds != alpha.Rect {
		t.Fatalf("inner shadow escaped interior output: %#v, %v", result, err)
	}
	coverage := result.Interior[0].Coverage
	if coverage.AlphaAt(6, 5).A != 255 || coverage.AlphaAt(5, 5).A != 0 || coverage.AlphaAt(0, 5).A != 0 {
		t.Fatal("hard inner shadow does not equal source clipped to displaced exterior")
	}
	shadow.UseGlobalLight = true
	global, err := Rasterize(nil, alpha, primitiveSet(shadow))
	if err != nil || global.Interior[0].Coverage.AlphaAt(4, 3).A != 255 ||
		global.Interior[0].Coverage.AlphaAt(4, 5).A != 0 {
		t.Fatalf("inner shadow did not obey global angle: %v", err)
	}
	coverage.Pix[0] = 99
	if !bytes.Equal(alpha.Pix, before) {
		t.Fatal("inner-shadow coverage borrowed caller source alpha")
	}
}

func TestInnerGlowEdgeCenterAreComplementaryInsideTheShape(t *testing.T) {
	alpha := rectangleAlpha(image.Rect(2, 3, 12, 13), 255)
	glow := primitive(InnerGlow)
	glow.Size, glow.Spread, glow.GlowSource = 2, 1, Edge
	edge, err := Rasterize(nil, alpha, primitiveSet(glow))
	if err != nil {
		t.Fatal(err)
	}
	glow.GlowSource = CenterSource
	center, err := Rasterize(nil, alpha, primitiveSet(glow))
	if err != nil {
		t.Fatal(err)
	}
	for y := alpha.Rect.Min.Y; y < alpha.Rect.Max.Y; y++ {
		for x := alpha.Rect.Min.X; x < alpha.Rect.Max.X; x++ {
			if int(edge.Interior[0].Coverage.AlphaAt(x, y).A)+int(center.Interior[0].Coverage.AlphaAt(x, y).A) != 255 {
				t.Fatal("inner edge and center glow do not partition source coverage")
			}
		}
	}
	if edge.Bounds != alpha.Rect || edge.Interior[0].Coverage.AlphaAt(2, 5).A != 255 ||
		center.Interior[0].Coverage.AlphaAt(7, 8).A != 255 || center.Interior[0].Coverage.AlphaAt(1, 5).A != 0 {
		t.Fatal("inner glow coverage escaped its source or lost its edge/center distinction")
	}
}

func TestInnerEffectsEstimateBudgetsAndCancellationRemainAtomic(t *testing.T) {
	alpha := rectangleAlpha(image.Rect(2, 3, 12, 13), 255)
	shadow := primitive(InnerShadow)
	shadow.Distance, shadow.Size = 2, 4
	set := primitiveSet(shadow)
	estimate, err := EstimateWorking(alpha.Rect, set)
	if err != nil || estimate.Bounds != alpha.Rect {
		t.Fatalf("inner effect bounds estimate differs: %#v, %v", estimate, err)
	}
	options := Options{MaxWorkingBytes: estimate.OutputBytes + estimate.ScratchBytes}
	if _, err := Rasterize(nil, alpha, set, options); err != nil {
		t.Fatalf("exact inner effect peak budget failed: %v", err)
	}
	options = Options{MaxSampleWork: estimate.SampleWork - 1}
	if output, err := Rasterize(nil, alpha, set, options); !errors.Is(err, ErrLimit) || output != nil {
		t.Fatalf("inner effect sample budget returned partial coverage: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if output, err := Rasterize(ctx, alpha, set); !errors.Is(err, context.Canceled) || output != nil {
		t.Fatalf("canceled inner effect returned partial coverage: %v", err)
	}
}

func TestInnerShadowMultiPlanesKeepTopmostDescriptorInstanceLast(t *testing.T) {
	top, bottom := primitive(InnerShadow), primitive(InnerShadow)
	top.Color, bottom.Color = [3]float64{1, 0, 0}, [3]float64{0, 0, 1}
	top.Distance, bottom.Distance = 1, 2
	set := primitiveSet(top, bottom)
	result, err := Rasterize(nil, rectangleAlpha(image.Rect(0, 0, 4, 4), 255), set)
	if err != nil || len(result.Interior) != 2 || result.Interior[0].Color != bottom.Color ||
		result.Interior[1].Color != top.Color {
		t.Fatalf("native topmost-first inner-shadow list lost its paint order: %#v, %v", result, err)
	}
	if set.Items[0].Color != top.Color || set.Items[1].Color != bottom.Color {
		t.Fatal("inner-shadow coverage generation reordered the caller descriptor")
	}
}

func TestLargeInnerShadowDistanceDoesNotAllocateDisplacedExterior(t *testing.T) {
	alpha := rectangleAlpha(image.Rect(5, 7, 9, 11), 255)
	shadow := primitive(InnerShadow)
	shadow.Distance = 100_000
	set := primitiveSet(shadow)
	estimate, err := EstimateWorking(alpha.Rect, set)
	if err != nil || estimate.OutputBytes != 528 || estimate.Bounds != alpha.Rect {
		t.Fatalf("long inner displacement unnecessarily retained exterior output: %#v, %v", estimate, err)
	}
	result, err := Rasterize(nil, alpha, set, Options{MaxWorkingBytes: 664})
	if err != nil || result.Interior[0].Coverage.Rect != alpha.Rect {
		t.Fatalf("bounded inner displacement failed despite a tiny output shape: %v", err)
	}
	for _, value := range result.Interior[0].Coverage.Pix {
		if value != 255 {
			t.Fatal("far-displaced exterior did not fully shadow the source interior")
		}
	}
}
