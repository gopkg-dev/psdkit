package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"path/filepath"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

type shadowVectorFixtureOptions struct {
	mixed, vector, notLinked, relative bool
	mode                               string
	vectorGlobal, pixelGlobal          *bool
	extra                              []byte
}

func shadowVectorEditMask(t *testing.T, width float64, notLinked bool) *psdvector.Mask {
	t.Helper()
	mask, err := psdvector.DecodeMask(t.Context(), editVectorPayload(), 8, 6)
	if err != nil {
		t.Fatal(err)
	}
	mask.NotLinked = notLinked
	for index, point := range [4][2]float64{{3, 2}, {3 + width, 2}, {3 + width, 4}, {3, 4}} {
		knot := &mask.Subpaths[0].Knots[index]
		knot.Preceding.X, knot.Anchor.X, knot.Leaving.X = point[0], point[0], point[0]
		knot.Preceding.Y, knot.Anchor.Y, knot.Leaving.Y = point[1], point[1], point[1]
	}
	return mask
}

func shadowVectorEditFixture(t *testing.T, version uint16, options shadowVectorFixtureOptions) []byte {
	t.Helper()
	descriptor := &psdfile.Descriptor{ClassID: "Lefx", Entries: []psdfile.DescriptorEntry{
		{Key: "masterFXSwitch", Type: "bool", Value: true},
		{Key: "DrSh", Type: "Objc", Value: mixedMaskEffectObject("DrSh")},
	}}
	legacy := effectLegacyFixture(true)
	if options.mixed {
		descriptor.Entries = append(descriptor.Entries,
			psdfile.DescriptorEntry{Key: "SoFi", Type: "Objc", Value: mixedMaskEffectObject("SoFi")})
		legacy = append(colorOverlayLegacy(), legacy[21:]...)
		binary.BigEndian.PutUint16(legacy[2:], 3)
	}
	descriptor.Entries = append(descriptor.Entries, psdfile.DescriptorEntry{
		Key: "futureInactive", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "futureFx",
			Entries: []psdfile.DescriptorEntry{
				{Key: "enab", Type: "bool", Value: false},
				{Key: "opaque", Type: "tdta", Value: []byte{0x13, 0x7b, 0x97}},
				{Key: "opaque", Type: "long", Value: int32(43)},
			}},
	})
	encoded, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(make([]byte, 4), encoded...)
	payload = append(payload, make([]byte, (4-len(payload)%4)%4)...)
	tags := compositeFixtureTag("lfx2", payload)
	tags = append(tags, compositeFixtureTag("lrFX", legacy)...)
	tags = append(tags, compositeFixtureTag("pRsv", []byte{0xa1, 0x53, 0x19})...)
	if options.vector {
		vector, err := psdvector.EncodeMask(t.Context(), shadowVectorEditMask(t, 1.25, options.notLinked))
		if err != nil {
			t.Fatal(err)
		}
		tags = append(tags, compositeFixtureTag("vmsk", vector)...)
	}
	for _, flag := range []struct {
		key   string
		value *bool
	}{{"vmgm", options.vectorGlobal}, {"lmgm", options.pixelGlobal}} {
		if flag.value == nil {
			continue
		}
		value := byte(0)
		if *flag.value {
			value = 1
		}
		tags = append(tags, compositeFixtureTag(flag.key, []byte{value, 0, 0, 0})...)
	}
	tags = append(tags, options.extra...)
	pixels := smartEditImage([4]color.NRGBA{{200, 100, 40, 127}, {80, 140, 90, 180},
		{40, 130, 220, 255}, {210, 60, 20, 128}})
	ids := []int16{0, 1, 2, -1}
	if options.mode != "vector" {
		ids = append(ids, -2)
	}
	if options.mode == "generated-real" {
		ids = append(ids, -3)
	}
	var record, channels bytes.Buffer
	for _, coordinate := range []int32{2, 3, 4, 5} {
		editorFixtureNumber(&record, coordinate)
	}
	editorFixtureNumber(&record, uint16(len(ids)))
	for component, id := range ids {
		editorFixtureNumber(&record, id)
		record.Write(writerLinkedLength(nil, version, 6))
		editorFixtureNumber(&channels, uint16(0))
		switch {
		case id == -2 && options.mode == "generated-real":
			channels.Write([]byte{17, 17, 17, 17})
		case id == -2 || id == -3:
			channels.Write([]byte{255, 128, 0, 255})
		default:
			for index := range 4 {
				channels.WriteByte(pixels.Pix[index*4+component])
			}
		}
	}
	record.WriteString("8BIMnorm")
	record.Write([]byte{255, 0, 0, 0})
	var extra bytes.Buffer
	if options.mode == "vector" {
		editorFixtureNumber(&extra, uint32(0))
	} else {
		length := uint32(20)
		if options.mode == "generated-real" {
			length = 36
		}
		editorFixtureNumber(&extra, length)
		for _, coordinate := range []int32{2, 3, 4, 5} {
			editorFixtureNumber(&extra, coordinate)
		}
		flags := byte(0)
		if options.relative {
			flags = 1
		}
		if options.mode == "generated-real" {
			extra.Write([]byte{255, 8, flags, 255})
			for _, coordinate := range []int32{2, 3, 4, 5} {
				editorFixtureNumber(&extra, coordinate)
			}
		} else {
			extra.Write([]byte{255, flags, 0, 0})
		}
	}
	editorFixtureNumber(&extra, uint32(0))
	extra.Write([]byte{1, 'S', 0, 0})
	extra.Write(tags)
	editorFixtureNumber(&record, uint32(extra.Len()))
	record.Write(extra.Bytes())
	info := binary.BigEndian.AppendUint16(nil, 0xffff)
	info = append(info, record.Bytes()...)
	info = append(info, channels.Bytes()...)
	if len(info)%2 != 0 {
		info = append(info, 0)
	}
	return smartEditDocument(version, 8, 6, 4, info, nil,
		append([]byte{0, 0}, bytes.Repeat([]byte{0xa5}, 8*6*4)...))
}

