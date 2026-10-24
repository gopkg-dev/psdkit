package psdcompose_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// featherShadowQuadrature integrates the triangular pixel footprint without production feather helpers.
func featherShadowQuadrature(sigma float64, support int) []float64 {
	weights, total := make([]float64, 2*support+1), 0.0
	for offset := -support; offset <= support; offset++ {
		integrate := func(lower, upper float64) float64 {
			const steps = 128
			step, sum := (upper-lower)/steps, 0.0
			for sample := 0; sample <= steps; sample++ {
				position := lower + float64(sample)*step
				value := (1 - math.Abs(position)) *
					math.Exp(-.5*math.Pow((float64(offset)+position)/sigma, 2)) /
					(sigma * math.Sqrt(2*math.Pi))
				coefficient := 2.0
				if sample == 0 || sample == steps {
					coefficient = 1
				} else if sample%2 == 1 {
					coefficient = 4
				}
				sum += coefficient * value
			}
			return sum * step / 3
		}
		weights[offset+support] = integrate(-1, 0) + integrate(0, 1)
		total += weights[offset+support]
	}
	for index := range weights {
		weights[index] /= total
	}
	return weights
}

func featherShadowVectorWeights(radius float64) []float64 {
	sigma, support := .565, 2
	switch radius {
	case 1.5:
		sigma, support = 1.53, 4
	case 3:
		sigma, support = 3.06, 7
	}
	return featherShadowQuadrature(sigma, support)
}

func featherShadowFixture(
	radius float64, bounds, rectangle image.Rectangle, dual, invert, mixed, reverse bool,
) (*psdmodel.Document, *psdmodel.Layer) {
	geometry := vectorShadowReviewRectangle{float64(rectangle.Min.X), float64(rectangle.Min.Y),
		float64(rectangle.Max.X), float64(rectangle.Max.Y)}
	document, layer := vectorShadowReviewFixture(bounds, geometry, false, invert, mixed)
	layer.VectorFeather = new(radius)
	layer.Effects.Items = mixedReviewEffects(reverse, true)
	for index := range layer.Effects.Items {
		effect := &layer.Effects.Items[index]
		if effect.Kind == psdeffects.DropShadow {
			effect.Size, effect.Spread = 1.5, .1
			effect.Distance, effect.Angle = 2.25, 127
			if effect.BlendMode == "norm" {
				effect.Size, effect.Spread, effect.Distance, effect.Angle = .5, 0, 1.25, 13
			}
		}
	}
	if !mixed {
		pure := make([]psdeffects.Effect, 0, 2)
		for _, effect := range layer.Effects.Items {
			if effect.Kind == psdeffects.DropShadow {
				pure = append(pure, effect)
			}
		}
		layer.Effects.Items = pure
	}
	if dual {
		maskBounds := image.Rect(0, 0, document.Width, document.Height)
		pixels := image.NewNRGBA(image.Rect(-3, 2, -3+maskBounds.Dx(), 2+maskBounds.Dy()))
		for y := range maskBounds.Dy() {
			for x := range maskBounds.Dx() {
				pixels.SetNRGBA(x+pixels.Rect.Min.X, y+pixels.Rect.Min.Y,
					color.NRGBA{R: byte(x*31 + y*67 + 53), A: 255})
			}
		}
		layer.Mask = &psdmodel.Mask{Bounds: maskBounds, Pixels: pixels, DefaultColor: new(uint8(37)),
			Invert: !invert, Density: new(uint8(173)), Feather: new(1.0)}
	}
	return document, layer
}

