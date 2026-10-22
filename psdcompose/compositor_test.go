package psdcompose

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func composeTestLayer(bounds image.Rectangle, value color.NRGBA) *psdmodel.Layer {
	pixels := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := range pixels.Rect.Dy() {
		for x := range pixels.Rect.Dx() {
			pixels.SetNRGBA(x, y, value)
		}
	}
	return &psdmodel.Layer{Bounds: bounds, Pixels: pixels, BlendMode: "normal"}
}

func composeTestDocument(layers ...*psdmodel.Layer) *psdmodel.Document {
	return &psdmodel.Document{Width: 1, Height: 1, Layers: layers}
}

func composeTestPixel(t *testing.T, document *psdmodel.Document) color.NRGBA {
	t.Helper()
	result, err := Render(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	return result.NRGBAAt(0, 0)
}

func TestComposeSourceOverAndOwnership(t *testing.T) {
	bottom := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 255, A: 128})
	top := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{B: 255, A: 128})
	beforeBottom, beforeTop := bytes.Clone(bottom.Pixels.Pix), bytes.Clone(top.Pixels.Pix)
	document := composeTestDocument(bottom, top)
	document.Composite = composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{G: 255, A: 255}).Pixels
	result, err := Render(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	if actual := result.NRGBAAt(0, 0); actual != (color.NRGBA{R: 85, B: 170, A: 192}) {
		t.Fatalf("source over = %v", actual)
	}
	result.Pix[0] = 0
	if !bytes.Equal(bottom.Pixels.Pix, beforeBottom) || !bytes.Equal(top.Pixels.Pix, beforeTop) {
		t.Fatal("render result aliases caller-owned pixels")
	}
	document.Layers = nil
	if actual := composeTestPixel(t, document); actual != (color.NRGBA{}) {
		t.Fatalf("removed all layers reused the saved composite: %v", actual)
	}
}

func TestComposeAbsoluteCoordinatesSubimagesAndMasks(t *testing.T) {
	pixels := image.NewNRGBA(image.Rect(10, 20, 14, 22))
	pixels.SetNRGBA(11, 20, color.NRGBA{R: 255, A: 255})
	pixels.SetNRGBA(12, 20, color.NRGBA{B: 255, A: 255})
	layer := &psdmodel.Layer{
		Bounds: image.Rect(-1, 0, 1, 1), Pixels: pixels.SubImage(image.Rect(11, 20, 13, 21)).(*image.NRGBA),
		Mask: &psdmodel.Mask{
			Bounds: image.Rect(0, 0, 1, 1), Pixels: composeTestLayer(
				image.Rect(0, 0, 1, 1), color.NRGBA{R: 128, A: 255},
			).Pixels,
			DefaultColor: new(uint8(0)), Invert: true, Density: new(uint8(128)),
			Relative: true,
		},
	}
	actual := composeTestPixel(t, composeTestDocument(layer))
	if actual != (color.NRGBA{B: 255, A: 191}) {
		t.Fatalf("absolute mask/subimage = %v", actual)
	}
	layer.Mask.Disabled = true
	if actual := composeTestPixel(t, composeTestDocument(layer)); actual != (color.NRGBA{B: 255, A: 255}) {
		t.Fatalf("disabled mask = %v", actual)
	}
}

func TestComposeGroupIsolationAndPassThrough(t *testing.T) {
	bottom := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 128, G: 128, B: 128, A: 255})
	child := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 128, G: 128, B: 128, A: 255})
	child.BlendMode = "mul "
	group := &psdmodel.Layer{Group: true, Children: []*psdmodel.Layer{child}, BlendMode: "norm"}
	if actual := composeTestPixel(t, composeTestDocument(bottom, group)); actual != (color.NRGBA{
		R: 128, G: 128, B: 128, A: 255,
	}) {
		t.Fatalf("isolated group = %v", actual)
	}
	group.BlendMode = "pass"
	if actual := composeTestPixel(t, composeTestDocument(bottom, group)); actual != (color.NRGBA{
		R: 64, G: 64, B: 64, A: 255,
	}) {
		t.Fatalf("pass-through group = %v", actual)
	}
	group.Opacity = new(.5)
	if actual := composeTestPixel(t, composeTestDocument(bottom, group)); actual != (color.NRGBA{
		R: 96, G: 96, B: 96, A: 255,
	}) {
		t.Fatalf("partial pass-through group = %v", actual)
	}
	group.Mask = &psdmodel.Mask{DefaultColor: new(uint8(128))}
	if actual := composeTestPixel(t, composeTestDocument(bottom, group)); actual != (color.NRGBA{
		R: 112, G: 112, B: 112, A: 255,
	}) {
		t.Fatalf("pass-through group mask = %v", actual)
	}
	group.Hidden = true
	if actual := composeTestPixel(t, composeTestDocument(bottom, group)); actual != (color.NRGBA{
		R: 128, G: 128, B: 128, A: 255,
	}) {
		t.Fatalf("hidden group = %v", actual)
	}
}

