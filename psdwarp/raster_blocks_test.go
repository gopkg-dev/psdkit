package psdwarp

import (
	"bytes"
	"image"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func rasterBlockFixture(t testing.TB) (*Patch, *image.NRGBA) {
	t.Helper()
	placed := &psdmodel.Placed{Width: 128, Height: 96,
		Transform: []float64{-14, -20, 150, -12, 148, 110, -22, 104},
		Warp: &psdmodel.Warp{Style: "custom", UOrder: 4, VOrder: 4, HasEnvelope: true,
			Bounds: &psdmodel.WarpBounds{Right: 128, Bottom: 96, Pixels: true}},
	}
	for row := range 4 {
		for column := range 4 {
			point := psdmodel.Point{X: float64(column) * 128 / 3, Y: float64(row) * 96 / 3}
			if column == 1 || column == 2 {
				point.Y -= 12
			}
			placed.Warp.MeshPoints = append(placed.Warp.MeshPoints, point)
		}
	}
	patch, err := CompileMathematical(t.Context(), placed)
	if err != nil {
		t.Fatal(err)
	}
	source := image.NewNRGBA(image.Rect(9, -4, 137, 92))
	for index := range source.Pix {
		source.Pix[index] = byte(index*31 + index/13)
	}
	return patch, source
}

// rowScanProjection preserves the pre-optimization lookup as an independent pixel-equivalence reference.
func rowScanProjection(t testing.TB, patch *Patch, source *image.NRGBA, canvas image.Rectangle) (
	*image.NRGBA, image.Point,
) {
	t.Helper()
	bounds, err := patch.Bounds(canvas)
	if err != nil {
		t.Fatal(err)
	}
	raster, err := patch.raster(t.Context(), source, bounds)
	if err != nil {
		t.Fatal(err)
	}
	result := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for row := range bounds.Dy() {
		for column := range bounds.Dx() {
			point := psdmodel.Point{X: float64(bounds.Min.X+column) + .5, Y: float64(bounds.Min.Y+row) + .5}
			var pixel [4]float64
			for _, index := range raster.indices[raster.offsets[row]:raster.offsets[row+1]] {
				cell := &raster.cells[index]
				if point.X < float64(cell.box.Min.X) || point.X >= float64(cell.box.Max.X) {
					continue
				}
				u, v, covered := cell.inverse(point)
				if covered {
					pixel = rasterSample(source, u, v)
					pixel[3] *= cell.edgeCoverage(point, source, u, v)
					break
				}
			}
			for component, value := range pixel {
				result.Pix[row*result.Stride+column*4+component] = byte(math.RoundToEven(max(0, min(255, value))))
			}
		}
	}
	return result, bounds.Min
}

func TestRasterBlocksPreserveRowScanPixelsAndCropOrigins(t *testing.T) {
	patch, source := rasterBlockFixture(t)
	before := bytes.Clone(source.Pix)
	for _, canvas := range []image.Rectangle{{}, image.Rect(1, 3, 140, 77)} {
		expected, expectedOrigin := rowScanProjection(t, patch, source, canvas)
		actual, origin, err := patch.Project(t.Context(), source, canvas)
		if err != nil || origin != expectedOrigin || actual.Rect != expected.Rect ||
			!bytes.Equal(actual.Pix, expected.Pix) {
			t.Fatalf("block lookup changed cropped or translucent projected pixels: %v", err)
		}
	}
	if !bytes.Equal(source.Pix, before) {
		t.Fatal("block lookup modified source pixels")
	}
}

func TestRasterBlocksDenseOverlapRetainsCompleteOrder(t *testing.T) {
	plan := &rasterPlan{}
	var indices []uint32
	for index := range 17 {
		plan.cells = append(plan.cells, rasterCell{box: image.Rect(index, 0, index+100, 1)})
		indices = append(indices, uint32(16-index))
	}
	var scratch [1]uint32
	actual := plan.blockIndices(indices, 20, 52, scratch[:0])
	if len(actual) != len(indices) {
		t.Fatal("dense overlap discarded potential covering cells")
	}
	for index := range indices {
		if actual[index] != indices[index] {
			t.Fatal("dense overlap changed first-covering-cell order")
		}
	}
}

func BenchmarkWarpRasterBlocks(b *testing.B) {
	patch, source := rasterBlockFixture(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := patch.Project(b.Context(), source, image.Rectangle{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWarpRasterRowScan(b *testing.B) {
	patch, source := rasterBlockFixture(b)
	b.ReportAllocs()
	for b.Loop() {
		rowScanProjection(b, patch, source, image.Rectangle{})
	}
}
