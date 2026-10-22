package psdscene

import (
	"context"
	"image"
	"image/color"
	"math/rand"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func TestTileFilterOpacityFactsTrackCompleteInputsAcrossStages(t *testing.T) {
	canvas := image.Rect(0, 0, 96, 48)
	prototype := image.NewNRGBA(canvas)
	random := rand.New(rand.NewSource(4917))
	for row := range 48 {
		for column := range 96 {
			prototype.SetNRGBA(column, row, color.NRGBA{R: byte(random.Intn(256)),
				G: byte(random.Intn(256)), B: byte(random.Intn(256)), A: 255})
		}
	}
	partial := prototype.NRGBAAt(70, 36)
	partial.A = 254
	prototype.SetNRGBA(70, 36, partial)
	layer := &psdmodel.Layer{Placed: &psdmodel.Placed{Width: 96, Height: 48, Type: "raster",
		Transform: []float64{0, 0, 96, 0, 96, 48, 0, 48}}}
	options := psdfilter.Options{ApproximateGaussian: true}
	stack := psdfilter.Stack{Enabled: true, Filters: []psdfilter.Filter{
		{Kind: psdfilter.GaussianBlur, Enabled: true, Radius: 1, Opacity: 1, BlendMode: "norm"},
		{Kind: psdfilter.GaussianBlur, Enabled: true, Radius: 1, Opacity: 1, BlendMode: "norm"},
	}}
	engine := rgbTileRenderer{source: &psdfile.Document{Header: psdfile.Header{Width: 96, Height: 48}},
		settings: TileOptions{MaxWorkingBytes: 8 << 20, SmartFilters: &options}}
	filters := &sceneSmartFilter{operation: stack}
	facts, err := engine.filterOpacityFacts(context.Background(), layer, prototype, filters, canvas)
	if err != nil || !slices.Equal(facts, []bool{false, true}) {
		t.Fatalf("complete-image alpha facts used local input or stale first-stage classification: %v, %v", facts, err)
	}
	full, err := psdfilter.Apply(nil, prototype, stack, options)
	if err != nil {
		t.Fatal(err)
	}
	interior := image.Rect(20, 10, 45, 30)
	pixels, err := engine.opacityWindow(context.Background(), layer, prototype,
		interior.Inset(-18).Intersect(canvas), stack, facts)
	if err != nil {
		t.Fatal(err)
	}
	for row := interior.Min.Y; row < interior.Max.Y; row++ {
		for column := interior.Min.X; column < interior.Max.X; column++ {
			if pixels.NRGBAAt(column, row) != full.NRGBAAt(column, row) {
				t.Fatalf("regional filter stage facts changed seam RGB at (%d,%d)", column, row)
			}
		}
	}
}
