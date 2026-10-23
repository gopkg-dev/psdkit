package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func vectorShadowGateFixture(t *testing.T, mixed, real bool) []byte {
	t.Helper()
	descriptor := &psdfile.Descriptor{ClassID: "Lefx", Entries: []psdfile.DescriptorEntry{
		{Key: "masterFXSwitch", Type: "bool", Value: true},
		{Key: "DrSh", Type: "Objc", Value: mixedMaskEffectObject("DrSh")},
	}}
	if mixed {
		descriptor.Entries = append(descriptor.Entries,
			psdfile.DescriptorEntry{Key: "SoFi", Type: "Objc", Value: mixedMaskEffectObject("SoFi")})
	}
	encoded, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(make([]byte, 4), encoded...)
	payload = append(payload, make([]byte, (4-len(payload)%4)%4)...)
	vector, err := psdvector.EncodeMask(t.Context(), colorOverlayVectorMask(t, .5))
	if err != nil {
		t.Fatal(err)
	}
	tags := compositeFixtureTag("lfx2", payload)
	tags = append(tags, compositeFixtureTag("vmsk", vector)...)
	layer := compositeFixtureLayer{color: []byte{200, 100, 40, 127}, mask: []byte{64}, tags: tags}
	if real {
		layer.extraID = -3
	}
	data := compositeFixture(1, 8, 4, []compositeFixtureLayer{layer})
	if real {
		source, err := psdfile.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		data[source.Layers[0].Mask.Span.Offset+17] |= 8
		_ = source.Close()
	}
	return data
}

// vectorShadowSourceMask replaces immutable source metadata without using the composite writer.
func vectorShadowSourceMask(t *testing.T, data, maskData []byte) []byte {
	t.Helper()
	data, maskData = bytes.Clone(data), bytes.Clone(maskData)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	record := source.Layers[0]
	if (int64(len(maskData))-record.MaskData.Length)%2 != 0 {
		maskData = append(maskData, 0)
		binary.BigEndian.PutUint32(maskData, uint32(len(maskData)-4))
	}
	delta := int64(len(maskData)) - record.MaskData.Length
	for _, offset := range []int64{source.LayerMaskSection.Offset, source.LayerInfo.Offset, record.Extra.Offset - 4} {
		length := binary.BigEndian.Uint32(data[offset:])
		binary.BigEndian.PutUint32(data[offset:], uint32(int64(length)+delta))
	}
	result := append([]byte{}, data[:record.MaskData.Offset]...)
	result = append(result, maskData...)
	return append(result, data[record.MaskData.Offset+record.MaskData.Length:]...)
}

func vectorShadowGateReject(t *testing.T, editor *Editor, ctx context.Context, desired *psdeffects.Set, want error) {
	t.Helper()
	node := editor.roots[0]
	before, _ := editor.Changes()
	owned, effects, additional := editor.ownedBytesLocked(nil), node.effects, len(node.additional)
	if err := editor.SetEffectsWithOptions(ctx, 1, *desired,
		EffectsEditOptions{ApproximateDropShadow: true}); !errors.Is(err, want) {
		t.Fatalf("unsupported vector-shadow edit = %v, want %v", err, want)
	}
	if after, _ := editor.Changes(); after != before || editor.ownedBytesLocked(nil) != owned ||
		node.effects != effects || len(node.additional) != additional {
		t.Fatal("rejected vector-shadow edit changed effects, revision or retained memory")
	}
}

