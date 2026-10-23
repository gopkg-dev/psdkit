package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func mixedMaskEffectObject(kind string) *psdfile.Descriptor {
	unit := func(name string, value float64) psdfile.DescriptorUnit {
		return psdfile.DescriptorUnit{Unit: name, Width: 64, Number: value}
	}
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
			psdfile.DescriptorEntry{Key: "Dstn", Type: "UntF", Value: unit("#Pxl", 3)},
			psdfile.DescriptorEntry{Key: "lagl", Type: "UntF", Value: unit("#Ang", 0)},
			psdfile.DescriptorEntry{Key: "blur", Type: "UntF", Value: unit("#Pxl", 0)},
			psdfile.DescriptorEntry{Key: "Ckmt", Type: "UntF", Value: unit("#Pxl", 0)},
			psdfile.DescriptorEntry{Key: "layerConceals", Type: "bool", Value: true})
	}
	return object
}

func mixedMaskEffectsFixture(t *testing.T, version uint16, multiple bool, extraTags ...[]byte) []byte {
	t.Helper()
	descriptor := &psdfile.Descriptor{ClassID: "Lefx", Entries: []psdfile.DescriptorEntry{
		{Key: "masterFXSwitch", Type: "bool", Value: true},
	}}
	for _, kind := range []string{"DrSh", "SoFi"} {
		if multiple {
			descriptor.Entries = append(descriptor.Entries, psdfile.DescriptorEntry{
				Key: kind + "Multi", Type: "VlLs", Value: []psdfile.DescriptorValue{
					{Type: "Objc", Value: mixedMaskEffectObject(kind)},
					{Type: "Objc", Value: mixedMaskEffectObject(kind)},
				},
			})
			continue
		}
		descriptor.Entries = append(descriptor.Entries,
			psdfile.DescriptorEntry{Key: kind, Type: "Objc", Value: mixedMaskEffectObject(kind)})
	}
	descriptor.Entries = append(descriptor.Entries, psdfile.DescriptorEntry{
		Key: "futureInactive", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "futureFx",
			Entries: []psdfile.DescriptorEntry{
				{Key: "enab", Type: "bool", Value: false},
				{Key: "opaque", Type: "tdta", Value: []byte{0x91, 0x37, 0xa5}},
				{Key: "opaque", Type: "long", Value: int32(39)},
			}},
	})
	encoded, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(make([]byte, 4), encoded...)
	payload = append(payload, make([]byte, (4-len(payload)%4)%4)...)
	legacy := colorOverlayLegacy()
	if !multiple {
		// One legacy record represents each family; append only the independent shadow record.
		shadow := effectLegacyFixture(true)
		legacy = append(legacy, shadow[21:]...)
		binary.BigEndian.PutUint16(legacy[2:], 3)
	}
	tags := compositeFixtureTag("lfx2", payload)
	tags = append(tags, compositeFixtureTag("lrFX", legacy)...)
	tags = append(tags, compositeFixtureTag("lmgm", []byte{0, 0, 0, 0})...)
	tags = append(tags, compositeFixtureTag("pRsv", []byte{0x31, 0x72, 0xa4})...)
	for _, extra := range extraTags {
		tags = append(tags, extra...)
	}
	pixels := smartEditImage(smartEditColors())
	record, channels := smartEditLayer(version, "Masked mixed effects", 93, image.Rect(3, 2, 5, 4), pixels, tags, true)
	info := binary.BigEndian.AppendUint16(nil, 0xffff)
	info = append(info, record...)
	info = append(info, channels...)
	if len(info)%2 != 0 {
		info = append(info, 0)
	}
	return smartEditDocument(version, 8, 6, 4, info, nil,
		append([]byte{0, 0}, bytes.Repeat([]byte{0xa5}, 8*6*4)...))
}

func mixedMaskEffectsDesired(set *psdeffects.Set) {
	shadowIndex, overlayIndex := 0, 0
	for index := range set.Items {
		effect := &set.Items[index]
		switch effect.Kind {
		case psdeffects.DropShadow:
			effect.Color, effect.Opacity = [3]float64{0, 0, 0}, .5
			effect.Distance, effect.Size = 1.25+float64(shadowIndex), .5
			shadowIndex++
		case psdeffects.ColorOverlay:
			effect.Color, effect.Opacity = [3]float64{0, 0, 1}, .5
			if overlayIndex != 0 {
				effect.Color = [3]float64{0, 1, 0}
			}
			overlayIndex++
		}
	}
}

