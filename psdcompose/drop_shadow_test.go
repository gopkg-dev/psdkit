package psdcompose

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/psdrendered"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

type shadowReferencePixel struct {
	color [3]float64
	alpha float64
}

// shadowReferenceOver uses scalar straight-alpha source-over, independent of compositor blend helpers.
func shadowReferenceOver(
	background shadowReferencePixel, rgb [3]float64, alpha float64, multiply bool,
) shadowReferencePixel {
	result := shadowReferencePixel{alpha: alpha + background.alpha*(1-alpha)}
	if result.alpha == 0 {
		return result
	}
	for channel := range rgb {
		blended := rgb[channel]
		if multiply {
			blended *= background.color[channel]
		}
		result.color[channel] = (background.alpha*background.color[channel]*(1-alpha) +
			alpha*((1-background.alpha)*rgb[channel]+background.alpha*blended)) / result.alpha
	}
	return result
}

func shadowReferenceNRGBA(pixel shadowReferencePixel) color.NRGBA {
	return color.NRGBA{R: byte(math.Round(pixel.color[0] * 255)), G: byte(math.Round(pixel.color[1] * 255)),
		B: byte(math.Round(pixel.color[2] * 255)), A: byte(math.Round(pixel.alpha * 255))}
}

func shadowReferenceMix(first, second shadowReferencePixel, amount float64) shadowReferencePixel {
	result := shadowReferencePixel{alpha: first.alpha*(1-amount) + second.alpha*amount}
	if result.alpha == 0 {
		return result
	}
	for channel := range result.color {
		result.color[channel] = (first.color[channel]*first.alpha*(1-amount) +
			second.color[channel]*second.alpha*amount) / result.alpha
	}
	return result
}

func TestApproximateDropShadowIndependentMaskAndBlendStages(t *testing.T) {
	bounds := image.Rect(3, 2, 9, 7)
	layer := composeTestLayer(bounds, color.NRGBA{R: 199, G: 77, B: 121, A: 123})
	maskBounds := image.Rect(2, 1, 11, 8)
	maskPixels := image.NewNRGBA(maskBounds)
	for y := maskBounds.Min.Y; y < maskBounds.Max.Y; y++ {
		for x := maskBounds.Min.X; x < maskBounds.Max.X; x++ {
			maskPixels.SetNRGBA(x, y, color.NRGBA{R: byte((x*43 + y*19) % 256)})
		}
	}
	density, defaultColor := uint8(173), uint8(31)
	layer.Mask = &psdmodel.Mask{Bounds: maskBounds, Pixels: maskPixels, Density: &density,
		DefaultColor: &defaultColor, Invert: true, Relative: true}
	layer.Opacity, layer.FillOpacity = new(.61), new(.37)
	shadow := coreEffect(psdeffects.DropShadow)
	shadow.Distance, shadow.Angle, shadow.Opacity = 2, 90, .73
	shadow.UseGlobalLight = false
	shadow.Color = [3]float64{.19, .71, .43}
	layer.Effects = coreEffectSet(shadow)
	layerPixels, originalMask := bytes.Clone(layer.Pixels.Pix), bytes.Clone(maskPixels.Pix)
	for _, backgroundAlpha := range []byte{0, 111, 255} {
		background := composeTestLayer(image.Rect(0, 0, 13, 10), color.NRGBA{R: 97, G: 151, B: 221,
			A: backgroundAlpha})
		document := &psdmodel.Document{Width: 13, Height: 10, Layers: []*psdmodel.Layer{background, layer}}
		for _, global := range []bool{false, true} {
			layer.MaskAsGlobal = &global
			for _, layerMode := range []string{"norm", "mul "} {
				layer.BlendMode = layerMode
				for _, effectMode := range []string{"norm", "mul "} {
					layer.Effects.Items[0].BlendMode = effectMode
					for _, conceal := range []bool{false, true} {
						layer.Effects.Items[0].LayerConceals = conceal
						actual, err := Render(nil, document, Options{ApproximateDropShadow: true})
						if err != nil {
							t.Fatal(err)
						}
						maskAt := func(x, y int) float64 {
							stored := float64(defaultColor) / 255
							if image.Pt(x, y).In(maskBounds) {
								stored = float64(maskPixels.NRGBAAt(x, y).R) / 255
							}
							return 1 - stored*float64(density)/255
						}
						shapeAt := func(x, y int) float64 {
							if !image.Pt(x, y).In(bounds) {
								return 0
							}
							alpha := 123.0 / 255
							if !global {
								alpha = math.Round(alpha*maskAt(x, y)*255) / 255
							}
							return alpha
						}
						for y := 0; y < document.Height; y++ {
							for x := 0; x < document.Width; x++ {
								original := shadowReferencePixel{color: [3]float64{97.0 / 255, 151.0 / 255,
									221.0 / 255}, alpha: float64(backgroundAlpha) / 255}
								if backgroundAlpha == 0 {
									original.color = [3]float64{}
								}
								coverage := shapeAt(x, y-2)
								outside := shadowReferenceOver(original, shadow.Color, coverage*.73*.61,
									effectMode == "mul ")
								inside := original
								if !conceal {
									inside = outside
								}
								inside = shadowReferenceOver(inside, [3]float64{199.0 / 255, 77.0 / 255,
									121.0 / 255}, .37*.61, layerMode == "mul ")
								painted := shadowReferenceMix(outside, inside, shapeAt(x, y))
								if global {
									painted = shadowReferenceMix(original, painted, maskAt(x, y))
								}
								if got, want := actual.NRGBAAt(x, y), shadowReferenceNRGBA(painted); got != want {
									t.Fatalf("global=%v modes=%q/%q conceal=%v backgroundAlpha=%d at %d,%d: %v != %v",
										global, layerMode, effectMode, conceal, backgroundAlpha, x, y, got, want)
								}
							}
						}
					}
				}
			}
		}
	}
	if !bytes.Equal(layerPixels, layer.Pixels.Pix) || !bytes.Equal(originalMask, maskPixels.Pix) {
		t.Fatal("shadow rendering changed caller source or mask pixels")
	}
}

