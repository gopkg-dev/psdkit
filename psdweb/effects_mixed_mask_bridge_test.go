package psdweb

import (
	"bytes"
	"encoding/binary"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func bridgeMixedMaskEffectsFixture(t *testing.T, version uint16) []byte {
	t.Helper()
	unit := func(kind string, value float64) psdfile.DescriptorUnit {
		return psdfile.DescriptorUnit{Unit: kind, Width: 64, Number: value}
	}
	descriptor := &psdfile.Descriptor{ClassID: "Lefx", Entries: []psdfile.DescriptorEntry{
		{Key: "masterFXSwitch", Type: "bool", Value: true},
	}}
	for _, kind := range []string{"DrSh", "SoFi"} {
		object := &psdfile.Descriptor{ClassID: kind, Entries: []psdfile.DescriptorEntry{
			{Key: "enab", Type: "bool", Value: true},
			{Key: "Opct", Type: "UntF", Value: unit("#Prc", 100)},
			{Key: "Md  ", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "BlnM", ValueID: "Nrml"}},
			{Key: "Clr ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "RGBC", Entries: []psdfile.DescriptorEntry{
				{Key: "Rd  ", Type: "doub", Value: float64(255)},
				{Key: "Grn ", Type: "doub", Value: float64(0)},
				{Key: "Bl  ", Type: "doub", Value: float64(0)},
			}}},
		}}
		if kind == "DrSh" {
			object.Entries = append(object.Entries,
				psdfile.DescriptorEntry{Key: "Dstn", Type: "UntF", Value: unit("#Pxl", 1)},
				psdfile.DescriptorEntry{Key: "lagl", Type: "UntF", Value: unit("#Ang", 0)},
				psdfile.DescriptorEntry{Key: "blur", Type: "UntF", Value: unit("#Pxl", 0)},
				psdfile.DescriptorEntry{Key: "layerConceals", Type: "bool", Value: true})
		}
		descriptor.Entries = append(descriptor.Entries,
			psdfile.DescriptorEntry{Key: kind, Type: "Objc", Value: object})
	}
	encoded, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(make([]byte, 4), encoded...)
	payload = append(payload, make([]byte, (4-len(payload)%4)%4)...)
	tags := binary.BigEndian.AppendUint32([]byte("8BIMlfx2"), uint32(len(payload)))
	tags = append(tags, payload...)
	tags = append(tags, []byte("8BIMlmgm\x00\x00\x00\x04\x00\x00\x00\x00")...)
	data := bridgeBlendingFixture(version)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	record := source.Layers[1]
	for _, offset := range []int64{source.LayerMaskSection.Offset, source.LayerInfo.Offset} {
		if version == 2 {
			binary.BigEndian.PutUint64(data[offset:], binary.BigEndian.Uint64(data[offset:])+uint64(len(tags)))
		} else {
			binary.BigEndian.PutUint32(data[offset:], binary.BigEndian.Uint32(data[offset:])+uint32(len(tags)))
		}
	}
	lengthOffset := record.Extra.Offset - 4
	binary.BigEndian.PutUint32(data[lengthOffset:], binary.BigEndian.Uint32(data[lengthOffset:])+uint32(len(tags)))
	position := record.Extra.Offset + record.Extra.Length
	result := append(bytes.Clone(data[:position]), tags...)
	return append(result, data[position:]...)
}

