package psdcompose_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

type vectorShadowReviewRectangle struct{ left, top, right, bottom float64 }

func vectorShadowReviewMask(rect vectorShadowReviewRectangle, width, height int, invert bool) *psdvector.Mask {
	mask := &psdvector.Mask{Version: 3, Width: width, Height: height, Invert: invert, HasInitialFill: true,
		Subpaths: []psdvector.Subpath{{Closed: true, Operation: 1, Unknown1: 1}}}
	for _, point := range []psdvector.Point{{X: rect.left, Y: rect.top}, {X: rect.right, Y: rect.top},
		{X: rect.right, Y: rect.bottom}, {X: rect.left, Y: rect.bottom}} {
		mask.Subpaths[0].Knots = append(mask.Subpaths[0].Knots,
			psdvector.Knot{Anchor: point, Preceding: point, Leaving: point})
	}
	return mask
}

// vectorShadowReviewCoverage counts rectangular membership directly instead of invoking the path rasterizer.
func vectorShadowReviewCoverage(rect vectorShadowReviewRectangle, invert bool, density byte, x, y int) float64 {
	inside := 0
	for row := range 8 {
		for column := range 8 {
			px, py := float64(x)+(float64(column)+.5)/8, float64(y)+(float64(row)+.5)/8
			covered := px >= rect.left && px < rect.right && py >= rect.top && py < rect.bottom
			if covered != invert {
				inside++
			}
		}
	}
	coverage := float64((inside*255+32)/64) / 255
	return 1 - (1-coverage)*float64(density)/255
}

func vectorShadowReviewUserCoverage(mask *psdmodel.Mask, x, y int) float64 {
	if mask == nil || mask.Disabled {
		return 1
	}
	value := byte(255)
	if mask.DefaultColor != nil {
		value = *mask.DefaultColor
	}
	if image.Pt(x, y).In(mask.Bounds) {
		value = mask.Pixels.NRGBAAt(x-mask.Bounds.Min.X+mask.Pixels.Rect.Min.X,
			y-mask.Bounds.Min.Y+mask.Pixels.Rect.Min.Y).R
	}
	coverage := float64(value) / 255
	if mask.Invert {
		coverage = 1 - coverage
	}
	if mask.Density != nil {
		coverage = 1 - (1-coverage)*float64(*mask.Density)/255
	}
	return coverage
}

func vectorShadowReviewFixture(
	bounds image.Rectangle, rect vectorShadowReviewRectangle, dual, invert, mixed bool,
) (*psdmodel.Document, *psdmodel.Layer) {
	layer := reviewShadowLayer(bounds)
	layer.Pixels.Pix[3], layer.Pixels.Pix[7] = 85, 255
	layer.VectorMask = vectorShadowReviewMask(rect, 9, 7, invert)
	layer.VectorDensity, layer.Opacity, layer.FillOpacity = new(uint8(191)), new(.61), new(.37)
	effects := mixedReviewEffects(false, true)
	effects[0].Size, effects[0].Spread, effects[0].Distance = .5, 0, 1.25
	if !mixed {
		pure := make([]psdeffects.Effect, 0, 2)
		for _, effect := range effects {
			if effect.Kind == psdeffects.DropShadow {
				pure = append(pure, effect)
			}
		}
		effects = pure
	}
	layer.Effects = reviewShadowSet(effects...)
	if dual {
		maskBounds := image.Rect(-2, -1, 6, 6)
		pixels := image.NewNRGBA(image.Rect(4, -3, 4+maskBounds.Dx(), -3+maskBounds.Dy()))
		for y := range pixels.Rect.Dy() {
			for x := range pixels.Rect.Dx() {
				pixels.SetNRGBA(x+pixels.Rect.Min.X, y+pixels.Rect.Min.Y,
					color.NRGBA{R: byte(x*31 + y*67 + 53), A: 255})
			}
		}
		layer.Mask = &psdmodel.Mask{Bounds: maskBounds, Pixels: pixels, DefaultColor: new(uint8(37)),
			Invert: true, Density: new(uint8(173))}
	}
	background := reviewShadowLayer(image.Rect(0, 0, 9, 7))
	for offset := 0; offset < len(background.Pixels.Pix); offset += 4 {
		copy(background.Pixels.Pix[offset:], []byte{73, 119, 211, 111})
	}
	return &psdmodel.Document{Width: 9, Height: 7, Layers: []*psdmodel.Layer{background, layer}}, layer
}