func TestComposePassGroupInterpolatesPremultipliedPixels(t *testing.T) {
	bottom := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 255, A: 128})
	child := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{B: 255, A: 255})
	group := &psdmodel.Layer{
		Group: true, Children: []*psdmodel.Layer{child}, BlendMode: "pass", Opacity: new(.5),
	}
	if actual := composeTestPixel(t, composeTestDocument(bottom, group)); actual != (color.NRGBA{R: 85, B: 170, A: 192}) {
		t.Fatalf("partial pass group on translucent backdrop = %v", actual)
	}
}

func TestComposeClippingBaseAndBlendGroup(t *testing.T) {
	background := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	base := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 128, G: 128, B: 128, A: 255})
	base.BlendMode, base.Opacity = "mul", new(.5)
	clipped := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 255, A: 255})
	clipped.Clipping = true
	if actual := composeTestPixel(t, composeTestDocument(background, base, clipped)); actual != (color.NRGBA{
		R: 255, G: 128, B: 128, A: 255,
	}) {
		t.Fatalf("grouped clipping chain = %v", actual)
	}
	base.BlendClipped = new(false)
	if actual := composeTestPixel(t, composeTestDocument(background, base, clipped)); actual != (color.NRGBA{
		R: 255, G: 128, B: 128, A: 255,
	}) {
		t.Fatalf("independent clipping chain = %v", actual)
	}
	background.Pixels.SetNRGBA(0, 0, color.NRGBA{R: 128, G: 128, B: 128, A: 255})
	base.BlendClipped = new(true)
	if actual := composeTestPixel(t, composeTestDocument(background, base, clipped)); actual != (color.NRGBA{
		R: 128, G: 64, B: 64, A: 255,
	}) {
		t.Fatalf("grouped clips should inherit the clipping base blend mode: %v", actual)
	}
	base.BlendClipped = new(false)
	if actual := composeTestPixel(t, composeTestDocument(background, base, clipped)); actual != (color.NRGBA{
		R: 192, G: 64, B: 64, A: 255,
	}) {
		t.Fatalf("individual clips should use their own blend mode: %v", actual)
	}
	background.Pixels.SetNRGBA(0, 0, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	base.Hidden = true
	if actual := composeTestPixel(t, composeTestDocument(background, base, clipped)); actual != (color.NRGBA{
		R: 255, G: 255, B: 255, A: 255,
	}) {
		t.Fatalf("hidden base revealed its clips = %v", actual)
	}
	if actual := composeTestPixel(t, composeTestDocument(clipped)); actual != (color.NRGBA{}) {
		t.Fatalf("orphan clip painted = %v", actual)
	}
	clipped.Unsupported = []string{"live effect"}
	if err := Validate(composeTestDocument(background, base, clipped)); err != nil {
		t.Fatalf("hidden clipping base should also hide unsupported clipped semantics: %v", err)
	}
	if err := Validate(composeTestDocument(clipped)); err != nil {
		t.Fatalf("orphan clips cannot contribute unsupported semantics: %v", err)
	}
}

func TestComposeClippingDoesNotGrowBaseTransparency(t *testing.T) {
	base := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 255, A: 64})
	clipped := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{B: 255, A: 255})
	clipped.Clipping = true
	if actual := composeTestPixel(t, composeTestDocument(base, clipped)); actual != (color.NRGBA{B: 255, A: 64}) {
		t.Fatalf("clipping grew base alpha = %v", actual)
	}
	clipped.Pixels.SetNRGBA(0, 0, color.NRGBA{G: 255, A: 128})
	if actual := composeTestPixel(t, composeTestDocument(base, clipped)); actual != (color.NRGBA{R: 127, G: 128, A: 64}) {
		t.Fatalf("clipped color applied base alpha twice = %v", actual)
	}
	base.BlendClipped = new(false)
	if actual := composeTestPixel(t, composeTestDocument(base, clipped)); actual != (color.NRGBA{R: 127, G: 128, A: 64}) {
		t.Fatalf("individual clips changed the clipping base alpha = %v", actual)
	}
}

