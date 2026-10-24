package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func vectorFeatherShadowFixture(t *testing.T, version uint16, mixed, vectorGlobal, pixelGlobal bool) []byte {
	t.Helper()
	seed, err := psdfile.Parse(shadowVectorEditFixture(t, version, shadowVectorFixtureOptions{
		mixed: mixed, mode: "vector", vectorGlobal: new(vectorGlobal), pixelGlobal: new(pixelGlobal),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	var tags []byte
	for _, block := range seed.Layers[0].AdditionalInfo {
		encoded, err := seed.ReadSpan(block.Span)
		if err != nil {
			t.Fatal(err)
		}
		tags = append(tags, encoded...)
	}
	pixels := image.NewNRGBA(image.Rect(0, 0, 32, 16))
	for offset := 0; offset < len(pixels.Pix); offset += 4 {
		copy(pixels.Pix[offset:], []byte{200, 100, 40, 127})
	}
	record, channels := smartEditLayer(version, "Feather shadow", 97, pixels.Rect, pixels, tags, false)
	info := binary.BigEndian.AppendUint16(nil, 0xffff)
	info = append(info, record...)
	info = append(info, channels...)
	if len(info)%2 != 0 {
		info = append(info, 0)
	}
	return smartEditDocument(version, 32, 16, 4, info, nil,
		append([]byte{0, 0}, bytes.Repeat([]byte{0xa5}, 32*16*4)...))
}

func vectorFeatherShadowMask(t *testing.T, editor *Editor, radius float64, dual, second bool) {
	t.Helper()
	rectangle := image.Rect(4, 3, 26, 13)
	if second {
		rectangle = image.Rect(-3, 1, 18, 12)
	}
	mask := disabledPixelFeatherVector(rectangle)
	if editor.roots[0].vectorMask == nil && lifecycleVectorBlocks(editor.source, 0) == 0 {
		if err := editor.AddVectorMask(t.Context(), 1, mask); err != nil {
			t.Fatal(err)
		}
	} else if err := editor.SetVectorMask(t.Context(), 1, mask); err != nil {
		t.Fatal(err)
	}
	samples := make([]byte, 32*16)
	for index := range samples {
		samples[index] = byte((index%32*17 + index/32*11) % 256)
	}
	settings := PixelMaskSettings{DefaultColor: 197, Invert: true,
		Density: new(uint8(137)), Feather: new(.5), VectorDensity: new(uint8(219)), VectorFeather: new(radius)}
	if second {
		settings.Feather = new(1.25)
	}
	if _, err := editor.PixelMask(t.Context(), 1); err != nil {
		plane := psdfile.Plane{Depth: 8, Width: 32, Height: 16, RowBytes: 32, Data: samples}
		if err := editor.AddPixelMask(t.Context(), 1, image.Rect(0, 0, 32, 16), plane, settings); err != nil {
			t.Fatal(err)
		}
	} else if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
		t.Fatal(err)
	}
	if !dual {
		if err := editor.RemovePixelMask(t.Context(), 1); err != nil {
			t.Fatal(err)
		}
	}
}

func vectorFeatherShadowSaved(
	t *testing.T, editor *Editor, version, compression uint16, desired psdeffects.Set, dual bool,
) *Editor {
	t.Helper()
	live, err := editor.Composite(t.Context(), psdcompose.Options{ApproximateDropShadow: true})
	if err != nil {
		t.Fatal(err)
	}
	maskState, err := editor.pixelMaskStateLocked(t.Context(), editor.roots[0])
	if err != nil {
		t.Fatal(err)
	}
	vectorState := editor.roots[0].vectorMask
	if vectorState == nil {
		t.Fatal("roundtrip must exercise an owned current vector rectangle")
	}
	extension := map[uint16]string{1: ".psd", 2: ".psb"}[version]
	path := filepath.Join(t.TempDir(), "vector-feather-shadow"+extension)
	if err := editor.Save(t.Context(), path,
		SaveOptions{Compression: compression, ApproximateDropShadow: true}); err != nil {
		t.Fatalf("current feather/shadow requires an external composite: %v", err)
	}
	saved, err := psdfile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = saved.Close() })
	if saved.Header.Version != version || saved.Layers[0].Bounds != image.Rect(0, 0, 32, 16) {
		t.Fatal("feather/shadow save changed the document format or source bounds")
	}
	maskBytes, err := saved.ReadSpan(saved.Layers[0].MaskData)
	if err != nil || !bytes.Equal(maskBytes, maskState.data) {
		t.Fatalf("effect edit changed current pixel/vector mask metadata: %v", err)
	}
	persistedVector := editVectorMask(t, saved)
	if persistedVector.Disabled != vectorState.Disabled || persistedVector.Invert != vectorState.Invert ||
		persistedVector.NotLinked != vectorState.NotLinked {
		t.Fatal("saved vector flags differ from current editor state")
	}
	// Parsed records carry source offsets, so compare physical coordinates independently.
	for index, knot := range persistedVector.Subpaths[0].Knots {
		if knot.Anchor != vectorState.Subpaths[0].Knots[index].Anchor ||
			knot.Preceding != vectorState.Subpaths[0].Knots[index].Preceding ||
			knot.Leaving != vectorState.Subpaths[0].Knots[index].Leaving {
			t.Fatal("saved vector geometry retained the earlier rectangle")
		}
	}
	maskChannels := 0
	for index, channel := range saved.Layers[0].Channels {
		plane, err := saved.DecodeChannel(t.Context(), 0, index)
		if err != nil {
			t.Fatal(err)
		}
		if channel.ID >= -1 {
			component := 3
			if channel.ID >= 0 {
				component = int(channel.ID)
			}
			if !bytes.Equal(plane.Data, bytes.Repeat([]byte{[]byte{200, 100, 40, 127}[component]}, 32*16)) {
				t.Fatal("saved raster channels baked in the vector feather, pixel mask or effects")
			}
			continue
		}
		maskChannels++
		if !dual {
			t.Fatal("vector-only effect save fabricated a removed pixel-mask channel")
		}
		for position, sample := range plane.Data {
			want := byte((position%32*17 + position/32*11) % 256)
			if sample != want {
				t.Fatal("effect edit rewrote retained user-mask samples")
			}
		}
	}
	if dual && maskChannels != 1 {
		t.Fatal("dual-mask save lost the original user-mask samples")
	}
	persisted, _ := effectSourceView(t, saved)
	if !effectsParametersEqual(*persisted, desired) {
		t.Fatal("saved feather/shadow retained previous effect parameters")
	}
	mixedMaskEffectsLegacy(t, saved, desired)
	if !bytes.Equal(vectorFeatherShadowUnknown(t, editor.source), vectorFeatherShadowUnknown(t, saved)) {
		t.Fatal("feather/shadow edit rewrote unknown inactive descriptor fields")
	}
	for _, key := range []string{"pRsv", "vmgm", "lmgm"} {
		var before, after []byte
		for _, block := range editor.source.Layers[0].AdditionalInfo {
			if block.Key == key {
				before, err = editor.source.ReadSpan(block.Span)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, block := range saved.Layers[0].AdditionalInfo {
			if block.Key == key {
				after, err = saved.ReadSpan(block.Span)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		if len(before) == 0 || !bytes.Equal(before, after) {
			t.Fatalf("feather/shadow edit changed unrelated %s metadata", key)
		}
	}
	merged, err := saved.DecodeComposite(t.Context())
	if err != nil || len(merged.Channels) != 4 {
		t.Fatalf("saved feather/shadow preview has no RGBA channels: %v", err)
	}
	for position := range 32 * 16 {
		alpha := live.Pix[position*4+3]
		if merged.Channels[3].Data[position] != alpha {
			t.Fatal("saved merged alpha is stale")
		}
		for component := range 3 {
			want := byte(math.RoundToEven(float64(live.Pix[position*4+component])*float64(alpha)/255 +
				255 - float64(alpha)))
			if merged.Channels[component].Data[position] != want {
				t.Fatal("saved merged RGB differs from the current feather/shadow composite")
			}
		}
	}
	reopened := lifecycleReopen(t, saved)
	actual, err := reopened.Composite(t.Context(), psdcompose.Options{ApproximateDropShadow: true})
	if err != nil || !bytes.Equal(actual.Pix, live.Pix) {
		t.Fatalf("feather/shadow pixels changed after disk save and reopen: %v", err)
	}
	return reopened
}

func vectorFeatherShadowUnknown(t *testing.T, source *psdfile.Document) []byte {
	t.Helper()
	_, block := effectSourceView(t, source)
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
	t.Fatal("feather/shadow fixture lost its unknown inactive effect")
	return nil
}

func TestVectorFeatherShadowEditDiskSaveReopenContinuePSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, mixed := range []bool{false, true} {
				for _, dual := range []bool{false, true} {
					for _, radius := range []float64{.5, 1.5, 3} {
						for stage := range 4 {
							t.Run(fmt.Sprintf("v%d/c%d/mixed%t/dual%t/f%g/stage%d",
								version, compression, mixed, dual, radius, stage), func(t *testing.T) {
								data := vectorFeatherShadowFixture(t, version, mixed, stage&1 != 0, stage&2 != 0)
								original := bytes.Clone(data)
								editor := compositeEditor(t, data)
								var previous []byte
								for round := range 2 {
									currentRadius := radius
									if round != 0 {
										currentRadius = map[float64]float64{.5: 1.5, 1.5: 3, 3: .5}[radius]
									}
									vectorFeatherShadowMask(t, editor, currentRadius, dual, round != 0)
									set, _ := effectSourceView(t, editor.source)
									shadowVectorEditDesired(set, round != 0)
									before, _ := editor.Changes()
									if err := editor.SetEffects(t.Context(), 1, *set); !errors.Is(err, ErrUnsupported) {
										t.Fatalf("missing explicit edit approximation was accepted: %v", err)
									}
									if after, _ := editor.Changes(); after != before {
										t.Fatal("strict effect refusal changed current mask state")
									}
									if err := editor.SetEffectsWithOptions(t.Context(), 1, *set,
										EffectsEditOptions{ApproximateDropShadow: true}); err != nil {
										t.Fatal(err)
									}
									desired := *set
									desired.Items = slices.Clone(set.Items)
									set.Items[0].Opacity = 0
									if _, err := editor.Composite(t.Context()); !errors.Is(err, ErrUnsupported) {
										t.Fatalf("edit policy leaked into the composite call: %v", err)
									}
									live, err := editor.Composite(t.Context(), psdcompose.Options{ApproximateDropShadow: true})
									if err != nil {
										t.Fatal(err)
									}
									if round != 0 && bytes.Equal(live.Pix, previous) {
										t.Fatal("continued radius, rectangle and shadow edits retained earlier pixels")
									}
									previous = bytes.Clone(live.Pix)
									var blocked bytes.Buffer
									if err := editor.WritePSD(t.Context(), &blocked); !errors.Is(err, ErrUnsupported) ||
										blocked.Len() != 0 {
										t.Fatalf("save policy was inferred or wrote a partial file: %v", err)
									}
									editor = vectorFeatherShadowSaved(t, editor, version, compression, desired, dual)
								}
								if !bytes.Equal(original, data) {
									t.Fatal("feather/shadow editing mutated immutable source bytes")
								}
							})
						}
					}
				}
			}
		}
	}
}

func TestVectorFeatherShadowRejectedEditsPreserveCurrentState(t *testing.T) {
	for _, scenario := range []string{"cancel", "budget", "read-failure", "unsupported-radius", "fractional"} {
		t.Run(scenario, func(t *testing.T) {
			data := vectorFeatherShadowFixture(t, 1, true, false, false)
			editor, source, reader := compositeLayerOnlyEditor(t, data)
			vectorFeatherShadowMask(t, editor, 1.5, true, false)
			set, block := effectSourceView(t, source)
			shadowVectorEditDesired(set, false)
			if err := editor.SetEffectsWithOptions(t.Context(), 1, *set,
				EffectsEditOptions{ApproximateDropShadow: true}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := ErrUnsupported
			switch scenario {
			case "cancel":
				cancel()
				want = context.Canceled
			case "budget":
				editor.roots[0].setSemanticCost("featherShadowBudget", source.Limits().MaxDecodeBytes-1024)
				want = psdfile.ErrLimit
			case "read-failure":
				reader.failure, reader.failureCause = block.Data, io.ErrClosedPipe
				want = io.ErrClosedPipe
			case "unsupported-radius":
				settings, err := editor.PixelMask(t.Context(), 1)
				if err != nil {
					t.Fatal(err)
				}
				settings.VectorFeather = new(float64(2))
				if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
					t.Fatal(err)
				}
			case "fractional":
				mask := disabledPixelFeatherVector(image.Rect(4, 3, 26, 13))
				knot := &mask.Subpaths[0].Knots[0]
				knot.Anchor.X, knot.Preceding.X, knot.Leaving.X = 4.5, 4.5, 4.5
				if err := editor.SetVectorMask(t.Context(), 1, mask); err != nil {
					t.Fatal(err)
				}
			}
			shadowVectorEditDesired(set, true)
			node := editor.roots[0]
			before, _ := editor.Changes()
			owned, effects, maskBytes := editor.ownedBytesLocked(nil), node.effects, bytes.Clone(node.maskData)
			additional := make(map[int64][]byte, len(node.additional))
			for offset, payload := range node.additional {
				additional[offset] = bytes.Clone(payload)
			}
			if err := editor.SetEffectsWithOptions(ctx, 1, *set,
				EffectsEditOptions{ApproximateDropShadow: true}); !errors.Is(err, want) {
				t.Fatalf("rejected current feather/shadow edit = %v, want %v", err, want)
			}
			if after, _ := editor.Changes(); after != before || editor.ownedBytesLocked(nil) != owned ||
				node.effects != effects || !bytes.Equal(node.maskData, maskBytes) ||
				!reflect.DeepEqual(node.additional, additional) {
				t.Fatal("rejected feather/shadow edit changed current pixels, metadata, budget or revision")
			}
		})
	}
}

func TestVectorFeatherShadowStrictSavePreservesExistingFile(t *testing.T) {
	editor := compositeEditor(t, vectorFeatherShadowFixture(t, 1, true, false, false))
	vectorFeatherShadowMask(t, editor, 1.5, true, false)
	set, _ := effectSourceView(t, editor.source)
	shadowVectorEditDesired(set, false)
	if err := editor.SetEffectsWithOptions(t.Context(), 1, *set,
		EffectsEditOptions{ApproximateDropShadow: true}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "retained.psd")
	sentinel := []byte("existing destination must survive rejected automatic approximation")
	if err := os.WriteFile(path, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := editor.Changes()
	if err := editor.Save(t.Context(), path); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("strict save inherited the preceding edit approximation: %v", err)
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, sentinel) {
		t.Fatalf("rejected strict save replaced the existing file: %v", err)
	}
	if after, _ := editor.Changes(); after != before {
		t.Fatal("strict save changed the editor revision or dirty state")
	}
}

func TestVectorFeatherShadowClearingSourceFeatherRestoresUnfeatheredScope(t *testing.T) {
	editor := compositeEditor(t, vectorFeatherShadowFixture(t, 2, true, false, false))
	vectorFeatherShadowMask(t, editor, 1.5, true, false)
	set, _ := effectSourceView(t, editor.source)
	shadowVectorEditDesired(set, false)
	policy := EffectsEditOptions{ApproximateDropShadow: true}
	if err := editor.SetEffectsWithOptions(t.Context(), 1, *set, policy); err != nil {
		t.Fatal(err)
	}
	reopened := vectorFeatherShadowSaved(t, editor, 2, 3, *set, true)
	mask := disabledPixelFeatherVector(image.Rect(4, 3, 26, 13))
	for _, index := range []int{0, 3} {
		knot := &mask.Subpaths[0].Knots[index]
		knot.Anchor.X, knot.Preceding.X, knot.Leaving.X = 4.5, 4.5, 4.5
	}
	if err := reopened.SetVectorMask(t.Context(), 1, mask); err != nil {
		t.Fatal(err)
	}
	desired, _ := effectSourceView(t, reopened.source)
	shadowVectorEditDesired(desired, true)
	vectorShadowGateReject(t, reopened, t.Context(), desired, ErrUnsupported)
	settings, err := reopened.PixelMask(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	settings.Feather, settings.VectorFeather = new(float64(0)), new(float64(0))
	if err := reopened.SetPixelMask(t.Context(), 1, settings); err != nil {
		t.Fatal(err)
	}
	if err := reopened.SetEffectsWithOptions(t.Context(), 1, *desired, policy); err != nil {
		t.Fatalf("clearing known source feather did not restore unfeathered fractional geometry: %v", err)
	}
	vectorFeatherShadowSaved(t, reopened, 2, 1, *desired, true)
}
