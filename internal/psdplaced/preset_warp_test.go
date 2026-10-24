package psdplaced_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/psdplaced"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func presetPlacement() *psdmodel.Placed {
	return &psdmodel.Placed{
		Width: 8, Height: 8, Transform: []float64{-3, 2, 13, 2, 13, 18, -3, 18},
		Warp: &psdmodel.Warp{
			Style: "warpArcLower", Rotate: "horizontal", Value: 25, HasValue: true,
			UOrder: 4, VOrder: 2, Bounds: &psdmodel.WarpBounds{Right: 8, Bottom: 8, Pixels: true},
		},
	}
}

func TestPresetWarpAdapterRequiresCurrentExplicitMathematicalSelection(t *testing.T) {
	placed := presetPlacement()
	for _, approximate := range []bool{false, true} {
		projection, operationErr := psdplaced.CompileWarp(nil, placed, approximate, psdwarp.Options{})
		if projection != nil || !errors.Is(operationErr, psdwarp.ErrUnsupported) {
			t.Fatalf("generic custom-warp compiler accepted preset with approximate=%t: %v", approximate, operationErr)
		}
	}
	if projection, err := psdplaced.CompilePresetWarp(
		nil, placed, false, psdwarp.Options{},
	); projection != nil || !errors.Is(err, psdwarp.ErrUnsupported) {
		t.Fatalf("preset adapter accepted omitted mathematical selection: %v", err)
	}
	projection, err := psdplaced.CompilePresetWarp(nil, placed, true, psdwarp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	geometry, err := psdwarp.CompilePresetGeometry(nil, placed)
	if err != nil {
		t.Fatal(err)
	}
	if projection.WorkingBytes() != geometry.WorkingBytes() {
		t.Fatal("preset adapter changed retained geometry accounting")
	}
	source := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for row := range 8 {
		for column := range 8 {
			source.SetNRGBA(column, row, color.NRGBA{
				R: byte(column * 31), G: byte(row * 31), B: byte(column*row + 50), A: 255,
			})
		}
	}
	for _, canvas := range []image.Rectangle{{}, image.Rect(0, 4, 10, 13)} {
		bounds, boundsErr := projection.Bounds(canvas)
		expectedBounds, expectedBoundsErr := geometry.Bounds(canvas)
		if boundsErr != nil || expectedBoundsErr != nil || bounds != expectedBounds {
			t.Fatalf("preset adapter changed clipped geometry bounds: %v / %v", boundsErr, expectedBoundsErr)
		}
		output, origin, err := projection.ProjectWithOptions(nil, source, canvas)
		expected, expectedOrigin, expectedErr := geometry.ProjectMathematicalWithOptions(nil, source, canvas)
		if err != nil || expectedErr != nil || origin != expectedOrigin || !bytes.Equal(output.Pix, expected.Pix) {
			t.Fatalf("preset adapter changed explicitly mathematical projection: %v / %v", err, expectedErr)
		}
		if limited, _, err := projection.ProjectWithOptions(nil, source, canvas,
			psdwarp.Options{MaxBytes: projection.WorkingBytes()}); limited != nil || !errors.Is(err, psdwarp.ErrLimit) {
			t.Fatalf("preset adapter reused a generous compile-time projection budget: %v", err)
		}
	}
}

func TestPresetWarpAdapterRetainsCertifiedSchemaAndCancellationGates(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*psdmodel.Placed)
	}{
		{"otherPreset", func(p *psdmodel.Placed) { p.Warp.Style = "warpArc" }},
		{"vertical", func(p *psdmodel.Placed) { p.Warp.Rotate = "vertical" }},
		{"perspective", func(p *psdmodel.Placed) { p.Warp.Perspective = 1 }},
		{"projective", func(p *psdmodel.Placed) { p.Transform[2]++ }},
		{"split", func(p *psdmodel.Placed) { p.Warp.Split = true }},
		{"deformCountWithoutSplit", func(p *psdmodel.Placed) { p.Warp.DeformRows = 10 }},
		{"deformPresenceWithoutSplit", func(p *psdmodel.Placed) { p.Warp.HasDeformColumns = true }},
		{"quiltWithoutSplit", func(p *psdmodel.Placed) {
			p.Warp.QuiltSlices = []psdmodel.WarpSlice{{Axis: "quiltSliceX"}}
		}},
		{"unprovenOrder", func(p *psdmodel.Placed) { p.Warp.VOrder = 4 }},
		{"absentBend", func(p *psdmodel.Placed) { p.Warp.HasValue = false }},
		{"bendBoundary", func(p *psdmodel.Placed) { p.Warp.Value = 100 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			placed := presetPlacement()
			test.edit(placed)
			projection, err := psdplaced.CompilePresetWarp(nil, placed, true, psdwarp.Options{})
			if projection != nil || !errors.Is(err, psdwarp.ErrUnsupported) {
				t.Fatalf("explicit mathematical selection broadened certified preset schema: %v", err)
			}
		})
	}
	if projection, err := psdplaced.CompilePresetWarp(
		nil, nil, true, psdwarp.Options{},
	); projection != nil || !errors.Is(err, psdwarp.ErrInvalid) {
		t.Fatalf("invalid preset returned a non-nil interface: %v", err)
	}
	if projection, err := psdplaced.CompilePresetWarp(
		nil, presetPlacement(), true, psdwarp.Options{MaxBytes: 16},
	); projection != nil || !errors.Is(err, psdwarp.ErrLimit) {
		t.Fatalf("preset geometry limit returned a non-nil interface: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, approximate := range []bool{false, true} {
		projection, err := psdplaced.CompilePresetWarp(ctx, presetPlacement(), approximate, psdwarp.Options{})
		if projection != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled preset adapter ignored context with approximation=%t: %v", approximate, err)
		}
	}
}
