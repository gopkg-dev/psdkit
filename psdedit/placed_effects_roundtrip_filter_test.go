package psdedit

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func placedEffectsRoundtripFilterFixture(t *testing.T, version uint16, external bool) []byte {
	t.Helper()
	data, _ := placedEffectsRoundtripFixture(t, version, true, false, external)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	var records, samples, global []byte
	for _, block := range source.AdditionalInfo {
		encoded, err := source.ReadSpan(block.Span)
		if err != nil {
			t.Fatal(err)
		}
		global = append(global, encoded...)
	}
	for index, left := range []int{2, 5} {
		var tags []byte
		for _, block := range source.Layers[index].AdditionalInfo {
			if block.Key == "lyid" || block.Key == "U000" {
				continue
			}
			payload, err := source.ReadSpan(block.Data)
			if err != nil {
				t.Fatal(err)
			}
			if block.Key == "SoLd" {
				descriptor, err := source.DecodeDescriptorContext(t.Context(), psdfile.Span{
					Offset: block.Data.Offset + 8, Length: block.Data.Length - 8,
				}, true)
				if err != nil {
					t.Fatal(err)
				}
				descriptor.Entries = append(descriptor.Entries,
					psdfile.DescriptorEntry{Key: "filterFX", Type: "Objc", Value: filterEditStackDescriptor()})
				encoded, err := source.EncodeDescriptor(t.Context(), descriptor, true)
				if err != nil {
					t.Fatal(err)
				}
				payload = append(bytes.Clone(payload[:8]), encoded...)
				payload = append(payload, 0xa1, 0xd3, 0x7f)
			}
			tags = append(tags, compositeFixtureTag(block.Key, payload)...)
		}
		record, planes := smartEditLayer(version, "Filtered shared effects", uint32(100+index),
			image.Rect(left, 0, left+2, 2), exactSmartFilterGrayPixels([4]byte{17, 17, 17, 17}), tags, true)
		lengthWidth := 4
		if version == 2 {
			lengthWidth = 8
		}
		record[18+5*(2+lengthWidth)+8] = 128
		records, samples = append(records, record...), append(samples, planes...)
	}
	info := binary.BigEndian.AppendUint16(nil, 0xfffe)
	info = append(info, records...)
	info = append(info, samples...)
	if len(info)%2 != 0 {
		info = append(info, 0)
	}
	return smartEditDocument(version, 9, 5, 4, info, global,
		append([]byte{0, 0}, bytes.Repeat([]byte{23}, 9*5*4)...))
}

func TestPlacedEffectsCurrentStrictFilterSaveReopenContinuePSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			t.Run(fmt.Sprintf("v%d/c%d", version, compression), func(t *testing.T) {
				editor := compositeEditor(t, placedEffectsRoundtripFilterFixture(t, version, false))
				original := smartEditImage(smartEditColors())
				for round := range 2 {
					stack := smartFilterAutoExactStack(t, editor.source)
					stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
					if round != 0 {
						stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = -37
					}
					expected, err := psdfilter.Apply(t.Context(), original, stack, psdfilter.Options{})
					if err != nil || bytes.Equal(expected.Pix, original.Pix) {
						t.Fatalf("independent strict filtered source did not exercise current controls: %v", err)
					}
					var desired [2]*psdeffects.Set
					for index, left := range []int{2, 5} {
						accurate := image.NewNRGBA(image.Rect(left, 0, left+2, 2))
						copy(accurate.Pix, expected.Pix)
						if err := editor.SetSmartFilterParameters(t.Context(), LayerID(index+1), stack,
							SmartFilterEditOptions{Pixels: accurate}); err != nil {
							t.Fatal(err)
						}
						desired[index] = placedEffectsRoundtripSet(t, editor.source, index)
						placedEffectsRoundtripDesired(desired[index], round, index)
						if err := editor.SetEffectsWithOptions(t.Context(), LayerID(index+1), *desired[index],
							EffectsEditOptions{ApproximateDropShadow: true}); err != nil {
							t.Fatalf("strict active current filter required unrelated approximation consent: %v", err)
						}
					}
					wanted := placedEffectsRoundtripExpected(t, expected, desired, false, 128, 173)
					path := filepath.Join(t.TempDir(), "filtered-placed-effects.psd")
					if err := editor.Save(t.Context(), path,
						SaveOptions{Compression: compression, ApproximateDropShadow: true}); err != nil {
						t.Fatal(err)
					}
					saved, err := psdfile.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = saved.Close() })
					placedEffectsRoundtripRaw(t, saved, expected)
					placedEffectsRoundtripPreview(t, saved, wanted)
					if saved.Header.Version != version {
						t.Fatal("filtered placed effects changed PSD/PSB version")
					}
					editor = lifecycleReopen(t, saved)
				}
			})
		}
	}
}

