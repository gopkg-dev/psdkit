package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func placedEffectsRoundtripFixture(t *testing.T, version uint16, mixed, global, external bool) ([]byte, []byte) {
	t.Helper()
	seed, err := psdfile.Parse(mixedMaskEffectsFixture(t, version, false))
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	_, block := effectSourceView(t, seed)
	descriptor, err := seed.DecodeDescriptorContext(t.Context(), psdfile.Span{
		Offset: block.Data.Offset + 4, Length: block.Data.Length - 4,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !mixed {
		for index, entry := range descriptor.Entries {
			if entry.Key == "SoFi" {
				descriptor.Entries = append(descriptor.Entries[:index], descriptor.Entries[index+1:]...)
				break
			}
		}
	}
	payload, err := seed.EncodeDescriptor(t.Context(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload = append(make([]byte, 4), payload...)
	payload = append(payload, make([]byte, (4-len(payload)%4)%4)...)
	contents := smartEditPrototype(version, smartEditImage(smartEditColors()))
	kind := "liFD"
	if external {
		kind = "liFE"
	}
	linked := writerLinkedEntry(kind, "fixture-id", 7, contents)
	lengthWidth := 4
	if version == 2 {
		lengthWidth = 8
		copy(linked.data[linked.sizeOffset:linked.sizeOffset+4], "8BPB")
	}
	globalTags := writerLinkedTag("8BIM", "lnk2", lengthWidth, linked.data, 4)
	poisoned := smartEditImage([4]color.NRGBA{
		{R: 250, B: 230, A: 255}, {R: 250, B: 230, A: 255},
		{R: 250, B: 230, A: 255}, {R: 250, B: 230, A: 255},
	})
	var records, samples []byte
	for index, left := range []int{2, 5} {
		tags := compositeFixtureTag("lfx2", payload)
		globalByte := byte(0)
		if global {
			globalByte = 1
		}
		tags = append(tags, compositeFixtureTag("lmgm", []byte{globalByte, 0, 0, 0})...)
		tags = append(tags, compositeFixtureTag("iOpa", []byte{173, 0, 0, 0})...)
		tags = append(tags, compositeFixtureTag("pRsv", []byte{0x31, 0x72, 0xa4})...)
		tags = append(tags, compositeFixtureTag("SoLd", smartEditPlacement(t, left, ""))...)
		record, planes := smartEditLayer(version, fmt.Sprintf("Shared placed %d", index), uint32(100+index),
			image.Rect(left, 0, left+2, 2), poisoned, tags, true)
		record[18+5*(2+lengthWidth)+8] = 128
		records, samples = append(records, record...), append(samples, planes...)
	}
	info := binary.BigEndian.AppendUint16(nil, 0xfffe)
	info = append(info, records...)
	info = append(info, samples...)
	if len(info)%2 != 0 {
		info = append(info, 0)
	}
	footer := append([]byte{0, 0}, bytes.Repeat([]byte{0xa5}, 9*5*4)...)
	return smartEditDocument(version, 9, 5, 4, info, globalTags, footer), contents
}

func placedEffectsRoundtripSet(t *testing.T, source *psdfile.Document, index int) *psdeffects.Set {
	t.Helper()
	view, err := source.ReadLayerMetadata(t.Context(), index)
	if err != nil || view.Layer == nil || view.Layer.Effects == nil {
		t.Fatalf("missing placed effect source %d: %v", index, err)
	}
	return view.Layer.Effects
}

func placedEffectsRoundtripDesired(set *psdeffects.Set, round, instance int) {
	for index := range set.Items {
		effect := &set.Items[index]
		switch effect.Kind {
		case psdeffects.DropShadow:
			effect.Color = [3]float64{.2, .1, .3}
			effect.Opacity, effect.Distance, effect.Size = .6, .75+float64(instance), .5
			if round != 0 {
				effect.Opacity, effect.Distance, effect.Size = .8, 1.25+float64(instance), 1.5
			}
		case psdeffects.ColorOverlay:
			effect.Color, effect.Opacity = [3]float64{0, .4, 1}, .35
			if round != 0 {
				effect.Color, effect.Opacity = [3]float64{1, .2, 0}, .65
			}
		}
	}
}

// placedEffectsRoundtripExpected is an ordinary-raster oracle with independently supplied artwork and styling.
func placedEffectsRoundtripExpected(
	t *testing.T, artwork *image.NRGBA, desired [2]*psdeffects.Set, global bool, opacity, fill byte,
) *image.NRGBA {
	t.Helper()
	layers := make([]*psdmodel.Layer, 0, 2)
	for index, left := range []int{2, 5} {
		mask := image.NewNRGBA(image.Rect(0, 0, 2, 2))
		for position, value := range []byte{255, 128, 0, 255} {
			mask.SetNRGBA(position%2, position/2, color.NRGBA{R: value, G: value, B: value, A: 255})
		}
		bounds := image.Rect(left, 0, left+2, 2)
		layers = append(layers, &psdmodel.Layer{
			Bounds: bounds, Pixels: artwork, BlendMode: "norm", Effects: desired[index],
			Opacity: new(float64(opacity) / 255), FillOpacity: new(float64(fill) / 255),
			MaskAsGlobal: new(global), Mask: &psdmodel.Mask{Bounds: bounds, Pixels: mask, DefaultColor: new(uint8(255))},
		})
	}
	wanted, err := psdcompose.Render(t.Context(), &psdmodel.Document{Width: 9, Height: 5, Layers: layers},
		psdcompose.Options{ApproximateDropShadow: true})
	if err != nil {
		t.Fatal(err)
	}
	return wanted
}

func placedEffectsRoundtripRaw(t *testing.T, saved *psdfile.Document, artwork *image.NRGBA) {
	t.Helper()
	for layerIndex := range 2 {
		for channelIndex, channel := range saved.Layers[layerIndex].Channels {
			plane, err := saved.DecodeChannel(t.Context(), layerIndex, channelIndex)
			if err != nil {
				t.Fatal(err)
			}
			if channel.ID == -2 {
				if !bytes.Equal(plane.Data, []byte{255, 128, 0, 255}) {
					t.Fatal("placed effect edit baked or changed the retained user mask")
				}
				continue
			}
			component := int(channel.ID)
			if component == -1 {
				component = 3
			}
			bounds := saved.Layers[layerIndex].Bounds
			left := []int{2, 5}[layerIndex]
			expected := make([]byte, plane.Width*plane.Height)
			for position := range expected {
				value := artwork.NRGBAAt(bounds.Min.X+position%plane.Width-left, bounds.Min.Y+position/plane.Width)
				expected[position] = [4]byte{value.R, value.G, value.B, value.A}[component]
			}
			if !bytes.Equal(plane.Data, expected) {
				t.Fatalf("placed raw channel %d/%d baked opacity, Fill, mask or effects: %v, want %v",
					layerIndex, channel.ID, plane.Data, expected)
			}
		}
	}
}

func placedEffectsRoundtripPreserved(t *testing.T, before, after *psdfile.Document) {
	t.Helper()
	for layerIndex := range 2 {
		left, right := before.Layers[layerIndex], after.Layers[layerIndex]
		beforeMask, err := before.ReadSpan(left.MaskData)
		if err != nil {
			t.Fatal(err)
		}
		afterMask, err := after.ReadSpan(right.MaskData)
		if err != nil || !bytes.Equal(beforeMask, afterMask) {
			t.Fatalf("placed effect edit changed mask metadata: %v", err)
		}
		for blockIndex, block := range left.AdditionalInfo {
			if block.Key == "lfx2" {
				continue
			}
			leftBytes, err := before.ReadSpan(block.Span)
			if err != nil {
				t.Fatal(err)
			}
			rightBytes, err := after.ReadSpan(right.AdditionalInfo[blockIndex].Span)
			if err != nil || block.Key != right.AdditionalInfo[blockIndex].Key || !bytes.Equal(leftBytes, rightBytes) {
				t.Fatalf("placed effect edit rewrote unrelated %s bytes: %v", block.Key, err)
			}
		}
	}
	if !bytes.Equal(vectorFeatherShadowUnknown(t, before), vectorFeatherShadowUnknown(t, after)) {
		t.Fatal("placed effect edit rewrote unknown inactive descriptor entries")
	}
}

func placedEffectsRoundtripPreview(t *testing.T, saved *psdfile.Document, expected *image.NRGBA) {
	t.Helper()
	merged, err := saved.DecodeComposite(t.Context())
	if err != nil || len(merged.Channels) != 4 {
		t.Fatalf("placed effects saved no editable RGBA preview: %v", err)
	}
	for position := range expected.Rect.Dx() * expected.Rect.Dy() {
		alpha := expected.Pix[position*4+3]
		if merged.Channels[3].Data[position] != alpha {
			t.Fatal("placed effect merged alpha differs from independent fresh source composition")
		}
		for component := range 3 {
			wanted := byte(math.RoundToEven(float64(expected.Pix[position*4+component])*float64(alpha)/255 +
				255 - float64(alpha)))
			if merged.Channels[component].Data[position] != wanted {
				t.Fatal("placed effect merged RGB retained stale source or double-applied parent styling")
			}
		}
	}
}

func TestPlacedEffectsCurrentSharedSourceSaveReopenContinuePSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, mixed := range []bool{false, true} {
				for _, global := range []bool{false, true} {
					t.Run(fmt.Sprintf("v%d/c%d/mixed%t/global%t", version, compression, mixed, global), func(t *testing.T) {
						data, _ := placedEffectsRoundtripFixture(t, version, mixed, global, false)
						original := bytes.Clone(data)
						editor := compositeEditor(t, data)
						for round := range 2 {
							colors := [4]color.NRGBA{{220, 40, 90, 255}, {30, 180, 50, 128},
								{10, 40, 200, 64}, {170, 120, 80, 255}}
							if round != 0 {
								colors[0], colors[3] = colors[3], colors[0]
							}
							artwork := smartEditImage(colors)
							contents := smartEditPrototype(3-version, artwork)
							immutable := bytes.Clone(contents)
							if err := editor.ReplaceContents(t.Context(), 1, contents); err != nil {
								t.Fatal(err)
							}
							contents[len(contents)-1] ^= 0xff
							opacity, fill := byte(128), byte(173)
							if round != 0 {
								opacity, fill = 196, 91
							}
							var desired [2]*psdeffects.Set
							for index := range 2 {
								id := LayerID(index + 1)
								if err := editor.SetOpacity(id, opacity); err != nil {
									t.Fatal(err)
								}
								if err := editor.SetFillOpacity(id, fill); err != nil {
									t.Fatal(err)
								}
								desired[index] = placedEffectsRoundtripSet(t, editor.source, index)
								placedEffectsRoundtripDesired(desired[index], round, index)
								unchanged := smartContentsFilterState(t, editor)
								if err := editor.SetEffects(t.Context(), id, *desired[index]); !errors.Is(err, ErrUnsupported) {
									t.Fatalf("placed effect edit inferred an approximate shadow policy: %v", err)
								}
								unchanged()
								if err := editor.SetEffectsWithOptions(t.Context(), id, *desired[index],
									EffectsEditOptions{ApproximateDropShadow: true}); err != nil {
									t.Fatalf("current shared source mixed effects failed: %v", err)
								}
							}
							expected := placedEffectsRoundtripExpected(t, artwork, desired, global, opacity, fill)
							var rejected bytes.Buffer
							if err := editor.WritePSD(t.Context(), &rejected); !errors.Is(err, ErrUnsupported) || rejected.Len() != 0 {
								t.Fatalf("save inherited edit approximation or wrote a partial preview: %v", err)
							}
							path := filepath.Join(t.TempDir(), "placed-effects.psd")
							if err := editor.Save(t.Context(), path,
								SaveOptions{Compression: compression, ApproximateDropShadow: true}); err != nil {
								t.Fatalf("placed effects require an externally supplied composite: %v", err)
							}
							saved, err := psdfile.Open(path)
							if err != nil {
								t.Fatal(err)
							}
							t.Cleanup(func() { _ = saved.Close() })
							if saved.Header.Version != version {
								t.Fatal("placed effect save changed PSD/PSB version")
							}
							placedEffectsRoundtripRaw(t, saved, artwork)
							placedEffectsRoundtripPreview(t, saved, expected)
							smartContentsFilterAssertSource(t, saved, immutable)
							if round == 0 {
								placedEffectsRoundtripPreserved(t, editor.source, saved)
							}
							for index := range 2 {
								persisted := placedEffectsRoundtripSet(t, saved, index)
								if !effectsParametersEqual(*persisted, *desired[index]) {
									t.Fatal("shared instance effects were aliased or reverted after saving")
								}
							}
							editor = lifecycleReopen(t, saved)
							actual, err := editor.Composite(t.Context(), psdcompose.Options{ApproximateDropShadow: true})
							if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
								t.Fatalf("reopened fresh placed effects differ from the unbaked raster oracle: %v", err)
							}
						}
						if !bytes.Equal(data, original) {
							t.Fatal("placed effect edit mutated original encoded source bytes")
						}
					})
				}
			}
		}
	}
}

