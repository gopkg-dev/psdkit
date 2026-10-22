package psdparse

import (
	"bytes"
	"math"
	"slices"
	"strings"
	"testing"
)

func reviewSliceEntry(axis string, coordinates []float64) semanticsDescriptorEntry {
	return semanticsDescriptorEntry{key: axis, kind: "ObAr", data: uniformObjectArrayWire("UntF",
		uint32(len(coordinates)), objectArrayItem{Kind: axis, Type: "UnFl", Unit: "#Pxl", Values: coordinates})}
}

func reviewQuiltWire(envelopeEntries []semanticsDescriptorEntry, outerEntries ...semanticsDescriptorEntry) []byte {
	entries := []semanticsDescriptorEntry{
		{key: "warpStyle", kind: "enum", data: warpEnumWire("warpStyle", "warpCustom")},
		{key: "warpRotate", kind: "enum", data: warpEnumWire("Ornt", "Hrzn")},
		{key: "uOrder", kind: "long", data: uniformLongWire(4)},
		{key: "vOrder", kind: "long", data: uniformLongWire(4)},
		{key: "deformNumRows", kind: "long", data: uniformLongWire(10)},
		{key: "deformNumCols", kind: "long", data: uniformLongWire(10)},
		{key: "customEnvelopeWarp", kind: "Objc", data: uniformDescriptorWire("customEnvelopeWarp", envelopeEntries...)},
	}
	for _, entry := range outerEntries {
		switch entry.key {
		case "uOrder":
			entries[2] = entry
			continue
		case "vOrder":
			entries[3] = entry
			continue
		case "deformNumRows":
			entries[4] = entry
			continue
		}
		entries = append(entries, entry)
	}
	return uniformDescriptorWire("quiltWarp", entries...)
}

func TestQuiltReviewWireDuplicatesRetainDistinctSourceValues(t *testing.T) {
	first := []float64{-0.6, 30, 60, 90.6}
	second := []float64{-0.6, 31, 60, 90.6}
	third := []float64{-0.6, 32, 60, 90.6}
	data := reviewQuiltWire([]semanticsDescriptorEntry{
		reviewSliceEntry("quiltSliceX", first),
		reviewSliceEntry("quiltSliceY", []float64{-0.6, 26, 52, 78.6}),
		reviewSliceEntry("quiltSliceX", second),
	}, reviewSliceEntry("quiltSliceX", third))
	before := bytes.Clone(data)
	warp := decodeUniformWarpWire(t, data)
	if len(warp.QuiltSlices) != 4 || !bytes.Equal(data, before) {
		t.Fatalf("duplicate object arrays or source bytes changed: %#v", warp.QuiltSlices)
	}
	if warp.QuiltSlices[0].Location != "warp" || !slices.Equal(warp.QuiltSlices[0].Values, third) ||
		warp.QuiltSlices[1].Location != "envelope" || !slices.Equal(warp.QuiltSlices[1].Values, first) ||
		warp.QuiltSlices[2].Axis != "quiltSliceY" ||
		warp.QuiltSlices[3].Location != "envelope" || !slices.Equal(warp.QuiltSlices[3].Values, second) {
		t.Fatal("the last map value replaced earlier repeated native coordinates or their location")
	}
	if !strings.Contains(strings.Join(warp.Unsupported, "\n"), "duplicate warp envelope property") {
		t.Fatal("repeated native quilt property was not marked unsupported")
	}
	warp.QuiltSlices[1].Values[1] = 999
	if warp.QuiltSlices[0].Values[1] != 32 || warp.QuiltSlices[3].Values[1] != 31 {
		t.Fatal("distinct native columns share one coordinate backing array")
	}
}

func TestQuiltReviewTypedWireCannotBecomeUnqualifiedGeometry(t *testing.T) {
	for _, test := range []struct {
		name   string
		entry  semanticsDescriptorEntry
		column bool
	}{
		{name: "doubleCubicUOrder", entry: semanticsDescriptorEntry{
			key: "uOrder", kind: "doub", data: uniformDoubleWire(4),
		}},
		{name: "doubleCubicVOrder", entry: semanticsDescriptorEntry{
			key: "vOrder", kind: "doub", data: uniformDoubleWire(4),
		}},
		{name: "doubleCount", entry: semanticsDescriptorEntry{
			key: "deformNumRows", kind: "doub", data: uniformDoubleWire(10),
		}},
		{name: "booleanCount", entry: semanticsDescriptorEntry{key: "deformNumRows", kind: "bool", data: []byte{1}}},
		{name: "fractionalCount", entry: semanticsDescriptorEntry{
			key: "deformNumRows", kind: "doub", data: uniformDoubleWire(10.25),
		}},
		{name: "doubleColumn", column: true, entry: semanticsDescriptorEntry{
			key: "quiltSliceX", kind: "ObAr", data: uniformObjectArrayWire("UntF", 4,
				objectArrayItem{Kind: "quiltSliceX", Type: "UntF", Unit: "#Pxl", Values: []float64{0, 30, 60, 90}}),
		}},
		{name: "unknownUnits", column: true, entry: semanticsDescriptorEntry{
			key: "quiltSliceX", kind: "ObAr", data: uniformObjectArrayWire("UntF", 4,
				objectArrayItem{Kind: "quiltSliceX", Type: "UnFl", Unit: "#Prc", Values: []float64{0, 30, 60, 90}}),
		}},
		{name: "prefixMismatch", column: true, entry: semanticsDescriptorEntry{
			key: "quiltSliceX", kind: "ObAr", data: uniformObjectArrayWire("UntF", 10,
				objectArrayItem{Kind: "quiltSliceX", Type: "UnFl", Unit: "#Pxl", Values: []float64{0, 30, 60, 90}}),
		}},
		{name: "nonfiniteColumn", column: true, entry: semanticsDescriptorEntry{
			key: "quiltSliceX", kind: "ObAr", data: uniformObjectArrayWire("UntF", 4,
				objectArrayItem{Kind: "quiltSliceX", Type: "UnFl", Unit: "#Pxl", Values: []float64{0, math.NaN(), 60, 90}}),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			envelope := []semanticsDescriptorEntry{reviewSliceEntry("quiltSliceX", []float64{-0.6, 30, 60, 90.6}),
				reviewSliceEntry("quiltSliceY", []float64{-0.6, 26, 52, 78.6})}
			var data []byte
			if test.column {
				envelope[0] = test.entry
				data = reviewQuiltWire(envelope)
			} else {
				data = reviewQuiltWire(envelope, test.entry)
			}
			warp := decodeUniformWarpWire(t, data)
			if len(warp.Unsupported) == 0 || !warp.HasDeformRows || !warp.HasDeformColumns {
				t.Fatal("native type/presence failure was silently normalized to supported geometry")
			}
			if test.column && len(warp.QuiltSlices) != 2 {
				t.Fatal("unsupported numeric type erased the source object-array column")
			}
			if test.name == "doubleColumn" && warp.QuiltSlices[0].Kind != "UntF" ||
				test.name == "unknownUnits" && warp.QuiltSlices[0].Unit != "#Prc" ||
				test.name == "prefixMismatch" && warp.QuiltSlices[0].Prefix != 10 ||
				test.name == "nonfiniteColumn" && !math.IsNaN(warp.QuiltSlices[0].Values[1]) {
				t.Fatal("an unsupported original type, unit, prefix or coordinate was overwritten")
			}
		})
	}
}
