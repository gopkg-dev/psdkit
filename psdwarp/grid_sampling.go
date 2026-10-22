package psdwarp

import (
	"image"
	"math"
)

// gridSampleAccumulator keeps signed filter results until every owned document-space subpixel is added.
// It is stack-local; renderers account for their lattice and image storage independently.
type gridSampleAccumulator struct {
	channels [4]float64
	count    int
}

func (accumulator *gridSampleAccumulator) add(sample [4]float64) {
	for component := range sample {
		accumulator.channels[component] += sample[component]
	}
	accumulator.count++
}

// pixel averages actual owned subpixels before clipping signed alpha or unpremultiplying colors.
func (accumulator *gridSampleAccumulator) pixel(premultiplied bool) [4]float64 {
	if accumulator.count == 0 {
		return [4]float64{}
	}
	result := accumulator.channels
	for component := range result {
		result[component] /= float64(accumulator.count)
	}
	if premultiplied {
		if result[3] > 1e-6 {
			for component := range 3 {
				result[component] = result[component] / result[3] * 255
			}
		} else {
			result[0], result[1], result[2] = 0, 0, 0
		}
	}
	for component := range result {
		result[component] = max(0, min(255, result[component]))
	}
	return result
}

// gridRGBKeys0 implements the report's fixed RGB Keys a=0 and alpha Keys a=-0.75 experiment.
// UV coordinates cover the entire source, including its transparent exterior, rather than one cubic piece.
// Source must already have passed the renderer's image validation. Its Rect.Min and Stride are preserved.
// RGB uses edge-clamped straight colors; alpha uses a transparent border and retains signed filter lobes.
// The explicit premultiplied negative control filters alpha-weighted RGB with the same RGB kernel.
func gridRGBKeys0(source *image.NRGBA, u, v float64, premultiplied bool) [4]float64 {
	if !isFinite(u) || !isFinite(v) {
		return [4]float64{}
	}
	width, height := source.Rect.Dx(), source.Rect.Dy()
	// Outside these coordinates all alpha taps are transparent and all RGB taps clamp to one edge.
	// Bounding before integer conversion also keeps very distant finite coordinates well-defined.
	x := max(-2, min(float64(width)+1, u*float64(width)-0.5))
	y := max(-2, min(float64(height)+1, v*float64(height)-0.5))
	left, top := int(math.Floor(x)), int(math.Floor(y))
	colorX, colorY := gridKeysWeights(x-float64(left), 0), gridKeysWeights(y-float64(top), 0)
	alphaX := gridKeysWeights(x-float64(left), -0.75)
	alphaY := gridKeysWeights(y-float64(top), -0.75)
	var result [4]float64
	for row := range 4 {
		var along [4]float64
		for column := range 4 {
			sx, sy := left+column-1, top+row-1
			inside := sx >= 0 && sy >= 0 && sx < width && sy < height
			offset := min(max(sy, 0), height-1)*source.Stride + min(max(sx, 0), width-1)*4
			alpha := 0.0
			if inside {
				alpha = float64(source.Pix[offset+3])
			}
			along[3] += alpha * alphaX[column]
			for component := range 3 {
				value := float64(source.Pix[offset+component])
				if premultiplied {
					value *= alpha / 255
				}
				along[component] += value * colorX[column]
			}
		}
		for component := range 3 {
			result[component] += along[component] * colorY[row]
		}
		result[3] += along[3] * alphaY[row]
	}
	return result
}

func gridKeysWeights(phase, tension float64) [4]float64 {
	other := 1 - phase
	first, last := phase+1, other+1
	return [4]float64{
		tension*first*first*first - 5*tension*first*first + 8*tension*first - 4*tension,
		(tension+2)*phase*phase*phase - (tension+3)*phase*phase + 1,
		(tension+2)*other*other*other - (tension+3)*other*other + 1,
		tension*last*last*last - 5*tension*last*last + 8*tension*last - 4*tension,
	}
}
