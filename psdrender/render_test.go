package psdrender

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"reflect"
	"testing"
)

func solid(width, height int, rgba ...uint8) *image.NRGBA {
	pixels := image.NewNRGBA(image.Rect(0, 0, width, height))
	for offset := 0; offset < len(pixels.Pix); offset += 4 {
		copy(pixels.Pix[offset:offset+4], rgba)
	}
	return pixels
}

func values(width int, pixels ...uint8) *image.NRGBA {
	return &image.NRGBA{Pix: append([]uint8{}, pixels...), Stride: width * 4, Rect: image.Rect(0, 0, width, len(pixels)/(width*4))}
}

func pixel(t *testing.T, pixels *image.NRGBA, x, y int, want ...uint8) {
	t.Helper()
	index := y*pixels.Stride + x*4
	if !bytes.Equal(pixels.Pix[index:index+4], want) {
		t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, pixels.Pix[index:index+4], want)
	}
}

func raster(pixels *image.NRGBA, left, top int) *Layer {
	return &Layer{Bounds: image.Rect(left, top, left+pixels.Rect.Dx(), top+pixels.Rect.Dy()), Pixels: pixels}
}

func placed(id string, left int, rgba ...uint8) *Layer {
	layer := raster(solid(2, 2, rgba...), left, 0)
	layer.Name = id
	layer.Placed = &Placed{ID: id, Type: "raster", Width: 2, Height: 2,
		Transform: []float64{float64(left), 0, float64(left + 2), 0, float64(left + 2), 2, float64(left), 2}}
	return layer
}

func fixture() *Document {
	first := placed("a", 0, 255, 0, 0, 255)
	first.Mask = &Mask{Bounds: image.Rect(0, 0, 2, 2), DefaultColor: new(uint8(0)),
		Pixels: values(2, 0, 0, 0, 255, 255, 255, 255, 255, 0, 0, 0, 255, 255, 255, 255, 255)}
	return &Document{Width: 6, Height: 2, Composite: solid(6, 2, 20, 40, 60, 255), Layers: []*Layer{
		raster(solid(6, 2, 20, 40, 60, 255), 0, 0), first, placed("b", 2, 0, 200, 0, 255),
		placed("a", 4, 255, 0, 0, 255), raster(solid(1, 1, 0, 0, 0, 128), 5, 0),
	}}
}

func TestClampMatchesUint8ClampedArray(t *testing.T) {
	for _, test := range []struct {
		input float64
		want  uint8
	}{
		{math.NaN(), 0}, {math.Inf(1), 255}, {math.Inf(-1), 0}, {-1, 0}, {.5, 0}, {1.5, 2}, {2.5, 2}, {127.5, 128}, {254.5, 254}, {255.1, 255},
	} {
		if actual := ClampByte(test.input); actual != test.want {
			t.Errorf("ClampByte(%v)=%d, want %d", test.input, actual, test.want)
		}
	}
}

func TestFrontendFitPixels(t *testing.T) {
	source := values(2, 255, 0, 0, 255, 0, 0, 255, 255)
	for _, test := range []struct {
		fit, align string
		want       []uint8
	}{
		{"cover", "left", []uint8{255, 0, 0, 255}}, {"cover", "right", []uint8{0, 0, 255, 255}},
		{"fill", "center", []uint8{128, 0, 128, 255}},
	} {
		actual, err := FitImage(source, 1, 1, test.fit, test.align)
		if err != nil {
			t.Fatal(err)
		}
		pixel(t, actual, 0, 0, test.want...)
	}
	actual, err := FitImage(source, 4, 4, "contain", "bottom")
	if err != nil {
		t.Fatal(err)
	}
	pixel(t, actual, 0, 0, 0, 0, 0, 0)
	pixel(t, actual, 0, 3, 255, 0, 0, 255)
	pixel(t, actual, 3, 3, 0, 0, 255, 255)
	edge, err := FitImage(values(2, 255, 0, 0, 255, 0, 0, 0, 0), 1, 1, "fill", "center")
	if err != nil {
		t.Fatal(err)
	}
	pixel(t, edge, 0, 0, 255, 0, 0, 128)
	for align, anchor := range alignments {
		vertical, err := FitImage(solid(1, 3, 255, 0, 0, 255), 3, 3, "contain", align)
		if err != nil {
			t.Fatal(err)
		}
		pixel(t, vertical, int(anchor[0]*2), 1, 255, 0, 0, 255)
		horizontal, err := FitImage(solid(3, 1, 0, 255, 0, 255), 3, 3, "contain", align)
		if err != nil {
			t.Fatal(err)
		}
		pixel(t, horizontal, 1, int(anchor[1]*2), 0, 255, 0, 255)
	}
}

