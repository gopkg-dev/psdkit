package psdadjust

import (
	"context"
	"image"
	"math"
)

// Transform is a validated immutable RGB adjustment prepared for repeated pixel evaluation.
// It retains no references to caller-owned parameter slices or image buffers.
type Transform struct {
	kind       Kind
	threshold  uint16
	present    [4]bool
	luts       *[4][256]float64
	mixer      *[3][4]float64
	monochrome *[4]int16
	hue        *HueSaturationParams
}

// Compile validates parameters and prepares a bounded immutable RGB8 transform.
func Compile(adjustment Adjustment, options ...CompileOptions) (*Transform, error) {
	if len(options) > 1 {
		return nil, adjustmentError(ErrInvalid, "options", 0, "provide at most one compile options value")
	}
	configuration := CompileOptions{}
	if len(options) == 1 {
		configuration = options[0]
	}
	if len(adjustment.Unsupported) != 0 {
		return nil, adjustmentError(ErrUnsupported, "adjustment", adjustment.Span.Offset, adjustment.Unsupported[0])
	}
	result := &Transform{kind: adjustment.Kind, threshold: adjustment.Threshold}
	switch adjustment.Kind {
	case PhotoFilter:
		if err := validatePhotoFilter(adjustment); err != nil {
			return nil, err
		}
		if adjustment.PhotoFilter.Density != 0 {
			return nil, adjustmentError(ErrUnsupported, "adjustment.photoFilter", adjustment.Span.Offset,
				"nonzero photo filter requires native color and luminosity verification")
		}
	case HueSaturation:
		if err := compileHueSaturation(result, adjustment.HueSaturation,
			configuration.ApproximateHueSaturation); err != nil {
			return nil, err
		}
	case Invert:
		return result, nil
	case Threshold:
		if adjustment.Threshold > 255 {
			return nil, adjustmentError(ErrInvalid, "adjustment.threshold", 0, "threshold must be between zero and 255")
		}
		return result, nil
	case Posterize:
		if adjustment.Posterize < 2 || adjustment.Posterize > 255 {
			return nil, adjustmentError(ErrInvalid, "adjustment.posterize", 0, "posterize level count must be 2 through 255")
		}
		result.luts, result.present[0] = new([4][256]float64), true
		for value := range 256 {
			result.luts[0][value] = float64(value*int(adjustment.Posterize)/256) / float64(adjustment.Posterize-1)
		}
	case Levels:
		if len(adjustment.Levels) > 64 {
			return nil, adjustmentError(ErrLimit, "adjustment.levels", 0, "level record count exceeds 64")
		}
		result.luts = new([4][256]float64)
		for _, level := range adjustment.Levels {
			if level.Channel > 3 {
				if emptyLevel(level) || identityLevel(level) {
					continue
				}
				return nil, adjustmentError(ErrUnsupported, "adjustment.levels.channel", 0, "non-RGB level changes are unsupported")
			}
			if result.present[level.Channel] {
				return nil, adjustmentError(ErrInvalid, "adjustment.levels.channel", 0, "duplicate level channel")
			}
			if level.InputBlack >= level.InputWhite || level.InputWhite > 255 || level.OutputBlack > 255 ||
				level.OutputWhite > 255 || !finite(level.Gamma) || level.Gamma < .01 || level.Gamma > 9.99 {
				return nil, adjustmentError(ErrInvalid, "adjustment.levels", 0, "invalid input/output range or gamma")
			}
			result.present[level.Channel] = true
			for value := range 256 {
				input := clamp(float64(value-int(level.InputBlack)) / float64(level.InputWhite-level.InputBlack))
				output := float64(level.OutputBlack) + math.Pow(input, 1/level.Gamma)*
					(float64(level.OutputWhite)-float64(level.OutputBlack))
				result.luts[level.Channel][value] = clamp(output / 255)
			}
		}
	case Curves:
		if len(adjustment.Curves) > 64 {
			return nil, adjustmentError(ErrLimit, "adjustment.curves", 0, "curve channel count exceeds 64")
		}
		result.luts = new([4][256]float64)
		for _, curve := range adjustment.Curves {
			if curve.Channel > 3 {
				if identityCurve(curve) {
					continue
				}
				return nil, adjustmentError(ErrUnsupported, "adjustment.curves.channel", 0, "non-RGB curve changes are unsupported")
			}
			if result.present[curve.Channel] {
				return nil, adjustmentError(ErrInvalid, "adjustment.curves.channel", 0, "duplicate curve channel")
			}
			if err := compileCurve(&result.luts[curve.Channel], curve); err != nil {
				return nil, err
			}
			result.present[curve.Channel] = true
		}
	case Exposure:
		parameters := adjustment.Exposure
		if parameters == nil || !finite(parameters.Exposure) || !finite(parameters.Offset) || !finite(parameters.Gamma) ||
			parameters.Exposure < -20 || parameters.Exposure > 20 || parameters.Offset < -.5 || parameters.Offset > .5 ||
			parameters.Gamma < float64(float32(.01)) || parameters.Gamma > 9.99 {
			return nil, adjustmentError(ErrInvalid, "adjustment.exposure", 0, "invalid exposure, offset or gamma")
		}
		result.luts, result.present[0] = new([4][256]float64), true
		factor := math.Exp2(parameters.Exposure)
		for value := range 256 {
			linear := clamp(math.Pow(float64(value)/255, 2.2)*factor + parameters.Offset)
			result.luts[0][value] = clamp(math.Pow(linear, 1/parameters.Gamma/2.2))
		}
	case BrightnessContrast:
		parameters := adjustment.BrightnessContrast
		if parameters == nil || parameters.Mean > 255 || parameters.Brightness < -100 || parameters.Brightness > 100 ||
			parameters.Contrast < -100 || parameters.Contrast > 100 {
			return nil, adjustmentError(ErrInvalid, "adjustment.brightnessContrast", 0, "invalid brightness/contrast fields")
		}
		if !parameters.Legacy || parameters.LabOnly || parameters.Brightness != 0 || parameters.Contrast != 0 {
			return nil, adjustmentError(ErrUnsupported, "adjustment.brightnessContrast", 0,
				"non-neutral brightness/contrast requires native algorithm verification")
		}
	case ColorBalance:
		if err := validateColorBalance(adjustment.ColorBalance); err != nil {
			return nil, err
		}
		if adjustment.ColorBalance.Shadows != ([3]int16{}) || adjustment.ColorBalance.Midtones != ([3]int16{}) ||
			adjustment.ColorBalance.Highlights != ([3]int16{}) {
			return nil, adjustmentError(ErrUnsupported, "adjustment.colorBalance", 0,
				"non-neutral color balance requires native transfer and luminosity verification")
		}
	case ChannelMixer:
		if err := compileChannelMixer(result, adjustment.ChannelMixer); err != nil {
			return nil, err
		}
	default:
		return nil, adjustmentError(ErrUnsupported, "adjustment.kind", 0, "adjustment kind is not implemented")
	}
	return result, nil
}

