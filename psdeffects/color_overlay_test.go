package psdeffects

import (
	"bytes"
	"context"
	"errors"
	"image"
	"testing"
)

func TestColorOverlayDecodeAndInteriorPlanePreserveSourceCoverage(t *testing.T) {
	overlay := &Descriptor{ClassID: "SoFi", Entries: []Entry{
		{Key: "enab", Type: "bool", Value: true},
		{Key: "Clr ", Type: "Objc", Value: effectRGB()},
		{Key: "Opct", Type: "UntF", Value: Unit{Unit: "#Prc", Number: 63}},
		{Key: "Md  ", Type: "enum", Value: Enum{TypeID: "BlnM", ValueID: "Mltp"}},
	}}
	set, err := Decode(nil, &Descriptor{Entries: []Entry{{Key: "SoFi", Type: "Objc", Value: overlay}}})
	if err != nil || len(set.Items) != 1 || set.Items[0].Kind != ColorOverlay ||
		set.Items[0].BlendMode != "mul " || set.Items[0].Opacity != .63 {
		t.Fatalf("color overlay descriptor differs: %v, %v", set, err)
	}
	alpha := &image.Alpha{Rect: image.Rect(7, 11, 10, 13), Stride: 5,
		Pix: []byte{0, 127, 255, 17, 19, 4, 63, 199}}
	before := bytes.Clone(alpha.Pix)
	result, err := Rasterize(nil, alpha, set)
	if err != nil || len(result.Interior) != 1 || len(result.Background) != 0 || len(result.Foreground) != 0 {
		t.Fatalf("overlay interior plane was not retained independently: %v, %v", result, err)
	}
	plane := result.Interior[0]
	if !bytes.Equal(plane.Coverage.Pix, []byte{0, 127, 255, 4, 63, 199}) || plane.Coverage.Rect != alpha.Rect ||
		plane.Kind != ColorOverlay || plane.Opacity != .63 || plane.Color != [3]float64{1, 0, 0} {
		t.Fatal("overlay coverage changed source alpha or baked color/opacity")
	}
	plane.Coverage.Pix[1] = 1
	if !bytes.Equal(alpha.Pix, before) {
		t.Fatal("overlay output borrowed caller alpha")
	}
	overlay.Entries[1].Value.(*Descriptor).Entries[0].Value = float64(0)
	if set.Items[0].Color[0] != 1 {
		t.Fatal("overlay typed metadata borrowed caller descriptor")
	}
}

func TestColorOverlayEstimateOwnsExactlyOnePlaneWithoutBlurScratch(t *testing.T) {
	set := primitiveSet(primitive(ColorOverlay))
	bounds := image.Rect(-2, 3, 8, 13)
	estimate, err := EstimateWorking(bounds, set, Options{MaxWorkingBytes: 612})
	if err != nil || estimate.OutputBytes != 612 || estimate.ScratchBytes != 0 || estimate.SampleWork != 100 ||
		estimate.Planes != 1 || estimate.Bounds != bounds {
		t.Fatalf("constant overlay plane allocation estimate differs: %#v, %v", estimate, err)
	}
	if _, err := Rasterize(nil, rectangleAlpha(bounds, 99), set, Options{MaxWorkingBytes: 612}); err != nil {
		t.Fatalf("exact overlay coverage budget was rejected: %v", err)
	}
	if result, err := Rasterize(nil, rectangleAlpha(bounds, 99), set, Options{MaxWorkingBytes: 611}); !errors.Is(err, ErrLimit) || result != nil {
		t.Fatalf("overlay coverage escaped working budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := Rasterize(ctx, rectangleAlpha(bounds, 99), set); !errors.Is(err, context.Canceled) || result != nil {
		t.Fatalf("canceled overlay returned coverage: %v", err)
	}
}

func TestColorOverlayPlanesReverseSourceOrderAndLeaveMetadataUnchanged(t *testing.T) {
	top, bottom := primitive(ColorOverlay), primitive(ColorOverlay)
	top.Color, bottom.Color = [3]float64{1, 0, 0}, [3]float64{0, 0, 1}
	set := primitiveSet(top, bottom)
	result, err := Rasterize(nil, rectangleAlpha(image.Rect(0, 0, 1, 1), 127), set)
	if err != nil || len(result.Interior) != 2 || result.Interior[0].Color != bottom.Color ||
		result.Interior[1].Color != top.Color {
		t.Fatalf("native topmost-first source list did not produce bottommost-first paint planes: %v, %v", result, err)
	}
	if set.Items[0].Color != top.Color || set.Items[1].Color != bottom.Color {
		t.Fatal("rasterizer reordered the retained source metadata")
	}
}