func TestVectorShadowEditRequiresExplicitPolicyAndRechecksCurrentMasks(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, real := range []bool{false, true} {
			t.Run(fmt.Sprintf("mixed-%t/real-%t", mixed, real), func(t *testing.T) {
				editor := compositeEditor(t, vectorShadowGateFixture(t, mixed, real))
				desired, _ := effectSourceView(t, editor.source)
				mixedMaskEffectsDesired(desired)
				before, _ := editor.Changes()
				if err := editor.SetEffects(t.Context(), 1, *desired); !errors.Is(err, ErrUnsupported) {
					t.Fatalf("active vector shadows lost the explicit approximation requirement: %v", err)
				}
				if after, _ := editor.Changes(); after != before {
					t.Fatal("missing approximation policy changed the editor")
				}
				apply := func() {
					t.Helper()
					if err := editor.SetEffectsWithOptions(t.Context(), 1, *desired,
						EffectsEditOptions{ApproximateDropShadow: true}); err != nil {
						t.Fatal(err)
					}
					if _, err := editor.Composite(t.Context(), psdcompose.Options{ApproximateDropShadow: true}); err != nil {
						t.Fatalf("accepted current mask combination cannot composite: %v", err)
					}
				}
				apply()
				settings, err := editor.PixelMask(t.Context(), 1)
				if err != nil {
					t.Fatal(err)
				}
				for _, vector := range []bool{true, false} {
					if vector {
						settings.VectorFeather = new(.5)
					} else {
						settings.Feather = new(.5)
					}
					if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
						t.Fatal(err)
					}
					vectorShadowGateReject(t, editor, t.Context(), desired, ErrUnsupported)
					settings.Feather, settings.VectorFeather = new(float64(0)), new(float64(0))
					if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
						t.Fatal(err)
					}
					apply()
				}
				settings.Feather, settings.Disabled = new(.5), true
				if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
					t.Fatal(err)
				}
				apply()
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				vectorShadowGateReject(t, editor, ctx, desired, context.Canceled)
				editor.roots[0].setSemanticCost("gateBudget", editor.source.Limits().MaxDecodeBytes-1024)
				vectorShadowGateReject(t, editor, t.Context(), desired, psdfile.ErrLimit)
			})
		}
	}
}

func TestVectorShadowEditKnownSourceFeatherCanBeCleared(t *testing.T) {
	for _, real := range []bool{false, true} {
		t.Run(fmt.Sprintf("real-%t", real), func(t *testing.T) {
			data := vectorShadowGateFixture(t, true, real)
			builder := compositeEditor(t, data)
			settings, err := builder.PixelMask(t.Context(), 1)
			if err != nil {
				t.Fatal(err)
			}
			settings.VectorDensity, settings.VectorFeather = new(uint8(137)), new(.5)
			if err := builder.SetPixelMask(t.Context(), 1, settings); err != nil {
				t.Fatal(err)
			}
			editor := compositeEditor(t, vectorShadowSourceMask(t, data, builder.roots[0].maskData))
			desired, _ := effectSourceView(t, editor.source)
			mixedMaskEffectsDesired(desired)
			vectorShadowGateReject(t, editor, t.Context(), desired, ErrUnsupported)
			settings.VectorFeather = new(float64(0))
			if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
				t.Fatal(err)
			}
			if err := editor.SetEffectsWithOptions(t.Context(), 1, *desired,
				EffectsEditOptions{ApproximateDropShadow: true}); err != nil {
				t.Fatalf("cleared known source feather still blocked effects: %v", err)
			}
			if _, err := editor.Composite(t.Context(), psdcompose.Options{ApproximateDropShadow: true}); err != nil {
				t.Fatalf("cleared source feather cannot composite: %v", err)
			}
		})
	}
}

func TestVectorShadowEditRealFlagsWithSharedParametersCannotBeHidden(t *testing.T) {
	for _, sourceFlag := range []bool{false, true} {
		t.Run(fmt.Sprintf("source-%t", sourceFlag), func(t *testing.T) {
			data := vectorShadowGateFixture(t, true, true)
			editor := compositeEditor(t, data)
			settings, err := editor.PixelMask(t.Context(), 1)
			if err != nil {
				t.Fatal(err)
			}
			settings.VectorDensity = new(uint8(137))
			if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
				t.Fatal(err)
			}
			state, err := editor.pixelMaskStateLocked(t.Context(), editor.roots[0])
			if err != nil || !state.hasReal || state.channelID != -3 || state.layout.settings.VectorDensity == nil {
				t.Fatalf("fixture lost the generated/real mask and shared parameters: %v", err)
			}
			clean := bytes.Clone(editor.roots[0].maskData)
			editor.roots[0].maskData[state.layout.flagsOffset] |= 0x20
			if sourceFlag {
				editor = compositeEditor(t, vectorShadowSourceMask(t, data, editor.roots[0].maskData))
				if editor.source.Layers[0].Mask.RealFlags&0x20 == 0 {
					t.Fatal("fixture did not retain the unknown real-mask flag")
				}
				// An apparently clean owned overlay must not conceal unknown immutable source flags.
				editor.roots[0].maskData = clean
			}
			set, _ := effectSourceView(t, editor.source)
			mixedMaskEffectsDesired(set)
			vectorShadowGateReject(t, editor, t.Context(), set, ErrUnsupported)
		})
	}
}
