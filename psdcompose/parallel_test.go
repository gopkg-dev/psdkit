package psdcompose

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"runtime"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func TestParallelCompositionMatchesSerialPixelsAndRegions(t *testing.T) {
	previous := runtime.GOMAXPROCS(4)
	t.Cleanup(func() { runtime.GOMAXPROCS(previous) })
	bounds := image.Rect(0, 0, 321, 257)
	background := composeTestLayer(bounds, color.NRGBA{R: 31, G: 81, B: 149, A: 173})
	child := composeTestLayer(bounds, color.NRGBA{R: 211, G: 55, B: 103, A: 197})
	child.BlendMode = "diss"
	group := &psdmodel.Layer{Group: true, BlendMode: "pass", Opacity: new(.7),
		VectorMask: vectorFeatherTestMask(image.Rect(17, 23, 309, 249)),
		Children:   []*psdmodel.Layer{child}}
	group.VectorMask.Width, group.VectorMask.Height = bounds.Dx(), bounds.Dy()
	document := &psdmodel.Document{Width: bounds.Dx(), Height: bounds.Dy(),
		Layers: []*psdmodel.Layer{background, group}}
	for _, window := range []image.Rectangle{bounds, image.Rect(5, 7, 320, 256)} {
		serial := Options{Workers: 1, DissolveSeed: new(uint64(109))}
		parallel := serial
		parallel.Workers = 4
		want, err := RenderRegion(t.Context(), document, window, serial)
		if err != nil {
			t.Fatal(err)
		}
		got, err := RenderRegion(t.Context(), document, window, parallel)
		if err != nil {
			t.Fatal(err)
		}
		if got.Rect != want.Rect || !bytes.Equal(got.Pix, want.Pix) {
			t.Fatal("parallel row blocks changed pixel values or absolute coordinates")
		}
	}
}

func TestParallelCompositionRespectsBudgetsAndWorkerValidation(t *testing.T) {
	previous := runtime.GOMAXPROCS(4)
	t.Cleanup(func() { runtime.GOMAXPROCS(previous) })
	bounds := image.Rect(0, 0, 256, 256)
	document := &psdmodel.Document{Width: 256, Height: 256, Layers: []*psdmodel.Layer{
		composeTestLayer(bounds, color.NRGBA{R: 61, G: 129, A: 255}),
	}}
	plan, err := preparePlan(document, []Options{{Workers: 1}})
	if err != nil {
		t.Fatal(err)
	}
	// An exact serial budget leaves no space for extra worker recursion storage.
	for _, workers := range []int{1, 4} {
		if _, err := Render(t.Context(), document, Options{
			Workers: workers, MaxWorkingBytes: plan.workingBytes,
		}); err != nil {
			t.Fatal("bounded worker fallback rejected a valid serial budget:", err)
		}
	}
	for _, workers := range []int{-1, 65} {
		if _, err := Render(t.Context(), document, Options{Workers: workers}); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid worker count was accepted:", workers, err)
		}
	}
}
