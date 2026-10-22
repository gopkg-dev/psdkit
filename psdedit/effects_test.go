package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func effectEditPayload(t *testing.T, shadow bool) []byte {
	t.Helper()
	unit := func(unit string, value float64) psdfile.DescriptorUnit {
		return psdfile.DescriptorUnit{Unit: unit, Width: 64, Number: value}
	}
	family, opacity := "FrFX", float64(100)
	if shadow {
		family, opacity = "DrSh", 75
	}
	effect := &psdfile.Descriptor{ClassID: family, Entries: []psdfile.DescriptorEntry{
		{Key: "enab", Type: "bool", Value: true},
		{Key: "Opct", Type: "UntF", Value: unit("#Prc", opacity)},
		{Key: "Clr ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "RGBC", Entries: []psdfile.DescriptorEntry{
			{Key: "Rd  ", Type: "doub", Value: float64(0)},
			{Key: "Grn ", Type: "doub", Value: float64(255)},
			{Key: "Bl  ", Type: "doub", Value: float64(0)},
		}}},
	}}
	if shadow {
		effect.Entries = append(effect.Entries,
			psdfile.DescriptorEntry{Key: "Dstn", Type: "UntF", Value: unit("#Pxl", 3)},
			psdfile.DescriptorEntry{Key: "lagl", Type: "UntF", Value: unit("#Ang", 0)},
			psdfile.DescriptorEntry{Key: "blur", Type: "UntF", Value: unit("#Pxl", 0)})
	} else {
		effect.Entries = append(effect.Entries,
			psdfile.DescriptorEntry{Key: "Sz  ", Type: "UntF", Value: unit("#Pxl", 2)},
			psdfile.DescriptorEntry{Key: "Styl", Type: "enum",
				Value: psdfile.DescriptorEnum{TypeID: "FStl", ValueID: "InsF"}})
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

func effectLegacyFixture(shadow bool) []byte {
	count := uint16(1)
	if shadow {
		count++
	}
	data := binary.BigEndian.AppendUint16(nil, 0)
	data = binary.BigEndian.AppendUint16(data, count)
	data = append(data, "8BIMzzzz"...)
	data = binary.BigEndian.AppendUint32(data, 5)
	data = append(data, 0x88, 0x99, 0xa1, 0xb2, 0xc3)
	if shadow {
		data = append(data, "8BIMdsdw"...)
		data = binary.BigEndian.AppendUint32(data, 51)
		payload := make([]byte, 51)
		binary.BigEndian.PutUint32(payload, 2)
		binary.BigEndian.PutUint32(payload[16:], 3<<16)
		binary.BigEndian.PutUint16(payload[24:], 65535)
		copy(payload[30:], "8BIMnorm")
		payload[38], payload[40] = 1, 191
		binary.BigEndian.PutUint16(payload[45:], 65535)
		data = append(data, payload...)
	}
	return data
}

func effectEditorFixture(
	t *testing.T, version uint16, key string, shadow bool, options ...psdfile.LoadOptions,
) *Editor {
	t.Helper()
	tags := compositeFixtureTag(key, effectEditPayload(t, shadow))
	tags = append(tags, compositeFixtureTag("lrFX", effectLegacyFixture(shadow))...)
	tags = append(tags, compositeFixtureTag("zzzz", []byte{0x19, 0x27, 0x35, 0x41})...)
	return compositeEditor(t, compositeFixture(version, 8, 3, []compositeFixtureLayer{
		{color: []byte{255, 0, 0, 255}, tags: tags},
	}), options...)
}

func effectSourceView(t *testing.T, source *psdfile.Document) (*psdeffects.Set, psdfile.AdditionalBlock) {
	t.Helper()
	for _, block := range source.Layers[0].AdditionalInfo {
		if block.Key == "lfx2" || block.Key == "lmfx" {
			set, err := source.DecodeEffects(nil, block)
			if err != nil {
				t.Fatal(err)
			}
			return set, block
		}
	}
	t.Fatal("fixture has no modern effect block")
	return nil, psdfile.AdditionalBlock{}
}

func TestSetEffectsOwnsTypedPreviewAndPreservesUnknownPSDPSBData(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, key := range []string{"lfx2", "lmfx"} {
			editor := effectEditorFixture(t, version, key, false)
			original, err := editor.source.ReadSpan(editor.source.Source)
			if err != nil {
				t.Fatal(err)
			}
			set, block := effectSourceView(t, editor.source)
			if err := editor.SetEffects(nil, 1, *set); err != nil || editor.revision != 0 {
				t.Fatalf("unchanged source effects dirtied the editor: %v", err)
			}
			editor.roots[0].setSemanticCost("other", 123)
			set.Items[0].Color, set.Items[0].Opacity = [3]float64{0, 0, 1}, 0.5
			if err := editor.SetEffects(nil, 1, *set); err != nil {
				t.Fatal(err)
			}
			if editor.roots[0].semanticCosts["other"] != 123 || editor.roots[0].semanticCosts["effects"] == 0 {
				t.Fatal("effect metadata overwrote another retained semantic reservation")
			}
			set.Items[0].Color = [3]float64{1, 0, 0}
			set.Descriptor.Entries[0].Value = false
			preview, err := editor.Composite(nil)
			if err != nil || !bytes.Equal(preview.Pix, []byte{0, 0, 255, 128}) {
				t.Fatalf("owned effect preview borrowed caller data or lost body knockout: %v, %v", preview, err)
			}
			saved := compositeSave(t, editor, SaveOptions{Compression: 3})
			compositeExpect(t, saved, []byte{127, 127, 255, 128})
			persisted, persistedBlock := effectSourceView(t, saved)
			if saved.Header.Version != version || persisted.Items[0].Color != [3]float64{0, 0, 1} ||
				persisted.Items[0].Opacity != 0.5 || len(persisted.Items) != 2 {
				t.Fatal("saved effect parameters, ordered family count or PSD/PSB version changed")
			}
			beforeDesc, err := editor.source.DecodeDescriptorContext(nil,
				psdfile.Span{Offset: block.Data.Offset + 4, Length: block.Data.Length - 4}, true)
			if err != nil {
				t.Fatal(err)
			}
			afterDesc, err := saved.DecodeDescriptorContext(nil,
				psdfile.Span{Offset: persistedBlock.Data.Offset + 4, Length: persistedBlock.Data.Length - 4}, true)
			if err != nil {
				t.Fatal(err)
			}
			before, err := editor.source.ReadSpan(beforeDesc.Entries[2].Span)
			if err != nil {
				t.Fatal(err)
			}
			after, err := saved.ReadSpan(afterDesc.Entries[2].Span)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("inactive unknown family, duplicate keys or lazy blob bytes changed: %v", err)
			}
			for _, tag := range []string{"lrFX", "zzzz"} {
				var left, right []byte
				for _, candidate := range editor.source.Layers[0].AdditionalInfo {
					if candidate.Key == tag {
						left, err = editor.source.ReadSpan(candidate.Span)
					}
				}
				for _, candidate := range saved.Layers[0].AdditionalInfo {
					if candidate.Key == tag {
						right, err = saved.ReadSpan(candidate.Span)
					}
				}
				if err != nil || !bytes.Equal(left, right) {
					t.Fatalf("unrelated legacy/unknown layer block %s changed: %v", tag, err)
				}
			}
			current, err := editor.source.ReadSpan(editor.source.Source)
			if err != nil || !bytes.Equal(original, current) {
				t.Fatalf("editing changed original source bytes: %v", err)
			}
		}
	}
}

