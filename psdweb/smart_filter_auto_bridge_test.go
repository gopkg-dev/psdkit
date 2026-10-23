package psdweb

import (
	"bytes"
	"encoding/binary"
	"encoding/json/v2"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
)

func bridgeFilterGray(values [4]byte) *image.NRGBA {
	pixels := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for index, value := range values {
		pixels.SetNRGBA(index%2, index/2, color.NRGBA{R: value, G: value, B: value, A: 255})
	}
	return pixels
}

func bridgeFilterContents(t *testing.T, pixels *image.NRGBA, raster bool) []byte {
	t.Helper()
	if !raster {
		data, err := psdedit.BuildRasterContents(t.Context(), pixels)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, pixels); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func bridgeFilterStackDescriptor() *psdfile.Descriptor {
	parameters := []*psdfile.Descriptor{
		{ClassID: "GsnB", Entries: []psdfile.DescriptorEntry{{Key: "Rds ", Type: "UntF",
			Value: psdfile.DescriptorUnit{Unit: "#Pxl", Number: float64(0)}}}},
		{ClassID: "HStr", Entries: []psdfile.DescriptorEntry{
			{Key: "Clrz", Type: "bool", Value: false},
			{Key: "Adjs", Type: "VlLs", Value: []psdfile.DescriptorValue{{Type: "Objc",
				Value: &psdfile.Descriptor{ClassID: "Hst2", Entries: []psdfile.DescriptorEntry{
					{Key: "LclR", Type: "long", Value: int32(0)}, {Key: "H   ", Type: "long", Value: int32(0)},
					{Key: "Strt", Type: "long", Value: int32(0)}, {Key: "Lght", Type: "long", Value: int32(0)},
				}}}}},
		}},
	}
	ids := []int32{0x47736e42, 0x48537472}
	items := make([]psdfile.DescriptorValue, len(parameters))
	for index, parameter := range parameters {
		items[index] = psdfile.DescriptorValue{Type: "Objc", Value: &psdfile.Descriptor{
			ClassID: "filterFX", Entries: []psdfile.DescriptorEntry{
				{Key: "filterID", Type: "long", Value: ids[index]}, {Key: "enab", Type: "bool", Value: true},
				{Key: "Fltr", Type: "Objc", Value: parameter},
				{Key: "blendOptions", Type: "Objc", Value: &psdfile.Descriptor{
					ClassID: "blendOptions", Entries: []psdfile.DescriptorEntry{
						{Key: "Opct", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Prc", Number: float64(100)}},
						{Key: "Md  ", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "BlnM", ValueID: "Nrml"}},
					}}},
				{Key: "opaqueItem", Type: "tdta", Value: []byte{31, byte(index), 97}},
			}}}
	}
	return &psdfile.Descriptor{ClassID: "filterFX", Entries: []psdfile.DescriptorEntry{
		{Key: "enab", Type: "bool", Value: true}, {Key: "validAtPosition", Type: "bool", Value: true},
		{Key: "filterMaskEnable", Type: "bool", Value: false}, {Key: "filterMaskLinked", Type: "bool", Value: true},
		{Key: "filterMaskExtendWithWhite", Type: "bool", Value: false},
		{Key: "filterFXList", Type: "VlLs", Value: items},
	}}
}

// bridgeFilterFixture builds poisoned parent caches independently of the production PSD writer.
func bridgeFilterFixture(t *testing.T, version uint16, external, raster bool) ([]byte, []byte) {
	t.Helper()
	contents := bridgeFilterContents(t, bridgeFilterGray([4]byte{0, 64, 128, 255}), raster)
	appendLength := func(data []byte, length int) []byte {
		if version == 2 {
			return binary.BigEndian.AppendUint64(data, uint64(length))
		}
		return binary.BigEndian.AppendUint32(data, uint32(length))
	}
	var transform []psdfile.DescriptorValue
	for _, number := range []float64{0, 0, 2, 0, 2, 2, 0, 2} {
		transform = append(transform, psdfile.DescriptorValue{Type: "doub", Value: number})
	}
	encoded, err := psdfile.EncodeDescriptor(t.Context(), &psdfile.Descriptor{
		ClassID: "null", Entries: []psdfile.DescriptorEntry{
			{Key: "Idnt", Type: "TEXT", Value: "filter-fixture"}, {Key: "Type", Type: "long", Value: int32(2)},
			{Key: "Sz  ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "size", Entries: []psdfile.DescriptorEntry{
				{Key: "Wdth", Type: "doub", Value: float64(2)}, {Key: "Hght", Type: "doub", Value: float64(2)},
			}}},
			{Key: "Trnf", Type: "VlLs", Value: transform},
			{Key: "filterFX", Type: "Objc", Value: bridgeFilterStackDescriptor()},
		},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	placement := append(binary.BigEndian.AppendUint32([]byte("soLD"), 4), encoded...)
	var record, samples []byte
	for _, coordinate := range []uint32{0, 0, 2, 2} {
		record = binary.BigEndian.AppendUint32(record, coordinate)
	}
	record = binary.BigEndian.AppendUint16(record, 4)
	for _, channel := range []int16{0, 1, 2, -1} {
		record = binary.BigEndian.AppendUint16(record, uint16(channel))
		record = appendLength(record, 6)
		value := byte(17)
		if channel == -1 {
			value = 255
		}
		samples = append(samples, 0, 0, value, value, value, value)
	}
	record = append(record, []byte("8BIMnorm\xff\x00\x00\x00")...)
	extra := append(make([]byte, 8), 1, 'F', 0, 0)
	extra = append(extra, bridgeSmartTag("lyid", binary.BigEndian.AppendUint32(nil, 20), false, 2)...)
	extra = append(extra, bridgeSmartTag("SoLd", placement, false, 2)...)
	record = binary.BigEndian.AppendUint32(record, uint32(len(extra)))
	record = append(record, extra...)
	layers := append(binary.BigEndian.AppendUint16(nil, 1), record...)
	layers = append(layers, samples...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	kind, key := "liFD", "lnk2"
	if external {
		kind, key = "liFE", "lnkE"
	}
	body := binary.BigEndian.AppendUint32([]byte(kind), 3)
	body = append(body, byte(len("filter-fixture")))
	body = append(body, "filter-fixture"...)
	body = binary.BigEndian.AppendUint32(body, 6)
	for _, unit := range "source" {
		body = binary.BigEndian.AppendUint16(body, uint16(unit))
	}
	fileType := "8BPS"
	if raster {
		fileType = "PNGf"
	}
	body = append(body, []byte(fileType+"\x00\x00\x00\x00")...)
	body = binary.BigEndian.AppendUint64(body, uint64(len(contents)))
	body = append(body, 0)
	if external {
		path, err := psdfile.EncodeDescriptor(t.Context(), &psdfile.Descriptor{ClassID: "null"}, true)
		if err != nil {
			t.Fatal(err)
		}
		body = append(body, path...)
		body = binary.BigEndian.AppendUint64(body, uint64(len(contents)))
	}
	body = append(body, contents...)
	linked := append(binary.BigEndian.AppendUint64(nil, uint64(len(body))), body...)
	linked = append(linked, make([]byte, (4-len(body)%4)%4)...)
	section := append(appendLength(nil, len(layers)), layers...)
	section = binary.BigEndian.AppendUint32(section, 0)
	section = append(section, bridgeSmartTag(key, linked, version == 2 && key == "lnk2", 4)...)
	header := binary.BigEndian.AppendUint16([]byte("8BPS"), version)
	header = append(header, make([]byte, 6)...)
	header = binary.BigEndian.AppendUint16(header, 3)
	header = binary.BigEndian.AppendUint32(header, 2)
	header = binary.BigEndian.AppendUint32(header, 2)
	header = binary.BigEndian.AppendUint16(header, 8)
	header = binary.BigEndian.AppendUint16(header, 3)
	header = append(header, make([]byte, 8)...)
	header = appendLength(header, len(section))
	header = append(header, section...)
	header = append(header, 0, 0)
	return append(header, bytes.Repeat([]byte{23}, 12)...), contents
}

func bridgeFilterStack(t *testing.T, data []byte) psdfilter.Stack {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	placements, err := source.SmartObjects(t.Context())
	if err != nil || len(placements) != 1 {
		t.Fatalf("filter fixture has no unique placement: %v", err)
	}
	decoded, err := psdfile.DecodeSmartFilters(t.Context(), placements[0])
	if err != nil || decoded == nil || len(decoded.Filters) != 2 {
		t.Fatalf("filter fixture has no supported stack: %v", err)
	}
	stack := psdfilter.Stack{Enabled: decoded.Enabled}
	for _, item := range decoded.Filters {
		stack.Filters = append(stack.Filters, item.Operation)
	}
	return stack
}

func bridgeFilterRequest(t *testing.T, engine *Engine, editor string, links map[string]string,
	stack psdfilter.Stack, options map[string]any,
) Response {
	t.Helper()
	request := Request{Operation: "editor.SetSmartFilterParameters", Target: editor, Links: links}
	for _, argument := range []any{1, stack, options} {
		encoded, err := json.Marshal(argument)
		if err != nil {
			t.Fatal(err)
		}
		request.Args = append(request.Args, encoded)
	}
	return engine.Execute(t.Context(), request, nil)
}

func bridgeFilterPixels(t *testing.T, response Response, expected *image.NRGBA) {
	t.Helper()
	if !response.OK || len(response.Binaries) == 0 || response.Binaries[0].MIME != "image/png" {
		t.Fatalf("filter preview is absent: %+v", response.Error)
	}
	pixels, err := png.Decode(bytes.NewReader(response.Binaries[0].Data))
	if err != nil || pixels.Bounds() != expected.Rect {
		t.Fatalf("filter preview dimensions differ: %v", err)
	}
	for y := range expected.Rect.Dy() {
		for x := range expected.Rect.Dx() {
			if actual := color.NRGBAModel.Convert(pixels.At(x, y)); actual != expected.NRGBAAt(x, y) {
				t.Fatalf("filter preview %d,%d = %v, want %v", x, y, actual, expected.NRGBAAt(x, y))
			}
		}
	}
}

func bridgeFilterSaveReopen(t *testing.T, engine *Engine, editor string, version, compression uint16,
	stack psdfilter.Stack, expected *image.NRGBA,
) (map[string]any, []byte) {
	t.Helper()
	// No editor.Composite call or caller Composite/Pixels is supplied before this writer operation.
	saved := bridgeCall(t, engine, "editor.WritePSD", editor, map[string]any{"Compression": compression})
	if len(saved.Binaries) != 1 {
		t.Fatal("automatic filter edit did not export a PSD/PSB")
	}
	data := saved.Binaries[0].Data
	if actual := bridgeFilterStack(t, data); !reflect.DeepEqual(actual, stack) {
		t.Fatalf("saved browser filter controls changed: %+v", actual)
	}
	asset := bridgeAsset(t, engine, "filters-edited.psd", data)
	opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
	if opened["header"].(psdfile.Header).Version != version {
		t.Fatal("automatic filter export changed the PSD/PSB format")
	}
	bridgeFilterPixels(t, bridgeCall(t, engine, "preview.saved", "", ref(opened["document"].(string))), expected)
	return opened, data
}

func TestSmartFilterAutoBridgeExactSaveReopenPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			t.Run(fmt.Sprintf("v%d/c%d", version, compression), func(t *testing.T) {
				engine, err := New(Config{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
				original, contents := bridgeFilterFixture(t, version, false, false)
				asset := bridgeAsset(t, engine, "filters.psd", original)
				opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
				editor := opened["editor"].(string)
				stack := bridgeFilterStack(t, original)
				for _, step := range []struct {
					lightness int16
					gray      [4]byte
					options   map[string]any
				}{
					{51, [4]byte{130, 161, 193, 255}, map[string]any{}},
					{0, [4]byte{0, 64, 128, 255}, map[string]any{"Pixels": nil}},
				} {
					stack.Filters[1].Adjustment.HueSaturation.Master.Lightness = step.lightness
					response := bridgeFilterRequest(t, engine, editor, nil, stack, step.options)
					if !response.OK {
						t.Fatalf("exact automatic bridge edit: %+v", response.Error)
					}
					var saved []byte
					opened, saved = bridgeFilterSaveReopen(t, engine, editor, version, compression,
						stack, bridgeFilterGray(step.gray))
					editor = opened["editor"].(string)
					bridgeFilterPixels(t, linkedRasterBridgeCall(t, engine, "render.source", nil,
						ref(opened["document"].(string)), map[string]any{"SmartFilters": map[string]any{}}),
						bridgeFilterGray(step.gray))
					parsed, err := psdfile.Parse(saved)
					if err != nil {
						t.Fatal(err)
					}
					links, err := parsed.LinkedFiles(t.Context())
					if err != nil || len(links) != 1 {
						t.Fatalf("saved original source disappeared: %v", err)
					}
					encoded, _ := parsed.ReadSpan(links[0].Data)
					_ = parsed.Close()
					if !bytes.Equal(encoded, contents) {
						t.Fatal("per-instance automatic filtering changed embedded source bytes")
					}
				}
				if !bytes.Equal(original, engine.assets[asset].data) {
					t.Fatal("automatic filtering mutated the imported parent asset")
				}
			})
		}
	}
}

func TestSmartFilterAutoBridgeApproximationRequiresNestedExplicitPolicy(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			engine, err := New(Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
			original, _ := bridgeFilterFixture(t, version, false, false)
			asset := bridgeAsset(t, engine, "filters.psd", original)
			opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
			editor := opened["editor"].(string)
			stack := bridgeFilterStack(t, original)
			stack.Filters[0].Radius = .7
			before := blendingBridgeResult[psdedit.Changes](t, bridgeCall(t, engine, "editor.Changes", editor))
			for _, options := range []map[string]any{{}, {"Render": map[string]any{
				"SmartFilters": map[string]any{"ApproximateGaussian": false}}}} {
				response := bridgeFilterRequest(t, engine, editor, nil, stack, options)
				if response.OK || response.Error == nil || response.Error.Code != "unsupported" {
					t.Fatalf("default browser rendering enabled Gaussian approximation: %+v", response.Error)
				}
				if after := blendingBridgeResult[psdedit.Changes](t,
					bridgeCall(t, engine, "editor.Changes", editor)); after != before {
					t.Fatal("refused approximation partially edited the document")
				}
			}
			unchanged := bridgeCall(t, engine, "editor.WritePSD", editor)
			if len(unchanged.Binaries) != 1 || !bytes.Equal(unchanged.Binaries[0].Data, original) {
				t.Fatal("refused approximation changed original output bytes")
			}
			options := map[string]any{"Render": map[string]any{
				"SmartFilters": map[string]any{"ApproximateGaussian": true}}}
			if response := bridgeFilterRequest(t, engine, editor, nil, stack, options); !response.OK {
				t.Fatalf("explicit nested Gaussian approximation was not decoded: %+v", response.Error)
			}
			artwork := bridgeFilterGray([4]byte{0, 64, 128, 255})
			expected, err := psdfilter.Apply(t.Context(), artwork, stack, psdfilter.Options{ApproximateGaussian: true})
			if err != nil || bytes.Equal(expected.Pix, artwork.Pix) {
				t.Fatalf("Gaussian bridge fixture has no observable filter effect: %v", err)
			}
			opened, _ = bridgeFilterSaveReopen(t, engine, editor, version, 3, stack, expected)
			stack.Filters[0].Radius = 0
			if response := bridgeFilterRequest(t, engine, opened["editor"].(string), nil, stack,
				map[string]any{}); !response.OK {
				t.Fatalf("reopened approximate filter could not return to exact controls: %+v", response.Error)
			}
			bridgeFilterSaveReopen(t, engine, opened["editor"].(string), version, 2, stack, artwork)
		})
	}
}

