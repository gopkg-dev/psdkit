package psdwarp

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

type gridNet [7][7]float64

func gridDirectionReference(ctx context.Context, controls []psdmodel.Point, order int) ([4]float64, error) {
	count := (order - 1) / 3
	var reference [4]float64
	for axis := range 2 {
		var angles [16 * 81]float64
		length := 0
		for row := range count {
			for column := range count {
				if err := ctx.Err(); err != nil {
					return reference, err
				}
				var piece Patch
				for y := range 4 {
					for x := range 4 {
						piece.controls[y*4+x] = controls[(row*3+y)*order+column*3+x]
					}
				}
				for y := range 9 {
					for x := range 9 {
						_, du, dv := piece.surface(float64(x)/8, float64(y)/8)
						direction := du
						if axis == 1 {
							direction = dv
						}
						if !isFinite(direction.X) || !isFinite(direction.Y) ||
							math.Hypot(direction.X, direction.Y) < 1e-12 {
							return reference, ErrUnsupported
						}
						angles[length] = math.Atan2(direction.Y, direction.X)
						length++
					}
				}
			}
		}
		sort.Float64s(angles[:length])
		gap, before := 0.0, 0.0
		for index, angle := range angles[:length] {
			after := angles[(index+1)%length]
			if index == length-1 {
				after += 2 * math.Pi
			}
			if after-angle > gap {
				gap, before = after-angle, angle
			}
		}
		width := 2*math.Pi - gap
		if width >= math.Pi-1e-8 {
			return reference, ErrUnsupported
		}
		center := before + gap + width/2
		reference[axis*2], reference[axis*2+1] = math.Cos(center), math.Sin(center)
	}
	if math.Abs(reference[0]*reference[3]-reference[1]*reference[2]) < 1e-10 {
		return reference, ErrUnsupported
	}
	// Samples only propose a source-derived common reference. They never certify
	// geometry: every principal minor is subsequently bounded on every complete
	// piece using Bernstein coefficients and conservative subdivision.
	return reference, nil
}

func gridReference(first, right, bottom psdmodel.Point) ([4]float64, error) {
	a, b := psdmodel.Point{X: right.X - first.X, Y: right.Y - first.Y},
		psdmodel.Point{X: bottom.X - first.X, Y: bottom.Y - first.Y}
	determinant := a.X*b.Y - a.Y*b.X
	if !isFinite(determinant) || math.Abs(determinant) < 1e-10 {
		return [4]float64{}, ErrInvalid
	}
	return [4]float64{b.Y / determinant, -b.X / determinant, -a.Y / determinant, a.X / determinant}, nil
}

func certifyGridPiece(ctx context.Context, piece *Patch, reference [4]float64, count int, work *int) error {
	var du, dv [4][4]psdmodel.Point
	transform := func(point psdmodel.Point) psdmodel.Point {
		return psdmodel.Point{
			X: float64(count) * (reference[0]*point.X + reference[1]*point.Y),
			Y: float64(count) * (reference[2]*point.X + reference[3]*point.Y),
		}
	}
	// Elevate each derivative from degree 2 to 3 on its differentiated axis.
	for row := range 4 {
		var values [3]psdmodel.Point
		for column := range 3 {
			first, second := piece.controls[row*4+column], piece.controls[row*4+column+1]
			values[column] = transform(psdmodel.Point{X: 3 * (second.X - first.X), Y: 3 * (second.Y - first.Y)})
		}
		du[0][row], du[3][row] = values[0], values[2]
		for column := 1; column < 3; column++ {
			amount := float64(column) / 3
			du[column][row] = psdmodel.Point{
				X: amount*values[column-1].X + (1-amount)*values[column].X,
				Y: amount*values[column-1].Y + (1-amount)*values[column].Y,
			}
		}
	}
	for column := range 4 {
		var values [3]psdmodel.Point
		for row := range 3 {
			first, second := piece.controls[row*4+column], piece.controls[(row+1)*4+column]
			values[row] = transform(psdmodel.Point{X: 3 * (second.X - first.X), Y: 3 * (second.Y - first.Y)})
		}
		dv[column][0], dv[column][3] = values[0], values[2]
		for row := 1; row < 3; row++ {
			amount := float64(row) / 3
			dv[column][row] = psdmodel.Point{
				X: amount*values[row-1].X + (1-amount)*values[row].X,
				Y: amount*values[row-1].Y + (1-amount)*values[row].Y,
			}
		}
	}
	choose3, choose6 := [4]float64{1, 3, 3, 1}, [7]float64{1, 6, 15, 20, 15, 6, 1}
	var determinant, diagonalU, diagonalV gridNet
	for u := range 4 {
		for v := range 4 {
			for s := range 4 {
				for t := range 4 {
					weight := choose3[u] * choose3[s] / choose6[u+s] * choose3[v] * choose3[t] / choose6[v+t]
					determinant[u+s][v+t] += weight * (du[u][v].X*dv[s][t].Y - du[u][v].Y*dv[s][t].X)
					// Multiplication by the constant-one polynomial elevates both diagonal entries.
					diagonalU[u+s][v+t] += weight * du[u][v].X
					diagonalV[u+s][v+t] += weight * dv[u][v].Y
				}
			}
		}
	}
	// Positive principal minors certify a P-matrix in one common global reference.
	// This finite C0 piecewise-polynomial map has the active one-sided Jacobians
	// as an H_K-differential at seams and vertices, so the nonsmooth Gale-Nikaido
	// theorem proves injectivity on the entire closed source rectangle. A separate
	// reference for each piece would not provide this global certificate.
	// See Gowda and Ravindran, Algebraic Univalence Theorems for Nonsmooth
	// Functions, Theorem 2, https://doi.org/10.1006/jmaa.2000.7171.
	if err := certifyGridPositive(ctx, diagonalU, 0, work); err != nil {
		return fmt.Errorf("global u principal minor: %w", err)
	}
	if err := certifyGridPositive(ctx, diagonalV, 0, work); err != nil {
		return fmt.Errorf("global v principal minor: %w", err)
	}
	if err := certifyGridPositive(ctx, determinant, 0, work); err != nil {
		return fmt.Errorf("global determinant: %w", err)
	}
	return nil
}

func splitGridBernstein(values [7]float64) ([7]float64, [7]float64) {
	var first, second [7]float64
	first[0], second[6] = values[0], values[6]
	for level := 1; level < 7; level++ {
		for index := range 7 - level {
			values[index] = (values[index] + values[index+1]) / 2
		}
		first[level], second[6-level] = values[0], values[6-level]
	}
	return first, second
}

func certifyGridPositive(ctx context.Context, net gridNet, depth int, work *int) error {
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
			if !isFinite(value) {
				return ErrInvalid
			}
			lowest, highest = math.Min(lowest, value), math.Max(highest, value)
		}
	}
	if lowest > 1e-10 {
		return nil
	}
	if highest <= 1e-10 || depth >= 6 {
		return ErrUnsupported
	}
	var first, second gridNet
	for v := range 7 {
		var column [7]float64
		for u := range 7 {
			column[u] = net[u][v]
		}
		a, b := splitGridBernstein(column)
		for u := range 7 {
			first[u][v], second[u][v] = a[u], b[u]
		}
	}
	for _, half := range [2]gridNet{first, second} {
		var top, bottom gridNet
		for u := range 7 {
			top[u], bottom[u] = splitGridBernstein(half[u])
		}
		for _, child := range [2]gridNet{top, bottom} {
			if err := certifyGridPositive(ctx, child, depth+1, work); err != nil {
				return err
			}
		}
	}
	return nil
}
