package jpeg

// canvasYCbCr uses the fixed-point JFIF conversion coefficients of the reference encoder.
func canvasYCbCr(red, green, blue uint8) (uint8, uint8, uint8) {
	r, g, b := int32(red), int32(green), int32(blue)
	y := (19595*r + 38470*g + 7471*b + (1 << 15)) >> 16
	cb := (-11059*r - 21709*g + 32768*b + (128 << 16) + (1 << 15) - 1) >> 16
	cr := (32768*r - 27439*g - 5329*b + (128 << 16) + (1 << 15) - 1) >> 16
	return uint8(y), uint8(cb), uint8(cr)
}