func TestPlacedEffectsRejectedEditsRemainAtomic(t *testing.T) {
	for _, scenario := range []string{"cancel", "budget", "read-failure", "vector", "unknown-parameter", "multiply"} {
		t.Run(scenario, func(t *testing.T) {
			data, _ := placedEffectsRoundtripFixture(t, 1, true, false, false)
			editor, source, reader := compositeLayerOnlyEditor(t, data)
			desired, block := effectSourceView(t, source)
			placedEffectsRoundtripDesired(desired, 0, 0)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := ErrUnsupported
			switch scenario {
			case "cancel":
				cancel()
				want = context.Canceled
			case "budget":
				editor.roots[0].setSemanticCost("placedEffectsBudget", source.Limits().MaxDecodeBytes-1024)
				want = psdfile.ErrLimit
			case "read-failure":
				reader.failure, reader.failureCause = block.Data, io.ErrClosedPipe
				want = io.ErrClosedPipe
			case "vector":
				mask := disabledPixelFeatherVector(image.Rect(2, 0, 4, 2))
				mask.Width, mask.Height = 9, 5
				if err := editor.AddVectorMask(t.Context(), 1, mask); err != nil {
					t.Fatal(err)
				}
			case "unknown-parameter":
				desired.Items[0].Noise = .1
			case "multiply":
				if err := editor.SetBlendMode(1, "mul "); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := editor.Changes()
			owned, effects := editor.ownedBytesLocked(nil), editor.roots[0].effects
			pixels, placed := editor.roots[0].pixels, editor.roots[0].placed
			var additional map[int64][]byte
			if editor.roots[0].additional != nil {
				additional = make(map[int64][]byte, len(editor.roots[0].additional))
			}
			for offset, payload := range editor.roots[0].additional {
				additional[offset] = bytes.Clone(payload)
			}
			err := editor.SetEffectsWithOptions(ctx, 1, *desired, EffectsEditOptions{ApproximateDropShadow: true})
			if !errors.Is(err, want) {
				t.Fatalf("rejected placed edit = %v, want %v", err, want)
			}
			if after, _ := editor.Changes(); after != before || editor.ownedBytesLocked(nil) != owned ||
				editor.roots[0].effects != effects || editor.roots[0].pixels != pixels || editor.roots[0].placed != placed ||
				!reflect.DeepEqual(editor.roots[0].additional, additional) {
				t.Fatal("rejected placed effects changed parameters, pixels, budget or revision")
			}
		})
	}
}

func TestPlacedEffectsStrictSavePreservesExistingDestination(t *testing.T) {
	data, _ := placedEffectsRoundtripFixture(t, 1, true, false, false)
	editor := compositeEditor(t, data)
	desired, _ := effectSourceView(t, editor.source)
	placedEffectsRoundtripDesired(desired, 0, 0)
	if err := editor.SetEffectsWithOptions(t.Context(), 1, *desired,
		EffectsEditOptions{ApproximateDropShadow: true}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "retained-placed.psd")
	sentinel := []byte("existing placed destination survives strict rejected automatic approximation")
	if err := os.WriteFile(path, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := editor.Changes()
	if err := editor.Save(t.Context(), path); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("strict placed save inherited preceding edit approximation: %v", err)
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, sentinel) {
		t.Fatalf("rejected strict placed save replaced existing destination: %v", err)
	}
	if after, _ := editor.Changes(); after != before {
		t.Fatal("rejected strict placed save changed the editor revision or dirty state")
	}
}

func TestPlacedEffectsDisabledOrRemovedVectorRestoresQualifiedSourceScope(t *testing.T) {
	data, _ := placedEffectsRoundtripFixture(t, 1, true, false, false)
	editor := compositeEditor(t, data)
	mask := disabledPixelFeatherVector(image.Rect(2, 0, 4, 2))
	mask.Width, mask.Height, mask.Disabled = 9, 5, true
	if err := editor.AddVectorMask(t.Context(), 1, mask); err != nil {
		t.Fatal(err)
	}
	desired, _ := effectSourceView(t, editor.source)
	placedEffectsRoundtripDesired(desired, 0, 0)
	policy := EffectsEditOptions{ApproximateDropShadow: true}
	if err := editor.SetEffectsWithOptions(t.Context(), 1, *desired, policy); err != nil {
		t.Fatalf("disabled current vector mask incorrectly excluded live placed effects: %v", err)
	}
	mask.Disabled = false
	if err := editor.SetVectorMask(t.Context(), 1, mask); err != nil {
		t.Fatal(err)
	}
	unchanged := smartContentsFilterState(t, editor)
	if err := editor.SetEffectsWithOptions(t.Context(), 1, *desired, policy); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("reenabled vector mask silently expanded live placed effect scope: %v", err)
	}
	unchanged()
	if err := editor.RemoveVectorMask(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	if err := editor.SetEffectsWithOptions(t.Context(), 1, *desired, policy); err != nil {
		t.Fatalf("removing current vector mask did not restore qualified live placed effect scope: %v", err)
	}
}
