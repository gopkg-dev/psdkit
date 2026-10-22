package psdscene

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func extendedReviewFilterOptions() psdfilter.Options {
	return psdfilter.Options{
		ApproximateGaussian: true, ApproximateSurfaceBlur: true, ApproximateSmartSharpen: true,
		ApproximateBrightnessContrast: true, ApproximateHueSaturation: true,
	}
}

func extendedReviewFilterStack() psdfilter.Stack {
	hue := &psdadjust.HueSaturationParams{}
	hue.Ranges[4] = psdadjust.HueSaturationRange{
		Bounds: [4]int16{195, 225, 255, 285}, Values: psdadjust.HueSaturationValues{Saturation: -100},
	}
	return psdfilter.Stack{Enabled: true, Filters: []psdfilter.Filter{
		{Kind: psdfilter.SurfaceBlur, Enabled: true, Radius: 3, Threshold: 5, Opacity: 1, BlendMode: "Nrml"},
		{Kind: psdfilter.BrightnessContrast, Enabled: true, Opacity: .61, BlendMode: "Nrml",
			Adjustment: &psdadjust.Adjustment{Kind: psdadjust.BrightnessContrast,
				BrightnessContrast: &psdadjust.BrightnessContrastParams{Brightness: 13, Contrast: 21}}},
		{Kind: psdfilter.SmartSharpen, Enabled: true, Radius: 1.9, Amount: 3.76, NoiseReduction: .22,
			BlurModel: "lens-blur", Opacity: .43, BlendMode: "Nrml"},
		{Kind: psdfilter.HueSaturation, Enabled: true, Opacity: .78, BlendMode: "Nrml",
			Adjustment: &psdadjust.Adjustment{Kind: psdadjust.HueSaturation, HueSaturation: hue}},
		{Kind: psdfilter.GaussianBlur, Enabled: true, Radius: 1, Opacity: 1, BlendMode: "Nrml"},
	}}
}

func extendedReviewPixels(bounds image.Rectangle) *image.NRGBA {
	pixels := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			alpha := uint8(255)
			if (x*7+y*11)%31 == 0 {
				alpha = uint8((x*13 + y*17) % 255)
			}
			pixels.SetNRGBA(x, y, color.NRGBA{R: uint8(x*5 + y*3), G: uint8(x*7 + y*11),
				B: uint8(x*13 + y*19), A: alpha})
		}
	}
	return pixels
}

func extendedReviewCopy(source *image.NRGBA, bounds image.Rectangle) *image.NRGBA {
	result := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		from, to := source.PixOffset(bounds.Min.X, y), result.PixOffset(bounds.Min.X, y)
		copy(result.Pix[to:to+bounds.Dx()*4], source.Pix[from:from+bounds.Dx()*4])
	}
	return result
}

func TestExtendedFilterReviewColorRadiusZeroRunsAndPreservesAlpha(t *testing.T) {
	source := extendedReviewPixels(image.Rect(-9, 13, 19, 32))
	before := bytes.Clone(source.Pix)
	stack := extendedReviewFilterStack()
	stack.Filters = []psdfilter.Filter{stack.Filters[1], stack.Filters[3]}
	options := extendedReviewFilterOptions()
	halo, err := tileFilterHalo(&sceneSmartFilter{operation: stack}, &options)
	if err != nil || halo != 0 {
		t.Fatalf("point controls incorrectly need spatial support: halo=%d error=%v", halo, err)
	}
	filtered, err := psdfilter.Apply(nil, source, stack, options)
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	for position := 0; position < len(source.Pix); position += 4 {
		if source.Pix[position+3] != filtered.Pix[position+3] {
			t.Fatal("point controls changed source alpha")
		}
		changed = changed || !bytes.Equal(source.Pix[position:position+3], filtered.Pix[position:position+3])
	}
	if !changed || !bytes.Equal(before, source.Pix) || filtered.Rect != source.Rect {
		t.Fatal("radius-zero color controls were skipped or changed source ownership/coordinates")
	}
}