func TestComposeBlendIfSplitAndRGBSliders(t *testing.T) {
	defaultSlider := psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 255}
	bottom := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 64, G: 64, B: 64, A: 255})
	top := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 128, G: 128, B: 128, A: 255})
	top.BlendRanges = []psdmodel.BlendRange{{
		Source:      psdmodel.BlendSlider{BlackLow: 64, BlackHigh: 192, WhiteLow: 255, WhiteHigh: 255},
		Destination: defaultSlider,
	}}
	if actual := composeTestPixel(t, composeTestDocument(bottom, top)); actual != (color.NRGBA{
		R: 96, G: 96, B: 96, A: 255,
	}) {
		t.Fatalf("gray split slider = %v", actual)
	}
	top.BlendRanges = []psdmodel.BlendRange{
		{Source: defaultSlider, Destination: defaultSlider},
		{Source: psdmodel.BlendSlider{BlackLow: 200, BlackHigh: 200, WhiteLow: 255, WhiteHigh: 255},
			Destination: defaultSlider},
	}
	if actual := composeTestPixel(t, composeTestDocument(bottom, top)); actual != (color.NRGBA{
		R: 64, G: 64, B: 64, A: 255,
	}) {
		t.Fatalf("red channel slider = %v", actual)
	}
	top.BlendRanges[1].Source = psdmodel.BlendSlider{BlackLow: 20, BlackHigh: 220, WhiteLow: 40, WhiteHigh: 240}
	if err := Validate(composeTestDocument(bottom, top)); err != nil {
		t.Fatalf("crossing split ranges should remain valid: %v", err)
	}
	top.BlendRanges = make([]psdmodel.BlendRange, 5)
	for index := range top.BlendRanges {
		top.BlendRanges[index] = psdmodel.BlendRange{Source: defaultSlider, Destination: defaultSlider}
	}
	if err := Validate(composeTestDocument(bottom, top)); err != nil {
		t.Fatalf("default extra-channel range should remain valid: %v", err)
	}
	if actual := composeTestPixel(t, composeTestDocument(bottom, top)); actual != (color.NRGBA{
		R: 128, G: 128, B: 128, A: 255,
	}) {
		t.Fatalf("default extra-channel ranges changed the image: %v", actual)
	}
	top.BlendRanges[4].Source.BlackHigh = 64
	if err := Validate(composeTestDocument(bottom, top)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("custom extra-channel range = %v", err)
	}
}

func TestComposeCombinedAndOverlappingBlendIf(t *testing.T) {
	defaultSlider := psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 255}
	overlapping := psdmodel.BlendSlider{BlackLow: 64, BlackHigh: 192, WhiteLow: 96, WhiteHigh: 224}
	if value := composeSlider(overlapping, 139.0/255); math.Abs(value-76.0/129) > 1e-12 {
		t.Fatalf("overlapping black and white ramps should use their minimum: %g", value)
	}
	ranges := []psdmodel.BlendRange{
		{Source: defaultSlider, Destination: defaultSlider},
		{Source: defaultSlider, Destination: psdmodel.BlendSlider{
			BlackHigh: 128, WhiteLow: 192, WhiteHigh: 255,
		}},
		{Source: defaultSlider, Destination: psdmodel.BlendSlider{
			BlackHigh: 128, WhiteLow: 192, WhiteHigh: 255,
		}},
	}
	backdrop := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{G: 255, B: 60, A: 128})
	source := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 255, B: 250, A: 128})
	source.BlendRanges = ranges
	if actual := composeTestPixel(t, composeTestDocument(backdrop, source)); actual != (color.NRGBA{
		R: 57, G: 198, B: 103, A: 144,
	}) {
		t.Fatalf("each destination channel must independently account for backdrop transparency: %v", actual)
	}
}

func TestComposeFillVersusOpacity(t *testing.T) {
	bottom := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 204, G: 204, B: 204, A: 255})
	top := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 128, G: 128, B: 128, A: 255})
	top.BlendMode, top.FillOpacity = "idiv", new(.5)
	if actual := composeTestPixel(t, composeTestDocument(bottom, top)); actual != (color.NRGBA{
		R: 187, G: 187, B: 187, A: 255,
	}) {
		t.Fatalf("special fill = %v", actual)
	}
	top.FillOpacity, top.Opacity = nil, new(.5)
	if actual := composeTestPixel(t, composeTestDocument(bottom, top)); actual != (color.NRGBA{
		R: 179, G: 179, B: 179, A: 255,
	}) {
		t.Fatalf("ordinary opacity = %v", actual)
	}
	top.FillOpacity, top.Opacity = new(.5), nil
	if actual := composeTestPixel(t, composeTestDocument(top)); actual != (color.NRGBA{R: 128, G: 128, B: 128, A: 128}) {
		t.Fatalf("special fill over transparent backdrop = %v", actual)
	}
}