func realShadowDocument() *psdmodel.Document {
	layer := composeTestLayer(image.Rect(4, 3, 14, 12), color.NRGBA{})
	for y := layer.Bounds.Min.Y; y < layer.Bounds.Max.Y; y++ {
		for x := layer.Bounds.Min.X; x < layer.Bounds.Max.X; x++ {
			layer.Pixels.SetNRGBA(x-layer.Bounds.Min.X, y-layer.Bounds.Min.Y,
				color.NRGBA{R: byte(x * 17), G: byte(y * 19), B: 89,
					A: byte((x*37 + y*13) % 256)})
		}
	}
	shadow := coreEffect(psdeffects.DropShadow)
	shadow.Color, shadow.BlendMode = [3]float64{.21, .35, .47}, "mul "
	shadow.Size, shadow.Spread, shadow.Distance, shadow.Angle = 2, .04, 4, 54
	shadow.Opacity, shadow.UseGlobalLight = .42, false
	layer.Effects, layer.BlendMode = coreEffectSet(shadow), "mul "
	return &psdmodel.Document{Width: 20, Height: 18, Layers: []*psdmodel.Layer{layer}}
}

func TestApproximateDropShadowRealParametersROIOwnershipAndStrictDefault(t *testing.T) {
	document := realShadowDocument()
	if output, err := Render(nil, document); output != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("real fractional shadow silently changed the strict default: %v", err)
	}
	option := Options{ApproximateDropShadow: true}
	actual, err := Render(nil, document, option)
	if err != nil {
		t.Fatal(err)
	}
	for _, rectangle := range []image.Rectangle{image.Rect(0, 0, 20, 18), image.Rect(1, 4, 8, 16),
		image.Rect(6, 1, 20, 14), image.Rect(5, 7, 5, 7)} {
		regional, err := RenderRegion(nil, document, rectangle, option)
		if err != nil {
			t.Fatal(err)
		}
		for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
			for x := rectangle.Min.X; x < rectangle.Max.X; x++ {
				if regional.NRGBAAt(x, y) != actual.NRGBAAt(x, y) {
					t.Fatalf("ROI changed the document-coordinate shadow at %d,%d", x, y)
				}
			}
		}
	}
	exteriorPixels := 0
	for y := 0; y < document.Height; y++ {
		for x := 0; x < document.Width; x++ {
			if !image.Pt(x, y).In(document.Layers[0].Bounds) && actual.NRGBAAt(x, y).A != 0 {
				exteriorPixels++
			}
		}
	}
	if exteriorPixels == 0 {
		t.Fatal("real size/spread/displacement produced no exterior shadow")
	}
	input := bytes.Clone(document.Layers[0].Pixels.Pix)
	actual.Pix[0] = 217
	if !bytes.Equal(input, document.Layers[0].Pixels.Pix) {
		t.Fatal("owned output changed caller pixels")
	}
}

