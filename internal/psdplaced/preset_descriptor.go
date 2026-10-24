package psdplaced

import (
	"math"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

// ValidatePresetDescriptor checks the original, unambiguous native Arc Lower descriptor schema.
// It reads no source bytes and does not replace the geometry compiler's placement and nonfolding certificates.
// Unrelated top-level fields remain opaque, including repeated fields that do not identify source or geometry.
func ValidatePresetDescriptor(descriptor *psdfile.Descriptor) error {
	if descriptor == nil {
		return psdwarp.ErrUnsupported
	}
	var seen uint16
	for _, entry := range descriptor.Entries {
		var flag uint16
		switch entry.Key {
		case "Idnt":
			flag = 1
			value, valid := entry.Value.(string)
			if entry.Type != "TEXT" || !valid || value == "" {
				return psdwarp.ErrUnsupported
			}
		case "Type":
			flag = 2
			value, valid := entry.Value.(int32)
			if entry.Type != "long" || !valid || value != 2 {
				return psdwarp.ErrUnsupported
			}
		case "Sz  ":
			flag = 4
			if err := validatePresetSize(entry); err != nil {
				return err
			}
		case "Trnf", "nonAffineTransform":
			flag = 8
			if entry.Key == "nonAffineTransform" {
				flag = 16
			}
			if err := validatePresetTransform(entry); err != nil {
				return err
			}
		case "warp":
			flag = 32
			if err := validatePresetWarp(entry); err != nil {
				return err
			}
		case "quiltWarp":
			return psdwarp.ErrUnsupported
		case "placed":
			flag = 64
			if _, valid := entry.Value.(string); entry.Type != "TEXT" || !valid {
				return psdwarp.ErrUnsupported
			}
		case "filterFX":
			flag = 128
		default:
			continue
		}
		if seen&flag != 0 {
			return psdwarp.ErrUnsupported
		}
		seen |= flag
	}
	if seen&47 != 47 {
		return psdwarp.ErrUnsupported
	}
	return nil
}

func presetDescriptorObject(entry psdfile.DescriptorEntry) (*psdfile.Descriptor, bool) {
	value, valid := entry.Value.(*psdfile.Descriptor)
	return value, valid && value != nil && (entry.Type == "Objc" || entry.Type == "GlbO")
}

func validatePresetSize(entry psdfile.DescriptorEntry) error {
	size, valid := presetDescriptorObject(entry)
	if !valid || size.ClassID != "size" {
		return psdwarp.ErrUnsupported
	}
	var seen uint8
	for _, axis := range size.Entries {
		var flag uint8
		switch axis.Key {
		case "Wdth":
			flag = 1
		case "Hght":
			flag = 2
		default:
			return psdwarp.ErrUnsupported
		}
		value, numeric := axis.Value.(float64)
		if seen&flag != 0 || axis.Type != "doub" || !numeric ||
			!finitePresetScalar(value) || value <= 0 || math.Trunc(value) != value {
			return psdwarp.ErrUnsupported
		}
		seen |= flag
	}
	if seen != 3 {
		return psdwarp.ErrUnsupported
	}
	return nil
}

func validatePresetTransform(entry psdfile.DescriptorEntry) error {
	values, valid := entry.Value.([]psdfile.DescriptorValue)
	if entry.Type != "VlLs" || !valid || len(values) != 8 {
		return psdwarp.ErrUnsupported
	}
	for _, item := range values {
		value, numeric := item.Value.(float64)
		if item.Type != "doub" || !numeric || !finitePresetScalar(value) {
			return psdwarp.ErrUnsupported
		}
	}
	return nil
}

func validatePresetWarp(entry psdfile.DescriptorEntry) error {
	warp, valid := presetDescriptorObject(entry)
	if !valid || warp.ClassID != "warp" {
		return psdwarp.ErrUnsupported
	}
	var seen uint8
	for _, property := range warp.Entries {
		var flag uint8
		switch property.Key {
		case "warpStyle", "warpRotate":
			flag = 1
			expected := psdfile.DescriptorEnum{TypeID: "warpStyle", ValueID: "warpArcLower"}
			if property.Key == "warpRotate" {
				flag = 2
				expected = psdfile.DescriptorEnum{TypeID: "Ornt", ValueID: "Hrzn"}
			}
			value, enumeration := property.Value.(psdfile.DescriptorEnum)
			if property.Type != "enum" || !enumeration ||
				value.TypeID != expected.TypeID || value.ValueID != expected.ValueID {
				return psdwarp.ErrUnsupported
			}
		case "warpValue", "warpPerspective", "warpPerspectiveOther":
			flag = 4
			switch property.Key {
			case "warpPerspective":
				flag = 8
			case "warpPerspectiveOther":
				flag = 16
			}
			value, numeric := property.Value.(float64)
			if property.Type != "doub" || !numeric || !finitePresetScalar(value) ||
				property.Key == "warpValue" && math.Abs(value) >= 100 ||
				property.Key != "warpValue" && value != 0 {
				return psdwarp.ErrUnsupported
			}
		case "uOrder", "vOrder":
			flag = 32
			expected := int32(4)
			if property.Key == "vOrder" {
				flag, expected = 64, 2
			}
			value, numeric := property.Value.(int32)
			if property.Type != "long" || !numeric || value != expected {
				return psdwarp.ErrUnsupported
			}
		case "bounds":
			flag = 128
			if err := validatePresetBounds(property); err != nil {
				return err
			}
		default:
			return psdwarp.ErrUnsupported
		}
		if seen&flag != 0 {
			return psdwarp.ErrUnsupported
		}
		seen |= flag
	}
	if seen != 255 {
		return psdwarp.ErrUnsupported
	}
	return nil
}

func validatePresetBounds(entry psdfile.DescriptorEntry) error {
	bounds, valid := presetDescriptorObject(entry)
	if !valid || bounds.ClassID != "classFloatRect" && bounds.ClassID != "Rctn" {
		return psdwarp.ErrUnsupported
	}
	var seen uint8
	for _, edge := range bounds.Entries {
		var flag uint8
		switch edge.Key {
		case "Left":
			flag = 1
		case "Top ":
			flag = 2
		case "Rght":
			flag = 4
		case "Btom":
			flag = 8
		default:
			return psdwarp.ErrUnsupported
		}
		if seen&flag != 0 || !validPresetBound(edge) {
			return psdwarp.ErrUnsupported
		}
		seen |= flag
	}
	if seen != 15 {
		return psdwarp.ErrUnsupported
	}
	return nil
}

func validPresetBound(entry psdfile.DescriptorEntry) bool {
	if value, valid := entry.Value.(float64); entry.Type == "doub" {
		return valid && finitePresetScalar(value)
	}
	unit, valid := entry.Value.(psdfile.DescriptorUnit)
	if !valid || unit.Unit != "#Pxl" {
		return false
	}
	switch entry.Type {
	case "UntF":
		value, numeric := unit.Number.(float64)
		return unit.Width == 64 && numeric && finitePresetScalar(value)
	case "UnFl":
		value, numeric := unit.Number.(float32)
		return unit.Width == 32 && numeric && finitePresetScalar(float64(value))
	}
	return false
}

func finitePresetScalar(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
