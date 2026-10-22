package psdkit_test

import (
	"context"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func publicUniformGrid(order int) *psdkit.Placed {
	const width, height = 54, 36
	count := (order - 1) / 3
	warp := &psdkit.Warp{ClassID: "quiltWarp", Style: "custom", Rotate: "horizontal",
		UOrder: 4, VOrder: 4, DeformRows: order, DeformColumns: order,
		HasDeformRows: true, HasDeformColumns: true, HasEnvelope: true, Split: true,
		Bounds:    &psdkit.WarpBounds{Right: width, Bottom: height, Pixels: true},
		MeshUnits: [2]string{"#Pxl", "#Pxl"}, MeshClass: "rationalPoint"}
	for row := range order {
		for column := range order {
			warp.MeshPoints = append(warp.MeshPoints, psdkit.Point{
				X: width * float64(column) / float64(order-1),
				Y: height * float64(row) / float64(order-1)})
		}
	}
	for axis, extent := range []float64{width, height} {
		name := []string{"quiltSliceX", "quiltSliceY"}[axis]
		slice := psdkit.WarpSlice{Axis: name, Location: "envelope", ClassID: "UntF", Field: name,
			Kind: "UnFl", Unit: "#Pxl", Prefix: uint32(count + 1)}
		for index := 0; index <= count; index++ {
			slice.Values = append(slice.Values, extent*float64(index)/float64(count))
		}
		warp.QuiltSlices = append(warp.QuiltSlices, slice)
	}
	return &psdkit.Placed{Width: width, Height: height, Warp: warp,
		Transform: []float64{7, -3, 61, -3, 61, 33, 7, 33}}
}

func TestPublicUniformGridGeometryAndOwnedProjection(t *testing.T) {
	for _, order := range []int{10, 13} {
		placed := publicUniformGrid(order)
		grid, err := psdkit.CompileUniformGridWarpGeometry(nil, placed)
		if err != nil {
			t.Fatal(err)
		}
		point, err := grid.Forward(.5, .5)
		if err != nil || point != (psdkit.Point{X: 34, Y: 15}) {
			t.Fatalf("order %d public coordinate mismatch: %+v, %v", order, point, err)
		}
		source := image.NewNRGBA(image.Rect(-4, 9, 50, 45))
		for y := source.Rect.Min.Y; y < source.Rect.Max.Y; y++ {
			for x := source.Rect.Min.X; x < source.Rect.Max.X; x++ {
				source.SetNRGBA(x, y, color.NRGBA{R: 31, G: 117, B: 203, A: 255})
			}
		}
		canvas := image.Rect(0, -8, 72, 40)
		pixels, origin, err := grid.Project(nil, source, canvas)
		if err != nil || pixels.NRGBAAt(34-origin.X, 15-origin.Y) !=
			(color.NRGBA{R: 31, G: 117, B: 203, A: 255}) {
			t.Fatalf("order %d public projection failed: %v", order, err)
		}
		pixels.Pix[0] ^= 255
		if source.NRGBAAt(-4, 9) != (color.NRGBA{R: 31, G: 117, B: 203, A: 255}) {
			t.Fatal("public projection aliases source pixels")
		}
		placed.Warp.MeshPoints[0].X = 900
		again, err := grid.Forward(.5, .5)
		if err != nil || again != point {
			t.Fatalf("compiled public geometry retained caller metadata: %v", err)
		}
	}
}

func TestPublicUniformGridRejectsUnqualifiedSlicesAndCancellation(t *testing.T) {
	placed := publicUniformGrid(13)
	placed.Warp.QuiltSlices[0].Values[1] += .125
	if grid, err := psdkit.CompileUniformGridWarpGeometry(nil, placed); grid != nil ||
		!errors.Is(err, psdwarp.ErrUnsupported) {
		t.Fatalf("public entry accepted nonuniform source slices: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if grid, err := psdkit.CompileUniformGridWarpGeometry(ctx, publicUniformGrid(10)); grid != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("public entry returned canceled geometry: %v", err)
	}
}
