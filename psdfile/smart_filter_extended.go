package psdfile

import (
	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdfilter"
)

func smartFilterExtended(record SmartFilterRecord) (SmartFilterRecord, error) {
	parameters := record.Parameters
	if parameters == nil {
		return record, nil
	}
	switch record.FilterID {
	case 745:
		if parameters.ClassID != "surfaceBlur" {
			return record, nil
		}
		radius, err := smartFilterUnitNumber(parameters, "Rds ", "#Pxl", 0, 1000)
		if err != nil {
			return record, err
		}
		threshold, err := smartFilterInteger(parameters, "Thsh", 0, 255)
		if err != nil {
			return record, err
		}
		record.Operation.Kind, record.Operation.Radius = psdfilter.SurfaceBlur, radius
		record.Operation.Threshold = float64(threshold)
		record.ParametersKnown = smartFilterSchemaKnown(parameters, "Rds ", "UntF", "Thsh", "long")
	case 741:
		if parameters.ClassID != "smartSharpen" {
			return record, nil
		}
		radius, err := smartFilterUnitNumber(parameters, "Rds ", "#Pxl", 0, 1000)
		if err != nil {
			return record, err
		}
		amount, err := smartFilterUnitNumber(parameters, "Amnt", "#Prc", 0, 500)
		if err != nil {
			return record, err
		}
		noise, err := smartFilterUnitNumber(parameters, "noiseReduction", "#Prc", 0, 100)
		if err != nil {
			return record, err
		}
		legacy, err := smartFilterBoolean(parameters, "useLegacy", false)
		if err != nil {
			return record, err
		}
		blur, ok := smartDescriptorValue(parameters, "blur").(DescriptorEnum)
		if !ok || blur.TypeID != "blurType" {
			return record, smartFilterParameterError(parameters, "blur", "invalid sharpening blur enumeration")
		}
		record.Operation.Kind, record.Operation.Radius = psdfilter.SmartSharpen, radius
		record.Operation.Amount, record.Operation.NoiseReduction = amount/100, noise/100
		record.Operation.UseLegacy = legacy
		if blur.ValueID == "lensBlur" {
			record.Operation.BlurModel = "lens-blur"
		}
		record.ParametersKnown = !legacy && blur.ValueID == "lensBlur" &&
			smartFilterPresetKnown(parameters) && smartFilterSchemaKnown(parameters,
			"Rds ", "UntF", "Amnt", "UntF", "noiseReduction", "UntF", "useLegacy", "bool",
			"blur", "enum", "presetKind", "enum")
	case 0x42726743:
		if parameters.ClassID != "BrgC" {
			return record, nil
		}
		legacy, err := smartFilterBoolean(parameters, "useLegacy", false)
		if err != nil {
			return record, err
		}
		brightnessLimit, contrastMinimum := int32(150), int32(-50)
		if legacy {
			brightnessLimit, contrastMinimum = 100, -100
		}
		brightness, err := smartFilterInteger(parameters, "Brgh", -brightnessLimit, brightnessLimit)
		if err != nil {
			return record, err
		}
		contrast, err := smartFilterInteger(parameters, "Cntr", contrastMinimum, 100)
		if err != nil {
			return record, err
		}
		record.Operation.Kind = psdfilter.BrightnessContrast
		record.Operation.Adjustment = &psdadjust.Adjustment{Kind: psdadjust.BrightnessContrast,
			BrightnessContrast: &psdadjust.BrightnessContrastParams{Brightness: int16(brightness),
				Contrast: int16(contrast), Mean: 128, Legacy: legacy}}
		record.ParametersKnown = smartFilterSchemaKnown(parameters,
			"Brgh", "long", "Cntr", "long", "useLegacy", "bool")
	case 0x48537472:
		if parameters.ClassID != "HStr" {
			return record, nil
		}
		return smartFilterHueSaturation(record)
	}
	return record, nil
}