// featherShadowExpected preserves source-shaping and final masks as distinct independently sampled fields.
func featherShadowExpected(
	document *psdmodel.Document, layer *psdmodel.Layer, rectangle image.Rectangle,
	vectorWeights, pixelWeights []float64, userGlobal, vectorGlobal, grouped bool,
) func(int, int) reviewShadowPremult {
	vectorValues := make(map[image.Point]float64)
	vectorAt := func(x, y int) float64 {
		if layer.VectorMask.Disabled || layer.VectorDensity != nil && *layer.VectorDensity == 0 {
			return 1
		}
		point := image.Pt(x, y)
		if value, exists := vectorValues[point]; exists {
			return value
		}
		coverage, support := 0.0, len(vectorWeights)/2
		for row, yWeight := range vectorWeights {
			for column, xWeight := range vectorWeights {
				if image.Pt(x+column-support, y+row-support).In(rectangle) {
					coverage += xWeight * yWeight
				}
			}
		}
		if layer.VectorMask.Invert {
			coverage = 1 - coverage
		}
		if layer.VectorDensity != nil {
			coverage = 1 - (1-coverage)*float64(*layer.VectorDensity)/255
		}
		vectorValues[point] = coverage
		return coverage
	}
	type pixelKey struct {
		point image.Point
		final bool
	}
	pixelValues := make(map[pixelKey]float64)
	pixelAt := func(x, y int, final bool) float64 {
		mask := layer.Mask
		if mask == nil || mask.Disabled || mask.Density != nil && *mask.Density == 0 {
			return 1
		}
		key := pixelKey{point: image.Pt(x, y), final: final}
		if value, exists := pixelValues[key]; exists {
			return value
		}
		sample := func(px, py int) float64 {
			if final {
				if mask.Bounds.Min.X >= 0 && mask.Bounds.Max.X <= document.Width {
					px = max(0, min(document.Width-1, px))
				}
				if mask.Bounds.Min.Y >= 0 && mask.Bounds.Max.Y <= document.Height {
					py = max(0, min(document.Height-1, py))
				}
			}
			value := 1.0
			if mask.DefaultColor != nil {
				value = float64(*mask.DefaultColor) / 255
			}
			if image.Pt(px, py).In(mask.Bounds) {
				value = float64(mask.Pixels.NRGBAAt(px-mask.Bounds.Min.X+mask.Pixels.Rect.Min.X,
					py-mask.Bounds.Min.Y+mask.Pixels.Rect.Min.Y).R) / 255
			}
			return value
		}
		coverage := sample(x, y)
		if mask.Feather != nil && *mask.Feather != 0 {
			coverage, support := 0.0, len(pixelWeights)/2
			for row, yWeight := range pixelWeights {
				for column, xWeight := range pixelWeights {
					coverage += xWeight * yWeight * sample(x+column-support, y+row-support)
				}
			}
			pixelValues[key] = coverage
		} else {
			pixelValues[key] = coverage
		}
		coverage = pixelValues[key]
		if mask.Invert {
			coverage = 1 - coverage
		}
		if mask.Density != nil {
			coverage = 1 - (1-coverage)*float64(*mask.Density)/255
		}
		pixelValues[key] = coverage
		return coverage
	}
	shapeValues := make(map[image.Point]float64)
	shape := func(x, y int) float64 {
		point := image.Pt(x, y)
		if value, exists := shapeValues[point]; exists {
			return value
		}
		alpha := float64(mixedReviewSourceAt(layer, x, y).A)
		if !vectorGlobal {
			alpha *= vectorAt(x, y)
		}
		if !userGlobal {
			alpha *= pixelAt(x, y, false)
		}
		value := math.Floor(alpha+.5) / 255
		shapeValues[point] = value
		return value
	}
	return func(x, y int) reviewShadowPremult {
		alpha := 111.0 / 255
		background := reviewShadowPremult{73.0 / 255 * alpha, 119.0 / 255 * alpha, 211.0 / 255 * alpha, alpha}
		source := mixedReviewSourceAt(layer, x, y)
		rgb := [3]float64{float64(source.R) / 255, float64(source.G) / 255, float64(source.B) / 255}
		painted := mixedReviewPixel(background, rgb, shape(x, y), *layer.FillOpacity, *layer.Opacity,
			grouped, layer.Effects.Items, func(effect psdeffects.Effect) float64 {
				effect.LayerConceals = false
				return reviewShadowCoverage(shape, effect, x, y)
			})
		coverage := 1.0
		if vectorGlobal {
			coverage *= vectorAt(x, y)
		}
		if userGlobal {
			coverage *= pixelAt(x, y, true)
		}
		return mixedReviewMix(background, painted, coverage)
	}
}

