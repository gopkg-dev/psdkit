package canvaspix

import (
	"bytes"
	"image"
	"image/color"
	"testing"
)

func TestEncodingQuantizationIsStableAndPreservesCoverage(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	for alpha := range 256 {
		for value := range 256 {
			source.SetNRGBA(value, alpha, color.NRGBA{R: uint8(value), G: uint8(255 - value), B: 200, A: uint8(alpha)})
		}
	}
	before := bytes.Clone(source.Pix)
	first := ForPNG(source)
	second := ForPNG(first)
	if !bytes.Equal(first.Pix, second.Pix) {
		t.Fatal("encoding an already encoded template changes its pixels")
	}
	inPlace := image.NewNRGBA(source.Rect)
	copy(inPlace.Pix, source.Pix)
	ReadbackInPlace(inPlace)
	if !bytes.Equal(first.Pix, inPlace.Pix) {
		t.Fatal("in-place Canvas readback differs from immutable export")
	}
	if !bytes.Equal(before, source.Pix) {
		t.Fatal("encoding mutated reusable template pixels")
	}
	for y := range 256 {
		for x := range 256 {
			if first.NRGBAAt(x, y).A != uint8(y) {
				t.Fatal("coverage changed")
			}
		}
	}
	if got := first.NRGBAAt(100, 128); got.B != 199 {
		t.Fatalf("Canvas export blue=%d, want independently observed 199", got.B)
	}
	if got := first.NRGBAAt(100, 0); got != (color.NRGBA{}) {
		t.Fatalf("transparent output retains hidden color: %v", got)
	}
}

func TestEncodingHandlesCroppedPixelBuffers(t *testing.T) {
	parent := image.NewNRGBA(image.Rect(0, 0, 3, 3))
	parent.SetNRGBA(1, 1, color.NRGBA{R: 17, G: 83, B: 200, A: 128})
	source := parent.SubImage(image.Rect(1, 1, 2, 2)).(*image.NRGBA)
	encoded := ForPNG(source)
	if encoded.Rect != (image.Rect(0, 0, 1, 1)) || encoded.NRGBAAt(0, 0).B != 199 {
		t.Fatal("cropped pixels use wrong stride or origin")
	}
}
