package psdcompose_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

// mixedReviewMix interpolates associated RGBA, independently of compositor interpolation helpers.
func mixedReviewMix(first, second reviewShadowPremult, amount float64) reviewShadowPremult {
	var result reviewShadowPremult
	for index := range result {
		result[index] = first[index]*(1-amount) + second[index]*amount
	}
	return result
}

// mixedReviewOverlay independently evaluates four informative overlay modes, including special Fill in Linear Dodge.
func mixedReviewOverlay(background reviewShadowPremult, effect psdeffects.Effect) reviewShadowPremult {
	alpha, opacity := background[3], effect.Opacity
	result := reviewShadowPremult{0, 0, 0, alpha + opacity*(1-alpha)}
	for channel, source := range effect.Color {
		destination := 0.0
		if alpha != 0 {
			destination = background[channel] / alpha
		}
		blended := source
		switch effect.BlendMode {
		case "mul ":
			blended = source * destination
		case "scrn":
			blended = 1 - (1-source)*(1-destination)
		case "lddg":
			result[channel] = alpha*math.Min(1, destination+source*opacity) + source*opacity*(1-alpha)
			continue
		}
		result[channel] = background[channel]*(1-opacity) + opacity*((1-alpha)*source+alpha*blended)
	}
	return result
}

func mixedReviewPixel(
	background reviewShadowPremult, rgb [3]float64, shape, fill, opacity float64, interiorGroup bool,
	effects []psdeffects.Effect, coverage func(psdeffects.Effect) float64,
) reviewShadowPremult {
	outside, inside := background, background
	for _, effect := range effects {
		if effect.Kind != psdeffects.DropShadow {
			continue
		}
		amount := coverage(effect) * effect.Opacity * opacity
		outside = reviewShadowOver(outside, effect.Color, amount, effect.BlendMode == "mul ")
		if !effect.LayerConceals {
			inside = reviewShadowOver(inside, effect.Color, amount, effect.BlendMode == "mul ")
		}
	}
	interior := reviewShadowOver(inside, rgb, fill, false)
	if interiorGroup {
		interior = reviewShadowPremult{rgb[0], rgb[1], rgb[2], 1}
	}
	// Existing Color Overlay descriptor order is reversed inside the layer, independently of shadow order.
	for _, effect := range slices.Backward(effects) {
		if effect.Kind == psdeffects.ColorOverlay {
			interior = mixedReviewOverlay(interior, effect)
		}
	}
	if interiorGroup {
		inside = reviewShadowOver(inside, [3]float64{interior[0], interior[1], interior[2]}, fill*opacity, false)
	} else {
		inside = mixedReviewMix(inside, interior, opacity)
	}
	return mixedReviewMix(outside, inside, shape)
}

func mixedReviewEffects(reverse, conceal bool) []psdeffects.Effect {
	first := psdeffects.Effect{Kind: psdeffects.DropShadow, Enabled: true, Opacity: .63,
		BlendMode: "norm", Color: [3]float64{.13, .47, .83}, Distance: 1, LayerConceals: conceal}
	second := psdeffects.Effect{Kind: psdeffects.DropShadow, Enabled: true, Opacity: .41,
		BlendMode: "mul ", Color: [3]float64{.79, .21, .35}, Distance: 2, Angle: 90, LayerConceals: !conceal}
	overlays := []psdeffects.Effect{
		{Kind: psdeffects.ColorOverlay, Enabled: true, BlendMode: "scrn", Opacity: .58, Color: [3]float64{.37, .71, .19}},
		{Kind: psdeffects.ColorOverlay, Enabled: true, BlendMode: "lddg", Opacity: .36, Color: [3]float64{.11, .23, .89}},
		{Kind: psdeffects.ColorOverlay, Enabled: true, BlendMode: "mul ", Opacity: .27, Color: [3]float64{.83, .31, .47}},
	}
	if reverse {
		return []psdeffects.Effect{overlays[2], second, overlays[1], first, overlays[0]}
	}
	return []psdeffects.Effect{first, overlays[0], second, overlays[1], overlays[2]}
}

