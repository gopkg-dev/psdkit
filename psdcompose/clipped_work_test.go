package psdcompose

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func clippedWorkNestedDocument(depth int) *psdmodel.Document {
	bounds := image.Rect(0, 0, 1, 1)
	current := composeTestLayer(bounds, color.NRGBA{R: 213, G: 54, B: 128, A: 173})
	current.BlendMode = "mul "
	for range depth {
		member := composeTestLayer(bounds, color.NRGBA{R: 80, G: 170, B: 30, A: 128})
		member.Hidden, member.Clipping, member.BlendMode = true, true, "scrn"
		current = &psdmodel.Layer{Group: true, BlendMode: "pass", Opacity: new(.5),
			Children: []*psdmodel.Layer{current, member}}
	}
	backdrop := composeTestLayer(bounds, color.NRGBA{R: 37, G: 119, B: 203, A: 96})
	member := composeTestLayer(bounds, color.NRGBA{R: 80, G: 170, B: 30, A: 128})
	member.Hidden, member.Clipping, member.BlendMode = true, true, "scrn"
	return &psdmodel.Document{Width: 1, Height: 1, Layers: []*psdmodel.Layer{backdrop, current, member}}
}

func TestClippedWorkExactBoundaryAndRegionPolicy(t *testing.T) {
	document := backdropApproximationDocument(image.Rect(0, 0, 8, 4))
	var previous *image.NRGBA
	for _, limit := range []int64{3, 4, 5} {
		option := Options{ApproximateClippedGroupBackdrop: true, MaxClippedGroupEvaluations: limit}
		full, err := Render(context.Background(), document, option)
		if limit == 3 {
			if full != nil || !errors.Is(err, ErrLimit) {
				t.Fatalf("budget3 did not fail closed: %v, %v", full, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if previous != nil && !bytes.Equal(previous.Pix, full.Pix) {
			t.Fatal("budget+1 changed image")
		}
		previous = full
		bounds := image.Rect(2, 1, 7, 4)
		region, err := RenderRegion(context.Background(), document, bounds, option)
		if err != nil {
			t.Fatal(err)
		}
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				if full.NRGBAAt(x, y) != region.NRGBAAt(x, y) {
					t.Fatalf("per-pixel budget changed ROI at%d,%d", x, y)
				}
			}
		}
	}
	legacy, err := Render(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	limitedOff, err := Render(context.Background(), document, Options{MaxClippedGroupEvaluations: 1})
	if err != nil || !bytes.Equal(legacy.Pix, limitedOff.Pix) {
		t.Fatalf("unused approximate work limit changed legacy: %v", err)
	}
	if err := Validate(document, Options{MaxClippedGroupEvaluations: -1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative evaluation budget accepted: %v", err)
	}
}

func TestClippedWorkNestedDefaultFailsClosed(t *testing.T) {
	for _, depth := range []int{12, 16, 22, 32} {
		document := clippedWorkNestedDocument(depth)
		option := Options{ApproximateClippedGroupBackdrop: true, MaxDepth: 64}
		if err := Validate(document, option); err != nil {
			t.Fatalf("depth%d structure unexpectedly invalid: %v", depth, err)
		}
		start := time.Now()
		partial, err := Render(context.Background(), document, option)
		elapsed := time.Since(start)
		if partial != nil || !errors.Is(err, ErrLimit) {
			t.Fatalf("depth%d did not stop at work budget: %v, %v", depth, partial, err)
		}
		t.Logf("depth%d default4096 returned ErrLimit in%s", depth, elapsed)
		legacy, err := Render(context.Background(), document, Options{MaxDepth: 64})
		if err != nil || legacy == nil {
			t.Fatalf("depth%d legacy rendering changed: %v", depth, err)
		}
	}
}

type clippedCancelContext struct {
	context.Context
	cancel context.CancelFunc
	calls  int
}

func (ctx *clippedCancelContext) Err() error {
	ctx.calls++
	if ctx.calls == 64 {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestClippedWorkMidPixelCancellation(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &clippedCancelContext{Context: base, cancel: cancel}
	document := clippedWorkNestedDocument(22)
	partial, err := Render(ctx, document, Options{
		ApproximateClippedGroupBackdrop: true, MaxClippedGroupEvaluations: 1 << 40,
	})
	if partial != nil || !errors.Is(err, context.Canceled) || ctx.calls > 70 {
		t.Fatalf("cancellation did not unwind promptly: pixels=%v err=%v checks=%d", partial, err, ctx.calls)
	}
	deadline, stop := context.WithTimeout(context.Background(), 2*time.Millisecond)
	defer stop()
	start := time.Now()
	partial, err = Render(deadline, document, Options{
		ApproximateClippedGroupBackdrop: true, MaxClippedGroupEvaluations: 1 << 40,
	})
	if partial != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("short deadline returned pixels=%v err=%v", partial, err)
	}
	t.Logf("depth22 highbudget2ms deadline unwound in%s", time.Since(start))
}