func TestApproximateDropShadowMaskStageAndPlacedIdentityGates(t *testing.T) {
	document := realShadowDocument()
	layer := document.Layers[0]
	option := Options{ApproximateDropShadow: true}
	layer.Mask = &psdmodel.Mask{Bounds: layer.Bounds, Pixels: image.NewNRGBA(layer.Bounds)}
	if err := Validate(document, option); err != nil {
		t.Fatalf("legacy linked-mask stage was rejected: %v", err)
	}
	layer.Mask.Disabled = true
	if err := Validate(document, option); err != nil {
		t.Fatal(err)
	}
	layer.Mask = nil
	layer.Placed = &psdmodel.Placed{ID: "identity-control"}
	if err := Validate(document, option); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("placed cached pixels were accepted without source-render identity: %v", err)
	}
	layer.RenderedEffects = psdrendered.New(layer.Pixels, layer.Bounds, layer.Placed)
	if err := Validate(document, option); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(){
		func() { layer.Placed = &psdmodel.Placed{ID: "identity-control"} },
		func() { layer.Bounds = layer.Bounds.Add(image.Pt(1, 0)) },
		func() {
			pixels := image.NewNRGBA(layer.Pixels.Rect)
			copy(pixels.Pix, layer.Pixels.Pix)
			layer.Pixels = pixels
		},
	} {
		saved := *layer
		mutate()
		if err := Validate(document, option); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("changed identity reused a projected-source qualification: %v", err)
		}
		*layer = saved
	}
}

func TestApproximateDropShadowShapeMaskROIHaloAndFeather(t *testing.T) {
	document := realShadowDocument()
	layer := document.Layers[0]
	layer.MaskAsGlobal = new(false)
	maskPixels := image.NewNRGBA(layer.Bounds)
	for pixel := 0; pixel < len(maskPixels.Pix); pixel += 4 {
		maskPixels.Pix[pixel] = byte((pixel*53 + 97) % 256)
	}
	layer.Mask = &psdmodel.Mask{Bounds: layer.Bounds, Pixels: maskPixels, Feather: new(.7)}
	option := Options{ApproximateDropShadow: true}
	full, err := Render(nil, document, option)
	if err != nil {
		t.Fatal(err)
	}
	window := image.Rect(1, 10, 7, 18)
	regional, err := RenderRegion(nil, document, window, option)
	if err != nil {
		t.Fatal(err)
	}
	for y := window.Min.Y; y < window.Max.Y; y++ {
		for x := window.Min.X; x < window.Max.X; x++ {
			if regional.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
				t.Fatalf("feathered shape mask was cached only inside output ROI at %d,%d", x, y)
			}
		}
	}
	partialBounds := image.Rect(4, 10, 7, 12)
	layer.Mask.SampleBounds = &partialBounds
	layer.Mask.Pixels = image.NewNRGBA(partialBounds)
	if err := ValidateRegion(document, window, option); !errors.Is(err, ErrInvalid) {
		t.Fatalf("incomplete shape-mask samples passed regional qualification: %v", err)
	}
}

