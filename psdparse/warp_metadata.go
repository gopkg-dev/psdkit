package psdparse

import (
	"math"
	"slices"
	"strings"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func warpKeys(value descriptor) []string {
	if keys, ok := value["_orderedKeys"].([]string); ok {
		return slices.Clone(keys)
	}
	var keys []string
	for key := range value {
		if key != "_classID" && key != "_orderedKeys" && key != "_orderedKinds" && key != "_quiltEntries" {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

func readWarpProperties(value descriptor, warp *psdmodel.Warp) {
	warp.ClassID = text(value["_classID"])
	if warp.ClassID != "" && warp.ClassID != "warp" && warp.ClassID != "quiltWarp" {
		warp.Unsupported = append(warp.Unsupported, "unknown warp descriptor class: "+warp.ClassID)
	}
	warp.StyleEnum, warp.RotateEnum = text(value["warpStyle"]), text(value["warpRotate"])
	_, warp.HasValue = value["warpValue"]
	_, warp.HasValues = value["warpValues"]
	warp.Fields = warpKeys(value)
	if kinds, ok := value["_orderedKinds"].([]string); ok {
		warp.Kinds = slices.Clone(kinds)
	}
	seen := make(map[string]bool, len(warp.Fields))
	for index, key := range warp.Fields {
		if seen[key] {
			warp.Unsupported = append(warp.Unsupported, "duplicate warp property: "+key)
		}
		seen[key] = true
		switch key {
		case "warpStyle", "warpRotate", "warpValue", "warpValues", "warpPerspective", "warpPerspectiveOther",
			"bounds", "uOrder", "vOrder", "customEnvelopeWarp", "deformNumRows", "deformNumCols",
			"quiltSliceX", "quiltSliceY":
		default:
			warp.Unsupported = append(warp.Unsupported, "unknown warp property: "+key)
		}
		if index < len(warp.Kinds) && (key == "warpStyle" || key == "warpRotate") && warp.Kinds[index] != "enum" {
			warp.Unsupported = append(warp.Unsupported, "warp enumeration has the wrong descriptor type: "+key)
		}
	}
	if !strings.HasPrefix(warp.StyleEnum, "warpStyle.") || enumValue(warp.StyleEnum) == "" {
		warp.Unsupported = append(warp.Unsupported, "warp style enumeration is absent or malformed")
	}
	if warp.RotateEnum != "" && warp.RotateEnum != "Ornt.Hrzn" && warp.RotateEnum != "Ornt.Vrtc" {
		warp.Unsupported = append(warp.Unsupported, "unknown warp orientation enumeration: "+warp.RotateEnum)
	}
	if _, exists := value["warpRotate"]; exists && warp.RotateEnum == "" ||
		!exists && warp.Style != "none" && warp.Style != "custom" {
		warp.Unsupported = append(warp.Unsupported, "required preset orientation is absent or malformed")
	}
	if entry, exists := value["bounds"]; exists && desc(entry) == nil {
		warp.Unsupported = append(warp.Unsupported, "warp bounds descriptor is malformed")
	}
	if entry, exists := value["customEnvelopeWarp"]; exists {
		warp.HasEnvelope = true
		if desc(entry) == nil {
			warp.Unsupported = append(warp.Unsupported, "custom warp envelope descriptor is malformed")
		}
	}
	for _, key := range []string{"warpValue", "warpPerspective", "warpPerspectiveOther"} {
		entry, exists := value[key]
		if !exists {
			if warp.Style != "none" && warp.Style != "custom" && (key != "warpValue" || !warp.HasValues) {
				warp.Unsupported = append(warp.Unsupported, "required preset scalar is absent: "+key)
			}
			continue
		}
		if scalar, ok := entry.(float64); !ok || math.IsNaN(scalar) || math.IsInf(scalar, 0) {
			warp.Unsupported = append(warp.Unsupported, "warp scalar is malformed: "+key)
		}
	}
	if entry, exists := value["warpValues"]; exists {
		items, ok := entry.([]any)
		if !ok || len(items) != len(warp.Values) {
			warp.Unsupported = append(warp.Unsupported, "warp scalar list is malformed")
		}
		for _, scalar := range warp.Values {
			if math.IsNaN(scalar) || math.IsInf(scalar, 0) {
				warp.Unsupported = append(warp.Unsupported, "warp scalar list contains a malformed number")
				break
			}
		}
	}
	for _, key := range []string{"uOrder", "vOrder"} {
		if entry, exists := value[key]; exists {
			order, ok := entry.(float64)
			if !ok || math.IsNaN(order) || math.IsInf(order, 0) || order < 0 || order > 100_000 ||
				math.Trunc(order) != order {
				warp.Unsupported = append(warp.Unsupported, "warp order is malformed: "+key)
			}
		}
	}
}

func readWarpBoundsProperties(bounds descriptor, warp *psdmodel.Warp) {
	if class := text(bounds["_classID"]); class != "" && class != "classFloatRect" && class != "Rctn" {
		warp.Bounds.Pixels = false
		warp.Unsupported = append(warp.Unsupported, "unknown warp bounds class: "+class)
	}
	for _, key := range warpKeys(bounds) {
		switch key {
		case "Left", "Top ", "Rght", "Btom":
			if scalar, ok := bounds[key].(float64); ok {
				if math.IsNaN(scalar) || math.IsInf(scalar, 0) {
					warp.Bounds.Pixels = false
					warp.Unsupported = append(warp.Unsupported, "warp bounds edge is nonfinite: "+key)
				}
				continue
			}
			if unit, ok := bounds[key].(unitValue); ok {
				if unit.Unit != "#Pxl" || math.IsNaN(unit.Value) || math.IsInf(unit.Value, 0) {
					warp.Unsupported = append(warp.Unsupported, "warp bounds edge has an unknown unit or value: "+key)
				}
			} else {
				warp.Bounds.Pixels = false
				warp.Unsupported = append(warp.Unsupported, "warp bounds edge is malformed: "+key)
			}
		default:
			warp.Unsupported = append(warp.Unsupported, "unknown warp bounds property: "+key)
		}
	}
}

func readWarpEnvelopeProperties(envelope descriptor, warp *psdmodel.Warp) {
	if class := text(envelope["_classID"]); class != "" && class != "customEnvelopeWarp" {
		warp.Unsupported = append(warp.Unsupported, "unknown warp envelope class: "+class)
	}
	for _, key := range warpKeys(envelope) {
		switch key {
		case "meshPoints":
			if _, ok := envelope[key].([]objectArrayItem); !ok {
				warp.Unsupported = append(warp.Unsupported, "custom warp mesh is malformed")
			}
		case "quiltSliceX", "quiltSliceY":
		default:
			warp.Unsupported = append(warp.Unsupported, "unknown warp envelope property: "+key)
		}
	}
}
