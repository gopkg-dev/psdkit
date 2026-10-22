package psdadjust

// monochromeRGB evaluates the encoded color model without quantizing arbitrary float inputs.
func (transform *Transform) monochromeRGB(color [3]float64) [3]float64 {
	row := transform.monochrome
	value := float64(row[3])
	for channel := range color {
		value += float64(row[channel]) * color[channel]
	}
	value = clamp(value / 100)
	return [3]float64{value, value, value}
}

// monochromeByte retains exact half-up RGB8 rounding after all signed contributions are accumulated.
func (transform *Transform) monochromeByte(color []byte) uint8 {
	row := transform.monochrome
	value := int(row[3]) * 255
	for channel := range 3 {
		value += int(row[channel]) * int(color[channel])
	}
	if value <= 0 {
		return 0
	}
	if value >= 25500 {
		return 255
	}
	return uint8((value + 50) / 100)
}
