package psdwarp_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"strconv"
	"testing"

	"github.com/gopkg-dev/psdkit/psdwarp"
)

func presetRasterSource() *image.NRGBA {
	source := image.NewNRGBA(image.Rect(7, -5, 103, 59))
	for row := source.Rect.Min.Y; row < source.Rect.Max.Y; row++ {
		for column := source.Rect.Min.X; column < source.Rect.Max.X; column++ {
			source.SetNRGBA(column, row, color.NRGBA{
				R: byte(column*7 + row*3), G: byte(column*2 - row*5),
				B: byte(column*11 + row*9), A: byte(80 + (column-row+256)%176),
			})
		}
	}
	return source
}

func TestPresetMathematicalRasterMatchesIndependentNativeControlSurface(t *testing.T) {
	for _, test := range []struct {
		name                   string
		bend, handleX, handleY float64
	}{
		{"positive25", 25, 30.73388390139061, 12.73039122030451},
		{"realCorpus68.8", 68.8, 20.483424248189106, 38.39429249552329},
		{"negative68.8", -68.8, 20.483424248189106, -38.39429249552329},
	} {
		t.Run(test.name, func(t *testing.T) {
			placed := arcLowerFixture(96, 64, test.bend)
			geometry, err := psdwarp.CompilePresetGeometry(nil, placed)
			if err != nil {
				t.Fatal(err)
			}
			// Recorded native controls are independent of the preset-control implementation.
			// Both surfaces deliberately use the mathematical sampler, not native pixel evidence.
			reference := nativeArcLowerGeometry(t, placed, test.handleX, test.handleY)
			source := presetRasterSource()
			before := bytes.Clone(source.Pix)
			for _, canvas := range []image.Rectangle{{}, image.Rect(132, 145, 189, 219)} {
				expected, expectedOrigin, err := reference.Project(nil, source, canvas)
				if err != nil {
					t.Fatal(err)
				}
				actual, origin, err := geometry.ProjectMathematicalWithOptions(nil, source, canvas)
				if err != nil || origin != expectedOrigin || actual.Rect != expected.Rect ||
					!bytes.Equal(actual.Pix, expected.Pix) {
					t.Fatalf("explicit mathematical raster differs from independent controls: %v", err)
				}
				actual.Pix[0] ^= 255
				if !bytes.Equal(before, source.Pix) {
					t.Fatal("mathematical output changed caller-owned source pixels")
				}
			}
			placed.Warp.Value, placed.Transform[0] = -25, 999
			owned, ownedOrigin, err := geometry.ProjectMathematicalWithOptions(nil, source, image.Rectangle{})
			if err != nil {
				t.Fatal(err)
			}
			expected, expectedOrigin, err := reference.Project(nil, source, image.Rectangle{})
			if err != nil || ownedOrigin != expectedOrigin || !bytes.Equal(owned.Pix, expected.Pix) {
				t.Fatalf("compiled mathematical sampler aliases caller-owned geometry: %v", err)
			}
		})
	}
}

