package psdfile

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"testing"
)

func TestJPEGMonotoneChromaControlledSlopesAndTies(t *testing.T) {
	for _, testCase := range []struct {
		name              string
		left, code, right uint8
		expected          [2]uint8
	}{
		{"symmetric step", 96, 128, 160, [2]uint8{120, 136}},
		{"falling asymmetric", 225, 132, 91, [2]uint8{152, 112}},
		{"rising asymmetric", 146, 157, 162, [2]uint8{154, 160}},
		{"half ties preserve mean", 99, 110, 136, [2]uint8{104, 116}},
		{"local maximum", 217, 225, 132, [2]uint8{225, 225}},
		{"flat neighbor", 128, 128, 160, [2]uint8{128, 128}},
		{"low extreme", 0, 1, 255, [2]uint8{0, 2}},
		{"high extreme", 0, 254, 255, [2]uint8{253, 255}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			plane := []byte{testCase.left, testCase.code, testCase.right}
			actual := [2]uint8{jpegMonotoneChroma(plane, 3, 0, 1, 3, true),
				jpegMonotoneChroma(plane, 3, 0, 1, 3, false)}
			if actual != testCase.expected {
				t.Fatalf("got %v, want %v", actual, testCase.expected)
			}
			if int(actual[0])+int(actual[1]) != int(testCase.code)*2 {
				t.Fatal("half-code ties shifted the reconstructed chroma-cell mean")
			}
		})
	}
}

func TestJPEGMonotoneOddSizeVerticalRepetitionAndOwnedOutput(t *testing.T) {
	source := image.NewYCbCr(image.Rect(0, 0, 5, 3), image.YCbCrSubsampleRatio420)
	for index := range source.Y {
		source.Y[index] = 128
	}
	copy(source.Cb, []byte{96, 128, 160, 180, 200, 220})
	for index := range source.Cr {
		source.Cr[index] = 128
	}
	originalCb := bytes.Clone(source.Cb)
	target := image.NewNRGBA(source.Rect)
	if err := renderLinkedJPEG(context.Background(), target, source, false); err != nil {
		t.Fatal(err)
	}
	for y, chroma := range [][]uint8{{96, 96, 120, 136, 160}, {96, 96, 120, 136, 160},
		{180, 180, 195, 205, 220}} {
		for x, cb := range chroma {
			red, green, blue := jpegFloatRGB(128, cb, 128)
			actual := target.NRGBAAt(x, y)
			if actual.R != red || actual.G != green || actual.B != blue || actual.A != 255 {
				t.Fatalf("pixel %d,%d = %v", x, y, actual)
			}
		}
	}
	if !bytes.Equal(originalCb, source.Cb) {
		t.Fatal("monotone reconstruction mutated borrowed decoder planes")
	}
	target.Pix[0] = 0
	if source.Y[0] != 128 {
		t.Fatal("output aliases a decoder plane")
	}
}

func TestJPEGMonotoneSubimageOriginAndExplicitTriangle(t *testing.T) {
	source := image.NewYCbCr(image.Rect(3, 5, 7, 8), image.YCbCrSubsampleRatio420)
	for index := range source.Y {
		source.Y[index] = 128
	}
	copy(source.Cb, []byte{96, 128, 160, 180, 200, 220})
	for index := range source.Cr {
		source.Cr[index] = 128
	}
	target := image.NewNRGBA(image.Rect(0, 0, 4, 3))
	if err := renderLinkedJPEGMonotone(context.Background(), target, source); err != nil {
		t.Fatal(err)
	}
	for x, cb := range []uint8{96, 120, 136, 160} {
		_, green, blue := jpegFloatRGB(128, cb, 128)
		if actual := target.NRGBAAt(x, 0); actual.G != green || actual.B != blue {
			t.Fatalf("odd-origin pixel %d = %v", x, actual)
		}
	}
	zeroOrigin := image.NewYCbCr(image.Rect(0, 0, 6, 2), image.YCbCrSubsampleRatio420)
	for index := range zeroOrigin.Y {
		zeroOrigin.Y[index] = 128
	}
	copy(zeroOrigin.Cb, []byte{96, 128, 160})
	for index := range zeroOrigin.Cr {
		zeroOrigin.Cr[index] = 128
	}
	monotone := image.NewNRGBA(zeroOrigin.Rect)
	triangle := image.NewNRGBA(zeroOrigin.Rect)
	if err := renderLinkedJPEG(context.Background(), monotone, zeroOrigin, false); err != nil {
		t.Fatal(err)
	}
	if err := renderLinkedJPEG(context.Background(), triangle, zeroOrigin, true); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(monotone.Pix, triangle.Pix) {
		t.Fatal("explicit centered triangle silently switched to the default monotone rule")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := renderLinkedJPEG(ctx, monotone, zeroOrigin, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled monotone conversion proceeded: %v", err)
	}
}

func TestJPEGOtherRatiosKeepNearestChroma(t *testing.T) {
	expected := map[uint8]color.NRGBA{96: {128, 139, 71, 255}, 128: {128, 128, 128, 255},
		160: {128, 117, 185, 255}}
	for _, ratio := range []image.YCbCrSubsampleRatio{image.YCbCrSubsampleRatio444,
		image.YCbCrSubsampleRatio422, image.YCbCrSubsampleRatio440, image.YCbCrSubsampleRatio411,
		image.YCbCrSubsampleRatio410} {
		t.Run(ratio.String(), func(t *testing.T) {
			source := image.NewYCbCr(image.Rect(0, 0, 5, 3), ratio)
			for index := range source.Y {
				source.Y[index] = 128
			}
			for index := range source.Cb {
				source.Cb[index] = []uint8{96, 128, 160}[index%source.CStride%3]
				source.Cr[index] = 128
			}
			target := image.NewNRGBA(source.Rect)
			if err := renderLinkedJPEG(context.Background(), target, source, false); err != nil {
				t.Fatal(err)
			}
			for y := range 3 {
				for x := range 5 {
					cb := source.Cb[source.COffset(x, y)]
					if actual := target.NRGBAAt(x, y); actual != expected[cb] {
						t.Fatalf("nearest fallback changed %d,%d = %v, want %v", x, y, actual, expected[cb])
					}
				}
			}
		})
	}
}
