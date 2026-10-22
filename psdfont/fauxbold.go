package psdfont

import (
	"context"
	"math"

	"github.com/gopkg-dev/psdkit/psdvector"
)

// fauxBoldPaths creates independent combine components, preserving holes within each translated glyph.
// Taking their union before coverage avoids repeated source-over increasing the original antialias alpha.
func fauxBoldPaths(ctx context.Context, paths []psdvector.Subpath, radius float64) ([]psdvector.Subpath, error) {
	return fauxBoldPathsWithAnchor(ctx, paths, radius, false)
}

func fauxBoldPathsWithAnchor(
	ctx context.Context, paths []psdvector.Subpath, radius float64, anchored bool,
) ([]psdvector.Subpath, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	diagonal := radius / math.Sqrt2
	offsets := [9][2]float64{{}, {radius, 0}, {-radius, 0}, {0, radius}, {0, -radius},
		{diagonal, diagonal}, {diagonal, -diagonal}, {-diagonal, diagonal}, {-diagonal, -diagonal}}
	if anchored {
		for index := 1; index < len(offsets); index++ {
			offsets[index][0] += radius
			offsets[index][1] += radius
		}
	}
	result := make([]psdvector.Subpath, 0, len(paths)*len(offsets))
	for _, offset := range offsets {
		for pathIndex, path := range paths {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			owned := path
			owned.Operation = -1
			if pathIndex == 0 {
				owned.Operation = 1
			}
			owned.Knots = make([]psdvector.Knot, len(path.Knots))
			for knotIndex, knot := range path.Knots {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				knot.Anchor.X, knot.Anchor.Y = knot.Anchor.X+offset[0], knot.Anchor.Y+offset[1]
				knot.Preceding.X, knot.Preceding.Y = knot.Preceding.X+offset[0], knot.Preceding.Y+offset[1]
				knot.Leaving.X, knot.Leaving.Y = knot.Leaving.X+offset[0], knot.Leaving.Y+offset[1]
				owned.Knots[knotIndex] = knot
			}
			result = append(result, owned)
		}
	}
	return result, nil
}
