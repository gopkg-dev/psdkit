package psdscene

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func projectiveSceneWarp() *psdmodel.Placed {
	width, height := 16, 24
	warp := &psdmodel.Warp{Style: "custom", Rotate: "horizontal", UOrder: 4, VOrder: 4, HasEnvelope: true,
		Bounds: &psdmodel.WarpBounds{Right: float64(width), Bottom: float64(height), Pixels: true}}
	for row := range 4 {
		for column := range 4 {
			warp.MeshPoints = append(warp.MeshPoints, psdmodel.Point{
				X: float64(column*width) / 3, Y: float64(row*height) / 3})
		}
	}
	return &psdmodel.Placed{Width: width, Height: height, Warp: warp,
		Transform:          []float64{3, 4, 19, 4, 19, 28, 3, 28},
		NonAffineTransform: []float64{3, 4, 25, 3, 23, 33, 2, 29}}
}

func projectiveScenePixels(placed *psdmodel.Placed) *image.NRGBA {
	result := image.NewNRGBA(image.Rect(-5, 7, -5+placed.Width, 7+placed.Height))
	for y := result.Rect.Min.Y; y < result.Rect.Max.Y; y++ {
		for x := result.Rect.Min.X; x < result.Rect.Max.X; x++ {
			result.SetNRGBA(x, y, color.NRGBA{R: byte(x*23 + y*11), G: byte(x*13 + y*7),
				B: byte(x*5 + y*17), A: byte(64 + (x+y+40)%191)})
		}
	}
	return result
}

func TestSceneCustomWarpExplicitPolicyDefaultGuardAndOwnedROI(t *testing.T) {
	placed := projectiveSceneWarp()
	defaultPatch, defaultErr := compileSceneWarp(nil, placed, false, psdwarp.Options{})
	if defaultPatch != nil || !errors.Is(defaultErr, psdwarp.ErrUnsupported) {
		t.Fatalf("default accepted unqualified native projective warp: %v", defaultErr)
	}
	patch, err := compileSceneWarp(nil, placed, true, psdwarp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	source := projectiveScenePixels(placed)
	before := bytes.Clone(source.Pix)
	canvas := image.Rect(0, 0, 40, 40)
	full, origin, err := projectSceneWarp(nil, patch, source, canvas, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	fullBounds := image.Rectangle{Min: origin, Max: origin.Add(full.Rect.Size())}
	window := image.Rect(9, 12, 19, 25)
	region, regionOrigin, err := projectSceneWarp(nil, patch, source, window, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	regionBounds := image.Rectangle{Min: regionOrigin, Max: regionOrigin.Add(region.Rect.Size())}
	for y := regionBounds.Min.Y; y < regionBounds.Max.Y; y++ {
		for x := regionBounds.Min.X; x < regionBounds.Max.X; x++ {
			if !image.Pt(x, y).In(fullBounds) ||
				region.NRGBAAt(x-regionOrigin.X, y-regionOrigin.Y) != full.NRGBAAt(x-origin.X, y-origin.Y) {
				t.Fatalf("mathematical full/region projection differs at (%d,%d)", x, y)
			}
		}
	}
	placed.Warp.MeshPoints[0].X = 999
	placed.NonAffineTransform[0] = 999
	again, againOrigin, err := projectSceneWarp(nil, patch, source, canvas, 1<<20)
	if err != nil || againOrigin != origin || !bytes.Equal(again.Pix, full.Pix) {
		t.Fatalf("compiled geometry retained mutable metadata: %v", err)
	}
	region.Pix[0] ^= 255
	if !bytes.Equal(before, source.Pix) {
		t.Fatal("projection changed or aliased source pixels")
	}
}

func TestSceneCustomWarpOpacityWindowUsesExplicitPolicyAndLimits(t *testing.T) {
	placed := projectiveSceneWarp()
	layer := &psdmodel.Layer{Placed: placed}
	source := projectiveScenePixels(placed)
	window := image.Rect(0, 0, 35, 38)
	engine := rgbTileRenderer{settings: TileOptions{MaxWorkingBytes: 1 << 20}}
	defaultOutput, defaultErr := engine.opacityWindow(
		context.Background(), layer, source, window, psdfilter.Stack{}, nil)
	if defaultOutput != nil || !errors.Is(defaultErr, psdfile.ErrUnsupported) {
		t.Fatalf("opacity scan bypassed the default projective guard: %v", defaultErr)
	}
	engine.settings.ApproximateCustomWarp = true
	actual, err := engine.opacityWindow(context.Background(), layer, source, window, psdfilter.Stack{}, nil)
	if err != nil || actual.Rect != window {
		t.Fatalf("explicit opacity scan failed: %v", err)
	}
	patch, err := compileSceneWarp(nil, placed, true, psdwarp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	reference, origin, err := projectSceneWarp(nil, patch, source, window, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for y := window.Min.Y; y < window.Max.Y; y++ {
		for x := window.Min.X; x < window.Max.X; x++ {
			if actual.NRGBAAt(x, y) != reference.NRGBAAt(x-origin.X, y-origin.Y) {
				t.Fatalf("opacity source did not use the same projective policy at (%d,%d)", x, y)
			}
		}
	}
	for _, budget := range []int64{0, -1, patch.WorkingBytes()} {
		output, _, operationErr := projectSceneWarp(nil, patch, source, window, budget)
		if output != nil || !errors.Is(operationErr, psdwarp.ErrLimit) {
			t.Fatalf("scene projection budget bypassed: %d, %v", budget, operationErr)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceledOutput, canceledErr := engine.opacityWindow(ctx, layer, source, window, psdfilter.Stack{}, nil)
	if canceledOutput != nil || !errors.Is(canceledErr, context.Canceled) {
		t.Fatalf("opacity scan returned canceled pixels: %v", canceledErr)
	}
	placed.Warp.Split = true
	quiltPatch, quiltErr := compileSceneWarp(nil, placed, true, psdwarp.Options{})
	if quiltPatch != nil || !errors.Is(quiltErr, psdwarp.ErrUnsupported) {
		t.Fatalf("explicit model ignored unsupported quilt geometry: %v", quiltErr)
	}
}