func mixedReviewSourceAt(layer *psdmodel.Layer, x, y int) color.NRGBA {
	if !image.Pt(x, y).In(layer.Bounds) {
		return color.NRGBA{}
	}
	return layer.Pixels.NRGBAAt(layer.Pixels.Rect.Min.X+x-layer.Bounds.Min.X,
		layer.Pixels.Rect.Min.Y+y-layer.Bounds.Min.Y)
}

func mixedReviewCompare(t *testing.T, actual color.NRGBA, reference reviewShadowPremult, x, y int) {
	t.Helper()
	expected := reviewShadowRGBA(reference)
	for component, got := range []byte{actual.R, actual.G, actual.B, actual.A} {
		want := []byte{expected.R, expected.G, expected.B, expected.A}[component]
		if difference := int(got) - int(want); difference < -1 || difference > 1 {
			t.Fatalf("mixed scalar reference differs at %d,%d component %d: %v != %v", x, y, component, actual, expected)
		}
	}
}

func TestMixedShadowOverlayIndependentPartialAlphaFillOpacityAndMaskStages(t *testing.T) {
	layer := reviewShadowLayer(image.Rect(2, 1, 8, 6))
	layer.Pixels.Pix[3], layer.Pixels.Pix[7] = 0, 255
	maskPixels := image.NewNRGBA(image.Rect(1, 0, 10, 8))
	for y := maskPixels.Rect.Min.Y; y < maskPixels.Rect.Max.Y; y++ {
		for x := maskPixels.Rect.Min.X; x < maskPixels.Rect.Max.X; x++ {
			maskPixels.SetNRGBA(x, y, color.NRGBA{R: byte(x*31 + y*67 + 23), A: 255})
		}
	}
	layer.Mask = &psdmodel.Mask{Bounds: maskPixels.Rect, Pixels: maskPixels, Invert: true,
		DefaultColor: new(uint8(31)), Density: new(uint8(173))}
	originalPixels, originalMask := bytes.Clone(layer.Pixels.Pix), bytes.Clone(maskPixels.Pix)
	maskAt := func(x, y int) float64 {
		value := byte(31)
		if image.Pt(x, y).In(maskPixels.Rect) {
			value = maskPixels.NRGBAAt(x, y).R
		}
		return 1 - float64(value)*173/(255*255)
	}
	orderChanged := false
	for _, backgroundAlpha := range []byte{0, 111, 255} {
		background := &psdmodel.Layer{Bounds: image.Rect(0, 0, 12, 9), Pixels: image.NewNRGBA(image.Rect(0, 0, 12, 9)),
			BlendMode: "norm"}
		for index := 0; index < len(background.Pixels.Pix); index += 4 {
			copy(background.Pixels.Pix[index:], []byte{73, 119, 211, backgroundAlpha})
		}
		document := &psdmodel.Document{Width: 12, Height: 9, Layers: []*psdmodel.Layer{background, layer}}
		for mode := range 4 {
			global := mode == 1 || mode == 3
			layer.Mask.Relative, layer.MaskAsGlobal = mode == 0 || mode == 3, nil
			if mode < 2 {
				layer.MaskAsGlobal = new(global)
			}
			shapeAt := func(x, y int) float64 {
				alpha := float64(mixedReviewSourceAt(layer, x, y).A)
				if !global {
					alpha = math.Floor(alpha*maskAt(x, y) + .5)
				}
				return alpha / 255
			}
			for _, interior := range []bool{false, true} {
				layer.BlendInterior = new(interior)
				for _, fill := range []float64{0, .37, 1} {
					layer.FillOpacity = new(fill)
					for _, opacity := range []float64{.61, 1} {
						layer.Opacity = new(opacity)
						for _, conceal := range []bool{false, true} {
							var previous []byte
							for _, reverse := range []bool{false, true} {
								effects := mixedReviewEffects(reverse, conceal)
								layer.Effects = reviewShadowSet(effects...)
								actual, err := psdcompose.Render(t.Context(), document,
									psdcompose.Options{ApproximateDropShadow: true})
								if err != nil {
									t.Fatal(err)
								}
								orderChanged = orderChanged || previous != nil && !bytes.Equal(previous, actual.Pix)
								previous = bytes.Clone(actual.Pix)
								for y := range document.Height {
									for x := range document.Width {
										alpha := float64(backgroundAlpha) / 255
										original := reviewShadowPremult{73.0 / 255 * alpha, 119.0 / 255 * alpha,
											211.0 / 255 * alpha, alpha}
										source := mixedReviewSourceAt(layer, x, y)
										rgb := [3]float64{float64(source.R) / 255, float64(source.G) / 255, float64(source.B) / 255}
										painted := mixedReviewPixel(original, rgb, shapeAt(x, y), fill, opacity, interior,
											effects, func(effect psdeffects.Effect) float64 {
												if effect.Angle == 90 {
													return shapeAt(x, y-int(effect.Distance))
												}
												return shapeAt(x+int(effect.Distance), y)
											})
										if global {
											painted = mixedReviewMix(original, painted, maskAt(x, y))
										}
										mixedReviewCompare(t, actual.NRGBAAt(x, y), painted, x, y)
									}
								}
							}
						}
					}
				}
			}
		}
	}
	if !orderChanged || !bytes.Equal(originalPixels, layer.Pixels.Pix) || !bytes.Equal(originalMask, maskPixels.Pix) {
		t.Fatal("descriptor order was uninformative or rendering changed borrowed input samples")
	}
}