func TestFeatherShadowIndependentGaussianDualMaskStagesAndEffectOrder(t *testing.T) {
	policies := []struct {
		name     string
		flag     *bool
		unlinked bool
		global   bool
	}{
		{name: "nil-linked"},
		{name: "nil-unlinked", unlinked: true, global: true},
		{name: "false-unlinked", flag: new(false), unlinked: true},
		{name: "true-linked", flag: new(true), global: true},
	}
	pixelWeights := featherShadowQuadrature(1.02, 4)
	rectangle := image.Rect(2, 1, 7, 5)
	for _, radius := range []float64{.5, 1.5, 3} {
		vectorWeights := featherShadowVectorWeights(radius)
		for _, dual := range []bool{false, true} {
			for _, mixed := range []bool{false, true} {
				for _, invert := range []bool{false, true} {
					for _, grouped := range []bool{false, true} {
						for _, vectorPolicy := range policies {
							for _, userPolicy := range policies {
								name := fmt.Sprintf("r%g_dual%t_mixed%t_invert%t_grouped%t_v%s_u%s", radius,
									dual, mixed, invert, grouped, vectorPolicy.name, userPolicy.name)
								t.Run(name, func(t *testing.T) {
									document, layer := featherShadowFixture(radius, image.Rect(1, 1, 7, 5),
										rectangle, dual, invert, mixed, invert)
									layer.VectorMaskAsGlobal, layer.MaskAsGlobal = vectorPolicy.flag, userPolicy.flag
									layer.VectorMask.NotLinked, layer.BlendInterior = vectorPolicy.unlinked, new(grouped)
									if dual {
										layer.Mask.Relative = userPolicy.unlinked
									}
									input := bytes.Clone(layer.Pixels.Pix)
									actual, err := psdcompose.Render(t.Context(), document,
										psdcompose.Options{ApproximateDropShadow: true})
									if err != nil {
										t.Fatal(err)
									}
									want := featherShadowExpected(document, layer, rectangle, vectorWeights, pixelWeights,
										userPolicy.global, vectorPolicy.global, grouped)
									for y := range document.Height {
										for x := range document.Width {
											mixedReviewCompare(t, actual.NRGBAAt(x, y), want(x, y), x, y)
										}
									}
									if !bytes.Equal(input, layer.Pixels.Pix) {
										t.Fatal("calibrated feather/shadow changed caller-owned source pixels")
									}
								})
							}
						}
					}
				}
			}
		}
	}
}