func TestPresetMathematicalRasterCurrentBudgetsAndErrors(t *testing.T) {
	geometry, err := psdwarp.CompilePresetGeometry(nil, arcLowerFixture(96, 64, 25))
	if err != nil {
		t.Fatal(err)
	}
	source := presetRasterSource()
	reference, referenceOrigin, err := geometry.ProjectMathematicalWithOptions(nil, source, image.Rectangle{})
	if err != nil {
		t.Fatal(err)
	}
	lower, upper := geometry.WorkingBytes(), int64(1<<20)
	for lower+1 < upper {
		middle := lower + (upper-lower)/2
		output, _, operationErr := geometry.ProjectMathematicalWithOptions(nil, source, image.Rectangle{},
			psdwarp.Options{MaxBytes: middle})
		if operationErr == nil {
			upper = middle
		} else if output == nil && errors.Is(operationErr, psdwarp.ErrLimit) {
			lower = middle
		} else {
			t.Fatalf("unexpected bounded mathematical sampling error: %v", operationErr)
		}
	}
	actual, origin, err := geometry.ProjectMathematicalWithOptions(nil, source, image.Rectangle{},
		psdwarp.Options{MaxBytes: upper})
	if err != nil || origin != referenceOrigin || !bytes.Equal(actual.Pix, reference.Pix) {
		t.Fatalf("sufficient current budget changed mathematical output: %v", err)
	}
	if output, _, err := geometry.ProjectMathematicalWithOptions(nil, source, image.Rectangle{},
		psdwarp.Options{MaxBytes: upper - 1}); output != nil || !errors.Is(err, psdwarp.ErrLimit) {
		t.Fatalf("minus-one current budget reused an earlier generous policy: %v", err)
	}
	for _, options := range []psdwarp.Options{
		{MaxBytes: -1}, {MaxBytes: 16}, {MaxPixels: 1}, {MaxIterations: 129},
	} {
		output, _, operationErr := geometry.ProjectMathematicalWithOptions(nil, source, image.Rectangle{}, options)
		if output != nil || !errors.Is(operationErr, psdwarp.ErrLimit) {
			t.Fatalf("invalid or insufficient mathematical sampling policy returned output: %v", operationErr)
		}
	}
	if output, _, err := geometry.ProjectMathematicalWithOptions(nil, source, image.Rectangle{},
		psdwarp.Options{}, psdwarp.Options{}); output != nil || !errors.Is(err, psdwarp.ErrInvalid) {
		t.Fatalf("multiple current policies were accepted: %v", err)
	}
	if output, _, err := geometry.ProjectMathematicalWithOptions(
		nil, nil, image.Rectangle{},
	); output != nil || !errors.Is(err, psdwarp.ErrInvalid) {
		t.Fatalf("nil source returned mathematical pixels: %v", err)
	}
	var missing *psdwarp.PresetGeometry
	if output, _, err := missing.ProjectMathematicalWithOptions(
		nil, source, image.Rectangle{},
	); output != nil || !errors.Is(err, psdwarp.ErrInvalid) {
		t.Fatalf("nil geometry returned mathematical pixels: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if output, _, err := geometry.ProjectMathematicalWithOptions(
		ctx, source, image.Rectangle{},
	); output != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mathematical sampler returned pixels: %v", err)
	}
	// A clipped-empty output must charge the preset wrapper as well as the patch and four output bytes.
	canvas := image.Rect(-200, -200, -190, -190)
	for _, sufficient := range []bool{false, true} {
		budget := geometry.WorkingBytes() + 3
		if sufficient {
			budget++
		}
		output, outputOrigin, operationErr := geometry.ProjectMathematicalWithOptions(nil, source, canvas,
			psdwarp.Options{MaxBytes: budget})
		if !sufficient {
			if output != nil || !errors.Is(operationErr, psdwarp.ErrLimit) {
				t.Fatalf("empty clipped result omitted retained preset wrapper bytes: %v", operationErr)
			}
			continue
		}
		if operationErr != nil || outputOrigin != canvas.Min || output.Rect != image.Rect(0, 0, 1, 1) {
			t.Fatalf("exact empty-result budget failed: %v", operationErr)
		}
	}
}

func TestPresetMathematicalRasterConcurrentPolicies(t *testing.T) {
	geometry, err := psdwarp.CompilePresetGeometry(nil, arcLowerFixture(96, 64, 25))
	if err != nil {
		t.Fatal(err)
	}
	source := presetRasterSource()
	reference, referenceOrigin, err := geometry.ProjectMathematicalWithOptions(nil, source, image.Rectangle{})
	if err != nil {
		t.Fatal(err)
	}
	for index := range 12 {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			t.Parallel()
			budget := int64(1 << 20)
			if index&1 != 0 {
				budget = geometry.WorkingBytes()
			}
			output, origin, operationErr := geometry.ProjectMathematicalWithOptions(nil, source, image.Rectangle{},
				psdwarp.Options{MaxBytes: budget})
			if index&1 != 0 {
				if output != nil || !errors.Is(operationErr, psdwarp.ErrLimit) {
					t.Fatalf("concurrent insufficient policy changed: %v", operationErr)
				}
				return
			}
			if operationErr != nil || origin != referenceOrigin || !bytes.Equal(output.Pix, reference.Pix) {
				t.Fatalf("concurrent sufficient policy changed: %v", operationErr)
			}
		})
	}
}
