package psdparse

import (
	"fmt"
	"math"
	"slices"
	"testing"
)

type semanticsDescriptorEntry struct {
	key, kind string
	data      []byte
}

func semanticsClassID(w *psbWriter, value string) {
	if len(value) == 4 {
		w.u32(0)
	} else {
		w.u32(uint32(len(value)))
	}
	w.WriteString(value)
}

func semanticsDescriptor(entries ...semanticsDescriptorEntry) []byte {
	w := &psbWriter{}
	w.u32(0)
	semanticsClassID(w, "null")
	w.u32(uint32(len(entries)))
	for _, entry := range entries {
		semanticsClassID(w, entry.key)
		w.WriteString(entry.kind)
		w.Write(entry.data)
	}
	return w.Bytes()
}

func semanticsDescriptorList(items ...[]byte) []byte {
	w := &psbWriter{}
	w.u32(uint32(len(items)))
	for _, item := range items {
		w.WriteString("Objc")
		w.Write(item)
	}
	return w.Bytes()
}

func semanticsEffectsBlock(key string, entries ...semanticsDescriptorEntry) semanticsBlock {
	w := &psbWriter{}
	w.u32(0)
	w.u32(16)
	w.Write(semanticsDescriptor(entries...))
	return semanticsBlock{key: key, data: w.Bytes()}
}

func semanticsEffect(enabled bool) []byte {
	flag := byte(0)
	if enabled {
		flag = 1
	}
	return semanticsDescriptor(
		semanticsDescriptorEntry{"enab", "bool", []byte{flag}},
		semanticsDescriptorEntry{"present", "bool", []byte{flag}},
		semanticsDescriptorEntry{"showInDialog", "bool", []byte{1}},
	)
}

func semanticsEffectScale() semanticsDescriptorEntry {
	w := &psbWriter{}
	w.WriteString("#Prc")
	w.u64(math.Float64bits(100))
	return semanticsDescriptorEntry{"Scl ", "UntF", w.Bytes()}
}

func TestObjectEffectsActualEnablement(t *testing.T) {
	disabledEffect := semanticsEffect(false)
	activeEffect := semanticsEffect(true)
	masterOn := semanticsDescriptorEntry{"masterFXSwitch", "bool", []byte{1}}
	masterOff := semanticsDescriptorEntry{"masterFXSwitch", "bool", []byte{0}}
	cases := []struct {
		name    string
		entries []semanticsDescriptorEntry
		active  bool
	}{
		{"empty", nil, false},
		{"master_only", []semanticsDescriptorEntry{masterOn}, false},
		{"master_scale_only", []semanticsDescriptorEntry{masterOn, semanticsEffectScale()}, false},
		{"known_disabled", []semanticsDescriptorEntry{masterOn, {"DrSh", "Objc", disabledEffect}}, false},
		{"known_active", []semanticsDescriptorEntry{masterOn, {"DrSh", "Objc", activeEffect}}, true},
		{"active_default_master", []semanticsDescriptorEntry{{"OrGl", "Objc", activeEffect}}, true},
		{"disabled_master", []semanticsDescriptorEntry{masterOff, {"DrSh", "Objc", activeEffect}}, false},
		{"multi_all_disabled", []semanticsDescriptorEntry{
			masterOn, {"dropShadowMulti", "VlLs", semanticsDescriptorList(disabledEffect, disabledEffect)},
		}, false},
		{"multi_some_active", []semanticsDescriptorEntry{
			masterOn, {"dropShadowMulti", "VlLs", semanticsDescriptorList(disabledEffect, activeEffect)},
		}, true},
		{"empty_multi", []semanticsDescriptorEntry{
			masterOn, {"solidFillMulti", "VlLs", semanticsDescriptorList()},
		}, false},
		{"zero_modifying", []semanticsDescriptorEntry{masterOn, {"numModifyingFX", "long", []byte{0, 0, 0, 0}}}, false},
		{"nonzero_modifying", []semanticsDescriptorEntry{masterOn, {"numModifyingFX", "long", []byte{0, 0, 0, 1}}}, true},
		{"unknown_effect", []semanticsDescriptorEntry{masterOn, {"futureEffect", "Objc", disabledEffect}}, true},
		{"unknown_metadata", []semanticsDescriptorEntry{masterOn, {"futureFXSwitch", "bool", []byte{0}}}, true},
		{"disabled_unknown", []semanticsDescriptorEntry{masterOff, {"futureEffect", "Objc", activeEffect}}, false},
		{"missing_enabled", []semanticsDescriptorEntry{masterOn, {"DrSh", "Objc", semanticsDescriptor()}}, true},
		{"invalid_enabled_type", []semanticsDescriptorEntry{masterOn, {"DrSh", "Objc", semanticsDescriptor(
			semanticsDescriptorEntry{"enab", "long", []byte{0, 0, 0, 0}},
		)}}, true},
		{"invalid_effect_type", []semanticsDescriptorEntry{masterOn, {"DrSh", "bool", []byte{0}}}, true},
		{"invalid_master_type", []semanticsDescriptorEntry{{"masterFXSwitch", "long", []byte{0, 0, 0, 0}}}, true},
	}
	for _, key := range []string{"lfx2", "lmfx", "lfxs"} {
		for _, version := range []uint16{1, 2} {
			for _, test := range cases {
				t.Run(fmt.Sprintf("%s/version_%d/%s", key, version, test.name), func(t *testing.T) {
					block := semanticsEffectsBlock(key, test.entries...)
					document, err := ParseBytes(semanticsDocument(version, nil, nil, block))
					if err != nil {
						t.Fatal(err)
					}
					active := slices.Contains(document.Layers[0].Unsupported, "暂不支持图层效果")
					if active != test.active {
						t.Fatalf("effect active=%t want=%t: %+v", active, test.active, document.Layers[0].Unsupported)
					}
				})
			}
		}
	}
}

