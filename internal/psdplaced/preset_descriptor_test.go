package psdplaced_test

import (
	"bytes"
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/psdplaced"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func presetDescriptor() *psdfile.Descriptor {
	bounds := &psdfile.Descriptor{ClassID: "classFloatRect", Entries: []psdfile.DescriptorEntry{
		{Key: "Left", Type: "doub", Value: float64(0)}, {Key: "Top ", Type: "doub", Value: float64(0)},
		{Key: "Rght", Type: "doub", Value: float64(8)}, {Key: "Btom", Type: "doub", Value: float64(8)},
	}}
	warp := &psdfile.Descriptor{ClassID: "warp", Entries: []psdfile.DescriptorEntry{
		{Key: "warpStyle", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "warpStyle", ValueID: "warpArcLower"}},
		{Key: "warpRotate", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "Ornt", ValueID: "Hrzn"}},
		{Key: "warpValue", Type: "doub", Value: float64(25)},
		{Key: "warpPerspective", Type: "doub", Value: float64(0)},
		{Key: "warpPerspectiveOther", Type: "doub", Value: float64(0)},
		{Key: "uOrder", Type: "long", Value: int32(4)}, {Key: "vOrder", Type: "long", Value: int32(2)},
		{Key: "bounds", Type: "Objc", Value: bounds},
	}}
	size := &psdfile.Descriptor{ClassID: "size", Entries: []psdfile.DescriptorEntry{
		{Key: "Wdth", Type: "doub", Value: float64(8)}, {Key: "Hght", Type: "doub", Value: float64(8)},
	}}
	var transform []psdfile.DescriptorValue
	for _, value := range []float64{-3, 2, 13, 2, 13, 18, -3, 18} {
		transform = append(transform, psdfile.DescriptorValue{Type: "doub", Value: value})
	}
	return &psdfile.Descriptor{ClassID: "null", Entries: []psdfile.DescriptorEntry{
		{Key: "Idnt", Type: "TEXT", Value: "native-source"},
		{Key: "Type", Type: "long", Value: int32(2)},
		{Key: "Sz  ", Type: "Objc", Value: size},
		{Key: "Trnf", Type: "VlLs", Value: transform},
		{Key: "warp", Type: "Objc", Value: warp},
		{Key: "opaque", Type: "doub", Value: math.Float64frombits(0x7ff8000000000042)},
		{Key: "opaque", Type: "doub", Value: math.Copysign(0, -1)},
	}}
}

func presetDescriptorEntry(descriptor *psdfile.Descriptor, key string) *psdfile.DescriptorEntry {
	for index := range descriptor.Entries {
		if descriptor.Entries[index].Key == key {
			return &descriptor.Entries[index]
		}
	}
	panic("missing test descriptor key: " + key)
}

func presetDescriptorChild(descriptor *psdfile.Descriptor, key string) *psdfile.Descriptor {
	return presetDescriptorEntry(descriptor, key).Value.(*psdfile.Descriptor)
}

func TestPresetDescriptorAcceptsOwnedNativeSchemaAndPixelBounds(t *testing.T) {
	for _, kind := range []string{"doub", "UntF", "UnFl"} {
		t.Run(kind, func(t *testing.T) {
			descriptor := presetDescriptor()
			warp := presetDescriptorChild(descriptor, "warp")
			bounds := presetDescriptorChild(warp, "bounds")
			bounds.ClassID = "Rctn"
			presetDescriptorEntry(descriptor, "warp").Type = "GlbO"
			presetDescriptorEntry(descriptor, "Sz  ").Type = "GlbO"
			presetDescriptorEntry(warp, "bounds").Type = "GlbO"
			for index := range bounds.Entries {
				edge := &bounds.Entries[index]
				value := edge.Value.(float64)
				switch kind {
				case "UntF":
					edge.Value = psdfile.DescriptorUnit{Unit: "#Pxl", Width: 64, Number: value}
				case "UnFl":
					edge.Value = psdfile.DescriptorUnit{Unit: "#Pxl", Width: 32, Number: float32(value)}
				}
				edge.Type = kind
			}
			before, err := psdfile.EncodeDescriptor(nil, descriptor, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := psdplaced.ValidatePresetDescriptor(descriptor); err != nil {
				t.Fatalf("certified native schema rejected: %v", err)
			}
			after, err := psdfile.EncodeDescriptor(nil, descriptor, true)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("schema validation mutated native or opaque descriptor bytes: %v", err)
			}
		})
	}
}

func TestPresetDescriptorRejectsRawAmbiguityAndMissingNativeFields(t *testing.T) {
	for _, path := range []string{"placement", "size", "warp", "bounds"} {
		base := presetDescriptor()
		for _, entry := range presetDescriptorAt(base, path).Entries {
			if path == "placement" && entry.Key == "opaque" {
				continue
			}
			for _, operation := range []string{"duplicate", "missing"} {
				t.Run(path+"/"+entry.Key+"/"+operation, func(t *testing.T) {
					descriptor := presetDescriptor()
					target := presetDescriptorAt(descriptor, path)
					if operation == "duplicate" {
						target.Entries = append(target.Entries, *presetDescriptorEntry(target, entry.Key))
					} else {
						target.Entries = slices.DeleteFunc(target.Entries, func(item psdfile.DescriptorEntry) bool {
							return item.Key == entry.Key
						})
					}
					before, err := psdfile.EncodeDescriptor(nil, descriptor, true)
					if err != nil {
						t.Fatal(err)
					}
					if err := psdplaced.ValidatePresetDescriptor(descriptor); !errors.Is(err, psdwarp.ErrUnsupported) {
						t.Fatalf("ambiguous or missing raw field accepted: %v", err)
					}
					after, err := psdfile.EncodeDescriptor(nil, descriptor, true)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatalf("rejection changed original descriptor bytes: %v", err)
					}
				})
			}
		}
	}
}