func TestComposeValidationLimitsAndMalformedData(t *testing.T) {
	valid := func() *psdmodel.Document {
		return composeTestDocument(composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{A: 255}))
	}
	tests := []struct {
		name    string
		mutate  func(*psdmodel.Document)
		options Options
		kind    error
	}{
		{"nil layer", func(document *psdmodel.Document) { document.Layers[0] = nil }, Options{}, ErrInvalid},
		{"cycle", func(document *psdmodel.Document) {
			document.Layers[0].Children = document.Layers
		}, Options{}, ErrInvalid},
		{"shared", func(document *psdmodel.Document) {
			document.Layers = append(document.Layers, document.Layers[0])
		}, Options{}, ErrInvalid},
		{"stride", func(document *psdmodel.Document) {
			document.Layers[0].Pixels.Stride = 0
		}, Options{}, ErrInvalid},
		{"backing", func(document *psdmodel.Document) {
			document.Layers[0].Pixels.Pix = document.Layers[0].Pixels.Pix[:3]
		}, Options{}, ErrInvalid},
		{"opacity", func(document *psdmodel.Document) {
			document.Layers[0].Opacity = new(math.NaN())
		}, Options{}, ErrInvalid},
		{"bounds", func(document *psdmodel.Document) {
			document.Layers[0].Bounds = image.Rectangle{Min: image.Pt(1, 1), Max: image.Pt(0, 0)}
		}, Options{}, ErrInvalid},
		{"canvas budget", func(document *psdmodel.Document) {}, Options{MaxWorkingBytes: 3}, ErrLimit},
		{"tree budget", func(document *psdmodel.Document) {}, Options{MaxWorkingBytes: 4}, ErrLimit},
		{"depth", func(document *psdmodel.Document) {
			document.Layers = []*psdmodel.Layer{{Children: document.Layers}}
		}, Options{MaxDepth: 1}, ErrLimit},
		{"layers", func(document *psdmodel.Document) {
			document.Layers = append(document.Layers, &psdmodel.Layer{})
		}, Options{MaxLayers: 1}, ErrLimit},
		{"negative options", func(document *psdmodel.Document) {}, Options{MaxLayers: -1}, ErrInvalid},
		{"unsafe depth", func(document *psdmodel.Document) {}, Options{MaxDepth: 257}, ErrLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := valid()
			test.mutate(document)
			_, err := Render(context.Background(), document, test.options)
			if !errors.Is(err, test.kind) {
				t.Fatalf("error = %v, want %v", err, test.kind)
			}
			var detail *Error
			if !errors.As(err, &detail) || detail.Path == "" {
				t.Fatalf("missing structured path: %v", err)
			}
		})
	}
	if err := Validate(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil document = %v", err)
	}
	if err := Validate(valid(), Options{}, Options{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("multiple options = %v", err)
	}
}

func TestComposeCachedPixelsUnsupportedSemanticsAndCancellation(t *testing.T) {
	layer := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 255, A: 255})
	layer.Placed = &psdmodel.Placed{Type: "vector", Warp: &psdmodel.Warp{Style: "warpArc"}}
	if err := Validate(composeTestDocument(layer)); err != nil {
		t.Fatalf("saved smart-object pixels should not require content reconstruction: %v", err)
	}
	layer.Placed.Unsupported = []string{"live smart filter"}
	if err := Validate(composeTestDocument(layer)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("live smart filter = %v", err)
	}
	layer.Placed.Unsupported = nil
	layer.Unsupported = []string{"live effect"}
	if err := Validate(composeTestDocument(layer)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("live effect = %v", err)
	}
	layer.Hidden = true
	if err := Validate(composeTestDocument(layer)); err != nil {
		t.Fatalf("hidden unsupported content = %v", err)
	}
	layer.Hidden, layer.Unsupported = false, nil
	layer.Mask = &psdmodel.Mask{Feather: new(float64(1))}
	if err := Validate(composeTestDocument(layer)); err != nil {
		t.Fatalf("feathering a constant mask requires no additional rendering semantics: %v", err)
	}
	layer.Mask.Disabled = true
	if err := Validate(composeTestDocument(layer)); err != nil {
		t.Fatalf("disabled feather = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Render(ctx, composeTestDocument(layer)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestComposePassThroughClippingBase(t *testing.T) {
	base := &psdmodel.Layer{Group: true, BlendMode: "pass", Children: []*psdmodel.Layer{
		composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 255, A: 255}),
	}}
	clipped := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{G: 255, A: 255})
	clipped.Clipping = true
	result, err := Render(context.Background(), composeTestDocument(base, clipped))
	if err != nil || result.NRGBAAt(0, 0) != (color.NRGBA{G: 255, A: 255}) {
		t.Fatalf("pass-through clipping base result=%v error=%v", result, err)
	}
	clipped.Hidden = true
	if err := Validate(composeTestDocument(base, clipped)); err != nil {
		t.Fatalf("hidden clips have no effect on pass-through bases: %v", err)
	}
}