func TestObjectEffectsPhotoshopDisabledDefaults(t *testing.T) {
	// Photoshop writes these settings when changing advanced blending options without adding an effect.
	entries := []semanticsDescriptorEntry{
		{"masterFXSwitch", "bool", []byte{1}}, semanticsEffectScale(),
		{"numModifyingFX", "long", []byte{0, 0, 0, 0}},
	}
	for _, key := range []string{
		"dropShadowMulti", "innerShadowMulti", "solidFillMulti", "gradientFillMulti", "frameFXMulti",
	} {
		entries = append(entries, semanticsDescriptorEntry{key, "VlLs", semanticsDescriptorList(semanticsEffect(false))})
	}
	for _, key := range []string{"OrGl", "IrGl", "ebbl", "ChFX"} {
		entries = append(entries, semanticsDescriptorEntry{key, "Objc", semanticsEffect(false)})
	}
	document, err := ParseBytes(semanticsDocument(1, nil, nil, semanticsEffectsBlock("lfx2", entries...)))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(document.Layers[0].Unsupported, "暂不支持图层效果") {
		t.Fatal("Photoshop's disabled default effects were incorrectly marked active")
	}
	for _, key := range []string{"PtFl", "patternFill", "FrFX", "frameFX", "DrSh", "dropShadow", "SoFi", "solidFill"} {
		block := semanticsEffectsBlock("lfx2", semanticsDescriptorEntry{key, "Objc", semanticsEffect(true)})
		document, err := ParseBytes(semanticsDocument(1, nil, nil, block))
		if err != nil || !slices.Contains(document.Layers[0].Unsupported, "暂不支持图层效果") {
			t.Fatalf("active effect %s was not blocked: %v", key, err)
		}
	}
}

func TestObjectEffectsUnknownVersionAndStructureConservative(t *testing.T) {
	unknownVersion := semanticsEffectsBlock("lfx2", semanticsDescriptorEntry{"masterFXSwitch", "bool", []byte{0}})
	unknownVersion.data[3] = 1
	nested := &psbWriter{}
	nested.u32(1)
	nested.WriteString("VlLs")
	nested.u32(0)
	for _, block := range []semanticsBlock{
		unknownVersion,
		semanticsEffectsBlock("lfx2", semanticsDescriptorEntry{"dropShadowMulti", "VlLs", nested.Bytes()}),
	} {
		document, err := ParseBytes(semanticsDocument(1, nil, nil, block))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(document.Layers[0].Unsupported, "暂不支持图层效果") {
			t.Fatal("unknown effect version/structure was silently ignored")
		}
	}
}