func TestFeatherShadowOffCanvasAndRegionalDependenciesMatchIndependentReference(t *testing.T) {
	pixelWeights := featherShadowQuadrature(1.02, 4)
	for _, radius := range []float64{.5, 1.5, 3} {
		vectorWeights := featherShadowVectorWeights(radius)
		for _, dual := range []bool{false, true} {
			for _, vectorGlobal := range []bool{false, true} {
				for _, userGlobal := range []bool{false, true} {
					rectangle := image.Rect(-7, -4, 1, 5)
					document, layer := featherShadowFixture(radius, image.Rect(-6, -3, -1, 4),
						rectangle, dual, false, true, false)
					layer.VectorMaskAsGlobal, layer.MaskAsGlobal = new(vectorGlobal), new(userGlobal)
					layer.FillOpacity = new(0.0)
					for index := range layer.Effects.Items {
						if layer.Effects.Items[index].Kind == psdeffects.DropShadow {
							layer.Effects.Items[index].Distance, layer.Effects.Items[index].Angle = 8.25, 180
						}
					}
					actual, err := psdcompose.Render(t.Context(), document, psdcompose.Options{ApproximateDropShadow: true})
					if err != nil {
						t.Fatal(err)
					}
					want := featherShadowExpected(document, layer, rectangle, vectorWeights, pixelWeights,
						userGlobal, vectorGlobal, false)
					changed := false
					for y := range document.Height {
						for x := range document.Width {
							mixedReviewCompare(t, actual.NRGBAAt(x, y), want(x, y), x, y)
							changed = changed || actual.NRGBAAt(x, y) != (color.NRGBA{R: 73, G: 119, B: 211, A: 111})
						}
					}
					if !changed {
						t.Fatal("off-canvas calibrated vector source produced no visible shadow")
					}
					for _, region := range []image.Rectangle{image.Rect(0, 0, 3, 4), image.Rect(3, 1, 8, 6)} {
						tile, err := psdcompose.RenderRegion(t.Context(), document, region,
							psdcompose.Options{ApproximateDropShadow: true})
						if err != nil {
							t.Fatal(err)
						}
						for y := region.Min.Y; y < region.Max.Y; y++ {
							for x := region.Min.X; x < region.Max.X; x++ {
								if tile.NRGBAAt(x, y) != actual.NRGBAAt(x, y) {
									t.Fatalf("r%g dual%t user%t vector%t ROI lost off-canvas source at %d,%d",
										radius, dual, userGlobal, vectorGlobal, x, y)
								}
							}
						}
					}
				}
			}
		}
	}
}

func TestFeatherShadowSharedGeometryRadiusCachesPeakBudgetAndCancellation(t *testing.T) {
	options := psdcompose.Options{ApproximateDropShadow: true}
	for _, reverse := range []bool{false, true} {
		shared, mask := vectorCacheReviewDocument(true, reverse)
		separate, _ := vectorCacheReviewDocument(false, reverse)
		for index, layer := range shared.Layers {
			layer.VectorFeather, separate.Layers[index].VectorFeather = new([]float64{.5, 3, 1.5, 3}[index]),
				new([]float64{.5, 3, 1.5, 3}[index])
		}
		geometry := vectorShadowReviewMask(vectorShadowReviewRectangle{-6, -3, 7, 6}, shared.Width, shared.Height, true)
		*mask = *geometry
		for _, layer := range separate.Layers {
			layer.VectorMask = cloneCacheReviewVector(mask)
		}
		before := cloneCacheReviewVector(mask)
		expected, err := psdcompose.Render(t.Context(), separate, options)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := psdcompose.Render(t.Context(), shared, options)
		if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
			t.Fatalf("shared calibrated feather cache lost a radius or source domain: %v", err)
		}
		for _, region := range []image.Rectangle{image.Rect(0, 0, 4, 5), image.Rect(4, 2, 11, 8)} {
			tile, err := psdcompose.RenderRegion(t.Context(), shared, region, options)
			if err != nil {
				t.Fatal(err)
			}
			for y := region.Min.Y; y < region.Max.Y; y++ {
				for x := region.Min.X; x < region.Max.X; x++ {
					if tile.NRGBAAt(x, y) != expected.NRGBAAt(x, y) {
						t.Fatalf("shared calibrated feather ROI lost another source domain at %d,%d", x, y)
					}
				}
			}
		}
		if !reflect.DeepEqual(mask, before) {
			t.Fatal("calibrated feather planning changed caller-owned vector paths")
		}
		low, high := int64(1), int64(1<<20)
		for low < high {
			middle := low + (high-low)/2
			options.MaxWorkingBytes = middle
			if err := psdcompose.Validate(shared, options); err == nil {
				high = middle
			} else if errors.Is(err, psdcompose.ErrLimit) {
				low = middle + 1
			} else {
				t.Fatal(err)
			}
		}
		options.MaxWorkingBytes = low
		actual, err = psdcompose.Render(t.Context(), shared, options)
		if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
			t.Fatalf("accepted calibrated cache peak budget %d cannot render: %v", low, err)
		}
		options.MaxWorkingBytes = low - 1
		if output, err := psdcompose.Render(t.Context(), shared, options); output != nil ||
			!errors.Is(err, psdcompose.ErrLimit) {
			t.Fatalf("calibrated vector-shadow cache escaped one-byte peak budget: %v", err)
		}
		options.MaxWorkingBytes = 0
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if output, err := psdcompose.Render(ctx, shared, options); output != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("calibrated vector-shadow render ignored cancellation: %v", err)
		}
	}
}

