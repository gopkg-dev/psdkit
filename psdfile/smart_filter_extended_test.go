package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdfilter"
)

func extendedFilterUnit(key, unit string, value float64) DescriptorEntry {
	return DescriptorEntry{Key: key, Type: "UntF",
		Value: DescriptorUnit{Unit: unit, Width: 64, Number: value}}
}

func extendedFilterLong(key string, value int32) DescriptorEntry {
	return DescriptorEntry{Key: key, Type: "long", Value: value}
}

func extendedFilterBoolean(key string, value bool) DescriptorEntry {
	return DescriptorEntry{Key: key, Type: "bool", Value: value}
}

func extendedFilterParameters() []*Descriptor {
	return []*Descriptor{
		{ClassID: "surfaceBlur", Entries: []DescriptorEntry{
			extendedFilterUnit("Rds ", "#Pxl", 3), extendedFilterLong("Thsh", 5)}},
		{ClassID: "smartSharpen", Entries: []DescriptorEntry{
			{Key: "presetKind", Type: "enum",
				Value: DescriptorEnum{TypeID: "presetKindType", ValueID: "presetKindCustom"}},
			extendedFilterBoolean("useLegacy", false), extendedFilterUnit("Amnt", "#Prc", 376),
			extendedFilterUnit("Rds ", "#Pxl", 1.9), extendedFilterUnit("noiseReduction", "#Prc", 22),
			{Key: "blur", Type: "enum", Value: DescriptorEnum{TypeID: "blurType", ValueID: "lensBlur"}}}},
		{ClassID: "BrgC", Entries: []DescriptorEntry{
			extendedFilterLong("Brgh", -35), extendedFilterLong("Cntr", 34),
			extendedFilterBoolean("useLegacy", false)}},
		{ClassID: "HStr", Entries: []DescriptorEntry{
			extendedFilterBoolean("Clrz", false),
			{Key: "Adjs", Type: "VlLs", Value: []DescriptorValue{
				{Type: "Objc", Value: extendedHueRange(5, [4]int32{195, 225, 255, 285}, -100)},
				{Type: "Objc", Value: extendedHueRange(2, [4]int32{15, 45, 75, 105}, -54)},
			}},
			{Key: "presetKind", Type: "enum",
				Value: DescriptorEnum{TypeID: "presetKindType", ValueID: "presetKindCustom"}}}},
	}
}

func extendedHueRange(index int32, bounds [4]int32, saturation int32) *Descriptor {
	result := &Descriptor{ClassID: "Hst2", Entries: []DescriptorEntry{
		extendedFilterLong("LclR", index), extendedFilterLong("H   ", 0),
		extendedFilterLong("Strt", saturation), extendedFilterLong("Lght", 0)}}
	if index != 0 {
		for position, key := range [4]string{"BgnR", "BgnS", "EndS", "EndR"} {
			result.Entries = append(result.Entries, extendedFilterLong(key, bounds[position]))
		}
	}
	return result
}

func extendedFilterPlacement(parameters ...*Descriptor) SmartObjectRecord {
	ids := []int32{745, 741, 0x42726743, 0x48537472}
	list := make([]DescriptorValue, 0, len(parameters))
	for index, parameter := range parameters {
		list = append(list, DescriptorValue{Type: "Objc", Value: &Descriptor{ClassID: "filterFX",
			Entries: []DescriptorEntry{extendedFilterLong("filterID", ids[index]),
				extendedFilterBoolean("enab", true),
				{Key: "blendOptions", Type: "Objc", Value: &Descriptor{ClassID: "blendOptions", Entries: []DescriptorEntry{
					extendedFilterUnit("Opct", "#Prc", 100),
					{Key: "Md  ", Type: "enum", Value: DescriptorEnum{TypeID: "BlnM", ValueID: "Nrml"}},
				}}},
				{Key: "Fltr", Type: "Objc", Value: parameter}}}})
	}
	return SmartObjectRecord{Descriptor: &Descriptor{ClassID: "null", Entries: []DescriptorEntry{
		{Key: "filterFX", Type: "Objc", Value: &Descriptor{ClassID: "filterFX", Entries: []DescriptorEntry{
			extendedFilterBoolean("enab", true), {Key: "filterFXList", Type: "VlLs", Value: list},
		}}},
	}}}
}