func TestComposeUnsupportedDoesNotHideInvalidStructure(t *testing.T) {
	unsupported := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{A: 255})
	unsupported.Unsupported = []string{"live effect"}
	malformed := &psdmodel.Layer{Group: true}
	malformed.Children = []*psdmodel.Layer{malformed}
	document := composeTestDocument(unsupported, malformed)
	if err := Validate(document); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported layer concealed later cycle: %v", err)
	}
	document.BlockingIssues = []string{"global rendering feature"}
	document.Layers = []*psdmodel.Layer{unsupported}
	unsupported.Pixels.Stride = 0
	if err := Validate(document); !errors.Is(err, ErrInvalid) {
		t.Fatalf("global feature concealed invalid image storage: %v", err)
	}
	unsupported.Pixels.Stride = 4
	if err := Validate(document); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("valid structure lost its global capability error: %v", err)
	}
}

func FuzzRenderDecodedTree(f *testing.F) {
	f.Add([]byte{1, 2, 3, 0, 255, 128, 64, 0})
	f.Add([]byte{4, 1, 7, 1, 128, 64, 255, 64, 2, 128})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 4 || len(data) > 256 {
			return
		}
		width, height, count := 1+int(data[0]%4), 1+int(data[1]%4), 1+int(data[2]%8)
		document := &psdmodel.Document{Width: width, Height: height}
		pixelsBefore := make(map[*image.NRGBA][]byte)
		for index := range count {
			flags := data[(3+index*3)%len(data)]
			value := color.NRGBA{
				R: data[(4+index*3)%len(data)], G: data[(5+index*3)%len(data)],
				B: flags, A: data[(6+index*3)%len(data)],
			}
			layer := composeTestLayer(image.Rect(-1, -1, width-1, height-1), value)
			layer.Opacity = new(float64(data[(7+index*3)%len(data)]) / 255)
			layer.FillOpacity = new(float64(data[(8+index*3)%len(data)]) / 255)
			layer.Hidden, layer.Clipping = flags&2 != 0, flags&4 != 0
			modes := [...]string{"normal", "mul ", "scrn", "pass", "diss", "unknown"}
			layer.BlendMode = modes[data[(9+index*3)%len(data)]%byte(len(modes))]
			if flags&1 != 0 {
				layer.Group = true
				layer.Children = []*psdmodel.Layer{}
				if len(document.Layers) != 0 {
					last := len(document.Layers) - 1
					layer.Children = append(layer.Children, document.Layers[last])
					document.Layers = document.Layers[:last]
				}
			}
			if flags&16 != 0 {
				layer.Bounds.Max.X = layer.Bounds.Min.X - 1
			}
			if flags&32 != 0 {
				layer.Opacity = new(math.NaN())
			}
			if flags&64 != 0 {
				layer.Pixels.Stride = int(flags%16) - 2
			}
			if flags&128 != 0 {
				layer.Children = []*psdmodel.Layer{layer}
			}
			pixelsBefore[layer.Pixels] = bytes.Clone(layer.Pixels.Pix)
			document.Layers = append(document.Layers, layer)
		}
		result, err := Render(context.Background(), document, Options{MaxWorkingBytes: 1 << 20, MaxDepth: 8})
		if err != nil && !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrLimit) {
			t.Fatalf("unexpected rendering error: %v", err)
		}
		if err == nil && (result == nil || result.Rect != image.Rect(0, 0, width, height)) {
			t.Fatalf("successful rendering returned an invalid image: %v", result)
		}
		for pixels, before := range pixelsBefore {
			if !bytes.Equal(pixels.Pix, before) {
				t.Fatal("render modified decoded source pixels")
			}
		}
	})
}