func TestSmartFilterAutoBridgeExternalResolversRequireRenderAndFreshLinks(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, raster := range []bool{false, true} {
			t.Run(fmt.Sprintf("v%d/raster%t", version, raster), func(t *testing.T) {
				engine, err := New(Config{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
				original, retained := bridgeFilterFixture(t, version, true, raster)
				asset := bridgeAsset(t, engine, "external-filters.psd", original)
				opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
				editor := opened["editor"].(string)
				stack := bridgeFilterStack(t, original)
				stack.Filters[1].Adjustment.HueSaturation.Master.Lightness = 51
				pixels := bridgeFilterGray([4]byte{255, 128, 64, 0})
				resolved := bridgeFilterContents(t, pixels, raster)
				mapped := bridgeAsset(t, engine, "current-source", resolved)
				links := map[string]string{"filter-fixture": mapped}
				before := blendingBridgeResult[psdedit.Changes](t, bridgeCall(t, engine, "editor.Changes", editor))
				for _, attempt := range []struct {
					links   map[string]string
					options map[string]any
				}{
					{links: links, options: map[string]any{}},
					{options: map[string]any{"Render": map[string]any{}}},
				} {
					response := bridgeFilterRequest(t, engine, editor, attempt.links, stack, attempt.options)
					if response.OK || response.Error == nil || len(response.Binaries) != 0 {
						t.Fatalf("external filtering reused retained caches or implicitly created Render: %+v", response.Error)
					}
					if after := blendingBridgeResult[psdedit.Changes](t,
						bridgeCall(t, engine, "editor.Changes", editor)); after != before {
						t.Fatal("missing resolver or mapping changed the editor")
					}
				}
				for index, expected := range []*image.NRGBA{
					bridgeFilterGray([4]byte{255, 193, 161, 130}), bridgeFilterGray([4]byte{0, 64, 128, 255}),
				} {
					if index == 1 {
						links["filter-fixture"] = bridgeAsset(t, engine, "next-source", retained)
						stack.Filters[1].Adjustment.HueSaturation.Master.Lightness = 0
					}
					options := map[string]any{"Render": map[string]any{}}
					response := bridgeFilterRequest(t, engine, editor, links, stack, options)
					if !response.OK {
						t.Fatalf("nested Render resolver was not injected for external source: %+v", response.Error)
					}
					var saved []byte
					opened, saved = bridgeFilterSaveReopen(t, engine, editor, version, uint16(index+1), stack, expected)
					editor = opened["editor"].(string)
					parsed, err := psdfile.Parse(saved)
					if err != nil {
						t.Fatal(err)
					}
					records, err := parsed.LinkedFiles(t.Context())
					if err != nil || len(records) != 1 || records[0].Kind != "liFE" {
						t.Fatalf("automatic filtering embedded or lost the external source: %v", err)
					}
					encoded, _ := parsed.ReadSpan(records[0].Data)
					_ = parsed.Close()
					if !bytes.Equal(encoded, retained) {
						t.Fatal("instance filtering rewrote the retained external source cache")
					}
					missing := bridgeFilterRequest(t, engine, editor, nil, stack, options)
					if missing.OK || missing.Error == nil || missing.Error.Code != "invalid_request" {
						t.Fatalf("external edit reused a previous request asset mapping: %+v", missing.Error)
					}
				}
				if !bytes.Equal(original, engine.assets[asset].data) || !bytes.Equal(resolved, engine.assets[mapped].data) {
					t.Fatal("automatic filtering mutated an imported parent or linked-source asset")
				}
			})
		}
	}
}

func TestSmartFilterAutoBridgeEmptyJSONControlArraysPreserveStrictSemantics(t *testing.T) {
	for _, scenario := range []string{"empty", "raw", "levels", "unsupported", "hue-unknown"} {
		t.Run(scenario, func(t *testing.T) {
			engine, err := New(Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
			original, _ := bridgeFilterFixture(t, 1, false, false)
			asset := bridgeAsset(t, engine, "json-controls.psd", original)
			opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
			editor := opened["editor"].(string)
			stack := bridgeFilterStack(t, original)
			adjustment := stack.Filters[1].Adjustment
			adjustment.HueSaturation.Master.Lightness = 51
			adjustment.Levels = []psdadjust.LevelChannel{}
			adjustment.Curves, adjustment.LegacyCurves = []psdadjust.CurveChannel{}, []psdadjust.CurveChannel{}
			adjustment.Raw, adjustment.Unsupported = []byte{}, []string{}
			adjustment.HueSaturation.Unknown = []byte{}
			switch scenario {
			case "raw":
				adjustment.Raw = []byte{1}
			case "levels":
				adjustment.Levels = []psdadjust.LevelChannel{{Channel: 0, Gamma: 1, InputWhite: 255, OutputWhite: 255}}
			case "unsupported":
				adjustment.Unsupported = []string{"unrecognized controls"}
			case "hue-unknown":
				adjustment.HueSaturation.Unknown = []byte{1}
			}
			before := blendingBridgeResult[psdedit.Changes](t, bridgeCall(t, engine, "editor.Changes", editor))
			response := bridgeFilterRequest(t, engine, editor, nil, stack, map[string]any{})
			if scenario == "empty" {
				if !response.OK {
					t.Fatalf("empty JSON arrays changed filter semantics: %+v", response.Error)
				}
				canonical := bridgeFilterStack(t, original)
				canonical.Filters[1].Adjustment.HueSaturation.Master.Lightness = 51
				bridgeFilterSaveReopen(t, engine, editor, 1, 3, canonical, bridgeFilterGray([4]byte{130, 161, 193, 255}))
				return
			}
			if response.OK || response.Error == nil || response.Error.Code != "unsupported" {
				t.Fatalf("nonempty fixed control was silently discarded: %+v", response.Error)
			}
			if after := blendingBridgeResult[psdedit.Changes](t,
				bridgeCall(t, engine, "editor.Changes", editor)); after != before {
				t.Fatal("rejected nonempty controls changed the editor")
			}
			saved := bridgeCall(t, engine, "editor.WritePSD", editor)
			if len(saved.Binaries) != 1 || !bytes.Equal(saved.Binaries[0].Data, original) {
				t.Fatal("rejected nonempty controls changed saved bytes")
			}
		})
	}
}
