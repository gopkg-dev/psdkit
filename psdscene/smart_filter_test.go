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
)

func smartFilterPlacement(opacity float64) psdfile.SmartObjectRecord {
	item := &psdfile.Descriptor{Entries: []psdfile.DescriptorEntry{
		{Key: "enab", Type: "bool", Value: true},
		{Key: "filterID", Type: "long", Value: int32(0x47736e42)},
		{Key: "Fltr", Type: "Objc", Value: &psdfile.Descriptor{Entries: []psdfile.DescriptorEntry{
			{Key: "Rds ", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Pxl", Number: float64(1), Width: 64}},
		}}},
		{Key: "blendOptions", Type: "Objc", Value: &psdfile.Descriptor{Entries: []psdfile.DescriptorEntry{
			{Key: "Opct", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Prc", Number: opacity, Width: 64}},
		}}},
	}}
	stack := &psdfile.Descriptor{Entries: []psdfile.DescriptorEntry{
		{Key: "enab", Type: "bool", Value: true},
		{Key: "filterFXList", Type: "VlLs", Value: []psdfile.DescriptorValue{{Type: "Objc", Value: item}}},
	}}
	return psdfile.SmartObjectRecord{Descriptor: &psdfile.Descriptor{Entries: []psdfile.DescriptorEntry{
		{Key: "filterFX", Type: "Objc", Value: stack},
	}}}
}

func TestSceneSmartFilterPolicyPreservesUnrelatedBlockersAndZeroOpacity(t *testing.T) {
	placement := smartFilterPlacement(100)
	makeLayer := func() *psdmodel.Layer {
		return &psdmodel.Layer{Placed: &psdmodel.Placed{Unsupported: []string{
			"暂不支持智能滤镜", "independent unsupported warp",
		}}}
	}
	engine := renderer{options: Options{MaxWorkingBytes: 1 << 20}}
	layer := makeLayer()
	if filters, err := engine.prepareSmartFilters(context.Background(), placement, layer); filters != nil ||
		!errors.Is(err, psdfile.ErrUnsupported) || len(layer.Placed.Unsupported) != 2 {
		t.Fatalf("implicit policy accepted or mutated source blockers: %v", err)
	}
	engine.options.SmartFilters = &psdfilter.Options{ApproximateGaussian: true}
	filters, err := engine.prepareSmartFilters(context.Background(), placement, layer)
	if err != nil || filters == nil || len(layer.Placed.Unsupported) != 1 ||
		layer.Placed.Unsupported[0] != "independent unsupported warp" {
		t.Fatalf("exact filter blocker selection failed: %v", err)
	}
	engine = renderer{options: Options{MaxWorkingBytes: 1 << 20}}
	layer = makeLayer()
	filters, err = engine.prepareSmartFilters(context.Background(), smartFilterPlacement(0), layer)
	if err != nil || filters != nil || len(layer.Placed.Unsupported) != 1 {
		t.Fatalf("zero-opacity filter incorrectly required a live policy: %v", err)
	}
}

func TestSceneSmartFilterRunsAfterProjectionOnFullCanvasWithPeakBudget(t *testing.T) {
	pixels := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for y := range 2 {
		for x := range 2 {
			pixels.SetNRGBA(x, y, color.NRGBA{220, 40, 10, 255})
		}
	}
	before := bytes.Clone(pixels.Pix)
	canvas := image.Rect(0, 0, 7, 5)
	stack := psdfilter.Stack{Enabled: true, Filters: []psdfilter.Filter{
		{Enabled: true, Kind: psdfilter.GaussianBlur, Radius: 1, Opacity: 1, BlendMode: "norm"},
	}}
	options := psdfilter.Options{ApproximateGaussian: true}
	estimate, err := psdfilter.WorkingEstimate(image.NewNRGBA(canvas), stack, options)
	if err != nil {
		t.Fatal(err)
	}
	budget := int64(len(pixels.Pix)) + int64(canvas.Dx()*canvas.Dy()*4) + estimate.WorkingBytes
	for _, amount := range []int64{budget, budget - 1} {
		layer := &psdmodel.Layer{Pixels: pixels, Bounds: image.Rect(2, 1, 4, 3)}
		engine := renderer{working: int64(len(pixels.Pix)),
			options: Options{MaxWorkingBytes: amount, SmartFilters: &options}}
		err := engine.applySmartFilters(context.Background(), nil, layer,
			&sceneSmartFilter{view: &psdfile.SmartFilterStack{}, operation: stack}, canvas)
		if amount < budget {
			if !errors.Is(err, psdfile.ErrLimit) || layer.Pixels != pixels || layer.Bounds != image.Rect(2, 1, 4, 3) {
				t.Fatalf("peak deficit committed a partial filter: %v", err)
			}
			continue
		}
		if err != nil || layer.Bounds != canvas || layer.Pixels.Rect != canvas {
			t.Fatalf("full-canvas filtering failed: %v", err)
		}
		if layer.Pixels.NRGBAAt(1, 1).A == 0 || layer.Pixels.NRGBAAt(4, 1).A == 0 ||
			layer.Pixels.NRGBAAt(2, 1).A == 255 {
			t.Fatal("filter was clipped to its projection rectangle or skipped")
		}
		if engine.working != int64(len(layer.Pixels.Pix)) {
			t.Fatalf("retained allocation ledger differs: %d", engine.working)
		}
	}
	if !bytes.Equal(before, pixels.Pix) {
		t.Fatal("filter mutated caller-owned projection")
	}
}

func TestSceneSmartFilterMaskInterpolatesPremultipliedPixels(t *testing.T) {
	filtered := []byte{255, 0, 0, 128}
	original := []byte{0, 0, 255, 64}
	blendSmartFilterMask(filtered, original, 0.5)
	// Half red coverage and half blue coverage give 2:1 straight colors and 96 alpha.
	if !bytes.Equal(filtered, []byte{170, 0, 85, 96}) {
		t.Fatalf("mask interpolated straight color: %v", filtered)
	}
	if !bytes.Equal(original, []byte{0, 0, 255, 64}) {
		t.Fatal("mask changed original pixels")
	}
	if !errors.Is(sceneFilterError(psdfilter.ErrUnsupported), psdfilter.ErrUnsupported) ||
		!errors.Is(sceneFilterError(psdfilter.ErrLimit), psdfile.ErrLimit) {
		t.Fatal("filter error category or cause lost")
	}
}
