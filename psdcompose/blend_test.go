package psdcompose

import (
	"errors"
	"math"
	"testing"
)

func TestBlendSeparableVectors(t *testing.T) {
	backdrop := [3]float64{0.25, 0.5, 0.75}
	source := [3]float64{0.75, 0.25, 0.5}
	tests := []struct {
		mode string
		want [3]float64
	}{
		{"norm", source},
		{"dark", [3]float64{0.25, 0.25, 0.5}},
		{"mul ", [3]float64{0.1875, 0.125, 0.375}},
		{"idiv", [3]float64{0, 0, 0.5}},
		{"lbrn", [3]float64{0, 0, 0.25}},
		{"lite", [3]float64{0.75, 0.5, 0.75}},
		{"scrn", [3]float64{0.8125, 0.625, 0.875}},
		{"div ", [3]float64{1, 2.0 / 3, 1}},
		{"lddg", [3]float64{1, 0.75, 1}},
		{"over", [3]float64{0.375, 0.25, 0.75}},
		{"sLit", [3]float64{0.375, 0.375, 0.75}},
		{"hLit", [3]float64{0.625, 0.25, 0.75}},
		{"vLit", [3]float64{0.5, 0, 0.75}},
		{"lLit", [3]float64{0.75, 0, 0.75}},
		{"pLit", [3]float64{0.5, 0.5, 0.75}},
		{"hMix", [3]float64{0, 0, 1}},
		{"diff", [3]float64{0.5, 0.25, 0.25}},
		{"smud", [3]float64{0.625, 0.5, 0.5}},
		{"fsub", [3]float64{0, 0.25, 0.25}},
		{"fdiv", [3]float64{1.0 / 3, 1, 1}},
	}
	for _, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			got, err := blendRGB(test.mode, backdrop, source, 1)
			if err != nil {
				t.Fatal(err)
			}
			assertBlendColor(t, got, test.want)
		})
	}
}

func TestBlendNonseparable(t *testing.T) {
	backdrop := [3]float64{0.25, 0.5, 0.75}
	source := [3]float64{0.75, 0.25, 0.5}
	tests := []struct {
		mode string
		want [3]float64
	}{
		{"dkCl", source},
		{"lgCl", backdrop},
		{"hue ", [3]float64{0.775, 0.275, 0.525}},
		{"sat ", backdrop},
		{"col ", [3]float64{0.775, 0.275, 0.525}},
		{"lum ", [3]float64{0.225, 0.475, 0.725}},
	}
	for _, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			got, err := blendRGB(test.mode, backdrop, source, 1)
			if err != nil {
				t.Fatal(err)
			}
			assertBlendColor(t, got, test.want)
		})
	}
	for _, gray := range []float64{0, 0.5, 1} {
		for _, mode := range []string{"hue", "sat", "col", "lum"} {
			got, err := blendRGB(mode, [3]float64{gray, gray, gray}, source, 1)
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range got {
				if math.IsNaN(value) || value < 0 || value > 1 {
					t.Fatalf("%s gray %g: invalid color %v", mode, gray, got)
				}
			}
		}
	}
}

