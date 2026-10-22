package psdadjust

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"math/bits"
	"unsafe"
)

// Decode reads an owned typed view of one supported Photoshop adjustment tagged-block payload.
// Original bytes remain in Raw. Unknown trailing fields, versions and modern CgEd semantics return ErrUnsupported.
func Decode(
	ctx context.Context, key string, data []byte, options ...DecodeOptions,
) (*Adjustment, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	configuration := DecodeOptions{MaxBytes: 32 << 20}
	if len(options) > 1 {
		return nil, adjustmentError(ErrInvalid, "options", 0, "provide at most one decode options value")
	}
	if len(options) == 1 {
		provided := options[0]
		if provided.MaxBytes < 0 || provided.SourceOffset < 0 {
			return nil, adjustmentError(ErrInvalid, "options", 0, "source offset and budget must not be negative")
		}
		configuration.SourceOffset = provided.SourceOffset
		if provided.MaxBytes != 0 {
			configuration.MaxBytes = provided.MaxBytes
		}
	}
	if configuration.SourceOffset > math.MaxInt64-int64(len(data)) {
		return nil, adjustmentError(ErrInvalid, "adjustment.span", configuration.SourceOffset, "source span overflows")
	}
	cursor := adjustCursor{ctx: ctx, data: data, origin: configuration.SourceOffset,
		limit: configuration.MaxBytes, allocated: int64(len(data))}
	if !cursor.reserve(int64(len(data)) + int64(unsafe.Sizeof(Adjustment{}))) {
		return nil, cursor.err
	}
	result := &Adjustment{
		Key: key, Raw: bytes.Clone(data), Span: Span{Offset: configuration.SourceOffset, Length: int64(len(data))},
	}
	switch key {
	case "phfl":
		result.Kind, result.Version = PhotoFilter, cursor.u16()
		if cursor.err == nil && result.Version != 2 && result.Version != 3 {
			cursor.unsupported("unknown photo filter version")
		}
		if cursor.err == nil && cursor.reserve(int64(unsafe.Sizeof(PhotoFilterParams{}))) {
			result.PhotoFilter = &PhotoFilterParams{}
			if result.Version == 2 {
				result.PhotoFilter.ColorSpace = cursor.u16()
				for index := range result.PhotoFilter.Components {
					result.PhotoFilter.Components[index] = cursor.u16()
				}
			} else {
				for index := range result.PhotoFilter.XYZWords {
					result.PhotoFilter.XYZWords[index] = cursor.u32()
				}
			}
			result.PhotoFilter.Density = cursor.u32()
			preserve := cursor.u8()
			if cursor.err == nil && preserve > 1 {
				cursor.invalid("photo filter luminosity flag must be boolean")
			}
			result.PhotoFilter.PreserveLuminosity = preserve == 1
		}
	case "hue2", "hue ":
		cursor.hueSaturation(result)
	case "nvrt":
		result.Kind = Invert
	case "thrs":
		result.Kind, result.Threshold = Threshold, cursor.u16()
	case "post":
		result.Kind, result.Posterize = Posterize, cursor.u16()
	case "blnc":
		result.Kind = ColorBalance
		if cursor.reserve(int64(unsafe.Sizeof(ColorBalanceParams{}))) {
			result.ColorBalance = &ColorBalanceParams{}
			for _, tonalRange := range []*[3]int16{
				&result.ColorBalance.Shadows, &result.ColorBalance.Midtones, &result.ColorBalance.Highlights,
			} {
				for component := range tonalRange {
					tonalRange[component] = int16(cursor.u16())
				}
			}
			preserve := cursor.u8()
			if cursor.err == nil && preserve > 1 {
				cursor.unsupported("unrecognized color balance luminosity flag")
			}
			result.ColorBalance.PreserveLuminosity = preserve == 1
		}
	case "mixr":
		result.Kind, result.Version = ChannelMixer, cursor.u16()
		if cursor.err == nil && result.Version != 1 {
			cursor.unsupported("unknown channel mixer version")
		}
		if cursor.reserve(int64(unsafe.Sizeof(ChannelMixerParams{}))) {
			result.ChannelMixer = &ChannelMixerParams{}
			monochrome := cursor.u16()
			if cursor.err == nil && monochrome > 1 {
				cursor.unsupported("unrecognized channel mixer monochrome flag")
			}
			result.ChannelMixer.Monochrome = monochrome == 1
			for output := range result.ChannelMixer.Rows {
				row := &result.ChannelMixer.Rows[output]
				for input := range row.Inputs {
					row.Inputs[input] = int16(cursor.u16())
				}
				row.Constant = int16(cursor.u16())
			}
		}
	case "levl":
		result.Kind, result.Version = Levels, cursor.u16()
		if cursor.err == nil && result.Version != 2 {
			cursor.unsupported("unknown levels version")
		}
		if cursor.err == nil {
			result.Levels = cursor.levels(29, 0)
		}
		if cursor.err == nil && cursor.remaining() > 3 {
			marker := cursor.take(4)
			result.ExtraVersion = cursor.u16()
			count := cursor.u16()
			if cursor.err == nil && (!bytes.Equal(marker, []byte("Lvls")) || result.ExtraVersion != 3 || count < 29) {
				cursor.unsupported("unknown extra levels marker")
			}
			if cursor.err == nil {
				result.Levels = append(result.Levels, cursor.levels(int(count)-29, 29)...)
			}
		}
	case "curv":
		result.Kind = Curves
		mapFlag := cursor.u8()
		result.CurveMap, result.Version, result.CountMap = mapFlag == 1, cursor.u16(), cursor.u32()
		if cursor.err == nil && (mapFlag > 1 || result.Version != 1 && result.Version != 4) {
			cursor.unsupported("unknown curves representation or version")
		}
		count := int(result.CountMap)
		if result.Version == 1 {
			count = bits.OnesCount32(result.CountMap)
		}
		result.Curves = cursor.curves(count, result.CurveMap, false)
		if result.Version == 1 && cursor.err == nil {
			index := 0
			for channel := range uint16(32) {
				if result.CountMap&(1<<channel) != 0 {
					result.Curves[index].Channel = channel
					index++
				}
			}
		}
		if cursor.err == nil && cursor.remaining() > 3 {
			marker := cursor.take(4)
			result.ExtraVersion = cursor.u16()
			extraCount := cursor.u32()
			if cursor.err == nil && (!bytes.Equal(marker, []byte("Crv ")) || result.ExtraVersion != 4) {
				cursor.unsupported("unknown extra curves marker")
			}
			if cursor.err == nil {
				result.LegacyCurves = result.Curves
				result.Curves = cursor.curves(int(extraCount), result.CurveMap, true)
			}
		}
	case "expA":
		result.Kind, result.Version = Exposure, cursor.u16()
		if cursor.err == nil && result.Version != 1 {
			cursor.unsupported("unknown exposure version")
		}
		if cursor.err == nil && cursor.reserve(int64(unsafe.Sizeof(ExposureParams{}))) {
			result.Exposure = &ExposureParams{Exposure: cursor.f32(), Offset: cursor.f32(), Gamma: cursor.f32()}
		}
	case "brit":
		result.Kind = BrightnessContrast
		if cursor.reserve(int64(unsafe.Sizeof(BrightnessContrastParams{}))) {
			result.BrightnessContrast = &BrightnessContrastParams{
				Brightness: int16(cursor.u16()), Contrast: int16(cursor.u16()), Mean: cursor.u16(), Legacy: true,
			}
			lab := cursor.u8()
			if cursor.err == nil && lab > 1 {
				cursor.invalid("brightness/contrast Lab flag is not boolean")
			}
			result.BrightnessContrast.LabOnly = lab != 0
		}
	default:
		cursor.unsupported("adjustment tagged-block semantics are not implemented")
	}
	if cursor.err == nil && (cursor.remaining() > 3 || !zeroAdjustmentBytes(cursor.take(cursor.remaining()))) {
		cursor.unsupported("unrecognized adjustment payload tail")
	}
	if cursor.err != nil {
		if errors.Is(cursor.err, ErrUnsupported) {
			result.Unsupported = append(result.Unsupported, "retained unrecognized adjustment semantics")
		}
		return result, cursor.err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, nil
}

type adjustCursor struct {
	ctx       context.Context
	data      []byte
	position  int
	origin    int64
	allocated int64
	limit     int64
	err       error
}

func (c *adjustCursor) remaining() int { return len(c.data) - c.position }

func (c *adjustCursor) invalid(detail string) {
	if c.err == nil {
		c.err = adjustmentError(ErrInvalid, "adjustment.payload", c.origin+int64(c.position), detail)
	}
}

func (c *adjustCursor) unsupported(detail string) {
	if c.err == nil {
		c.err = adjustmentError(ErrUnsupported, "adjustment.payload", c.origin+int64(c.position), detail)
	}
}

func (c *adjustCursor) reserve(length int64) bool {
	if c.err != nil {
		return false
	}
	if length < 0 || length > c.limit-c.allocated {
		c.err = adjustmentError(ErrLimit, "adjustment.metadata", c.origin+int64(c.position), "owned metadata exceeds budget")
	}
	if c.err == nil {
		c.allocated += length
	}
	return c.err == nil
}

func (c *adjustCursor) take(count int) []byte {
	if c.err != nil {
		return nil
	}
	if err := c.ctx.Err(); err != nil {
		c.err = err
		return nil
	}
	if count < 0 || count > c.remaining() {
		c.invalid("adjustment field exceeds its payload")
		return nil
	}
	data := c.data[c.position : c.position+count]
	c.position += count
	return data
}

func (c *adjustCursor) u8() uint8 {
	data := c.take(1)
	if len(data) == 0 {
		return 0
	}
	return data[0]
}

func (c *adjustCursor) u16() uint16 {
	data := c.take(2)
	if len(data) != 2 {
		return 0
	}
	return binary.BigEndian.Uint16(data)
}

func (c *adjustCursor) u32() uint32 {
	data := c.take(4)
	if len(data) != 4 {
		return 0
	}
	return binary.BigEndian.Uint32(data)
}

func (c *adjustCursor) f32() float64 { return float64(math.Float32frombits(c.u32())) }

func (c *adjustCursor) levels(count int, start uint16) []LevelChannel {
	if count < 0 || count > 64 || count > c.remaining()/10 ||
		!c.reserve(int64(count)*int64(unsafe.Sizeof(LevelChannel{}))) {
		if c.err == nil {
			c.invalid("level count exceeds its payload or channel limit")
		}
		return nil
	}
	result := make([]LevelChannel, count)
	for index := range result {
		result[index] = LevelChannel{Channel: start + uint16(index), InputBlack: c.u16(), InputWhite: c.u16(),
			OutputBlack: c.u16(), OutputWhite: c.u16(), Gamma: float64(c.u16()) / 100}
	}
	return result
}

func (c *adjustCursor) curves(count int, mapped, indexed bool) []CurveChannel {
	if c.err != nil {
		return nil
	}
	if count < 0 || count > 64 || !c.reserve(int64(count)*int64(unsafe.Sizeof(CurveChannel{}))) {
		if c.err == nil {
			c.invalid("curve count exceeds its channel limit")
		}
		return nil
	}
	result := make([]CurveChannel, count)
	for index := range result {
		channel := uint16(index)
		if indexed {
			channel = c.u16()
		}
		result[index].Channel = channel
		if mapped {
			if !c.reserve(256) {
				return nil
			}
			result[index].LUT = bytes.Clone(c.take(256))
		} else {
			points := int(c.u16())
			if c.err == nil && (points < 2 || points > 19 || points > c.remaining()/4) {
				c.invalid("curve point count exceeds its payload or supported range")
			}
			if !c.reserve(int64(points) * int64(unsafe.Sizeof(CurvePoint{}))) {
				return nil
			}
			result[index].Points = make([]CurvePoint, points)
			for point := range result[index].Points {
				output, input := c.u16(), c.u16()
				result[index].Points[point] = CurvePoint{Input: input, Output: output}
			}
		}
		if c.err != nil {
			return nil
		}
	}
	return result
}

func zeroAdjustmentBytes(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}