func mixedMaskEffectsPreserved(t *testing.T, original, saved *psdfile.Document) {
	t.Helper()
	before, after := original.Layers[0], saved.Layers[0]
	oldMask, err := original.ReadSpan(before.MaskData)
	if err != nil {
		t.Fatal(err)
	}
	newMask, err := saved.ReadSpan(after.MaskData)
	if err != nil || !bytes.Equal(newMask, oldMask) {
		t.Fatalf("mixed effect edit changed original mask metadata: %v", err)
	}
	if len(before.Channels) != len(after.Channels) || len(before.AdditionalInfo) != len(after.AdditionalInfo) {
		t.Fatal("mixed effect edit changed source channels or unrelated tagged-block count")
	}
	for index, channel := range before.Channels {
		left, err := original.ReadSpan(channel.Span)
		if err != nil {
			t.Fatal(err)
		}
		right, err := saved.ReadSpan(after.Channels[index].Span)
		if err != nil || channel.ID != after.Channels[index].ID || !bytes.Equal(left, right) {
			t.Fatalf("mixed effect edit changed original channel %d: %v", channel.ID, err)
		}
	}
	for index, block := range before.AdditionalInfo {
		if block.Key == "lfx2" || block.Key == "lrFX" {
			continue
		}
		left, err := original.ReadSpan(block.Span)
		if err != nil {
			t.Fatal(err)
		}
		right, err := saved.ReadSpan(after.AdditionalInfo[index].Span)
		if err != nil || !bytes.Equal(left, right) {
			t.Fatalf("mixed effect edit changed unrelated %s block: %v", block.Key, err)
		}
	}
	_, leftBlock := effectSourceView(t, original)
	_, rightBlock := effectSourceView(t, saved)
	readUnknown := func(source *psdfile.Document, block psdfile.AdditionalBlock) []byte {
		descriptor, err := source.DecodeDescriptorContext(t.Context(), psdfile.Span{
			Offset: block.Data.Offset + 4, Length: block.Data.Length - 4,
		}, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range descriptor.Entries {
			if entry.Key == "futureInactive" {
				data, err := source.ReadSpan(entry.Span)
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
		}
		t.Fatal("independent inactive unknown effect disappeared")
		return nil
	}
	if !bytes.Equal(readUnknown(original, leftBlock), readUnknown(saved, rightBlock)) {
		t.Fatal("mixed effect edit rewrote inactive unknown descriptor entries")
	}
}

func mixedMaskEffectsExpectSaved(t *testing.T, editor *Editor, saved *psdfile.Document, desired psdeffects.Set) *Editor {
	t.Helper()
	options := psdcompose.Options{ApproximateDropShadow: true}
	live, err := editor.Composite(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	reopened := lifecycleReopen(t, saved)
	actual, err := reopened.Composite(t.Context(), options)
	if err != nil || !bytes.Equal(actual.Pix, live.Pix) {
		t.Fatalf("mixed effects preview changed after save and reopen: %v", err)
	}
	persisted, _ := effectSourceView(t, saved)
	if !effectsParametersEqual(*persisted, desired) {
		t.Fatal("saved mixed effect parameters, order or fixed semantics differ")
	}
	mixedMaskEffectsLegacy(t, saved, desired)
	merged, err := saved.DecodeComposite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for position := range 8 * 6 {
		alpha := uint32(live.Pix[position*4+3])
		if merged.Channels[3].Data[position] != byte(alpha) {
			t.Fatal("saved composite retained stale mixed-effects alpha")
		}
		for channel := range 3 {
			value := uint32(live.Pix[position*4+channel])
			want := byte((value*alpha + 255*(255-alpha) + 127) / 255)
			if merged.Channels[channel].Data[position] != want {
				t.Fatal("saved composite RGB differs from live mixed effects")
			}
		}
	}
	return reopened
}

func mixedMaskEffectsLegacy(t *testing.T, saved *psdfile.Document, desired psdeffects.Set) {
	t.Helper()
	for _, block := range saved.Layers[0].AdditionalInfo {
		if block.Key != "lrFX" {
			continue
		}
		data, err := saved.ReadSpan(block.Data)
		if err != nil {
			t.Fatal(err)
		}
		unknown := effectLegacyFixture(false)
		if len(data) < 21 || !bytes.Equal(data[4:21], unknown[4:]) {
			t.Fatal("mixed edit changed unknown legacy effect record")
		}
		position := 4
		for range binary.BigEndian.Uint16(data[2:]) {
			key := string(data[position+4 : position+8])
			length := int(binary.BigEndian.Uint32(data[position+8:]))
			payload := data[position+12 : position+12+length]
			position += 12 + length
			if key != "sofi" && key != "dsdw" {
				continue
			}
			kind := psdeffects.ColorOverlay
			if key == "dsdw" {
				kind = psdeffects.DropShadow
			}
			for _, effect := range desired.Items {
				if effect.Kind != kind {
					continue
				}
				colorOffset, opacityOffset, modeOffset := 12, 22, 8
				if kind == psdeffects.DropShadow {
					colorOffset, opacityOffset, modeOffset = 20, 40, 34
					if float64(int32(binary.BigEndian.Uint32(payload[4:])))/65536 != effect.Size ||
						float64(int32(binary.BigEndian.Uint32(payload[16:])))/65536 != effect.Distance {
						t.Fatal("legacy shadow cache retained stale distance or radius")
					}
				}
				if payload[opacityOffset] != byte(math.Round(effect.Opacity*255)) ||
					string(payload[modeOffset:modeOffset+4]) != effect.BlendMode {
					t.Fatal("legacy mixed effect retained stale opacity or blend mode")
				}
				for channel, value := range effect.Color {
					if binary.BigEndian.Uint16(payload[colorOffset+2+channel*2:]) != uint16(math.Round(value*65535)) {
						t.Fatal("legacy mixed effect retained stale color")
					}
				}
				break
			}
		}
		return
	}
	t.Fatal("independent legacy effect fixture disappeared")
}

func TestMixedEffectsPixelMaskPSDPSBAllCompressionsAndContinuedEditing(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, multiple := range []bool{false, true} {
			for compression := range uint16(4) {
				t.Run(fmt.Sprintf("version%d_multiple%t_compression%d", version, multiple, compression), func(t *testing.T) {
					data := mixedMaskEffectsFixture(t, version, multiple)
					original := bytes.Clone(data)
					editor := compositeEditor(t, data)
					id, set, _ := maskedOverlaySourceSet(t, editor)
					mixedMaskEffectsDesired(set)
					if err := editor.SetEffects(nil, id, *set); !errors.Is(err, ErrUnsupported) {
						t.Fatalf("strict editing silently accepted mixed approximate effects: %v", err)
					}
					if editor.revision != 0 {
						t.Fatal("strict rejection partially committed")
					}
					option := EffectsEditOptions{ApproximateDropShadow: true}
					if err := editor.SetEffectsWithOptions(nil, id, *set, option); err != nil {
						t.Fatal(err)
					}
					desired := *set
					desired.Items = append([]psdeffects.Effect(nil), set.Items...)
					set.Items[0].Color, set.Items[0].Opacity = [3]float64{1, 1, 1}, 0
					live, err := editor.Composite(nil, psdcompose.Options{ApproximateDropShadow: true})
					if err != nil {
						t.Fatal(err)
					}
					want := color.NRGBA{R: 5, G: 10, B: 143, A: 255}
					if multiple {
						want = color.NRGBA{R: 3, G: 69, B: 135, A: 255}
					}
					actual := live.NRGBAAt(3, 2)
					if math.Abs(float64(actual.R)-float64(want.R)) > 1 ||
						math.Abs(float64(actual.G)-float64(want.G)) > 1 ||
						math.Abs(float64(actual.B)-float64(want.B)) > 1 || actual.A != want.A {
						t.Fatalf("independent full-coverage overlay sample = %v, want %v", actual, want)
					}
					shadowPixels := 0
					for y := range 6 {
						for x := range 8 {
							if !image.Pt(x, y).In(image.Rect(3, 2, 5, 4)) && live.NRGBAAt(x, y).A > 0 {
								shadowPixels++
							}
						}
					}
					if shadowPixels == 0 {
						t.Fatal("mixed fixture did not render any shadow outside the source rectangle")
					}
					var rejected bytes.Buffer
					if err := editor.Write(nil, &rejected); !errors.Is(err, ErrUnsupported) || rejected.Len() != 0 {
						t.Fatalf("strict save wrote an approximate mixed preview: %v", err)
					}
					saveOptions := SaveOptions{Compression: compression, ApproximateDropShadow: true}
					saved := compositeSave(t, editor, saveOptions)
					reopened := mixedMaskEffectsExpectSaved(t, editor, saved, desired)
					mixedMaskEffectsPreserved(t, editor.source, saved)
					if !bytes.Equal(data, original) || saved.Header.Version != version {
						t.Fatal("mixed edit mutated source bytes or PSD/PSB version")
					}
					nextID, next, _ := maskedOverlaySourceSet(t, reopened)
					for index := range next.Items {
						switch next.Items[index].Kind {
						case psdeffects.ColorOverlay:
							next.Items[index].Opacity = .25
						case psdeffects.DropShadow:
							next.Items[index].Distance += .25
						}
					}
					if err := reopened.SetEffectsWithOptions(nil, nextID, *next, option); err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(t.TempDir(), "mixed-effects.psd")
					if err := reopened.Save(nil, path, saveOptions); err != nil {
						t.Fatal(err)
					}
					resaved, err := psdfile.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = resaved.Close() })
					mixedMaskEffectsExpectSaved(t, reopened, resaved, *next)
					mixedMaskEffectsPreserved(t, editor.source, resaved)
				})
			}
		}
	}
}