func shadowVectorEditDesired(set *psdeffects.Set, second bool) {
	mixedMaskEffectsDesired(set)
	if !second {
		return
	}
	for index := range set.Items {
		effect := &set.Items[index]
		switch effect.Kind {
		case psdeffects.DropShadow:
			effect.Distance, effect.Size, effect.Opacity, effect.Color = 1.5, .75, .75, [3]float64{.25, 0, .5}
		case psdeffects.ColorOverlay:
			effect.Opacity, effect.Color = .25, [3]float64{0, 1, 0}
		}
	}
}

func TestShadowVectorEditPSDPSBCompressionStagesAndContinuedDiskSave(t *testing.T) {
	stages := []struct {
		name      string
		global    *bool
		notLinked bool
	}{{"shape", new(false), true}, {"global", new(true), false},
		{"absent-linked", nil, false}, {"absent-unlinked", nil, true}}
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, mixed := range []bool{false, true} {
				for _, mode := range []string{"vector", "ordinary-dual", "generated-real"} {
					for _, stage := range stages {
						t.Run(fmt.Sprintf("v%d/c%d/mixed%t/%s/%s", version, compression, mixed, mode, stage.name),
							func(t *testing.T) {
								data := shadowVectorEditFixture(t, version, shadowVectorFixtureOptions{
									mixed: mixed, vector: true, notLinked: stage.notLinked, mode: mode,
									vectorGlobal: stage.global, pixelGlobal: new(false),
								})
								original := bytes.Clone(data)
								editor := compositeEditor(t, data)
								var previousPixels []byte
								for round := range 2 {
									set, _ := effectSourceView(t, editor.source)
									shadowVectorEditDesired(set, round != 0)
									before, _ := editor.Changes()
									if err := editor.SetEffects(t.Context(), 1, *set); !errors.Is(err, ErrUnsupported) {
										t.Fatalf("missing edit approximation policy was accepted: %v", err)
									}
									if after, _ := editor.Changes(); after != before {
										t.Fatal("policy refusal changed the editor")
									}
									if err := editor.SetEffectsWithOptions(t.Context(), 1, *set,
										EffectsEditOptions{ApproximateDropShadow: true}); err != nil {
										t.Fatal(err)
									}
									desired := *set
									desired.Items = slices.Clone(set.Items)
									set.Items[0].Color, set.Items[0].Opacity = [3]float64{1, 1, 1}, 0
									if _, err := editor.Composite(t.Context()); !errors.Is(err, ErrUnsupported) {
										t.Fatalf("missing composite approximation policy was accepted: %v", err)
									}
									live, err := editor.Composite(t.Context(), psdcompose.Options{ApproximateDropShadow: true})
									if err != nil {
										t.Fatal(err)
									}
									if round != 0 && bytes.Equal(live.Pix, previousPixels) {
										t.Fatal("continued shadow edit retained the earlier artwork")
									}
									previousPixels = bytes.Clone(live.Pix)
									var blocked bytes.Buffer
									if err := editor.Write(t.Context(), &blocked); !errors.Is(err, ErrUnsupported) || blocked.Len() != 0 {
										t.Fatalf("missing save approximation policy wrote a preview: %v", err)
									}
									extension := map[uint16]string{1: ".psd", 2: ".psb"}[version]
									path := filepath.Join(t.TempDir(), fmt.Sprintf("shadow-vector-%d%s", round, extension))
									if err := editor.Save(t.Context(), path,
										SaveOptions{Compression: compression, ApproximateDropShadow: true}); err != nil {
										t.Fatal(err)
									}
									saved, err := psdfile.Open(path)
									if err != nil {
										t.Fatal(err)
									}
									t.Cleanup(func() { _ = saved.Close() })
									if saved.Header.Version != version {
										t.Fatal("vector shadow edit changed PSD/PSB format")
									}
									mixedMaskEffectsPreserved(t, editor.source, saved)
									editor = mixedMaskEffectsExpectSaved(t, editor, saved, desired)
								}
								if !bytes.Equal(original, data) {
									t.Fatal("vector shadow editing mutated immutable source bytes")
								}
							})
					}
				}
			}
		}
	}
}

