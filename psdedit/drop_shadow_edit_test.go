package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func placedShadowEditFixture(t *testing.T, version uint16) []byte {
	t.Helper()
	shadowSource, err := psdfile.Parse(fractionalShadowEditFixture(t, version, false, false))
	if err != nil {
		t.Fatal(err)
	}
	defer shadowSource.Close()
	var tags []byte
	for _, block := range shadowSource.Layers[0].AdditionalInfo {
		if block.Key == "lfx2" || block.Key == "lmgm" || block.Key == "pRsv" {
			payload, err := shadowSource.ReadSpan(block.Data)
			if err != nil {
				t.Fatal(err)
			}
			tags = append(tags, compositeFixtureTag(block.Key, payload)...)
		}
	}
	tags = append(tags, writerLinkedTag("8BIM", "SoLd", 4, smartEditPlacement(t, 0, ""), 2)...)
	wanted := smartEditImage(smartEditColors())
	prototype := smartEditPrototype(version, wanted)
	linked := writerLinkedEntry("liFD", "fixture-id", 7, prototype)
	lengthWidth := 4
	if version == 2 {
		lengthWidth = 8
	}
	global := writerLinkedTag("8BIM", "lnk2", lengthWidth, linked.data, 4)
	stale := smartEditImage([4]color.NRGBA{{255, 0, 255, 255}, {255, 0, 255, 255},
		{255, 0, 255, 255}, {255, 0, 255, 255}})
	record, channels := smartEditLayer(version, "Placed shadow", 40, image.Rect(0, 0, 2, 2), stale, tags, true)
	info := binary.BigEndian.AppendUint16(nil, 0xffff)
	info = append(info, record...)
	info = append(info, channels...)
	if len(info)%2 != 0 {
		info = append(info, 0)
	}
	footer := append([]byte{0, 0}, bytes.Repeat([]byte{0xa5}, 16)...)
	return smartEditDocument(version, 2, 2, 4, info, global, footer)
}

func TestDropShadowEditPlacedOriginalContentAndAutomaticPreview(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		data := placedShadowEditFixture(t, version)
		editor := compositeEditor(t, data)
		id, set, index := maskedOverlaySourceSet(t, editor)
		set.Items[0].Distance, set.Items[0].Size = .25, .5
		if err := editor.SetEffectsWithOptions(nil, id, *set, EffectsEditOptions{ApproximateDropShadow: true}); err != nil {
			t.Fatal(err)
		}
		configuration := psdcompose.Options{ApproximateDropShadow: true}
		layer, err := psdscene.RenderPlacedLayer(nil, editor.source, index, psdscene.Options{Compose: configuration})
		if err != nil {
			t.Fatal(err)
		}
		layer.Effects = set
		wanted, err := psdcompose.Render(nil, &psdmodel.Document{Width: 2, Height: 2,
			Layers: []*psdmodel.Layer{layer}}, configuration)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := editor.Composite(nil, configuration)
		if err != nil || !bytes.Equal(actual.Pix, wanted.Pix) {
			t.Fatalf("live placed composition differs from original content: %v", err)
		}
		if bytes.Equal(actual.Pix, bytes.Repeat([]byte{255, 0, 255, 255}, 4)) {
			t.Fatal("placed preview used the poisoned parent cache")
		}
		var output bytes.Buffer
		if err := editor.WritePSD(nil, &output, SaveOptions{ApproximateDropShadow: true}); err != nil {
			t.Fatal(err)
		}
		saved, err := psdfile.Parse(output.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		defer saved.Close()
		for blockIndex, block := range editor.source.Layers[index].AdditionalInfo {
			if block.Key == "lfx2" {
				continue
			}
			before, beforeErr := editor.source.ReadSpan(block.Span)
			after, afterErr := saved.ReadSpan(saved.Layers[index].AdditionalInfo[blockIndex].Span)
			if beforeErr != nil || afterErr != nil || !bytes.Equal(before, after) {
				t.Fatal("effect edit changed placement or unrelated original metadata")
			}
		}
		preview, err := saved.DecodeComposite(nil)
		if err != nil {
			t.Fatal(err)
		}
		for position := range 4 {
			alpha := uint32(actual.Pix[position*4+3])
			if preview.Channels[3].Data[position] != byte(alpha) {
				t.Fatal("saved preview alpha differs from live placed composition")
			}
			for component := range 3 {
				value := uint32(actual.Pix[position*4+component])
				encoded := byte((value*alpha + 255*(255-alpha) + 127) / 255)
				if preview.Channels[component].Data[position] != encoded {
					t.Fatal("saved preview differs from the required white-matte RGB encoding")
				}
			}
		}
		reopened := compositeEditor(t, output.Bytes())
		painted, err := reopened.Composite(nil, configuration)
		if err != nil || !bytes.Equal(painted.Pix, actual.Pix) {
			t.Fatalf("reopened live placed preview differs: %v", err)
		}
		metadata, err := saved.ReadLayerMetadata(nil, index)
		if err != nil || metadata.Layer.Effects.Items[0].Distance != .25 ||
			metadata.Layer.Effects.Items[0].Size != .5 {
			t.Fatalf("saved effect parameters differ: %v", err)
		}
	}
}

