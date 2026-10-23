package psdweb

import (
	"bytes"
	"encoding/binary"
	"encoding/json/v2"
	"fmt"
	"image/color"
	"image/png"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit"
	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func TestBlendingEditingCatalogProvidesCallableParameterSchemas(t *testing.T) {
	engine, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	catalog := bridgeCall(t, engine, "system.capabilities", "").Data.([]Capability)
	for _, method := range []string{
		"SetBlendMode", "SetBlendModeContext", "SetClipping",
		"BlendIf", "BlendIfContext", "SetBlendIf", "SetBlendIfContext",
		"AdvancedBlending", "SetAdvancedBlending", "PixelMask", "SetPixelMask",
	} {
		found := false
		for _, capability := range catalog {
			if capability.ID != "editor."+method {
				continue
			}
			found = true
			if capability.TargetKind != "editor" || capability.Description == "" ||
				len(capability.Parameters) == 0 || capability.Parameters[0].Name != "id" {
				t.Fatalf("missing browser invocation contract: %+v", capability)
			}
			for _, parameter := range capability.Parameters {
				if parameter.Name == "ctx" || parameter.Schema == nil {
					t.Fatalf("context leaked into JSON arguments or schema missing: %+v", capability)
				}
			}
		}
		if !found {
			t.Fatalf("editor.%s is missing from the browser capability catalog", method)
		}
	}
}

func TestBlendingAndMaskEditingBridgeSavesAndReopensPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprintf("version%d", version), func(t *testing.T) {
			engine, err := New(Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
			original := bridgeBlendingFixture(version)
			asset := bridgeAsset(t, engine, "blending.psd", original)
			opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
			editor := opened["editor"].(string)
			bridgeCall(t, engine, "editor.SetBlendModeContext", editor, 2, "multiply")
			bridgeCall(t, engine, "editor.SetClipping", editor, 2, true)
			ranges := []psdmodel.BlendRange{{
				Source:      psdmodel.BlendSlider{BlackHigh: 128, WhiteLow: 255, WhiteHigh: 255},
				Destination: psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 255},
			}}
			bridgeCall(t, engine, "editor.SetBlendIfContext", editor, 2, ranges)
			bridgeCall(t, engine, "editor.SetAdvancedBlending", editor, 2, map[string]any{
				"BlendInterior": true, "ChannelRestrictions": []uint32{0},
			})
			bridgeCall(t, engine, "editor.SetAdvancedBlending", editor, 2, map[string]any{
				"BlendInterior": false, "ChannelRestrictions": nil,
			})
			mask := blendingBridgeResult[psdedit.PixelMaskSettings](t,
				bridgeCall(t, engine, "editor.PixelMask", editor, 2))
			mask.Disabled, mask.Density, mask.Feather = true, new(uint8(128)), new(0.0)
			bridgeCall(t, engine, "editor.SetPixelMask", editor, 2, mask)
			mask.Disabled = false
			bridgeCall(t, engine, "editor.SetPixelMask", editor, 2, mask)
			bridgeCall(t, engine, "editor.ReplaceChannel", editor, 2, -2, psdfile.Plane{
				ID: -2, Width: 1, Height: 1, Depth: 8, RowBytes: 1, Data: []byte{255},
			})
			blendingBridgePixel(t, bridgeCall(t, engine, "editor.Composite", editor, map[string]any{}),
				color.NRGBA{R: 255, G: 159, B: 159, A: 255})
			saved := bridgeCall(t, engine, "editor.WritePSD", editor, map[string]any{"Compression": 3})
			if len(saved.Binaries) != 1 || saved.Binaries[0].MIME != "image/vnd.adobe.photoshop" {
				t.Fatal("edited PSD binary was not exported")
			}
			if !bytes.Equal(original, engine.assets[asset].data) {
				t.Fatal("browser edits changed immutable source bytes")
			}
			parsed, err := psdkit.ParseDocument(saved.Binaries[0].Data)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = parsed.Close() }()
			if parsed.Header.Version != version || parsed.Layers[1].BlendMode != "mul " ||
				parsed.Layers[1].Clipping != 1 {
				t.Fatal("PSD format, blend mode or clipping flag did not survive export")
			}
			outputAsset := bridgeAsset(t, engine, "edited.psd", saved.Binaries[0].Data)
			reopened := bridgeCall(t, engine, "document.open", "", assetRef(outputAsset)).Data.(map[string]any)
			reopenedEditor := reopened["editor"].(string)
			actualRanges := blendingBridgeResult[[]psdmodel.BlendRange](t,
				bridgeCall(t, engine, "editor.BlendIfContext", reopenedEditor, 2))
			if !reflect.DeepEqual(actualRanges, ranges) {
				t.Fatalf("Blend If did not survive JSON invocation and save: %+v", actualRanges)
			}
			actualAdvanced := blendingBridgeResult[psdedit.AdvancedBlendingOptions](t,
				bridgeCall(t, engine, "editor.AdvancedBlending", reopenedEditor, 2))
			if actualAdvanced.BlendInterior == nil || *actualAdvanced.BlendInterior ||
				!reflect.DeepEqual(actualAdvanced.ChannelRestrictions, []uint32{0}) {
				t.Fatalf("false or omitted advanced options were lost: %+v", actualAdvanced)
			}
			actualMask := blendingBridgeResult[psdedit.PixelMaskSettings](t,
				bridgeCall(t, engine, "editor.PixelMask", reopenedEditor, 2))
			if !reflect.DeepEqual(actualMask, mask) {
				t.Fatalf("pixel mask settings did not survive export: %+v", actualMask)
			}
			blendingBridgePixel(t, bridgeCall(t, engine, "preview.saved", "", ref(reopened["document"].(string))),
				color.NRGBA{R: 255, G: 159, B: 159, A: 255})
			blendingBridgePixel(t, bridgeCall(t, engine, "editor.Composite", reopenedEditor, map[string]any{}),
				color.NRGBA{R: 255, G: 159, B: 159, A: 255})
			bridgeCall(t, engine, "editor.SetAdvancedBlending", reopenedEditor, 2,
				map[string]any{"Knockout": 1})
			blocked := engine.Execute(nil, Request{Operation: "editor.WritePSD", Target: reopenedEditor}, nil)
			if blocked.OK || blocked.Error.Code != "unsupported" || len(blocked.Binaries) != 0 {
				t.Fatalf("unsupported Knockout with restrictions exported a stale preview: %+v", blocked)
			}
		})
	}
}