func TestFeatherShadowFinalStageFarSourceAndOverflowStayBounded(t *testing.T) {
	document, layer := vectorCacheReviewFarSource()
	layer.VectorMask = vectorShadowReviewMask(vectorShadowReviewRectangle{-100_010, -10, 10, 10}, 2, 2, false)
	layer.VectorFeather = new(3.0)
	options := psdcompose.Options{ApproximateDropShadow: true, MaxWorkingBytes: 64 << 10}
	window := image.Rect(0, 0, 1, 1)
	for _, global := range []*bool{new(true), nil} {
		layer.VectorMaskAsGlobal, layer.VectorMask.NotLinked = global, true
		output, err := psdcompose.RenderRegion(t.Context(), document, window, options)
		if err != nil || output.NRGBAAt(0, 0) != (color.NRGBA{R: 255, A: 255}) {
			t.Fatalf("final feather mask reserved the distant source domain or lost its shadow: %v", err)
		}
	}
	layer.VectorMaskAsGlobal = new(false)
	if output, err := psdcompose.RenderRegion(t.Context(), document, window, options); output != nil ||
		!errors.Is(err, psdcompose.ErrLimit) {
		t.Fatalf("source-shaped feather escaped its full-domain budget: %v", err)
	}
	layer.VectorDensity = new(uint8(0))
	if output, err := psdcompose.RenderRegion(t.Context(), document, window, options); err != nil ||
		output.NRGBAAt(0, 0) != (color.NRGBA{R: 255, A: 255}) {
		t.Fatalf("zero vector density unnecessarily reserved distant feather coverage: %v", err)
	}
	layer.VectorDensity = nil
	empty := image.Rect(1, 0, 1, 1)
	if output, err := psdcompose.RenderRegion(t.Context(), document, empty, options); err != nil ||
		output.Rect != empty || len(output.Pix) != 0 {
		t.Fatalf("empty calibrated feather ROI expanded its source domain: %v", err)
	}
	for _, origin := range []int{math.MinInt + 4, math.MaxInt - 4} {
		layer.Bounds, layer.Effects.Items[0].Distance = image.Rect(origin, 0, origin+2, 2), 0
		if output, err := psdcompose.RenderRegion(t.Context(), document, window, options); output != nil ||
			!errors.Is(err, psdcompose.ErrInvalid) && !errors.Is(err, psdcompose.ErrLimit) {
			t.Fatalf("calibrated feather source expansion overflow was not classified: %v", err)
		}
	}
}

