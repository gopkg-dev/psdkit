package psdwarp_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func TestUniformGridSourceSlicesAndMetadataGate(t *testing.T) {
	mutations := []struct {
		name   string
		change func(*psdmodel.Placed)
	}{
		{"missing-axis", func(placed *psdmodel.Placed) { placed.Warp.QuiltSlices = placed.Warp.QuiltSlices[:1] }},
		{"duplicate-axis", func(placed *psdmodel.Placed) {
			placed.Warp.QuiltSlices[1] = placed.Warp.QuiltSlices[0]
		}},
		{"nonuniform", func(placed *psdmodel.Placed) { placed.Warp.QuiltSlices[0].Values[1] += 0.1 }},
		{"stale-source-size", func(placed *psdmodel.Placed) {
			for index := range placed.Warp.QuiltSlices[0].Values {
				placed.Warp.QuiltSlices[0].Values[index] *= 0.5
			}
		}},
		{"unpaired-guard", func(placed *psdmodel.Placed) { placed.Warp.QuiltSlices[0].Values[0] = 0 }},
		{"unknown-guard", func(placed *psdmodel.Placed) {
			placed.Warp.QuiltSlices[0].Values[0] = -0.7
			placed.Warp.QuiltSlices[0].Values[3] = 90.7
		}},
		{"unknown-location", func(placed *psdmodel.Placed) { placed.Warp.QuiltSlices[0].Location = "warp" }},
		{"unknown-class", func(placed *psdmodel.Placed) { placed.Warp.QuiltSlices[0].ClassID = "other" }},
		{"unknown-kind", func(placed *psdmodel.Placed) { placed.Warp.QuiltSlices[0].Kind = "doub" }},
		{"unknown-unit", func(placed *psdmodel.Placed) { placed.Warp.QuiltSlices[0].Unit = "#Pnt" }},
		{"wrong-count", func(placed *psdmodel.Placed) { placed.Warp.QuiltSlices[0].Prefix-- }},
		{"mixed-orders", func(placed *psdmodel.Placed) { placed.Warp.DeformRows = 13 }},
		{"unknown-order", func(placed *psdmodel.Placed) { placed.Warp.DeformColumns = 7 }},
		{"missing-count", func(placed *psdmodel.Placed) { placed.Warp.HasDeformRows = false }},
		{"mesh-truncated", func(placed *psdmodel.Placed) { placed.Warp.MeshPoints = placed.Warp.MeshPoints[:99] }},
		{"mesh-unit", func(placed *psdmodel.Placed) { placed.Warp.MeshUnits[0] = "#Pnt" }},
		{"mesh-class", func(placed *psdmodel.Placed) { placed.Warp.MeshClass = "rationalOther" }},
		{"unsupported-warp", func(placed *psdmodel.Placed) { placed.Warp.Unsupported = []string{"unknown"} }},
		{"unsupported-placement", func(placed *psdmodel.Placed) { placed.Unsupported = []string{"unknown"} }},
		{"preset-value", func(placed *psdmodel.Placed) { placed.Warp.Value = 2 }},
		{"perspective-value", func(placed *psdmodel.Placed) { placed.Warp.Perspective = 2 }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			placed := reviewGridFixture(10)
			mutation.change(placed)
			if grid, err := psdwarp.CompileUniformGrid(nil, placed); grid != nil ||
				!errors.Is(err, psdwarp.ErrUnsupported) {
				t.Fatalf("unqualified metadata returned geometry and %v", err)
			}
		})
	}
	for _, nonfinite := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		placed := reviewGridFixture(13)
		placed.Warp.QuiltSlices[1].Values[2] = nonfinite
		if grid, err := psdwarp.CompileUniformGrid(nil, placed); grid != nil || !errors.Is(err, psdwarp.ErrInvalid) {
			t.Fatalf("nonfinite source slice returned geometry and %v", err)
		}
	}
	for _, order := range []int{10, 13} {
		placed := reviewGridFixture(order)
		for axis, extent := range []float64{90, 78} {
			values := placed.Warp.QuiltSlices[axis].Values
			values[0], values[len(values)-1] = 0, extent
		}
		if _, err := psdwarp.CompileUniformGrid(nil, placed); err != nil {
			t.Fatalf("unguarded source bounds were rejected: %v", err)
		}
		if patch, err := psdwarp.Compile(nil, placed); patch != nil || !errors.Is(err, psdwarp.ErrUnsupported) {
			t.Fatalf("legacy single-patch gate changed: %v", err)
		}
	}
}

