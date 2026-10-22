package psdprecision_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"image"
	"math/big"
	"testing"

	"github.com/gopkg-dev/psdkit/psdprecision"
)

func geometryScene(bounds []image.Rectangle, sequence, seed int) *psdprecision.Scene {
	scene := &psdprecision.Scene{Width: 7, Height: 4}
	for index, rectangle := range bounds {
		pixels := &psdprecision.FloatImage{Width: rectangle.Dx(), Height: rectangle.Dy(), SourceDepth: 16}
		for sample := 0; sample < rectangle.Dx()*rectangle.Dy()*4; sample++ {
			code := uint16((sample*7919 + index*19483 + seed*601) & 65535)
			if sample%4 == 3 && (sample/4+seed)%3 == 0 {
				code = 0
			}
			pixels.Pix = append(pixels.Pix, float64(code)/65535)
		}
		mode := "norm"
		if index != 0 && sequence&(1<<(index-1)) != 0 {
			mode = "diff"
		}
		opacity := float64([]int{163, 239, 113, 255}[index]) / 255
		scene.Layers = append(scene.Layers, &psdprecision.Layer{
			Bounds: rectangle, Pixels: pixels, BlendMode: mode, Opacity: &opacity,
		})
	}
	return scene
}

// geometryNearest uses arbitrary-precision rational rounding independently of the production kernel.
func geometryNearest(numerator, denominator int64) int64 {
	var value, divisor, doubled big.Int
	value.SetInt64(numerator)
	divisor.SetInt64(denominator)
	doubled.Lsh(&value, 1)
	doubled.Add(&doubled, &divisor)
	divisor.Lsh(&divisor, 1)
	doubled.Quo(&doubled, &divisor)
	return doubled.Int64()
}

func geometryExpected(scene *psdprecision.Scene, point image.Point) [4]int64 {
	const q = int64(32768)
	var coverage image.Rectangle
	for _, layer := range scene.Layers {
		coverage = coverage.Union(layer.Bounds)
	}
	result := [4]int64{}
	if point.In(coverage) {
		result = [4]int64{q, q, q, 0}
	}
	for index, layer := range scene.Layers {
		if !point.In(layer.Bounds) {
			continue
		}
		offset := ((point.Y-layer.Bounds.Min.Y)*layer.Pixels.Width + point.X - layer.Bounds.Min.X) * 4
		var source [4]int64
		for component := range source {
			raw := int64(layer.Pixels.Pix[offset+component]*65535 + .5)
			source[component] = (raw + 1) / 2
		}
		opacity := int64(*layer.Opacity*255 + .5)
		alpha := geometryNearest(source[3]*geometryNearest(opacity*q, 255), q)
		if index == 0 {
			result = source
			result[3] = alpha
			continue
		}
		outAlpha := alpha + result[3] - geometryNearest(alpha*result[3], q)
		if outAlpha != 0 {
			weight := geometryNearest(alpha*q, outAlpha)
			for component := range 3 {
				adjusted := source[component]
				if layer.BlendMode == "diff" {
					distance := result[component] - source[component]
					if distance < 0 {
						distance = -distance
					}
					adjusted = geometryNearest(source[component]*(q-result[3])+distance*result[3], q)
				}
				result[component] = geometryNearest(result[component]*(q-weight)+adjusted*weight, q)
			}
		}
		result[3] = outAlpha
	}
	return result
}

