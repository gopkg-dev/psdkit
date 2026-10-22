package psdeffects

import (
	"errors"
	"image"
	"testing"
)

func TestEstimateCoverageScratchAndRasterBudgetsMatch(t *testing.T) {
	shadow := primitive(DropShadow)
	shadow.Distance = 3
	set := primitiveSet(shadow)
	bounds := image.Rect(10, 7, 22, 17)
	estimate, err := EstimateWorking(bounds, set)
	if err != nil || estimate.OutputBytes != 632 || estimate.ScratchBytes != 968 || estimate.Planes != 1 ||
		estimate.SampleWork != 480 {
		t.Fatalf("hard-shadow storage/work should be computed without allocating coverage: %+v, %v", estimate, err)
	}
	limit := estimate.OutputBytes + estimate.ScratchBytes
	if _, err := Rasterize(nil, rectangleAlpha(bounds, 255), set, Options{MaxWorkingBytes: limit}); err != nil {
		t.Fatalf("estimated peak should suffice for real rasterization: %v", err)
	}
	_, err = Rasterize(nil, rectangleAlpha(bounds, 255), set, Options{MaxWorkingBytes: limit - 1})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("a lower limit should reject the same peak accounted by estimation: %v", err)
	}
}

func TestEstimateAccountsEveryRetainedPlaneAndLargestScratch(t *testing.T) {
	shadow := primitive(DropShadow)
	set := primitiveSet(shadow, shadow)
	estimate, err := EstimateWorking(image.Rect(0, 0, 1, 1), set)
	if err != nil || estimate.OutputBytes != 1026 || estimate.ScratchBytes != 16 || estimate.Planes != 2 {
		t.Fatalf("multiple planes should reserve outputs plus the largest temporary peak: %+v %v", estimate, err)
	}
	if _, err := EstimateWorking(image.Rect(0, 0, 1, 1), set, Options{MaxWorkingBytes: 1041}); !errors.Is(err, ErrLimit) {
		t.Fatalf("estimate ignored retained output from another effect: %v", err)
	}
}