func TestSmartFilterExtendedRealParameterContract(t *testing.T) {
	placement := extendedFilterPlacement(extendedFilterParameters()...)
	stack, err := DecodeSmartFilters(nil, placement)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []psdfilter.Kind{psdfilter.SurfaceBlur, psdfilter.SmartSharpen,
		psdfilter.BrightnessContrast, psdfilter.HueSaturation}
	for index, record := range stack.Filters {
		if !record.ParametersKnown || record.Operation.Kind != kinds[index] ||
			!record.Operation.Enabled || record.Operation.BlendMode != "Nrml" || record.Operation.Opacity != 1 {
			t.Fatalf("filter %d: %+v", index, record)
		}
	}
	if shadow := stack.Filters[0].Operation; shadow.Radius != 3 || shadow.Threshold != 5 {
		t.Fatalf("surface blur controls: %+v", shadow)
	}
	if sharpen := stack.Filters[1].Operation; sharpen.Radius != 1.9 || sharpen.Amount != 3.76 ||
		sharpen.NoiseReduction != .22 || sharpen.BlurModel != "lens-blur" || sharpen.UseLegacy {
		t.Fatalf("smart sharpen controls: %+v", sharpen)
	}
	brightness := stack.Filters[2].Operation.Adjustment.BrightnessContrast
	if *brightness != (psdadjust.BrightnessContrastParams{Brightness: -35, Contrast: 34, Mean: 128}) {
		t.Fatalf("brightness controls: %+v", brightness)
	}
	hue := stack.Filters[3].Operation.Adjustment.HueSaturation
	if hue.Ranges[4].Values.Saturation != -100 || hue.Ranges[4].Bounds != [4]int16{195, 225, 255, 285} ||
		hue.Ranges[1].Values.Saturation != -54 || hue.Ranges[0].Bounds != [4]int16{315, 345, 15, 45} {
		t.Fatalf("hue ranges or unchanged default bounds differ: %+v", hue)
	}
	if _, err := DecodeSmartFilters(nil, placement, MetadataOptions{MaxBytes: stack.MetadataBytes}); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSmartFilters(nil, placement,
		MetadataOptions{MaxBytes: stack.MetadataBytes - 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("typed adjustment reservation is not enforced: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DecodeSmartFilters(ctx, placement); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestSmartFilterExtendedRejectsInvalidControlsAndRetainsUnknownFields(t *testing.T) {
	cases := []struct {
		name    string
		change  func([]*Descriptor)
		invalid bool
	}{
		{"radius percentage", func(p []*Descriptor) {
			p[0].Entries[0] = extendedFilterUnit("Rds ", "#Prc", 3)
		}, true},
		{"radius NaN", func(p []*Descriptor) {
			p[0].Entries[0] = extendedFilterUnit("Rds ", "#Pxl", math.NaN())
		}, true},
		{"threshold float", func(p []*Descriptor) { p[0].Entries[1].Value = float64(5) }, true},
		{"threshold range", func(p []*Descriptor) { p[0].Entries[1].Value = int32(256) }, true},
		{"sharp amount", func(p []*Descriptor) {
			p[1].Entries[2] = extendedFilterUnit("Amnt", "#Prc", 501)
		}, true},
		{"sharp noise unit", func(p []*Descriptor) {
			p[1].Entries[4] = extendedFilterUnit("noiseReduction", "#Pxl", 22)
		}, true},
		{"sharp blur enum", func(p []*Descriptor) {
			p[1].Entries[5].Value = DescriptorEnum{TypeID: "wrong", ValueID: "lensBlur"}
		}, true},
		{"modern brightness", func(p []*Descriptor) { p[2].Entries[0].Value = int32(-151) }, true},
		{"modern contrast", func(p []*Descriptor) { p[2].Entries[1].Value = int32(-51) }, true},
		{"hue range invalid", func(p []*Descriptor) {
			p[3].Entries[1].Value.([]DescriptorValue)[0].Value.(*Descriptor).Entries[0].Value = int32(7)
		}, true},
		{"hue saturation invalid", func(p []*Descriptor) {
			p[3].Entries[1].Value.([]DescriptorValue)[0].Value.(*Descriptor).Entries[2].Value = int32(-101)
		}, true},
		{"unknown surface control", func(p []*Descriptor) {
			p[0].Entries = append(p[0].Entries, extendedFilterLong("futureControl", 7))
		}, false},
		{"duplicate radius", func(p []*Descriptor) { p[0].Entries = append(p[0].Entries, p[0].Entries[0]) }, false},
		{"legacy sharpen", func(p []*Descriptor) { p[1].Entries[1].Value = true }, false},
		{"motion sharpening", func(p []*Descriptor) {
			p[1].Entries[5].Value = DescriptorEnum{TypeID: "blurType", ValueID: "motionBlur"}
		}, false},
		{"unknown preset", func(p []*Descriptor) {
			p[1].Entries[0].Value = DescriptorEnum{TypeID: "presetKindType", ValueID: "futurePreset"}
		}, false},
		{"unknown hue control", func(p []*Descriptor) {
			local := p[3].Entries[1].Value.([]DescriptorValue)[0].Value.(*Descriptor)
			local.Entries = append(local.Entries, extendedFilterLong("futureHue", 1))
		}, false},
		{"colorize", func(p []*Descriptor) { p[3].Entries[0].Value = true }, false},
	}
	for _, fixture := range cases {
		t.Run(fixture.name, func(t *testing.T) {
			parameters := extendedFilterParameters()
			fixture.change(parameters)
			placement := extendedFilterPlacement(parameters...)
			stack, err := DecodeSmartFilters(nil, placement)
			if fixture.invalid {
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("invalid control was accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			unknown := false
			for index, record := range stack.Filters {
				unknown = unknown || !record.ParametersKnown
				if record.Parameters != parameters[index] {
					t.Fatal("source descriptor was flattened or replaced")
				}
			}
			if !unknown {
				t.Fatal("unimplemented controls were silently treated as known")
			}
		})
	}
}

func extendedEncodedSource(t *testing.T, version uint16) (*Document, AdditionalBlock, *SmartFilterStack) {
	t.Helper()
	placement := extendedFilterPlacement(extendedFilterParameters()...)
	encoded, err := EncodeDescriptor(nil, placement.Descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := append([]byte("soLD"), []byte{0, 0, 0, 4}...)
	payload = append(payload, encoded...)
	payload = append(payload, 0xde, 0xad, 0xbe, 0xef)
	source := smartFixtureParse(t, version, "SoLd", payload)
	block := source.Layers[0].AdditionalInfo[0]
	descriptor, err := source.DecodeDescriptor(Span{Offset: block.Data.Offset + 8,
		Length: block.Data.Length - 8}, true)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := DecodeSmartFilters(nil, SmartObjectRecord{Descriptor: descriptor})
	if err != nil {
		t.Fatal(err)
	}
	return source, block, stack
}

func TestSmartFilterExtendedPSDPSBEncoderPreservesAndEditsKnownControls(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(map[uint16]string{1: "PSD", 2: "PSB"}[version], func(t *testing.T) {
			source, block, view := extendedEncodedSource(t, version)
			before, err := source.ReadSpan(block.Data)
			if err != nil {
				t.Fatal(err)
			}
			stack := psdfilter.Stack{Enabled: view.Enabled, Filters: make([]psdfilter.Filter, len(view.Filters))}
			for index, record := range view.Filters {
				stack.Filters[index] = record.Operation
			}
			unchanged, err := source.EncodeSmartFilterParameters(nil, block, stack)
			if err != nil || !bytes.Equal(before, unchanged) {
				t.Fatalf("unchanged extended controls did not preserve source: %v", err)
			}
			changes := []func(*psdfilter.Stack){
				func(s *psdfilter.Stack) { s.Filters[0].Radius = 4 },
				func(s *psdfilter.Stack) { s.Filters[0].Threshold = 6 },
				func(s *psdfilter.Stack) { s.Filters[1].Amount = .7 },
				func(s *psdfilter.Stack) { s.Filters[1].NoiseReduction = .4 },
				func(s *psdfilter.Stack) { s.Filters[1].UseLegacy = true },
				func(s *psdfilter.Stack) { s.Filters[1].BlurModel = "motion-blur" },
				func(s *psdfilter.Stack) {
					adjustment := *s.Filters[2].Adjustment
					controls := *adjustment.BrightnessContrast
					controls.Brightness++
					adjustment.BrightnessContrast = &controls
					s.Filters[2].Adjustment = &adjustment
				},
				func(s *psdfilter.Stack) {
					adjustment := *s.Filters[3].Adjustment
					controls := *adjustment.HueSaturation
					controls.Ranges[4].Values.Saturation++
					adjustment.HueSaturation = &controls
					s.Filters[3].Adjustment = &adjustment
				},
			}
			for index, change := range changes {
				requested := stack
				requested.Filters = append([]psdfilter.Filter(nil), stack.Filters...)
				change(&requested)
				encoded, err := source.EncodeSmartFilterParameters(nil, block, requested)
				if index == 4 || index == 5 {
					if !errors.Is(err, ErrUnsupported) || encoded != nil {
						t.Fatalf("fixed sharpen mode edit %d was accepted: %v", index, err)
					}
					continue
				}
				if err != nil || bytes.Equal(encoded, before) {
					t.Fatalf("known control edit %d was not encoded: %v", index, err)
				}
			}
			requested := stack
			requested.Filters = append([]psdfilter.Filter(nil), stack.Filters...)
			requested.Filters[1].Enabled, requested.Filters[1].Opacity = false, .5
			changed, err := source.EncodeSmartFilterParameters(nil, block, requested)
			if err != nil {
				t.Fatal(err)
			}
			bridge := &Document{reader: bytes.NewReader(changed), size: int64(len(changed)), limits: source.Limits()}
			descriptor, err := bridge.DecodeDescriptor(Span{Offset: 8, Length: int64(len(changed)) - 8}, true)
			if err != nil {
				t.Fatal(err)
			}
			patched, err := DecodeSmartFilters(nil, SmartObjectRecord{Descriptor: descriptor})
			if err != nil || patched.Filters[1].Operation.Enabled || patched.Filters[1].Operation.Opacity != .5 ||
				patched.Filters[1].Operation.Amount != 3.76 || binary.BigEndian.Uint32(changed[4:8]) != 4 ||
				!bytes.Equal(changed[len(changed)-4:], []byte{0xde, 0xad, 0xbe, 0xef}) {
				t.Fatalf("editable filter flags lost existing controls, prefix or tail: %v", err)
			}
			after, err := source.ReadSpan(block.Data)
			if err != nil || !bytes.Equal(before, after) || !reflect.DeepEqual(stack.Filters[1], view.Filters[1].Operation) {
				t.Fatalf("parameter operations modified caller controls or source bytes: %v", err)
			}
		})
	}
}

func TestSmartFilterExtendedHueMasterDuplicatesAndCircularValidation(t *testing.T) {
	parameters := extendedFilterParameters()
	hue := parameters[3]
	list := hue.Entries[1].Value.([]DescriptorValue)
	master := extendedHueRange(0, [4]int32{}, 27)
	list = append(list, DescriptorValue{Type: "Objc", Value: master})
	hue.Entries[1].Value = list
	stack, err := DecodeSmartFilters(nil, extendedFilterPlacement(parameters...))
	if err != nil || !stack.Filters[3].ParametersKnown ||
		stack.Filters[3].Operation.Adjustment.HueSaturation.Master.Saturation != 27 {
		t.Fatalf("master and local hue controls were not retained: %v", err)
	}
	list = append(list, list[0])
	hue.Entries[1].Value = list
	stack, err = DecodeSmartFilters(nil, extendedFilterPlacement(parameters...))
	if err != nil || stack.Filters[3].ParametersKnown {
		t.Fatalf("duplicate local hue control was silently flattened: %v", err)
	}
	hue.Entries[1].Value = []DescriptorValue{{Type: "Objc",
		Value: extendedHueRange(1, [4]int32{0, 200, 100, 300}, 10)}}
	if _, err := DecodeSmartFilters(nil, extendedFilterPlacement(parameters...)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("out-of-order circular hue range: %v", err)
	}
}

func TestSmartFilterExtendedRejectsDuplicateOuterAndBlendControls(t *testing.T) {
	for _, target := range []string{"filterID", "Fltr", "opacity", "futureBlend"} {
		t.Run(target, func(t *testing.T) {
			placement := extendedFilterPlacement(extendedFilterParameters()...)
			filterStack := placement.Descriptor.Entries[0].Value.(*Descriptor)
			item := filterStack.Entries[1].Value.([]DescriptorValue)[0].Value.(*Descriptor)
			switch target {
			case "filterID":
				item.Entries = append(item.Entries, item.Entries[0])
			case "Fltr":
				item.Entries = append(item.Entries, item.Entries[3])
			case "opacity":
				blending := item.Entries[2].Value.(*Descriptor)
				blending.Entries = append(blending.Entries, blending.Entries[0])
			case "futureBlend":
				blending := item.Entries[2].Value.(*Descriptor)
				blending.Entries = append(blending.Entries, extendedFilterBoolean("futureBlend", true))
			}
			stack, err := DecodeSmartFilters(nil, placement)
			if err != nil || stack.Filters[0].ParametersKnown {
				t.Fatalf("ambiguous filter semantics were silently accepted: %v", err)
			}
		})
	}
}

func TestSmartFilterExtendedMetadataNeverReadsRasterPayloads(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		source, _, _ := extendedEncodedSource(t, version)
		data, err := source.ReadSpan(source.Source)
		if err != nil {
			t.Fatal(err)
		}
		reader := &indexForbiddenReader{data: data}
		guarded, err := Read(reader, int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = guarded.Close() })
		reader.forbidden = append(reader.forbidden, guarded.Composite.Data)
		for _, layer := range guarded.Layers {
			for _, channel := range layer.Channels {
				reader.forbidden = append(reader.forbidden, channel.Data)
			}
		}
		block := guarded.Layers[0].AdditionalInfo[0]
		descriptor, err := guarded.DecodeDescriptor(Span{Offset: block.Data.Offset + 8,
			Length: block.Data.Length - 8}, true)
		if err != nil {
			t.Fatal(err)
		}
		view, err := DecodeSmartFilters(nil, SmartObjectRecord{Descriptor: descriptor})
		if err != nil {
			t.Fatal(err)
		}
		stack := psdfilter.Stack{Enabled: true}
		for _, record := range view.Filters {
			stack.Filters = append(stack.Filters, record.Operation)
		}
		if _, err := guarded.EncodeSmartFilterParameters(nil, block, stack); err != nil {
			t.Fatalf("metadata edit read a raster payload: %v", err)
		}
		if err := guarded.Close(); err != nil {
			t.Fatal(err)
		}
		if view.Filters[3].Operation.Adjustment.HueSaturation.Ranges[4].Values.Saturation != -100 {
			t.Fatal("owned typed color controls were lost after source close")
		}
	}
}
