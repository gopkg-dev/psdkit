package psdvector

import (
	"image"
	"math"
	"slices"
)

const areaVerticalSamples int64 = 64

// rasterizeArea integrates each disjoint horizontal filled interval before quantizing coverage once.
// The vertical midpoint coordinates are absolute, so cropping cannot shift the sampling phase.
func rasterizeArea(plan *rasterPlan, bounds image.Rectangle, result *image.Alpha) (*image.Alpha, error) {
	coverage := make([]float64, bounds.Dx())
	verticalSamples := int64(plan.options.AreaSamples)
	winding := make([]int, len(plan.components))
	crossings := make([]vectorCrossing, 0, len(plan.lines))
	for row := range bounds.Dy() {
		clear(coverage)
		for sample := range verticalSamples {
			if err := plan.ctx.Err(); err != nil {
				return nil, err
			}
			clear(winding)
			crossings = crossings[:0]
			y := float64(bounds.Min.Y+row) + (float64(sample)+.5)/float64(verticalSamples)
			for _, line := range plan.lines {
				direction := 0
				if line.y1 <= y && y < line.y2 {
					direction = 1
				} else if line.y2 <= y && y < line.y1 {
					direction = -1
				}
				if direction != 0 {
					x := line.x1 + (y-line.y1)/(line.y2-line.y1)*(line.x2-line.x1)
					crossings = append(crossings, vectorCrossing{x: x, component: line.component, winding: direction})
				}
			}
			slices.SortFunc(crossings, func(left, right vectorCrossing) int {
				if left.x < right.x {
					return -1
				}
				if left.x > right.x {
					return 1
				}
				return 0
			})
			activeComponents := 0
			previous := float64(bounds.Min.X)
			for index := 0; index < len(crossings); {
				position := crossings[index].x
				inside := activeComponents != 0
				if !plan.combineOnly {
					inside = plan.inside(winding)
				}
				if inside != plan.mask.Invert {
					addAreaSpan(coverage, bounds.Min.X, previous, position)
				}
				for index < len(crossings) && crossings[index].x == position {
					crossing := crossings[index]
					previousInside := componentInside(winding[crossing.component], plan.rule)
					winding[crossing.component] += crossing.winding
					if plan.combineOnly {
						currentInside := componentInside(winding[crossing.component], plan.rule)
						if currentInside && !previousInside {
							activeComponents++
						} else if previousInside && !currentInside {
							activeComponents--
						}
					}
					index++
				}
				previous = position
			}
			inside := activeComponents != 0
			if !plan.combineOnly {
				inside = plan.inside(winding)
			}
			if inside != plan.mask.Invert {
				addAreaSpan(coverage, bounds.Min.X, previous, float64(bounds.Max.X))
			}
		}
		for column, amount := range coverage {
			value := math.Round(amount * 255 / float64(verticalSamples))
			alpha := byte(math.Max(0, math.Min(255, value)))
			if plan.options.CoverageLevels16 {
				alpha = coverageLevel16(amount, float64(verticalSamples))
			}
			result.Pix[row*result.Stride+column] = alpha
		}
	}
	if err := plan.ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func componentInside(winding int, rule FillRule) bool {
	if rule == EvenOdd {
		return winding%2 != 0
	}
	return winding != 0
}

func addAreaSpan(coverage []float64, origin int, start, end float64) {
	left := math.Max(0, math.Min(float64(len(coverage)), start-float64(origin)))
	right := math.Max(0, math.Min(float64(len(coverage)), end-float64(origin)))
	for left < right {
		column := int(math.Floor(left))
		boundary := math.Min(right, float64(column+1))
		coverage[column] += boundary - left
		left = boundary
	}
}