func TestMixedShadowOverlayIndependentFeatherCoverageAndRegionalOrigin(t *testing.T) {
	layer := reviewShadowLayer(image.Rect(-4, -1, 8, 9))
	maskPixels := image.NewNRGBA(image.Rect(-2, 0, 7, 9))
	for y := maskPixels.Rect.Min.Y; y < maskPixels.Rect.Max.Y; y++ {
		for x := maskPixels.Rect.Min.X; x < maskPixels.Rect.Max.X; x++ {
			maskPixels.SetNRGBA(x, y, color.NRGBA{R: byte(x*31 + y*67 + 89), A: 255})
		}
	}
	layer.Mask = &psdmodel.Mask{Bounds: maskPixels.Rect, Pixels: maskPixels, Invert: true,
		DefaultColor: new(uint8(31)), Density: new(uint8(173)), Feather: new(1.0)}
	layer.MaskAsGlobal, layer.Opacity, layer.FillOpacity = new(false), new(.61), new(.37)
	effects := mixedReviewEffects(false, true)
	effects[0].Size, effects[0].Spread, effects[0].Distance, effects[0].Angle = 2, .04, 4, 54
	layer.Effects = reviewShadowSet(effects...)
	shapeValues := make(map[image.Point]float64)
	for y := layer.Bounds.Min.Y; y < layer.Bounds.Max.Y; y++ {
		for x := layer.Bounds.Min.X; x < layer.Bounds.Max.X; x++ {
			shapeValues[image.Pt(x, y)] = math.Floor(float64(mixedReviewSourceAt(layer, x, y).A)*
				reviewShadowGaussian(layer.Mask, x, y)+.5) / 255
		}
	}
	shape := func(x, y int) float64 { return shapeValues[image.Pt(x, y)] }
	document := &psdmodel.Document{Width: 16, Height: 12, Layers: []*psdmodel.Layer{layer}}
	options := psdcompose.Options{ApproximateDropShadow: true}
	for _, interior := range []bool{false, true} {
		layer.BlendInterior = new(interior)
		full, err := psdcompose.Render(t.Context(), document, options)
		if err != nil {
			t.Fatal(err)
		}
		for y := range document.Height {
			for x := range document.Width {
				source := mixedReviewSourceAt(layer, x, y)
				rgb := [3]float64{float64(source.R) / 255, float64(source.G) / 255, float64(source.B) / 255}
				want := mixedReviewPixel(reviewShadowPremult{}, rgb, shape(x, y), .37, .61, interior,
					effects, func(effect psdeffects.Effect) float64 {
						effect.LayerConceals = false
						return reviewShadowCoverage(shape, effect, x, y)
					})
				mixedReviewCompare(t, full.NRGBAAt(x, y), want, x, y)
			}
		}
		for _, region := range []image.Rectangle{image.Rect(0, 0, 5, 7), image.Rect(4, 3, 13, 10)} {
			partial, err := psdcompose.RenderRegion(t.Context(), document, region, options)
			if err != nil {
				t.Fatal(err)
			}
			for y := region.Min.Y; y < region.Max.Y; y++ {
				for x := region.Min.X; x < region.Max.X; x++ {
					if partial.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
						t.Fatal("mixed feathered ROI disagrees with full-canvas pixels")
					}
				}
			}
		}
	}
}