func validateColorBalance(parameters *ColorBalanceParams) error {
	if parameters == nil {
		return adjustmentError(ErrInvalid, "adjustment.colorBalance", 0, "color balance parameters are absent")
	}
	for _, tonalRange := range [][3]int16{parameters.Shadows, parameters.Midtones, parameters.Highlights} {
		for _, offset := range tonalRange {
			if offset < -100 || offset > 100 {
				return adjustmentError(ErrInvalid, "adjustment.colorBalance", 0, "tonal color offset exceeds -100 to 100")
			}
		}
	}
	return nil
}

// RGB evaluates one straight encoded-RGB sample, clamping inputs and outputs to zero through one.
// It does not alter alpha or retain HDR values; callers supply alpha independently.
func (transform *Transform) RGB(color [3]float64) [3]float64 {
	for index := range color {
		color[index] = clamp(color[index])
	}
	if transform.kind == Invert {
		for index := range color {
			color[index] = 1 - color[index]
		}
		return color
	}
	if transform.kind == Threshold {
		luminance := math.Round((.299*color[0] + .587*color[1] + .114*color[2]) * 255)
		value := float64(0)
		if luminance >= float64(transform.threshold) {
			value = 1
		}
		return [3]float64{value, value, value}
	}
	if transform.mixer != nil {
		var result [3]float64
		for output, row := range transform.mixer {
			value := row[3]
			for input := range color {
				value += row[input] * color[input]
			}
			result[output] = clamp(value)
		}
		return result
	}
	if transform.monochrome != nil {
		return transform.monochromeRGB(color)
	}
	if transform.hue != nil {
		return transform.hueRGB(color)
	}
	if transform.luts != nil {
		for component := range color {
			if transform.present[component+1] {
				color[component] = lookup(&transform.luts[component+1], color[component])
			}
			if transform.present[0] {
				color[component] = lookup(&transform.luts[0], color[component])
			}
		}
	}
	return color
}

