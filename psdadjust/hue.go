package psdadjust

import (
	"bytes"
	"encoding/binary"
	"math"
	"unsafe"
)

// HueSaturationValues contains hue degrees, saturation percentage and lightness percentage.
type HueSaturationValues struct{ Hue, Saturation, Lightness int16 }

// HueSaturationRange retains a circular feather and sustain band plus its adjustment.
// Bounds are hue degrees in begin-ramp, begin-sustain, end-sustain, end-ramp order.
type HueSaturationRange struct {
	Bounds [4]int16
	Values HueSaturationValues
}

// HueSaturationParams retains master, colorize and six ordered red-through-magenta range controls.
// Padding and Unknown retain ignored source bytes, including Photoshop's auxiliary range-color triples.
// Plain master Lightness uses the native-verified RGB8 byte transfer.
// Other non-neutral RGB8 rendering requires an explicit approximation policy; native failures remain in QA.
type HueSaturationParams struct {
	Colorize     bool
	Padding      uint8
	Colorization HueSaturationValues
	Master       HueSaturationValues
	Ranges       [6]HueSaturationRange
	Unknown      []byte
}

func (c *adjustCursor) hueSaturation(result *Adjustment) {
	result.Kind, result.Version = HueSaturation, c.u16()
	if c.err == nil && result.Version != 2 {
		c.unsupported("unknown hue/saturation version")
	}
	if !c.reserve(int64(unsafe.Sizeof(HueSaturationParams{}))) {
		return
	}
	parameters := &HueSaturationParams{}
	result.HueSaturation = parameters
	colorize := c.u8()
	parameters.Padding = c.u8()
	if c.err == nil && colorize > 1 {
		c.unsupported("unrecognized hue/saturation colorize flag")
	}
	parameters.Colorize = colorize == 1
	parameters.Colorization, parameters.Master = c.hueValues(), c.hueValues()
	for index := range parameters.Ranges {
		rangeValues := &parameters.Ranges[index]
		for bound := range rangeValues.Bounds {
			rangeValues.Bounds[bound] = int16(c.u16())
		}
		rangeValues.Values = c.hueValues()
	}
	if c.reserve(int64(c.remaining())) {
		parameters.Unknown = bytes.Clone(c.take(c.remaining()))
	}
}

func (c *adjustCursor) hueValues() HueSaturationValues {
	return HueSaturationValues{Hue: int16(c.u16()), Saturation: int16(c.u16()), Lightness: int16(c.u16())}
}

func validateHueSaturation(parameters *HueSaturationParams) error {
	if parameters == nil {
		return adjustmentError(ErrInvalid, "adjustment.hueSaturation", 0, "hue/saturation parameters are absent")
	}
	values := parameters.Colorization
	if values.Hue < -180 || values.Hue > 360 || values.Saturation < 0 || values.Saturation > 100 ||
		values.Lightness < -100 || values.Lightness > 100 {
		return adjustmentError(ErrInvalid, "adjustment.hueSaturation.colorization", 0, "invalid colorization controls")
	}
	if err := validateHueValues(parameters.Master); err != nil {
		return err
	}
	for _, rangeValues := range parameters.Ranges {
		if err := validateHueValues(rangeValues.Values); err != nil {
			return err
		}
		for _, bound := range rangeValues.Bounds {
			if bound < 0 || bound > 360 {
				return adjustmentError(ErrInvalid, "adjustment.hueSaturation.range", 0, "range bounds exceed zero to 360")
			}
		}
		// Bands may wrap through red, but their four boundaries must advance at most one complete circle.
		total := 0.0
		for index := 1; index < 4; index++ {
			total += hueModulo(float64(rangeValues.Bounds[index] - rangeValues.Bounds[index-1]))
		}
		if total > 360 {
			return adjustmentError(ErrInvalid, "adjustment.hueSaturation.range", 0, "range boundaries are out of circular order")
		}
	}
	return nil
}

func validateHueValues(values HueSaturationValues) error {
	if values.Hue < -180 || values.Hue > 180 || values.Saturation < -100 || values.Saturation > 100 ||
		values.Lightness < -100 || values.Lightness > 100 {
		return adjustmentError(ErrInvalid, "adjustment.hueSaturation.values", 0, "controls exceed hue or percentage limits")
	}
	return nil
}