func TestMixedEffectsPixelMaskTypedJSONBridgePSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			engine, err := New(Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
			original := bridgeMixedMaskEffectsFixture(t, version)
			source, err := psdfile.Parse(original)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = source.Close() })
			var set *psdeffects.Set
			for _, block := range source.Layers[1].AdditionalInfo {
				if block.Key == "lfx2" {
					set, err = source.DecodeEffects(t.Context(), block)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if set == nil || len(set.Items) != 2 {
				t.Fatal("independent mixed bridge fixture has no effect set")
			}
			set.Items[0].Distance, set.Items[0].Size, set.Items[0].Opacity = 1.25, .5, .5
			set.Items[1].Color, set.Items[1].Opacity = [3]float64{0, 0, 1}, .5
			serialized, err := json.Marshal(set)
			if err != nil {
				t.Fatal(err)
			}
			asset := bridgeAsset(t, engine, "mixed-mask.psd", original)
			opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
			editor := opened["editor"].(string)
			response := engine.Execute(t.Context(), Request{Operation: "editor.SetEffectsWithOptions", Target: editor,
				Args: []jsontext.Value{[]byte(`2`), serialized, []byte(`{"ApproximateDropShadow":true}`)}}, nil)
			if !response.OK {
				decoded, decodeErr := engine.argument(t.Context(), serialized, reflect.TypeFor[psdeffects.Set]())
				if decodeErr != nil {
					t.Fatal(decodeErr)
				}
				input := decoded.Interface().(psdeffects.Set)
				t.Fatalf("typed effect JSON failed: %+v; Set.Unsupported before=%#v after=%#v; effect before=%#v after=%#v",
					response.Error, set.Unsupported, input.Unsupported, set.Items[0].Unsupported, input.Items[0].Unsupported)
			}
			state := blendingBridgeResult[psdedit.Changes](t, bridgeCall(t, engine, "editor.Changes", editor))
			for _, scenario := range []string{"set-unsupported", "effect-unsupported", "scale", "noise"} {
				invalid := *set
				invalid.Items = append([]psdeffects.Effect(nil), set.Items...)
				switch scenario {
				case "set-unsupported":
					invalid.Unsupported = []string{"unrecognized effect metadata"}
				case "effect-unsupported":
					invalid.Items[0].Unsupported = []string{"unknown shadow field"}
				case "scale":
					invalid.Scale = 2
				case "noise":
					invalid.Items[0].Noise = .2
				}
				encoded, err := json.Marshal(invalid)
				if err != nil {
					t.Fatal(err)
				}
				rejected := engine.Execute(t.Context(), Request{Operation: "editor.SetEffectsWithOptions", Target: editor,
					Args: []jsontext.Value{[]byte(`2`), encoded, []byte(`{"ApproximateDropShadow":true}`)}}, nil)
				current := blendingBridgeResult[psdedit.Changes](t, bridgeCall(t, engine, "editor.Changes", editor))
				if rejected.OK || rejected.Error.Code != "unsupported" || len(rejected.Binaries) != 0 ||
					state != current {
					t.Fatalf("invalid %s JSON effects were accepted or partially committed: %+v; before=%#v after=%#v",
						scenario, rejected.Error, state, current)
				}
			}
			bridgeCall(t, engine, "editor.SetEffectsWithOptions", editor, 2, set,
				map[string]any{"ApproximateDropShadow": true})
			if current := blendingBridgeResult[psdedit.Changes](t,
				bridgeCall(t, engine, "editor.Changes", editor)); state != current {
				t.Fatal("equivalent typed JSON effects created a new revision")
			}
			policy := map[string]any{"ApproximateDropShadow": true}
			before := bridgeCall(t, engine, "editor.Composite", editor, policy)
			preview := bridgeMixedMaskRGBA(t, before)
			output := bridgeCall(t, engine, "editor.WritePSD", editor,
				map[string]any{"Compression": 3, "ApproximateDropShadow": true})
			if len(output.Binaries) != 1 {
				t.Fatal("mixed edit did not export PSD/PSB")
			}
			saved := bridgeAsset(t, engine, "mixed-edited.psd", output.Binaries[0].Data)
			reopened := bridgeCall(t, engine, "document.open", "", assetRef(saved)).Data.(map[string]any)
			after := bridgeCall(t, engine, "editor.Composite", reopened["editor"].(string), policy)
			actual := bridgeMixedMaskRGBA(t, after)
			if !bytes.Equal(actual, preview) {
				t.Fatal("bridge mixed preview changed on reopen")
			}
			parsed, err := psdfile.Parse(output.Binaries[0].Data)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = parsed.Close() })
			oldMask, _ := source.ReadSpan(source.Layers[1].MaskData)
			newMask, _ := parsed.ReadSpan(parsed.Layers[1].MaskData)
			if parsed.Header.Version != version || !bytes.Equal(oldMask, newMask) ||
				!bytes.Equal(engine.assets[asset].data, original) {
				t.Fatal("bridge mixed edit changed source asset, mask bytes or file format")
			}
		})
	}
}

func bridgeMixedMaskRGBA(t *testing.T, response Response) []byte {
	t.Helper()
	for _, binary := range response.Binaries {
		if binary.MIME == "application/x-rgba8" {
			if len(binary.Data) != 4 {
				t.Fatal("mixed bridge fixture returned an unexpected image shape")
			}
			return binary.Data
		}
	}
	t.Fatal("mixed preview did not return packed RGBA")
	return nil
}
