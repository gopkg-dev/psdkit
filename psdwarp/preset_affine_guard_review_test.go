package psdwarp_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func TestPresetAffineGuardIndependent(t *testing.T) {
	projective := []float64{112, 128, 208, 128, 208, 230, 114, 230}
	affine := []float64{112, 128, 208, 128, 208, 230, 112, 230}
	rotated := []float64{123.45326626490188, 115.3644059824915,
		215.25852283735327, 143.4320896358742, 185.32132898655857, 241.3522385328132,
		93.51607241410721, 213.28455487943052}
	tests := []struct {
		name      string
		transform []float64
		nonAffine []float64
		want      error
	}{
		{"affineTransform", affine, nil, nil},
		{"rotatedTransform", rotated, nil, nil},
		{"projectiveTransform", projective, nil, psdwarp.ErrUnsupported},
		{"projectiveNonAffine", affine, projective, psdwarp.ErrUnsupported},
		{"affineNonAffinePrecedence", projective, affine, nil},
		{"ignoredMalformedTransform", []float64{1}, affine, nil},
		{"emptyNonAffineDoesNotFallback", affine, []float64{}, psdwarp.ErrInvalid},
		{"shortNonAffineDoesNotFallback", affine, []float64{1}, psdwarp.ErrInvalid},
		{"emptyTransform", []float64{}, nil, psdwarp.ErrInvalid},
		{"nilTransform", nil, nil, psdwarp.ErrInvalid},
		{"nonfiniteTransform", []float64{math.NaN(), 0, 1, 0, 1, 1, 0, 1}, nil, psdwarp.ErrInvalid},
		{"nonfiniteNonAffine", affine, []float64{0, 0, math.Inf(1), 0, 1, 1, 0, 1}, psdwarp.ErrInvalid},
		{"degenerateAffine", []float64{0, 0, 1, 0, 2, 0, 1, 0}, nil, psdwarp.ErrInvalid},
		{"bowTie", []float64{0, 0, 1, 1, 0, 1, 1, 0}, nil, psdwarp.ErrInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			placed := arcLowerFixture(96, 64, 68.8)
			placed.Transform = test.transform
			placed.NonAffineTransform = test.nonAffine
			geometry, err := psdwarp.CompilePresetGeometry(context.Background(), placed)
			if !errors.Is(err, test.want) || (geometry == nil) != (test.want != nil) {
				t.Fatalf("geometry=%v, err=%v, want=%v", geometry, err, test.want)
			}
		})
	}
}

func TestPresetAffineGuardScaleAndTranslation(t *testing.T) {
	for _, test := range []struct {
		name                string
		scale, offset, skew float64
		want                error
	}{
		{"smallAffine", 1e-4, 0, 0, nil},
		{"largeAffine", 100, 0, 0, nil},
		{"hugeAffineRetainsEdgeBudget", 1e5, 0, 0, psdwarp.ErrLimit},
		{"translatedAffine", 1, 1e9, 0, nil},
		{"smallProjective", 1e-4, 0, 1e-7, psdwarp.ErrUnsupported},
		{"largeProjective", 1e5, 0, 1, psdwarp.ErrUnsupported},
		{"translatedProjective", 1, 1e9, .01, psdwarp.ErrUnsupported},
		{"affineRoundoff", 1, 0, 1e-10, nil},
		{"materialSkew", 1, 0, 1e-5, psdwarp.ErrUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			placed := arcLowerFixture(96, 64, 68.8)
			for index := range placed.Transform {
				placed.Transform[index] = placed.Transform[index]*test.scale + test.offset
			}
			placed.Transform[6] += test.skew
			before, err := json.Marshal(placed, json.Deterministic(true))
			if err != nil {
				t.Fatal(err)
			}
			geometry, err := psdwarp.CompilePresetGeometry(nil, placed)
			if !errors.Is(err, test.want) || (geometry == nil) != (test.want != nil) {
				t.Fatalf("geometry=%v, err=%v, want=%v", geometry, err, test.want)
			}
			after, marshalErr := json.Marshal(placed, json.Deterministic(true))
			if marshalErr != nil || string(before) != string(after) {
				t.Fatalf("candidate mutated placement: %v", marshalErr)
			}
		})
	}
}

func TestPresetAffineGuardKeepsExplicitMathematicalProjectiveBehavior(t *testing.T) {
	placed := arcLowerFixture(96, 64, 68.8)
	placed.Transform[6] += 2
	custom := *placed
	warp := *placed.Warp
	warp.Style, warp.Value = "custom", 0
	warp.HasEnvelope, warp.UOrder, warp.VOrder = true, 4, 4
	warp.MeshPoints = make([]psdmodel.Point, 16)
	for row := range 4 {
		for column := range 4 {
			warp.MeshPoints[row*4+column] = psdmodel.Point{X: float64(column) * 32, Y: float64(row) * 64 / 3}
		}
	}
	custom.Warp = &warp
	for _, selectedNonAffine := range []bool{false, true} {
		if selectedNonAffine {
			custom.NonAffineTransform = custom.Transform
			custom.Transform = []float64{0, 0, 96, 0, 96, 64, 0, 64}
		}
		if patch, err := psdwarp.Compile(nil, &custom); patch != nil || !errors.Is(err, psdwarp.ErrUnsupported) {
			t.Fatalf("default custom projective geometry was admitted: %v", err)
		}
		patch, err := psdwarp.CompileMathematical(nil, &custom)
		if patch == nil || err != nil {
			t.Fatalf("generic custom projective geometry changed: %v", err)
		}
		// These values were captured from the immutable 707e production snapshot before this guard.
		for _, sample := range []struct {
			u, v, x, y float64
		}{
			{0, 0, 112, 128},
			{.125, .875, 125.53524804177546, 217.48302872062663},
			{.3, .7, 141.78616352201257, 199.84905660377356},
			{.5, .5, 160.50526315789472, 179.53684210526316},
			{.91, .09, 199.37651306009772, 137.35740072202165},
			{1, 1, 208, 230.00000000000003},
		} {
			point, err := patch.Forward(sample.u, sample.v)
			// Race instrumentation and compiler optimizations can change the last few float bits.
			ulpX := math.Abs(math.Nextafter(sample.x, math.Inf(1)) - sample.x)
			ulpY := math.Abs(math.Nextafter(sample.y, math.Inf(1)) - sample.y)
			if err != nil || math.Abs(point.X-sample.x) > 8*ulpX || math.Abs(point.Y-sample.y) > 8*ulpY {
				t.Fatalf("custom projective Forward changed: NonAffine=%t, point=%v, err=%v",
					selectedNonAffine, point, err)
			}
		}
	}
	geometry, err := psdwarp.CompilePresetGeometry(nil, placed)
	if geometry != nil || !errors.Is(err, psdwarp.ErrUnsupported) {
		t.Fatalf("unverified preset projective geometry was admitted: %v", err)
	}
}