func compileHueSaturation(transform *Transform, parameters *HueSaturationParams, approximate bool) error {
	if err := validateHueSaturation(parameters); err != nil {
		return err
	}
	if !approximate && !huePlainMasterLightness(parameters) {
		return adjustmentError(ErrUnsupported, "adjustment.hueSaturation", 0,
			"Hue, Saturation, local and Colorize controls require ApproximateHueSaturation")
	}
	owned := *parameters
	owned.Unknown = nil
	transform.hue = &owned
	return nil
}

func encodeHueSaturation(adjustment Adjustment, original *Adjustment) ([]byte, error) {
	parameters := adjustment.HueSaturation
	data := bytes.Clone(adjustment.Raw)
	if original != nil && (parameters.Padding != original.HueSaturation.Padding ||
		!bytes.Equal(parameters.Unknown, original.HueSaturation.Unknown)) {
		return nil, adjustmentError(ErrUnsupported, "adjustment.hueSaturation.unknown", 0,
			"ignored source hue/saturation bytes cannot be edited")
	}
	if len(data) == 0 {
		data = make([]byte, 100, 100+len(parameters.Unknown))
		binary.BigEndian.PutUint16(data, 2)
		data = append(data, parameters.Unknown...)
	}
	data[2], data[3] = 0, parameters.Padding
	if parameters.Colorize {
		data[2] = 1
	}
	putHueValues(data[4:], parameters.Colorization)
	putHueValues(data[10:], parameters.Master)
	for index, rangeValues := range parameters.Ranges {
		position := 16 + index*14
		for bound, value := range rangeValues.Bounds {
			binary.BigEndian.PutUint16(data[position+bound*2:], uint16(value))
		}
		putHueValues(data[position+8:], rangeValues.Values)
	}
	return data, nil
}

func putHueValues(data []byte, values HueSaturationValues) {
	for index, value := range []int16{values.Hue, values.Saturation, values.Lightness} {
		binary.BigEndian.PutUint16(data[index*2:], uint16(value))
	}
}

func hueModulo(value float64) float64 {
	value = math.Mod(value, 360)
	if value < 0 {
		value += 360
	}
	if value >= 360 {
		return 0
	}
	return value
}

func hueRangeWeight(hue float64, bounds [4]int16) float64 {
	position := hueModulo(hue - float64(bounds[0]))
	start := hueModulo(float64(bounds[1] - bounds[0]))
	end := start + hueModulo(float64(bounds[2]-bounds[1]))
	stop := end + hueModulo(float64(bounds[3]-bounds[2]))
	if position < start {
		return position / start
	}
	if position <= end {
		return 1
	}
	if position < stop {
		return (stop - position) / (stop - end)
	}
	return 0
}

