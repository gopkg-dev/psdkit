package psdprecision

import (
	"errors"
	"image"
	"math"
	"testing"
)

func TestPrecisionBlendAlphaAndHDRMathematics(t *testing.T) {
	for _, testCase := range []struct {
		name, mode string
		encoding   Encoding
		base, top  [4]float64
		expected   [4]float64
	}{
		{"multiply-partial-alpha", "mul ", LinearRGB, [4]float64{2, -.5, .25, .5},
			[4]float64{3, 2, .8, .5}, [4]float64{11.0 / 3, 1.0 / 6, 5.0 / 12, .75}},
		{"screen-encoded", "scrn", EncodedRGB, [4]float64{.2, .4, .6, .5},
			[4]float64{.5, .25, .75, .5}, [4]float64{13.0 / 30, .4, .75, .75}},
		{"linear-dodge-hdr", "lddg", LinearRGB, [4]float64{2, -.5, .25, .5},
			[4]float64{3, 2, .8, .5}, [4]float64{10.0 / 3, 1, .7, .75}},
		{"linear-dodge-encoded-overlap-saturation", "lddg", EncodedRGB, [4]float64{.8, .2, .1, 1},
			[4]float64{.7, .3, .2, .5}, [4]float64{.9, .35, .2, 1}},
		{"multiply-transparent-backdrop", "mul", LinearRGB, [4]float64{0, 0, 0, 0},
			[4]float64{3, -.75, .123456789, .25}, [4]float64{3, -.75, .123456789, .25}},
		{"darken-hdr", "dark", LinearRGB, [4]float64{-2, .25, 3, .5},
			[4]float64{2, -.75, 4, .5}, [4]float64{-2.0 / 3, -5.0 / 12, 10.0 / 3, .75}},
		{"lighten-hdr", "lighten", LinearRGB, [4]float64{-2, .25, 3, .5},
			[4]float64{2, -.75, 4, .5}, [4]float64{2.0 / 3, -1.0 / 12, 11.0 / 3, .75}},
		{"difference-encoded", "diff", EncodedRGB, [4]float64{.2, .4, .6, .5},
			[4]float64{.7, .25, .9, .5}, [4]float64{7.0 / 15, 4.0 / 15, .6, .75}},
		{"exclusion-encoded", "smud", EncodedRGB, [4]float64{.2, .4, .6, .5},
			[4]float64{.7, .25, .9, .5}, [4]float64{38.0 / 75, 11.0 / 30, 16.0 / 25, .75}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			bottom := precisionLayer(testCase.encoding, testCase.base[:]...)
			top := precisionLayer(testCase.encoding, testCase.top[:]...)
			top.BlendMode = testCase.mode
			result, err := Render(nil, &Scene{Width: 1, Height: 1, Encoding: testCase.encoding,
				Layers: []*Layer{bottom, top}})
			if err != nil {
				t.Fatal(err)
			}
			precisionCheck(t, result, testCase.expected[:])
		})
	}
}

func TestPrecisionPassGroupUsesBackdropThenInterpolatesPremultiplied(t *testing.T) {
	base := precisionLayer(LinearRGB, 2, 0, 0, .5)
	multiply := precisionLayer(LinearRGB, 3, 0, 0, .5)
	multiply.BlendMode = "mul"
	green := precisionLayer(LinearRGB, 0, 4, 0, .5)
	opacity := .5
	group := &Layer{BlendMode: "pass", Opacity: &opacity, Children: []*Layer{multiply, green},
		Mask: &Mask{Bounds: image.Rect(0, 0, 1, 1), Values: []float64{.5}, Default: 0}}
	result, err := Render(nil, &Scene{Width: 1, Height: 1, Encoding: LinearRGB, Layers: []*Layer{base, group}})
	if err != nil {
		t.Fatal(err)
	}
	// Child result premultiplication is (1.375,.5*4,0,.875); interpolate it 25 percent into (1,0,0,.5).
	precisionCheck(t, result, []float64{35.0 / 19, 16.0 / 19, 0, 19.0 / 32})
	if _, err := Render(nil, &Scene{Width: 1, Height: 1, Encoding: LinearRGB, Layers: []*Layer{base, group}},
		Options{MaxWorkingBytes: 63}); !errors.Is(err, ErrLimit) {
		t.Fatalf("partial pass group backdrop copy was not included in working budget: %v", err)
	}
	if _, err := Render(nil, &Scene{Width: 1, Height: 1, Encoding: LinearRGB, Layers: []*Layer{base, group}},
		Options{MaxWorkingBytes: 64}); err != nil {
		t.Fatalf("exact output and pass-interpolation buffer budget rejected: %v", err)
	}
}

func TestPrecision32ScreenUnsupportedAndOverflowPolicies(t *testing.T) {
	if !SupportsBlend("screen", EncodedRGB) || SupportsBlend("screen", LinearRGB) {
		t.Fatal("Photoshop RGB32 Screen availability was ignored")
	}
	if !SupportsBlend("exclusion", EncodedRGB) || SupportsBlend("exclusion", LinearRGB) ||
		SupportsBlend("difference", LinearRGB) {
		t.Fatal("unavailable or uncalibrated 32-bit modes were implicitly enabled")
	}
	top := precisionLayer(LinearRGB, 1e200, 1e200, 1e200, 1)
	top.BlendMode = "scrn"
	scene := &Scene{Width: 1, Height: 1, Encoding: LinearRGB, Layers: []*Layer{
		precisionLayer(LinearRGB, 1e200, 1e200, 1e200, 1), top,
	}}
	if _, err := Render(nil, scene); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unavailable native 32-bit Screen was approximated: %v", err)
	}
	top.BlendMode = "mul "
	if _, err := Render(nil, scene); !errors.Is(err, ErrNonFinite) {
		t.Fatalf("finite-input multiplication overflow silently escaped the non-finite policy: %v", err)
	}
	result, err := Render(nil, scene, Options{NonFinite: NonFiniteZero})
	if err != nil {
		t.Fatal(err)
	}
	precisionCheck(t, result, []float64{0, 0, 0, 1})
	if math.IsInf(top.Pixels.Pix[0], 0) || top.Pixels.Pix[0] != 1e200 {
		t.Fatal("derived overflow policy modified original finite samples")
	}
}