func TestRGB16GeometryBigIntModelAndGlobalROI(t *testing.T) {
	geometries := [][]image.Rectangle{
		{image.Rect(0, 0, 7, 4), image.Rect(0, 0, 7, 4), image.Rect(0, 0, 7, 4), image.Rect(0, 0, 7, 4)},
		{image.Rect(-2, -1, 2, 2), image.Rect(5, 1, 8, 3), image.Rect(3, 2, 4, 4), image.Rect(6, -1, 8, 1)},
		{image.Rect(3, 1, 5, 2), image.Rect(1, 0, 6, 4), image.Rect(2, 2, 8, 5), image.Rect(0, 1, 3, 3)},
		{image.Rect(1, 1, 6, 4), image.Rect(2, 0, 7, 3), image.Rect(0, 2, 5, 4), image.Rect(4, 1, 6, 3)},
	}
	for geometry, bounds := range geometries {
		for sequence := range 8 {
			scene := geometryScene(bounds, sequence, geometry+17)
			before, err := json.Marshal(scene, json.Deterministic(true))
			if err != nil {
				t.Fatal(err)
			}
			full, err := psdprecision.RenderRGB16(nil, scene)
			if err != nil {
				t.Fatal(err)
			}
			for y := range 4 {
				for x := range 7 {
					expected := geometryExpected(scene, image.Pt(x, y))
					for component := range 4 {
						if full.Pix[(y*7+x)*4+component]*32768 != float64(expected[component]) {
							t.Fatalf("geometry%d sequence%d (%d,%d) component%d differs", geometry, sequence, x, y, component)
						}
					}
				}
			}
			for _, rectangle := range []image.Rectangle{image.Rect(0, 0, 3, 2), image.Rect(5, 2, 7, 4),
				image.Rect(2, 1, 6, 3), image.Rect(4, 2, 4, 2)} {
				region, err := psdprecision.RenderRGB16Region(nil, scene, rectangle)
				if err != nil {
					t.Fatal(err)
				}
				for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
					for x := rectangle.Min.X; x < rectangle.Max.X; x++ {
						for component := range 4 {
							if region.Image.Pix[((y-rectangle.Min.Y)*rectangle.Dx()+x-rectangle.Min.X)*4+component] !=
								full.Pix[(y*7+x)*4+component] {
								t.Fatal("ROI changed whole-scene first or global background")
							}
						}
					}
				}
			}
			after, err := json.Marshal(scene, json.Deterministic(true))
			if err != nil || string(after) != string(before) {
				t.Fatal("render mutated borrowed scene")
			}
		}
	}
}

func TestRGB16GeometryBoundariesCancelAndLayerQuota(t *testing.T) {
	scene := geometryScene([]image.Rectangle{image.Rect(-1, 0, 2, 2), image.Rect(4, 1, 6, 3),
		image.Rect(1, 1, 3, 3), image.Rect(3, 2, 7, 4)}, 5, 73)
	for _, amount := range []int64{7*4*32 - 1, 7 * 4 * 32, 7*4*32 + 1} {
		result, err := psdprecision.RenderRGB16(nil, scene, psdprecision.Options{MaxWorkingBytes: amount})
		if amount < 7*4*32 {
			if result != nil || !errors.Is(err, psdprecision.ErrLimit) {
				t.Fatalf("minus-one quota did not fail closed: %v", err)
			}
		} else if err != nil || result == nil {
			t.Fatalf("sufficient quota rejected: %v", err)
		}
	}
	probe := &normalFinalCancellation{Context: context.Background()}
	if _, err := psdprecision.RenderRGB16(probe, scene); err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(context.Background())
	late := &normalFinalCancellation{Context: parent, cancel: cancel, after: probe.calls}
	if result, err := psdprecision.RenderRGB16(late, scene); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("late cancellation exposed result: %v", err)
	}
	ctx, stop := context.WithCancelCause(context.Background())
	cause := errors.New("empty-window cancellation")
	stop(cause)
	if result, err := psdprecision.RenderRGB16Region(ctx, scene, image.Rect(0, 0, 0, 0)); result != nil ||
		!errors.Is(err, context.Canceled) || !errors.Is(context.Cause(ctx), cause) {
		t.Fatalf("empty ROI hid cancellation: %v", err)
	}
	scene.Layers = append(scene.Layers, scene.Layers[3])
	if result, err := psdprecision.RenderRGB16(nil, scene, psdprecision.Options{MaxLayers: 4}); result != nil ||
		!errors.Is(err, psdprecision.ErrLimit) {
		t.Fatalf("five-layer tree escaped the explicit layer quota: %v", err)
	}
}

func TestRGB16GeometryOffCanvasSamplesRemainWholeSceneInputs(t *testing.T) {
	scene := geometryScene([]image.Rectangle{image.Rect(-2, 0, 1, 1), image.Rect(5, 2, 7, 4),
		image.Rect(2, 1, 3, 2), image.Rect(4, 1, 5, 2)}, 5, 89)
	scene.Layers[0].Pixels.Pix[0] = .5
	for _, window := range []image.Rectangle{image.Rect(0, 0, 1, 1), image.Rect(5, 2, 7, 4), {}} {
		result, err := psdprecision.RenderRGB16Region(nil, scene, window)
		if result != nil || !errors.Is(err, psdprecision.ErrUnsupported) {
			t.Fatalf("off-canvas external float sample escaped validation: %v", err)
		}
	}
}
