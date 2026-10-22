package psdwarp_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"strconv"
	"testing"

	"github.com/gopkg-dev/psdkit/psdwarp"
)

func TestCompiledWarpCurrentProjectionBudgetAndOptionsOwnership(t *testing.T) {
	placed := warpFixture(4, 4, 10, 12)
	patch, err := psdwarp.Compile(nil, placed)
	if err != nil {
		t.Fatal(err)
	}
	source := nativeWarpSource().SubImage(image.Rect(0, 0, 4, 4)).(*image.NRGBA)
	before := bytes.Clone(source.Pix)
	reference, referenceOrigin, err := patch.Project(nil, source, image.Rectangle{})
	if err != nil {
		t.Fatal(err)
	}
	lower, upper := patch.WorkingBytes(), int64(1<<20)
	for lower+1 < upper {
		middle := lower + (upper-lower)/2
		result, _, projectErr := patch.ProjectWithOptions(nil, source, image.Rectangle{}, psdwarp.Options{MaxBytes: middle})
		if projectErr == nil {
			upper = middle
		} else if result == nil && errors.Is(projectErr, psdwarp.ErrLimit) {
			lower = middle
		} else {
			t.Fatalf("unexpected bounded projection error: %v", projectErr)
		}
	}
	actual, origin, err := patch.ProjectWithOptions(nil, source, image.Rectangle{}, psdwarp.Options{MaxBytes: upper})
	if err != nil || origin != referenceOrigin || !bytes.Equal(actual.Pix, reference.Pix) {
		t.Fatalf("current sufficient budget changed pixels: %v", err)
	}
	if output, _, err := patch.ProjectWithOptions(nil, source, image.Rectangle{},
		psdwarp.Options{MaxBytes: upper - 1}); output != nil || !errors.Is(err, psdwarp.ErrLimit) {
		t.Fatalf("current minus-one budget reused the old generous budget: %v", err)
	}
	actual.Pix[0] ^= 255
	if !bytes.Equal(before, source.Pix) {
		t.Fatal("owned output aliased caller source")
	}
	unchanged, unchangedOrigin, err := patch.Project(nil, source, image.Rectangle{})
	if err != nil || unchangedOrigin != referenceOrigin || !bytes.Equal(unchanged.Pix, reference.Pix) {
		t.Fatalf("operation options mutated the retained patch: %v", err)
	}
}

func TestCompiledWarpProjectionOptionsDefaultsErrorsAndCancellation(t *testing.T) {
	patch, err := psdwarp.Compile(nil, warpFixture(4, 4, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	source := nativeWarpSource()
	reference, origin, err := patch.Project(nil, source, image.Rectangle{})
	if err != nil {
		t.Fatal(err)
	}
	defaults, defaultOrigin, err := patch.ProjectWithOptions(nil, source, image.Rectangle{}, psdwarp.Options{})
	if err != nil || origin != defaultOrigin || !bytes.Equal(reference.Pix, defaults.Pix) {
		t.Fatalf("zero/default projection options changed output: %v", err)
	}
	for _, options := range []psdwarp.Options{{MaxBytes: -1}, {MaxPixels: 1}, {MaxIterations: 129}} {
		output, _, operationErr := patch.ProjectWithOptions(nil, source, image.Rectangle{}, options)
		if output != nil || !errors.Is(operationErr, psdwarp.ErrLimit) {
			t.Fatalf("invalid or insufficient operation options returned output: %v", operationErr)
		}
	}
	if output, _, err := patch.ProjectWithOptions(nil, source, image.Rectangle{}, psdwarp.Options{},
		psdwarp.Options{}); output != nil || !errors.Is(err, psdwarp.ErrInvalid) {
		t.Fatalf("multiple option values accepted: %v", err)
	}
	var absent *psdwarp.Patch
	missingOutput, _, missingErr := absent.ProjectWithOptions(nil, source, image.Rectangle{})
	if missingOutput != nil || !errors.Is(missingErr, psdwarp.ErrInvalid) {
		t.Fatalf("nil compiled patch accepted: %v", missingErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceledOutput, _, canceledErr := patch.ProjectWithOptions(ctx, source, image.Rectangle{})
	if canceledOutput != nil || !errors.Is(canceledErr, context.Canceled) {
		t.Fatalf("canceled operation returned output: %v", canceledErr)
	}
}

func TestCompiledWarpConcurrentProjectionPolicies(t *testing.T) {
	patch, err := psdwarp.Compile(nil, warpFixture(4, 4, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	source := nativeWarpSource()
	reference, origin, err := patch.Project(nil, source, image.Rectangle{})
	if err != nil {
		t.Fatal(err)
	}
	for index := range 16 {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			t.Parallel()
			budget := int64(1 << 20)
			if index&1 != 0 {
				budget = patch.WorkingBytes()
			}
			output, outputOrigin, operationErr := patch.ProjectWithOptions(nil, source, image.Rectangle{},
				psdwarp.Options{MaxBytes: budget})
			if index&1 != 0 {
				if output != nil || !errors.Is(operationErr, psdwarp.ErrLimit) {
					t.Fatalf("concurrent small budget changed: %v", operationErr)
				}
				return
			}
			if operationErr != nil || outputOrigin != origin || !bytes.Equal(output.Pix, reference.Pix) {
				t.Fatalf("concurrent sufficient budget changed: %v", operationErr)
			}
		})
	}
}