// Apply returns independently owned adjusted pixels with the source rectangle and exact original alpha.
// Caller-owned input buffers and parameters must remain immutable for the duration of the call.
func Apply(
	ctx context.Context, source *image.NRGBA, adjustment Adjustment, options ...Options,
) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	configuration, err := applyConfiguration(options)
	if err != nil {
		return nil, err
	}
	width, height, err := validateImage(source)
	if err != nil {
		return nil, err
	}
	working := int64(128)
	if adjustment.Kind == Levels || adjustment.Kind == Curves ||
		adjustment.Kind == Exposure || adjustment.Kind == Posterize || adjustment.Kind == ChannelMixer ||
		adjustment.Kind == HueSaturation {
		working = 16 << 10
	}
	if int64(width) > configuration.MaxPixels/int64(height) || working > configuration.MaxWorkingBytes ||
		int64(width) > (configuration.MaxWorkingBytes-working)/4/int64(height) {
		return nil, adjustmentError(ErrLimit, "image", 0, "adjusted output and transform exceed resource budgets")
	}
	transform, err := Compile(adjustment, CompileOptions{
		ApproximateHueSaturation: configuration.ApproximateHueSaturation,
	})
	if err != nil {
		return nil, err
	}
	result := image.NewNRGBA(source.Rect)
	for row := range height {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for column := range width {
			if column&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			input, output := row*source.Stride+column*4, row*result.Stride+column*4
			if transform.monochrome != nil {
				value := transform.monochromeByte(source.Pix[input : input+3])
				for component := range 3 {
					result.Pix[output+component] = value
				}
			} else {
				color := transform.RGB([3]float64{
					float64(source.Pix[input]) / 255, float64(source.Pix[input+1]) / 255, float64(source.Pix[input+2]) / 255,
				})
				for component := range 3 {
					result.Pix[output+component] = uint8(math.Round(clamp(color[component]) * 255))
				}
			}
			result.Pix[output+3] = source.Pix[input+3]
		}
	}
	return result, nil
}

func validateImage(source *image.NRGBA) (int, int, error) {
	if source == nil || source.Rect.Max.X < source.Rect.Min.X || source.Rect.Max.Y < source.Rect.Min.Y {
		return 0, 0, adjustmentError(ErrInvalid, "image", 0, "source image or rectangle is invalid")
	}
	width, height := source.Rect.Dx(), source.Rect.Dy()
	if width <= 0 || height <= 0 || width > int(^uint(0)>>1)/4 || source.Stride < width*4 ||
		len(source.Pix) < width*4 || height-1 > (len(source.Pix)-width*4)/source.Stride {
		return 0, 0, adjustmentError(ErrInvalid, "image", 0, "image dimensions, stride or backing buffer are invalid")
	}
	return width, height, nil
}

func applyConfiguration(options []Options) (Options, error) {
	configuration := Options{MaxPixels: 20_000_000, MaxWorkingBytes: 256 << 20}
	if len(options) > 1 {
		return configuration, adjustmentError(ErrInvalid, "options", 0, "provide at most one options value")
	}
	if len(options) == 1 {
		provided := options[0]
		configuration.ApproximateHueSaturation = provided.ApproximateHueSaturation
		if provided.MaxPixels < 0 || provided.MaxWorkingBytes < 0 {
			return configuration, adjustmentError(ErrInvalid, "options", 0, "budgets must not be negative")
		}
		if provided.MaxPixels != 0 {
			configuration.MaxPixels = provided.MaxPixels
		}
		if provided.MaxWorkingBytes != 0 {
			configuration.MaxWorkingBytes = provided.MaxWorkingBytes
		}
	}
	return configuration, nil
}

func emptyLevel(level LevelChannel) bool {
	return level.InputBlack == 0 && level.InputWhite == 0 && level.OutputBlack == 0 &&
		level.OutputWhite == 0 && level.Gamma == 0
}

func identityLevel(level LevelChannel) bool {
	return level.InputBlack == 0 && level.InputWhite == 255 && level.OutputBlack == 0 &&
		level.OutputWhite == 255 && level.Gamma == 1
}

func lookup(table *[256]float64, value float64) float64 {
	position := clamp(value) * 255
	index := int(position)
	if index >= 255 {
		return table[255]
	}
	return table[index] + (table[index+1]-table[index])*(position-float64(index))
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func clamp(value float64) float64 {
	if !(value > 0) {
		return 0
	}
	return min(value, 1)
}
