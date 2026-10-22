package psdparse

import (
	"bytes"
	"math"
	"slices"
	"strings"
	"testing"
)

func warpMetadataFixture() descriptor {
	return descriptor{"warpStyle": "warpStyle.warpArcLower", "warpValue": float64(68.8),
		"warpPerspective": float64(0), "warpPerspectiveOther": float64(0), "warpRotate": "Ornt.Hrzn",
		"uOrder": float64(4), "vOrder": float64(2),
		"bounds": descriptor{"Left": float64(124), "Top ": float64(238), "Rght": float64(879), "Btom": float64(927)}}
}

func TestWarpMetadataRetainsNativePresenceAndUnknownOrientation(t *testing.T) {
	value := warpMetadataFixture()
	warp := readWarp(value)
	if !warp.HasValue || warp.HasValues || warp.StyleEnum != "warpStyle.warpArcLower" ||
		warp.RotateEnum != "Ornt.Hrzn" || warp.Rotate != "horizontal" || len(warp.Unsupported) != 0 {
		t.Fatalf("valid native preset metadata changed: %#v", warp)
	}
	value["warpRotate"] = "Ornt.Diagonal"
	warp = readWarp(value)
	if warp.Rotate != "Diagonal" || warp.RotateEnum != "Ornt.Diagonal" || len(warp.Unsupported) == 0 {
		t.Fatal("unknown orientation was silently changed to vertical")
	}
	value["warpStyle"], value["warpValues"] = "warpStyle.warpCylinder", []any{float64(105), float64(563)}
	value["warpRotate"] = "Ornt.Hrzn"
	delete(value, "warpValue")
	warp = readWarp(value)
	if warp.HasValue || !warp.HasValues || !slices.Equal(warp.Values, []float64{105, 563}) ||
		len(warp.Unsupported) != 0 {
		t.Fatalf("native cylinder scalar-list presence was lost: %#v", warp)
	}
}

func TestWarpMetadataPreservesUnknownUnitsAndRejectsMalformedProperties(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(descriptor)
	}{
		{"missingPresetBend", func(d descriptor) { delete(d, "warpValue") }},
		{"missingPresetOrientation", func(d descriptor) { delete(d, "warpRotate") }},
		{"malformedPresetOrientation", func(d descriptor) { d["warpRotate"] = false }},
		{"malformedEnvelopeBoolean", func(d descriptor) { d["customEnvelopeWarp"] = false }},
		{"malformedEnvelopeNumber", func(d descriptor) { d["customEnvelopeWarp"] = float64(0) }},
		{"malformedBoundsDescriptor", func(d descriptor) { d["bounds"] = false }},
		{"booleanBend", func(d descriptor) { d["warpValue"] = true }},
		{"unknownEnumCategory", func(d descriptor) { d["warpStyle"] = "futureStyle.warpArcLower" }},
		{"unknownDescriptorClass", func(d descriptor) { d["_classID"] = "futureWarp" }},
		{"unknownBoundsClass", func(d descriptor) { desc(d["bounds"])["_classID"] = "futureRect" }},
		{"fractionalOrder", func(d descriptor) { d["uOrder"] = float64(4.5) }},
		{"unknownField", func(d descriptor) { d["futureControlRule"] = false }},
		{"malformedList", func(d descriptor) { d["warpValues"] = false }},
		{"malformedBounds", func(d descriptor) { desc(d["bounds"])["Top "] = false }},
		{"nonfiniteBounds", func(d descriptor) { desc(d["bounds"])["Top "] = math.NaN() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := warpMetadataFixture()
			test.edit(value)
			if warp := readWarp(value); len(warp.Unsupported) == 0 {
				t.Fatal("unsupported native property was reduced to a valid zero/default value")
			}
		})
	}
	value := warpMetadataFixture()
	desc(value["bounds"])["Top "] = unitValue{Unit: "#Prc", Value: 238}
	warp := readWarp(value)
	if warp.Bounds.Pixels || warp.Bounds.Units[1] != "#Prc" {
		t.Fatal("original nonpixel bounds unit was lost")
	}
	value["warpStyle"] = "warpStyle.warpCustom"
	value["customEnvelopeWarp"] = descriptor{"meshPoints": []objectArrayItem{
		{Kind: "Hrzn", Unit: "#Prc", Values: []float64{0, 1}},
		{Kind: "Vrtc", Unit: "#Pxl", Values: []float64{0, 1}},
	}}
	warp = readWarp(value)
	if warp.MeshUnits != [2]string{"#Prc", "#Pxl"} || len(warp.Unsupported) == 0 {
		t.Fatal("nonpixel mesh was admitted after dropping its object-array unit")
	}
}

func TestWarpWireFieldOrderAndDuplicateKindsRemainAuditable(t *testing.T) {
	for _, class := range []string{"warp", "futureWarp"} {
		t.Run(class, func(t *testing.T) {
			checkWarpWireOrder(t, class)
		})
	}
}

func checkWarpWireOrder(t *testing.T, class string) {
	t.Helper()
	writer := &psbWriter{}
	writer.u32(0)
	semanticsClassID(writer, class)
	writer.u32(4)
	for _, item := range []semanticsDescriptorEntry{
		{key: "futureProperty", kind: "bool", data: []byte{1}},
		{key: "warpStyle", kind: "enum", data: warpEnumWire("warpStyle", "warpNone")},
		{key: "warpStyle", kind: "TEXT", data: []byte{0, 0, 0, 0}},
		{key: "warpRotate", kind: "enum", data: warpEnumWire("Ornt", "Hrzn")},
	} {
		semanticsClassID(writer, item.key)
		writer.WriteString(item.kind)
		writer.Write(item.data)
	}
	data := bytes.Clone(writer.Bytes())
	before := bytes.Clone(data)
	cursor := &cursor{source: &source{reader: bytes.NewReader(data), size: int64(len(data))}, end: int64(len(data))}
	warp := readWarp(readDescriptor(cursor, 0))
	if cursor.err != nil || !slices.Equal(warp.Fields,
		[]string{"futureProperty", "warpStyle", "warpStyle", "warpRotate"}) ||
		!slices.Equal(warp.Kinds, []string{"bool", "enum", "TEXT", "enum"}) {
		t.Fatalf("wire order or duplicate value kinds changed: %#v, %v", warp, cursor.err)
	}
	if !bytes.Equal(before, data) || !strings.Contains(strings.Join(warp.Unsupported, "\n"), "duplicate warp property") {
		t.Fatal("duplicate semantics were silently discarded or source bytes changed")
	}
}

func warpEnumWire(category, value string) []byte {
	writer := &psbWriter{}
	semanticsClassID(writer, category)
	semanticsClassID(writer, value)
	return writer.Bytes()
}
