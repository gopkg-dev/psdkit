package psdcompose_test

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

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func cloneCacheReviewVector(mask *psdvector.Mask) *psdvector.Mask {
	owned := *mask
	owned.Subpaths = slices.Clone(mask.Subpaths)
	for index := range owned.Subpaths {
		owned.Subpaths[index].Knots = slices.Clone(mask.Subpaths[index].Knots)
	}
	return &owned
}

func vectorCacheReviewDocument(shared, reverse bool) (*psdmodel.Document, *psdvector.Mask) {
	mask := vectorShadowReviewMask(vectorShadowReviewRectangle{-6.25, -2.5, 6.5, 5.25}, 18, 10, true)
	ordinary := reviewShadowLayer(image.Rect(1, 1, 7, 6))
	first := reviewShadowLayer(image.Rect(-6, -2, 1, 5))
	final := reviewShadowLayer(image.Rect(10, -3, 16, 4))
	second := reviewShadowLayer(image.Rect(9, -4, 16, 3))
	layers := []*psdmodel.Layer{ordinary, first, final, second}
	for index, layer := range layers {
		layer.VectorMask = mask
		if !shared {
			layer.VectorMask = cloneCacheReviewVector(mask)
		}
		layer.VectorDensity = new([]uint8{177, 201, 121, 97}[index])
		layer.Opacity = new(.73)
	}
	first.VectorMaskAsGlobal, second.VectorMaskAsGlobal, final.VectorMaskAsGlobal = new(false), new(false), new(true)
	first.Effects = reviewShadowSet(psdeffects.Effect{Kind: psdeffects.DropShadow, Enabled: true,
		BlendMode: "norm", Opacity: .63, Color: [3]float64{.7, .1, .4}, Size: .75, Distance: 9,
		Angle: 180, LayerConceals: true})
	final.Effects = reviewShadowSet(psdeffects.Effect{Kind: psdeffects.DropShadow, Enabled: true,
		BlendMode: "mul ", Opacity: .41, Color: [3]float64{.1, .8, .3}, Size: .5, Distance: 5})
	second.Effects = reviewShadowSet(psdeffects.Effect{Kind: psdeffects.DropShadow, Enabled: true,
		BlendMode: "norm", Opacity: .58, Color: [3]float64{.2, .3, .9}, Size: 1.25, Distance: 6,
		LayerConceals: true}, psdeffects.Effect{Kind: psdeffects.ColorOverlay, Enabled: true,
		BlendMode: "scrn", Opacity: .36, Color: [3]float64{.8, .4, .2}})
	if reverse {
		slices.Reverse(layers)
	}
	return &psdmodel.Document{Width: 18, Height: 10, Layers: layers}, mask
}

func TestVectorShadowCacheReviewSharedPointerMatchesIndependentPerLayerCoverage(t *testing.T) {
	options := psdcompose.Options{ApproximateDropShadow: true}
	for _, reverse := range []bool{false, true} {
		shared, mask := vectorCacheReviewDocument(true, reverse)
		separate, _ := vectorCacheReviewDocument(false, reverse)
		before := cloneCacheReviewVector(mask)
		expected, err := psdcompose.Render(t.Context(), separate, options)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := psdcompose.Render(t.Context(), shared, options)
		if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
			t.Fatalf("sharing vector geometry lost a source domain or changed a normal/final-mask sibling: %v", err)
		}
		for _, region := range []image.Rectangle{
			image.Rect(0, 0, 4, 5), image.Rect(4, 2, 11, 8), image.Rect(12, 0, 18, 10),
		} {
			partial, err := psdcompose.RenderRegion(t.Context(), shared, region, options)
			if err != nil {
				t.Fatal(err)
			}
			for y := region.Min.Y; y < region.Max.Y; y++ {
				for x := region.Min.X; x < region.Max.X; x++ {
					if partial.NRGBAAt(x, y) != expected.NRGBAAt(x, y) {
						t.Fatalf("shared vector ROI misses another source bounds at %d,%d", x, y)
					}
				}
			}
		}
		if !reflect.DeepEqual(mask, before) {
			t.Fatal("shared coverage planning changed caller-owned vector paths or flags")
		}
	}
}

