package psdwarp_test

import (
	"context"
	"errors"
	"image"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func TestCustomProjectiveDefaultQualificationAndExplicitMath(t *testing.T) {
	affine := []float64{2, 3, 13, 3, 13, 19, 2, 19}
	projective := []float64{2, 3, 13, 4, 12, 19, 1, 16}
	for _, test := range []struct {
		name      string
		transform []float64
		nonAffine []float64
		want      error
	}{
		{"affine", affine, nil, nil},
		{"projectiveTransform", projective, nil, psdwarp.ErrUnsupported},
		{"projectiveNonAffine", affine, projective, psdwarp.ErrUnsupported},
		{"selectedAffineNonAffine", projective, affine, nil},
		{"selectedAffineOverridesBadTransform", []float64{1}, affine, nil},
		{"emptyNonAffineDoesNotFallback", affine, []float64{}, psdwarp.ErrInvalid},
		{"emptyTransform", []float64{}, nil, psdwarp.ErrInvalid},
		{"nonfiniteTransform", []float64{math.NaN(), 0, 1, 0, 1, 1, 0, 1}, nil, psdwarp.ErrInvalid},
		{"degenerateTransform", []float64{0, 0, 1, 0, 2, 0, 1, 0}, nil, psdwarp.ErrInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			placed := warpFixture(4, 4, 0, 0)
			placed.Transform, placed.NonAffineTransform = test.transform, test.nonAffine
			layer := &psdmodel.Layer{Placed: placed}
			patch, err := psdwarp.Compile(nil, placed)
			if !errors.Is(err, test.want) || (patch == nil) != (test.want != nil) {
				t.Fatalf("default Compile: patch=%v, error=%v, wanted=%v", patch, err, test.want)
			}
			wrapped, err := psdrender.CompileWarp(nil, layer)
			if !errors.Is(err, test.want) || (wrapped == nil) != (test.want != nil) {
				t.Fatalf("default wrapper: patch=%v, error=%v, wanted=%v", wrapped, err, test.want)
			}
			if test.want != nil {
				pixels, _, err := psdrender.ProjectWarp(nil, layer,
					image.NewNRGBA(image.Rect(0, 0, 4, 4)), image.Rect(0, 0, 24, 24))
				if pixels != nil || !errors.Is(err, test.want) {
					t.Fatalf("default projection admitted unsupported geometry: %v", err)
				}
			}
			mathPatch, mathErr := psdwarp.CompileMathematical(nil, placed)
			wantedMath := test.want
			if errors.Is(wantedMath, psdwarp.ErrUnsupported) {
				wantedMath = nil
			}
			if !errors.Is(mathErr, wantedMath) || (mathPatch == nil) != (wantedMath != nil) {
				t.Fatalf("explicit math: patch=%v, error=%v, wanted=%v", mathPatch, mathErr, wantedMath)
			}
		})
	}
}

func TestCustomProjectiveGatePreservesAffineScaleTranslationAndReflection(t *testing.T) {
	for _, test := range []struct {
		name               string
		quad               []float64
		ordinaryProjective bool
		projectiveWarp     bool
	}{
		{"translated", []float64{1e9, 1e9, 1e9 + 4, 1e9, 1e9 + 4, 1e9 + 4, 1e9, 1e9 + 4}, false, false},
		{"tiny", []float64{0, 0, .0004, 0, .0004, .0004, 0, .0004}, false, false},
		{"large", []float64{0, 0, 4e6, 0, 4e6, 4e6, 0, 4e6}, false, false},
		{"reflected", []float64{4, 0, 0, 0, 0, 4, 4, 4}, false, false},
		{"sheared", []float64{0, 0, 4, 1, 6, 5, 2, 4}, false, false},
		{"roundoff", []float64{0, 0, 4, 0, 4, 4, 1e-10, 4}, false, false},
		{"translatedProjective", []float64{1e9, 1e9, 1e9 + 4, 1e9, 1e9 + 4, 1e9 + 4, 1e9 + .01, 1e9 + 4}, false, true},
		{"ordinaryProjectiveWithoutWarp", []float64{2, 3, 13, 4, 12, 19, 1, 16}, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			placed := warpFixture(4, 4, 0, 0)
			placed.Transform = test.quad
			if test.ordinaryProjective {
				placed.Warp = nil
				plan, err := psdrender.CompileProjection(&psdmodel.Layer{Placed: placed}, 24, 24, false)
				if err != nil || plan == nil || plan.Inverse == nil {
					t.Fatalf("ordinary perspective route was rejected: %v", err)
				}
				return
			}
			patch, err := psdwarp.Compile(nil, placed)
			if test.projectiveWarp {
				if patch != nil || !errors.Is(err, psdwarp.ErrUnsupported) {
					t.Fatalf("translation hid projective placement: %v", err)
				}
				return
			}
			if err != nil || patch == nil {
				t.Fatalf("affine geometry was rejected: %v", err)
			}
		})
	}
}

func TestCustomProjectiveGatePreservesCancellationAndBudgetErrors(t *testing.T) {
	placed := warpFixture(4, 4, 0, 0)
	placed.Transform = []float64{2, 3, 13, 4, 12, 19, 1, 16}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, compile := range []func(context.Context, *psdmodel.Placed, ...psdwarp.Options) (*psdwarp.Patch, error){
		psdwarp.Compile, psdwarp.CompileMathematical,
	} {
		if patch, err := compile(ctx, placed); patch != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost its causal error: %v", err)
		}
		if patch, err := compile(nil, placed, psdwarp.Options{MaxBytes: -1}); patch != nil || !errors.Is(err, psdwarp.ErrLimit) {
			t.Fatalf("invalid budget lost its error: %v", err)
		}
	}
}