func smartFilterHueSaturation(record SmartFilterRecord) (SmartFilterRecord, error) {
	parameters := record.Parameters
	colorize, err := smartFilterBoolean(parameters, "Clrz", false)
	if err != nil {
		return record, err
	}
	values, ok := smartDescriptorValue(parameters, "Adjs").([]DescriptorValue)
	if !ok || len(values) > 7 {
		return record, smartFilterParameterError(parameters, "Adjs", "invalid hue adjustment list")
	}
	hue := &psdadjust.HueSaturationParams{Colorize: colorize}
	for index := range hue.Ranges {
		for bound, base := range [4]int16{315, 345, 15, 45} {
			hue.Ranges[index].Bounds[bound] = (base + int16(index)*60) % 360
		}
	}
	record.Operation.Kind = psdfilter.HueSaturation
	record.Operation.Adjustment = &psdadjust.Adjustment{Kind: psdadjust.HueSaturation, HueSaturation: hue}
	record.ParametersKnown = !colorize && smartFilterPresetKnown(parameters) &&
		smartFilterSchemaKnown(parameters, "Clrz", "bool", "Adjs", "VlLs", "presetKind", "enum")
	var present [7]bool
	for _, value := range values {
		local, valid := value.Value.(*Descriptor)
		if value.Type != "Objc" || !valid || local == nil {
			return record, smartFilterParameterError(parameters, "Adjs.item", "invalid hue range descriptor")
		}
		if local.ClassID != "Hst2" {
			record.ParametersKnown = false
			continue
		}
		rangeIndex, err := smartFilterInteger(local, "LclR", 0, 6)
		if err != nil {
			return record, err
		}
		if present[rangeIndex] {
			record.ParametersKnown = false
		}
		present[rangeIndex] = true
		hueValue, err := smartFilterInteger(local, "H   ", -180, 180)
		if err != nil {
			return record, err
		}
		saturation, err := smartFilterInteger(local, "Strt", -100, 100)
		if err != nil {
			return record, err
		}
		lightness, err := smartFilterInteger(local, "Lght", -100, 100)
		if err != nil {
			return record, err
		}
		controls := psdadjust.HueSaturationValues{Hue: int16(hueValue),
			Saturation: int16(saturation), Lightness: int16(lightness)}
		if rangeIndex == 0 {
			hue.Master = controls
			record.ParametersKnown = record.ParametersKnown && smartFilterSchemaKnown(local,
				"LclR", "long", "H   ", "long", "Strt", "long", "Lght", "long")
			continue
		}
		selected := &hue.Ranges[rangeIndex-1]
		selected.Values = controls
		for index, key := range [4]string{"BgnR", "BgnS", "EndS", "EndR"} {
			bound, err := smartFilterInteger(local, key, 0, 360)
			if err != nil {
				return record, err
			}
			selected.Bounds[index] = int16(bound)
		}
		total := 0
		for index := 1; index < len(selected.Bounds); index++ {
			total += (int(selected.Bounds[index]-selected.Bounds[index-1]) + 360) % 360
		}
		if total > 360 {
			return record, smartFilterParameterError(local, "range", "hue bounds are out of circular order")
		}
		record.ParametersKnown = record.ParametersKnown && smartFilterSchemaKnown(local,
			"LclR", "long", "H   ", "long", "Strt", "long", "Lght", "long",
			"BgnR", "long", "BgnS", "long", "EndS", "long", "EndR", "long")
	}
	return record, nil
}

func smartFilterSchemaKnown(descriptor *Descriptor, allowed ...string) bool {
	for index, entry := range descriptor.Entries {
		matched := false
		for keyIndex := 0; keyIndex < len(allowed); keyIndex += 2 {
			if entry.Key == allowed[keyIndex] && entry.Type == allowed[keyIndex+1] {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
		for _, previous := range descriptor.Entries[:index] {
			if previous.Key == entry.Key {
				return false
			}
		}
	}
	return true
}

func smartFilterPresetKnown(descriptor *Descriptor) bool {
	value := smartDescriptorValue(descriptor, "presetKind")
	if value == nil {
		return true
	}
	preset, ok := value.(DescriptorEnum)
	return ok && preset.TypeID == "presetKindType" &&
		(preset.ValueID == "presetKindCustom" || preset.ValueID == "presetKindDefault")
}

func smartFilterUnitNumber(descriptor *Descriptor, key, unit string, minimum, maximum float64) (float64, error) {
	value := smartDescriptorValue(descriptor, key)
	typed, ok := value.(DescriptorUnit)
	number, valid := smartDescriptorNumber(value)
	if !ok || !valid || typed.Unit != unit || typed.Width != 64 || !smartFinite(number) ||
		number < minimum || number > maximum {
		return 0, smartFilterParameterError(descriptor, key, "filter control has an invalid unit or range")
	}
	return number, nil
}

func smartFilterInteger(descriptor *Descriptor, key string, minimum, maximum int32) (int32, error) {
	value, ok := smartDescriptorValue(descriptor, key).(int32)
	if !ok || value < minimum || value > maximum {
		return 0, smartFilterParameterError(descriptor, key, "filter control is not an in-range integer")
	}
	return value, nil
}

func smartFilterParameterError(descriptor *Descriptor, key, detail string) error {
	return fileError(ErrMalformed, "smartFilters", "filter."+key, descriptor.Span.Offset, detail)
}