func TestTransparentIdentityNormalizesHiddenRGB(t *testing.T) {
	source := values(2, 33, 44, 55, 0, 11, 22, 33, 128)
	fitted, err := FitImage(source, 2, 1, "fill", "center")
	if err != nil {
		t.Fatal(err)
	}
	pixel(t, fitted, 0, 0, 0, 0, 0, 0)
	pixel(t, fitted, 1, 0, 11, 22, 33, 128)
	pixel(t, source, 0, 0, 33, 44, 55, 0)
	opaque := solid(2, 1, 1, 2, 3, 255)
	borrowed, err := FitImage(opaque, 2, 1, "cover", "center")
	if err != nil || borrowed != opaque {
		t.Fatal("opaque identity should borrow pixels", err)
	}
}

func TestBackgroundReplacementFrontendAlpha(t *testing.T) {
	target := solid(3, 1, 200, 30, 10, 255)
	original := raster(solid(3, 1, 255, 255, 255, 255), 0, 0)
	candidate := values(3, 99, 88, 77, 0, 0, 100, 200, 128, 20, 40, 60, 255)
	if err := CompositeBackgroundReplacement(target, original, candidate); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(target.Pix, []uint8{0, 0, 0, 0, 0, 100, 200, 128, 20, 40, 60, 255}) {
		t.Fatal(target.Pix)
	}
	pixel(t, candidate, 0, 0, 99, 88, 77, 0)
	target = solid(1, 1, 0, 0, 0, 0)
	original = raster(solid(1, 1, 1, 2, 3, 128), 0, 0)
	original.Opacity, original.FillOpacity = new(.5), new(.5)
	original.Mask = &Mask{Bounds: image.Rect(0, 0, 1, 1), Pixels: solid(1, 1, 128, 128, 128, 255), DefaultColor: new(uint8(0))}
	if err := CompositeBackgroundReplacement(target, original, solid(1, 1, 200, 100, 50, 128)); err != nil {
		t.Fatal(err)
	}
	pixel(t, target, 0, 0, 200, 100, 50, 8)
	target = solid(1, 1, 0, 0, 200, 255)
	original = raster(solid(1, 1, 1, 2, 3, 255), 0, 0)
	original.Opacity = new(.5)
	if err := CompositeBackgroundReplacement(target, original, solid(1, 1, 200, 0, 0, 128)); err != nil {
		t.Fatal(err)
	}
	pixel(t, target, 0, 0, 67, 0, 133, 192)
}

func TestNegativeBackgroundOffsetsAndMask(t *testing.T) {
	target := solid(3, 2, 200, 0, 0, 255)
	original := raster(solid(3, 3, 1, 2, 3, 255), -1, -1)
	original.Pixels.Pix[(2*3+2)*4+3] = 0
	original.Mask = &Mask{Bounds: image.Rect(0, 0, 2, 2), DefaultColor: new(uint8(0)),
		Pixels: values(2, 255, 255, 255, 255, 0, 0, 0, 255, 128, 128, 128, 255, 255, 255, 255, 255)}
	if err := CompositeBackgroundReplacement(target, original, solid(1, 1, 10, 20, 30, 0)); err != nil {
		t.Fatal(err)
	}
	pixel(t, target, 0, 0, 0, 0, 0, 0)
	pixel(t, target, 1, 0, 200, 0, 0, 255)
	pixel(t, target, 0, 1, 200, 0, 0, 127)
	pixel(t, target, 1, 1, 200, 0, 0, 255)
	pixel(t, target, 2, 0, 200, 0, 0, 255)
	pixel(t, target, 2, 1, 200, 0, 0, 255)
}