func TestExtendedFilterReviewFullROIWithFilterAndPixelMasks(t *testing.T) {
	canvas := image.Rect(0, 0, 96, 68)
	source := extendedReviewPixels(canvas)
	before := bytes.Clone(source.Pix)
	stack, options := extendedReviewFilterStack(), extendedReviewFilterOptions()
	facts := make([]bool, len(stack.Filters))
	current := source
	for index, operation := range stack.Filters {
		facts[index] = current.Opaque()
		var err error
		current, err = psdfilter.Apply(nil, current,
			psdfilter.Stack{Enabled: true, Filters: []psdfilter.Filter{operation}}, options)
		if err != nil {
			t.Fatal(err)
		}
	}
	halo, err := tileFilterHalo(&sceneSmartFilter{operation: stack}, &options)
	if err != nil {
		t.Fatal(err)
	}
	render := func(window image.Rectangle, regional bool) *image.NRGBA {
		layer := &psdmodel.Layer{Pixels: extendedReviewCopy(source, window), Bounds: window}
		engine := renderer{options: Options{MaxWorkingBytes: 16 << 20, SmartFilters: &options},
			working: int64(len(layer.Pixels.Pix))}
		filters := &sceneSmartFilter{operation: stack, view: &psdfile.SmartFilterStack{}}
		if regional {
			filters.opaqueFacts = facts
		}
		if err := engine.applySmartFilters(context.Background(), nil, layer, filters, window); err != nil {
			t.Fatal(err)
		}
		if engine.working != int64(len(layer.Pixels.Pix)) {
			t.Fatalf("filter pipeline leaked retained-byte accounting: %d", engine.working)
		}
		for y := window.Min.Y; y < window.Max.Y; y++ {
			for x := window.Min.X; x < window.Max.X; x++ {
				position := layer.Pixels.PixOffset(x, y)
				original := source.PixOffset(x, y)
				weight := float64((x*3+y*5)%11) / 10
				blendSmartFilterMask(layer.Pixels.Pix[position:position+4], source.Pix[original:original+4], weight)
			}
		}
		maskPixels := image.NewNRGBA(canvas)
		for y := canvas.Min.Y; y < canvas.Max.Y; y++ {
			for x := canvas.Min.X; x < canvas.Max.X; x++ {
				maskPixels.SetNRGBA(x, y, color.NRGBA{R: uint8(x*19 + y*23), A: 255})
			}
		}
		density := uint8(173)
		layer.Mask = &psdmodel.Mask{Bounds: canvas, Pixels: maskPixels, Density: &density}
		layer.BlendMode = "norm"
		document := &psdmodel.Document{Width: canvas.Dx(), Height: canvas.Dy(), Layers: []*psdmodel.Layer{layer}}
		result, err := psdcompose.RenderRegion(nil, document, window)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	full := render(canvas, false)
	for _, interior := range []image.Rectangle{
		image.Rect(1, 3, 17, 19), image.Rect(43, 29, 58, 42), image.Rect(83, 57, 95, 68),
	} {
		window := interior.Inset(-halo).Intersect(canvas)
		regional := render(window, true)
		for y := interior.Min.Y; y < interior.Max.Y; y++ {
			for x := interior.Min.X; x < interior.Max.X; x++ {
				if actual, expected := regional.NRGBAAt(x, y), full.NRGBAAt(x, y); actual != expected {
					t.Fatalf("mixed spatial/color stack and masks introduced an ROI seam at (%d,%d): %v != %v",
						x, y, actual, expected)
				}
			}
		}
	}
	if !bytes.Equal(before, source.Pix) {
		t.Fatal("filter or mask stages mutated source artwork")
	}
}

func TestExtendedFilterReviewScenePeakBudgetIsAtomic(t *testing.T) {
	canvas := image.Rect(0, 0, 24, 16)
	stack, options := extendedReviewFilterStack(), extendedReviewFilterOptions()
	input := extendedReviewPixels(canvas)
	estimate, err := psdfilter.WorkingEstimate(input, stack, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, deficit := range []int64{0, 1} {
		layer := &psdmodel.Layer{Pixels: extendedReviewCopy(input, canvas), Bounds: canvas}
		before := bytes.Clone(layer.Pixels.Pix)
		original := layer.Pixels
		engine := renderer{options: Options{SmartFilters: &options,
			MaxWorkingBytes: int64(len(layer.Pixels.Pix))*2 + estimate.WorkingBytes - deficit},
			working: int64(len(layer.Pixels.Pix))}
		err := engine.applySmartFilters(context.Background(), nil, layer,
			&sceneSmartFilter{operation: stack, view: &psdfile.SmartFilterStack{}}, canvas)
		if deficit == 0 {
			if err != nil {
				t.Fatalf("exact peak budget was rejected: %v", err)
			}
			continue
		}
		if !errors.Is(err, psdfile.ErrLimit) || layer.Pixels != original || !bytes.Equal(before, layer.Pixels.Pix) {
			t.Fatalf("insufficient extended-filter budget committed partial pixels: %v", err)
		}
	}
}

func TestExtendedFilterReviewOpacityFactsIncludeMixedRadiusZeroPrefixes(t *testing.T) {
	canvas := image.Rect(0, 0, 96, 64)
	prototype := extendedReviewPixels(canvas)
	for position := 3; position < len(prototype.Pix); position += 4 {
		prototype.Pix[position] = 255
	}
	partial := prototype.NRGBAAt(70, 36)
	partial.A = 254
	prototype.SetNRGBA(70, 36, partial)
	stack, options := extendedReviewFilterStack(), extendedReviewFilterOptions()
	stack.Filters = append(stack.Filters, stack.Filters[len(stack.Filters)-1])
	layer := &psdmodel.Layer{Placed: &psdmodel.Placed{Width: 96, Height: 64, Type: "raster",
		Transform: []float64{0, 0, 96, 0, 96, 64, 0, 64}}}
	engine := rgbTileRenderer{source: &psdfile.Document{Header: psdfile.Header{Width: 96, Height: 64}},
		settings: TileOptions{MaxWorkingBytes: 16 << 20, SmartFilters: &options}}
	facts, err := engine.filterOpacityFacts(context.Background(), layer, prototype,
		&sceneSmartFilter{operation: stack}, canvas)
	if err != nil || len(facts) != len(stack.Filters) || !slices.Equal(facts[4:], []bool{false, true}) {
		t.Fatalf("Gaussian complete-input facts lost the mixed spatial/color prefix: facts=%v error=%v", facts, err)
	}
	full, err := psdfilter.Apply(nil, prototype, stack, options)
	if err != nil {
		t.Fatal(err)
	}
	halo, err := tileFilterHalo(&sceneSmartFilter{operation: stack}, &options)
	if err != nil {
		t.Fatal(err)
	}
	interior := image.Rect(20, 13, 45, 31)
	regional, err := engine.opacityWindow(context.Background(), layer, prototype,
		interior.Inset(-halo).Intersect(canvas), stack, facts)
	if err != nil {
		t.Fatal(err)
	}
	for y := interior.Min.Y; y < interior.Max.Y; y++ {
		for x := interior.Min.X; x < interior.Max.X; x++ {
			if actual, expected := regional.NRGBAAt(x, y), full.NRGBAAt(x, y); actual != expected {
				t.Fatalf("mixed prefix complete-input proof changed ROI pixel at (%d,%d): %v != %v", x, y, actual, expected)
			}
		}
	}
}

func TestExtendedFilterReviewFilterMaskHonorsSmallerSourceDecodeLimit(t *testing.T) {
	var channels []byte
	for _, value := range []uint32{0, 0, 1, 2, 8, 0, 0, 0} {
		channels = binary.BigEndian.AppendUint32(channels, value)
	}
	record := append([]byte{6}, []byte("review")...)
	record = binary.BigEndian.AppendUint32(record, 1)
	record = binary.BigEndian.AppendUint64(record, uint64(len(channels)))
	record = append(record, channels...)
	record = append(record, 1)
	for _, value := range []uint32{0, 0, 1, 2} {
		record = binary.BigEndian.AppendUint32(record, value)
	}
	record = binary.BigEndian.AppendUint64(record, 4)
	record = append(record, 0, 0, 73, 211)
	payload := binary.BigEndian.AppendUint32(nil, 3)
	payload = binary.BigEndian.AppendUint64(payload, uint64(len(record)))
	payload = append(payload, record...)
	for len(record)%4 != 0 {
		payload = append(payload, 0)
		record = append(record, 0)
	}
	source, err := psdfile.Parse(cachedTestSource(artboardTestBlock("FXid", payload)),
		psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	engine := renderer{options: Options{MaxWorkingBytes: 2 << 30}}
	mask, bounds, err := engine.smartFilterMask(context.Background(), source,
		&psdfile.SmartFilterStack{PlacementID: "review", MaskEnable: true}, image.Rect(0, 0, 2, 1))
	if err != nil || mask == nil || bounds != image.Rect(0, 0, 2, 1) ||
		!bytes.Equal(mask.Data, []byte{73, 211}) || engine.working != 2 {
		t.Fatalf("larger scene budget attempted to raise the source's mask decode bound: bounds=%v work=%d error=%v",
			bounds, engine.working, err)
	}
}

func TestExtendedFilterReviewVectorStrokeAppliesOuterCoverageOnce(t *testing.T) {
	mask := &psdvector.Mask{Width: 32, Height: 28, Version: 3,
		Subpaths: []psdvector.Subpath{{Closed: true, Operation: 1}}}
	for _, coordinates := range [][2]float64{{2.25, 3.75}, {26.75, 3.75}, {26.75, 23.25}, {2.25, 23.25}} {
		point := psdvector.Point{X: coordinates[0], Y: coordinates[1]}
		mask.Subpaths[0].Knots = append(mask.Subpaths[0].Knots,
			psdvector.Knot{Preceding: point, Anchor: point, Leaving: point})
	}
	bounds := image.Rect(2, 3, 27, 24)
	style := vectorStrokeStyle{fill: [3]float64{250, 40, 30}, stroke: [3]float64{25, 130, 210}, width: 1.4}
	fullPixels, err := rasterVectorStroke(context.Background(), mask, bounds, style, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	interior := image.Rect(2, 3, 11, 12)
	regional, err := rasterVectorStroke(context.Background(), mask, interior, style, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	for y := interior.Min.Y; y < interior.Max.Y; y++ {
		for x := interior.Min.X; x < interior.Max.X; x++ {
			if regional.NRGBAAt(x, y) != fullPixels.NRGBAAt(x, y) {
				t.Fatalf("fractional inside stroke changed with the ROI at (%d,%d)", x, y)
			}
		}
	}
	if pixel := fullPixels.NRGBAAt(11, 11); pixel != (color.NRGBA{R: 250, G: 40, B: 30, A: 255}) {
		t.Fatalf("deep shape interior was converted into a stroke or pre-masked: %v", pixel)
	}
	outer, err := psdvector.Rasterize(context.Background(), mask, bounds)
	if err != nil {
		t.Fatal(err)
	}
	layer := &psdmodel.Layer{Pixels: fullPixels, Bounds: bounds, VectorMask: mask, BlendMode: "norm"}
	result, err := psdcompose.RenderRegion(nil,
		&psdmodel.Document{Width: 32, Height: 28, Layers: []*psdmodel.Layer{layer}}, bounds)
	if err != nil {
		t.Fatal(err)
	}
	partialEdges := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			alpha := outer.AlphaAt(x, y).A
			if result.NRGBAAt(x, y).A != alpha {
				t.Fatalf("original vector coverage was applied twice at (%d,%d)", x, y)
			}
			if alpha > 0 && alpha < 255 {
				partialEdges++
			}
		}
	}
	if partialEdges == 0 {
		t.Fatal("fractional path did not exercise antialiased outer coverage")
	}
}
