package psdedit

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func passRangeEffectMaster(t *testing.T, data []byte, enabled bool) {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	value := byte(0)
	if enabled {
		value = 1
	}
	for _, layer := range source.Layers {
		for _, block := range layer.AdditionalInfo {
			if block.Key != "lfx2" {
				continue
			}
			set, err := source.DecodeEffects(t.Context(), block)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range set.Descriptor.Entries {
				if entry.Key == "masterFXSwitch" {
					if entry.Type != "bool" || entry.Data.Length != 1 {
						t.Fatal("effect master flag is not an unambiguous boolean")
					}
					data[entry.Data.Offset] = value
				}
			}
		}
	}
}

func passRangeEffectFixture(
	t *testing.T, version uint16, multiple, globalHidden bool, location string,
) ([]byte, LayerID) {
	t.Helper()
	tags := compositeFixtureTag("lfx2", effectEditPayload(t, false))
	if multiple {
		tags = append(tags, compositeFixtureTag("zzZZ", []byte{9, 87, 253})...)
	}
	layers := []compositeFixtureLayer{{divider: 3},
		{color: []byte{203, 224, 154, 230}, blend: "scrn"}}
	if location != "group" {
		layers[1].tags = tags
	}
	if multiple {
		layers[1].blend = "norm"
		layers = append(layers, compositeFixtureLayer{color: []byte{20, 40, 210, 64}})
	}
	group := LayerID(len(layers) + 1)
	groupLayer := compositeFixtureLayer{divider: 1, blend: "pass"}
	if location != "child" {
		groupLayer.tags = tags
	}
	layers = append(layers, groupLayer, compositeFixtureLayer{color: []byte{136, 4, 39, 74}})
	data := compositeFixture(version, 8, 4, layers)
	if !globalHidden {
		passRangeEffectMaster(t, data, false)
		return data, group
	}
	resource := []byte("8BIM")
	resource = binary.BigEndian.AppendUint16(resource, 1042)
	resource = append(resource, 0, 0)
	resource = binary.BigEndian.AppendUint32(resource, 1)
	resource = append(resource, 0, 0)
	// The fixture has an empty image-resource section immediately after its empty color-mode section.
	binary.BigEndian.PutUint32(data[30:], uint32(len(resource)))
	result := append(bytes.Clone(data[:34]), resource...)
	return append(result, data[34:]...), group
}

func passRangeEffectMetadata(t *testing.T, source *psdfile.Document) [][]byte {
	t.Helper()
	var result [][]byte
	for _, layer := range source.Layers {
		for _, block := range layer.AdditionalInfo {
			if block.Key != "lfx2" && block.Key != "zzZZ" {
				continue
			}
			data, err := source.ReadSpan(block.Span)
			if err != nil {
				t.Fatal(err)
			}
			result = append(result, data)
		}
	}
	for _, resource := range source.Resources {
		if resource.ID != 1042 {
			continue
		}
		data, err := source.ReadSpan(resource.Span)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, data)
	}
	return result
}

func TestPassRangesDisabledEffectsEditSaveReopen(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, multiple := range []bool{false, true} {
				for _, globalHidden := range []bool{false, true} {
					for _, location := range []string{"group", "child", "both"} {
						name := fmt.Sprintf("v%d_c%d_multiple%t_global%t_%s",
							version, compression, multiple, globalHidden, location)
						t.Run(name, func(t *testing.T) {
							data, group := passRangeEffectFixture(t, version, multiple, globalHidden, location)
							original := bytes.Clone(data)
							editor := compositeEditor(t, data)
							metadata := passRangeEffectMetadata(t, editor.source)
							ranges := []psdmodel.BlendRange{{
								Source:      psdmodel.BlendSlider{BlackLow: 24, BlackHigh: 100, WhiteLow: 175, WhiteHigh: 230},
								Destination: psdmodel.BlendSlider{BlackLow: 10, BlackHigh: 75, WhiteLow: 165, WhiteHigh: 245},
							}}
							if err := editor.SetBlendIf(group, ranges); err != nil {
								t.Fatal(err)
							}
							if err := editor.SetOpacity(group, 161); err != nil {
								t.Fatal(err)
							}
							if err := editor.SetOpacity(2, 191); err != nil {
								t.Fatal(err)
							}
							reopened, _ := verifyPassRangeEffectRoundTrip(t, editor, metadata, version, compression)
							ranges[0].Source.BlackHigh, ranges[0].Destination.WhiteLow = 145, 190
							if err := reopened.SetBlendIf(group, ranges); err != nil {
								t.Fatal(err)
							}
							if err := reopened.SetOpacity(group, 121); err != nil {
								t.Fatal(err)
							}
							_, saved := verifyPassRangeEffectRoundTrip(t, reopened, metadata, version, compression)
							active, err := saved.ReadSpan(saved.Source)
							if err != nil {
								t.Fatal(err)
							}
							if globalHidden {
								for _, resource := range saved.Resources {
									if resource.ID == 1042 {
										active[resource.Data.Offset] = 1
									}
								}
							} else {
								passRangeEffectMaster(t, active, true)
							}
							activated := compositeEditor(t, active)
							if err := activated.SetOpacity(group, 122); err != nil {
								t.Fatal(err)
							}
							before, _ := activated.Changes()
							var rejected bytes.Buffer
							if err := activated.Write(t.Context(), &rejected); !errors.Is(err, ErrUnsupported) || rejected.Len() != 0 {
								t.Fatalf("reactivated effects escaped the pass-group restriction: %v", err)
							}
							after, _ := activated.Changes()
							if before != after || !bytes.Equal(data, original) {
								t.Fatal("failed automatic save changed state or borrowed original bytes")
							}
						})
					}
				}
			}
		}
	}
}

func verifyPassRangeEffectRoundTrip(
	t *testing.T, editor *Editor, metadata [][]byte, version, compression uint16,
) (*Editor, *psdfile.Document) {
	t.Helper()
	pixels, err := editor.Composite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	saved := compositeSave(t, editor, SaveOptions{Compression: compression})
	if saved.Header.Version != version {
		t.Fatal("save changed the PSD/PSB version")
	}
	preserved := passRangeEffectMetadata(t, saved)
	if len(preserved) != len(metadata) {
		t.Fatal("save removed inactive effect metadata")
	}
	for index := range metadata {
		if !bytes.Equal(metadata[index], preserved[index]) {
			t.Fatal("Blend If or opacity editing rewrote inactive effect fields, padding or visibility resource")
		}
	}
	pixel := pixels.NRGBAAt(0, 0)
	reopened := lifecycleExpectPreview(t, editor, saved, pixel)
	merged := []byte{0, 0, 0, pixel.A}
	for index, value := range []byte{pixel.R, pixel.G, pixel.B} {
		merged[index] = byte((int(value)*int(pixel.A) + 255*(255-int(pixel.A)) + 127) / 255)
	}
	compositeExpect(t, saved, merged)
	return reopened, saved
}