func TestEngineFrontendExpectedPixelsAndCaches(t *testing.T) {
	engine, err := New(fixture())
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig(engine.Metadata())
	for index := range config.Objects {
		config.Objects[index].Enabled = config.Objects[index].ObjectID == "a"
		config.Objects[index].Tag = " 共用印花 "
	}
	config.Fit = "fill"
	prints := map[string]*image.NRGBA{"共用印花": solid(2, 2, 10, 100, 240, 255)}
	actual, err := engine.Render(context.Background(), config, prints, nil)
	if err != nil {
		t.Fatal(err)
	}
	pixel(t, actual, 0, 0, 20, 40, 60, 255)
	pixel(t, actual, 1, 0, 10, 100, 240, 255)
	pixel(t, actual, 2, 0, 0, 200, 0, 255)
	pixel(t, actual, 4, 0, 10, 100, 240, 255)
	pixel(t, actual, 5, 0, 5, 50, 120, 255)
	clear(actual.Pix)
	for index := range config.Objects {
		config.Objects[index].Enabled = config.Objects[index].ObjectID == "b"
	}
	actual, err = engine.Render(context.Background(), config, prints, nil)
	if err != nil {
		t.Fatal(err)
	}
	pixel(t, actual, 1, 0, 255, 0, 0, 255)
	pixel(t, actual, 2, 0, 10, 100, 240, 255)
	pixel(t, actual, 4, 0, 255, 0, 0, 255)
	for index := range config.Objects {
		config.Objects[index].Enabled = config.Objects[index].ObjectID == "a"
	}
	actual, err = engine.Render(context.Background(), config, map[string]*image.NRGBA{"共用印花": solid(2, 2, 33, 77, 99, 0)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pixel(t, actual, 1, 0, 20, 40, 60, 255)
	pixel(t, actual, 4, 0, 20, 40, 60, 255)
	pixel(t, engine.document.Layers[1].Pixels, 1, 0, 255, 0, 0, 255)
}

func TestProjectionUsesFullObjectAndCachedMasks(t *testing.T) {
	document := fixture()
	document.Layers[3].Bounds = image.Rect(5, 0, 6, 2)
	document.Layers[3].Pixels = solid(1, 2, 255, 0, 0, 255)
	engine, err := New(document)
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig(engine.Metadata())
	config.Objects[1].Enabled = false
	actual, err := engine.Render(context.Background(), config, map[string]*image.NRGBA{"a": solid(2, 2, 0, 0, 255, 255)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pixel(t, actual, 4, 0, 0, 0, 255, 255)
	layer := document.Layers[1]
	raw, err := CompileProjection(layer, 6, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	masked, err := CompileProjection(layer, 6, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	source := solid(2, 2, 44, 99, 177, 127)
	first, origin, err := ProjectImage(layer, source, raw)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := ProjectImage(layer, source, masked)
	if err != nil {
		t.Fatal(err)
	}
	layerCopy := *layer
	layerCopy.Bounds.Min = origin
	a, b := solid(6, 2, 0, 0, 0, 0), solid(6, 2, 0, 0, 0, 0)
	if err := CompositeLayer(a, &layerCopy, first); err != nil {
		t.Fatal(err)
	}
	if err := CompositeLayer(b, &layerCopy, second); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Pix, b.Pix) {
		t.Fatal("cached spans changed composite pixels")
	}
}

func identityWarp(left, top float64) *Warp {
	warp := &Warp{Style: "custom", Rotate: "horizontal", UOrder: 4, VOrder: 4, HasEnvelope: true,
		Bounds: &WarpBounds{Left: left, Top: top, Right: left + 2, Bottom: top + 4, Pixels: true}}
	for index := range 16 {
		warp.MeshPoints = append(warp.MeshPoints, Point{
			X: left + float64(index%4)*2/3,
			Y: top + float64(index/4)*4/3,
		})
	}
	return warp
}

func TestIdentityWarpStrictness(t *testing.T) {
	for _, origin := range []Point{{X: 0, Y: 0}, {X: 260, Y: 0}, {X: 12, Y: 9}} {
		object := &Placed{Width: 2, Height: 4, Warp: identityWarp(origin.X, origin.Y)}
		if RequiresWarpRendering(object) {
			t.Fatal("identity rejected", origin)
		}
		object.Warp.MeshPoints[5].X += 1e-11
		if RequiresWarpRendering(object) {
			t.Fatal("serialization noise rejected")
		}
		object.Warp.MeshPoints[5].X += .01
		if !RequiresWarpRendering(object) {
			t.Fatal("real deformation accepted")
		}
	}
	for _, warp := range []*Warp{{Style: "arc"}, {Style: "custom"}, {Value: math.NaN()}, identityWarp(1e16, 0)} {
		if !RequiresWarpRendering(&Placed{Width: 2, Height: 4, Warp: warp}) {
			t.Fatalf("invalid warp accepted: %#v", warp)
		}
	}
}

func TestUnsupportedFeaturesFailExplicitly(t *testing.T) {
	for _, feature := range []string{"unknown-blend", "effect", "warp"} {
		t.Run(feature, func(t *testing.T) {
			document := fixture()
			layer := document.Layers[1]
			switch feature {
			case "unknown-blend":
				layer.BlendMode = "unknown"
			case "effect":
				layer.Unsupported = []string{"暂不支持图层效果"}
			case "warp":
				layer.Placed.Warp = &Warp{Style: "arc", Value: 20}
			}
			if _, err := New(document); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestTrimOutputAndEncoders(t *testing.T) {
	source := solid(10, 10, 0, 0, 0, 0)
	for y := 2; y < 6; y++ {
		for x := 3; x < 5; x++ {
			source.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	trimmed, err := TrimInputTransparency(source)
	if err != nil || trimmed.Rect.Size() != image.Pt(2, 4) {
		t.Fatal(trimmed, err)
	}
	prepared, err := PrepareOutput(source, Options{Format: "png", TrimTransparent: true})
	if err != nil || prepared.Rect.Size() != image.Pt(10, 10) {
		t.Fatal(prepared, err)
	}
	prepared, err = PrepareOutput(trimmed, Options{Format: "png", MaxSize: 2})
	if err != nil || prepared.Rect.Size() != image.Pt(1, 2) {
		t.Fatal(prepared, err)
	}
	empty, err := TrimInputTransparency(solid(4, 4, 8, 9, 10, 0))
	if err != nil {
		t.Fatal(err)
	}
	pixel(t, empty, 0, 0, 0, 0, 0, 0)
	transparent := values(2, 99, 88, 77, 0, 0, 100, 200, 128)
	white, err := FlattenWhite(transparent)
	if err != nil {
		t.Fatal(err)
	}
	pixel(t, white, 0, 0, 255, 255, 255, 255)
	pixel(t, white, 1, 0, 127, 177, 227, 255)
	var encoded bytes.Buffer
	if err := Encode(&encoded, transparent, "png"); err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	// Canvas PNG encoding round-trips through premultiplied 8-bit pixels before restoring straight alpha.
	if got := color.NRGBAModel.Convert(decoded.At(1, 0)).(color.NRGBA); got != (color.NRGBA{G: 100, B: 199, A: 128}) {
		t.Fatal(got)
	}
	encoded.Reset()
	if err := Encode(&encoded, solid(16, 16, 127, 177, 227, 255), "jpeg"); err != nil {
		t.Fatal(err)
	}
	jpegImage, err := jpeg.Decode(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	decodedColor := color.NRGBAModel.Convert(jpegImage.At(8, 8)).(color.NRGBA)
	for index, got := range []uint8{decodedColor.R, decodedColor.G, decodedColor.B} {
		want := []uint8{127, 177, 227}[index]
		if math.Abs(float64(got)-float64(want)) > 2 {
			t.Fatalf("JPEG channel %d=%d want within2 of%d", index, got, want)
		}
	}
}

func TestBatchPairingRandomAndNames(t *testing.T) {
	engine, err := New(fixture())
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig(engine.Metadata())
	groups, err := BuildBatchInputs(config, map[string][]string{"a": {"first", "second"}, "b": {"shared"}})
	if err != nil || !reflect.DeepEqual(groups, []map[string]string{{"a": "first", "b": "shared"}, {"a": "second", "b": "shared"}}) {
		t.Fatal(groups, err)
	}
	config.RandomImageRows = []RandomRow{{LayerID: "layer:0", Enabled: true, RandomImages: []Candidate{{URL: "red"}, {URL: "blue"}}}}
	for _, test := range []struct {
		value float64
		want  string
	}{{0, "red"}, {.5, "blue"}, {.999, ""}} {
		selected, err := PickBackgrounds(config, engine.Metadata(), func() float64 { return test.value })
		if err != nil || selected["layer:0"] != test.want {
			t.Fatal(selected, err)
		}
	}
	names := OutputNames([]Slice{{Name: "a/b"}, {Name: "a/b"}, {Name: ".."}, {}})
	if !reflect.DeepEqual(names, []string{"a_b", "a_b-2", "_", "切片-4"}) {
		t.Fatal(names)
	}
	if got := GroupOutputName(0, 1, "图/一.JPEG", "png"); got != "001-02-图_一.png" {
		t.Fatal(got)
	}
	first, second := SeededRandom(42, 9), SeededRandom(42, 9)
	for range 5 {
		if first() != second() {
			t.Fatal("random sequence changed")
		}
	}
}

func TestCancellationAndInvalidBuffers(t *testing.T) {
	engine, err := New(fixture())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = engine.Render(ctx, DefaultConfig(engine.Metadata()), nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := FitImage(&image.NRGBA{Pix: []byte{1}, Rect: image.Rect(0, 0, 2, 2), Stride: 8}, 2, 2, "fill", "center"); err == nil {
		t.Fatal("short pixel buffer accepted")
	}
	if _, err := EmptyImage(30001, 1); !errors.Is(err, ErrImageSize) {
		t.Fatal(err)
	}
}
