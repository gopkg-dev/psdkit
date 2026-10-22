package psdwarp

import (
	"context"
	"math"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

type determinantNet [6][6]float64

func (p *Patch) certify(ctx context.Context) error {
	return p.certifyWithSubdivision(ctx, false)
}

func (p *Patch) certifyWithSubdivision(ctx context.Context, adaptive bool) error {
	corner, right, bottom := p.controls[0], p.controls[3], p.controls[12]
	a, b := psdmodel.Point{X: right.X - corner.X, Y: right.Y - corner.Y},
		psdmodel.Point{X: bottom.X - corner.X, Y: bottom.Y - corner.Y}
	determinant := a.X*b.Y - a.Y*b.X
	if math.Abs(determinant) < 1e-10 {
		return ErrInvalid
	}
	p.reference = [6]float64{b.Y / determinant, -b.X / determinant, -a.Y / determinant, a.X / determinant,
		corner.X, corner.Y}
	var du [3][4]psdmodel.Point
	var dv [4][3]psdmodel.Point
	var principalU, principalV [4][4]float64
	for u := range 3 {
		for v := range 4 {
			first, second := p.controls[v*4+u], p.controls[v*4+u+1]
			du[u][v] = psdmodel.Point{X: 3 * (second.X - first.X), Y: 3 * (second.Y - first.Y)}
			principalU[u][v] = p.reference[0]*du[u][v].X + p.reference[1]*du[u][v].Y
			if !adaptive && principalU[u][v] <= 1e-10 {
				return ErrUnsupported
			}
		}
	}
	for u := range 4 {
		for v := range 3 {
			first, second := p.controls[v*4+u], p.controls[(v+1)*4+u]
			dv[u][v] = psdmodel.Point{X: 3 * (second.X - first.X), Y: 3 * (second.Y - first.Y)}
			principalV[u][v] = p.reference[2]*dv[u][v].X + p.reference[3]*dv[u][v].Y
			if !adaptive && principalV[u][v] <= 1e-10 {
				return ErrUnsupported
			}
		}
	}
	if adaptive {
		reference, err := principalReference(ctx, du, dv, determinant)
		if err != nil {
			return err
		}
		copy(p.reference[:4], reference[:])
		for u := range 3 {
			for v := range 4 {
				principalU[u][v] = reference[0]*du[u][v].X + reference[1]*du[u][v].Y
				principalV[v][u] = reference[2]*dv[v][u].X + reference[3]*dv[v][u].Y
			}
		}
		work := 0
		if err := certifyPrincipal(ctx, principalU, 2, 3, 0, &work); err != nil {
			return err
		}
		if err := certifyPrincipal(ctx, principalV, 3, 2, 0, &work); err != nil {
			return err
		}
	}
	choose2, choose3, choose5 := [3]float64{1, 2, 1}, [4]float64{1, 3, 3, 1}, [6]float64{1, 5, 10, 10, 5, 1}
	var net determinantNet
	for u := range 3 {
		for v := range 4 {
			for s := range 4 {
				for t := range 3 {
					weight := choose2[u] * choose3[s] / choose5[u+s] * choose3[v] * choose2[t] / choose5[v+t]
					net[u+s][v+t] += weight * (du[u][v].X*dv[s][t].Y - du[u][v].Y*dv[s][t].X) / determinant
				}
			}
		}
	}
	// Positive principal minors certify a P-matrix Jacobian on the rectangle.
	// Together they imply global injectivity, in addition to excluding local folds.
	work := 0
	return certifyDeterminant(ctx, net, 0, &work)
}

// certifyPrincipal uses Bernstein convex bounds, not sampled derivatives, to prove positivity.
func certifyPrincipal(ctx context.Context, net [4][4]float64, degreeU, degreeV, depth int, work *int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	*work++
	if *work > 16384 {
		return ErrLimit
	}
	low, high := math.Inf(1), math.Inf(-1)
	for u := range degreeU + 1 {
		for v := range degreeV + 1 {
			low, high = math.Min(low, net[u][v]), math.Max(high, net[u][v])
		}
	}
	if low > 1e-10 {
		return nil
	}
	if high <= 1e-10 || depth >= 12 {
		return ErrUnsupported
	}
	split := func(values [4]float64, degree int) ([4]float64, [4]float64) {
		var left, right [4]float64
		left[0], right[degree] = values[0], values[degree]
		for level := 1; level <= degree; level++ {
			for i := range degree - level + 1 {
				values[i] = (values[i] + values[i+1]) / 2
			}
			left[level], right[degree-level] = values[0], values[degree-level]
		}
		return left, right
	}
	var first, second [4][4]float64
	if depth%2 == 0 {
		for v := range degreeV + 1 {
			var column [4]float64
			for u := range degreeU + 1 {
				column[u] = net[u][v]
			}
			a, b := split(column, degreeU)
			for u := range degreeU + 1 {
				first[u][v], second[u][v] = a[u], b[u]
			}
		}
	} else {
		for u := range degreeU + 1 {
			first[u], second[u] = split(net[u], degreeV)
		}
	}
	if err := certifyPrincipal(ctx, first, degreeU, degreeV, depth+1, work); err != nil {
		return err
	}
	return certifyPrincipal(ctx, second, degreeU, degreeV, depth+1, work)
}

func splitBernstein(values [6]float64) ([6]float64, [6]float64) {
	var first, second [6]float64
	first[0], second[5] = values[0], values[5]
	for level := 1; level < 6; level++ {
		for index := range 6 - level {
			values[index] = (values[index] + values[index+1]) / 2
		}
		first[level], second[5-level] = values[0], values[5-level]
	}
	return first, second
}

func certifyDeterminant(ctx context.Context, net determinantNet, depth int, work *int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	*work++
	if *work > 16_384 {
		return ErrLimit
	}
	lowest, highest := math.Inf(1), math.Inf(-1)
	for _, row := range net {
		for _, value := range row {
			lowest, highest = math.Min(lowest, value), math.Max(highest, value)
		}
	}
	if lowest > 1e-10 {
		return nil
	}
	if highest <= 1e-10 || depth >= 6 {
		return ErrUnsupported
	}
	var first, second determinantNet
	for v := range 6 {
		var column [6]float64
		for u := range 6 {
			column[u] = net[u][v]
		}
		a, b := splitBernstein(column)
		for u := range 6 {
			first[u][v], second[u][v] = a[u], b[u]
		}
	}
	for _, half := range []determinantNet{first, second} {
		var top, bottom determinantNet
		for u := range 6 {
			top[u], bottom[u] = splitBernstein(half[u])
		}
		for _, child := range []determinantNet{top, bottom} {
			if err := certifyDeterminant(ctx, child, depth+1, work); err != nil {
				return err
			}
		}
	}
	return nil
}

func midpoint(first, second [3]float64) [3]float64 {
	return [3]float64{(first[0] + second[0]) / 2, (first[1] + second[1]) / 2, (first[2] + second[2]) / 2}
}

func curvePoint(point [3]float64) psdmodel.Point {
	return psdmodel.Point{X: point[0] / point[2], Y: point[1] / point[2]}
}

func segmentDistance(point, first, second psdmodel.Point) float64 {
	dx, dy := second.X-first.X, second.Y-first.Y
	length := dx*dx + dy*dy
	if length == 0 {
		return math.Hypot(point.X-first.X, point.Y-first.Y)
	}
	amount := max(0, min(1, ((point.X-first.X)*dx+(point.Y-first.Y)*dy)/length))
	return math.Hypot(point.X-first.X-amount*dx, point.Y-first.Y-amount*dy)
}

func (p *Patch) flattenEdge(ctx context.Context, curve [4][3]float64, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	first, last := curvePoint(curve[0]), curvePoint(curve[3])
	flat := segmentDistance(curvePoint(curve[1]), first, last) <= 0.025 &&
		segmentDistance(curvePoint(curve[2]), first, last) <= 0.025
	if flat {
		if len(p.edges) >= 4096 {
			return ErrLimit
		}
		if len(p.edges) == cap(p.edges) {
			capacity := max(1, min(4096, cap(p.edges)*2))
			// A growth retains both backing arrays until the old contents have been copied.
			if 1024+int64(cap(p.edges)+capacity)*32 > p.options.MaxBytes {
				return ErrLimit
			}
			edges := make([]segment, len(p.edges), capacity)
			copy(edges, p.edges)
			p.edges = edges
		}
		p.edges = append(p.edges, segment{first: first, second: last})
		return nil
	}
	if depth >= 12 {
		return ErrLimit
	}
	a, b, c := midpoint(curve[0], curve[1]), midpoint(curve[1], curve[2]), midpoint(curve[2], curve[3])
	d, e := midpoint(a, b), midpoint(b, c)
	center := midpoint(d, e)
	if err := p.flattenEdge(ctx, [4][3]float64{curve[0], a, d, center}, depth+1); err != nil {
		return err
	}
	return p.flattenEdge(ctx, [4][3]float64{center, e, c, curve[3]}, depth+1)
}
