package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func innerEffectPayload(t *testing.T, family string) []byte {
	t.Helper()
	unit := func(name string, value float64) psdfile.DescriptorUnit {
		return psdfile.DescriptorUnit{Unit: name, Width: 64, Number: value}
	}
	effect := &psdfile.Descriptor{ClassID: family, Entries: []psdfile.DescriptorEntry{
		{Key: "enab", Type: "bool", Value: true},
		{Key: "Md  ", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "BlnM", ValueID: "Nrml"}},
		{Key: "Opct", Type: "UntF", Value: unit("#Prc", 75)},
		{Key: "Clr ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "RGBC", Entries: []psdfile.DescriptorEntry{
			{Key: "Rd  ", Type: "doub", Value: float64(0)},
			{Key: "Grn ", Type: "doub", Value: float64(255)},
			{Key: "Bl  ", Type: "doub", Value: float64(0)},
		}}},
		{Key: "blur", Type: "UntF", Value: unit("#Pxl", 0)},
		{Key: "Ckmt", Type: "UntF", Value: unit("#Pxl", 100)},
	}}
	if family == "IrSh" {
		effect.Entries = append(effect.Entries,
			psdfile.DescriptorEntry{Key: "Dstn", Type: "UntF", Value: unit("#Pxl", 1)},
			psdfile.DescriptorEntry{Key: "lagl", Type: "UntF", Value: unit("#Ang", 0)})
	} else {
		effect.Entries = append(effect.Entries, psdfile.DescriptorEntry{Key: "glwS", Type: "enum",
			Value: psdfile.DescriptorEnum{TypeID: "IGSr", ValueID: "SrcE"}})
	}
	descriptor := &psdfile.Descriptor{ClassID: "Lefx", Version: 16, Entries: []psdfile.DescriptorEntry{
		{Key: "masterFXSwitch", Type: "bool", Value: true},
		{Key: family, Type: "Objc", Value: effect},
		{Key: "futureInactive", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "futureFx",
			Entries: []psdfile.DescriptorEntry{
				{Key: "enab", Type: "bool", Value: false},
				{Key: "opaque", Type: "tdta", Value: []byte{0x9f, 0x21, 0x77}},
				{Key: "opaque", Type: "long", Value: int32(17)},
			}}},
	}}
	encoded, err := psdfile.EncodeDescriptor(nil, descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(make([]byte, 4), encoded...)
	return append(payload, make([]byte, (4-len(payload)%4)%4)...)
}

func TestSetInnerEffectsPreservesOrderedUnknownMetadataAndOwnsPreview(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, family := range []string{"IrSh", "IrGl"} {
			tags := compositeFixtureTag("lfx2", innerEffectPayload(t, family))
			tags = append(tags, compositeFixtureTag("zzzz", []byte{0x19, 0x27, 0x35, 0x41})...)
			editor := compositeEditor(t, compositeFixture(version, 8, 3, []compositeFixtureLayer{
				{color: []byte{255, 0, 0, 255}, tags: tags},
			}))
			original, err := editor.source.ReadSpan(editor.source.Source)
			if err != nil {
				t.Fatal(err)
			}
			set, _ := effectSourceView(t, editor.source)
			set.Items[0].Color, set.Items[0].Opacity, set.Items[0].BlendMode = [3]float64{0, 0, 1}, .5, "scrn"
			if family == "IrSh" {
				set.Items[0].Distance, set.Items[0].Angle = 2, 90
			} else {
				set.Items[0].Size = 2
			}
			if err := editor.SetEffects(nil, 1, *set); err != nil {
				t.Fatal(err)
			}
			set.Items[0].Color = [3]float64{1, 0, 0}
			saved := compositeSave(t, editor, SaveOptions{Compression: 3})
			persisted, _ := effectSourceView(t, saved)
			if saved.Header.Version != version || persisted.Items[0].Color != [3]float64{0, 0, 1} ||
				persisted.Items[0].Opacity != .5 || persisted.Items[0].BlendMode != "scrn" || len(persisted.Items) != 2 {
				t.Fatal("saved inner effect borrowed caller parameters or changed source structure")
			}
			for _, block := range saved.Layers[0].AdditionalInfo {
				if block.Key == "zzzz" {
					data, err := saved.ReadSpan(block.Data)
					if err != nil || !bytes.Equal(data, []byte{0x19, 0x27, 0x35, 0x41}) {
						t.Fatalf("inner effect edit changed unknown source block: %v", err)
					}
				}
			}
			after, err := editor.source.ReadSpan(editor.source.Source)
			if err != nil || !bytes.Equal(original, after) {
				t.Fatalf("inner effect edit modified original source bytes: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := editor.SetEffects(ctx, 1, *persisted); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled inner effect edit was accepted: %v", err)
			}
		}
	}
}

func TestLegacyInnerEffectLayoutsPatchTypedParametersAndRetainUnknownBytes(t *testing.T) {
	for _, kind := range []psdeffects.Kind{psdeffects.InnerShadow, psdeffects.InnerGlow} {
		for _, version := range []uint32{0, 2} {
			length, colorOffset, opacityOffset := 41, 20, 40
			if kind == psdeffects.InnerGlow {
				length, colorOffset, opacityOffset = 32, 12, 31
			}
			if version == 2 {
				length += 10
				if kind == psdeffects.InnerGlow {
					length++
				}
			}
			payload := make([]byte, length)
			binary.BigEndian.PutUint32(payload, version)
			copy(payload[colorOffset+10:], "8BIMnorm")
			effect := psdeffects.Effect{Color: [3]float64{0, 1, 0}, Opacity: .5, Size: 2,
				Distance: 3, Angle: 90, BlendMode: "mul "}
			if err := patchLegacyEffect(payload, kind, effect, 30); err != nil {
				t.Fatal(err)
			}
			if binary.BigEndian.Uint32(payload[4:]) != 2<<16 || payload[opacityOffset] != 128 ||
				string(payload[colorOffset+14:colorOffset+18]) != "mul " ||
				binary.BigEndian.Uint16(payload[colorOffset+4:]) != 65535 {
				t.Fatal("legacy inner-effect layout lost editable RGB, opacity, size or blend mode")
			}
		}
	}
}