func TestApproximateDropShadowExactBudgetAndCancellation(t *testing.T) {
	document := realShadowDocument()
	plan, err := preparePlan(document, []Options{{ApproximateDropShadow: true}})
	if err != nil {
		t.Fatal(err)
	}
	largestScratch := int64(0)
	for _, cache := range plan.effects {
		largestScratch = max(largestScratch, cache.scratchBytes)
	}
	bound := plan.workingBytes + largestScratch
	for _, budget := range []int64{bound - 1, bound} {
		output, err := Render(nil, document, Options{ApproximateDropShadow: true, MaxWorkingBytes: budget})
		if budget == bound-1 && (output != nil || !errors.Is(err, ErrLimit)) {
			t.Fatalf("effect shape/planes/scratch exceeded the charged bound: %v", err)
		}
		if budget == bound && err != nil {
			t.Fatalf("exact charged effect bound failed: %v", err)
		}
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &clippedCancelContext{Context: base, cancel: cancel}
	if output, err := Render(ctx, document, Options{ApproximateDropShadow: true}); output != nil ||
		!errors.Is(err, context.Canceled) || ctx.calls > 70 {
		t.Fatalf("mid-effect cancellation returned partial output or failed to unwind: %v checks=%d", err, ctx.calls)
	}
}

func TestApproximateDropShadowConcealUsesShapeBeforeFill(t *testing.T) {
	layer := composeTestLayer(image.Rect(1, 1, 2, 2), color.NRGBA{R: 213, G: 67, B: 119, A: 127})
	shadow := coreEffect(psdeffects.DropShadow)
	shadow.Color, shadow.Opacity = [3]float64{.3, .6, .9}, .8
	layer.Effects = coreEffectSet(shadow)
	layer.FillOpacity, layer.Opacity = new(float64(0)), new(.5)
	document := &psdmodel.Document{Width: 3, Height: 3, Layers: []*psdmodel.Layer{layer}}
	actual, err := Render(nil, document, Options{ApproximateDropShadow: true})
	if err != nil {
		t.Fatal(err)
	}
	coverage := 127.0 / 255 * (1 - 127.0/255)
	expected := shadowReferenceNRGBA(shadowReferenceOver(shadowReferencePixel{}, shadow.Color, coverage*.8*.5, false))
	if actual.NRGBAAt(1, 1) != expected {
		t.Fatal("Fill-zero concealment used the filled body instead of the original partial-alpha source shape")
	}
	layer.Effects.Items[0].LayerConceals = false
	actual, err = Render(nil, document, Options{ApproximateDropShadow: true})
	if err != nil {
		t.Fatal(err)
	}
	expected = shadowReferenceNRGBA(shadowReferenceOver(shadowReferencePixel{}, shadow.Color, 127.0/255*.8*.5, false))
	if actual.NRGBAAt(1, 1) != expected {
		t.Fatal("disabling concealment did not retain full unfilled source-alpha shadow")
	}
	layer.Pixels.SetNRGBA(0, 0, color.NRGBA{R: 237, G: 189, B: 71})
	actual, err = Render(nil, document, Options{ApproximateDropShadow: true})
	if err != nil || actual.NRGBAAt(1, 1) != (color.NRGBA{}) {
		t.Fatalf("hidden RGB from zero-alpha pixels shaped or colored the shadow: %v", err)
	}
}

func TestApproximateDropShadowMultipleOrderAndNestedAncestorMask(t *testing.T) {
	bounds := image.Rect(2, 2, 5, 5)
	layer := composeTestLayer(bounds, color.NRGBA{R: 189, A: 255})
	layer.FillOpacity = new(float64(0))
	first, second := coreEffect(psdeffects.DropShadow), coreEffect(psdeffects.DropShadow)
	first.LayerConceals, second.LayerConceals = false, false
	first.Color, second.Color = [3]float64{.7, .2, .1}, [3]float64{.1, .3, .9}
	first.Opacity, second.Opacity = .63, .47
	second.BlendMode = "mul "
	layer.Effects = coreEffectSet(first, second)
	inner := &psdmodel.Layer{Group: true, Bounds: image.Rect(0, 0, 7, 7), BlendMode: "norm",
		Children: []*psdmodel.Layer{layer}, Opacity: new(.67)}
	maskPixels := image.NewNRGBA(inner.Bounds)
	for index := 0; index < len(maskPixels.Pix); index += 4 {
		maskPixels.Pix[index] = 173
	}
	inner.Mask = &psdmodel.Mask{Bounds: inner.Bounds, Pixels: maskPixels}
	outer := &psdmodel.Layer{Group: true, Bounds: inner.Bounds, BlendMode: "norm",
		Children: []*psdmodel.Layer{inner}, Opacity: new(.73)}
	background := composeTestLayer(inner.Bounds, color.NRGBA{R: 103, G: 151, B: 211, A: 111})
	document := &psdmodel.Document{Width: 7, Height: 7, Layers: []*psdmodel.Layer{background, outer}}
	actual, err := Render(nil, document, Options{ApproximateDropShadow: true})
	if err != nil {
		t.Fatal(err)
	}
	// The explicit policy retains descriptor-order shadow planes, matching the legacy primitive's ordering.
	child := shadowReferenceOver(shadowReferencePixel{}, first.Color, first.Opacity, false)
	child = shadowReferenceOver(child, second.Color, second.Opacity, true)
	child.alpha *= .67 * 173 / 255 * .73
	original := shadowReferencePixel{color: [3]float64{103.0 / 255, 151.0 / 255, 211.0 / 255}, alpha: 111.0 / 255}
	expected := shadowReferenceNRGBA(shadowReferenceOver(original, child.color, child.alpha, false))
	if actual.NRGBAAt(3, 3) != expected || actual.NRGBAAt(0, 0) != background.Pixels.NRGBAAt(0, 0) {
		t.Fatal("multiple shadows lost descriptor order or nested ancestor opacity/mask was applied at the wrong stage")
	}
	if layer.Effects.Items[0].Color != first.Color || layer.Effects.Items[1].Color != second.Color {
		t.Fatal("multiple shadow painting changed the ordered caller metadata")
	}
}

func TestApproximateDropShadowDoesNotWaiveUnimplementedEffects(t *testing.T) {
	for _, mutate := range []func(*psdmodel.Layer){
		func(layer *psdmodel.Layer) { layer.Effects.Items[0].Noise = .1 },
		func(layer *psdmodel.Layer) { layer.Effects.Items[0].BlendMode = "scrn" },
		func(layer *psdmodel.Layer) { layer.BlendMode = "over" },
		func(layer *psdmodel.Layer) {
			layer.Effects.Items = append(layer.Effects.Items, coreEffect(psdeffects.OuterGlow))
		},
		func(layer *psdmodel.Layer) { layer.ChannelRestrictions = []uint32{1} },
		func(layer *psdmodel.Layer) { layer.Knockout = psdmodel.KnockoutDeep },
		func(layer *psdmodel.Layer) { layer.TransparencyShapes = new(false) },
	} {
		document := realShadowDocument()
		mutate(document.Layers[0])
		if output, err := Render(nil, document, Options{ApproximateDropShadow: true}); output != nil ||
			!errors.Is(err, ErrUnsupported) {
			t.Fatalf("drop-shadow selection admitted an unrelated unsupported interaction: %v", err)
		}
	}
	document := realShadowDocument()
	layer := document.Layers[0]
	layer.Bounds, layer.Pixels = image.Rectangle{}, image.NewNRGBA(image.Rectangle{})
	layer.Placed = &psdmodel.Placed{ID: "empty-projected-source"}
	layer.RenderedEffects = psdrendered.New(layer.Pixels, layer.Bounds, layer.Placed)
	if output, err := Render(nil, document, Options{ApproximateDropShadow: true}); err != nil || output == nil ||
		!bytes.Equal(output.Pix, make([]byte, len(output.Pix))) {
		t.Fatalf("qualified empty source projected into a shadow or failed coverage allocation: %v", err)
	}
}

func TestApproximateDropShadowLegacyMaskStageMatchesExplicitOverride(t *testing.T) {
	document := realShadowDocument()
	layer := document.Layers[0]
	maskBounds := image.Rect(-2, 1, 16, 15)
	maskPixels := image.NewNRGBA(maskBounds)
	for y := maskBounds.Min.Y; y < maskBounds.Max.Y; y++ {
		for x := maskBounds.Min.X; x < maskBounds.Max.X; x++ {
			maskPixels.SetNRGBA(x, y, color.NRGBA{R: byte((x*17 + y*71 + 256) % 256)})
		}
	}
	defaultColor := uint8(0)
	layer.Mask = &psdmodel.Mask{Bounds: maskBounds, Pixels: maskPixels, DefaultColor: &defaultColor}
	option := Options{ApproximateDropShadow: true}
	for _, fill := range []float64{0, 1} {
		layer.FillOpacity = &fill
		for _, relative := range []bool{false, true} {
			layer.Mask.Relative, layer.MaskAsGlobal = relative, nil
			legacy, err := Render(nil, document, option)
			if err != nil {
				t.Fatal(err)
			}
			layer.MaskAsGlobal = new(relative)
			explicit, err := Render(nil, document, option)
			if err != nil || !bytes.Equal(legacy.Pix, explicit.Pix) {
				t.Fatalf("legacy Relative=%v Fill=%g did not match its explicit mask stage: %v", relative, fill, err)
			}
			layer.MaskAsGlobal = new(!relative)
			opposite, err := Render(nil, document, option)
			if err != nil || bytes.Equal(legacy.Pix, opposite.Pix) {
				t.Fatalf("explicit mask stage did not override legacy state with informative shadow pixels: %v", err)
			}
			if layer.Mask.Bounds != maskBounds {
				t.Fatal("legacy mask link fallback changed the original absolute bounds")
			}
		}
	}
}

func TestApproximateDropShadowConcealedStylePartitionsBodyAndShadowOnce(t *testing.T) {
	layer := composeTestLayer(image.Rect(0, 0, 2, 1), color.NRGBA{R: 234, G: 67, B: 119, A: 255})
	layer.Pixels.SetNRGBA(1, 0, color.NRGBA{R: 234, G: 67, B: 119, A: 128})
	shadow := coreEffect(psdeffects.DropShadow)
	shadow.Color = [3]float64{31.0 / 255, 59.0 / 255, 111.0 / 255}
	shadow.Distance, shadow.Angle, shadow.UseGlobalLight = 1, 180, false
	layer.Effects = coreEffectSet(shadow)
	document := &psdmodel.Document{Width: 2, Height: 1, Layers: []*psdmodel.Layer{layer}}
	option := Options{ApproximateDropShadow: true}
	full, err := Render(nil, document, option)
	if err != nil {
		t.Fatal(err)
	}
	// An opaque shifted source casts a complete shadow behind a half-alpha body.
	// The original shape partitions body 128/255 and shadow 127/255; their alpha therefore totals one.
	expected := color.NRGBA{R: byte(math.Round((234.0*128 + 31.0*127) / 255)),
		G: byte(math.Round((67.0*128 + 59.0*127) / 255)),
		B: byte(math.Round((119.0*128 + 111.0*127) / 255)), A: 255}
	if full.NRGBAAt(1, 0) != expected {
		t.Fatalf("concealed shadow was attenuated by body alpha a second time: %v != %v", full.NRGBAAt(1, 0), expected)
	}
	layer.MaskAsGlobal = new(true)
	maskPixels := image.NewNRGBA(layer.Bounds)
	maskPixels.SetNRGBA(0, 0, color.NRGBA{R: 255})
	maskPixels.SetNRGBA(1, 0, color.NRGBA{R: 77})
	layer.Mask = &psdmodel.Mask{Bounds: layer.Bounds, Pixels: maskPixels}
	masked, err := Render(nil, document, option)
	if err != nil {
		t.Fatal(err)
	}
	expected.A = 77
	if masked.NRGBAAt(1, 0) != expected {
		t.Fatal("global mask did not crossfade the complete mutually partitioned body/shadow result")
	}
	layer.FillOpacity = new(float64(0))
	masked, err = Render(nil, document, option)
	if err != nil || masked.NRGBAAt(1, 0) != (color.NRGBA{R: 31, G: 59, B: 111, A: 38}) {
		t.Fatalf("Fill-zero shadow lost the original unfilled shape concealment: %v", err)
	}
}