func vectorShadowReviewExpected(
	layer *psdmodel.Layer, rect vectorShadowReviewRectangle, userGlobal, vectorGlobal, grouped bool,
) func(int, int) reviewShadowPremult {
	vectorAt := func(x, y int) float64 {
		if layer.VectorMask.Disabled {
			return 1
		}
		density := byte(255)
		if layer.VectorDensity != nil {
			density = *layer.VectorDensity
		}
		return vectorShadowReviewCoverage(rect, layer.VectorMask.Invert, density, x, y)
	}
	shape := func(x, y int) float64 {
		alpha := float64(mixedReviewSourceAt(layer, x, y).A)
		if !vectorGlobal {
			alpha *= vectorAt(x, y)
		}
		if !userGlobal {
			alpha *= vectorShadowReviewUserCoverage(layer.Mask, x, y)
		}
		// Every non-global mask participates before the one source-shape byte quantization.
		return math.Floor(alpha+.5) / 255
	}
	return func(x, y int) reviewShadowPremult {
		alpha := 111.0 / 255
		original := reviewShadowPremult{73.0 / 255 * alpha, 119.0 / 255 * alpha, 211.0 / 255 * alpha, alpha}
		source := mixedReviewSourceAt(layer, x, y)
		rgb := [3]float64{float64(source.R) / 255, float64(source.G) / 255, float64(source.B) / 255}
		painted := mixedReviewPixel(original, rgb, shape(x, y), .37, .61, grouped,
			layer.Effects.Items, func(effect psdeffects.Effect) float64 {
				effect.LayerConceals = false
				return reviewShadowCoverage(shape, effect, x, y)
			})
		coverage := float64(1)
		if vectorGlobal {
			coverage *= vectorAt(x, y)
		}
		if userGlobal {
			coverage *= vectorShadowReviewUserCoverage(layer.Mask, x, y)
		}
		return mixedReviewMix(original, painted, coverage)
	}
}

func TestVectorShadowStageIndependentGlobalFlagsDualMasksAndPartialAlpha(t *testing.T) {
	policies := []struct {
		name     string
		flag     *bool
		unlinked bool
		global   bool
	}{
		{name: "nil-linked"},
		{name: "nil-unlinked", unlinked: true, global: true},
		{name: "false-overrides-unlinked", flag: new(false), unlinked: true},
		{name: "true-overrides-linked", flag: new(true), global: true},
	}
	rect := vectorShadowReviewRectangle{2.5, 1.5, 6.5, 4.5}
	options := psdcompose.Options{ApproximateDropShadow: true}
	for _, dual := range []bool{false, true} {
		for _, invert := range []bool{false, true} {
			for _, mixed := range []bool{false, true} {
				for _, grouped := range []bool{false, true} {
					for _, userPolicy := range policies {
						for _, vectorPolicy := range policies {
							name := fmt.Sprintf("dual%t_invert%t_mixed%t_grouped%t_user%s_vector%s", dual,
								invert, mixed, grouped, userPolicy.name, vectorPolicy.name)
							t.Run(name, func(t *testing.T) {
								document, layer := vectorShadowReviewFixture(image.Rect(1, 1, 7, 5), rect, dual, invert, mixed)
								layer.MaskAsGlobal, layer.VectorMaskAsGlobal = userPolicy.flag, vectorPolicy.flag
								layer.VectorMask.NotLinked, layer.BlendInterior = vectorPolicy.unlinked, new(grouped)
								if layer.Mask != nil {
									layer.Mask.Relative = userPolicy.unlinked
								}
								sourceBytes := bytes.Clone(layer.Pixels.Pix)
								var maskBytes []byte
								if dual {
									maskBytes = bytes.Clone(layer.Mask.Pixels.Pix)
								}
								actual, err := psdcompose.Render(t.Context(), document, options)
								if err != nil {
									t.Fatal(err)
								}
								want := vectorShadowReviewExpected(layer, rect, userPolicy.global, vectorPolicy.global, grouped)
								for y := range document.Height {
									for x := range document.Width {
										mixedReviewCompare(t, actual.NRGBAAt(x, y), want(x, y), x, y)
									}
								}
								if !bytes.Equal(sourceBytes, layer.Pixels.Pix) || dual && !bytes.Equal(maskBytes, layer.Mask.Pixels.Pix) {
									t.Fatal("shadow/vector rendering mutated borrowed source or user-mask pixels")
								}
							})
						}
					}
				}
			}
		}
	}
}

