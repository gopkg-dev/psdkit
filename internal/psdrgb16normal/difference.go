package psdrgb16normal

// OverDifference applies the qualified encoded RGB16 Difference integer stages.
// The blend-adjusted source is rounded before the normalized source-over interpolation.
// Zero output alpha retains current backdrop RGB, matching Over's boundary contract.
func OverDifference(backdrop, source Pixel, opacity uint8) Pixel {
	alphaSource := effectiveAlpha(source[3], opacity)
	alphaOutput := alphaSource + backdrop[3] - (alphaSource*backdrop[3]+One/2)/One
	result := backdrop
	result[3] = alphaOutput
	if alphaOutput == 0 {
		return result
	}
	weight := (alphaSource*One + alphaOutput/2) / alphaOutput
	for component := range 3 {
		var difference uint32
		if backdrop[component] > source[component] {
			difference = backdrop[component] - source[component]
		} else {
			difference = source[component] - backdrop[component]
		}
		adjusted := (source[component]*(One-backdrop[3]) + difference*backdrop[3] + One/2) / One
		result[component] = (backdrop[component]*(One-weight) + adjusted*weight + One/2) / One
	}
	return result
}