func TestSetEffectsSynchronizesKnownLegacyFieldsAndKeepsUnknownRecordBytes(t *testing.T) {
	editor := effectEditorFixture(t, 1, "lfx2", true)
	set, _ := effectSourceView(t, editor.source)
	original := *set
	original.Items = append([]psdeffects.Effect(nil), set.Items...)
	set.Items[0].Color, set.Items[0].Distance, set.Items[0].Opacity = [3]float64{0, 0, 1}, 5, 0.5
	if err := editor.SetEffects(nil, 1, *set); err != nil {
		t.Fatal(err)
	}
	saved := compositeSave(t, editor, SaveOptions{Compression: 3})
	for _, block := range saved.Layers[0].AdditionalInfo {
		if block.Key != "lrFX" {
			continue
		}
		data, err := saved.ReadSpan(block.Data)
		if err != nil {
			t.Fatal(err)
		}
		before := effectLegacyFixture(true)
		if !bytes.Equal(data[:21], before[:21]) {
			t.Fatal("unknown legacy record bytes changed while updating the known shadow cache")
		}
		payload := data[33:]
		if binary.BigEndian.Uint32(payload[16:20]) != 5<<16 || payload[40] != 128 ||
			binary.BigEndian.Uint16(payload[24:26]) != 0 || binary.BigEndian.Uint16(payload[26:28]) != 65535 ||
			binary.BigEndian.Uint16(payload[45:47]) != 0 || binary.BigEndian.Uint16(payload[47:49]) != 65535 {
			t.Fatal("legacy fixed distance, opacity or RGB/native RGB fields contradict the modern effect")
		}
	}
	if err := editor.SetEffects(nil, 1, original); err != nil {
		t.Fatal(err)
	}
	reverted := compositeSave(t, editor, SaveOptions{Compression: 3})
	for _, block := range reverted.Layers[0].AdditionalInfo {
		if block.Key != "lrFX" {
			continue
		}
		data, err := reverted.ReadSpan(block.Data)
		if err != nil || !bytes.Equal(data, effectLegacyFixture(true)) {
			t.Fatalf("reverting modern parameters retained a contradictory edited legacy cache: %v", err)
		}
	}
}