func TestMixedShadowOverlayIndependentPoliciesLimitsAndCancellation(t *testing.T) {
	for _, mutate := range []func(*psdmodel.Layer){
		func(layer *psdmodel.Layer) { layer.BlendMode = "mul " },
		func(layer *psdmodel.Layer) { layer.Placed = &psdmodel.Placed{Type: "raster"} },
		func(layer *psdmodel.Layer) { layer.VectorMask = &psdvector.Mask{Version: 4, HasInitialFill: true} },
		func(layer *psdmodel.Layer) { layer.TransparencyShapes = new(false) },
		func(layer *psdmodel.Layer) { layer.ChannelRestrictions = []uint32{0} },
		func(layer *psdmodel.Layer) { layer.Knockout = psdmodel.KnockoutShallow },
		func(layer *psdmodel.Layer) { layer.Group = true },
		func(layer *psdmodel.Layer) {
			layer.BlendRanges = []psdmodel.BlendRange{{
				Source:      psdmodel.BlendSlider{BlackHigh: 100, WhiteLow: 255, WhiteHigh: 255},
				Destination: psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 255},
			}}
		},
		func(layer *psdmodel.Layer) { layer.Effects.Items[0].BlendMode = "scrn" },
		func(layer *psdmodel.Layer) { layer.Effects.Items[1].BlendMode = "diss" },
		func(layer *psdmodel.Layer) { layer.Effects.Items[0].Unsupported = []string{"unknown shadow semantics"} },
		func(layer *psdmodel.Layer) { layer.Effects.Unsupported = []string{"unknown effect-set semantics"} },
		func(layer *psdmodel.Layer) {
			layer.Effects.Items = append(layer.Effects.Items, psdeffects.Effect{
				Kind: psdeffects.InnerGlow, Enabled: true, Opacity: 1, BlendMode: "norm", Range: .5, Technique: "SfBL",
			})
		},
	} {
		layer := reviewShadowLayer(image.Rect(1, 1, 5, 5))
		layer.Effects = reviewShadowSet(mixedReviewEffects(false, true)...)
		layer.Mask = &psdmodel.Mask{Bounds: layer.Bounds, DefaultColor: new(uint8(128))}
		mutate(layer)
		pixels, err := psdcompose.Render(t.Context(), &psdmodel.Document{Width: 7, Height: 7,
			Layers: []*psdmodel.Layer{layer}}, psdcompose.Options{ApproximateDropShadow: true})
		if pixels != nil || !errors.Is(err, psdcompose.ErrUnsupported) {
			t.Fatalf("mixed effect qualification accepted an unrelated contract: %v", err)
		}
	}
	layer := reviewShadowLayer(image.Rect(1, 1, 5, 5))
	layer.Effects = reviewShadowSet(mixedReviewEffects(false, true)...)
	layer.Mask = &psdmodel.Mask{Bounds: layer.Bounds, DefaultColor: new(uint8(128))}
	document := &psdmodel.Document{Width: 7, Height: 7, Layers: []*psdmodel.Layer{layer}}
	pixels, err := psdcompose.Render(t.Context(), document)
	if pixels != nil || !errors.Is(err, psdcompose.ErrUnsupported) {
		t.Fatalf("mixed mask/effects silently enabled approximation: %v", err)
	}
	options := psdcompose.Options{ApproximateDropShadow: true, MaxWorkingBytes: 1}
	pixels, err = psdcompose.Render(t.Context(), document, options)
	if pixels != nil || !errors.Is(err, psdcompose.ErrLimit) {
		t.Fatalf("mixed mask/effects bypassed working allocation limit: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	options.MaxWorkingBytes = 0
	if pixels, err := psdcompose.Render(ctx, document, options); pixels != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mixed mask/effects returned partial output: %v", err)
	}
	layer.Clipping = true
	base := reviewShadowLayer(layer.Bounds)
	document.Layers = []*psdmodel.Layer{base, layer}
	pixels, err = psdcompose.Render(t.Context(), document, options)
	if pixels != nil || !errors.Is(err, psdcompose.ErrUnsupported) {
		t.Fatalf("mixed mask/effects enabled an active clipping-chain target: %v", err)
	}
}