func TestPlacedEffectsApproximateFilterRequiresIndependentExplicitSourcePolicy(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			editor := compositeEditor(t, placedEffectsRoundtripFilterFixture(t, version, false))
			stack := smartFilterAutoExactStack(t, editor.source)
			stack.Filters[3].Adjustment.BrightnessContrast.Brightness = 15
			artwork := smartEditImage(smartEditColors())
			filterPolicy := psdfilter.Options{ApproximateBrightnessContrast: true}
			expected, err := psdfilter.Apply(t.Context(), artwork, stack, filterPolicy)
			if err != nil || bytes.Equal(expected.Pix, artwork.Pix) {
				t.Fatalf("independent approximate filter did not exercise selected source policy: %v", err)
			}
			for index, left := range []int{2, 5} {
				accurate := image.NewNRGBA(image.Rect(left, 0, left+2, 2))
				copy(accurate.Pix, expected.Pix)
				if err := editor.SetSmartFilterParameters(t.Context(), LayerID(index+1), stack,
					SmartFilterEditOptions{Pixels: accurate}); err != nil {
					t.Fatal(err)
				}
			}
			var desired [2]*psdeffects.Set
			for index := range 2 {
				desired[index] = placedEffectsRoundtripSet(t, editor.source, index)
				placedEffectsRoundtripDesired(desired[index], 0, index)
				unchanged := smartContentsFilterState(t, editor)
				if err := editor.SetEffectsWithOptions(t.Context(), LayerID(index+1), *desired[index],
					EffectsEditOptions{ApproximateDropShadow: true}); !errors.Is(err, ErrUnsupported) {
					t.Fatalf("parent shadow consent silently enabled approximate source filtering: %v", err)
				}
				unchanged()
				if err := editor.SetEffectsWithOptions(t.Context(), LayerID(index+1), *desired[index], EffectsEditOptions{
					Render: &psdscene.Options{SmartFilters: &filterPolicy},
				}); !errors.Is(err, ErrUnsupported) {
					t.Fatalf("source filter consent silently enabled parent shadow approximation: %v", err)
				}
				unchanged()
				if err := editor.SetEffectsWithOptions(t.Context(), LayerID(index+1), *desired[index], EffectsEditOptions{
					ApproximateDropShadow: true, Render: &psdscene.Options{SmartFilters: &filterPolicy},
				}); err != nil {
					t.Fatalf("independently selected source filter and parent shadow policies failed: %v", err)
				}
			}
			var rejected bytes.Buffer
			if err := editor.WritePSD(t.Context(), &rejected,
				SaveOptions{ApproximateDropShadow: true}); !errors.Is(err, ErrUnsupported) || rejected.Len() != 0 {
				t.Fatalf("save inherited approximate source filtering from an earlier edit: %v", err)
			}
			wanted := placedEffectsRoundtripExpected(t, expected, desired, false, 128, 173)
			saved := compositeSave(t, editor, SaveOptions{Compression: 3, CompositeImage: wanted})
			placedEffectsRoundtripRaw(t, saved, expected)
			placedEffectsRoundtripPreview(t, saved, wanted)
		})
	}
}