func TestSetEffectsRejectsFixedSemanticsInvalidValuesAndCancellationWithoutMutation(t *testing.T) {
	for _, change := range []func(*psdeffects.Set){
		func(set *psdeffects.Set) { set.GlobalAngle++ },
		func(set *psdeffects.Set) { set.EffectsVisible = false },
		func(set *psdeffects.Set) { set.Items = set.Items[:1] },
		func(set *psdeffects.Set) { set.Items[0].Position = psdeffects.Center },
		func(set *psdeffects.Set) { set.Items[0].Size = 3 },
		func(set *psdeffects.Set) { set.Items[0].Noise = 0.1 },
		func(set *psdeffects.Set) { set.Items[0].Kind = psdeffects.OuterGlow },
	} {
		editor := effectEditorFixture(t, 1, "lfx2", false)
		set, _ := effectSourceView(t, editor.source)
		change(set)
		if err := editor.SetEffects(nil, 1, *set); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("fixed/unverified effect parameter was changed: %v", err)
		}
		if editor.revision != 0 || editor.roots[0].effects != nil || len(editor.roots[0].additional) != 0 {
			t.Fatal("rejected effect edit left a partial overlay")
		}
	}
	editor := effectEditorFixture(t, 1, "lfx2", false)
	set, _ := effectSourceView(t, editor.source)
	set.Items[0].Opacity = math.NaN()
	if err := editor.SetEffects(nil, 1, *set); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("nonfinite editable opacity should be invalid input: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.SetEffects(ctx, 1, *set); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled effect edit: %v", err)
	}
	if editor.revision != 0 || editor.roots[0].effects != nil {
		t.Fatal("invalid input or cancellation changed editor state")
	}
}

func TestSetEffectsPeakBudgetRetainsPreviousOverlayUntilCommit(t *testing.T) {
	templateEditor := effectEditorFixture(t, 1, "lfx2", false)
	template, _ := effectSourceView(t, templateEditor.source)
	trial := func(budget int64) (*Editor, *psdeffects.Set, error) {
		editor := effectEditorFixture(t, 1, "lfx2", false,
			psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: budget}})
		set := *template
		set.Items = append([]psdeffects.Effect(nil), template.Items...)
		set.Items[0].Color, set.Items[0].Opacity = [3]float64{0, 0, 1}, 0.5
		return editor, &set, editor.SetEffects(nil, 1, set)
	}
	lower, upper := int64(1000), int64(1<<20)
	for upper-lower > 1 {
		middle := (upper + lower) / 2
		_, _, err := trial(middle)
		if err == nil {
			upper = middle
			continue
		}
		if !errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("budget search found a non-limit effect error: %v", err)
		}
		lower = middle
	}
	editor, set, err := trial(upper)
	if err != nil {
		t.Fatal(err)
	}
	node := editor.roots[0]
	previous, costBefore := node.effects, node.semanticBytes
	retained := make(map[int64][]byte, len(node.additional))
	for offset, value := range node.additional {
		retained[offset] = bytes.Clone(value)
	}
	set.Items[0].Color = [3]float64{1, 0, 1}
	if err := editor.SetEffects(nil, 1, *set); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("equally sized replacement should retain the old overlay during its peak: %v", err)
	}
	if node.effects != previous || node.semanticBytes != costBefore || editor.revision != 1 ||
		!reflect.DeepEqual(node.additional, retained) {
		t.Fatal("failed replacement budget changed the previous descriptor, semantic view, cost or revision")
	}
	if int64(len(retained)) == 0 || editor.ownedBytesLocked(nil) >= upper {
		t.Fatal("test must fail on temporary peak while the retained final overlay itself still fits")
	}
}
