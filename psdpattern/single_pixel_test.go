package psdpattern

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"testing"
)

func TestSinglePixelPatternUsesExactRGBAForAllFiniteControls(t *testing.T) {
	for _, alpha := range []byte{0, 1, 128, 255} {
		tile := &image.NRGBA{Rect: image.Rect(7, -9, 8, -8), Stride: 40, Pix: []byte{31, 67, 193, alpha}}
		original := bytes.Clone(tile.Pix)
		for _, fill := range []Fill{
			{Scale: 75, Angle: 37, Phase: Phase{X: .5, Y: -.25}},
			{Scale: math.SmallestNonzeroFloat64, Angle: math.MaxFloat64,
				Phase: Phase{X: -math.MaxFloat64, Y: math.MaxFloat64}},
			{Scale: math.MaxFloat64, Angle: -math.MaxFloat64, Phase: Phase{X: .1, Y: -.1}},
		} {
			bounds, crop := image.Rect(-7, -5, 9, 8), image.Rect(-2, 1, 1, 3)
			if err := Validate(tile, fill); err != nil {
				t.Fatal(err)
			}
			pixels, err := Render(nil, tile, fill, bounds, Options{Crop: &crop, MaxPixels: 6, MaxWorkingBytes: 24})
			if err != nil || pixels.Rect != crop || !bytes.Equal(pixels.Pix, bytes.Repeat(original, 6)) {
				t.Fatalf("single-pixel phase/rotation/scale changed RGBA or crop: %v", err)
			}
			clear(pixels.Pix)
			if !bytes.Equal(tile.Pix, original) {
				t.Fatal("single-pixel rendering borrowed or changed source samples")
			}
		}
	}
}

func TestSinglePixelPatternKeepsValidationBudgetsAndCancellation(t *testing.T) {
	tile := &image.NRGBA{Rect: image.Rect(0, 0, 1, 1), Stride: 4, Pix: []byte{21, 43, 87, 0}}
	fill := Fill{Scale: 75, Angle: 17, Phase: Phase{X: .5}}
	for _, change := range []func(*Fill){
		func(value *Fill) { value.Scale = 0 },
		func(value *Fill) { value.Scale = -1 },
		func(value *Fill) { value.Scale = math.NaN() },
		func(value *Fill) { value.Angle = math.Inf(1) },
		func(value *Fill) { value.Phase.X = math.NaN() },
		func(value *Fill) { value.Phase.Y = math.Inf(-1) },
	} {
		invalid := fill
		change(&invalid)
		if _, err := Render(nil, tile, invalid, image.Rect(0, 0, 2, 2)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("single-pixel tile concealed invalid controls: %v", err)
		}
	}
	unknown := fill
	unknown.Unsupported = []string{"future"}
	if _, err := Render(nil, tile, unknown, image.Rect(0, 0, 1, 1)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("single-pixel tile concealed unknown semantics: %v", err)
	}
	for _, broken := range []*image.NRGBA{nil,
		{Rect: tile.Rect, Stride: 3, Pix: tile.Pix}, {Rect: tile.Rect, Stride: 4, Pix: tile.Pix[:3]}} {
		if _, err := Render(nil, broken, fill, image.Rect(0, 0, 1, 1)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("single-pixel tile bypassed layout validation: %v", err)
		}
	}
	for _, options := range []Options{{MaxPixels: 3}, {MaxWorkingBytes: 15}} {
		output, err := Render(nil, tile, fill, image.Rect(0, 0, 2, 2), options)
		if output != nil || !errors.Is(err, ErrLimit) {
			t.Fatalf("single-pixel rendering bypassed output budgets: %v", err)
		}
	}
	invalidCrop := image.Rectangle{Min: image.Pt(2, 2), Max: image.Pt(1, 1)}
	_, err := Render(nil, tile, fill, image.Rect(0, 0, 2, 2), Options{Crop: &invalidCrop})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("single-pixel rendering concealed invalid crop: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if output, err := Render(ctx, tile, fill, image.Rect(0, 0, 2, 2)); output != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("single-pixel rendering ignored cancellation: %v", err)
	}
	midway := &singlePixelCancelContext{Context: context.Background()}
	output, err := Render(midway, tile, fill, image.Rect(0, 0, 3000, 2))
	if output != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("single-pixel rendering returned partial mid-row output: %v", err)
	}
}

type singlePixelCancelContext struct {
	context.Context
	calls int
}

func (c *singlePixelCancelContext) Err() error {
	c.calls++
	if c.calls >= 4 {
		return context.Canceled
	}
	return nil
}

func TestMultiPixelPatternDoesNotInheritSinglePixelSampling(t *testing.T) {
	for _, bounds := range []image.Rectangle{image.Rect(0, 0, 1, 2), image.Rect(0, 0, 2, 1)} {
		tile := image.NewNRGBA(bounds)
		copy(tile.Pix, bytes.Repeat([]byte{31, 67, 193, 128}, 2))
		for _, fill := range []Fill{{Scale: 75}, {Scale: 100, Angle: 37},
			{Scale: 100, Phase: Phase{X: .5}}, {Scale: 100, Phase: Phase{X: 1<<40 + 1}}} {
			if output, err := Render(nil, tile, fill, image.Rect(0, 0, 1, 1)); output != nil || err == nil {
				t.Fatalf("multiple identical pixels incorrectly enabled single-pixel sampling: %v", err)
			}
		}
	}
}