func (transform *Transform) hueRGB(color [3]float64) [3]float64 {
	parameters := transform.hue
	if !parameters.Colorize && hueNeutral(parameters) {
		return color
	}
	if huePlainMasterLightness(parameters) {
		return hueMasterLightnessRGB(color, parameters.Master.Lightness)
	}
	if huePlainMasterHue(parameters) {
		return hueMasterHueRGB(color, parameters.Master.Hue)
	}
	packed := huePackedRGB(color)
	if !parameters.Colorize {
		color = hueLightness(color, float64(parameters.Master.Lightness)/100)
	}
	highest, lowest := max(color[0], color[1], color[2]), min(color[0], color[1], color[2])
	chroma, lightness, hue := highest-lowest, (highest+lowest)/2, 0.0
	if !parameters.Colorize && chroma == 0 && parameters.Master.Lightness == 0 {
		return color
	}
	if chroma != 0 {
		switch highest {
		case color[0]:
			hue = 60 * (color[1] - color[2]) / chroma
		case color[1]:
			hue = 60 * (2 + (color[2]-color[0])/chroma)
		case color[2]:
			hue = 60 * (4 + (color[0]-color[1])/chroma)
		}
		hue = hueModulo(hue)
	}
	if parameters.Colorize {
		amount := float64(parameters.Colorization.Lightness) / 100
		if amount < 0 {
			lightness *= 1 + amount
		} else {
			lightness += (1 - lightness) * amount
		}
		color = hueColor(float64(parameters.Colorization.Hue),
			(1-math.Abs(2*lightness-1))*float64(parameters.Colorization.Saturation)/100, lightness)
		return hueOutput(color)
	}
	hueShift := float64(parameters.Master.Hue)
	var lightnessShift float64
	rangeGain := 1.0
	if chroma != 0 {
		for _, rangeValues := range parameters.Ranges {
			weight := huePackedRangeWeight(packed, rangeValues.Bounds)
			hueShift += float64(rangeValues.Values.Hue) * weight
			saturation := float64(rangeValues.Values.Saturation) / 100
			switch {
			case saturation > 0:
				rangeGain += (huePositiveGain(saturation) - 1) * weight
			case saturation < 0:
				rangeGain += saturation * weight
			}
			lightnessShift += float64(rangeValues.Values.Lightness) / 100 * weight
		}
	}
	lightnessShift = max(-1, min(1, lightnessShift))
	if lightnessShift < 0 {
		highest += lightnessShift * chroma
	} else {
		lowest += lightnessShift * chroma
	}
	chroma, lightness = highest-lowest, (highest+lowest)/2
	if chroma != 0 {
		chroma = min(1-math.Abs(2*lightness-1), chroma*max(0, rangeGain))
	}
	chroma = hueChroma(chroma, lightness, float64(parameters.Master.Saturation)/100)
	color = hueColor(hue+hueShift, chroma, lightness)
	return hueOutput(color)
}

func huePlainMasterLightness(parameters *HueSaturationParams) bool {
	if parameters.Colorize || parameters.Master.Hue != 0 || parameters.Master.Saturation != 0 {
		return false
	}
	for _, rangeValues := range parameters.Ranges {
		if rangeValues.Values != (HueSaturationValues{}) {
			return false
		}
	}
	return true
}

func hueMasterLightnessRGB(color [3]float64, lightness int16) [3]float64 {
	// Photoshop's RGB8 master control quantizes the percentage before rounding each channel.
	strength := int(lightness)
	if strength < 0 {
		strength = -strength
	}
	code := strength * 255 / 100
	for component := range color {
		value := clamp(color[component]) * float64(255-code)
		if lightness > 0 {
			value += float64(code)
		}
		color[component] = math.Floor(value+.5) / 255
	}
	return color
}

func huePlainMasterHue(parameters *HueSaturationParams) bool {
	if parameters.Colorize || parameters.Master.Saturation != 0 || parameters.Master.Lightness != 0 {
		return false
	}
	for _, rangeValues := range parameters.Ranges {
		if rangeValues.Values != (HueSaturationValues{}) {
			return false
		}
	}
	return true
}

func hueMasterHueRGB(color [3]float64, hue int16) [3]float64 {
	// This RGB8 phase and inverse apply only to the plain master Hue control.
	var codes [3]int
	for component := range codes {
		codes[component] = int(math.Round(clamp(color[component]) * 255))
	}
	highest, lowest := max(codes[0], codes[1], codes[2]), min(codes[0], codes[1], codes[2])
	chroma := highest - lowest
	if chroma == 0 {
		return [3]float64{float64(lowest) / 255, float64(lowest) / 255, float64(lowest) / 255}
	}
	phase, numerator := 0, codes[1]-codes[2]
	switch highest {
	case codes[0]:
	case codes[1]:
		phase, numerator = 510, codes[2]-codes[0]
	case codes[2]:
		phase, numerator = 1020, codes[0]-codes[1]
	}
	// Source phase ties round away from zero; control ties round toward positive infinity.
	sign := 1
	if numerator < 0 {
		sign, numerator = -1, -numerator
	}
	phase += sign * (2*numerator*255 + chroma) / (2 * chroma)
	phase += int(math.Floor(float64(hue)*17/4 + .5))
	phase = (phase%1530 + 1530) % 1530
	sector, remainder := phase/255, phase%255
	if sector%2 != 0 {
		remainder = 255 - remainder
	}
	secondary := lowest + (chroma*remainder+128)/256
	switch sector {
	case 0:
		codes = [3]int{highest, secondary, lowest}
	case 1:
		codes = [3]int{secondary, highest, lowest}
	case 2:
		codes = [3]int{lowest, highest, secondary}
	case 3:
		codes = [3]int{lowest, secondary, highest}
	case 4:
		codes = [3]int{secondary, lowest, highest}
	case 5:
		codes = [3]int{highest, lowest, secondary}
	}
	for component := range color {
		color[component] = float64(codes[component]) / 255
	}
	return color
}