func shadowVectorEditedPixels(t *testing.T, options shadowVectorFixtureOptions) *image.NRGBA {
	t.Helper()
	editor := compositeEditor(t, shadowVectorEditFixture(t, 1, options))
	set, _ := effectSourceView(t, editor.source)
	shadowVectorEditDesired(set, false)
	if err := editor.SetEffectsWithOptions(t.Context(), 1, *set,
		EffectsEditOptions{ApproximateDropShadow: true}); err != nil {
		t.Fatal(err)
	}
	pixels, err := editor.Composite(t.Context(), psdcompose.Options{ApproximateDropShadow: true})
	if err != nil {
		t.Fatal(err)
	}
	return pixels
}

func TestShadowVectorEditMaskStagesAndGeneratedCoverageArePreserved(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, relative := range []bool{false, true} {
			for _, pixelGlobal := range []*bool{nil, new(false), new(true)} {
				for _, notLinked := range []bool{false, true} {
					options := shadowVectorFixtureOptions{mixed: mixed, vector: true, notLinked: notLinked,
						relative: relative, mode: "ordinary-dual", pixelGlobal: pixelGlobal}
					absent := shadowVectorEditedPixels(t, options)
					options.vectorGlobal, options.notLinked = new(notLinked), !notLinked
					explicit := shadowVectorEditedPixels(t, options)
					if !bytes.Equal(absent.Pix, explicit.Pix) {
						t.Fatal("absent vmgm ignored NotLinked or explicit vmgm failed to override it")
					}
					options.mode = "generated-real"
					generated := shadowVectorEditedPixels(t, options)
					if !bytes.Equal(generated.Pix, explicit.Pix) {
						t.Fatal("generated vector cache replaced or multiplied real user-mask coverage")
					}
					if pixelGlobal == nil {
						options.mode, options.pixelGlobal = "ordinary-dual", new(relative)
						legacy := shadowVectorEditedPixels(t, options)
						if !bytes.Equal(legacy.Pix, explicit.Pix) {
							t.Fatal("absent lmgm no longer follows the existing Relative-mask rule")
						}
					}
				}
			}
		}
	}
	options := shadowVectorFixtureOptions{mixed: true, vector: true, mode: "ordinary-dual",
		vectorGlobal: new(false), pixelGlobal: new(false)}
	shaped := shadowVectorEditedPixels(t, options)
	options.vectorGlobal = new(true)
	global := shadowVectorEditedPixels(t, options)
	outside := false
	for y := range 6 {
		for x := range 8 {
			if image.Pt(x, y).In(image.Rect(3, 2, 5, 4)) {
				continue
			}
			outside = outside || shaped.NRGBAAt(x, y).A != 0
			if global.NRGBAAt(x, y).A != 0 {
				t.Fatal("global vector mask did not clip the completed effect outside its bounds")
			}
		}
	}
	if !outside || bytes.Equal(shaped.Pix, global.Pix) {
		t.Fatal("fixture did not distinguish vector source shaping from final effect clipping")
	}
}

