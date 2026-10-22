package psdadjust

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"reflect"
	"slices"
	"unsafe"
)

// EncodeOptions bounds owned original copies, transform scratch and the encoded adjustment payload.
// Zero MaxBytes selects a 32 MiB budget.
type EncodeOptions struct{ MaxBytes int64 }

// Encode serializes a known RGB8 adjustment family, preserving original bytes when semantically unchanged.
// Color Balance's known metadata can round-trip unchanged while non-neutral rendering remains unsupported.
// With Raw supplied, the original family and supported wire representation remain fixed.
// Scalar and level edits patch original fields; changed curves retain the source channel sets and extra-marker version.
func Encode(
	ctx context.Context, adjustment Adjustment, options ...EncodeOptions,
) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := int64(32 << 20)
	if len(options) > 1 || len(options) == 1 && options[0].MaxBytes < 0 {
		return nil, adjustmentError(ErrInvalid, "options", 0, "invalid encoding budget or option count")
	}
	if len(options) == 1 && options[0].MaxBytes != 0 {
		limit = options[0].MaxBytes
	}
	key := adjustmentKey(adjustment.Kind)
	if adjustment.Kind == HueSaturation && adjustment.Key == "hue " {
		key = adjustment.Key
	}
	if key == "" || adjustment.Key != "" && adjustment.Key != key {
		return nil, adjustmentError(ErrUnsupported, "adjustment.kind", 0, "adjustment family or key is unsupported")
	}
	working := int64(256)
	if adjustment.Kind == PhotoFilter {
		working += int64(unsafe.Sizeof(PhotoFilterParams{}))
	}
	if adjustment.Kind == Levels || adjustment.Kind == Curves ||
		adjustment.Kind == Exposure || adjustment.Kind == Posterize || adjustment.Kind == ChannelMixer ||
		adjustment.Kind == HueSaturation {
		working = 16 << 10
	}
	if working > limit || int64(len(adjustment.Raw)) > (limit-working)/4 {
		return nil, adjustmentError(ErrLimit, "adjustment.raw", 0, "original payload exceeds encoding budgets")
	}
	if adjustment.HueSaturation != nil && int64(len(adjustment.HueSaturation.Unknown)) > limit-working-100 {
		return nil, adjustmentError(ErrLimit, "adjustment.hueSaturation.unknown", 0,
			"ignored hue/saturation metadata exceeds encoding budgets")
	}
	if len(adjustment.Raw) == 0 && !canonicalAdjustmentVersion(adjustment) {
		return nil, adjustmentError(ErrUnsupported, "adjustment.version", 0, "unsupported new adjustment version")
	}
	if adjustment.Kind == ColorBalance && len(adjustment.Raw) != 0 {
		if len(adjustment.Unsupported) != 0 {
			return nil, adjustmentError(ErrUnsupported, "adjustment", adjustment.Span.Offset, adjustment.Unsupported[0])
		}
		if adjustment.Version != 0 || adjustment.ExtraVersion != 0 {
			return nil, adjustmentError(ErrUnsupported, "adjustment.version", 0, "color balance has no version field")
		}
		if err := validateColorBalance(adjustment.ColorBalance); err != nil {
			return nil, err
		}
		original, err := Decode(ctx, key, adjustment.Raw, DecodeOptions{MaxBytes: limit})
		if err != nil {
			return nil, err
		}
		if sameAdjustment(adjustment, *original) {
			data := bytes.Clone(adjustment.Raw)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return data, nil
		}
	}
	// Encoding validates Hue/Saturation parameters independently of the pixel approximation policy.
	compileOptions := CompileOptions{ApproximateHueSaturation: adjustment.Kind == HueSaturation}
	if adjustment.Kind == PhotoFilter {
		if len(adjustment.Unsupported) != 0 {
			return nil, adjustmentError(ErrUnsupported, "adjustment", adjustment.Span.Offset, adjustment.Unsupported[0])
		}
		if err := validatePhotoFilter(adjustment); err != nil {
			return nil, err
		}
	} else {
		if _, err := Compile(adjustment, compileOptions); err != nil {
			return nil, err
		}
	}
	var original *Adjustment
	if len(adjustment.Raw) != 0 {
		var err error
		original, err = Decode(ctx, key, adjustment.Raw, DecodeOptions{MaxBytes: limit})
		if err != nil {
			return nil, err
		}
		if adjustment.Version != 0 && adjustment.Version != original.Version ||
			adjustment.ExtraVersion != 0 && adjustment.ExtraVersion != original.ExtraVersion {
			return nil, adjustmentError(ErrUnsupported, "adjustment.version", 0, "source encoding version cannot be changed")
		}
		if sameAdjustment(adjustment, *original) {
			data := bytes.Clone(adjustment.Raw)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return data, nil
		}
	}
	var data []byte
	switch adjustment.Kind {
	case PhotoFilter:
		data = bytes.Clone(adjustment.Raw)
		minimum := 17
		if adjustment.Version == 3 {
			minimum = 19
		}
		if len(data) == 0 {
			data = make([]byte, minimum)
			binary.BigEndian.PutUint16(data, adjustment.Version)
		}
		parameters := adjustment.PhotoFilter
		densityOffset := 12
		if adjustment.Version == 2 {
			binary.BigEndian.PutUint16(data[2:], parameters.ColorSpace)
			for index, word := range parameters.Components {
				binary.BigEndian.PutUint16(data[4+index*2:], word)
			}
		} else {
			densityOffset = 14
			for index, word := range parameters.XYZWords {
				binary.BigEndian.PutUint32(data[2+index*4:], word)
			}
		}
		binary.BigEndian.PutUint32(data[densityOffset:], parameters.Density)
		data[densityOffset+4] = 0
		if parameters.PreserveLuminosity {
			data[densityOffset+4] = 1
		}
	case HueSaturation:
		var err error
		data, err = encodeHueSaturation(adjustment, original)
		if err != nil {
			return nil, err
		}
	case Invert:
		data = bytes.Clone(adjustment.Raw)
	case Threshold, Posterize:
		data = bytes.Clone(adjustment.Raw)
		if len(data) == 0 {
			data = make([]byte, 2)
		}
		value := adjustment.Threshold
		if adjustment.Kind == Posterize {
			value = adjustment.Posterize
		}
		binary.BigEndian.PutUint16(data, value)
	case Exposure:
		data = bytes.Clone(adjustment.Raw)
		if len(data) == 0 {
			data = make([]byte, 14)
			binary.BigEndian.PutUint16(data, 1)
		}
		parameters := adjustment.Exposure
		for index, value := range []float64{parameters.Exposure, parameters.Offset, parameters.Gamma} {
			binary.BigEndian.PutUint32(data[2+index*4:], math.Float32bits(float32(value)))
		}
	case BrightnessContrast:
		data = bytes.Clone(adjustment.Raw)
		if len(data) == 0 {
			data = make([]byte, 8)
		}
		parameters := adjustment.BrightnessContrast
		binary.BigEndian.PutUint16(data, uint16(parameters.Brightness))
		binary.BigEndian.PutUint16(data[2:], uint16(parameters.Contrast))
		binary.BigEndian.PutUint16(data[4:], parameters.Mean)
		data[6] = 0
		if parameters.LabOnly {
			data[6] = 1
		}
	case ColorBalance:
		data = bytes.Clone(adjustment.Raw)
		if len(data) == 0 {
			data = make([]byte, 20)
		}
		parameters := adjustment.ColorBalance
		for tone, offsets := range [][3]int16{parameters.Shadows, parameters.Midtones, parameters.Highlights} {
			for component, offset := range offsets {
				binary.BigEndian.PutUint16(data[(tone*3+component)*2:], uint16(offset))
			}
		}
		preserve := byte(0)
		if parameters.PreserveLuminosity {
			preserve = 1
		}
		data[18] = preserve
	case ChannelMixer:
		data = bytes.Clone(adjustment.Raw)
		if len(data) == 0 {
			data = make([]byte, 44)
			binary.BigEndian.PutUint16(data, 1)
		}
		parameters := adjustment.ChannelMixer
		monochrome := uint16(0)
		if parameters.Monochrome {
			monochrome = 1
		}
		binary.BigEndian.PutUint16(data[2:], monochrome)
		for output, row := range parameters.Rows {
			for input, value := range row.Inputs {
				binary.BigEndian.PutUint16(data[4+(output*5+input)*2:], uint16(value))
			}
			binary.BigEndian.PutUint16(data[4+(output*5+4)*2:], uint16(row.Constant))
		}
	case Levels:
		var err error
		data, err = encodeLevels(adjustment, original)
		if err != nil {
			return nil, err
		}
	case Curves:
		var err error
		data, err = encodeCurves(adjustment, original)
		if err != nil {
			return nil, err
		}
	}
	if int64(len(data)) > limit-working {
		return nil, adjustmentError(ErrLimit, "adjustment.payload", 0, "encoded payload exceeds its budget")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func canonicalAdjustmentVersion(adjustment Adjustment) bool {
	if adjustment.Kind == PhotoFilter {
		return (adjustment.Version == 2 || adjustment.Version == 3) && adjustment.ExtraVersion == 0
	}
	version := uint16(0)
	switch adjustment.Kind {
	case Levels:
		version = 2
	case HueSaturation:
		version = 2
	case Curves, Exposure, ChannelMixer:
		version = 1
	}
	if adjustment.Version != 0 && adjustment.Version != version {
		return false
	}
	return adjustment.ExtraVersion == 0 || adjustment.Kind == Curves && adjustment.ExtraVersion == 4
}

func adjustmentKey(kind Kind) string {
	switch kind {
	case PhotoFilter:
		return "phfl"
	case Invert:
		return "nvrt"
	case Threshold:
		return "thrs"
	case Posterize:
		return "post"
	case Levels:
		return "levl"
	case Curves:
		return "curv"
	case Exposure:
		return "expA"
	case BrightnessContrast:
		return "brit"
	case ColorBalance:
		return "blnc"
	case ChannelMixer:
		return "mixr"
	case HueSaturation:
		return "hue2"
	}
	return ""
}

func sameAdjustment(first, second Adjustment) bool {
	return first.Kind == second.Kind && first.Threshold == second.Threshold && first.Posterize == second.Posterize &&
		reflect.DeepEqual(first.Levels, second.Levels) && reflect.DeepEqual(first.Curves, second.Curves) &&
		reflect.DeepEqual(first.Exposure, second.Exposure) &&
		reflect.DeepEqual(first.BrightnessContrast, second.BrightnessContrast) &&
		reflect.DeepEqual(first.ColorBalance, second.ColorBalance) &&
		reflect.DeepEqual(first.ChannelMixer, second.ChannelMixer) &&
		reflect.DeepEqual(first.HueSaturation, second.HueSaturation) &&
		reflect.DeepEqual(first.PhotoFilter, second.PhotoFilter)
}

func encodeLevels(adjustment Adjustment, original *Adjustment) ([]byte, error) {
	data := bytes.Clone(adjustment.Raw)
	if len(data) == 0 {
		data = binary.BigEndian.AppendUint16(nil, 2)
		for channel := range 29 {
			level := LevelChannel{Channel: uint16(channel), InputWhite: 255, OutputWhite: 255, Gamma: 1}
			if channel >= 27 {
				level = LevelChannel{Channel: uint16(channel)}
			}
			data = append(data, levelBytes(level)...)
		}
	}
	var channels [4]LevelChannel
	for channel := range channels {
		channels[channel] = LevelChannel{Channel: uint16(channel), InputWhite: 255, OutputWhite: 255, Gamma: 1}
	}
	for _, level := range adjustment.Levels {
		if level.Channel < 4 {
			channels[level.Channel] = level
			continue
		}
		if original != nil {
			for _, previous := range original.Levels {
				if previous.Channel == level.Channel && !reflect.DeepEqual(previous, level) {
					return nil, adjustmentError(ErrUnsupported, "adjustment.levels", 0, "non-RGB source levels cannot be edited")
				}
			}
		}
	}
	for channel, level := range channels {
		if math.Abs(level.Gamma*100-math.Round(level.Gamma*100)) > 1e-7 {
			return nil, adjustmentError(ErrUnsupported, "adjustment.levels.gamma", 0, "wire gamma requires hundredth precision")
		}
		copy(data[2+channel*10:], levelBytes(level))
	}
	return data, nil
}

func levelBytes(level LevelChannel) []byte {
	var data []byte
	for _, value := range []uint16{level.InputBlack, level.InputWhite, level.OutputBlack, level.OutputWhite,
		uint16(math.Round(level.Gamma * 100))} {
		data = binary.BigEndian.AppendUint16(data, value)
	}
	return data
}

func encodeCurves(adjustment Adjustment, original *Adjustment) ([]byte, error) {
	version, extraVersion, mapped := uint16(1), uint16(4), false
	var baseTemplate, extraTemplate []CurveChannel
	if original != nil {
		version, extraVersion, mapped = original.Version, original.ExtraVersion, original.CurveMap
		baseTemplate = original.Curves
		if len(original.LegacyCurves) != 0 {
			baseTemplate = original.LegacyCurves
		}
		extraTemplate = original.Curves
		for _, desired := range adjustment.Curves {
			if desired.Channel < 4 {
				continue
			}
			for _, previous := range original.Curves {
				if desired.Channel == previous.Channel && !reflect.DeepEqual(previous, desired) {
					return nil, adjustmentError(ErrUnsupported, "adjustment.curves", 0,
						"non-RGB source curves cannot be edited")
				}
			}
		}
	} else {
		for _, curve := range adjustment.Curves {
			mapped = mapped || len(curve.LUT) != 0
		}
	}
	base := mergeCurveChannels(adjustment.Curves, baseTemplate, mapped)
	extra := mergeCurveChannels(adjustment.Curves, extraTemplate, mapped)
	for _, curve := range base {
		if mapped && len(curve.LUT) != 256 || !mapped && len(curve.Points) < 2 {
			return nil, adjustmentError(ErrUnsupported, "adjustment.curves", 0,
				"source mapped/point curve representation cannot be changed")
		}
		if curve.Channel >= 32 {
			return nil, adjustmentError(ErrUnsupported, "adjustment.curves.channel", 0, "legacy channel mask cannot encode this index")
		}
	}
	if version == 4 {
		for index, curve := range base {
			if curve.Channel != uint16(index) {
				return nil, adjustmentError(ErrUnsupported, "adjustment.curves.channel", 0,
					"version-four legacy channels must remain contiguous")
			}
		}
	}
	flag := byte(0)
	if mapped {
		flag = 1
	}
	data := binary.BigEndian.AppendUint16([]byte{flag}, version)
	count := uint32(len(base))
	if version == 1 {
		count = 0
		for _, curve := range base {
			count |= 1 << curve.Channel
		}
	}
	data = binary.BigEndian.AppendUint32(data, count)
	for _, curve := range base {
		data = appendCurve(data, curve, mapped, false)
	}
	if extraVersion != 0 {
		data = append(data, "Crv "...)
		data = binary.BigEndian.AppendUint16(data, extraVersion)
		data = binary.BigEndian.AppendUint32(data, uint32(len(extra)))
		for _, curve := range extra {
			data = appendCurve(data, curve, mapped, true)
		}
	}
	if original == nil || len(original.Raw)%4 == 0 {
		data = append(data, make([]byte, (4-len(data)%4)%4)...)
	}
	return data, nil
}

func mergeCurveChannels(desired, template []CurveChannel, mapped bool) []CurveChannel {
	var result []CurveChannel
	seen := make(map[uint16]bool)
	for _, previous := range template {
		curve := previous
		if previous.Channel < 4 {
			if !identityCurve(previous) {
				curve = identityCurveChannel(previous.Channel, mapped)
			}
			for _, next := range desired {
				if next.Channel == previous.Channel {
					curve = next
					break
				}
			}
		}
		result, seen[curve.Channel] = append(result, curve), true
	}
	for _, curve := range desired {
		if !seen[curve.Channel] {
			result = append(result, curve)
			seen[curve.Channel] = true
		}
	}
	slices.SortFunc(result, func(first, second CurveChannel) int { return int(first.Channel) - int(second.Channel) })
	return result
}

func identityCurveChannel(channel uint16, mapped bool) CurveChannel {
	curve := CurveChannel{Channel: channel, Points: []CurvePoint{{Input: 0, Output: 0}, {Input: 255, Output: 255}}}
	if mapped {
		curve.Points, curve.LUT = nil, make([]byte, 256)
		for index := range curve.LUT {
			curve.LUT[index] = byte(index)
		}
	}
	return curve
}

func appendCurve(data []byte, curve CurveChannel, mapped, indexed bool) []byte {
	if indexed {
		data = binary.BigEndian.AppendUint16(data, curve.Channel)
	}
	if mapped {
		return append(data, curve.LUT...)
	}
	data = binary.BigEndian.AppendUint16(data, uint16(len(curve.Points)))
	for _, point := range curve.Points {
		data = binary.BigEndian.AppendUint16(data, point.Output)
		data = binary.BigEndian.AppendUint16(data, point.Input)
	}
	return data
}