func TestDropShadowPolicyKeepsOrdinaryAbsoluteCoordinatesAndMasks(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		pixels := smartEditImage(smartEditColors())
		bounds := image.Rect(3, 2, 5, 4)
		record, channels := smartEditLayer(version, "Ordinary absolute", 40, bounds, pixels, nil, true)
		info := binary.BigEndian.AppendUint16(nil, 0xffff)
		info = append(info, record...)
		info = append(info, channels...)
		if len(info)%2 != 0 {
			info = append(info, 0)
		}
		data := smartEditDocument(version, 6, 5, 4, info, nil,
			append([]byte{0, 0}, bytes.Repeat([]byte{0xa5}, 120)...))
		editor := compositeEditor(t, data)
		ordinary, err := editor.Composite(nil)
		if err != nil {
			t.Fatal(err)
		}
		selected, err := editor.Composite(nil, psdcompose.Options{ApproximateDropShadow: true})
		if err != nil || !bytes.Equal(selected.Pix, ordinary.Pix) {
			t.Fatalf("explicit policy changed an ordinary absolute-coordinate layer: %v", err)
		}
		if selected.NRGBAAt(3, 2).A != 255 || selected.NRGBAAt(4, 2).A != 128 ||
			selected.NRGBAAt(3, 3).A != 0 || selected.NRGBAAt(0, 0).A != 0 {
			t.Fatal("source channels or pixel mask were shifted during selective decoding")
		}
	}
}

func TestDropShadowLegacySpreadChangeIsRejectedWithoutMutation(t *testing.T) {
	data := effectLegacyFixture(true)
	original := bytes.Clone(data)
	set := psdeffects.Set{Items: []psdeffects.Effect{{Kind: psdeffects.DropShadow, Enabled: true,
		Spread: .04, BlendMode: "norm"}}}
	desired := set
	desired.Items = append([]psdeffects.Effect(nil), set.Items...)
	desired.Items[0].Spread = .07
	if _, err := patchLegacyEffects(context.Background(), data, set, desired); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("legacy spread cache remained stale: %v", err)
	}
	if !bytes.Equal(data, original) {
		t.Fatal("unsupported legacy spread change modified original cache bytes")
	}
}

func fractionalShadowEditFixture(t *testing.T, version uint16, nested, masked bool) []byte {
	t.Helper()
	unit := func(kind string, value float64) psdfile.DescriptorUnit {
		return psdfile.DescriptorUnit{Unit: kind, Width: 64, Number: value}
	}
	shadow := &psdfile.Descriptor{ClassID: "DrSh", Entries: []psdfile.DescriptorEntry{
		{Key: "enab", Type: "bool", Value: true},
		{Key: "Md  ", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "BlnM", ValueID: "Mltp"}},
		{Key: "Opct", Type: "UntF", Value: unit("#Prc", 10)},
		{Key: "blur", Type: "UntF", Value: unit("#Pxl", 2)},
		{Key: "Ckmt", Type: "UntF", Value: unit("#Pxl", 4)},
		{Key: "Dstn", Type: "UntF", Value: unit("#Pxl", 4)},
		{Key: "lagl", Type: "UntF", Value: unit("#Ang", 54)},
		{Key: "layerConceals", Type: "bool", Value: true},
		{Key: "Clr ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "RGBC", Entries: []psdfile.DescriptorEntry{
			{Key: "Rd  ", Type: "doub", Value: float64(41)},
			{Key: "Grn ", Type: "doub", Value: float64(77)},
			{Key: "Bl  ", Type: "doub", Value: float64(119)},
		}}},
	}}
	descriptor := &psdfile.Descriptor{ClassID: "Lefx", Entries: []psdfile.DescriptorEntry{
		{Key: "masterFXSwitch", Type: "bool", Value: true},
		{Key: "DrSh", Type: "Objc", Value: shadow},
	}}
	encoded, err := psdfile.EncodeDescriptor(nil, descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(make([]byte, 4), encoded...)
	payload = append(payload, make([]byte, (4-len(payload)%4)%4)...)
	tags := compositeFixtureTag("lfx2", payload)
	tags = append(tags, compositeFixtureTag("lmgm", []byte{0, 0, 0, 0})...)
	tags = append(tags, compositeFixtureTag("pRsv", []byte{0x12, 0x34, 0x56, 0x78})...)
	leaf := compositeFixtureLayer{color: []byte{180, 101, 39, 128}, blend: "mul ", tags: tags}
	if masked {
		leaf.mask = []byte{173}
	}
	layers := []compositeFixtureLayer{leaf}
	if nested {
		layers = []compositeFixtureLayer{{divider: 3}, leaf, {divider: 1, blend: "norm"}}
	}
	return compositeFixture(version, 8, 4, layers)
}

