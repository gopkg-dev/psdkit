package psdprecision_test

import (
	"context"
	"errors"
	"image"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdprecision"
)

func TestRGB16DisabledMaskRetainsNormalDifferenceIntegerResults(t *testing.T) {
	for _, mode := range []string{"norm", "diff"} {
		scene := &psdprecision.Scene{Width: 3, Height: 1, Layers: []*psdprecision.Layer{
			{Bounds: image.Rect(0, 0, 1, 1), Pixels: normalRawPixel(32767, 32768), BlendMode: "norm"},
			{Bounds: image.Rect(2, 0, 3, 1), Pixels: normalRawPixel(49151, 49151), BlendMode: mode},
		}}
		baseline, err := psdprecision.RenderRGB16(nil, scene)
		if err != nil {
			t.Fatal(err)
		}
		for _, layer := range scene.Layers {
			layer.Mask = &psdprecision.Mask{Bounds: image.Rect(0, 0, 1, 1), Values: []float64{0}, Disabled: true}
		}
		result, err := psdprecision.RenderRGB16(nil, scene)
		if err != nil || !reflect.DeepEqual(result.Pix, baseline.Pix) {
			t.Fatalf("disabled mask changed calibrated %s samples: %v", mode, err)
		}
		region, err := psdprecision.RenderRGB16Region(nil, scene, image.Rect(1, 0, 3, 1))
		if err != nil || !reflect.DeepEqual(region.Image.Pix, baseline.Pix[4:]) {
			t.Fatalf("disabled mask changed global coverage or ROI: %v", err)
		}
		if mode == "norm" {
			normal, err := psdprecision.RenderRGB16Normal(nil, scene)
			if err != nil || !reflect.DeepEqual(normal.Pix, baseline.Pix) {
				t.Fatalf("dedicated Normal path retained a different mask qualification: %v", err)
			}
			normalROI, err := psdprecision.RenderRGB16NormalRegion(nil, scene, image.Rect(1, 0, 3, 1))
			if err != nil || !reflect.DeepEqual(normalROI.Image.Pix, baseline.Pix[4:]) {
				t.Fatalf("Normal ROI changed its disabled-mask domain: %v", err)
			}
		}
		scene.Layers[0].Mask.Disabled = false
		if _, err := psdprecision.RenderRGB16(nil, scene); !errors.Is(err, psdprecision.ErrUnsupported) {
			t.Fatalf("active mask was promoted to unmasked integers: %v", err)
		}
		scene.Layers[0].Mask.Disabled = true
		scene.Layers[0].Unsupported = []string{"unknown source semantics"}
		if _, err := psdprecision.RenderRGB16(nil, scene); !errors.Is(err, psdprecision.ErrUnsupported) {
			t.Fatalf("disabled mask bypassed unknown active-layer metadata: %v", err)
		}
		scene.Layers[0].Unsupported = nil
		if result, err := psdprecision.RenderRGB16(nil, scene,
			psdprecision.Options{MaxWorkingBytes: 95}); result != nil || !errors.Is(err, psdprecision.ErrLimit) {
			t.Fatalf("disabled mask bypassed output memory limit: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if result, err := psdprecision.RenderRGB16(ctx, scene); result != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("disabled-mask composition ignored cancellation: %v", err)
		}
	}
}
