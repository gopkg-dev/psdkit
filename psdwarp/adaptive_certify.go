package psdwarp

import (
	"context"
	"math"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

// principalReference selects a candidate coordinate frame; certifyPrincipal still proves every derivative bound.
// The finite search is only a heuristic for finding a proof, never evidence of injectivity on its own.
func principalReference(
	ctx context.Context, du [3][4]psdmodel.Point, dv [4][3]psdmodel.Point, orientation float64,
) ([4]float64, error) {
	type candidate struct{ x, y, u, v float64 }
	var choices [32]candidate
	for index := range choices {
		angle := float64(index) * 2 * math.Pi / float64(len(choices))
		choices[index] = candidate{x: math.Cos(angle), y: math.Sin(angle), u: math.Inf(1), v: math.Inf(1)}
	}
	quadratic := func(t float64) [3]float64 { return [3]float64{(1 - t) * (1 - t), 2 * t * (1 - t), t * t} }
	cubic := func(t float64) [4]float64 {
		return [4]float64{(1 - t) * (1 - t) * (1 - t), 3 * t * (1 - t) * (1 - t), 3 * t * t * (1 - t), t * t * t}
	}
	for row := range 9 {
		if err := ctx.Err(); err != nil {
			return [4]float64{}, err
		}
		v := float64(row) / 8
		for column := range 9 {
			u := float64(column) / 8
			qu, qv, cu, cv := quadratic(u), quadratic(v), cubic(u), cubic(v)
			var first, second psdmodel.Point
			for i := range 3 {
				for j := range 4 {
					first.X += du[i][j].X * qu[i] * cv[j]
					first.Y += du[i][j].Y * qu[i] * cv[j]
					second.X += dv[j][i].X * cu[j] * qv[i]
					second.Y += dv[j][i].Y * cu[j] * qv[i]
				}
			}
			for i := range choices {
				c := &choices[i]
				c.u = math.Min(c.u, c.x*first.X+c.y*first.Y)
				c.v = math.Min(c.v, c.x*second.X+c.y*second.Y)
			}
		}
	}
	best := 0.0
	var reference [4]float64
	for _, a := range choices {
		for _, b := range choices {
			determinant := a.x*b.y - a.y*b.x
			if a.u <= 1e-10 || b.v <= 1e-10 || determinant*orientation <= 1e-10 {
				continue
			}
			score := a.u * b.v * math.Abs(determinant)
			if score > best {
				best = score
				reference = [4]float64{a.x, a.y, b.x, b.y}
			}
		}
	}
	if best == 0 {
		return reference, ErrUnsupported
	}
	return reference, nil
}
