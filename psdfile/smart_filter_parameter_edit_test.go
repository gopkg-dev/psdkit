package psdfile

import (
	"bytes"
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfilter"
)

func filterParameterSource(
	t *testing.T, mutate func(*Descriptor),
) (*Document, AdditionalBlock, *SmartFilterStack) {
	t.Helper()
	parameters := extendedFilterParameters()
	hue := parameters[3]
	for index := range hue.Entries {
		if hue.Entries[index].Key == "Adjs" {
			values := hue.Entries[index].Value.([]DescriptorValue)
			hue.Entries[index].Value = append(values,
				DescriptorValue{Type: "Objc", Value: extendedHueRange(0, [4]int32{}, 0)})
		}
	}
	placement := extendedFilterPlacement(parameters...)
	placement.Descriptor.Entries = append(placement.Descriptor.Entries,
		DescriptorEntry{Key: "opaquePlacement", Type: "tdta", Value: []byte{7, 19, 31}})
	if mutate != nil {
		mutate(placement.Descriptor)
	}
	encoded, err := EncodeDescriptor(t.Context(), placement.Descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := append([]byte{'s', 'o', 'L', 'D', 0, 0, 0, 4}, encoded...)
	payload = append(payload, 0x73, 0x99)
	source := smartFixtureParse(t, 1, "SoLd", payload)
	block := source.Layers[0].AdditionalInfo[0]
	_, view := filterParameterDecode(t, source, block.Data.Offset+8, block.Data.Length-8)
	return source, block, view
}

func filterParameterDecode(t *testing.T, source *Document, offset, length int64) (*Descriptor, *SmartFilterStack) {
	t.Helper()
	descriptor, err := source.DecodeDescriptor(Span{Offset: offset, Length: length}, true)
	if err != nil {
		t.Fatal(err)
	}
	view, err := DecodeSmartFilters(t.Context(), SmartObjectRecord{Descriptor: descriptor})
	if err != nil {
		t.Fatal(err)
	}
	return descriptor, view
}

func filterParameterStack(view *SmartFilterStack) psdfilter.Stack {
	stack := psdfilter.Stack{Enabled: view.Enabled, Filters: make([]psdfilter.Filter, len(view.Filters))}
	for index, record := range view.Filters {
		stack.Filters[index] = record.Operation
		if record.Operation.Adjustment == nil {
			continue
		}
		adjustment := *record.Operation.Adjustment
		if adjustment.BrightnessContrast != nil {
			controls := *adjustment.BrightnessContrast
			adjustment.BrightnessContrast = &controls
		}
		if adjustment.HueSaturation != nil {
			controls := *adjustment.HueSaturation
			adjustment.HueSaturation = &controls
		}
		stack.Filters[index].Adjustment = &adjustment
	}
	return stack
}

func TestSmartFilterParameterEditsRoundTripAndPreserveSource(t *testing.T) {
	source, block, view := filterParameterSource(t, func(descriptor *Descriptor) {
		stack := smartDescriptorValue(descriptor, "filterFX").(*Descriptor)
		items := smartDescriptorValue(stack, "filterFXList").([]DescriptorValue)
		parameters := smartDescriptorValue(items[1].Value.(*Descriptor), "Fltr").(*Descriptor)
		parameters.Entries = append(parameters.Entries,
			DescriptorEntry{Key: "opaqueControl", Type: "tdta", Value: []byte{1, 3, 5}},
			DescriptorEntry{Key: "opaqueControl", Type: "long", Value: int32(9)})
	})
	before, err := source.ReadSpan(block.Data)
	if err != nil {
		t.Fatal(err)
	}
	wanted := filterParameterStack(view)
	wanted.Enabled = false
	wanted.Filters[0].Radius, wanted.Filters[0].Threshold = 7.5, 255
	wanted.Filters[1].Radius, wanted.Filters[1].Amount, wanted.Filters[1].NoiseReduction = .5, 5, .625
	wanted.Filters[1].Enabled, wanted.Filters[1].Opacity = false, .625
	brightness := wanted.Filters[2].Adjustment.BrightnessContrast
	brightness.Brightness, brightness.Contrast, brightness.Legacy = -100, -100, true
	hue := wanted.Filters[3].Adjustment.HueSaturation
	hue.Master.Hue, hue.Master.Saturation, hue.Master.Lightness = 180, 100, -100
	hue.Ranges[4].Values.Hue, hue.Ranges[4].Values.Lightness = -180, 100
	hue.Ranges[4].Bounds = [4]int16{350, 10, 20, 30}
	encoded, err := source.EncodeSmartFilterParameters(t.Context(), block, wanted)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &Document{reader: bytes.NewReader(encoded), size: int64(len(encoded)), limits: source.Limits()}
	descriptor, patched := filterParameterDecode(t, bridge, 8, int64(len(encoded))-8)
	if !reflect.DeepEqual(filterParameterStack(patched), wanted) {
		t.Fatal("decoded edited controls differ from the requested stack")
	}
	if !bytes.Equal(encoded[:8], before[:8]) || !bytes.Equal(encoded[len(encoded)-2:], before[len(before)-2:]) {
		t.Fatal("placement prefix or opaque tail changed")
	}
	placementBlob := smartDescriptorValue(descriptor, "opaquePlacement").(DescriptorBlob)
	opaque, err := bridge.ReadSpan(placementBlob.Data)
	if err != nil || !bytes.Equal(opaque, []byte{7, 19, 31}) {
		t.Fatal("opaque placement changed")
	}
	oldEntries, newEntries := view.Filters[1].Parameters.Entries, patched.Filters[1].Parameters.Entries
	for index := len(oldEntries) - 2; index < len(oldEntries); index++ {
		oldBytes, err := source.ReadSpan(oldEntries[index].Span)
		if err != nil {
			t.Fatal(err)
		}
		newBytes, err := bridge.ReadSpan(newEntries[index].Span)
		if err != nil || !bytes.Equal(oldBytes, newBytes) {
			t.Fatalf("unknown parameter or duplicate order changed: %v", err)
		}
	}
	after, err := source.ReadSpan(block.Data)
	if err != nil || !bytes.Equal(before, after) || view.Filters[0].Operation.Radius != 3 ||
		view.Filters[2].Operation.Adjustment.BrightnessContrast.Legacy {
		t.Fatal("encoding mutated original source or borrowed typed metadata")
	}
}

func TestSmartFilterParameterEditsRejectInvalidOrUnrepresentableControls(t *testing.T) {
	cases := []struct {
		name   string
		change func(*psdfilter.Stack)
		kind   error
	}{
		{"surface fractional threshold", func(s *psdfilter.Stack) { s.Filters[0].Threshold = .5 }, ErrMalformed},
		{"surface threshold overflow", func(s *psdfilter.Stack) { s.Filters[0].Threshold = 256 }, ErrMalformed},
		{"surface threshold NaN", func(s *psdfilter.Stack) { s.Filters[0].Threshold = math.NaN() }, ErrMalformed},
		{"sharpen amount overflow", func(s *psdfilter.Stack) { s.Filters[1].Amount = 5.1 }, ErrMalformed},
		{"sharpen noise NaN", func(s *psdfilter.Stack) { s.Filters[1].NoiseReduction = math.NaN() }, ErrMalformed},
		{"sharpen radius negative", func(s *psdfilter.Stack) { s.Filters[1].Radius = -1 }, ErrMalformed},
		{"fixed sharpen legacy", func(s *psdfilter.Stack) { s.Filters[1].UseLegacy = true }, ErrUnsupported},
		{"brightness modern range", func(s *psdfilter.Stack) {
			s.Filters[2].Adjustment.BrightnessContrast.Contrast = -51
		}, ErrMalformed},
		{"brightness legacy range", func(s *psdfilter.Stack) {
			s.Filters[2].Adjustment.BrightnessContrast.Legacy = true
			s.Filters[2].Adjustment.BrightnessContrast.Brightness = 101
		}, ErrMalformed},
		{"fixed brightness mean", func(s *psdfilter.Stack) {
			s.Filters[2].Adjustment.BrightnessContrast.Mean = 127
		}, ErrUnsupported},
		{"missing brightness controls", func(s *psdfilter.Stack) { s.Filters[2].Adjustment = nil }, ErrMalformed},
		{"hue degrees", func(s *psdfilter.Stack) {
			s.Filters[3].Adjustment.HueSaturation.Master.Hue = 181
		}, ErrMalformed},
		{"hue circular bounds", func(s *psdfilter.Stack) {
			s.Filters[3].Adjustment.HueSaturation.Ranges[4].Bounds = [4]int16{0, 300, 200, 100}
		}, ErrMalformed},
		{"absent hue range", func(s *psdfilter.Stack) {
			s.Filters[3].Adjustment.HueSaturation.Ranges[0].Values.Hue = 20
		}, ErrUnsupported},
		{"fixed hue colorization", func(s *psdfilter.Stack) {
			s.Filters[3].Adjustment.HueSaturation.Colorize = true
		}, ErrUnsupported},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			source, block, view := filterParameterSource(t, nil)
			wanted := filterParameterStack(view)
			test.change(&wanted)
			encoded, err := source.EncodeSmartFilterParameters(t.Context(), block, wanted)
			if !errors.Is(err, test.kind) || encoded != nil {
				t.Fatalf("invalid controls returned data or the wrong error: %v", err)
			}
			if _, err := source.EncodeSmartFilterParameters(t.Context(), block, filterParameterStack(view)); err != nil {
				t.Fatalf("failed edit changed the retained source: %v", err)
			}
		})
	}
}