func TestDropShadowEditExplicitPolicyPSDPSBAndSourcePreservation(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, masked := range []bool{false, true} {
			for _, nested := range []bool{false, true} {
				data := fractionalShadowEditFixture(t, version, nested, masked)
				original := bytes.Clone(data)
				editor := compositeEditor(t, data)
				id, set, index := maskedOverlaySourceSet(t, editor)
				before, err := editor.source.ReadSpan(editor.source.Layers[index].MaskData)
				if err != nil {
					t.Fatal(err)
				}
				set.Items[0].Distance, set.Items[0].Size, set.Items[0].Spread = 5.25, 2.5, .07
				set.Items[0].Color = [3]float64{31.0 / 255, 89.0 / 255, 147.0 / 255}
				if err := editor.SetEffects(nil, id, *set); !errors.Is(err, ErrUnsupported) {
					t.Fatalf("default effects edit accepted an approximate shadow: %v", err)
				}
				changes, _ := editor.Changes()
				if changes.Dirty {
					t.Fatal("rejected default edit changed revision")
				}
				if err := editor.SetEffectsWithOptions(nil, id, *set, EffectsEditOptions{
					ApproximateDropShadow: true,
				}); err != nil {
					t.Fatal(err)
				}
				live, err := editor.Composite(nil, psdcompose.Options{ApproximateDropShadow: true})
				if err != nil {
					t.Fatal(err)
				}
				var output bytes.Buffer
				if err := editor.WritePSD(nil, &output, SaveOptions{ApproximateDropShadow: true}); err != nil {
					t.Fatal(err)
				}
				saved, err := psdfile.Parse(output.Bytes())
				if err != nil {
					t.Fatal(err)
				}
				defer saved.Close()
				if !bytes.Equal(data, original) || saved.Header.Version != version {
					t.Fatal("shadow edit changed original bytes or PSD/PSB version")
				}
				after, err := saved.ReadSpan(saved.Layers[index].MaskData)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("shadow edit changed pixel-mask representation")
				}
				opened, err := New(saved)
				if err != nil {
					t.Fatal(err)
				}
				defer opened.Close()
				_, actual, _ := maskedOverlaySourceSet(t, opened)
				if actual.Items[0].Spread != .07 || actual.Items[0].Distance != 5.25 || actual.Items[0].Size != 2.5 {
					t.Fatal("saved shadow lost fractional geometry")
				}
				pixels, err := opened.Composite(nil, psdcompose.Options{ApproximateDropShadow: true})
				if err != nil || !bytes.Equal(live.Pix, pixels.Pix) {
					t.Fatalf("saved shadow preview and reopened tree differ: %v", err)
				}
			}
		}
	}
}

func TestDropShadowEditCancellationAndUnrelatedSemanticsAreAtomic(t *testing.T) {
	editor := compositeEditor(t, fractionalShadowEditFixture(t, 1, true, true))
	id, set, _ := maskedOverlaySourceSet(t, editor)
	set.Items[0].Spread = .08
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	option := EffectsEditOptions{ApproximateDropShadow: true}
	if err := editor.SetEffectsWithOptions(ctx, id, *set, option); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	set.Items[0].Noise = .05
	if err := editor.SetEffectsWithOptions(nil, id, *set, option); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("noise became editable through unrelated approximation: %v", err)
	}
	changes, _ := editor.Changes()
	if changes.Dirty {
		t.Fatal("failed shadow edit changed source state")
	}
	if effectsPureDropShadow(psdeffects.Set{}) {
		t.Fatal("empty effect set qualified as a shadow")
	}
}
