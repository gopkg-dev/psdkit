package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func colorOverlayPayload(t *testing.T) []byte {
	t.Helper()
	descriptor := &psdfile.Descriptor{ClassID: "Lefx", Version: 16, Entries: []psdfile.DescriptorEntry{
		{Key: "masterFXSwitch", Type: "bool", Value: true},
		{Key: "SoFi", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "SoFi",
			Entries: []psdfile.DescriptorEntry{
				{Key: "enab", Type: "bool", Value: true},
				{Key: "Opct", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Prc", Width: 64,
					Number: float64(100)}},
				{Key: "Md  ", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "BlnM", ValueID: "Nrml"}},
				{Key: "Clr ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "RGBC",
					Entries: []psdfile.DescriptorEntry{
						{Key: "Rd  ", Type: "doub", Value: float64(255)},
						{Key: "Grn ", Type: "doub", Value: float64(0)},
						{Key: "Bl  ", Type: "doub", Value: float64(0)},
					}}},
			}}},
	}}
	encoded, err := psdfile.EncodeDescriptor(nil, descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(make([]byte, 4), encoded...)
	return append(payload, make([]byte, (4-len(payload)%4)%4)...)
}

func TestMultipleColorOverlayLegacyRepresentsOnlyTopmostSourceInstance(t *testing.T) {
	original := psdeffects.Set{Items: []psdeffects.Effect{
		{Kind: psdeffects.ColorOverlay, Enabled: true, Color: [3]float64{1, 0, 0}, Opacity: 1, BlendMode: "norm"},
		{Kind: psdeffects.ColorOverlay, Enabled: true, Color: [3]float64{0, 0, 1}, Opacity: .3, BlendMode: "mul "},
	}}
	desired := original
	desired.Items = append([]psdeffects.Effect(nil), original.Items...)
	desired.Items[1].Color = [3]float64{0, 1, 0}
	data := colorOverlayLegacy()
	before := bytes.Clone(data)
	changed, err := patchLegacyEffects(context.Background(), data, original, desired)
	if err != nil || changed || !bytes.Equal(data, before) {
		t.Fatalf("changing a lower overlay rewrote topmost legacy cache: %v", err)
	}
	desired.Items[0].Color, desired.Items[0].Opacity, desired.Items[0].BlendMode = [3]float64{0, 0, 1}, .5, "scrn"
	changed, err = patchLegacyEffects(context.Background(), data, original, desired)
	if err != nil || !changed || !bytes.Equal(data[:21], before[:21]) {
		t.Fatalf("changing the topmost overlay lost unknown records or failed to update: %v", err)
	}
	payload := data[33:]
	if string(payload[4:12]) != "8BIMscrn" || payload[22] != 128 ||
		binary.BigEndian.Uint16(payload[18:]) != 65535 || binary.BigEndian.Uint16(payload[30:]) != 65535 {
		t.Fatal("multiple overlay legacy cache does not represent its first modern source instance")
	}
}

func colorOverlayLegacy() []byte {
	data := effectLegacyFixture(false)
	binary.BigEndian.PutUint16(data[2:], 2)
	data = append(data, "8BIMsofi"...)
	data = binary.BigEndian.AppendUint32(data, 34)
	payload := make([]byte, 34)
	binary.BigEndian.PutUint32(payload, 2)
	copy(payload[4:12], "8BIMnorm")
	binary.BigEndian.PutUint16(payload[14:], 65535)
	binary.BigEndian.PutUint16(payload[26:], 65535)
	payload[22], payload[23] = 255, 1
	return append(data, payload...)
}

func TestSetColorOverlayEditsPartialSourceAndSynchronizesLegacyPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		tags := compositeFixtureTag("lfx2", colorOverlayPayload(t))
		tags = append(tags, compositeFixtureTag("lrFX", colorOverlayLegacy())...)
		editor := compositeEditor(t, compositeFixture(version, 8, 3, []compositeFixtureLayer{
			{color: []byte{200, 100, 40, 127}, tags: tags},
		}))
		set, _ := effectSourceView(t, editor.source)
		set.Items[0].Color, set.Items[0].Opacity = [3]float64{60.0 / 255, 180.0 / 255, 220.0 / 255}, .5
		if err := editor.SetEffects(nil, 1, *set); err != nil {
			t.Fatal(err)
		}
		preview, err := editor.Composite(nil)
		if err != nil || preview.NRGBAAt(0, 0) != (color.NRGBA{R: 130, G: 140, B: 130, A: 127}) {
			t.Fatalf("partial source shape alpha was applied twice: %v, %v", preview, err)
		}
		set.Items[0].BlendMode = "scrn"
		if err := editor.SetEffects(nil, 1, *set); err != nil {
			t.Fatal(err)
		}
		saved := compositeSave(t, editor, SaveOptions{Compression: 3})
		persisted, _ := effectSourceView(t, saved)
		if persisted.Items[0].Kind != psdeffects.ColorOverlay || persisted.Items[0].BlendMode != "scrn" ||
			persisted.Items[0].Opacity != .5 || persisted.Items[0].Color != set.Items[0].Color {
			t.Fatal("modern color, mode or opacity was lost")
		}
		for _, block := range saved.Layers[0].AdditionalInfo {
			if block.Key != "lrFX" {
				continue
			}
			data, err := saved.ReadSpan(block.Data)
			if err != nil {
				t.Fatal(err)
			}
			before := colorOverlayLegacy()
			if !bytes.Equal(data[:21], before[:21]) {
				t.Fatal("unknown legacy record changed")
			}
			payload := data[33:]
			if string(payload[4:12]) != "8BIMscrn" || payload[22] != 128 || payload[23] != 1 {
				t.Fatal("legacy overlay blend, opacity or fixed flag differs")
			}
			for channel, expected := range []uint16{60 * 257, 180 * 257, 220 * 257} {
				if binary.BigEndian.Uint16(payload[14+channel*2:]) != expected ||
					binary.BigEndian.Uint16(payload[26+channel*2:]) != expected {
					t.Fatal("legacy RGB and native RGB contradict modern overlay color")
				}
			}
		}
		if err := editor.SetEffects(nil, 1, *persisted); err != nil {
			t.Fatal(err)
		}
		invalid := *persisted
		invalid.Items = append([]psdeffects.Effect(nil), persisted.Items...)
		invalid.Items[0].BlendMode = "diss"
		if err := editor.SetEffects(nil, 1, invalid); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported overlay blend was committed: %v", err)
		}
	}
}
