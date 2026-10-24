package psdedit

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func smartWarpInactiveEffectsFixture(t *testing.T, version uint16, enabled bool, opacity float64) ([]byte, []byte) {
	t.Helper()
	color := &psdfile.Descriptor{ClassID: "RGBC", Entries: []psdfile.DescriptorEntry{
		{Key: "Rd  ", Type: "doub", Value: float64(0)},
		{Key: "Grn ", Type: "doub", Value: float64(255)},
		{Key: "Bl  ", Type: "doub", Value: float64(0)},
	}}
	stroke := &psdfile.Descriptor{ClassID: "FrFX", Entries: []psdfile.DescriptorEntry{
		{Key: "enab", Type: "bool", Value: enabled},
		{Key: "Opct", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Prc", Number: opacity}},
		{Key: "Clr ", Type: "Objc", Value: color},
		{Key: "Sz  ", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Pxl", Number: float64(2)}},
		{Key: "Styl", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "FStl", ValueID: "InsF"}},
	}}
	descriptor := &psdfile.Descriptor{ClassID: "Lefx", Entries: []psdfile.DescriptorEntry{
		{Key: "masterFXSwitch", Type: "bool", Value: true},
		{Key: "FrFX", Type: "Objc", Value: stroke},
		{Key: "futureInactive", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "futureFx",
			Entries: []psdfile.DescriptorEntry{
				{Key: "enab", Type: "bool", Value: false},
				{Key: "opaque", Type: "tdta", Value: []byte{0x8a, 0x07, 0xf1}},
			}}},
	}}
	encoded, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(make([]byte, 4), encoded...)
	payload = append(payload, make([]byte, (4-len(payload)%4)%4)...)
	tags := compositeFixtureTag("SoLd", smartWarpRoundtripPlacement(t, 4, false))
	tags = append(tags, compositeFixtureTag("lfx2", payload)...)
	artwork := smartEditImage(smartEditColors())
	record, channels := smartEditLayer(version, "Warp with retained effects", 73,
		image.Rect(2, 2, 4, 4), artwork, tags, false)
	info := binary.BigEndian.AppendUint16(nil, 0xffff)
	info = append(info, record...)
	info = append(info, channels...)
	if len(info)%2 != 0 {
		info = append(info, 0)
	}
	contents := smartEditPrototype(version, artwork)
	linked := writerLinkedEntry("liFD", "fixture-id", 7, contents)
	lengthWidth := 4
	if version == 2 {
		lengthWidth = 8
	}
	global := writerLinkedTag("8BIM", "lnk2", lengthWidth, linked.data, 4)
	footer := append([]byte{0, 0}, bytes.Repeat([]byte{0x97}, 24*16*4)...)
	return smartEditDocument(version, 24, 16, 4, info, global, footer), payload
}

func TestSmartWarpInactiveEffectsPreserveMetadataAndNeedNoShadowPolicy(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, scenario := range []string{"disabled", "zero-opacity"} {
			t.Run(fmt.Sprintf("v%d/%s", version, scenario), func(t *testing.T) {
				enabled, opacity := scenario == "zero-opacity", float64(100)
				if enabled {
					opacity = 0
				}
				data, originalEffects := smartWarpInactiveEffectsFixture(t, version, enabled, opacity)
				editor, source := smartEditOpen(t, data)
				for round := range 2 {
					view, err := source.ReadLayerMetadata(t.Context(), 0)
					if err != nil || view.Layer.Effects == nil || !view.Layer.Effects.Enabled ||
						!view.Layer.Effects.EffectsVisible {
						t.Fatalf("fixture must keep its effects master switch enabled: %v", err)
					}
					points := slices.Clone(view.Layer.Placed.Warp.MeshPoints)
					points[5].Y -= .05 * float64(round+1)
					if err := editor.SetSmartObjectWarp(t.Context(), 1, points, SmartWarpEditOptions{}); err != nil {
						t.Fatalf("inactive effect requested an unnecessary shadow approximation: %v", err)
					}
					var output bytes.Buffer
					if err := editor.WritePSD(t.Context(), &output); err != nil {
						t.Fatal(err)
					}
					saved, err := psdfile.Parse(output.Bytes())
					if err != nil {
						t.Fatal(err)
					}
					_, block := effectSourceView(t, saved)
					retained, err := saved.ReadSpan(block.Data)
					if err != nil || !bytes.Equal(retained, originalEffects) {
						t.Fatalf("Warp edit rewrote inactive effect or unknown bytes: %v", err)
					}
					if _, err := psdscene.Render(t.Context(), saved); err != nil {
						t.Fatalf("reopened inactive effects need an unnecessary source policy: %v", err)
					}
					editor, err = New(saved)
					if err != nil {
						t.Fatal(err)
					}
					source = saved
					t.Cleanup(func() { _ = saved.Close() })
				}
			})
		}
	}
}

func TestSmartWarpActiveUnsupportedEffectsRemainAtomic(t *testing.T) {
	for _, approximate := range []bool{false, true} {
		data, _ := smartWarpInactiveEffectsFixture(t, 1, true, 100)
		editor, source := smartEditOpen(t, data)
		view, err := source.ReadLayerMetadata(t.Context(), 0)
		if err != nil {
			t.Fatal(err)
		}
		points := slices.Clone(view.Layer.Placed.Warp.MeshPoints)
		points[5].Y -= .1
		unchanged := smartContentsFilterState(t, editor)
		settings := &psdscene.Options{}
		settings.Compose.ApproximateDropShadow = approximate
		if err := editor.SetSmartObjectWarp(t.Context(), 1, points,
			SmartWarpEditOptions{Render: settings}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("active Stroke bypassed the supported Warp source-neighborhood scope: %v", err)
		}
		unchanged()
	}
}
