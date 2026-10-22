package psdfilter

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"
	"testing"
)

func TestGeneralGaussianKernelMassSymmetryAndBound(t *testing.T) {
	for _, radius := range []float64{.1, .25, .5, 1, 2, 2.01, 2.5, 2.75, 3, 3.25, 4, 5, 8, 16, 32, 100, 250} {
		kernel := gaussianFilterKernel(radius)
		allocated := int64(len(kernel)) * 8
		reserved := int64(2*int(math.Ceil(4*gaussianSigma(radius)))+1) * 8
		if allocated > reserved {
			t.Fatalf("radius %g: actual kernel %d exceeds budget %d", radius, allocated, reserved)
		}
		var mass float64
		for index, weight := range kernel {
			if weight < 0 || !finite(weight) || math.Abs(weight-kernel[len(kernel)-1-index]) > 1e-10 {
				t.Fatalf("radius %g: invalid or asymmetric weight %d = %g", radius, index, weight)
			}
			mass += weight
		}
		if math.Abs(mass-1) > 1e-12 {
			t.Fatalf("radius %g: kernel mass %g", radius, mass)
		}
	}
}

func TestNativeGaussianCapturedControl(t *testing.T) {
	readImage := func(t *testing.T, path string) *image.NRGBA {
		t.Helper()
		file, err := os.Open("testdata/" + path)
		if err != nil {
			t.Fatal(err)
		}
		decoded, decodeErr := png.Decode(file)
		closeErr := file.Close()
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		result := image.NewNRGBA(decoded.Bounds())
		draw.Draw(result, result.Rect, decoded, decoded.Bounds().Min, draw.Src)
		return result
	}
	for _, item := range []struct {
		kind, label string
		radius      float64
		tolerance   float64
	}{
		{"opaque", "0p5", .5, 0}, {"opaque", "1", 1, 0}, {"opaque", "2", 2, 0},
		{"alpha", "3", 3, 2}, {"alpha", "8", 8, 2}, {"alpha", "16", 16, 2},
	} {
		t.Run(item.kind+"-r"+item.label, func(t *testing.T) {
			source := readImage(t, "gaussian-noise-"+item.kind+"-input.png")
			expected := readImage(t, "gaussian-noise-"+item.kind+"-r"+item.label+".png")
			actual, err := Apply(nil, source, gaussianStack(item.radius), Options{ApproximateGaussian: true})
			if err != nil {
				t.Fatal(err)
			}
			if actual.Rect != expected.Rect {
				t.Fatalf("native rectangle %v differs from actual %v", expected.Rect, actual.Rect)
			}
			for offset := 0; offset < len(actual.Pix); offset += 4 {
				actualAlpha, expectedAlpha := float64(actual.Pix[offset+3]), float64(expected.Pix[offset+3])
				if math.Abs(actualAlpha-expectedAlpha) > item.tolerance {
					t.Fatalf("alpha difference at pixel %d exceeds %g", offset/4, item.tolerance)
				}
				for component := range 3 {
					actualValue := float64(actual.Pix[offset+component]) * actualAlpha / 255
					expectedValue := float64(expected.Pix[offset+component]) * expectedAlpha / 255
					if math.Abs(actualValue-expectedValue) > item.tolerance {
						t.Fatalf("premultiplied component %d at pixel %d exceeds %g", component, offset/4, item.tolerance)
					}
				}
			}
		})
	}
}

func TestFourBoxIntegerKernelAndTinyRadius(t *testing.T) {
	// Four width-three uniform boxes have these exact integer convolution multiplicities.
	expected := []float64{1, 4, 10, 16, 19, 16, 10, 4, 1}
	actual := gaussianBoxKernel(3)
	if len(actual) != len(expected) {
		t.Fatalf("kernel length %d", len(actual))
	}
	for index, value := range expected {
		if math.Abs(actual[index]-value/81) > 1e-12 {
			t.Fatalf("coefficient %d: %g, want %g", index, actual[index], value/81)
		}
	}
	source := image.NewNRGBA(image.Rect(0, 0, 31, 1))
	for offset := 0; offset < len(source.Pix); offset += 4 {
		source.Pix[offset+3] = 255
	}
	source.Pix[15*4], source.Pix[15*4+1], source.Pix[15*4+2] = 255, 127, 63
	result, err := Apply(nil, source, gaussianStack(.1), Options{ApproximateGaussian: true})
	if err != nil || !bytes.Equal(result.Pix, source.Pix) {
		t.Fatalf("native-observed tiny radius changed opaque source: %v", err)
	}
}
