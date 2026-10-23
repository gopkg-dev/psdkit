package psdweb

import (
	"bytes"
	"encoding/binary"
	"encoding/json/v2"
	"fmt"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func bridgeVectorOverlayFixture(t *testing.T, version uint16) []byte {
	t.Helper()
	effect, err := psdfile.EncodeDescriptor(t.Context(), &psdfile.Descriptor{ClassID: "Lefx",
		Entries: []psdfile.DescriptorEntry{
			{Key: "masterFXSwitch", Type: "bool", Value: true},
			{Key: "SoFi", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "SoFi",
				Entries: []psdfile.DescriptorEntry{
					{Key: "enab", Type: "bool", Value: true},
					{Key: "Opct", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Prc", Number: float64(100)}},
					{Key: "Md  ", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "BlnM", ValueID: "Nrml"}},
					{Key: "Clr ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "RGBC",
						Entries: []psdfile.DescriptorEntry{
							{Key: "Rd  ", Type: "doub", Value: float64(255)},
							{Key: "Grn ", Type: "doub", Value: float64(0)},
							{Key: "Bl  ", Type: "doub", Value: float64(0)},
						}}},
				}}},
		}}, true)
	if err != nil {
		t.Fatal(err)
	}
	vector, err := psdvector.EncodeMask(t.Context(), bridgeCompletionVectorMask(t))
	if err != nil {
		t.Fatal(err)
	}
	appendLength := func(data []byte, length int) []byte {
		if version == 2 {
			return binary.BigEndian.AppendUint64(data, uint64(length))
		}
		return binary.BigEndian.AppendUint32(data, uint32(length))
	}
	var record, channels []byte
	for _, coordinate := range []uint32{0, 0, 1, 1} {
		record = binary.BigEndian.AppendUint32(record, coordinate)
	}
	record = binary.BigEndian.AppendUint16(record, 5)
	for index, id := range []int16{0, 1, 2, -1, -2} {
		record = binary.BigEndian.AppendUint16(record, uint16(id))
		record = appendLength(record, 3)
		channels = append(channels, 0, 0, []byte{200, 100, 40, 127, 64}[index])
	}
	record = append(record, []byte("8BIMnorm\xff\x00\x00\x00")...)
	extra := binary.BigEndian.AppendUint32(nil, 20)
	for _, coordinate := range []uint32{0, 0, 1, 1} {
		extra = binary.BigEndian.AppendUint32(extra, coordinate)
	}
	extra = append(extra, 255, 0, 0, 0)
	extra = binary.BigEndian.AppendUint32(extra, 0)
	extra = append(extra, 1, 'L', 0, 0)
	extra = append(extra, bridgeSmartTag("lfx2", append(make([]byte, 4), effect...), false, 2)...)
	extra = append(extra, bridgeSmartTag("vmsk", vector, false, 2)...)
	extra = append(extra, bridgeSmartTag("pRsv", []byte{0x7a, 0x11, 0x91}, false, 2)...)
	record = binary.BigEndian.AppendUint32(record, uint32(len(extra)))
	record = append(record, extra...)
	layers := append(binary.BigEndian.AppendUint16(nil, 0xffff), record...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	section := append(appendLength(nil, len(layers)), layers...)
	section = binary.BigEndian.AppendUint32(section, 0)
	header := binary.BigEndian.AppendUint16([]byte("8BPS"), version)
	header = append(header, make([]byte, 6)...)
	header = binary.BigEndian.AppendUint16(header, 4)
	header = binary.BigEndian.AppendUint32(header, 1)
	header = binary.BigEndian.AppendUint32(header, 1)
	header = binary.BigEndian.AppendUint16(header, 8)
	header = binary.BigEndian.AppendUint16(header, 3)
	header = append(header, make([]byte, 8)...)
	header = appendLength(header, len(section))
	header = append(header, section...)
	return append(header, 0, 0, 23, 23, 23, 23)
}

func bridgeVectorOverlaySet(t *testing.T, data []byte) *psdeffects.Set {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, block := range source.Layers[0].AdditionalInfo {
		if block.Key != "lfx2" {
			continue
		}
		set, err := source.DecodeEffects(t.Context(), block)
		if err != nil {
			t.Fatal(err)
		}
		return set
	}
	t.Fatal("vector color-overlay fixture has no effects")
	return nil
}

func bridgeVectorOverlaySavedPixel(pixel color.NRGBA) color.NRGBA {
	// PSD's saved RGB8 composite stores white-matted bytes; low alpha magnifies that encoding's quantization.
	if pixel.A == 0 {
		return color.NRGBA{}
	}
	coverage := float64(pixel.A) / 255
	result := color.NRGBA{A: pixel.A}
	for index, component := range []byte{pixel.R, pixel.G, pixel.B} {
		matted := math.RoundToEven(float64(component)*coverage + 255 - float64(pixel.A))
		straight := (matted/255 - (1 - coverage)) / coverage
		value := byte(math.Round(max(0, min(1, straight)) * 255))
		switch index {
		case 0:
			result.R = value
		case 1:
			result.G = value
		case 2:
			result.B = value
		}
	}
	return result
}