func TestMixedShadowOverlayNoShadowCoveragePreservesEveryExistingOverlayMode(t *testing.T) {
	for _, mode := range []string{"norm", "dark", "mul ", "idiv", "lbrn", "dkCl", "lite", "scrn", "div ",
		"lddg", "lgCl", "over", "sLit", "hLit", "vLit", "lLit", "pLit", "hMix", "diff", "smud",
		"fsub", "fdiv", "hue ", "sat ", "colr", "lum "} {
		for _, grouped := range []bool{false, true} {
			layer := reviewShadowLayer(image.Rect(2, 1, 8, 6))
			layer.Opacity, layer.FillOpacity, layer.BlendInterior = new(.61), new(.37), new(grouped)
			layer.MaskAsGlobal = new(true)
			layer.Mask = &psdmodel.Mask{Bounds: layer.Bounds, DefaultColor: new(uint8(173))}
			background := reviewShadowLayer(image.Rect(0, 0, 12, 9))
			overlay := psdeffects.Effect{Kind: psdeffects.ColorOverlay, Enabled: true, BlendMode: mode,
				Opacity: .63, Color: [3]float64{.37, .71, .19}}
			layer.Effects = reviewShadowSet(overlay)
			document := &psdmodel.Document{Width: 12, Height: 9, Layers: []*psdmodel.Layer{background, layer}}
			expected, err := psdcompose.Render(t.Context(), document)
			if err != nil {
				t.Fatal(err)
			}
			// This shadow is active but displaced completely outside the canvas, so the interior contract is unchanged.
			shadow := psdeffects.Effect{Kind: psdeffects.DropShadow, Enabled: true, BlendMode: "norm",
				Opacity: .73, Color: [3]float64{.89, .31, .13}, Distance: 50, LayerConceals: true}
			layer.Effects = reviewShadowSet(shadow, overlay)
			actual, err := psdcompose.Render(t.Context(), document, psdcompose.Options{ApproximateDropShadow: true})
			if err != nil {
				t.Fatalf("mixed interior rejected supported overlay mode %s: %v", mode, err)
			}
			for index, value := range actual.Pix {
				if delta := int(value) - int(expected.Pix[index]); delta < -1 || delta > 1 {
					t.Fatalf("zero shadow coverage changed existing %s overlay at byte %d: %d != %d",
						mode, index, value, expected.Pix[index])
				}
			}
		}
	}
}
