package psdartboard

import (
	"bytes"
	"context"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func prepareFixture() (*psdmodel.Layer, *psdfile.ArtboardData) {
	child := &psdmodel.Layer{Name: "original child", Bounds: image.Rect(0, 0, 8, 6),
		Pixels: image.NewNRGBA(image.Rect(0, 0, 8, 6)), BlendMode: "norm"}
	mask := &psdmodel.Mask{Bounds: child.Bounds, Pixels: image.NewNRGBA(child.Bounds), Disabled: true,
		Unsupported: []string{"暂不支持矢量蒙版羽化"}}
	layer := &psdmodel.Layer{Group: true, BlendMode: "norm", Children: []*psdmodel.Layer{child},
		Mask: mask, VectorMask: &psdvector.Mask{Disabled: true}, VectorFeather: new(3.0),
		Effects: &psdeffects.Set{Enabled: false, EffectsVisible: true}, Unsupported: []string{Issue}}
	board := &psdfile.ArtboardData{Rect: &psdfile.ArtboardRect{Left: 2, Top: 1, Right: 6, Bottom: 5},
		BackgroundType: new(int32(4)), BackgroundRGB: &[3]float64{10.0 / 255, 80.0 / 255, 160.0 / 255}}
	return layer, board
}

func TestPrepareOwnsOnlyGeneratedStateAndHonorsExactBudget(t *testing.T) {
	layer, board := prepareFixture()
	mask, child, originalVector := layer.Mask, layer.Children[0], layer.VectorMask
	maskBytes := bytes.Clone(mask.Pixels.Pix)
	window := image.Rect(3, 2, 5, 4)
	prepared, cost, err := Prepare(t.Context(), layer, board, window, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if prepared == layer || prepared.Mask == mask || len(prepared.Children) != 2 ||
		prepared.Children[1] != child || prepared.Children[0].FillColor == nil ||
		prepared.Mask.SampleBounds == nil || *prepared.Mask.SampleBounds != window ||
		prepared.Mask.Bounds != image.Rect(2, 1, 6, 5) || len(prepared.Unsupported) != 0 {
		t.Fatal("prepared snapshot lost owned clipping or original child identity")
	}
	if *prepared.Children[0].FillColor != [3]float64{10.0 / 255, 80.0 / 255, 160.0 / 255} {
		t.Fatal("shared background quantization differs")
	}
	for index := 0; index < len(prepared.Mask.Pixels.Pix); index += 4 {
		if prepared.Mask.Pixels.Pix[index] != 255 {
			t.Fatal("generated rectangular coverage is not opaque")
		}
	}
	if layer.Mask != mask || layer.VectorMask != originalVector || len(layer.Children) != 1 ||
		layer.Children[0] != child || len(layer.Unsupported) != 1 || layer.Unsupported[0] != Issue ||
		!bytes.Equal(mask.Pixels.Pix, maskBytes) {
		t.Fatal("snapshot preparation modified borrowed layer metadata or samples")
	}
	prepared.Children[1] = nil
	prepared.Unsupported = append(prepared.Unsupported, "owned-only")
	if layer.Children[0] != child || layer.Unsupported[0] != Issue {
		t.Fatal("prepared container slices alias the input")
	}
	if _, exact, err := Prepare(t.Context(), layer, board, window, cost); err != nil || exact != cost {
		t.Fatalf("exact retained budget failed: cost=%d error=%v", exact, err)
	}
	for _, budget := range []int64{-1, 0, cost - 1} {
		result, retained, err := Prepare(t.Context(), layer, board, window, budget)
		if result != nil || retained != 0 || !errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("insufficient budget produced a partial snapshot: budget=%d cost=%d error=%v", budget, retained, err)
		}
	}
}

type cancelPreparation struct {
	context.Context
	checks int
}

func (ctx *cancelPreparation) Err() error {
	ctx.checks++
	if ctx.checks >= 3 {
		return context.Canceled
	}
	return ctx.Context.Err()
}

func TestPrepareCancellationAndHiddenBoardsKeepOriginalMetadata(t *testing.T) {
	layer, board := prepareFixture()
	ctx := &cancelPreparation{Context: t.Context()}
	result, cost, err := Prepare(ctx, layer, board, image.Rect(0, 0, 8, 6), 1<<20)
	if result != nil || cost != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-coverage cancellation returned a partial snapshot: %v", err)
	}
	if !layer.Mask.Disabled || layer.Mask.Pixels.Pix[0] != 0 || len(layer.Children) != 1 || layer.Unsupported[0] != Issue {
		t.Fatal("canceled preparation modified the original layer")
	}
	layer.Hidden = true
	layer.Unsupported = append(layer.Unsupported, "retained hidden limitation")
	prepared, hiddenCost, err := Prepare(t.Context(), layer, board, image.Rect(0, 0, 8, 6), 1<<20)
	if err != nil || !prepared.Hidden || prepared.Mask != layer.Mask || len(prepared.Children) != 1 ||
		len(prepared.Unsupported) != 1 || prepared.Unsupported[0] != "retained hidden limitation" {
		t.Fatalf("hidden board acquired visible clipping/background or lost unrelated metadata: %v", err)
	}
	_, actualCost, err := Prepare(t.Context(), layer, board, image.Rect(0, 0, 300000, 300000), hiddenCost)
	if err != nil || actualCost != hiddenCost {
		t.Fatalf("hidden board allocated window-dependent storage: %v", err)
	}
}

func TestPrepareUsesSharedQualificationBeforeAllocation(t *testing.T) {
	for _, change := range []func(*psdmodel.Layer, *psdfile.ArtboardData){
		func(layer *psdmodel.Layer, _ *psdfile.ArtboardData) { layer.Knockout = psdmodel.KnockoutShallow },
		func(layer *psdmodel.Layer, _ *psdfile.ArtboardData) { layer.ChannelRestrictions = []uint32{1} },
		func(layer *psdmodel.Layer, _ *psdfile.ArtboardData) { layer.Mask.Disabled = false },
		func(layer *psdmodel.Layer, _ *psdfile.ArtboardData) { layer.VectorMask.Disabled = false },
		func(layer *psdmodel.Layer, _ *psdfile.ArtboardData) { layer.Effects.Enabled = true },
		func(layer *psdmodel.Layer, _ *psdfile.ArtboardData) {
			layer.Unsupported = []string{Issue, "other source issue"}
		},
		func(_ *psdmodel.Layer, board *psdfile.ArtboardData) {
			board.Unsupported = []string{"unknown artboard field"}
		},
		func(_ *psdmodel.Layer, board *psdfile.ArtboardData) { board.Rect.Right = 2.5 },
	} {
		layer, board := prepareFixture()
		change(layer, board)
		result, cost, err := Prepare(t.Context(), layer, board, image.Rect(0, 0, 8, 6), 1<<20)
		if result != nil || cost != 0 || !errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatalf("shared preparation admitted unqualified artboard semantics: %v", err)
		}
	}
}
