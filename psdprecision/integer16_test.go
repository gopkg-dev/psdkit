package psdprecision

import (
	"context"
	"errors"
	"image"
	"math"
	"testing"
)

func opaqueRGB16Layer(bounds image.Rectangle, codes [3]uint16, opacity uint8, mode string) *Layer {
	value := float64(opacity) / 255
	pixels := &FloatImage{Width: bounds.Dx(), Height: bounds.Dy(), Encoding: EncodedRGB,
		Pix: make([]float64, bounds.Dx()*bounds.Dy()*4)}
	for offset := 0; offset < len(pixels.Pix); offset += 4 {
		for component := range codes {
			pixels.Pix[offset+component] = float64(codes[component]) / 65535
		}
		pixels.Pix[offset+3] = 1
	}
	return &Layer{Bounds: bounds, Pixels: pixels, Opacity: &value, BlendMode: mode}
}

func TestRGB16OpaqueRawCodeMappingAndStoredHalf(t *testing.T) {
	layer := opaqueRGB16Layer(image.Rect(0, 0, 65536, 1), [3]uint16{}, 255, "norm")
	for pixel := range 65536 {
		for component := range 3 {
			layer.Pixels.Pix[pixel*4+component] = float64(pixel) / 65535
		}
	}
	before := append([]float64(nil), layer.Pixels.Pix...)
	result, err := RenderRGB16Opaque(nil, &Scene{Width: 65536, Height: 1, Layers: []*Layer{layer}})
	if err != nil {
		t.Fatal(err)
	}
	for pixel := range 65536 {
		wanted := float64((pixel+1)/2) / 32768
		if result.Pix[pixel*4] != wanted || result.Pix[pixel*4+3] != 1 {
			t.Fatalf("raw%d integer result %.17g alpha %.17g", pixel, result.Pix[pixel*4], result.Pix[pixel*4+3])
		}
		if layer.Pixels.Pix[pixel*4] != before[pixel*4] {
			t.Fatal("integer renderer changed borrowed raw samples")
		}
	}
	if saved := math.Round(result.Pix[32767*4] * 65535); saved != 32768 {
		t.Fatalf("native half-domain code saved as %.0f, expected 32768", saved)
	}
	result.Pix[0] = 1
	if layer.Pixels.Pix[0] != 0 {
		t.Fatal("integer output aliases its raw source")
	}
}

func TestRGB16OpaqueKernelDomainAndByteOpacity(t *testing.T) {
	bounds := image.Rect(0, 0, 1, 1)
	cases := []struct {
		mode   string
		wanted uint32
	}{
		{"norm", 16385}, {"mul ", 8193}, {"scrn", 24576}, {"diff", 1}, {"lddg", 32768},
	}
	for _, item := range cases {
		t.Run(item.mode, func(t *testing.T) {
			base := opaqueRGB16Layer(bounds, [3]uint16{32767, 32767, 32767}, 255, "norm")
			top := opaqueRGB16Layer(bounds, [3]uint16{32769, 32769, 32769}, 255, item.mode)
			result, err := RenderRGB16Opaque(nil, &Scene{Width: 1, Height: 1, Layers: []*Layer{base, top}})
			if err != nil {
				t.Fatal(err)
			}
			if result.Pix[0] != float64(item.wanted)/32768 {
				t.Fatalf("kernel result %.17g, expected internal code %d", result.Pix[0], item.wanted)
			}
		})
	}
	base := opaqueRGB16Layer(bounds, [3]uint16{}, 255, "norm")
	top := opaqueRGB16Layer(bounds, [3]uint16{65535, 65535, 65535}, 128, "norm")
	result, err := RenderRGB16Opaque(nil, &Scene{Width: 1, Height: 1, Layers: []*Layer{base, top}})
	if err != nil || result.Pix[0] != float64(16448)/32768 {
		t.Fatalf("byte128 Q15 opacity = %v, %v", result, err)
	}
}

