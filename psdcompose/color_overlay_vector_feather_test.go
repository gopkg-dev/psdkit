package psdcompose

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// overlayFeatherReferenceWeights independently integrates the calibrated pixel-area Gaussian.
// It deliberately uses quadrature and explicit calibration constants instead of any production feather helper.
func overlayFeatherReferenceWeights(radius float64) []float64 {
	sigma, support := .565, 2
	switch radius {
	case 1.5:
		sigma, support = 1.53, 4
	case 3:
		sigma, support = 3.06, 7
	}
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
		weight := integrate(-1, 0) + integrate(0, 1)
		weights[offset+support] = weight
		total += weight
	}
	for index := range weights {
		weights[index] /= total
	}
	return weights
}

func overlayFeatherReferenceCoverage(
	weights []float64, bounds image.Rectangle, inverted bool, density *uint8, x, y int,
) float64 {
	if density != nil && *density == 0 {
		return 1
	}
	support, coverage := len(weights)/2, 0.0
	for row, yWeight := range weights {
		for column, xWeight := range weights {
			if image.Pt(x+column-support, y+row-support).In(bounds) {
				coverage += xWeight * yWeight
			}
		}
	}
	if inverted {
		coverage = 1 - coverage
	}
	if density != nil {
		coverage = 1 - (1-coverage)*float64(*density)/255
	}
	return coverage
}

func TestFeatheredVectorOverlayIndependentGaussianPremultipliedReferenceAndROI(t *testing.T) {
	for _, radius := range []float64{.5, 1.5, 3} {
		weights := overlayFeatherReferenceWeights(radius)
		for _, bounds := range []image.Rectangle{image.Rect(2, 1, 8, 5), image.Rect(-3, -2, 6, 4)} {
			for _, inverted := range []bool{false, true} {
				for _, density := range []*uint8{nil, new(uint8(137)), new(uint8(0))} {
					document, layer := vectorOverlayFixture(false, inverted)
					layer.VectorMask = vectorFeatherTestMask(bounds)
					layer.VectorMask.Width, layer.VectorMask.Height = document.Width, document.Height
					layer.VectorMask.Invert, layer.VectorDensity, layer.VectorFeather = inverted, density, new(radius)
					original := bytes.Clone(layer.Pixels.Pix)
					for _, mode := range []string{"norm", "mul ", "diff"} {
						layer.Effects.Items[0].BlendMode = mode
						for _, interior := range []bool{false, true} {
							layer.BlendInterior = &interior
							for _, vectorGlobal := range []*bool{nil, new(false), new(true)} {
								for _, pixelGlobal := range []*bool{nil, new(true)} {
									layer.VectorMaskAsGlobal, layer.MaskAsGlobal = vectorGlobal, pixelGlobal
									actual, err := Render(t.Context(), document)
									if err != nil {
										t.Fatal(err)
									}
									region := image.Rect(1, 2, 8, 5)
									tile, err := RenderRegion(t.Context(), document, region)
									if err != nil {
										t.Fatal(err)
									}
									for y := range document.Height {
										for x := range document.Width {
											coverage := overlayFeatherReferenceCoverage(weights, bounds, inverted, density, x, y)
											want := vectorOverlayReferencePixel(layer, x, y, coverage, interior)
											got := actual.NRGBAAt(x, y)
											for component, pair := range [][2]byte{
												{got.R, want.R}, {got.G, want.G}, {got.B, want.B}, {got.A, want.A},
											} {
												if effectByteDifference(pair[0], pair[1]) > 1 {
													t.Fatalf("radius=%g bounds=%v inverted=%t mode=%s interior=%t at %d,%d channel %d: %v != %v",
														radius, bounds, inverted, mode, interior, x, y, component, got, want)
												}
											}
											if image.Pt(x, y).In(region) && tile.NRGBAAt(x, y) != got {
												t.Fatal("feathered-overlay ROI changed Gaussian halo or mask/effect stages")
											}
										}
									}
								}
							}
						}
					}
					if !bytes.Equal(layer.Pixels.Pix, original) || layer.VectorMask.Invert != inverted {
						t.Fatal("feathered overlay mutated borrowed source pixels or vector metadata")
					}
				}
			}
		}
	}
}

func TestFeatheredVectorOverlayRetainsNativeAlphaSamples(t *testing.T) {
	points := []image.Point{image.Pt(0, 0), image.Pt(3, 8), image.Pt(4, 8), image.Pt(15, 8), image.Pt(9, 7)}
	for _, test := range []struct {
		radius   float64
		expected []byte
	}{
		{.5, []byte{0, 56, 199, 255, 255}},
		{1.5, []byte{0, 95, 160, 255, 255}},
		{3, []byte{7, 101, 130, 231, 224}},
	} {
		document, layer := vectorFeatherTestDocument(test.radius)
		layer.Effects = coreEffectSet(coreEffect(psdeffects.ColorOverlay))
		actual, err := Render(t.Context(), document)
		if err != nil {
			t.Fatal(err)
		}
		for index, point := range points {
			if effectByteDifference(actual.NRGBAAt(point.X, point.Y).A, test.expected[index]) > 2 {
				t.Fatalf("radius %g overlay changed native rectangle feather alpha at %v", test.radius, point)
			}
		}
	}
}

