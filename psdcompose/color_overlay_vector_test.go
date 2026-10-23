package psdcompose

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func vectorOverlayFixture(dual, invert bool) (*psdmodel.Document, *psdmodel.Layer) {
	bounds := image.Rect(1, 1, 9, 5)
	layer := composeTestLayer(bounds, color.NRGBA{})
	for index := range 32 {
		layer.Pixels.SetNRGBA(index%8, index/8, color.NRGBA{
			R: byte(19 + index*73), G: byte(101 + index*151), B: byte(7 + index*43), A: byte(index * 53),
		})
	}
	mask := &psdvector.Mask{Version: 3, Width: 10, Height: 6, Invert: invert, HasInitialFill: true,
		Subpaths: []psdvector.Subpath{{Closed: true, Operation: 1, Unknown1: 1}}}
	for _, point := range []psdvector.Point{{X: 2.25, Y: 1.5}, {X: 7.5, Y: 1.5},
		{X: 7.5, Y: 4.25}, {X: 2.25, Y: 4.25}} {
		mask.Subpaths[0].Knots = append(mask.Subpaths[0].Knots,
			psdvector.Knot{Anchor: point, Preceding: point, Leaving: point})
	}
	layer.VectorMask, layer.VectorDensity = mask, new(uint8(191))
	layer.FillOpacity, layer.Opacity = new(74.0/255), new(186.0/255)
	if dual {
		pixels := image.NewNRGBA(image.Rect(0, 2, 8, 5))
		for y := pixels.Rect.Min.Y; y < pixels.Rect.Max.Y; y++ {
			for x := pixels.Rect.Min.X; x < pixels.Rect.Max.X; x++ {
				pixels.SetNRGBA(x, y, color.NRGBA{R: byte(x*31 + y*67 + 257), A: 255})
			}
		}
		layer.Mask = &psdmodel.Mask{Bounds: pixels.Rect, Pixels: pixels, DefaultColor: new(uint8(11)),
			Invert: true, Density: new(uint8(173))}
	}
	first, second := coreEffect(psdeffects.ColorOverlay), coreEffect(psdeffects.ColorOverlay)
	first.Color, first.Opacity = [3]float64{173.0 / 255, 59.0 / 255, 211.0 / 255}, .63
	second.Color, second.Opacity, second.BlendMode = [3]float64{20.0 / 255, 220.0 / 255, 80.0 / 255}, .37, "mul "
	layer.Effects = coreEffectSet(first, second)
	backdrop := composeTestLayer(image.Rect(0, 0, 10, 6), color.NRGBA{R: 73, G: 119, B: 211, A: 167})
	return &psdmodel.Document{Width: 10, Height: 6, Layers: []*psdmodel.Layer{backdrop, layer}}, layer
}

func vectorOverlayReferenceCoverage(x, y int, dual, invert bool) float64 {
	// An independent 8x8 point-in-rectangle count gives the established vector rasterizer's byte coverage.
	inside := 0
	for row := range 8 {
		for column := range 8 {
			px, py := float64(x)+(float64(column)+.5)/8, float64(y)+(float64(row)+.5)/8
			covered := px >= 2.25 && px < 7.5 && py >= 1.5 && py < 4.25
			if covered != invert {
				inside++
			}
		}
	}
	alpha := float64((inside*255 + 32) / 64)
	coverage := 1 - (1-alpha/255)*191/255
	if dual {
		stored := byte(11)
		if image.Pt(x, y).In(image.Rect(0, 2, 8, 5)) {
			stored = byte(x*31 + y*67 + 257)
		}
		coverage *= 1 - float64(stored)/255*173/255
	}
	return coverage
}

func vectorOverlayReferenceBlend(mode string, backdrop, source float64) float64 {
	switch mode {
	case "mul ":
		return backdrop * source
	case "scrn":
		return backdrop + source - backdrop*source
	case "over":
		if backdrop <= .5 {
			return 2 * backdrop * source
		}
		return 1 - 2*(1-backdrop)*(1-source)
	default:
		return source
	}
}

