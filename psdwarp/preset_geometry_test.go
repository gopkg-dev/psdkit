package psdwarp_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"image"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func arcLowerFixture(width, height int, bend float64) *psdmodel.Placed {
	return &psdmodel.Placed{Width: width, Height: height,
		Transform: []float64{112, 128, 208, 128, 208, 230, 112, 230},
		Warp: &psdmodel.Warp{Style: "warpArcLower", Rotate: "horizontal", Value: bend, HasValue: true,
			UOrder: 4, VOrder: 2,
			Bounds: &psdmodel.WarpBounds{Right: float64(width), Bottom: float64(height), Pixels: true}}}
}

func nativeArcLowerGeometry(t *testing.T, placed *psdmodel.Placed, handleX, handleY float64) *psdwarp.Patch {
	t.Helper()
	geometry := *placed
	warp := *placed.Warp
	warp.Style, warp.Value = "custom", 0
	warp.UOrder, warp.VOrder, warp.HasEnvelope = 4, 4, true
	warp.MeshPoints = make([]psdmodel.Point, 16)
	width, height := float64(placed.Width), float64(placed.Height)
	// Native refreshed Photoshop fixtures store these exact linearly elevated 4x2 controls.
	for row := range 4 {
		fraction := float64(row) / 3
		for column := range 4 {
			x, y := width*float64(column)/3, height
			if column == 1 {
				x, y = handleX, height+handleY
			}
			if column == 2 {
				x, y = width-handleX, height+handleY
			}
			warp.MeshPoints[row*4+column] = psdmodel.Point{
				X: width*float64(column)/3*(1-fraction) + x*fraction, Y: y * fraction}
		}
	}
	geometry.Warp = &warp
	patch, err := psdwarp.Compile(nil, &geometry)
	if err != nil {
		t.Fatal(err)
	}
	return patch
}

func TestPresetGeometryMatchesIndependentNativeArcLowerControls(t *testing.T) {
	for _, test := range []struct {
		name                   string
		width, height          int
		bend, handleX, handleY float64
	}{
		{"positive25", 96, 64, 25, 30.73388390139061, 12.73039122030451},
		{"realCorpus68.8", 96, 64, 68.8, 20.483424248189106, 38.39429249552329},
		{"negative68.8", 96, 64, -68.8, 20.483424248189106, -38.39429249552329},
		{"tall37.5", 37, 83, 37.5, 11.198429791505951, 7.48255135165476},
	} {
		t.Run(test.name, func(t *testing.T) {
			placed := arcLowerFixture(test.width, test.height, test.bend)
			before, err := json.Marshal(placed, json.Deterministic(true))
			if err != nil {
				t.Fatal(err)
			}
			native := nativeArcLowerGeometry(t, placed, test.handleX, test.handleY)
			geometry, err := psdrender.CompilePresetWarpGeometry(nil, &psdmodel.Layer{Placed: placed})
			if err != nil {
				t.Fatal(err)
			}
			for row := range 9 {
				for column := range 9 {
					u, v := float64(column)/8, float64(row)/8
					actual, err := geometry.Forward(u, v)
					if err != nil {
						t.Fatal(err)
					}
					expected, err := native.Forward(u, v)
					if err != nil || math.Hypot(actual.X-expected.X, actual.Y-expected.Y) > 3e-6 {
						t.Fatalf("native control surface differs at %g,%g: %v / %v, %v", u, v, actual, expected, err)
					}
					gotU, gotV, inside, err := geometry.Inverse(nil, actual)
					if err != nil || !inside || math.Abs(gotU-u) > 1e-7 || math.Abs(gotV-v) > 1e-7 {
						t.Fatalf("certified inverse differs: %g,%g,%t,%v", gotU, gotV, inside, err)
					}
				}
			}
			after, err := json.Marshal(placed, json.Deterministic(true))
			if err != nil || string(before) != string(after) {
				t.Fatalf("geometry compilation mutated native parameters: %v", err)
			}
			_, _, projectErr := psdrender.ProjectWarp(nil, &psdmodel.Layer{Placed: placed},
				image.NewNRGBA(image.Rect(0, 0, test.width, test.height)), image.Rectangle{})
			if !errors.Is(projectErr, psdwarp.ErrUnsupported) {
				t.Fatalf("unverified preset pixels entered the custom renderer: %v", projectErr)
			}
		})
	}
}