func TestFeatheredVectorOverlayZeroFillAndLayerOpacityReference(t *testing.T) {
	weights, bounds := overlayFeatherReferenceWeights(1.5), image.Rect(2, 1, 8, 5)
	document, layer := vectorOverlayFixture(false, false)
	layer.VectorMask, layer.VectorFeather = vectorFeatherTestMask(bounds), new(1.5)
	layer.VectorMask.Width, layer.VectorMask.Height = document.Width, document.Height
	for _, fill := range []float64{0, .29, 1} {
		layer.FillOpacity = &fill
		for _, opacity := range []float64{.17, 1} {
			layer.Opacity = &opacity
			for _, interior := range []bool{false, true} {
				layer.BlendInterior = &interior
				for _, global := range []bool{false, true} {
					layer.VectorMaskAsGlobal = &global
					actual, err := Render(t.Context(), document)
					if err != nil {
						t.Fatal(err)
					}
					for y := range document.Height {
						for x := range document.Width {
							coverage := overlayFeatherReferenceCoverage(weights, bounds, false, layer.VectorDensity, x, y)
							want := vectorOverlayReferencePixel(layer, x, y, coverage, interior)
							got := actual.NRGBAAt(x, y)
							for _, pair := range [][2]byte{{got.R, want.R}, {got.G, want.G}, {got.B, want.B}, {got.A, want.A}} {
								if effectByteDifference(pair[0], pair[1]) > 1 {
									t.Fatalf("Fill=%g Opacity=%g interior=%t global=%t at %d,%d: %v != %v",
										fill, opacity, interior, global, x, y, got, want)
								}
							}
						}
					}
				}
			}
		}
	}
}