func vectorCacheReviewFarSource() (*psdmodel.Document, *psdmodel.Layer) {
	layer := reviewShadowLayer(image.Rect(-100_000, 0, -99_998, 2))
	for offset := 0; offset < len(layer.Pixels.Pix); offset += 4 {
		copy(layer.Pixels.Pix[offset:], []byte{31, 67, 193, 255})
	}
	layer.VectorMask = &psdvector.Mask{Version: 3, Width: 2, Height: 2,
		HasInitialFill: true, InitialFill: true}
	layer.Effects = reviewShadowSet(psdeffects.Effect{Kind: psdeffects.DropShadow, Enabled: true,
		BlendMode: "norm", Opacity: 1, Color: [3]float64{1, 0, 0}, Distance: 100_000, Angle: 180})
	return &psdmodel.Document{Width: 2, Height: 2, Layers: []*psdmodel.Layer{layer}}, layer
}

func TestVectorShadowCacheReviewFinalStageDoesNotReserveFarSourceUnion(t *testing.T) {
	document, layer := vectorCacheReviewFarSource()
	options := psdcompose.Options{ApproximateDropShadow: true, MaxWorkingBytes: 64 << 10}
	window := image.Rect(0, 0, 1, 1)
	for _, global := range []*bool{new(true), nil} {
		layer.VectorMaskAsGlobal, layer.VectorMask.NotLinked = global, true
		result, err := psdcompose.RenderRegion(t.Context(), document, window, options)
		if err != nil || result.NRGBAAt(0, 0) != (color.NRGBA{R: 255, A: 255}) {
			t.Fatalf("final-stage vector mask reserved the distant source bounds or lost the visible shadow: %v", err)
		}
	}
	layer.VectorMaskAsGlobal = new(false)
	if result, err := psdcompose.RenderRegion(t.Context(), document, window, options); result != nil || !errors.Is(err, psdcompose.ErrLimit) {
		t.Fatalf("shape-before vector cache escaped its full-union memory budget: %v", err)
	}
	// Zero vector density is coverage one and requires no source-domain vector cache.
	layer.VectorDensity = new(uint8(0))
	result, err := psdcompose.RenderRegion(t.Context(), document, window, options)
	if err != nil || result.NRGBAAt(0, 0) != (color.NRGBA{R: 255, A: 255}) {
		t.Fatalf("zero-density vector unnecessarily reserved far source coverage: %v", err)
	}
	layer.VectorDensity = nil
	// Empty output must not expand or allocate a vector cache just to cover an invisible source.
	empty := image.Rect(1, 0, 1, 1)
	result, err = psdcompose.RenderRegion(t.Context(), document, empty, options)
	if err != nil || result.Rect != empty || len(result.Pix) != 0 {
		t.Fatalf("empty ROI expanded source vector coverage: %v", err)
	}
}

func TestVectorShadowCacheReviewOverflowAndNilVectorStayBounded(t *testing.T) {
	for _, origin := range []int{math.MinInt + 4, math.MaxInt - 4} {
		document, layer := vectorCacheReviewFarSource()
		layer.Bounds = image.Rect(origin, 0, origin+2, 2)
		layer.Effects.Items[0].Distance = 0
		layer.VectorMaskAsGlobal = new(false)
		result, err := psdcompose.RenderRegion(t.Context(), document, image.Rect(0, 0, 1, 1),
			psdcompose.Options{ApproximateDropShadow: true, MaxWorkingBytes: 64 << 10})
		if result != nil || err == nil || !errors.Is(err, psdcompose.ErrInvalid) && !errors.Is(err, psdcompose.ErrLimit) {
			t.Fatalf("overflowing vector union allocated or returned an unclassified failure: %v", err)
		}
	}
	document, layer := vectorCacheReviewFarSource()
	layer.VectorMask = nil
	layer.Mask = &psdmodel.Mask{Bounds: layer.Bounds, DefaultColor: new(uint8(128))}
	layer.MaskAsGlobal = new(false)
	options := psdcompose.Options{ApproximateDropShadow: true, MaxWorkingBytes: 64 << 10}
	result, err := psdcompose.RenderRegion(t.Context(), document, image.Rect(0, 0, 1, 1), options)
	if err != nil || result.NRGBAAt(0, 0) != (color.NRGBA{R: 255, A: 128}) {
		t.Fatalf("pixel-only shadow failed after vector-cache integration: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := psdcompose.RenderRegion(ctx, document, image.Rect(0, 0, 1, 1), options); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("vector-related shadow planning ignored cancellation: %v", err)
	}
}