func TestShadowVectorEditUsesCurrentAddSetRemoveGeometry(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, mixed := range []bool{false, true} {
			editor := compositeEditor(t, shadowVectorEditFixture(t, version,
				shadowVectorFixtureOptions{mixed: mixed, mode: "ordinary-dual", pixelGlobal: new(false)}))
			for index, width := range []float64{1.25, .75, 0, 1.75} {
				var err error
				switch index {
				case 0, 3:
					err = editor.AddVectorMask(t.Context(), 1, shadowVectorEditMask(t, width, false))
				case 1:
					err = editor.SetVectorMask(t.Context(), 1, shadowVectorEditMask(t, width, false))
				case 2:
					err = editor.RemoveVectorMask(t.Context(), 1)
				}
				if err != nil {
					t.Fatal(err)
				}
				set, _ := effectSourceView(t, editor.source)
				shadowVectorEditDesired(set, index%2 != 0)
				if err := editor.SetEffectsWithOptions(t.Context(), 1, *set,
					EffectsEditOptions{ApproximateDropShadow: true}); err != nil {
					t.Fatalf("shadow edit ignored current vector lifecycle step %d: %v", index, err)
				}
				saved := compositeSave(t, editor, SaveOptions{Compression: uint16(index), ApproximateDropShadow: true})
				mixedMaskEffectsExpectSaved(t, editor, saved, *set)
				count := lifecycleVectorBlocks(saved, 0)
				if width == 0 && count != 0 || width != 0 && count != 1 {
					t.Fatal("saved shadow retained a stale vector-mask block")
				}
				oldMask, _ := editor.source.ReadSpan(editor.source.Layers[0].MaskData)
				newMask, _ := saved.ReadSpan(saved.Layers[0].MaskData)
				if !bytes.Equal(oldMask, newMask) {
					t.Fatal("vector shadow lifecycle changed user-mask bytes")
				}
			}
		}
	}
}

func TestShadowVectorEditInvalidMaskAdvancedPlacedAndPoliciesAreAtomic(t *testing.T) {
	for _, scenario := range []string{"vector-feather", "pixel-feather", "source-advanced", "edited-advanced",
		"placed", "missing-policy", "cancel", "noise", "edited-multiply"} {
		t.Run(scenario, func(t *testing.T) {
			options := shadowVectorFixtureOptions{mixed: true, vector: true, mode: "ordinary-dual",
				vectorGlobal: new(false), pixelGlobal: new(false)}
			switch scenario {
			case "source-advanced":
				options.extra = compositeFixtureTag("knko", []byte{2, 0, 0, 0})
			case "placed":
				options.extra = compositeFixtureTag("SoLd", smartEditPlacement(t, 0, ""))
			}
			editor := compositeEditor(t, shadowVectorEditFixture(t, 1, options))
			set, _ := effectSourceView(t, editor.source)
			shadowVectorEditDesired(set, false)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			policy := EffectsEditOptions{ApproximateDropShadow: true}
			want := ErrUnsupported
			switch scenario {
			case "vector-feather", "pixel-feather":
				settings, err := editor.PixelMask(t.Context(), 1)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "vector-feather" {
					settings.VectorFeather = new(.5)
				} else {
					settings.Feather = new(.5)
				}
				if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
					t.Fatal(err)
				}
			case "edited-advanced":
				if err := editor.SetAdvancedBlending(t.Context(), 1,
					AdvancedBlendingOptions{Knockout: new(psdmodel.KnockoutDeep)}); err != nil {
					t.Fatal(err)
				}
			case "edited-multiply":
				if err := editor.SetBlendMode(1, "mul "); err != nil {
					t.Fatal(err)
				}
			case "missing-policy":
				policy.ApproximateDropShadow = false
			case "cancel":
				cancel()
				want = context.Canceled
			case "noise":
				set.Items[0].Noise = .25
			}
			before, _ := editor.Changes()
			owned := editor.ownedBytesLocked(nil)
			err := editor.SetEffectsWithOptions(ctx, 1, *set, policy)
			if !errors.Is(err, want) {
				t.Fatalf("unsafe vector-shadow edit = %v, want %v", err, want)
			}
			if after, _ := editor.Changes(); after != before || editor.ownedBytesLocked(nil) != owned ||
				editor.roots[0].effects != nil {
				t.Fatal("rejected vector-shadow edit changed parameters, revision or retained allocation")
			}
		})
	}
}
