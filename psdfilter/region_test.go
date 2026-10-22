package psdfilter

import (
	"errors"
	"image"
	"image/color"
	"math/rand"
	"testing"
)

func TestRegionOpacityFactsPreserveMixedImageShortKernelQuantization(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 96, 48))
	random := rand.New(rand.NewSource(4917))
	for row := range 48 {
		for column := range 96 {
			source.SetNRGBA(column, row, color.NRGBA{R: byte(random.Intn(256)),
				G: byte(random.Intn(256)), B: byte(random.Intn(256)), A: 255})
		}
	}
	source.SetNRGBA(95, 47, color.NRGBA{R: 17, A: 0})
	stack := gaussianStack(1)
	options := Options{ApproximateGaussian: true}
	full, err := Apply(nil, source, stack, options)
	if err != nil {
		t.Fatal(err)
	}
	window := image.Rect(20, 10, 45, 35)
	input := image.NewNRGBA(window)
	for row := window.Min.Y; row < window.Max.Y; row++ {
		for column := window.Min.X; column < window.Max.X; column++ {
			input.SetNRGBA(column, row, source.NRGBAAt(column, row))
		}
	}
	local, err := Apply(nil, input, stack, options)
	if err != nil {
		t.Fatal(err)
	}
	correct, err := ApplyRegion(nil, input, stack, []bool{false}, options)
	if err != nil {
		t.Fatal(err)
	}
	seenIncorrectLocal := false
	interior := window.Inset(4)
	for row := interior.Min.Y; row < interior.Max.Y; row++ {
		for column := interior.Min.X; column < interior.Max.X; column++ {
			if correct.NRGBAAt(column, row) != full.NRGBAAt(column, row) {
				t.Fatal("full mixed-alpha input was replaced by local opaque quantization")
			}
			seenIncorrectLocal = seenIncorrectLocal || local.NRGBAAt(column, row) != full.NRGBAAt(column, row)
		}
	}
	if !seenIncorrectLocal {
		t.Fatal("fixture did not expose the local/global opacity one-byte seam")
	}
	if _, err := ApplyRegion(nil, source, stack, []bool{true}, options); !errors.Is(err, ErrInvalid) {
		t.Fatal("contradicted complete-image opacity fact was accepted")
	}
}
