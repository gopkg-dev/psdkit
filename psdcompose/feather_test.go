package psdcompose

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func featherTestDocument(radius float64) (*psdmodel.Document, *psdmodel.Mask) {
	mask := &psdmodel.Mask{
		Bounds: image.Rect(3, 2, 8, 7), DefaultColor: new(uint8(0)), Feather: new(radius),
		Pixels: composeTestLayer(image.Rect(0, 0, 5, 5), color.NRGBA{R: 255, A: 255}).Pixels,
	}
	layer := composeTestLayer(image.Rect(0, 0, 11, 9), color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	layer.Mask = mask
	return &psdmodel.Document{Width: 11, Height: 9, Layers: []*psdmodel.Layer{layer}}, mask
}

func TestFeatherGaussianKernelSymmetryAndNormalization(t *testing.T) {
	for _, sigma := range []float64{1e-8, .5, 1, 2, 32} {
		radius := int(math.Ceil(4 * sigma))
		kernel := featherKernel(sigma, radius)
		total := float64(0)
		for index, weight := range kernel {
			total += weight
			if weight < 0 || weight != kernel[len(kernel)-index-1] || math.IsNaN(weight) {
				t.Fatalf("invalid Gaussian kernel sigma %g: %v", sigma, kernel)
			}
		}
		if math.Abs(total-1) > 1e-12 {
			t.Fatalf("Gaussian kernel mass sigma %g = %g", sigma, total)
		}
	}
}

func TestFeatherWidthCalibrationContinuityAndMonotonicity(t *testing.T) {
	for _, boundary := range []float64{.25, .5, .75} {
		left, right := featherSigma(boundary-1e-8), featherSigma(boundary+1e-8)
		if math.Abs(left-right) > 3e-8 {
			t.Fatalf("feather calibration discontinuity at %g: %g vs %g", boundary, left, right)
		}
	}
	previous := float64(0)
	for step := 1; step <= 10000; step++ {
		value := featherSigma(float64(step) / 100)
		if value <= previous {
			t.Fatalf("increasing feather radius narrowed its kernel at step %d", step)
		}
		previous = value
	}
}

func TestFeatherSeparableMaskAndBorrowedPixels(t *testing.T) {
	document, mask := featherTestDocument(1)
	before := bytes.Clone(mask.Pixels.Pix)
	result, err := Render(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	if result.NRGBAAt(5, 4).A <= result.NRGBAAt(3, 2).A || result.NRGBAAt(2, 2).A == 0 {
		t.Fatalf("mask was not feathered into the default background: center=%v corner=%v outside=%v",
			result.NRGBAAt(5, 4), result.NRGBAAt(3, 2), result.NRGBAAt(2, 2))
	}
	for y := range document.Height {
		for x := range document.Width {
			if result.NRGBAAt(x, y).A != result.NRGBAAt(document.Width-x-1, y).A ||
				result.NRGBAAt(x, y).A != result.NRGBAAt(x, document.Height-y-1).A {
				t.Fatalf("symmetric binary mask produced asymmetric feather at %d,%d", x, y)
			}
		}
	}
	if !bytes.Equal(mask.Pixels.Pix, before) || *mask.Feather != 1 {
		t.Fatal("feathering modified borrowed pixels or metadata")
	}
}

func TestFeatherDefaultInversionAndDensityOrder(t *testing.T) {
	document, mask := featherTestDocument(1)
	original, err := Render(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	mask.Invert = true
	mask.Density = new(uint8(128))
	adjusted, err := Render(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	for y := range document.Height {
		for x := range document.Width {
			value := original.NRGBAAt(x, y).A
			expected := 255 - int(math.Round(float64(value)*128/255))
			if difference := math.Abs(float64(int(adjusted.NRGBAAt(x, y).A) - expected)); difference > 1 {
				t.Fatalf("invert/density must follow feather at %d,%d: got %d, expected %d",
					x, y, adjusted.NRGBAAt(x, y).A, expected)
			}
		}
	}
	mask.Invert, mask.Density = false, nil
	mask.DefaultColor = new(uint8(255))
	for index := range mask.Pixels.Pix {
		if index%4 == 0 {
			mask.Pixels.Pix[index] = 0
		}
	}
	inverse, err := Render(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	for y := range document.Height {
		for x := range document.Width {
			if int(original.NRGBAAt(x, y).A)+int(inverse.NRGBAAt(x, y).A) != 255 {
				t.Fatalf("outside-mask white defaults must survive Gaussian sampling at %d,%d", x, y)
			}
		}
	}
}

func TestFeatherGroupsAndSharedMaskCache(t *testing.T) {
	document, mask := featherTestDocument(.5)
	child := document.Layers[0]
	child.Mask = nil
	group := &psdmodel.Layer{Group: true, BlendMode: "norm", Children: []*psdmodel.Layer{child}, Mask: mask}
	document.Layers = []*psdmodel.Layer{group}
	isolated, err := Render(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	group.BlendMode = "pass"
	pass, err := Render(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(isolated.Pix, pass.Pix) {
		t.Fatal("group mask feather differs between isolated and pass groups without a backdrop")
	}
	group.Children[0].Mask = mask
	plan, err := preparePlan(document, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.feathers) != 1 {
		t.Fatalf("shared immutable masks should use one cache, got %d", len(plan.feathers))
	}
}

func TestFeatherExtendsCanvasEdgesWithoutDiscardingExternalMaskPixels(t *testing.T) {
	layer := composeTestLayer(image.Rect(0, 0, 5, 5), color.NRGBA{R: 255, A: 255})
	mask := &psdmodel.Mask{
		Bounds: image.Rect(0, 0, 5, 5), Pixels: composeTestLayer(
			image.Rect(0, 0, 5, 5), color.NRGBA{R: 255, A: 255},
		).Pixels,
		DefaultColor: new(uint8(0)), Feather: new(float64(1)),
	}
	layer.Mask = mask
	document := &psdmodel.Document{Width: 5, Height: 5, Layers: []*psdmodel.Layer{layer}}
	result, err := Render(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	if actual := result.NRGBAAt(0, 0).A; actual != 255 {
		t.Fatalf("document edges should extend their existing mask coverage: %d", actual)
	}
	mask.Bounds = image.Rect(-1, -1, 6, 6)
	mask.Pixels = composeTestLayer(image.Rect(0, 0, 7, 7), color.NRGBA{A: 255}).Pixels
	for y := 1; y < 6; y++ {
		for x := 1; x < 6; x++ {
			mask.Pixels.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	result, err = Render(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	if actual := result.NRGBAAt(0, 0).A; actual == 255 {
		t.Fatal("real mask pixels outside the document must still influence feathering")
	}
}

func TestFeatherWorkingAndRadiusBudgets(t *testing.T) {
	document, mask := featherTestDocument(2)
	if err := Validate(document, Options{MaxWorkingBytes: 2500}); !errors.Is(err, ErrLimit) {
		t.Fatalf("feather working caches exceeded the configured budget: %v", err)
	}
	if err := Validate(document, Options{MaxFeatherRadius: 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("feather radius exceeded its configured budget: %v", err)
	}
	mask.Disabled = true
	if err := Validate(document, Options{MaxWorkingBytes: 1500, MaxFeatherRadius: 1}); err != nil {
		t.Fatalf("disabled feather masks should use no cache budget: %v", err)
	}
	mask.Disabled = false
	mask.Density = new(uint8(0))
	if err := Validate(document, Options{MaxWorkingBytes: 1500, MaxFeatherRadius: 1}); err != nil {
		t.Fatalf("zero-density masks should use no feather cache budget: %v", err)
	}
	mask.Density = nil
	for _, value := range []float64{math.NaN(), math.Inf(1), -1} {
		if err := Validate(document, Options{MaxFeatherRadius: value}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid radius option %v: %v", value, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan, err := preparePlan(document, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.prepareFeathers(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("feather preparation ignored cancellation: %v", err)
	}
}

func TestFeatherFarAndOverflowingMaskBounds(t *testing.T) {
	canvas := image.Rect(0, 0, 11, 9)
	for _, bounds := range []image.Rectangle{
		image.Rect(10000, 10000, 10005, 10005),
		image.Rect(-10000, -10000, -9995, -9995),
		{Min: image.Pt(-int(^uint(0)>>1), -int(^uint(0)>>1)), Max: image.Pt(-1000, -1000)},
	} {
		if expanded := featherBounds(bounds, canvas, 8); !expanded.Empty() {
			t.Fatalf("far-off mask unexpectedly needed feather cache: %v", expanded)
		}
	}
	if actual := featherBounds(image.Rect(12, 0, 13, 2), canvas, 8); actual != image.Rect(3, 0, 11, 9) {
		t.Fatalf("near off-canvas mask cannot influence the canvas: %v", actual)
	}
}

func TestFeatherAndDissolveConcurrentDocuments(t *testing.T) {
	document, mask := featherTestDocument(1)
	document.Layers[0].BlendMode = "diss"
	seed := uint64(42)
	options := Options{DissolveSeed: &seed}
	expected, err := Render(context.Background(), document, options)
	if err != nil {
		t.Fatal(err)
	}
	maskBefore, pixelsBefore := bytes.Clone(mask.Pixels.Pix), bytes.Clone(document.Layers[0].Pixels.Pix)
	t.Cleanup(func() {
		if !bytes.Equal(maskBefore, mask.Pixels.Pix) || !bytes.Equal(pixelsBefore, document.Layers[0].Pixels.Pix) {
			t.Error("concurrent renders modified their shared decoded document")
		}
	})
	for range 8 {
		t.Run("parallel", func(t *testing.T) {
			t.Parallel()
			result, err := Render(context.Background(), document, options)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(result.Pix, expected.Pix) {
				t.Fatal("concurrent feather/Dissolve rendering changed the deterministic result")
			}
			result.Pix[0] ^= 255
		})
	}
}