func vectorOverlayReferenceOver(backdrop, source [4]float64, mode string) [4]float64 {
	alpha := source[3] + backdrop[3]*(1-source[3])
	if alpha == 0 {
		return [4]float64{}
	}
	result := [4]float64{0, 0, 0, alpha}
	for component := range 3 {
		if mode == "diff" {
			// The existing Difference contract modulates the source before absolute difference, like special Fill.
			result[component] = (backdrop[3]*math.Abs(backdrop[component]-source[3]*source[component]) +
				(1-backdrop[3])*source[3]*source[component]) / alpha
			continue
		}
		blend := vectorOverlayReferenceBlend(mode, backdrop[component], source[component])
		result[component] = ((1-source[3])*backdrop[3]*backdrop[component] +
			(1-backdrop[3])*source[3]*source[component] + source[3]*backdrop[3]*blend) / alpha
	}
	return result
}

func vectorOverlayReferencePixel(layer *psdmodel.Layer, x, y int, coverage float64, interior bool) color.NRGBA {
	backdrop := [4]float64{73.0 / 255, 119.0 / 255, 211.0 / 255, 167.0 / 255}
	if !image.Pt(x, y).In(layer.Bounds) {
		return color.NRGBA{R: 73, G: 119, B: 211, A: 167}
	}
	pixel := layer.Pixels.NRGBAAt(x-layer.Bounds.Min.X, y-layer.Bounds.Min.Y)
	source := [4]float64{float64(pixel.R) / 255, float64(pixel.G) / 255,
		float64(pixel.B) / 255, 74.0 / 255}
	painted := vectorOverlayReferenceOver(backdrop, source, "norm")
	if interior {
		painted = source
		painted[3] = 1
	}
	// Retained interior effects are ordered top-first; reference composition paints the bottom entry first.
	for _, effect := range slices.Backward(layer.Effects.Items) {
		painted = vectorOverlayReferenceOver(painted,
			[4]float64{effect.Color[0], effect.Color[1], effect.Color[2], effect.Opacity}, effect.BlendMode)
	}
	amount := float64(pixel.A) / 255 * (186.0 / 255) * coverage
	if interior {
		painted[3] = amount * (74.0 / 255)
		painted = vectorOverlayReferenceOver(backdrop, painted, "norm")
	} else {
		alpha := backdrop[3]*(1-amount) + painted[3]*amount
		for component := range 3 {
			painted[component] = (backdrop[component]*backdrop[3]*(1-amount) +
				painted[component]*painted[3]*amount) / alpha
		}
		painted[3] = alpha
	}
	return color.NRGBA{R: byte(math.Round(painted[0] * 255)), G: byte(math.Round(painted[1] * 255)),
		B: byte(math.Round(painted[2] * 255)), A: byte(math.Round(painted[3] * 255))}
}

func TestVectorMaskedOverlayIndependentPremultipliedReferenceAndROI(t *testing.T) {
	for _, mode := range []string{"norm", "mul ", "scrn", "over", "diff"} {
		for _, dual := range []bool{false, true} {
			for _, invert := range []bool{false, true} {
				document, layer := vectorOverlayFixture(dual, invert)
				layer.Effects.Items[0].BlendMode = mode
				original := bytes.Clone(layer.Pixels.Pix)
				for _, interior := range []bool{false, true} {
					layer.BlendInterior = &interior
					for _, pixelGlobal := range []*bool{nil, new(false), new(true)} {
						for _, vectorGlobal := range []*bool{nil, new(false), new(true)} {
							layer.MaskAsGlobal, layer.VectorMaskAsGlobal = pixelGlobal, vectorGlobal
							actual, err := Render(t.Context(), document)
							if err != nil {
								t.Fatal(err)
							}
							region := image.Rect(2, 1, 8, 5)
							tile, err := RenderRegion(t.Context(), document, region)
							if err != nil {
								t.Fatal(err)
							}
							for y := range document.Height {
								for x := range document.Width {
									want := vectorOverlayReferencePixel(layer, x, y,
										vectorOverlayReferenceCoverage(x, y, dual, invert), interior)
									got := actual.NRGBAAt(x, y)
									for component, pair := range [][2]byte{{got.R, want.R}, {got.G, want.G}, {got.B, want.B}, {got.A, want.A}} {
										if effectByteDifference(pair[0], pair[1]) > 1 {
											t.Fatalf("mode %s dual=%t invert=%t interior=%t pixel(%d,%d) channel%d: %v != %v",
												mode, dual, invert, interior, x, y, component, got, want)
										}
									}
									if image.Pt(x, y).In(region) && tile.NRGBAAt(x, y) != got {
										t.Fatal("vector-overlay ROI changed mask origin or coverage stages")
									}
								}
							}
						}
					}
				}
				if !bytes.Equal(layer.Pixels.Pix, original) || layer.VectorMask.Invert != invert {
					t.Fatal("vector-mask effect painting modified source pixels or geometry")
				}
			}
		}
	}
}