func TestVectorShadowStageSingleShapeRoundingAndNoRepeatedCoverage(t *testing.T) {
	rect := vectorShadowReviewRectangle{.5, 0, 1.5, 1}
	layer := reviewShadowLayer(image.Rect(0, 0, 1, 1))
	layer.Pixels.SetNRGBA(layer.Pixels.Rect.Min.X, layer.Pixels.Rect.Min.Y, color.NRGBA{R: 255, A: 85})
	layer.VectorMask = vectorShadowReviewMask(rect, 2, 2, false)
	maskPixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	maskPixels.SetNRGBA(0, 0, color.NRGBA{R: 128, A: 255})
	layer.Mask = &psdmodel.Mask{Bounds: maskPixels.Rect, Pixels: maskPixels}
	layer.Effects = reviewShadowSet(psdeffects.Effect{Kind: psdeffects.DropShadow, Enabled: true,
		BlendMode: "norm", Opacity: 1, Distance: 100, Size: .5, LayerConceals: true})
	document := &psdmodel.Document{Width: 2, Height: 2, Layers: []*psdmodel.Layer{layer}}
	for _, global := range []bool{false, true} {
		layer.VectorMaskAsGlobal = new(global)
		actual, err := psdcompose.Render(t.Context(), document, psdcompose.Options{ApproximateDropShadow: true})
		if err != nil {
			t.Fatal(err)
		}
		want := byte(21)
		if global {
			want = 22
		}
		if got := actual.NRGBAAt(0, 0); got != (color.NRGBA{R: 255, A: want}) {
			t.Fatalf("global=%t multiplied coverage twice or quantized dual shape more than once: %v", global, got)
		}
	}
}

func TestVectorShadowStageIndependentNegativeBoundsAndROISourceCoverage(t *testing.T) {
	rect := vectorShadowReviewRectangle{-2.5, -1.5, 4.5, 5.5}
	options := psdcompose.Options{ApproximateDropShadow: true}
	for _, invert := range []bool{false, true} {
		for _, userGlobal := range []bool{false, true} {
			for _, vectorGlobal := range []bool{false, true} {
				document, layer := vectorShadowReviewFixture(image.Rect(-4, -3, 6, 6), rect, true, invert, true)
				layer.MaskAsGlobal, layer.VectorMaskAsGlobal = new(userGlobal), new(vectorGlobal)
				layer.Effects.Items[0].Angle, layer.Effects.Items[0].Distance = 180, 3.25
				full, err := psdcompose.Render(t.Context(), document, options)
				if err != nil {
					t.Fatal(err)
				}
				want := vectorShadowReviewExpected(layer, rect, userGlobal, vectorGlobal, false)
				for y := range document.Height {
					for x := range document.Width {
						mixedReviewCompare(t, full.NRGBAAt(x, y), want(x, y), x, y)
					}
				}
				for _, region := range []image.Rectangle{image.Rect(0, 0, 3, 4), image.Rect(3, 1, 8, 6)} {
					tile, err := psdcompose.RenderRegion(t.Context(), document, region, options)
					if err != nil {
						t.Fatal(err)
					}
					for y := region.Min.Y; y < region.Max.Y; y++ {
						for x := region.Min.X; x < region.Max.X; x++ {
							if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
								t.Fatalf("off-canvas vector source changed shadow ROI at %d,%d", x, y)
							}
						}
					}
				}
			}
		}
	}
}

