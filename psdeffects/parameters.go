package psdeffects

import "math"

func decodeParameter(effect *Effect, entry Entry) error {
	switch entry.Key {
	case "enab", "present", "showInDialog", "uglg", "layerConceals", "AntA":
		value, valid := entry.Value.(bool)
		if !valid {
			return effectError(ErrInvalid, "effects."+entry.Key, "effect flag is not boolean")
		}
		switch entry.Key {
		case "enab":
			effect.Enabled = value
		case "present":
			effect.Present = value
		case "showInDialog":
			effect.Shown = value
		case "uglg":
			effect.UseGlobalLight = value
		case "layerConceals":
			effect.LayerConceals = value
		case "AntA":
			effect.AntiAlias = value
		}
	case "Opct", "blur", "Sz  ", "Ckmt", "Dstn", "lagl", "Nose", "ShdN", "Inpr":
		value, valid := scalar(entry.Value)
		if !valid {
			return effectError(ErrInvalid, "effects."+entry.Key, "effect scalar is not finite")
		}
		if unit, valid := entry.Value.(Unit); valid {
			expected := "#Prc"
			if entry.Key == "blur" || entry.Key == "Sz  " || entry.Key == "Ckmt" || entry.Key == "Dstn" {
				expected = "#Pxl"
			}
			if entry.Key == "lagl" {
				expected = "#Ang"
			}
			if unit.Unit != expected {
				effect.Unsupported = append(effect.Unsupported, "unrecognized unit for "+entry.Key)
			}
		}
		switch entry.Key {
		case "Opct":
			effect.Opacity = value / 100
		case "blur", "Sz  ":
			effect.Size = value
		case "Ckmt":
			effect.Spread = value / 100
		case "Dstn":
			effect.Distance = value
		case "lagl":
			effect.Angle = value
		case "Nose":
			effect.Noise = value / 100
		case "ShdN":
			effect.Jitter = value / 100
		case "Inpr":
			effect.Range = value / 100
		}
	case "Clr ":
		color, valid := entry.Value.(*Descriptor)
		if !valid || color == nil || color.ClassID != "RGBC" {
			effect.Unsupported = append(effect.Unsupported, "effect color is not RGB")
			return nil
		}
		seen := [3]bool{}
		for _, component := range color.Entries {
			matched := false
			for index, key := range []string{"Rd  ", "Grn ", "Bl  "} {
				if component.Key != key {
					continue
				}
				matched = true
				value, valid := scalar(component.Value)
				if !valid {
					return effectError(ErrInvalid, "effects.color", "RGB effect component is not finite")
				}
				if seen[index] {
					effect.Unsupported = append(effect.Unsupported, "duplicate RGB effect component")
				}
				effect.Color[index], seen[index] = value/255, true
			}
			if !matched {
				effect.Unsupported = append(effect.Unsupported, "unknown RGB color parameter "+component.Key)
			}
		}
		if !seen[0] || !seen[1] || !seen[2] {
			effect.Unsupported = append(effect.Unsupported, "incomplete RGB effect color")
		}
	case "Md  ", "GlwT", "glwS", "Styl", "PntT":
		enumeration, valid := entry.Value.(Enum)
		if !valid {
			return effectError(ErrInvalid, "effects."+entry.Key, "effect enumeration is malformed")
		}
		expectedClass := map[string]string{
			"Md  ": "BlnM", "GlwT": "BETE", "glwS": "IGSr", "Styl": "FStl", "PntT": "FrFl",
		}[entry.Key]
		if enumeration.TypeID != expectedClass {
			effect.Unsupported = append(effect.Unsupported, "unrecognized enumeration class for "+entry.Key)
		}
		switch entry.Key {
		case "Md  ":
			effect.BlendMode = blendMode(enumeration.ValueID)
		case "GlwT":
			effect.Technique = enumeration.ValueID
		case "glwS":
			switch enumeration.ValueID {
			case "SrcE", "edgeGlow":
				effect.GlowSource = Edge
			case "SrcC", "centerGlow":
				effect.GlowSource = CenterSource
			default:
				effect.Unsupported = append(effect.Unsupported, "unknown inner-glow source")
			}
		case "Styl":
			switch enumeration.ValueID {
			case "OutF":
				effect.Position = Outside
			case "InsF":
				effect.Position = Inside
			case "CtrF":
				effect.Position = Center
			default:
				effect.Unsupported = append(effect.Unsupported, "unknown stroke position")
			}
		case "PntT":
			if enumeration.ValueID != "SClr" {
				effect.Unsupported = append(effect.Unsupported, "gradient or pattern effect fill")
			}
		}
	case "TrnS":
		contour, valid := entry.Value.(*Descriptor)
		if !valid || !linearContour(contour) {
			effect.Unsupported = append(effect.Unsupported, "nonlinear or unrecognized effect contour")
		}
	case "overprint":
		value, valid := entry.Value.(bool)
		if !valid || value {
			effect.Unsupported = append(effect.Unsupported, "effect stroke overprint")
		}
	default:
		effect.Unsupported = append(effect.Unsupported, "unrecognized effect parameter "+entry.Key)
	}
	return nil
}

func linearContour(descriptor *Descriptor) bool {
	if descriptor == nil {
		return false
	}
	var points []Value
	for _, entry := range descriptor.Entries {
		switch entry.Key {
		case "Nm  ":
			if _, valid := entry.Value.(string); !valid {
				return false
			}
		case "Crv ":
			if points != nil {
				return false
			}
			var valid bool
			points, valid = entry.Value.([]Value)
			if !valid {
				return false
			}
		default:
			return false
		}
	}
	if len(points) != 2 {
		return false
	}
	for index, value := range points {
		point, valid := value.Value.(*Descriptor)
		if !valid || point == nil || len(point.Entries) != 2 {
			return false
		}
		seen := [2]bool{}
		for _, coordinate := range point.Entries {
			axis := -1
			if coordinate.Key == "Hrzn" {
				axis = 0
			}
			if coordinate.Key == "Vrtc" {
				axis = 1
			}
			number, valid := scalar(coordinate.Value)
			if axis < 0 || !valid || seen[axis] || math.Abs(number-float64(index)*255) > 1e-9 {
				return false
			}
			seen[axis] = true
		}
	}
	return true
}

func blendMode(identifier string) string {
	switch identifier {
	case "Nrml":
		return "norm"
	case "Mltp":
		return "mul "
	case "Scrn":
		return "scrn"
	case "Ovrl":
		return "over"
	case "HrdL":
		return "hLit"
	case "SftL":
		return "sLit"
	case "Dfrn":
		return "diff"
	case "CBrn":
		return "idiv"
	case "CDdg":
		return "div "
	case "darkerColor":
		return "dkCl"
	case "lighterColor":
		return "lgCl"
	case "vividLight":
		return "vLit"
	case "linearLight":
		return "lLit"
	case "pinLight":
		return "pLit"
	case "hardMix":
		return "hMix"
	case "Xclu":
		return "smud"
	case "H   ":
		return "hue "
	case "Strt":
		return "sat "
	case "Clr ":
		return "colr"
	case "Lmns":
		return "lum "
	case "blendSubtraction":
		return "fsub"
	case "blendDivide":
		return "fdiv"
	case "Drkn":
		return "dark"
	case "Lghn":
		return "lite"
	case "linearDodge":
		return "lddg"
	case "linearBurn":
		return "lbrn"
	default:
		return identifier
	}
}