func bridgeVectorOverlayPreview(t *testing.T, response Response, expected color.NRGBA) {
	t.Helper()
	blendingBridgePixel(t, response, expected)
	for _, binary := range response.Binaries {
		if binary.MIME != "application/x-rgba8" {
			continue
		}
		if !bytes.Equal(binary.Data, []byte{expected.R, expected.G, expected.B, expected.A}) {
			t.Fatalf("packed straight RGBA differs from the PNG preview: %v", binary.Data)
		}
		return
	}
	t.Fatal("browser preview omitted its packed straight RGBA buffer")
}

func TestColorOverlayVectorBridgeEditSaveReopenAndCurrentMasks(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			engine, err := New(Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
			original := bridgeVectorOverlayFixture(t, version)
			asset := bridgeAsset(t, engine, "masked-overlay.psd", original)
			opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
			editor := opened["editor"].(string)
			current := original
			for index, step := range []struct {
				rgb      [3]byte
				opacity  float64
				maskEdit string
				pixel    color.NRGBA
			}{
				{[3]byte{60, 180, 220}, .5, "", color.NRGBA{R: 130, G: 140, B: 130, A: 16}},
				{[3]byte{20, 220, 100}, .25, "", color.NRGBA{R: 155, G: 130, B: 55, A: 16}},
				{[3]byte{40, 60, 240}, 1, "RemoveVectorMask", color.NRGBA{R: 40, G: 60, B: 240, A: 32}},
				{[3]byte{80, 120, 200}, 1, "AddVectorMask", color.NRGBA{R: 80, G: 120, B: 200, A: 16}},
			} {
				switch step.maskEdit {
				case "RemoveVectorMask":
					bridgeCall(t, engine, "editor.RemoveVectorMask", editor, 1)
				case "AddVectorMask":
					bridgeCall(t, engine, "editor.AddVectorMask", editor, 1, bridgeCompletionVectorMask(t))
				}
				set := bridgeVectorOverlaySet(t, current)
				for channel, value := range step.rgb {
					set.Items[0].Color[channel] = float64(value) / 255
				}
				set.Items[0].Opacity = step.opacity
				bridgeCall(t, engine, "editor.SetEffects", editor, 1, set)
				bridgeVectorOverlayPreview(t, bridgeCall(t, engine, "editor.Composite", editor), step.pixel)
				saved := bridgeCall(t, engine, "editor.WritePSD", editor, map[string]any{"Compression": index})
				if len(saved.Binaries) != 1 {
					t.Fatal("browser vector-overlay edit did not produce PSD/PSB bytes")
				}
				current = saved.Binaries[0].Data
				persisted := bridgeVectorOverlaySet(t, current)
				if persisted.Items[0].Color != set.Items[0].Color || persisted.Items[0].Opacity != step.opacity {
					t.Fatal("browser JSON parameter transport lost the edited overlay controls")
				}
				output := bridgeAsset(t, engine, "masked-overlay-saved.psd", current)
				opened = bridgeCall(t, engine, "document.open", "", assetRef(output)).Data.(map[string]any)
				editor = opened["editor"].(string)
				if opened["header"].(psdfile.Header).Version != version {
					t.Fatal("browser mask/effect edit changed the PSD/PSB format")
				}
				bridgeVectorOverlayPreview(t, bridgeCall(t, engine, "preview.saved", "", ref(opened["document"].(string))),
					bridgeVectorOverlaySavedPixel(step.pixel))
				bridgeVectorOverlayPreview(t, bridgeCall(t, engine, "editor.Composite", editor), step.pixel)
			}
			if !bytes.Equal(original, engine.assets[asset].data) {
				t.Fatal("browser masked overlay mutated original imported bytes")
			}
		})
	}
}

func TestColorOverlayVectorBridgeRejectsNonemptyUnsupportedMetadata(t *testing.T) {
	for _, scope := range []string{"set", "effect"} {
		t.Run(scope, func(t *testing.T) {
			engine, err := New(Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
			original := bridgeVectorOverlayFixture(t, 1)
			asset := bridgeAsset(t, engine, "strict-overlay.psd", original)
			opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
			editor := opened["editor"].(string)
			set := bridgeVectorOverlaySet(t, original)
			set.Items[0].Color, set.Items[0].Opacity = [3]float64{0, 0, 1}, .5
			if scope == "set" {
				set.Unsupported = []string{"unknown set semantics"}
			} else {
				set.Items[0].Unsupported = []string{"unknown effect semantics"}
			}
			before := blendingBridgeResult[psdedit.Changes](t, bridgeCall(t, engine, "editor.Changes", editor))
			request := Request{Operation: "editor.SetEffects", Target: editor}
			for _, argument := range []any{1, set} {
				encoded, err := json.Marshal(argument)
				if err != nil {
					t.Fatal(err)
				}
				request.Args = append(request.Args, encoded)
			}
			response := engine.Execute(t.Context(), request, nil)
			if response.OK || response.Error == nil || response.Error.Code != "unsupported" {
				t.Fatalf("nonempty unsupported metadata was silently discarded: %+v", response.Error)
			}
			if after := blendingBridgeResult[psdedit.Changes](t,
				bridgeCall(t, engine, "editor.Changes", editor)); after != before {
				t.Fatal("rejected unsupported metadata changed editor state")
			}
			saved := bridgeCall(t, engine, "editor.WritePSD", editor)
			if len(saved.Binaries) != 1 || !bytes.Equal(saved.Binaries[0].Data, original) {
				t.Fatal("rejected metadata changed saved source bytes")
			}
		})
	}
}