func TestRGB16OpaqueGuardPreservesGeneralFloatContract(t *testing.T) {
	bounds := image.Rect(0, 0, 1, 1)
	layer := opaqueRGB16Layer(bounds, [3]uint16{32767, 32767, 32767}, 255, "norm")
	scene := &Scene{Width: 1, Height: 1, Layers: []*Layer{layer}}
	layer.Pixels.Pix[0] = 2.125
	if _, err := RenderRGB16Opaque(nil, scene); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("arbitrary HDR value escaped raw RGB16 guard: %v", err)
	}
	result, err := Render(nil, scene)
	if err != nil || result.Pix[0] != 2.125 || result.Pix[1] != float64(32767)/65535 {
		t.Fatalf("generic floating contract changed: %v, %v", result, err)
	}
	for _, change := range []func(*Layer){
		func(item *Layer) { item.Clipping = true },
		func(item *Layer) { item.Mask = &Mask{Bounds: bounds, Values: []float64{1}, Default: 1} },
		func(item *Layer) { item.Pixels.Pix[3] = .5 },
		func(item *Layer) { value := .5; item.Opacity = &value },
		func(item *Layer) { item.BlendMode = "dark" },
	} {
		candidate := opaqueRGB16Layer(bounds, [3]uint16{32767, 32767, 32767}, 255, "norm")
		change(candidate)
		scene.Layers = []*Layer{candidate}
		if _, err := RenderRGB16Opaque(nil, scene); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("uncalibrated integer scene escaped guard: %v", err)
		}
	}
	layer.Pixels.Pix[0] = math.NaN()
	scene.Layers = []*Layer{layer}
	if _, err := RenderRGB16Opaque(nil, scene); !errors.Is(err, ErrNonFinite) {
		t.Fatalf("RGB16 non-finite failure was swallowed: %v", err)
	}
}

func TestRGB16OpaqueRejectsNoncanonicalAdjacentFloats(t *testing.T) {
	bounds := image.Rect(0, 0, 1, 1)
	for _, component := range []int{0, 1, 2} {
		layer := opaqueRGB16Layer(bounds, [3]uint16{32767, 32767, 32767}, 255, "norm")
		layer.Pixels.Pix[component] = math.Nextafter(layer.Pixels.Pix[component], 1)
		scene := &Scene{Width: 1, Height: 1, Layers: []*Layer{layer}}
		if _, err := RenderRGB16Opaque(nil, scene); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("component%d adjacent noncanonical float entered raw16 arithmetic: %v", component, err)
		}
		if _, err := Render(nil, scene); err != nil {
			t.Fatalf("generic float scene rejected a legitimate adjacent float: %v", err)
		}
	}
	base := opaqueRGB16Layer(bounds, [3]uint16{32767, 32767, 32767}, 255, "norm")
	top := opaqueRGB16Layer(bounds, [3]uint16{32767, 32767, 32767}, 128, "norm")
	opacity := math.Nextafter(*top.Opacity, 1)
	top.Opacity = &opacity
	scene := &Scene{Width: 1, Height: 1, Layers: []*Layer{base, top}}
	if _, err := RenderRGB16Opaque(nil, scene); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("adjacent noncanonical opacity entered source byte arithmetic: %v", err)
	}
	if _, err := Render(nil, scene); err != nil {
		t.Fatalf("generic float scene rejected a legitimate adjacent opacity: %v", err)
	}
}

func TestRGB16OpaqueAbsoluteRegionBudgetsAndCancellation(t *testing.T) {
	bounds := image.Rect(100, 200, 102, 201)
	layer := opaqueRGB16Layer(bounds, [3]uint16{32767, 1, 65535}, 255, "norm")
	scene := &Scene{Width: 1 << 20, Height: 1 << 20, Layers: []*Layer{layer}}
	region, err := RenderRGB16OpaqueRegion(nil, scene, bounds, Options{MaxWorkingBytes: 64, MaxPixels: 2})
	if err != nil || region.Bounds != bounds || region.Image.Width != 2 || region.Image.Height != 1 ||
		region.Image.Pix[0] != .5 || region.Image.Pix[1] != float64(1)/32768 {
		t.Fatalf("absolute RGB16 ROI: %v, %v", region, err)
	}
	if _, err := RenderRGB16OpaqueRegion(nil, scene, bounds, Options{MaxWorkingBytes: 63}); !errors.Is(err, ErrLimit) {
		t.Fatalf("RGB16 ROI allocation escaped budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RenderRGB16OpaqueRegion(ctx, scene, bounds); !errors.Is(err, context.Canceled) {
		t.Fatalf("RGB16 ROI ignored cancellation: %v", err)
	}
}