func TestFeatherShadowDensityDisabledMasksFillAndOpacityIndependentReference(t *testing.T) {
	vectorWeights, pixelWeights := featherShadowVectorWeights(1.5), featherShadowQuadrature(1.02, 4)
	rectangle := image.Rect(2, 1, 7, 5)
	for _, density := range []*uint8{nil, new(uint8(0)), new(uint8(255))} {
		for _, vectorDisabled := range []bool{false, true} {
			for _, userDisabled := range []bool{false, true} {
				for _, fill := range []float64{0, .37, 1} {
					for _, opacity := range []float64{0, .61, 1} {
						for _, vectorGlobal := range []bool{false, true} {
							document, layer := featherShadowFixture(1.5, image.Rect(1, 1, 7, 5), rectangle,
								true, true, true, false)
							layer.VectorDensity, layer.Mask.Density = density, density
							layer.VectorMask.Disabled, layer.Mask.Disabled = vectorDisabled, userDisabled
							layer.VectorMaskAsGlobal, layer.MaskAsGlobal = new(vectorGlobal), new(!vectorGlobal)
							layer.FillOpacity, layer.Opacity = new(fill), new(opacity)
							input, maskInput := bytes.Clone(layer.Pixels.Pix), bytes.Clone(layer.Mask.Pixels.Pix)
							output, err := psdcompose.Render(t.Context(), document,
								psdcompose.Options{ApproximateDropShadow: true})
							if err != nil {
								t.Fatal(err)
							}
							want := featherShadowExpected(document, layer, rectangle, vectorWeights, pixelWeights,
								!vectorGlobal, vectorGlobal, false)
							for y := range document.Height {
								for x := range document.Width {
									mixedReviewCompare(t, output.NRGBAAt(x, y), want(x, y), x, y)
								}
							}
							if !bytes.Equal(input, layer.Pixels.Pix) || !bytes.Equal(maskInput, layer.Mask.Pixels.Pix) {
								t.Fatal("feather shadow changed borrowed source or pixel-mask bytes")
							}
						}
					}
				}
			}
		}
	}
}

func TestFeatherShadowExplicitPolicyAndUnsupportedBoundaries(t *testing.T) {
	mutations := []func(*psdmodel.Layer){
		func(layer *psdmodel.Layer) { layer.VectorFeather = new(2.0) },
		func(layer *psdmodel.Layer) { layer.VectorMask.Subpaths[0].Knots[0].Leaving.X++ },
		func(layer *psdmodel.Layer) { layer.VectorMask.Subpaths[0].Knots[0].Anchor.X += .25 },
		func(layer *psdmodel.Layer) { layer.VectorMask.Unsupported = []string{"unknown feather geometry"} },
		func(layer *psdmodel.Layer) { layer.VectorMask.Flags = 16 },
		func(layer *psdmodel.Layer) { layer.Mask.Unsupported = []string{"unknown retained mask flags"} },
		func(layer *psdmodel.Layer) { layer.BlendMode = "mul " },
		func(layer *psdmodel.Layer) { layer.Placed = &psdmodel.Placed{Type: "raster"} },
		func(layer *psdmodel.Layer) { layer.TransparencyShapes = new(false) },
		func(layer *psdmodel.Layer) {
			layer.Effects.Items = append(layer.Effects.Items,
				psdeffects.Effect{Kind: psdeffects.InnerShadow, Enabled: true, BlendMode: "norm", Opacity: 1})
		},
	}
	for index, mutate := range mutations {
		document, layer := featherShadowFixture(1.5, image.Rect(1, 1, 7, 5), image.Rect(2, 1, 7, 5),
			true, false, true, false)
		mutate(layer)
		for _, options := range []psdcompose.Options{{}, {ApproximateDropShadow: true}} {
			if output, err := psdcompose.Render(t.Context(), document, options); output != nil ||
				!errors.Is(err, psdcompose.ErrUnsupported) {
				t.Fatalf("uncalibrated vector-shadow mutation %d returned partial pixels: %v", index, err)
			}
		}
	}
	document, _ := featherShadowFixture(3, image.Rect(1, 1, 7, 5), image.Rect(2, 1, 7, 5),
		true, false, true, false)
	if output, err := psdcompose.Render(t.Context(), document); output != nil ||
		!errors.Is(err, psdcompose.ErrUnsupported) {
		t.Fatalf("calibrated feather/shadow enabled approximation without explicit policy: %v", err)
	}
	options := psdcompose.Options{ApproximateDropShadow: true, MaxFeatherRadius: 1.5}
	if output, err := psdcompose.Render(t.Context(), document, options); output != nil ||
		!errors.Is(err, psdcompose.ErrLimit) {
		t.Fatalf("calibrated vector-shadow bypassed vector feather radius limit: %v", err)
	}
}