func TestSmartFilterParameterEditsRejectAmbiguousFields(t *testing.T) {
	for _, location := range []string{"placement", "stack", "filter", "blending", "parameter", "range", "range identity"} {
		t.Run(location, func(t *testing.T) {
			source, block, view := filterParameterSource(t, func(descriptor *Descriptor) {
				stack := smartDescriptorValue(descriptor, "filterFX").(*Descriptor)
				items := smartDescriptorValue(stack, "filterFXList").([]DescriptorValue)
				filter := items[3].Value.(*Descriptor)
				parameters := smartDescriptorValue(filter, "Fltr").(*Descriptor)
				ranges := smartDescriptorValue(parameters, "Adjs").([]DescriptorValue)
				target, key := descriptor, "filterFX"
				switch location {
				case "stack":
					target, key = stack, "filterFXList"
				case "filter":
					target, key = filter, "Fltr"
				case "blending":
					target, key = smartDescriptorValue(filter, "blendOptions").(*Descriptor), "Opct"
				case "parameter":
					target, key = parameters, "Adjs"
				case "range":
					target, key = ranges[0].Value.(*Descriptor), "H   "
				case "range identity":
					for index := range parameters.Entries {
						if parameters.Entries[index].Key == "Adjs" {
							parameters.Entries[index].Value = append(ranges, ranges[0])
							return
						}
					}
				}
				for _, entry := range target.Entries {
					if entry.Key == key {
						target.Entries = append(target.Entries, entry)
						return
					}
				}
			})
			encoded, err := source.EncodeSmartFilterParameters(t.Context(), block, filterParameterStack(view))
			if !errors.Is(err, ErrUnsupported) || encoded != nil {
				t.Fatalf("ambiguous source accepted: %v", err)
			}
		})
	}
}

func TestSmartFilterParameterEditsBudgetAndCancellation(t *testing.T) {
	source, block, view := filterParameterSource(t, nil)
	wanted := filterParameterStack(view)
	wanted.Filters[0].Threshold = 19
	if encoded, err := source.EncodeSmartFilterParameters(t.Context(), block, wanted,
		MetadataOptions{MaxBytes: block.Data.Length * 8}); !errors.Is(err, ErrLimit) || encoded != nil {
		t.Fatalf("metadata working budget ignored: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if encoded, err := source.EncodeSmartFilterParameters(ctx, block, wanted); !errors.Is(err, context.Canceled) ||
		encoded != nil {
		t.Fatalf("cancelled parameter edit returned output: %v", err)
	}
	if _, err := source.EncodeSmartFilterParameters(t.Context(), block, wanted); err != nil {
		t.Fatalf("failed operation mutated the source: %v", err)
	}
}