func TestMixedEffectsPixelMaskInvalidCanceledAndBudgetAreAtomic(t *testing.T) {
	for _, scenario := range []string{"cancel", "opacity", "family", "noise", "budget"} {
		t.Run(scenario, func(t *testing.T) {
			editor := compositeEditor(t, mixedMaskEffectsFixture(t, 1, true))
			id, desired, _ := maskedOverlaySourceSet(t, editor)
			mixedMaskEffectsDesired(desired)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := ErrUnsupported
			switch scenario {
			case "cancel":
				cancel()
				want = context.Canceled
			case "opacity":
				desired.Items[0].Opacity = math.NaN()
				want = ErrInvalidEdit
			case "family":
				desired.Items[0].Kind = psdeffects.OuterGlow
			case "noise":
				desired.Items[0].Noise = .2
			case "budget":
				editor.roots[0].setSemanticCost("other", editor.source.Limits().MaxDecodeBytes-1024)
				want = psdfile.ErrLimit
			}
			node := editor.roots[0]
			before, _ := editor.Changes()
			retained := editor.ownedBytesLocked(nil)
			err := editor.SetEffectsWithOptions(ctx, id, *desired, EffectsEditOptions{ApproximateDropShadow: true})
			if !errors.Is(err, want) {
				t.Fatalf("invalid mixed effect edit = %v, want %v", err, want)
			}
			after, _ := editor.Changes()
			if !reflect.DeepEqual(before, after) || editor.ownedBytesLocked(nil) != retained ||
				node.effects != nil || len(node.additional) != 0 {
				t.Fatal("failed mixed effect edit changed overlays or retained budget")
			}
		})
	}
}

