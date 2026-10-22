package psdfilter

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"
)

func gaussianStack(radii ...float64) Stack {
	stack := Stack{Enabled: true}
	for _, radius := range radii {
		stack.Filters = append(stack.Filters, Filter{Kind: GaussianBlur, Enabled: true,
			Radius: radius, Opacity: 1, BlendMode: "norm"})
	}
	return stack
}

func TestGaussianOwnedStridedNonzeroOriginAndBudget(t *testing.T) {
	container := image.NewNRGBA(image.Rect(-7, 3, 14, 25))
	for y := container.Rect.Min.Y; y < container.Rect.Max.Y; y++ {
		for x := container.Rect.Min.X; x < container.Rect.Max.X; x++ {
			container.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 13), G: uint8(y * 17), B: 77, A: uint8(x*y + 200)})
		}
	}
	source := container.SubImage(image.Rect(-2, 8, 10, 17)).(*image.NRGBA)
	before := bytes.Clone(container.Pix)
	stack := gaussianStack(.5, 2, 5)
	options := Options{ApproximateGaussian: true}
	estimate, err := WorkingEstimate(source, stack, options)
	if err != nil {
		t.Fatal(err)
	}
	options.MaxWorkingBytes = estimate.WorkingBytes
	result, err := Apply(context.Background(), source, stack, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rect != source.Rect || !bytes.Equal(before, container.Pix) {
		t.Fatal("output rectangle changed or caller input mutated")
	}
	options.MaxWorkingBytes--
	if output, err := Apply(nil, source, stack, options); output != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("minus-one budget: %v, %v", output, err)
	}
	result.Pix[0] ^= 0xff
	if !bytes.Equal(before, container.Pix) {
		t.Fatal("result aliases caller input")
	}
}

func TestGaussianPremultipliedEdgesKernelAndOrder(t *testing.T) {
	for _, radius := range []float64{.1, .25, .5, 1, 2, 3, 5, 50, 250} {
		sigma := gaussianSigma(radius)
		kernel := gaussianKernel(sigma, int(math.Ceil(4*sigma)))
		var total float64
		for index, weight := range kernel {
			if weight < 0 || !finite(weight) || math.Abs(weight-kernel[len(kernel)-1-index]) > 1e-11 {
				t.Fatalf("invalid kernel for radius %g", radius)
			}
			total += weight
		}
		if math.Abs(total-1) > 1e-12 {
			t.Fatalf("kernel mass %g", total)
		}
	}
	uniform := image.NewNRGBA(image.Rect(2, -1, 15, 12))
	for offset := 0; offset < len(uniform.Pix); offset += 4 {
		copy(uniform.Pix[offset:offset+4], []byte{40, 120, 220, 75})
	}
	result, err := Apply(nil, uniform, gaussianStack(5), Options{ApproximateGaussian: true})
	if err != nil || !bytes.Equal(result.Pix, uniform.Pix) {
		t.Fatalf("clamped uniform field changed: %v", err)
	}
	source := image.NewNRGBA(image.Rect(0, 0, 9, 9))
	for offset := 0; offset < len(source.Pix); offset += 4 {
		copy(source.Pix[offset:offset+4], []byte{0, 255, 0, 0})
	}
	source.SetNRGBA(4, 4, color.NRGBA{R: 255, A: 255})
	result, err = Apply(nil, source, gaussianStack(2), Options{ApproximateGaussian: true})
	if err != nil {
		t.Fatal(err)
	}
	if pixel := result.NRGBAAt(4, 3); pixel.R != 255 || pixel.G != 0 || pixel.B != 0 || pixel.A == 0 {
		t.Fatalf("hidden transparent RGB leaked into convolution: %v", pixel)
	}
	first, err := Apply(nil, source, gaussianStack(1), Options{ApproximateGaussian: true})
	if err != nil {
		t.Fatal(err)
	}
	sequential, err := Apply(nil, first, gaussianStack(2), Options{ApproximateGaussian: true})
	if err != nil {
		t.Fatal(err)
	}
	chain, err := Apply(nil, source, gaussianStack(1, 2), Options{ApproximateGaussian: true})
	if err != nil || !bytes.Equal(chain.Pix, sequential.Pix) {
		t.Fatalf("stack did not execute stored order: %v", err)
	}
}

func TestGaussianPolicyCancellationInvalidAndDisabled(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 3, 3))
	if _, err := Apply(nil, source, gaussianStack(1)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("private-kernel approximation was not explicit: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, applyErr := Apply(ctx, source, gaussianStack(1), Options{ApproximateGaussian: true})
	if !errors.Is(applyErr, context.Canceled) {
		t.Fatalf("cancelled operation: %v", applyErr)
	}
	for _, radius := range []float64{-1, math.NaN(), math.Inf(1)} {
		_, applyErr := Apply(nil, source, gaussianStack(radius), Options{ApproximateGaussian: true})
		if !errors.Is(applyErr, ErrInvalid) {
			t.Fatalf("invalid radius %g: %v", radius, applyErr)
		}
	}
	disabled := Stack{Filters: []Filter{{Kind: "unrecognized", Enabled: true, Opacity: 1}}}
	result, err := Apply(nil, source, disabled)
	if err != nil || !bytes.Equal(result.Pix, source.Pix) {
		t.Fatalf("disabled stack touched pixels: %v", err)
	}
	malformed := &image.NRGBA{Rect: image.Rect(0, 0, 3, 3), Stride: 12, Pix: make([]byte, 35)}
	if _, err := Apply(nil, malformed, Stack{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short pixels: %v", err)
	}
}
