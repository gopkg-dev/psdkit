package psdrendered

import (
	"image"
	"testing"
)

type testPlacement struct{ value byte }

func (placement *testPlacement) RasterFingerprint() [32]byte { return [32]byte{placement.value} }

func TestRasterIdentityRejectsReplacementsAndNonPointerValues(t *testing.T) {
	bounds := image.Rect(3, 4, 5, 7)
	pixels := image.NewNRGBA(bounds)
	placement := &testPlacement{value: 1}
	binding := New(pixels, bounds, placement)
	if !binding.Valid(pixels, bounds, placement) {
		t.Fatal("the scene-generated raster identity did not validate")
	}
	for _, value := range []any{nil, (*testPlacement)(nil), []int{1}, map[int]int{1: 2}, 1,
		&testPlacement{value: 1}} {
		if binding.Valid(pixels, bounds, value) {
			t.Fatalf("unrelated or nonpointer placement identity accepted: %T", value)
		}
	}
	if binding.Valid(image.NewNRGBA(bounds), bounds, placement) ||
		binding.Valid(pixels, bounds.Add(image.Pt(1, 0)), placement) {
		t.Fatal("replacement pixels or geometry reused a scene qualification")
	}
	if New(pixels, bounds, []int{1}).Valid(pixels, bounds, []int{1}) {
		t.Fatal("noncomparable identity should be rejected without panicking")
	}
	for _, mutate := range []func(){
		func() { pixels.Pix = append([]byte(nil), pixels.Pix...) },
		func() { pixels.Pix = pixels.Pix[:len(pixels.Pix)-1] },
		func() { pixels.Stride++ },
		func() { pixels.Rect = pixels.Rect.Add(image.Pt(1, 0)) },
		func() { placement.value++ },
	} {
		savedPixels, savedPlacement := *pixels, *placement
		mutate()
		if binding.Valid(pixels, bounds, placement) {
			t.Fatal("changing a source header or placement contents reused the raster identity")
		}
		*pixels, *placement = savedPixels, savedPlacement
	}
	var missing *Raster
	if missing.Valid(pixels, bounds, placement) {
		t.Fatal("missing rendered-raster identity validated")
	}
}