func TestPresetGeometryOwnsPlacementAndBoundsAndHonorsBudgets(t *testing.T) {
	placed := arcLowerFixture(96, 64, 68.8)
	geometry, err := psdwarp.CompilePresetGeometry(nil, placed)
	if err != nil {
		t.Fatal(err)
	}
	before, err := geometry.Forward(.3, .7)
	if err != nil {
		t.Fatal(err)
	}
	whole, err := geometry.Bounds(image.Rectangle{})
	if err != nil {
		t.Fatal(err)
	}
	canvas := image.Rect(140, 140, 180, 220)
	clipped, err := geometry.Bounds(canvas)
	if err != nil || clipped != whole.Intersect(canvas) {
		t.Fatalf("ROI changed complete geometry bounds: %v, %v", clipped, err)
	}
	if _, err := psdwarp.CompilePresetGeometry(nil, placed,
		psdwarp.Options{MaxBytes: geometry.WorkingBytes() - 1}); !errors.Is(err, psdwarp.ErrLimit) {
		t.Fatalf("retained geometry budget was not charged: %v", err)
	}
	if _, err := psdwarp.CompilePresetGeometry(nil, placed,
		psdwarp.Options{MaxBytes: geometry.WorkingBytes() * 2}); err != nil {
		t.Fatalf("adequate retained and compilation-scratch budget failed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := psdwarp.CompilePresetGeometry(ctx, placed); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled geometry compile returned a result: %v", err)
	}
	if _, _, _, err := geometry.Inverse(ctx, before); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled inverse ignored context: %v", err)
	}
	placed.Transform[0], placed.Warp.Value, placed.Warp.Bounds.Left = 999, -25, 123
	after, err := geometry.Forward(.3, .7)
	if err != nil || after != before {
		t.Fatalf("compiled geometry aliases caller-owned placement: %v, %v", after, err)
	}
}

func TestPresetGeometryRejectsUnprovenNativeSchemas(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*psdmodel.Placed)
	}{
		{"cylinder", func(p *psdmodel.Placed) { p.Warp.Style = "warpCylinder" }},
		{"vertical", func(p *psdmodel.Placed) { p.Warp.Rotate = "vertical" }},
		{"absentBend", func(p *psdmodel.Placed) { p.Warp.HasValue = false }},
		{"extraScalarList", func(p *psdmodel.Placed) { p.Warp.HasValues = true }},
		{"perspective", func(p *psdmodel.Placed) { p.Warp.Perspective = 7 }},
		{"split", func(p *psdmodel.Placed) { p.Warp.Split = true }},
		{"customEnvelope", func(p *psdmodel.Placed) { p.Warp.HasEnvelope = true }},
		{"unknownProperty", func(p *psdmodel.Placed) { p.Warp.Unsupported = []string{"unknown native field"} }},
		{"unknownUnits", func(p *psdmodel.Placed) { p.Warp.Bounds.Pixels = false }},
		{"wrongOrder", func(p *psdmodel.Placed) { p.Warp.VOrder = 4 }},
		{"fullHalfCircle", func(p *psdmodel.Placed) { p.Warp.Value = 100 }},
		{"nonfinite", func(p *psdmodel.Placed) { p.Warp.Value = math.NaN() }},
		{"fold", func(p *psdmodel.Placed) {
			p.Height, p.Warp.Bounds.Bottom, p.Warp.Value = 10, 10, -68.8
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			placed := arcLowerFixture(96, 64, 68.8)
			test.edit(placed)
			if geometry, err := psdwarp.CompilePresetGeometry(nil, placed); geometry != nil || err == nil {
				t.Fatal("unsupported or uncertifiable native preset returned geometry")
			}
		})
	}
}