func TestBlendBoundariesAndSpecialFill(t *testing.T) {
	for _, test := range []struct {
		mode string
		back float64
		src  float64
		fill float64
		want float64
	}{
		{"idiv", 1, 0, 1, 1},
		{"div", 0, 1, 1, 0},
		{"vLit", 1, 0, 1, 0},
		{"vLit", 0, 1, 1, 1},
		{"fdiv", 0, 0, 1, 0},
		{"fdiv", 0.5, 0, 1, 1},
		{"div", 0.25, 0.75, 0.5, 0.4},
		{"idiv", 0.75, 0.25, 0.5, 0.6},
		{"lddg", 0.75, 0.75, 0.5, 1},
		{"lbrn", 0.25, 0.25, 0.5, 0},
		{"lLit", 0.25, 0.75, 0.5, 0.5},
		{"vLit", 0.25, 0.75, 0.5, 1.0 / 3},
		{"hMix", 0.4, 0.7, 0.5, 0.5},
		{"diff", 0.25, 0.75, 0.5, 0.125},
	} {
		t.Run(test.mode, func(t *testing.T) {
			backdrop := [3]float64{test.back, test.back, test.back}
			source := [3]float64{test.src, test.src, test.src}
			got, err := blendRGB(test.mode, backdrop, source, test.fill)
			if err != nil {
				t.Fatal(err)
			}
			assertBlendColor(t, got, [3]float64{test.want, test.want, test.want})
		})
	}
	for _, mode := range []string{"idiv", "div", "lbrn", "lddg", "vLit", "lLit", "hMix", "diff"} {
		backdrop := [3]float64{0.125, 0.5, 0.875}
		got, err := blendRGB(mode, backdrop, [3]float64{1, 0.75, 0}, 0)
		if err != nil || !blendUsesSpecialFill(mode) {
			t.Fatalf("%s: %v", mode, err)
		}
		assertBlendColor(t, got, backdrop)
	}
}

func TestBlendRejectsUnknownMode(t *testing.T) {
	for _, mode := range []string{"diss", "pass", "unknown", "NORM"} {
		if SupportedBlend(mode) {
			t.Fatalf("unexpected support for %q", mode)
		}
		_, err := blendRGB(mode, [3]float64{}, [3]float64{}, 1)
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("%q: %v", mode, err)
		}
	}
}

func TestBlendFriendlyNames(t *testing.T) {
	tests := map[string]string{
		"normal": "norm", "darken": "dark", "multiply": "mul", "colorBurn": "idiv",
		"linearBurn": "lbrn", "darkerColor": "dkCl", "lighten": "lite", "screen": "scrn",
		"colorDodge": "div", "linearDodge": "lddg", "lighterColor": "lgCl", "overlay": "over",
		"softLight": "sLit", "hardLight": "hLit", "vividLight": "vLit", "linearLight": "lLit",
		"pinLight": "pLit", "hardMix": "hMix", "difference": "diff", "exclusion": "smud",
		"subtract": "fsub", "divide": "fdiv", "hue": "hue", "saturation": "sat",
		"color": "col", "colr": "col", "luminosity": "lum",
	}
	for friendly, key := range tests {
		if !SupportedBlend(friendly) || normalizeBlend(friendly) != key {
			t.Errorf("unsupported alias %q for %q", friendly, key)
		}
	}
}

func TestBlendAllModesFinite(t *testing.T) {
	modes := []string{
		"norm", "dark", "mul", "idiv", "lbrn", "dkCl", "lite", "scrn", "div", "lddg", "lgCl",
		"over", "sLit", "hLit", "vLit", "lLit", "pLit", "hMix", "diff", "smud", "fsub", "fdiv",
		"hue", "sat", "col", "lum",
	}
	colors := [][3]float64{{0, 0, 0}, {1, 1, 1}, {1, 0, 0}, {0, 1, 0}, {0, 0, 1}, {0.2, 0.5, 0.9}}
	for _, mode := range modes {
		if !SupportedBlend(mode) {
			t.Fatalf("missing supported mode %q", mode)
		}
		for _, backdrop := range colors {
			for _, source := range colors {
				for _, fill := range []float64{0, 0.5, 1} {
					got, err := blendRGB(mode, backdrop, source, fill)
					if err != nil {
						t.Fatal(err)
					}
					for _, value := range got {
						if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
							t.Fatalf("%s %v %v fill %g: invalid color %v", mode, backdrop, source, fill, got)
						}
					}
				}
			}
		}
	}
}

func assertBlendColor(t *testing.T, got, want [3]float64) {
	t.Helper()
	for channel := range got {
		if math.Abs(got[channel]-want[channel]) > 1e-12 {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
