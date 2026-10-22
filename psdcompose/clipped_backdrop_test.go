package psdcompose

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func backdropApproximationDocument(bounds image.Rectangle) *psdmodel.Document {
	backdrop := composeTestLayer(bounds, color.NRGBA{R: 37, G: 119, B: 203, A: 96})
	child := composeTestLayer(bounds, color.NRGBA{R: 213, G: 54, B: 128, A: 173})
	child.BlendMode = "mul "
	group := &psdmodel.Layer{Group: true, BlendMode: "pass", Opacity: new(137.0 / 255),
		Children: []*psdmodel.Layer{child}}
	member := composeTestLayer(bounds, color.NRGBA{R: 80, G: 170, B: 30, A: 128})
	member.BlendMode, member.Hidden, member.Clipping = "scrn", true, true
	return &psdmodel.Document{Width: bounds.Max.X, Height: bounds.Max.Y,
		Layers: []*psdmodel.Layer{backdrop, group, member}}
}

func TestApproximateClippedBackdropDefaultsAndExplicitRGB(t *testing.T) {
	document := backdropApproximationDocument(image.Rect(0, 0, 1, 1))
	legacy, err := Render(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	explicitFalse, err := Render(context.Background(), document, Options{ApproximateClippedGroupBackdrop: false})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacy.Pix, explicitFalse.Pix) {
		t.Fatal("zero-value approximation changed legacy pixels")
	}
	approximate, err := Render(context.Background(), document, Options{ApproximateClippedGroupBackdrop: true})
	if err != nil {
		t.Fatal(err)
	}
	// These two RGB8 values are independently frozen public408 base/v3 predictions for the own U fixture.
	if actual := legacy.NRGBAAt(0, 0); actual != (color.NRGBA{R: 102, G: 73, B: 152, A: 154}) {
		t.Fatalf("legacy color changed: %v", actual)
	}
	if actual := approximate.NRGBAAt(0, 0); actual != (color.NRGBA{R: 106, G: 74, B: 152, A: 154}) {
		t.Fatalf("explicit reconstruction changed frozen v3 color: %v", actual)
	}
}

func TestApproximateClippedBackdropScopeAndRegions(t *testing.T) {
	document := backdropApproximationDocument(image.Rect(0, 0, 8, 4))
	option := Options{ApproximateClippedGroupBackdrop: true}
	full, err := Render(context.Background(), document, option)
	if err != nil {
		t.Fatal(err)
	}
	region := image.Rect(2, 1, 7, 4)
	tile, err := RenderRegion(context.Background(), document, region, option)
	if err != nil {
		t.Fatal(err)
	}
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
				t.Fatalf("regional approximation differs at %d,%d", x, y)
			}
		}
	}
	document.Layers = document.Layers[:2]
	for _, groupMode := range []string{"pass", "norm"} {
		document.Layers[1].BlendMode = groupMode
		baseline, err := Render(context.Background(), document)
		if err != nil {
			t.Fatal(err)
		}
		selected, err := Render(context.Background(), document, option)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(baseline.Pix, selected.Pix) {
			t.Fatalf("non-clipped %s group changed", groupMode)
		}
	}
}

func TestApproximateClippedBackdropKnockoutAndGuards(t *testing.T) {
	document := backdropApproximationDocument(image.Rect(0, 0, 1, 1))
	option := Options{ApproximateClippedGroupBackdrop: true}
	regular, err := Render(context.Background(), document, option)
	if err != nil {
		t.Fatal(err)
	}
	inactive := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{})
	inactive.Hidden, inactive.Knockout = true, psdmodel.KnockoutShallow
	document.Layers = append(document.Layers, inactive)
	traversed, err := Render(context.Background(), document, option)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(regular.Pix, traversed.Pix) {
		t.Fatal("Knockout traversal changed the clipping reconstruction")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if partial, err := Render(ctx, document, option); partial != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled approximation returned %v, %v", partial, err)
	}
	if partial, err := Render(context.Background(), document, Options{
		ApproximateClippedGroupBackdrop: true, MaxWorkingBytes: 1,
	}); partial != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("budgeted approximation returned %v, %v", partial, err)
	}
	document.Layers[1].Children = append(document.Layers[1].Children, document.Layers[1])
	if err := Validate(document, option); !errors.Is(err, ErrInvalid) {
		t.Fatalf("approximation bypassed cycle validation: %v", err)
	}
}