func blendingBridgeResult[T any](t *testing.T, response Response) T {
	t.Helper()
	view := response.Data.(map[string]any)["results"].([]any)[0].(map[string]any)["value"]
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var result T
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func blendingBridgePixel(t *testing.T, response Response, expected color.NRGBA) {
	t.Helper()
	if len(response.Binaries) == 0 || response.Binaries[0].MIME != "image/png" {
		t.Fatal("rendered PNG missing from bridge output")
	}
	pixels, err := png.Decode(bytes.NewReader(response.Binaries[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	if actual := color.NRGBAModel.Convert(pixels.At(0, 0)); actual != expected {
		t.Fatalf("edited pixel = %v, want %v", actual, expected)
	}
}

// bridgeBlendingFixture creates a two-layer one-pixel PSD/PSB without the production editor or writer.
func bridgeBlendingFixture(version uint16) []byte {
	appendLength := func(data []byte, length int) []byte {
		if version == 2 {
			return binary.BigEndian.AppendUint64(data, uint64(length))
		}
		return binary.BigEndian.AppendUint32(data, uint32(length))
	}
	header := binary.BigEndian.AppendUint16([]byte("8BPS"), version)
	header = append(header, make([]byte, 6)...)
	header = binary.BigEndian.AppendUint16(header, 3)
	header = binary.BigEndian.AppendUint32(header, 1)
	header = binary.BigEndian.AppendUint32(header, 1)
	header = binary.BigEndian.AppendUint16(header, 8)
	header = binary.BigEndian.AppendUint16(header, 3)
	header = binary.BigEndian.AppendUint32(header, 0)
	header = binary.BigEndian.AppendUint32(header, 0)
	info := binary.BigEndian.AppendUint16(nil, 2)
	var channelData []byte
	for layer := range 2 {
		var record []byte
		for _, coordinate := range []uint32{0, 0, 1, 1} {
			record = binary.BigEndian.AppendUint32(record, coordinate)
		}
		channels := []int16{0, 1, 2, -1}
		if layer == 1 {
			channels = append(channels, -2)
		}
		record = binary.BigEndian.AppendUint16(record, uint16(len(channels)))
		for _, channel := range channels {
			record = binary.BigEndian.AppendUint16(record, uint16(channel))
			record = appendLength(record, 3)
			sample := byte(255)
			if layer == 1 && channel != -1 {
				sample = 64
			}
			channelData = append(channelData, 0, 0, sample)
		}
		record = append(record, []byte("8BIMnorm")...)
		record = append(record, 255, 0, 0, 0)
		var extra []byte
		if layer == 1 {
			extra = binary.BigEndian.AppendUint32(extra, 20)
			for _, coordinate := range []uint32{0, 0, 1, 1} {
				extra = binary.BigEndian.AppendUint32(extra, coordinate)
			}
			extra = append(extra, 255, 0, 0, 0)
		} else {
			extra = binary.BigEndian.AppendUint32(extra, 0)
		}
		extra = binary.BigEndian.AppendUint32(extra, 0)
		extra = append(extra, 1, 'L', 0, 0)
		record = binary.BigEndian.AppendUint32(record, uint32(len(extra)))
		info = append(info, record...)
		info = append(info, extra...)
	}
	info = append(info, channelData...)
	if len(info)%2 != 0 {
		info = append(info, 0)
	}
	section := appendLength(nil, len(info))
	section = append(section, info...)
	section = binary.BigEndian.AppendUint32(section, 0)
	header = appendLength(header, len(section))
	header = append(header, section...)
	return append(header, 0, 0, 255, 255, 255)
}