func TestVectorShadowStageLimitsCancellationAndUnsupportedMetadata(t *testing.T) {
	rect := vectorShadowReviewRectangle{1.5, 1.5, 4.5, 4.5}
	for _, mutate := range []func(*psdmodel.Layer){
		func(layer *psdmodel.Layer) { layer.VectorMask.Unsupported = []string{"unknown vector record"} },
		func(layer *psdmodel.Layer) { layer.VectorMask.Flags = 16 },
		func(layer *psdmodel.Layer) { layer.VectorFeather = new(1.5) },
		func(layer *psdmodel.Layer) { layer.Mask.Feather = new(.5) },
		func(layer *psdmodel.Layer) { layer.Mask.Unsupported = []string{"unknown pixel-mask flag"} },
		func(layer *psdmodel.Layer) { layer.BlendMode = "mul " },
		func(layer *psdmodel.Layer) { layer.Placed = &psdmodel.Placed{Type: "raster"} },
	} {
		document, layer := vectorShadowReviewFixture(image.Rect(1, 1, 6, 6), rect, true, false, true)
		mutate(layer)
		actual, err := psdcompose.Render(t.Context(), document, psdcompose.Options{ApproximateDropShadow: true})
		if actual != nil || !errors.Is(err, psdcompose.ErrUnsupported) {
			t.Fatalf("unsupported vector/shadow combination returned partial output: %v", err)
		}
	}
	document, _ := vectorShadowReviewFixture(image.Rect(1, 1, 6, 6), rect, true, false, false)
	if actual, err := psdcompose.Render(t.Context(), document); actual != nil || !errors.Is(err, psdcompose.ErrUnsupported) {
		t.Fatalf("vector shadows silently enabled approximation: %v", err)
	}
	options := psdcompose.Options{ApproximateDropShadow: true, MaxWorkingBytes: 1}
	if actual, err := psdcompose.Render(t.Context(), document, options); actual != nil || !errors.Is(err, psdcompose.ErrLimit) {
		t.Fatalf("vector shadows bypassed working budget: %v", err)
	}
	options.MaxWorkingBytes = 0
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if actual, err := psdcompose.Render(ctx, document, options); actual != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled vector shadows returned partial output: %v", err)
	}
}

func TestVectorShadowStageDisabledMasksAndDensityDefaultsRemainIdentity(t *testing.T) {
	rect := vectorShadowReviewRectangle{2.5, 1.5, 6.5, 4.5}
	options := psdcompose.Options{ApproximateDropShadow: true}
	for _, vectorDisabled := range []bool{false, true} {
		for _, userDisabled := range []bool{false, true} {
			for _, density := range []*uint8{nil, new(uint8(0)), new(uint8(255))} {
				for _, userGlobal := range []bool{false, true} {
					for _, vectorGlobal := range []bool{false, true} {
						document, layer := vectorShadowReviewFixture(image.Rect(1, 1, 7, 5), rect, true, true, true)
						layer.VectorMask.Disabled, layer.Mask.Disabled = vectorDisabled, userDisabled
						layer.VectorDensity = density
						layer.MaskAsGlobal, layer.VectorMaskAsGlobal = new(userGlobal), new(vectorGlobal)
						actual, err := psdcompose.Render(t.Context(), document, options)
						if err != nil {
							t.Fatal(err)
						}
						want := vectorShadowReviewExpected(layer, rect, userGlobal, vectorGlobal, false)
						for y := range document.Height {
							for x := range document.Width {
								mixedReviewCompare(t, actual.NRGBAAt(x, y), want(x, y), x, y)
							}
						}
					}
				}
			}
		}
	}
}
