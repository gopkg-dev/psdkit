package psdweb

import (
	"bytes"
	"encoding/binary"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"image"
	"image/color"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func TestCompletedEditingCapabilitiesExposeBrowserContracts(t *testing.T) {
	engine, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	catalog := bridgeCall(t, engine, "system.capabilities", "").Data.([]Capability)
	for method, names := range map[string][]string{
		"AddVectorMask":                      {"id", "mask"},
		"RemoveVectorMask":                   {"id"},
		"SetSmartObjectTransform":            {"id", "corners"},
		"SetSmartObjectTransformWithOptions": {"id", "corners", "options"},
		"SetPatternFillWithOptions":          {"id", "requested", "options"},
		"SetTextContent":                     {"id", "text", "options"},
		"SetSmartFilterParameters":           {"id", "stack", "options"},
	} {
		found := false
		for _, capability := range catalog {
			if capability.ID != "editor."+method {
				continue
			}
			found = true
			if capability.TargetKind != "editor" || capability.Description == "" ||
				len(capability.Parameters) != len(names) {
				t.Fatalf("missing completed browser capability: %+v", capability)
			}
			for index, parameter := range capability.Parameters {
				if parameter.Name != names[index] || parameter.Schema == nil || parameter.Optional {
					t.Fatalf("callback/context leaked or browser argument contract changed: %+v", capability)
				}
			}
		}
		if !found {
			t.Fatalf("editor.%s is absent from the browser catalog", method)
		}
	}
}

func TestFixedByteArraysAcceptBase64AndNumericJSONWithoutPartialEdits(t *testing.T) {
	engine, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
	for _, raw := range []jsontext.Value{[]byte(`"AAH/"`), []byte(`"\u0041AH/"`), []byte(`[0,1,255]`)} {
		value, err := engine.argument(t.Context(), raw, reflect.TypeFor[[3]byte]())
		if err != nil || value.Interface() != ([3]byte{0, 1, 255}) {
			t.Fatalf("fixed bytes were not decoded losslessly: %v", err)
		}
	}
	for _, raw := range []jsontext.Value{
		[]byte(`"AA=="`), []byte(`"AAAAAA=="`), []byte(`"!?"`), []byte(`[0,1]`),
		[]byte(`"` + strings.Repeat("A", 4096) + `"`),
	} {
		if _, err := engine.argument(t.Context(), raw, reflect.TypeFor[[3]byte]()); err == nil {
			t.Fatalf("invalid fixed byte-array encoding accepted: %s", raw)
		}
	}
	if _, err := engine.argument(t.Context(), []byte(`"AAH/"`), reflect.TypeFor[[3]uint16]()); err == nil {
		t.Fatal("Base64 was accepted as a non-byte fixed array")
	}
	original := bridgeBlendingFixture(1)
	asset := bridgeAsset(t, engine, "invalid-vector.psd", original)
	opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
	editor := opened["editor"].(string)
	encoded, err := json.Marshal(bridgeCompletionVectorMask(t))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, header := range []jsontext.Value{[]byte(`"AA=="`), []byte(`"!?"`)} {
		fields["Header"] = header
		invalid, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		response := engine.Execute(t.Context(), Request{Operation: "editor.AddVectorMask", Target: editor,
			Args: []jsontext.Value{[]byte(`2`), invalid}}, nil)
		if response.OK || response.Error.Code != "invalid_request" || len(response.Binaries) != 0 {
			t.Fatalf("malformed vector bytes did not fail before editing: %+v", response)
		}
		saved := bridgeCall(t, engine, "editor.WritePSD", editor)
		if len(saved.Binaries) != 1 || !bytes.Equal(saved.Binaries[0].Data, original) {
			t.Fatal("malformed fixed byte array partially edited the document")
		}
	}
}

func TestVectorLifecycleBridgeSaveReopenPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			engine, err := New(Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
			original := bridgeBlendingFixture(version)
			asset := bridgeAsset(t, engine, "vector.psd", original)
			opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
			editor := opened["editor"].(string)
			bridgeCall(t, engine, "editor.RemovePixelMask", editor, 2)
			mask := bridgeCompletionVectorMask(t)
			bridgeCall(t, engine, "editor.AddVectorMask", editor, 2, mask)
			for index, step := range []struct {
				operation string
				present   bool
				disabled  bool
				gray      byte
			}{
				{present: true, gray: 159},
				{operation: "SetVectorMask", present: true, disabled: true, gray: 64},
				{operation: "RemoveVectorMask", gray: 64},
				{operation: "AddVectorMask", present: true, gray: 159},
			} {
				if step.operation == "RemoveVectorMask" {
					bridgeCall(t, engine, "editor."+step.operation, editor, 2)
				} else if step.operation != "" {
					mask.Disabled = step.disabled
					bridgeCall(t, engine, "editor."+step.operation, editor, 2, mask)
				}
				expected := color.NRGBA{R: step.gray, G: step.gray, B: step.gray, A: 255}
				blendingBridgePixel(t, bridgeCall(t, engine, "editor.Composite", editor), expected)
				saved := bridgeCall(t, engine, "editor.WritePSD", editor, map[string]any{"Compression": index})
				if len(saved.Binaries) != 1 {
					t.Fatal("vector lifecycle edit did not produce a PSD/PSB binary")
				}
				parsed, err := psdfile.Parse(saved.Binaries[0].Data)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = parsed.Close() })
				if parsed.Header.Version != version || len(parsed.Layers[1].Channels) != 4 {
					t.Fatal("vector lifecycle changed the format or added raster mask channels")
				}
				count := 0
				for _, block := range parsed.Layers[1].AdditionalInfo {
					if block.Key != "vmsk" && block.Key != "vsms" {
						continue
					}
					count++
					actual, err := parsed.DecodeVectorMaskBlock(t.Context(), block)
					if err != nil || actual.Disabled != step.disabled || actual.Subpaths[0].Knots[1].Anchor.X != 0.5 {
						t.Fatalf("vector geometry or disabled flag changed after saving: %v", err)
					}
				}
				if step.present && count != 1 || !step.present && count != 0 {
					t.Fatalf("saved vector block count = %d", count)
				}
				output := bridgeAsset(t, engine, "vector-edited.psd", saved.Binaries[0].Data)
				reopened := bridgeCall(t, engine, "document.open", "", assetRef(output)).Data.(map[string]any)
				editor = reopened["editor"].(string)
				blendingBridgePixel(t, bridgeCall(t, engine, "editor.Composite", editor), expected)
				blendingBridgePixel(t, bridgeCall(t, engine, "preview.saved", "",
					ref(reopened["document"].(string))), expected)
			}
			if !bytes.Equal(original, engine.assets[asset].data) {
				t.Fatal("vector lifecycle mutated the source browser asset")
			}
		})
	}
}

func TestSmartTransformBridgeSaveReopenPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, external := range []bool{false, true} {
			for compression := range 4 {
				t.Run(fmt.Sprintf("%d_external%t_compression%d", version, external, compression), func(t *testing.T) {
					engine, err := New(Config{})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
					original, contents := bridgeSmartFixture(t, version, external)
					asset := bridgeAsset(t, engine, "transform.psd", original)
					opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
					editor := opened["editor"].(string)
					infos := blendingBridgeResult[[]psdedit.SmartObjectInfo](t,
						bridgeCall(t, engine, "editor.SmartObjects", editor))
					links := map[string]string{infos[0].SourceID: bridgeAsset(t, engine, "source.psd", contents)}
					corners := [4]psdmodel.Point{{X: 0, Y: 0}, {X: 2, Y: 0}, {X: 2, Y: 1}, {X: 0, Y: 1}}
					if external {
						bridgeSmartCall(t, engine, "SetSmartObjectTransformWithOptions", editor, links,
							infos[0].ID, corners, psdedit.SmartTransformOptions{})
					} else {
						bridgeCall(t, engine, "editor.SetSmartObjectTransform", editor, infos[0].ID, corners)
					}
					saved := bridgeCall(t, engine, "editor.WritePSD", editor, map[string]any{"Compression": compression})
					if len(saved.Binaries) != 1 {
						t.Fatal("single-instance transform did not export a PSD/PSB")
					}
					parsed, err := psdfile.Parse(saved.Binaries[0].Data)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = parsed.Close() })
					placed, err := parsed.SmartObjects(t.Context())
					if err != nil || len(placed) != 2 || parsed.Header.Version != version ||
						!slices.Equal(placed[0].Transform, []float64{0, 0, 2, 0, 2, 1, 0, 1}) ||
						!slices.Equal(placed[0].NonAffine, placed[0].Transform) ||
						!slices.Equal(placed[1].Transform, []float64{1, 0, 2, 0, 2, 1, 1, 1}) ||
						parsed.Layers[0].Bounds != image.Rect(0, 0, 2, 1) {
						t.Fatalf("transform changed a shared peer or lost its saved geometry: %v", err)
					}
					output := bridgeAsset(t, engine, "transform-edited.psd", saved.Binaries[0].Data)
					reopened := bridgeCall(t, engine, "document.open", "", assetRef(output)).Data.(map[string]any)
					newEditor := reopened["editor"].(string)
					for _, info := range infos {
						read := bridgeSmartCall(t, engine, "ReadSmartObjectContents", newEditor, links, info.ID)
						bridgeSmartBytes(t, read, contents)
					}
					gray := color.NRGBA{R: 120, G: 120, B: 120, A: 255}
					bridgeSmartPreview(t, bridgeCall(t, engine, "editor.Composite", newEditor), gray)
					bridgeSmartPreview(t, bridgeCall(t, engine, "preview.saved", "",
						ref(reopened["document"].(string))), gray)
					if !bytes.Equal(original, engine.assets[asset].data) {
						t.Fatal("transform mutated the original browser asset")
					}
				})
			}
		}
	}
}

func bridgeCompletionVectorMask(t *testing.T) *psdvector.Mask {
	t.Helper()
	data := binary.BigEndian.AppendUint32(nil, 3)
	data = binary.BigEndian.AppendUint32(data, 0)
	var record [26]byte
	binary.BigEndian.PutUint16(record[:2], 6)
	data = append(data, record[:]...)
	clear(record[:])
	binary.BigEndian.PutUint16(record[:2], 8)
	data = append(data, record[:]...)
	clear(record[:])
	binary.BigEndian.PutUint16(record[2:4], 4)
	binary.BigEndian.PutUint16(record[4:6], 1)
	binary.BigEndian.PutUint16(record[6:8], 1)
	data = append(data, record[:]...)
	for _, point := range [][2]uint32{{0, 0}, {1 << 23, 0}, {1 << 23, 1 << 24}, {0, 1 << 24}} {
		clear(record[:])
		binary.BigEndian.PutUint16(record[:2], 2)
		for offset := 2; offset < 26; offset += 8 {
			binary.BigEndian.PutUint32(record[offset:], point[1])
			binary.BigEndian.PutUint32(record[offset+4:], point[0])
		}
		data = append(data, record[:]...)
	}
	mask, err := psdvector.DecodeMask(t.Context(), append(data, 0, 0), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return mask
}