func TestVectorMaskedOverlayCoverageBudgetCancellationAndDefaults(t *testing.T) {
	for _, dual := range []bool{false, true} {
		document, layer := vectorOverlayFixture(dual, false)
		if result, err := Render(t.Context(), document, Options{MaxWorkingBytes: 256}); result != nil || !errors.Is(err, ErrLimit) {
			t.Fatalf("vector/dual overlay bypassed working storage limits: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if result, err := Render(ctx, document); result != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("vector/dual overlay ignored cancellation: %v", err)
		}
		vector := layer.VectorMask
		layer.VectorMask = nil
		baseline, err := Render(t.Context(), document)
		if err != nil {
			t.Fatal(err)
		}
		for _, disabled := range []bool{false, true} {
			layer.VectorMask = vector
			layer.VectorMask.Disabled = disabled
			layer.VectorDensity = new(uint8(0))
			actual, err := Render(t.Context(), document)
			if err != nil || !bytes.Equal(actual.Pix, baseline.Pix) {
				t.Fatalf("inactive vector density or disabled vector changed overlay output: %v", err)
			}
		}
	}
	document, layer := vectorOverlayFixture(false, false)
	layer.VectorMask.Subpaths = nil
	layer.VectorDensity = nil
	for _, fill := range []bool{false, true} {
		layer.VectorMask.InitialFill = fill
		pixels, err := Render(t.Context(), document)
		if err != nil {
			t.Fatal(err)
		}
		layer.VectorMask = nil
		expected, err := Render(t.Context(), document)
		if err != nil {
			t.Fatal(err)
		}
		if !fill {
			copy(expected.Pix, document.Layers[0].Pixels.Pix)
		}
		if !bytes.Equal(pixels.Pix, expected.Pix) {
			t.Fatal("constant vector fill did not affect body and overlay together")
		}
		layer.VectorMask = &psdvector.Mask{Version: 3, Width: document.Width, Height: document.Height,
			HasInitialFill: true}
	}
}

func TestVectorMaskedOverlayKeepsUnsupportedSemanticsRejected(t *testing.T) {
	for _, mutate := range []func(*psdmodel.Layer){
		func(layer *psdmodel.Layer) { layer.VectorMask.Unsupported = []string{"unknown vector semantics"} },
		func(layer *psdmodel.Layer) { layer.VectorMask.Flags = 8 },
		func(layer *psdmodel.Layer) { layer.VectorMask.Version = 99 },
		func(layer *psdmodel.Layer) { layer.VectorMask.Subpaths[0].Operation = 7 },
		func(layer *psdmodel.Layer) { layer.Mask.Unsupported = []string{"unknown pixel mask"} },
		func(layer *psdmodel.Layer) { layer.VectorFeather = new(.5) },
		func(layer *psdmodel.Layer) { layer.Mask.Feather = new(1.0) },
		func(layer *psdmodel.Layer) { layer.Clipping = true },
		func(layer *psdmodel.Layer) { layer.Effects.Items[0].Unsupported = []string{"ambiguous effect opacity"} },
		func(layer *psdmodel.Layer) {
			layer.Effects.Items = append(layer.Effects.Items, coreEffect(psdeffects.InnerShadow))
		},
	} {
		document, layer := vectorOverlayFixture(true, false)
		mutate(layer)
		if result, err := Render(t.Context(), document); result != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatalf("pure vector-overlay scope admitted unsupported metadata or other active semantics: %v", err)
		}
	}
	document, layer := vectorOverlayFixture(true, false)
	layer.VectorMask.Subpaths[0].Knots[0].Anchor.X = math.NaN()
	if result, err := Render(t.Context(), document); result != nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("vector-overlay coverage hid a nonfinite coordinate: %v", err)
	}
	document, layer = vectorOverlayFixture(true, false)
	layer.VectorMask.Unsupported = []string{"unknown inactive vector metadata"}
	layer.VectorDensity = new(uint8(0))
	if err := Validate(document); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("vector density zero hid unknown vector-effect metadata: %v", err)
	}
	if !reflect.DeepEqual(layer.VectorMask.Unsupported, []string{"unknown inactive vector metadata"}) {
		t.Fatal("rejected vector qualification changed caller metadata")
	}
}
