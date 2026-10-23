package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
)

func filterEditUnit(key, unit string, value float64) psdfile.DescriptorEntry {
	return psdfile.DescriptorEntry{Key: key, Type: "UntF",
		Value: psdfile.DescriptorUnit{Unit: unit, Width: 64, Number: value}}
}

func filterEditLong(key string, value int32) psdfile.DescriptorEntry {
	return psdfile.DescriptorEntry{Key: key, Type: "long", Value: value}
}

func filterEditBool(key string, value bool) psdfile.DescriptorEntry {
	return psdfile.DescriptorEntry{Key: key, Type: "bool", Value: value}
}

func filterEditHueRange(index int32, bounds [4]int32) psdfile.DescriptorValue {
	rangeValue := &psdfile.Descriptor{ClassID: "Hst2", Entries: []psdfile.DescriptorEntry{
		filterEditLong("LclR", index), filterEditLong("H   ", 0),
		filterEditLong("Strt", 0), filterEditLong("Lght", 0),
	}}
	if index != 0 {
		for position, key := range []string{"BgnR", "BgnS", "EndS", "EndR"} {
			rangeValue.Entries = append(rangeValue.Entries, filterEditLong(key, bounds[position]))
		}
	}
	return psdfile.DescriptorValue{Type: "Objc", Value: rangeValue}
}

func filterEditStackDescriptor() *psdfile.Descriptor {
	parameters := []*psdfile.Descriptor{
		{ClassID: "GsnB", Entries: []psdfile.DescriptorEntry{filterEditUnit("Rds ", "#Pxl", 1)}},
		{ClassID: "surfaceBlur", Entries: []psdfile.DescriptorEntry{
			filterEditUnit("Rds ", "#Pxl", 3), filterEditLong("Thsh", 5)}},
		{ClassID: "smartSharpen", Entries: []psdfile.DescriptorEntry{
			filterEditBool("useLegacy", false), filterEditUnit("Amnt", "#Prc", 150),
			filterEditUnit("Rds ", "#Pxl", 2), filterEditUnit("noiseReduction", "#Prc", 20),
			{Key: "blur", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "blurType", ValueID: "lensBlur"}},
		}},
		{ClassID: "BrgC", Entries: []psdfile.DescriptorEntry{
			filterEditLong("Brgh", 15), filterEditLong("Cntr", 20), filterEditBool("useLegacy", false)}},
		{ClassID: "HStr", Entries: []psdfile.DescriptorEntry{
			filterEditBool("Clrz", false), {Key: "Adjs", Type: "VlLs", Value: []psdfile.DescriptorValue{
				filterEditHueRange(5, [4]int32{195, 225, 255, 285}),
				filterEditHueRange(0, [4]int32{}),
				filterEditHueRange(2, [4]int32{15, 45, 75, 105}),
			}},
		}},
	}
	ids := []int32{0x47736e42, 745, 741, 0x42726743, 0x48537472}
	values := make([]psdfile.DescriptorValue, len(parameters))
	for index, parameters := range parameters {
		values[index] = psdfile.DescriptorValue{Type: "Objc", Value: &psdfile.Descriptor{
			ClassID: "filterFX", Entries: []psdfile.DescriptorEntry{
				filterEditLong("filterID", ids[index]), filterEditBool("enab", true),
				{Key: "blendOptions", Type: "Objc", Value: &psdfile.Descriptor{
					ClassID: "blendOptions", Entries: []psdfile.DescriptorEntry{
						filterEditUnit("Opct", "#Prc", 100),
						{Key: "Md  ", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "BlnM", ValueID: "Nrml"}},
					},
				}},
				{Key: "Fltr", Type: "Objc", Value: parameters},
				{Key: "opaqueItem", Type: "tdta", Value: []byte{9, byte(index), 255}},
			},
		}}
	}
	return &psdfile.Descriptor{ClassID: "filterFX", Entries: []psdfile.DescriptorEntry{
		filterEditBool("enab", true), filterEditBool("validAtPosition", true),
		filterEditBool("filterMaskEnable", false), filterEditBool("filterMaskLinked", true),
		filterEditBool("filterMaskExtendWithWhite", false),
		{Key: "filterFXList", Type: "VlLs", Value: values},
		{Key: "opaqueStack", Type: "tdta", Value: []byte{17, 23, 31}},
	}}
}