func presetDescriptorAt(descriptor *psdfile.Descriptor, path string) *psdfile.Descriptor {
	switch path {
	case "size":
		return presetDescriptorChild(descriptor, "Sz  ")
	case "warp":
		return presetDescriptorChild(descriptor, "warp")
	case "bounds":
		return presetDescriptorChild(presetDescriptorChild(descriptor, "warp"), "bounds")
	}
	return descriptor
}

func TestPresetDescriptorRejectsUnprovenWireTypesClassesAndScalars(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*psdfile.Descriptor)
	}{
		{"source-type", func(d *psdfile.Descriptor) { presetDescriptorEntry(d, "Type").Value = int32(1) }},
		{"source-id", func(d *psdfile.Descriptor) { presetDescriptorEntry(d, "Idnt").Value = "" }},
		{"size-class", func(d *psdfile.Descriptor) { presetDescriptorAt(d, "size").ClassID = "future" }},
		{"size-kind", func(d *psdfile.Descriptor) {
			entry := presetDescriptorEntry(presetDescriptorAt(d, "size"), "Wdth")
			entry.Type, entry.Value = "long", int32(8)
		}},
		{"size-fraction", func(d *psdfile.Descriptor) {
			presetDescriptorEntry(presetDescriptorAt(d, "size"), "Wdth").Value = 8.5
		}},
		{"size-unknown", func(d *psdfile.Descriptor) {
			size := presetDescriptorAt(d, "size")
			size.Entries = append(size.Entries, psdfile.DescriptorEntry{Key: "future", Type: "doub", Value: float64(0)})
		}},
		{"transform-kind", func(d *psdfile.Descriptor) {
			items := presetDescriptorEntry(d, "Trnf").Value.([]psdfile.DescriptorValue)
			items[0] = psdfile.DescriptorValue{Type: "long", Value: int32(0)}
		}},
		{"warp-class", func(d *psdfile.Descriptor) { presetDescriptorAt(d, "warp").ClassID = "quiltWarp" }},
		{"quilt", func(d *psdfile.Descriptor) {
			entry := *presetDescriptorEntry(d, "warp")
			entry.Key = "quiltWarp"
			d.Entries = append(d.Entries, entry)
		}},
		{"bend-kind", func(d *psdfile.Descriptor) {
			entry := presetDescriptorEntry(presetDescriptorAt(d, "warp"), "warpValue")
			entry.Type, entry.Value = "long", int32(25)
		}},
		{"bend-limit", func(d *psdfile.Descriptor) {
			presetDescriptorEntry(presetDescriptorAt(d, "warp"), "warpValue").Value = float64(100)
		}},
		{"bend-nan", func(d *psdfile.Descriptor) {
			presetDescriptorEntry(presetDescriptorAt(d, "warp"), "warpValue").Value = math.NaN()
		}},
		{"perspective-kind", func(d *psdfile.Descriptor) {
			entry := presetDescriptorEntry(presetDescriptorAt(d, "warp"), "warpPerspective")
			entry.Type, entry.Value = "long", int32(0)
		}},
		{"perspective", func(d *psdfile.Descriptor) {
			presetDescriptorEntry(presetDescriptorAt(d, "warp"), "warpPerspectiveOther").Value = float64(1)
		}},
		{"order-kind", func(d *psdfile.Descriptor) {
			entry := presetDescriptorEntry(presetDescriptorAt(d, "warp"), "uOrder")
			entry.Type, entry.Value = "doub", float64(4)
		}},
		{"order-value", func(d *psdfile.Descriptor) {
			presetDescriptorEntry(presetDescriptorAt(d, "warp"), "vOrder").Value = int32(4)
		}},
		{"bounds-class", func(d *psdfile.Descriptor) { presetDescriptorAt(d, "bounds").ClassID = "future" }},
		{"bounds-kind", func(d *psdfile.Descriptor) {
			entry := presetDescriptorEntry(presetDescriptorAt(d, "bounds"), "Left")
			entry.Type, entry.Value = "long", int32(0)
		}},
		{"bounds-unit", func(d *psdfile.Descriptor) {
			entry := presetDescriptorEntry(presetDescriptorAt(d, "bounds"), "Left")
			entry.Type, entry.Value = "UntF", psdfile.DescriptorUnit{Unit: "#Prc", Width: 64, Number: float64(0)}
		}},
		{"bounds-nan", func(d *psdfile.Descriptor) {
			presetDescriptorEntry(presetDescriptorAt(d, "bounds"), "Left").Value = math.Inf(1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			descriptor := presetDescriptor()
			test.edit(descriptor)
			if err := psdplaced.ValidatePresetDescriptor(descriptor); !errors.Is(err, psdwarp.ErrUnsupported) {
				t.Fatalf("unproven wire schema accepted: %v", err)
			}
		})
	}
	if err := psdplaced.ValidatePresetDescriptor(nil); !errors.Is(err, psdwarp.ErrUnsupported) {
		t.Fatalf("nil raw descriptor accepted: %v", err)
	}
}
