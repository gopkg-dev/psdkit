package psdadjust

func compileCurve(table *[256]float64, curve CurveChannel) error {
	if len(curve.LUT) != 0 {
		if len(curve.Points) != 0 || len(curve.LUT) != 256 {
			return adjustmentError(ErrInvalid, "adjustment.curves", 0, "a curve must contain points or exactly 256 LUT samples")
		}
		for index, value := range curve.LUT {
			table[index] = float64(value) / 255
		}
		return nil
	}
	points := curve.Points
	if len(points) < 2 || len(points) > 19 {
		return adjustmentError(ErrInvalid, "adjustment.curves", 0, "a curve must contain 2 through 19 points")
	}
	increasing, decreasing := true, true
	for index, point := range points {
		if point.Input > 255 || point.Output > 255 || index != 0 && point.Input <= points[index-1].Input {
			return adjustmentError(ErrInvalid, "adjustment.curves", 0, "curve inputs must strictly increase within byte range")
		}
		if index != 0 {
			increasing = increasing && point.Output >= points[index-1].Output
			decreasing = decreasing && point.Output <= points[index-1].Output
		}
	}
	if !increasing && !decreasing {
		return adjustmentError(ErrUnsupported, "adjustment.curves", 0,
			"non-monotone curve control values are not implemented")
	}
	var second, diagonal [19]float64
	for index := 1; index < len(points)-1; index++ {
		left := float64(points[index].Input - points[index-1].Input)
		right := float64(points[index+1].Input - points[index].Input)
		total := left + right
		ratio := left / total
		denominator := ratio*second[index-1] + 2
		second[index] = (ratio - 1) / denominator
		leftSlope := (float64(points[index].Output) - float64(points[index-1].Output)) / left
		rightSlope := (float64(points[index+1].Output) - float64(points[index].Output)) / right
		diagonal[index] = (6*(rightSlope-leftSlope)/total - ratio*diagonal[index-1]) / denominator
	}
	for index := len(points) - 2; index >= 0; index-- {
		second[index] = second[index]*second[index+1] + diagonal[index]
	}
	segment := 0
	for value := range 256 {
		if value <= int(points[0].Input) {
			table[value] = float64(points[0].Output) / 255
			continue
		}
		if value >= int(points[len(points)-1].Input) {
			table[value] = float64(points[len(points)-1].Output) / 255
			continue
		}
		for segment+1 < len(points)-1 && value > int(points[segment+1].Input) {
			segment++
		}
		width := float64(points[segment+1].Input - points[segment].Input)
		a := (float64(points[segment+1].Input) - float64(value)) / width
		b := (float64(value) - float64(points[segment].Input)) / width
		output := a*float64(points[segment].Output) + b*float64(points[segment+1].Output) +
			((a*a*a-a)*second[segment]+(b*b*b-b)*second[segment+1])*width*width/6
		table[value] = clamp(output / 255)
	}
	return nil
}

func identityCurve(curve CurveChannel) bool {
	if len(curve.LUT) != 0 {
		if len(curve.LUT) != 256 || len(curve.Points) != 0 {
			return false
		}
		for index, value := range curve.LUT {
			if value != uint8(index) {
				return false
			}
		}
		return true
	}
	if len(curve.Points) < 2 || len(curve.Points) > 19 || curve.Points[0] != (CurvePoint{Input: 0, Output: 0}) ||
		curve.Points[len(curve.Points)-1] != (CurvePoint{Input: 255, Output: 255}) {
		return false
	}
	for index, point := range curve.Points {
		if point.Input != point.Output || index != 0 && point.Input <= curve.Points[index-1].Input {
			return false
		}
	}
	return true
}