func filterEditPlacement(t *testing.T, left int, key string) []byte {
	t.Helper()
	payload := smartEditPlacement(t, left, "warp")
	bridge, block, err := smartFilterEditSource(context.Background(), payload, key, psdfile.DefaultLimits(), 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close() }()
	descriptor, err := bridge.DecodeDescriptor(psdfile.Span{
		Offset: block.Data.Offset + 8, Length: block.Data.Length - 8,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.Entries = append(descriptor.Entries,
		psdfile.DescriptorEntry{Key: "filterFX", Type: "Objc", Value: filterEditStackDescriptor()})
	encoded, err := bridge.EncodeDescriptor(nil, descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	result := append(bytes.Clone(payload[:8]), encoded...)
	return append(result, payload[len(payload)-3:]...)
}

func filterEditFixture(t *testing.T, version uint16, filteredPeer bool) []byte {
	t.Helper()
	pixels := smartEditImage(smartEditColors())
	linked := writerLinkedEntry("liFD", "fixture-id", 7, smartEditPrototype(version, pixels))
	width := 4
	if version == 2 {
		width = 8
	}
	global := writerLinkedTag("8BIM", "lnk2", width, linked.data, 4)
	var records, payloads []byte
	for index, left := range []int{0, 4} {
		key := []string{"SoLd", "SoLE"}[index]
		placement := filterEditPlacement(t, left, key)
		if index == 1 && !filteredPeer {
			placement = smartEditPlacement(t, left, "warp")
		}
		tags := writerLinkedTag("8BIM", key, 4, placement, 2)
		record, channels := smartEditLayer(version, "Filter instance", uint32(20+index),
			image.Rect(left, 0, left+2, 2), pixels, tags, true)
		records, payloads = append(records, record...), append(payloads, channels...)
	}
	layers := binary.BigEndian.AppendUint16(nil, 2)
	layers = append(layers, records...)
	layers = append(layers, payloads...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	composite := append([]byte{0, 0}, make([]byte, 6*2*3)...)
	return smartEditDocument(version, 6, 2, 3, layers, global, composite)
}

func filterEditReadStack(t *testing.T, source *psdfile.Document, index int) psdfilter.Stack {
	t.Helper()
	placements, err := source.SmartObjects(nil)
	if err != nil || len(placements) != 2 {
		t.Fatalf("placements: count=%d, %v", len(placements), err)
	}
	view, err := psdfile.DecodeSmartFilters(nil, placements[index])
	if err != nil || view == nil || len(view.Filters) != 5 {
		t.Fatalf("filter stack: %+v, %v", view, err)
	}
	stack := psdfilter.Stack{Enabled: view.Enabled}
	for _, item := range view.Filters {
		stack.Filters = append(stack.Filters, item.Operation)
	}
	return stack
}

func filterEditDesired(t *testing.T, source *psdfile.Document) psdfilter.Stack {
	t.Helper()
	stack := filterEditReadStack(t, source, 0)
	stack.Filters[0].Radius = 4.5
	stack.Filters[1].Radius, stack.Filters[1].Threshold = 9.25, 200
	stack.Filters[2].Radius, stack.Filters[2].Amount, stack.Filters[2].NoiseReduction = 3.5, 4.25, 0.75
	stack.Filters[3].Adjustment.BrightnessContrast = &psdadjust.BrightnessContrastParams{
		Brightness: -80, Contrast: -75, Mean: 128, Legacy: true,
	}
	hue := stack.Filters[4].Adjustment.HueSaturation
	hue.Master = psdadjust.HueSaturationValues{Hue: 35, Saturation: 25, Lightness: -15}
	hue.Ranges[4].Values = psdadjust.HueSaturationValues{Hue: -40, Saturation: 100, Lightness: 10}
	hue.Ranges[4].Bounds = [4]int16{180, 210, 250, 290}
	hue.Ranges[1].Values = psdadjust.HueSaturationValues{Hue: 10, Saturation: -80, Lightness: 20}
	for index := range stack.Filters {
		stack.Filters[index].Opacity = 0.5
	}
	stack.Filters[1].Enabled = false
	return stack
}

func filterEditAssertSpan(t *testing.T, before, after *psdfile.Document, first, second psdfile.Span) {
	t.Helper()
	original, err := before.ReadSpan(first)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := after.ReadSpan(second)
	if err != nil || !bytes.Equal(original, actual) {
		t.Fatalf("unrelated source bytes changed: %v", err)
	}
}

func filterEditNonfilterDescriptor(t *testing.T, source *psdfile.Document, index int) []byte {
	t.Helper()
	placements, err := source.SmartObjects(nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := placements[index].Descriptor
	entries := make([]psdfile.DescriptorEntry, 0, len(descriptor.Entries))
	for _, entry := range descriptor.Entries {
		if entry.Key != "filterFX" {
			entries = append(entries, entry)
		}
	}
	descriptor.Entries = entries
	encoded, err := source.EncodeDescriptor(nil, descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func filterEditAssertPreserved(t *testing.T, source, saved *psdfile.Document) {
	t.Helper()
	if len(source.AdditionalInfo) != len(saved.AdditionalInfo) {
		t.Fatal("global linked-source block count changed")
	}
	for index, block := range source.AdditionalInfo {
		filterEditAssertSpan(t, source, saved, block.Span, saved.AdditionalInfo[index].Span)
	}
	for index, original := range source.Layers {
		actual := saved.Layers[index]
		filterEditAssertSpan(t, source, saved, original.MaskData, actual.MaskData)
		filterEditAssertSpan(t, source, saved, original.BlendRanges, actual.BlendRanges)
		if len(original.AdditionalInfo) != len(actual.AdditionalInfo) {
			t.Fatal("layer block count changed")
		}
		for blockIndex, block := range original.AdditionalInfo {
			if index == 0 && block.Key == "SoLd" {
				continue
			}
			filterEditAssertSpan(t, source, saved, block.Span, actual.AdditionalInfo[blockIndex].Span)
		}
		for channelIndex, channel := range original.Channels {
			if index == 1 || channel.ID == -2 {
				filterEditAssertSpan(t, source, saved, channel.Span, actual.Channels[channelIndex].Span)
			}
		}
		if !bytes.Equal(filterEditNonfilterDescriptor(t, source, index),
			filterEditNonfilterDescriptor(t, saved, index)) {
			t.Fatal("placement geometry, warp or opaque metadata changed")
		}
	}
}

func filterEditPreview(first *image.NRGBA) *image.NRGBA {
	preview := image.NewNRGBA(image.Rect(0, 0, 6, 2))
	mask := []byte{255, 128, 0, 255}
	for index, pixels := range []*image.NRGBA{first, smartEditImage(smartEditColors())} {
		for pixel := range 4 {
			value := pixels.NRGBAAt(pixel%2, pixel/2)
			value.A = mask[pixel]
			if value.A == 0 {
				value.R, value.G, value.B = 0, 0, 0
			}
			preview.SetNRGBA(index*4+pixel%2, pixel/2, value)
		}
	}
	return preview
}

func filterEditAssertPreview(t *testing.T, saved *psdfile.Document, expected *image.NRGBA) {
	t.Helper()
	merged, err := saved.DecodeComposite(nil)
	if err != nil || len(merged.Channels) != 4 {
		t.Fatalf("updated saved preview is missing transparency: %v", err)
	}
	for pixel, actual := range merged.Channels[3].Data {
		alpha := uint32(expected.Pix[pixel*4+3])
		if actual != byte(alpha) {
			t.Fatal("saved preview retained stale mask alpha")
		}
		for component := range 3 {
			value := uint32(expected.Pix[pixel*4+component])
			flattened := byte((value*alpha + 255*(255-alpha) + 127) / 255)
			if merged.Channels[component].Data[pixel] != flattened {
				t.Fatal("saved preview retained stale RGB artwork")
			}
		}
	}
}

func TestSmartFilterExtendedEditPSDPSBFourCompressionsAndReopen(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprintf("version-%d", version), func(t *testing.T) {
			editor, source := smartEditOpen(t, filterEditFixture(t, version, true))
			originalPeer := filterEditReadStack(t, source, 1)
			stack := filterEditDesired(t, source)
			pixels := smartEditImage(smartEditColors())
			pixels.Pix[0] = 150
			wantedPixels := bytes.Clone(pixels.Pix)
			preview := filterEditPreview(pixels)
			if err := editor.SetSmartFilterParameters(nil, 1, stack, SmartFilterEditOptions{Pixels: pixels}); err != nil {
				t.Fatal(err)
			}
			before, err := editor.Changes()
			if err != nil || before.Revision != 1 {
				t.Fatalf("first edit revision: %+v, %v", before, err)
			}
			if err := editor.SetSmartFilterParameters(nil, 1, stack, SmartFilterEditOptions{Pixels: pixels}); err != nil {
				t.Fatal(err)
			}
			after, err := editor.Changes()
			if err != nil || after != before {
				t.Fatalf("repeated edit is not a no-op: %+v, %v", after, err)
			}
			stack.Filters[1].Threshold, stack.Filters[2].Amount = 255, 2.5
			stack.Filters[3].Adjustment.BrightnessContrast.Brightness = 70
			stack.Filters[4].Adjustment.HueSaturation.Master.Hue = -60
			stack.Enabled = false
			if err := editor.SetSmartFilterParameters(nil, 1, stack, SmartFilterEditOptions{Pixels: pixels}); err != nil {
				t.Fatal(err)
			}
			want := filterEditDesired(t, source)
			want.Filters[1].Threshold, want.Filters[2].Amount = 255, 2.5
			want.Filters[3].Adjustment.BrightnessContrast.Brightness = 70
			want.Filters[4].Adjustment.HueSaturation.Master.Hue, want.Enabled = -60, false
			clear(pixels.Pix)
			stack.Filters[3].Adjustment.BrightnessContrast.Brightness = 0
			stack.Filters[4].Adjustment.HueSaturation.Master.Hue = 0
			node, _, _, err := editor.findNodeLocked(1)
			if err != nil || node.pixels == nil || !bytes.Equal(node.pixels.Pix, wantedPixels) {
				t.Fatalf("caller pixels are not owned: %v", err)
			}
			for compression := range uint16(4) {
				t.Run(fmt.Sprintf("compression-%d", compression), func(t *testing.T) {
					extension := map[uint16]string{1: "psd", 2: "psb"}[version]
					filename := filepath.Join(t.TempDir(), "edited."+extension)
					if err := editor.Save(nil, filename, SaveOptions{
						Compression: compression, CompositeImage: preview,
					}); err != nil {
						t.Fatal(err)
					}
					saved, err := psdfile.Open(filename)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = saved.Close() }()
					if saved.Header.Version != version || !reflect.DeepEqual(filterEditReadStack(t, saved, 0), want) ||
						!reflect.DeepEqual(filterEditReadStack(t, saved, 1), originalPeer) {
						t.Fatal("saved parameters, order, shared peer or format differ")
					}
					layer, err := saved.DecodeLayer(nil, 0)
					if err != nil {
						t.Fatal(err)
					}
					for component, channel := range layer.Channels[:4] {
						for pixel, actual := range channel.Data {
							if actual != wantedPixels[pixel*4+component] {
								t.Fatal("reopened layer lost accurate caller artwork")
							}
						}
					}
					filterEditAssertPreserved(t, source, saved)
					filterEditAssertPreview(t, saved, preview)
					reopened, err := New(saved)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = reopened.Close() }()
					want.Filters[0].Radius = 6
					owned := smartEditImage(smartEditColors())
					owned.Pix[0] = 150
					if err := reopened.SetSmartFilterParameters(nil, 1, want, SmartFilterEditOptions{Pixels: owned}); err != nil {
						t.Fatalf("reopened extended filter is not editable: %v", err)
					}
					continuedPath := filepath.Join(t.TempDir(), "continued."+extension)
					if err := reopened.Save(nil, continuedPath, SaveOptions{
						Compression: compression, CompositeImage: preview,
					}); err != nil {
						t.Fatalf("saving edits after reopen: %v", err)
					}
					continued, err := psdfile.Open(continuedPath)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = continued.Close() }()
					if !reflect.DeepEqual(filterEditReadStack(t, continued, 0), want) {
						t.Fatal("second save lost the new Gaussian radius or retained extended controls")
					}
					want.Filters[0].Radius = 4.5
				})
			}
		})
	}
}

func TestSmartFilterExtendedEditAutomaticPreviewFromAccurateArtwork(t *testing.T) {
	editor, source := smartEditOpen(t, filterEditFixture(t, 1, false))
	stack := filterEditDesired(t, source)
	pixels := smartEditImage(smartEditColors())
	pixels.Pix[0] = 170
	if err := editor.SetSmartFilterParameters(nil, 1, stack, SmartFilterEditOptions{Pixels: pixels}); err != nil {
		t.Fatal(err)
	}
	preview, err := editor.Composite(nil)
	if err != nil || !bytes.Equal(preview.Pix, filterEditPreview(pixels).Pix) {
		t.Fatalf("accurate filtered artwork and retained masks do not compose: %v", err)
	}
	saved := compositeSave(t, editor, SaveOptions{Compression: 3})
	filterEditAssertPreview(t, saved, preview)
	filterEditAssertPreserved(t, source, saved)
	if !reflect.DeepEqual(filterEditReadStack(t, saved, 0), stack) {
		t.Fatal("automatically saved preview changed live filter controls")
	}
}

func TestSmartFilterExtendedEditFailuresAreAtomic(t *testing.T) {
	cases := []struct {
		name   string
		change func(*psdfilter.Stack)
		want   error
	}{
		{"surface fraction", func(s *psdfilter.Stack) { s.Filters[1].Threshold = 1.5 }, psdfile.ErrMalformed},
		{"surface overflow", func(s *psdfilter.Stack) { s.Filters[1].Threshold = 256 }, psdfile.ErrMalformed},
		{"sharpen NaN", func(s *psdfilter.Stack) { s.Filters[2].Amount = math.NaN() }, psdfile.ErrMalformed},
		{"sharpen noise", func(s *psdfilter.Stack) { s.Filters[2].NoiseReduction = 1.1 }, psdfile.ErrMalformed},
		{"sharpen legacy", func(s *psdfilter.Stack) { s.Filters[2].UseLegacy = true }, psdfile.ErrUnsupported},
		{"brightness domain", func(s *psdfilter.Stack) {
			s.Filters[3].Adjustment.BrightnessContrast.Brightness = 101
		}, psdfile.ErrMalformed},
		{"hue absent range", func(s *psdfilter.Stack) {
			s.Filters[4].Adjustment.HueSaturation.Ranges[0].Values.Hue = 20
		}, psdfile.ErrUnsupported},
		{"hue circular bounds", func(s *psdfilter.Stack) {
			s.Filters[4].Adjustment.HueSaturation.Ranges[4].Bounds = [4]int16{0, 90, 45, 100}
		}, psdfile.ErrMalformed},
		{"missing pixels", nil, ErrUnsupported},
		{"cancelled", nil, context.Canceled},
		{"budget", nil, psdfile.ErrLimit},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			limits := psdfile.DefaultLimits()
			if test.name == "budget" {
				limits.MaxDecodeBytes = 16 << 10
			}
			editor, source := smartEditOpen(t, filterEditFixture(t, 1, true), psdfile.LoadOptions{Limits: limits})
			stack := filterEditDesired(t, source)
			if test.change != nil {
				test.change(&stack)
			}
			ctx := context.Background()
			if test.name == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			options := SmartFilterEditOptions{Pixels: smartEditImage(smartEditColors())}
			if test.name == "missing pixels" {
				options.Pixels = nil
			}
			before, err := editor.Changes()
			if err != nil {
				t.Fatal(err)
			}
			if err := editor.SetSmartFilterParameters(ctx, 1, stack, options); !errors.Is(err, test.want) {
				t.Fatalf("failure category: got %v, want %v", err, test.want)
			}
			after, err := editor.Changes()
			if err != nil || before != after {
				t.Fatalf("failed edit changed revision: %+v, %v", after, err)
			}
			for _, node := range editor.roots {
				if len(node.additional) != 0 || node.pixels != nil || node.placed != nil {
					t.Fatal("failed edit leaked partial overlays")
				}
			}
		})
	}
}
