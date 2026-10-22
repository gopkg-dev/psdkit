package psdedit

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestCompositeGroupIsolatesChildrenAndPreservesRevision(t *testing.T) {
	editor, err := Create(t.Context(), 8, 6)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = editor.Close() })
	background := image.NewNRGBA(image.Rect(0, 0, 8, 6))
	draw.Draw(background, background.Rect, image.NewUniform(color.NRGBA{G: 255, A: 255}), image.Point{}, draw.Src)
	if err := editor.ReplacePixels(1, background); err != nil {
		t.Fatal(err)
	}
	ancestor, err := editor.AddGroup(0, 1, "ancestor", true)
	if err != nil {
		t.Fatal(err)
	}
	group, err := editor.AddGroup(ancestor, 0, "isolated", true)
	if err != nil {
		t.Fatal(err)
	}
	red := image.NewNRGBA(image.Rect(0, 0, 3, 3))
	draw.Draw(red, red.Rect, image.NewUniform(color.NRGBA{R: 255, A: 255}), image.Point{}, draw.Src)
	child, err := editor.AddRaster(group, 0, "red", red, image.Pt(2, 1))
	if err != nil {
		t.Fatal(err)
	}
	blue := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	blue.SetNRGBA(0, 0, color.NRGBA{B: 255, A: 255})
	if _, err := editor.AddRaster(group, 1, "blue", blue, image.Pt(3, 2)); err != nil {
		t.Fatal(err)
	}
	if err := editor.SetVisible(ancestor, false); err != nil {
		t.Fatal(err)
	}
	if err := editor.SetOpacity(group, 128); err != nil {
		t.Fatal(err)
	}
	before, err := editor.Changes()
	if err != nil {
		t.Fatal(err)
	}
	ownedBefore := editor.ownedBytesLocked(nil)
	for range 2 {
		pixels, err := editor.CompositeGroup(nil, group)
		if err != nil {
			t.Fatal(err)
		}
		if pixels.Rect != image.Rect(2, 1, 5, 4) || pixels.Stride != 12 || len(pixels.Pix) != 36 {
			t.Fatalf("group was not tightly cropped in document coordinates: %+v", pixels)
		}
		if pixels.NRGBAAt(2, 1) != (color.NRGBA{R: 255, A: 128}) ||
			pixels.NRGBAAt(3, 2) != (color.NRGBA{B: 255, A: 128}) {
			t.Fatal("group opacity, child order or isolation changed")
		}
		pixels.SetNRGBA(2, 1, color.NRGBA{})
	}
	after, err := editor.Changes()
	if err != nil || after != before || editor.ownedBytesLocked(nil) != ownedBefore {
		t.Fatal("group preview changed editing state or retained pixels", err)
	}
	full, err := editor.Composite(t.Context())
	if err != nil || full.NRGBAAt(2, 1) != (color.NRGBA{G: 255, A: 255}) {
		t.Fatal("group preview changed the full document or hidden ancestor", err)
	}
	if _, err := editor.CompositeGroup(t.Context(), child); !errors.Is(err, ErrInvalidEdit) {
		t.Fatal("raster was accepted as a group", err)
	}
	if _, err := editor.CompositeGroup(t.Context(), group,
		psdcompose.Options{MaxWorkingBytes: 1}); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatal("group preview ignored the working budget", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := editor.CompositeGroup(ctx, group); !errors.Is(err, context.Canceled) {
		t.Fatal("group preview ignored cancellation", err)
	}
	if err := editor.SetVisible(group, false); err != nil {
		t.Fatal(err)
	}
	pixels, err := editor.CompositeGroup(t.Context(), group)
	if err != nil || pixels.Rect != image.Rect(0, 0, 1, 1) || pixels.NRGBAAt(0, 0).A != 0 {
		t.Fatal("hidden group did not return a transparent pixel", err)
	}
	empty, err := editor.AddGroup(0, 2, "empty", true)
	if err != nil {
		t.Fatal(err)
	}
	pixels, err = editor.CompositeGroup(t.Context(), empty)
	if err != nil || pixels.Rect != image.Rect(0, 0, 1, 1) || pixels.NRGBAAt(0, 0).A != 0 {
		t.Fatal("empty group did not return a transparent pixel", err)
	}
}

func TestCompositeGroupPreservesOriginalGroupMask(t *testing.T) {
	editor := compositeEditor(t, compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{divider: 3},
		{color: []byte{255, 0, 0, 255}},
		{color: []byte{0, 0, 0, 0}, divider: 1, blend: "pass", mask: []byte{128}},
		{color: []byte{0, 255, 0, 255}},
	}))
	pixels, err := editor.CompositeGroup(t.Context(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if pixels.Rect != image.Rect(0, 0, 1, 1) || pixels.NRGBAAt(0, 0) != (color.NRGBA{R: 255, A: 128}) {
		t.Fatalf("original group mask or transparent isolation was lost: %v", pixels.NRGBAAt(0, 0))
	}
}
