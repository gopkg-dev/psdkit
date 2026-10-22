package psdscene

import (
	"context"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func (engine *rgbTileRenderer) filterOpacityFacts(
	ctx context.Context, layer *psdmodel.Layer, prototype *image.NRGBA, filters *sceneSmartFilter,
	canvas image.Rectangle,
) ([]bool, error) {
	facts := make([]bool, len(filters.operation.Filters))
	workLimit := engine.settings.Region.MaxRLEWorkBytes
	if workLimit == 0 {
		workLimit = 256 << 20
	}
	var work int64
	if layer.Placed.Warp == nil || layer.Placed.Warp.Style != "custom" {
		corners, err := psdrender.PlacedCorners(layer.Placed)
		if err != nil {
			return nil, err
		}
		inverse, err := psdrender.SolveHomography(corners,
			[4]psdrender.Point{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 1, Y: 1}, {X: 0, Y: 1}})
		if err != nil {
			return nil, err
		}
		covered := true
		for _, point := range []image.Point{canvas.Min, {X: canvas.Max.X - 1, Y: canvas.Min.Y},
			{X: canvas.Min.X, Y: canvas.Max.Y - 1}, {X: canvas.Max.X - 1, Y: canvas.Max.Y - 1}} {
			x, y := float64(point.X)+.5, float64(point.Y)+.5
			denominator := inverse[6]*x + inverse[7]*y + inverse[8]
			u, v := (inverse[0]*x+inverse[1]*y+inverse[2])/denominator,
				(inverse[3]*x+inverse[4]*y+inverse[5])/denominator
			covered = covered && u >= 0 && u <= 1 && v >= 0 && v <= 1
		}
		if covered {
			opaque := true
			for row := 0; row < prototype.Rect.Dy() && opaque; row++ {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				for column := 0; column < prototype.Rect.Dx(); column++ {
					if workLimit-work < 4 {
						return nil, sceneError(psdfile.ErrLimit, "tile.smartObject.opacity", 0,
							"prototype opacity proof exceeds the regional work limit")
					}
					work += 4
					if prototype.Pix[row*prototype.Stride+column*4+3] != 255 {
						opaque = false
						break
					}
				}
			}
			if opaque {
				for index := range facts {
					facts[index] = true
				}
				return facts, nil
			}
		}
		left, top, right, bottom := corners[0].X, corners[0].Y, corners[0].X, corners[0].Y
		for _, point := range corners[1:] {
			left, top, right, bottom = min(left, point.X), min(top, point.Y), max(right, point.X), max(bottom, point.Y)
		}
		halo, err := tileFilterHalo(filters, engine.settings.SmartFilters)
		if err != nil {
			return nil, err
		}
		// A transparent document-edge strip wider than every accumulated support stays transparent in all stages.
		if math.Floor(left) > float64(canvas.Min.X+halo+1) || math.Floor(top) > float64(canvas.Min.Y+halo+1) ||
			math.Ceil(right) < float64(canvas.Max.X-halo-1) || math.Ceil(bottom) < float64(canvas.Max.Y-halo-1) {
			return facts, nil
		}
	}
	for index, operation := range filters.operation.Filters {
		if !operation.Enabled || operation.Opacity == 0 || operation.Radius == 0 ||
			operation.Kind != psdfilter.GaussianBlur {
			continue
		}
		prefix := psdfilter.Stack{Enabled: true, Filters: filters.operation.Filters[:index]}
		prefixHalo, err := tileFilterHalo(&sceneSmartFilter{operation: prefix}, engine.settings.SmartFilters)
		if err != nil {
			return nil, err
		}
		opaque := true
		for y := canvas.Min.Y; y < canvas.Max.Y && opaque; y += min(64, canvas.Max.Y-y) {
			for x := canvas.Min.X; x < canvas.Max.X && opaque; x += min(64, canvas.Max.X-x) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				interior := image.Rect(x, y, x+min(64, canvas.Max.X-x), y+min(64, canvas.Max.Y-y))
				window := interior.Inset(-prefixHalo).Intersect(canvas)
				area := int64(window.Dx()) * int64(window.Dy())
				factor := int64(4 + prefixHalo*16)
				if area > (workLimit-work)/factor {
					return nil, sceneError(psdfile.ErrLimit, "tile.smartObject.opacity", 0,
						"complete filter-input opacity classification exceeds the region work limit")
				}
				work += area * factor
				pixels, err := engine.opacityWindow(ctx, layer, prototype, window, prefix, facts[:index])
				if err != nil {
					return nil, err
				}
				for row := interior.Min.Y; row < interior.Max.Y && opaque; row++ {
					for column := interior.Min.X; column < interior.Max.X; column++ {
						if pixels.NRGBAAt(column, row).A != 255 {
							opaque = false
							break
						}
					}
				}
			}
		}
		facts[index] = opaque
	}
	return facts, nil
}

func (engine *rgbTileRenderer) opacityWindow(
	ctx context.Context, layer *psdmodel.Layer, prototype *image.NRGBA, window image.Rectangle,
	prefix psdfilter.Stack, facts []bool,
) (*image.NRGBA, error) {
	available := engine.settings.MaxWorkingBytes - engine.legacy.working
	area := int64(window.Dx()) * int64(window.Dy())
	if available <= 0 || area > available/8 {
		return nil, sceneError(psdfile.ErrLimit, "tile.smartObject.opacity", 0,
			"opacity scan projection and input exceed the regional working budget")
	}
	var projected *image.NRGBA
	var origin image.Point
	var err error
	if layer.Placed.Warp != nil && layer.Placed.Warp.Style == "custom" {
		patch, compileErr := compileSceneWarp(ctx, layer.Placed, engine.settings.ApproximateCustomWarp,
			psdwarp.Options{MaxBytes: available - area*4})
		if compileErr != nil {
			return nil, sceneProjectionError(compileErr, "tile.smartObject.opacity", 0)
		}
		projected, origin, err = projectSceneWarp(ctx, patch, prototype, window, available-area*4)
	} else {
		canvas := image.Rect(0, 0, int(engine.source.Header.Width), int(engine.source.Header.Height)).Union(window)
		plan, planErr := psdrender.CompileProjectionRegion(layer, canvas, window)
		if planErr != nil {
			return nil, planErr
		}
		origin = image.Pt(plan.Left, plan.Top)
		if plan.Width != 0 && plan.Height != 0 {
			projected, origin, err = projectPlacedSource(ctx, layer, prototype, plan,
				engine.settings.ApproximatePlacedProjection)
		}
	}
	if err != nil {
		return nil, sceneProjectionError(err, "tile.smartObject.opacity", 0)
	}
	input := image.NewNRGBA(window)
	if projected != nil {
		bounds := image.Rectangle{Min: origin, Max: origin.Add(projected.Rect.Size())}.Intersect(window)
		for row := bounds.Min.Y; row < bounds.Max.Y; row++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			from := projected.PixOffset(projected.Rect.Min.X+bounds.Min.X-origin.X,
				projected.Rect.Min.Y+row-origin.Y)
			to := input.PixOffset(bounds.Min.X, row)
			copy(input.Pix[to:to+bounds.Dx()*4], projected.Pix[from:from+bounds.Dx()*4])
		}
	}
	if len(prefix.Filters) == 0 {
		return input, nil
	}
	options := *engine.settings.SmartFilters
	options.MaxWorkingBytes = available - area*8
	if options.MaxWorkingBytes <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "tile.smartObject.opacity", 0, "no opacity filter scratch remains")
	}
	return psdfilter.ApplyRegion(ctx, input, prefix, facts, options)
}
