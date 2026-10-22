package psdvector

import (
	"context"
	"image"
	"math"
	"testing"
)

func phaseReviewClip(polygon []Point, axis int, boundary float64, keepGreater bool) []Point {
	if len(polygon) == 0 {
		return nil
	}
	coordinate := func(point Point) float64 {
		if axis == 0 {
			return point.X
		}
		return point.Y
	}
	inside := func(point Point) bool {
		if keepGreater {
			return coordinate(point) >= boundary
		}
		return coordinate(point) <= boundary
	}
	result := make([]Point, 0, len(polygon)+2)
	previous := polygon[len(polygon)-1]
	for _, current := range polygon {
		previousInside, currentInside := inside(previous), inside(current)
		if previousInside != currentInside {
			fraction := (boundary - coordinate(previous)) / (coordinate(current) - coordinate(previous))
			result = append(result, Point{
				X: previous.X + fraction*(current.X-previous.X),
				Y: previous.Y + fraction*(current.Y-previous.Y),
			})
		}
		if currentInside {
			result = append(result, current)
		}
		previous = current
	}
	return result
}

func phaseReviewPixelArea(polygon []Point, x, y int) float64 {
	// Translate into the tested pixel before clipping to avoid subtracting large shoelace terms.
	local := make([]Point, len(polygon))
	for index, point := range polygon {
		local[index] = Point{X: point.X - float64(x), Y: point.Y - float64(y)}
	}
	local = phaseReviewClip(local, 0, 0, true)
	local = phaseReviewClip(local, 0, 1, false)
	local = phaseReviewClip(local, 1, 0, true)
	local = phaseReviewClip(local, 1, 1, false)
	if len(local) == 0 {
		return 0
	}
	area := 0.0
	previous := local[len(local)-1]
	for _, current := range local {
		area += previous.X*current.Y - previous.Y*current.X
		previous = current
	}
	return math.Abs(area) / 2
}

func TestAreaCoverageQuarterPixelPhaseMatchesIndependentPolygonArea(t *testing.T) {
	for _, origin := range []image.Point{{X: 3765, Y: 1786}, {X: -19, Y: -23}} {
		for phaseY := range 4 {
			for phaseX := range 4 {
				polygon := []Point{{X: .2, Y: .1}, {X: 4.4, Y: .9}, {X: 4.1, Y: 3.8}, {X: .3, Y: 3.1}}
				mask := &Mask{Width: 1, Height: 1, HasFillRule: true, FillRule: NonZero}
				path := Subpath{Closed: true, Operation: 1}
				for index := range polygon {
					polygon[index].X += float64(origin.X) + float64(phaseX)/4
					polygon[index].Y += float64(origin.Y) + float64(phaseY)/4
					point := polygon[index]
					path.Knots = append(path.Knots, Knot{Anchor: point, Preceding: point, Leaving: point})
				}
				mask.Subpaths = []Subpath{path}
				bounds := image.Rect(-1, -1, 7, 6).Add(origin)
				coverage, err := Rasterize(context.Background(), mask, bounds,
					Options{AreaCoverage: true, AreaSamples: 64})
				if err != nil {
					t.Fatal(err)
				}
				for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
					for x := bounds.Min.X; x < bounds.Max.X; x++ {
						expected := int(math.Round(phaseReviewPixelArea(polygon, x, y) * 255))
						actual := int(coverage.AlphaAt(x, y).A)
						if absDifference := math.Abs(float64(expected - actual)); absDifference > 1 {
							t.Fatalf("origin=%v phase=(%d,%d)/4 pixel=(%d,%d): alpha=%d analytic=%d",
								origin, phaseX, phaseY, x, y, actual, expected)
						}
					}
				}
			}
		}
	}
}