func TestMixedEffectsPixelMaskAdmitsVectorButRetainsNonNormalAndPlacedRefusals(t *testing.T) {
	for _, scenario := range []string{"source-multiply", "edited-multiply", "source-vector", "added-vector", "placed"} {
		t.Run(scenario, func(t *testing.T) {
			var extra []byte
			switch scenario {
			case "source-vector":
				extra = compositeFixtureTag("vmsk", editVectorPayload())
			case "placed":
				extra = compositeFixtureTag("SoLd", smartEditPlacement(t, 0, ""))
			}
			data := mixedMaskEffectsFixture(t, 1, false, extra)
			if scenario == "source-multiply" {
				data = bytes.Replace(data, []byte("8BIMnorm"), []byte("8BIMmul "), 1)
			}
			editor := compositeEditor(t, data)
			id, desired, _ := maskedOverlaySourceSet(t, editor)
			mixedMaskEffectsDesired(desired)
			switch scenario {
			case "edited-multiply":
				if err := editor.SetBlendMode(id, "mul "); err != nil {
					t.Fatal(err)
				}
			case "added-vector":
				mask := lifecycleVectorMask(t)
				mask.Width, mask.Height = 8, 6
				if err := editor.AddVectorMask(nil, id, mask); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := editor.Changes()
			retained := editor.ownedBytesLocked(nil)
			err := editor.SetEffectsWithOptions(nil, id, *desired, EffectsEditOptions{ApproximateDropShadow: true})
			if scenario == "source-vector" || scenario == "added-vector" {
				if err != nil {
					t.Fatalf("known unfeathered vector/dual mask did not qualify for explicit shadow/overlay editing: %v", err)
				}
				if after, _ := editor.Changes(); after.Revision != before.Revision+1 || editor.roots[0].effects == nil {
					t.Fatal("accepted vector/dual-mask edit did not commit one effect revision")
				}
				if _, err := editor.Composite(nil, psdcompose.Options{ApproximateDropShadow: true}); err != nil {
					t.Fatalf("accepted vector-masked effect edit cannot rerender: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("uncalibrated mixed source was accepted: %v", err)
			}
			after, _ := editor.Changes()
			if !reflect.DeepEqual(before, after) || retained != editor.ownedBytesLocked(nil) || editor.roots[0].effects != nil {
				t.Fatal("rejected mixed source partially changed effect state")
			}
		})
	}
}
