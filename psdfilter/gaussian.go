package psdfilter

import (
	"context"
	"image"
	"math"
)

func gaussian(
	ctx context.Context, source, target *image.NRGBA, horizontal []float64, filter Filter, fullOpaque *bool,
) error {
	width, height := source.Rect.Dx(), source.Rect.Dy()
	kernel := gaussianFilterKernel(filter.Radius)
	radius := len(kernel) / 2
	quantizeHorizontal, err := gaussianOpaqueShortKernel(ctx, source, filter.Radius)
	if err != nil {
		return err
	}
	if fullOpaque != nil {
		if *fullOpaque && !source.Opaque() {
			return filterError(ErrInvalid, "region.opacity", "full-image opaque fact conflicts with regional alpha")
		}
		quantizeHorizontal = quantizeHorizontal && *fullOpaque
	}
	for row := range height {
		if err := ctx.Err(); err != nil {
			return err
		}
		for column := range width {
			if column&255 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			base := (row*width + column) * 4
			for component := range 4 {
				horizontal[base+component] = 0
			}
			for index, weight := range kernel {
				x := max(0, min(width-1, column+index-radius))
				offset := row*source.Stride + x*4
				alpha := float64(source.Pix[offset+3]) / 255
				for component := range 3 {
					horizontal[base+component] += weight * alpha * float64(source.Pix[offset+component])
				}
				horizontal[base+3] += weight * float64(source.Pix[offset+3])
			}
			if quantizeHorizontal {
				for component := range 3 {
					horizontal[base+component] = math.Floor(horizontal[base+component] + .5)
				}
			}
		}
	}
	for row := range height {
		if err := ctx.Err(); err != nil {
			return err
		}
		for column := range width {
			if column&255 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			var values [4]float64
			for index, weight := range kernel {
				y := max(0, min(height-1, row+index-radius))
				base := (y*width + column) * 4
				for component := range 4 {
					values[component] += weight * horizontal[base+component]
				}
			}
			offset := row*target.Stride + column*4
			sourceOffset := row*source.Stride + column*4
			originalAlpha := float64(source.Pix[sourceOffset+3]) / 255
			values[3] = values[3]*filter.Opacity + float64(source.Pix[sourceOffset+3])*(1-filter.Opacity)
			for component := range 3 {
				values[component] = values[component]*filter.Opacity +
					float64(source.Pix[sourceOffset+component])*originalAlpha*(1-filter.Opacity)
				if values[3] > 0 {
					target.Pix[offset+component] = roundedByte(values[component] * 255 / values[3])
				} else {
					target.Pix[offset+component] = 0
				}
			}
			target.Pix[offset+3] = roundedByte(values[3])
		}
	}
	return nil
}

// gaussianOpaqueShortKernel reproduces observed horizontal RGB8 quantization only for calibrated opaque inputs.
// Transparent inputs retain premultiplied precision so uniform encoded colors and hidden RGB remain correct.
func gaussianOpaqueShortKernel(ctx context.Context, source *image.NRGBA, radius float64) (bool, error) {
	switch radius {
	case .25, .5, .75, 1, 1.5, 2:
	default:
		return false, nil
	}
	for row := 0; row < source.Rect.Dy(); row++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		for column := 0; column < source.Rect.Dx(); column++ {
			if column&255 == 0 {
				if err := ctx.Err(); err != nil {
					return false, err
				}
			}
			if source.Pix[row*source.Stride+column*4+3] != 255 {
				return false, nil
			}
		}
	}
	return true, nil
}

// gaussianFilterKernel keeps the bounded approximation independent of retained native filter caches.
func gaussianFilterKernel(radius float64) []float64 {
	if radius <= .1 {
		return []float64{1}
	}
	if radius > 2 {
		return gaussianBoxKernel(radius * 1.71)
	}
	if kernel := gaussianCalibratedKernel(radius); kernel != nil {
		return kernel
	}
	sigma := gaussianSigma(radius)
	return gaussianKernel(sigma, int(math.Ceil(4*sigma)))
}

