package psdscene

import (
	"bytes"
	"context"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func TestSceneIdentityAndLightnessFiltersRequireNoApproximation(t *testing.T) {
	bounds := image.Rect(-2, 3, 0, 4)
	source := image.NewNRGBA(bounds)
	copy(source.Pix, []byte{255, 128, 0, 128, 89, 166, 157, 255})
	stack := psdfilter.Stack{Enabled: true, Filters: []psdfilter.Filter{
		{Kind: psdfilter.GaussianBlur, Enabled: true, Opacity: .25, BlendMode: "norm"},
		{Kind: psdfilter.SurfaceBlur, Enabled: true, Radius: 3, Opacity: 1, BlendMode: "norm"},
		{Kind: psdfilter.SmartSharpen, Enabled: true, Radius: 2, BlurModel: "lens-blur", Opacity: 1, BlendMode: "norm"},
		{Kind: psdfilter.HueSaturation, Enabled: true, Opacity: 1, BlendMode: "norm",
			Adjustment: &psdadjust.Adjustment{Kind: psdadjust.HueSaturation,
				HueSaturation: &psdadjust.HueSaturationParams{Master: psdadjust.HueSaturationValues{Lightness: 51}}}},
	}}
	options := psdfilter.Options{}
	halo, err := tileFilterHalo(&sceneSmartFilter{operation: stack}, &options)
	if err != nil || halo != 0 {
		t.Fatalf("default exact filter stack has nonzero halo: %d, %v", halo, err)
	}
	for _, regional := range []bool{false, true} {
		layer := &psdmodel.Layer{Pixels: source, Bounds: bounds}
		engine := renderer{working: int64(len(source.Pix)),
			options: Options{SmartFilters: &options, MaxWorkingBytes: 1 << 20}}
		filters := &sceneSmartFilter{view: &psdfile.SmartFilterStack{}, operation: stack}
		if regional {
			filters.opaqueFacts = []bool{false, false, false, false}
		}
		if err := engine.applySmartFilters(context.Background(), nil, layer, filters, bounds); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(layer.Pixels.Pix, []byte{255, 193, 130, 128, 174, 211, 207, 255}) ||
			engine.working != int64(len(layer.Pixels.Pix)) {
			t.Fatalf("default Scene filter transfer or retained accounting differs: %v", layer.Pixels.Pix)
		}
	}
}

func TestSceneIdentityFiltersDoNotAcceptUnknownActiveParameters(t *testing.T) {
	placement := smartFilterPlacement(100)
	stack := placement.Descriptor.Entries[0].Value.(*psdfile.Descriptor)
	items := stack.Entries[1].Value.([]psdfile.DescriptorValue)
	item := items[0].Value.(*psdfile.Descriptor)
	parameters := item.Entries[2].Value.(*psdfile.Descriptor)
	parameters.Entries[0].Value = psdfile.DescriptorUnit{Unit: "#Pxl", Number: float64(0), Width: 64}
	parameters.Entries = append(parameters.Entries,
		psdfile.DescriptorEntry{Key: "unknownAlgorithmControl", Type: "long", Value: int32(1)})
	layer := &psdmodel.Layer{Placed: &psdmodel.Placed{Unsupported: []string{"暂不支持智能滤镜"}}}
	engine := renderer{options: Options{MaxWorkingBytes: 1 << 20, SmartFilters: &psdfilter.Options{}}}
	filters, err := engine.prepareSmartFilters(context.Background(), placement, layer)
	if filters != nil || !errors.Is(err, psdfile.ErrUnsupported) || len(layer.Placed.Unsupported) != 1 {
		t.Fatalf("zero radius bypassed active unknown semantics or changed blockers: %v", err)
	}
}
