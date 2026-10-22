package psdmodel

// These allocation guards preserve the existing backend's supported subset.
// They are application policy, not the maximum values allowed by the PSD format.
const (
	// MaxPixels limits each decoded canvas, layer or mask.
	MaxPixels = 20_000_000
	// MaxDimension limits each side of a decoded image.
	MaxDimension = 30_000
	// MaxDecodedPixels limits the combined canvas, layer and mask pixels.
	MaxDecodedPixels = 60_000_000
	// MaxLayers limits the number of layer records.
	MaxLayers = 500
	// MaxFileBytes limits the compressed source size.
	MaxFileBytes = 500 * 1024 * 1024
)