func hueNeutral(parameters *HueSaturationParams) bool {
	if parameters.Master != (HueSaturationValues{}) {
		return false
	}
	for _, rangeValues := range parameters.Ranges {
		if rangeValues.Values != (HueSaturationValues{}) {
			return false
		}
	}
	return true
}

func hueChroma(chroma, lightness, saturation float64) float64 {
	if saturation < 0 {
		return chroma * max(0, 1+saturation)
	}
	if chroma == 0 {
		return 0
	}
	ceiling := 1 - math.Abs(2*lightness-1)
	return min(ceiling, chroma*huePositiveGain(saturation))
}

func huePositiveGain(saturation float64) float64 {
	control := min(254, math.Floor(saturation*255+1e-9))
	return 256 / (256 - control)
}

func hueOutput(color [3]float64) [3]float64 {
	for component := range color {
		color[component] = math.Floor(clamp(color[component])*255+1e-8) / 255
	}
	return color
}

func huePackedRGB(color [3]float64) float64 {
	// The signed-sector selector is verified against independent RGB8 +100 range controls.
	// Other Hue/Saturation transfers still require the explicit approximation policy.
	var codes [3]float64
	for component := range codes {
		codes[component] = math.Round(clamp(color[component]) * 255)
	}
	highest, lowest := max(codes[0], codes[1], codes[2]), min(codes[0], codes[1], codes[2])
	chroma := highest - lowest
	if chroma == 0 {
		return 0
	}
	var packed float64
	switch highest {
	case codes[0]:
		fraction := (codes[1] - codes[2]) / chroma
		packed = math.Round(fraction * 255)
		if fraction < 0 {
			packed += 1533
			if fraction <= -.5 {
				packed--
			}
		}
	case codes[1]:
		packed = 511 + math.Round((codes[2]-codes[0])*255/chroma)
	case codes[2]:
		fraction := (codes[0] - codes[1]) / chroma
		packed = 1022 + math.Round(fraction*255)
		if fraction <= -.5 {
			packed--
		}
	}
	return math.Mod(packed+1536, 1536)
}

func huePackedRangeWeight(packed float64, bounds [4]int16) float64 {
	var codes [4]float64
	for index, bound := range bounds {
		codes[index] = math.Floor(float64(bound) * 1536 / 360)
	}
	position := math.Mod(packed-codes[0]+1536, 1536)
	start := math.Mod(codes[1]-codes[0]+1536, 1536)
	end := start + math.Mod(codes[2]-codes[1]+1536, 1536)
	stop := end + math.Mod(codes[3]-codes[2]+1536, 1536)
	if position < start {
		return position / start
	}
	if position <= end {
		return 1
	}
	if position < stop {
		return (stop - position) / (stop - end)
	}
	return 0
}

func hueColor(hue, chroma, lightness float64) [3]float64 {
	hue = hueModulo(hue) / 60
	second := chroma * (1 - math.Abs(math.Mod(hue, 2)-1))
	var color [3]float64
	switch int(hue) {
	case 0:
		color = [3]float64{chroma, second, 0}
	case 1:
		color = [3]float64{second, chroma, 0}
	case 2:
		color = [3]float64{0, chroma, second}
	case 3:
		color = [3]float64{0, second, chroma}
	case 4:
		color = [3]float64{second, 0, chroma}
	case 5:
		color = [3]float64{chroma, 0, second}
	}
	for component := range color {
		color[component] += lightness - chroma/2
	}
	return color
}

func hueLightness(color [3]float64, amount float64) [3]float64 {
	amount = max(-1, min(1, amount))
	for component := range color {
		if amount < 0 {
			color[component] *= 1 + amount
		} else {
			color[component] += (1 - color[component]) * amount
		}
		color[component] = clamp(color[component])
	}
	return color
}