func TestUniformGridCertificationRejectsFoldBetweenReferenceSamples(t *testing.T) {
	placed := reviewGridFixture(10)
	const center, depth = 7.0 / 16, 0.0001
	coefficient := [3]float64{center*center - depth, center*center - center - depth,
		(1-center)*(1-center) - depth}
	var positions [4]float64
	scale := 30 / (center*center - center + 1.0/3 - depth)
	for index := range 3 {
		positions[index+1] = positions[index] + scale*coefficient[index]/3
	}
	for row := range 10 {
		for column := range 4 {
			placed.Warp.MeshPoints[row*10+column].X = positions[column]
		}
	}
	for index := range 9 {
		parameter := float64(index) / 8
		if (parameter-center)*(parameter-center)-depth <= 0 {
			t.Fatal("reference proposal samples accidentally include the narrow fold")
		}
	}
	if grid, err := psdwarp.CompileUniformGrid(nil, placed); grid != nil ||
		!errors.Is(err, psdwarp.ErrUnsupported) {
		t.Fatalf("Bernstein proof missed an unsampled negative Jacobian: %v", err)
	}
}

func TestUniformGridRasterWholeSourcePhaseAndClipping(t *testing.T) {
	for _, order := range []int{10, 13} {
		placed := reviewGridFixture(order)
		for row := range order {
			for column := range order {
				if row > 0 && row < order-1 && column > 0 && column < order-1 {
					placed.Warp.MeshPoints[row*order+column].Y += 1.7 * math.Sin(float64(column))
				}
			}
		}
		grid, err := psdwarp.CompileUniformGrid(nil, placed)
		if err != nil {
			t.Fatal(err)
		}
		source := image.NewNRGBA(image.Rect(3, -2, 93, 76))
		for row := source.Rect.Min.Y; row < source.Rect.Max.Y; row++ {
			for column := source.Rect.Min.X; column < source.Rect.Max.X; column++ {
				source.SetNRGBA(column, row, color.NRGBA{R: byte(column), G: byte(row + 2), B: 79, A: 193})
			}
		}
		before := bytes.Clone(source.Pix)
		complete, origin, err := grid.Project(nil, source, image.Rectangle{})
		if err != nil {
			t.Fatal(err)
		}
		canvas := image.Rect(26, 3, 82, 57)
		clipped, clippedOrigin, err := grid.Project(nil, source, canvas)
		if err != nil || clippedOrigin != canvas.Min || clipped.Rect.Size() != canvas.Size() {
			t.Fatalf("invalid clipped projection: %v, %v", clippedOrigin, err)
		}
		for row := range clipped.Rect.Dy() {
			for column := range clipped.Rect.Dx() {
				actual := clipped.NRGBAAt(column, row)
				expected := complete.NRGBAAt(column+clippedOrigin.X-origin.X, row+clippedOrigin.Y-origin.Y)
				if actual != expected || actual.A != 193 {
					t.Fatalf("global phase or seam alpha changed at %d,%d: %v vs %v", column, row, actual, expected)
				}
			}
		}
		if !bytes.Equal(source.Pix, before) {
			t.Fatal("projection modified source pixels")
		}
		bounds, err := grid.Bounds(canvas)
		if err != nil || bounds != canvas {
			t.Fatalf("clipped conservative bounds differ: %v, %v", bounds, err)
		}
	}
}

func TestUniformGridInvalidInputsAndCompileLimits(t *testing.T) {
	for _, options := range []psdwarp.Options{{MaxBytes: 1}, {MaxBytes: 1024 + 9*1024},
		{MaxIterations: 129}, {MaxPixels: -1}} {
		if grid, err := psdwarp.CompileUniformGrid(nil, reviewGridFixture(10), options); grid != nil ||
			!errors.Is(err, psdwarp.ErrLimit) {
			t.Fatalf("compile budget returned geometry and %v", err)
		}
	}
	duplicate, err := psdwarp.CompileUniformGrid(nil, reviewGridFixture(13), psdwarp.Options{}, psdwarp.Options{})
	if duplicate != nil || !errors.Is(err, psdwarp.ErrInvalid) {
		t.Fatalf("duplicate options returned geometry and %v", err)
	}
	grid, err := psdwarp.CompileUniformGrid(nil, reviewGridFixture(13))
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []*image.NRGBA{nil, {}, {Rect: image.Rect(0, 0, 2, 2), Stride: 8, Pix: make([]byte, 15)}} {
		if result, _, err := grid.Project(nil, source, image.Rectangle{}); result != nil ||
			!errors.Is(err, psdwarp.ErrInvalid) {
			t.Fatalf("malformed source returned pixels and %v", err)
		}
	}
	for _, parameter := range [][2]float64{{-0.1, 0}, {0, 1.1}, {math.NaN(), 0}, {0, math.Inf(1)}} {
		if _, err := grid.Forward(parameter[0], parameter[1]); !errors.Is(err, psdwarp.ErrInvalid) {
			t.Fatalf("invalid whole-source parameter returned %v", err)
		}
	}
	if _, _, inside, err := grid.Inverse(context.Background(), psdmodel.Point{X: -1000, Y: 3000}); err != nil || inside {
		t.Fatalf("exterior inverse differs: inside=%v, %v", inside, err)
	}
	var absent *psdwarp.UniformGrid
	if _, err := absent.Bounds(image.Rectangle{}); !errors.Is(err, psdwarp.ErrInvalid) {
		t.Fatalf("nil geometry bounds returned %v", err)
	}
}