func TestFeatheredVectorOverlayPeakBudgetCancellationAndDisabledPixelMask(t *testing.T) {
	document, layer := vectorFeatherTestDocument(1.5)
	layer.Effects = coreEffectSet(coreEffect(psdeffects.ColorOverlay))
	second := composeTestLayer(layer.Bounds, color.NRGBA{B: 255, A: 173})
	second.VectorMask, second.VectorFeather = layer.VectorMask, new(3.0)
	second.Effects = coreEffectSet(coreEffect(psdeffects.ColorOverlay))
	document.Layers = append(document.Layers, second)
	baseline, err := Render(t.Context(), document)
	if err != nil {
		t.Fatal(err)
	}
	low, high := int64(1), int64(1<<20)
	for low < high {
		middle := low + (high-low)/2
		if err := Validate(document, Options{MaxWorkingBytes: middle}); err == nil {
			high = middle
		} else if errors.Is(err, ErrLimit) {
			low = middle + 1
		} else {
			t.Fatal(err)
		}
	}
	if actual, err := Render(t.Context(), document, Options{MaxWorkingBytes: low}); err != nil ||
		!bytes.Equal(actual.Pix, baseline.Pix) {
		t.Fatalf("effect planes and shared-geometry feather peak cannot render at accepted budget %d: %v", low, err)
	}
	if actual, err := Render(t.Context(), document, Options{MaxWorkingBytes: low - 1}); actual != nil ||
		!errors.Is(err, ErrLimit) {
		t.Fatalf("effect planes and feather caches escaped the peak budget: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if actual, err := Render(ctx, document); actual != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("feathered overlay ignored cancellation: %v", err)
	}
	plan, err := preparePlan(document, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.prepareVectors(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("feathered-overlay vector preparation ignored cancellation: %v", err)
	}
	if err := plan.prepareEffects(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("feathered-overlay effect preparation ignored cancellation: %v", err)
	}
	layer.Mask = &psdmodel.Mask{Disabled: true, Invert: true, Feather: new(1000.0),
		Unsupported: []string{"retained inactive pixel-mask semantics"}}
	actual, err := Render(t.Context(), document, Options{MaxWorkingBytes: low})
	if err != nil || !bytes.Equal(actual.Pix, baseline.Pix) {
		t.Fatalf("disabled pixel mask changed calibrated overlay or working peak: %v", err)
	}
}

func TestFeatheredVectorOverlayZeroOpacityDisabledAndHiddenEffectsRetainOrdinaryFeather(t *testing.T) {
	for _, disable := range []func(*psdeffects.Set){
		func(set *psdeffects.Set) { set.Items[0].Opacity = 0 },
		func(set *psdeffects.Set) { set.Items[0].Enabled = false },
		func(set *psdeffects.Set) { set.Enabled = false },
		func(set *psdeffects.Set) { set.EffectsVisible = false },
	} {
		document, layer := vectorFeatherTestDocument(1.5)
		baseline, err := Render(t.Context(), document)
		if err != nil {
			t.Fatal(err)
		}
		layer.Effects = coreEffectSet(coreEffect(psdeffects.ColorOverlay))
		active, err := Render(t.Context(), document)
		if err != nil {
			t.Fatal(err)
		}
		disable(layer.Effects)
		actual, err := Render(t.Context(), document)
		if err != nil || !bytes.Equal(actual.Pix, baseline.Pix) {
			t.Fatalf("inactive overlay changed established ordinary vector feather: %v", err)
		}
		layer.Effects = coreEffectSet(coreEffect(psdeffects.ColorOverlay))
		restored, err := Render(t.Context(), document)
		if err != nil || !bytes.Equal(restored.Pix, active.Pix) {
			t.Fatalf("reactivating a retained overlay did not restore the calibrated render: %v", err)
		}
	}
}

func TestFeatheredVectorOverlayPreservesUnsupportedBoundaries(t *testing.T) {
	for index, mutate := range []func(*psdmodel.Layer){
		func(layer *psdmodel.Layer) { layer.VectorFeather = new(2.0) },
		func(layer *psdmodel.Layer) { layer.VectorMask.Subpaths = nil },
		func(layer *psdmodel.Layer) { layer.VectorMask.Subpaths[0].Knots[0].Leaving.X++ },
		func(layer *psdmodel.Layer) { layer.VectorMask.Subpaths[0].Knots[0].Anchor.X += .25 },
		func(layer *psdmodel.Layer) { layer.VectorMask.Flags = 8 },
		func(layer *psdmodel.Layer) { layer.VectorMask.Unsupported = []string{"unrecognized geometry"} },
		func(layer *psdmodel.Layer) { layer.VectorMask.Version = 99 },
		func(layer *psdmodel.Layer) {
			layer.VectorMask.Unsupported = []string{"unknown inactive vector metadata"}
			layer.VectorDensity = new(uint8(0))
		},
		func(layer *psdmodel.Layer) {
			layer.Effects.Enabled = false
			layer.Effects.Unsupported = []string{"unknown dormant effects"}
			layer.VectorDensity = new(uint8(0))
		},
		func(layer *psdmodel.Layer) {
			layer.Effects.EffectsVisible = false
			layer.VectorMask.Unsupported = []string{"unknown dormant vector geometry"}
			layer.VectorDensity = new(uint8(0))
		},
		func(layer *psdmodel.Layer) {
			layer.Effects.Items[0].Enabled = false
			layer.Effects.Items[0].Unsupported = []string{"unknown dormant overlay"}
		},
		func(layer *psdmodel.Layer) { layer.Mask = &psdmodel.Mask{} },
		func(layer *psdmodel.Layer) { layer.BlendMode = "mul " },
		func(layer *psdmodel.Layer) { layer.TransparencyShapes = new(false) },
		func(layer *psdmodel.Layer) {
			layer.Effects.Items = append(layer.Effects.Items, coreEffect(psdeffects.InnerShadow))
		},
	} {
		document, layer := vectorFeatherTestDocument(1.5)
		layer.Effects = coreEffectSet(coreEffect(psdeffects.ColorOverlay))
		mutate(layer)
		for _, options := range []Options{{}, {ApproximateDropShadow: true}} {
			if actual, err := Render(t.Context(), document, options); actual != nil || !errors.Is(err, ErrUnsupported) {
				t.Fatalf("uncalibrated feathered-overlay case %d was admitted: %v", index, err)
			}
		}
	}
	for _, mixed := range []bool{false, true} {
		document, layer := vectorFeatherTestDocument(1.5)
		layer.Effects = coreEffectSet(coreEffect(psdeffects.DropShadow))
		if mixed {
			layer.Effects.Items = append(layer.Effects.Items, coreEffect(psdeffects.ColorOverlay))
		}
		if actual, err := Render(t.Context(), document); actual != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatalf("calibrated feather shadow enabled approximation by default: %v", err)
		}
		if _, err := Render(t.Context(), document, Options{ApproximateDropShadow: true}); err != nil {
			t.Fatalf("explicit calibrated feather shadow remained blocked: %v", err)
		}
		layer.VectorFeather = new(2.0)
		if actual, err := Render(t.Context(), document, Options{ApproximateDropShadow: true}); actual != nil ||
			!errors.Is(err, ErrUnsupported) {
			t.Fatalf("explicit shadow widened the calibrated feather radius: %v", err)
		}
	}
	document, layer := vectorFeatherTestDocument(3)
	layer.Effects = coreEffectSet(coreEffect(psdeffects.ColorOverlay))
	if actual, err := Render(t.Context(), document, Options{MaxFeatherRadius: 1.5}); actual != nil ||
		!errors.Is(err, ErrLimit) {
		t.Fatalf("feathered overlay bypassed the configured radius limit: %v", err)
	}
}