// gaussianCalibratedKernel contains normalized short FIR coefficients identified by independent RGB8 controls.
// Unmeasured small radii retain the integrated approximation, with no claim of private-kernel parity.
func gaussianCalibratedKernel(radius float64) []float64 {
	var coefficients [7]uint16
	var count int
	switch radius {
	case .25:
		coefficients, count = [7]uint16{206, 25}, 2
	case .5:
		coefficients, count = [7]uint16{144, 55, 1}, 3
	case .75:
		coefficients, count = [7]uint16{116, 61, 9}, 3
	case 1:
		coefficients, count = [7]uint16{96, 60, 18, 2}, 4
	case 1.5:
		coefficients, count = [7]uint16{66, 52, 29, 11, 3}, 5
	case 2:
		coefficients, count = [7]uint16{48, 44, 31, 17, 8, 3, 1}, 7
	default:
		return nil
	}
	weights := make([]float64, count*2-1)
	for offset := range count {
		weight := float64(coefficients[offset]) / 256
		weights[count-1-offset], weights[count-1+offset] = weight, weight
	}
	return weights
}

// gaussianBoxKernel integrates four equal boxes onto the pixel grid and convolves their discrete weights.
// A shared width factor improves measured radii 2.5 through 32; it is not a private Photoshop kernel clone.
// The generating-function expansion uses one final allocation instead of four intermediate kernel buffers.
func gaussianBoxKernel(width float64) []float64 {
	boxRadius := int(math.Ceil((width - 1) / 2))
	boxEdge := (width - float64(2*boxRadius-1)) / 2
	outerWidth, innerWidth := 2*boxRadius+1, 2*boxRadius-1
	weights := make([]float64, 8*boxRadius+1)
	coefficients := [5][5]float64{
		{1}, {1, 1}, {1, 2, 1}, {1, 3, 3, 1}, {1, 4, 6, 4, 1},
	}
	denominator := math.Pow(width, 4)
	var total float64
	for index := range weights {
		var value float64
		for outerCount := range 5 {
			innerCount := 4 - outerCount
			mixture := coefficients[4][outerCount] * math.Pow(boxEdge, float64(outerCount)) *
				math.Pow(1-boxEdge, float64(innerCount))
			for outerStep := 0; outerStep <= outerCount; outerStep++ {
				for innerStep := 0; innerStep <= innerCount; innerStep++ {
					sample := index - innerCount - outerStep*outerWidth - innerStep*innerWidth
					if sample < 0 {
						continue
					}
					count := float64(sample)
					term := mixture * coefficients[outerCount][outerStep] *
						coefficients[innerCount][innerStep] * (count + 1) * (count + 2) * (count + 3) / 6
					if (outerStep+innerStep)&1 != 0 {
						term = -term
					}
					value += term
				}
			}
		}
		weights[index] = max(0, value/denominator)
		total += weights[index]
	}
	for index := range weights {
		weights[index] /= total
	}
	return weights
}

// gaussianSigma retains a continuous pixel-area correction calibrated for native mask and filter captures.
// It does not claim to reproduce Photoshop's private kernel or all radii within a fixed pixel tolerance.
func gaussianSigma(radius float64) float64 {
	if radius < .25 {
		return radius
	}
	if radius < .5 {
		return radius + .26*(radius-.25)
	}
	if radius < .75 {
		return radius*1.02 + .055*(.75-radius)/.25
	}
	return radius * 1.02
}

func gaussianKernel(sigma float64, radius int) []float64 {
	weights := make([]float64, radius*2+1)
	if sigma < 1e-3 {
		weights[radius] = 1
		return weights
	}
	var total float64
	for index := range weights {
		distance := float64(index - radius)
		weight := max(0, gaussianIntegral(sigma, distance+1)-
			2*gaussianIntegral(sigma, distance)+gaussianIntegral(sigma, distance-1))
		weights[index] = weight
		total += weight
	}
	for index := range weights {
		weights[index] /= total
	}
	return weights
}

func gaussianIntegral(sigma, value float64) float64 {
	standardized := value / sigma
	return sigma*math.Exp(-.5*standardized*standardized)/math.Sqrt(2*math.Pi) +
		value*.5*(1+math.Erf(standardized/math.Sqrt2))
}

func roundedByte(value float64) uint8 { return uint8(max(0, min(255, math.Floor(value+.5)))) }
