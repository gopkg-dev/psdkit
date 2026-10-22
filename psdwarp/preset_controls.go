package psdwarp

import (
	"math"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

// arcLowerControls constructs the single circular-arc cubic used by the native horizontal preset.
// Photoshop's native 4x2 descriptor is elevated exactly to a 4x4 tensor-product cubic.
func arcLowerControls(placed *psdmodel.Placed) ([16]psdmodel.Point, error) {
	var controls [16]psdmodel.Point
	warp := placed.Warp
	if warp.Style != "warpArcLower" || warp.Rotate != "horizontal" || warp.Split || warp.HasEnvelope ||
		!warp.HasValue || warp.HasValues || len(warp.Unsupported) != 0 ||
		warp.UOrder != 4 || warp.VOrder != 2 || len(warp.MeshPoints) != 0 || len(warp.Values) != 0 {
		return controls, ErrUnsupported
	}
	if !isFinite(warp.Value) || math.Abs(warp.Value) >= 100 || warp.Perspective != 0 || warp.PerspectiveOther != 0 {
		return controls, ErrUnsupported
	}
	bounds := warp.Bounds
	if bounds == nil || !bounds.Pixels {
		return controls, ErrUnsupported
	}
	width, height := bounds.Right-bounds.Left, bounds.Bottom-bounds.Top
	if !isFinite(bounds.Left) || !isFinite(bounds.Top) || !isFinite(width) || !isFinite(height) ||
		width <= 0 || height <= 0 || math.Abs(bounds.Left) > 1e12 || math.Abs(bounds.Top) > 1e12 {
		return controls, ErrInvalid
	}
	if math.Abs(width-float64(placed.Width)) > 1e-8*math.Max(1, width) ||
		math.Abs(height-float64(placed.Height)) > 1e-8*math.Max(1, height) {
		return controls, ErrUnsupported
	}
	angle := math.Pi * warp.Value / 100
	halfCosine := math.Cos(angle / 2)
	handleX := (2 * width / 3) * halfCosine / (1 + halfCosine)
	handleY := (2 * width / 3) * math.Tan(angle/4)
	for row := range 4 {
		fraction := float64(row) / 3
		for column := range 4 {
			top := psdmodel.Point{X: bounds.Left + width*float64(column)/3, Y: bounds.Top}
			bottom := psdmodel.Point{X: top.X, Y: bounds.Bottom}
			switch column {
			case 1:
				bottom.X, bottom.Y = bounds.Left+handleX, bounds.Bottom+handleY
			case 2:
				bottom.X, bottom.Y = bounds.Right-handleX, bounds.Bottom+handleY
			}
			controls[row*4+column] = psdmodel.Point{
				X: top.X*(1-fraction) + bottom.X*fraction,
				Y: top.Y*(1-fraction) + bottom.Y*fraction,
			}
		}
	}
	return controls, nil
}
