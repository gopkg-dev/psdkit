package psdedit

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image/color"
	"math"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func colorOverlayVectorMask(t *testing.T, fraction float64) *psdvector.Mask {
	t.Helper()
	mask := lifecycleVectorMask(t)
	for _, index := range []int{1, 2} {
		knot := &mask.Subpaths[0].Knots[index]
		knot.Preceding.X, knot.Anchor.X, knot.Leaving.X = fraction, fraction, fraction
	}
	return mask
}

func colorOverlayVectorEditFixture(t *testing.T, version uint16, pixel, vector, real bool) []byte {
	t.Helper()
	tags := compositeFixtureTag("lfx2", colorOverlayPayload(t))
	tags = append(tags, compositeFixtureTag("lrFX", colorOverlayLegacy())...)
	tags = append(tags, compositeFixtureTag("pRsv", []byte{0x97, 0x41, 0x23})...)
	tags = append(tags, compositeFixtureTag("pRsv", []byte{0x5a, 0x81})...)
	if vector {
		encoded, err := psdvector.EncodeMask(t.Context(), colorOverlayVectorMask(t, .5))
		if err != nil {
			t.Fatal(err)
		}
		tags = append(tags, compositeFixtureTag("vmsk", encoded)...)
	}
	layer := compositeFixtureLayer{color: []byte{200, 100, 40, 127}, tags: tags}
	if pixel {
		layer.mask = []byte{64}
	}
	if real {
		layer.extraID = -3
	}
	data := compositeFixture(version, 8, 4, []compositeFixtureLayer{layer})
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

func colorOverlayVectorPreserved(
	t *testing.T, before, after *psdfile.Document, desired psdeffects.Effect, vectorUnchanged bool,
) {
	t.Helper()
	originalMask, err := before.ReadSpan(before.Layers[0].MaskData)
	if err != nil {
		t.Fatal(err)
	}
	savedMask, err := after.ReadSpan(after.Layers[0].MaskData)
	if err != nil || !bytes.Equal(originalMask, savedMask) {
		t.Fatalf("color-overlay edit rewrote pixel-mask metadata: %v", err)
	}
	for index, channel := range before.Layers[0].Channels {
		if channel.ID != -2 && channel.ID != -3 {
			continue
		}
		first, err := before.DecodeChannel(t.Context(), 0, index)
		if err != nil {
			t.Fatal(err)
		}
		second, err := after.DecodeChannel(t.Context(), 0, index)
		if err != nil || first.ID != second.ID || !bytes.Equal(first.Data, second.Data) {
			t.Fatalf("color-overlay edit changed user-mask samples: %v", err)
		}
	}
	selectPayloads := func(source *psdfile.Document) [][]byte {
		var payloads [][]byte
		for _, block := range source.Layers[0].AdditionalInfo {
			if block.Key != "pRsv" && (!vectorUnchanged || block.Key != "vmsk" && block.Key != "vsms") {
				continue
			}
			data, err := source.ReadSpan(block.Span)
			if err != nil {
				t.Fatal(err)
			}
			payloads = append(payloads, data)
		}
		return payloads
	}
	first, second := selectPayloads(before), selectPayloads(after)
	if len(first) != len(second) {
		t.Fatal("color-overlay edit added or lost unrelated tagged blocks")
	}
	for index := range first {
		if !bytes.Equal(first[index], second[index]) {
			t.Fatal("color-overlay edit normalized vector or duplicate opaque blocks")
		}
	}
	modern, _ := effectSourceView(t, after)
	if modern.Items[0].Color != desired.Color || modern.Items[0].Opacity != desired.Opacity ||
		modern.Items[0].Kind != psdeffects.ColorOverlay || modern.Items[0].BlendMode != "norm" {
		t.Fatal("saved modern overlay parameters differ from the requested edit")
	}
	found := false
	for _, block := range after.Layers[0].AdditionalInfo {
		if block.Key != "lrFX" {
			continue
		}
		found = true
		data, err := after.ReadSpan(block.Data)
		if err != nil || len(data) != len(colorOverlayLegacy()) || !bytes.Equal(data[:21], colorOverlayLegacy()[:21]) {
			t.Fatalf("color-overlay edit changed an unknown legacy effect record: %v", err)
		}
		payload := data[33:]
		if string(payload[4:12]) != "8BIMnorm" || payload[22] != byte(math.Round(desired.Opacity*255)) || payload[23] != 1 {
			t.Fatal("legacy overlay blend, opacity or enabled state disagrees with modern metadata")
		}
		for index, component := range desired.Color {
			want := uint16(math.Round(component * 65535))
			if binary.BigEndian.Uint16(payload[14+index*2:]) != want ||
				binary.BigEndian.Uint16(payload[26+index*2:]) != want {
				t.Fatal("legacy RGB copies disagree with the saved modern overlay")
			}
		}
	}
	if !found {
		t.Fatal("saved overlay lost the retained legacy effects block")
	}
}

func colorOverlayVectorRoundTrip(t *testing.T, editor *Editor, compression uint16, disk bool,
	expected color.NRGBA,
) (*Editor, *psdfile.Document) {
	t.Helper()
	preview, err := editor.Composite(t.Context())
	if err != nil || preview.NRGBAAt(0, 0) != expected {
		t.Fatalf("current color overlay and mask preview = %v, expected %v: %v", preview, expected, err)
	}
	var saved *psdfile.Document
	if disk {
		extension := map[uint16]string{1: ".psd", 2: ".psb"}[editor.source.Header.Version]
		path := filepath.Join(t.TempDir(), "vector-overlay"+extension)
		if err := editor.Save(t.Context(), path, SaveOptions{Compression: compression}); err != nil {
			t.Fatal(err)
		}
		saved, err = psdfile.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = saved.Close() })
	} else {
		saved = compositeSave(t, editor, SaveOptions{Compression: compression})
	}
	if saved.Header.Version != editor.source.Header.Version {
		t.Fatal("masked color-overlay edit changed PSD/PSB format")
	}
	reopened := lifecycleReopen(t, saved)
	persisted, err := reopened.Composite(t.Context())
	if err != nil || !bytes.Equal(persisted.Pix, preview.Pix) {
		t.Fatalf("saved mask/effect metadata no longer reconstructs the edited pixels: %v", err)
	}
	planes := make([]byte, 4)
	for index, component := range []byte{expected.R, expected.G, expected.B} {
		planes[index] = byte(math.RoundToEven(float64(component)*float64(expected.A)/255 + 255 - float64(expected.A)))
	}
	planes[3] = expected.A
	compositeExpect(t, saved, planes)
	return reopened, saved
}

func TestColorOverlayActiveVectorAndDualMaskEditSaveReopenPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, mode := range []string{"vector", "pixel-vector", "generated-real"} {
				t.Run(fmt.Sprintf("v%d/c%d/%s", version, compression, mode), func(t *testing.T) {
					data := colorOverlayVectorEditFixture(t, version, mode != "vector", true, mode == "generated-real")
					original := bytes.Clone(data)
					editor := compositeEditor(t, data)
					alpha := byte(64)
					switch mode {
					case "pixel-vector":
						alpha = 16
					case "generated-real":
						alpha = 32
					}
					for index, step := range []struct {
						color   [3]byte
						opacity float64
						rgb     [3]byte
					}{
						{[3]byte{60, 180, 220}, .5, [3]byte{130, 140, 130}},
						{[3]byte{20, 220, 100}, .25, [3]byte{155, 130, 55}},
					} {
						set, _ := effectSourceView(t, editor.source)
						for channel, component := range step.color {
							set.Items[0].Color[channel] = float64(component) / 255
						}
						set.Items[0].Opacity = step.opacity
						desired := set.Items[0]
						if err := editor.SetEffects(t.Context(), 1, *set); err != nil {
							t.Fatal(err)
						}
						set.Items[0].Color, set.Items[0].Opacity = [3]float64{}, 0
						expected := color.NRGBA{R: step.rgb[0], G: step.rgb[1], B: step.rgb[2], A: alpha}
						reopened, saved := colorOverlayVectorRoundTrip(t, editor, compression, index != 0, expected)
						colorOverlayVectorPreserved(t, editor.source, saved, desired, true)
						editor = reopened
					}
					if !bytes.Equal(data, original) {
						t.Fatal("masked color-overlay edits mutated immutable original bytes")
					}
				})
			}
		}
	}
}

func TestColorOverlayUsesCurrentVectorLifecycleBeforeEachEdit(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, originalVector := range []bool{false, true} {
				t.Run(fmt.Sprintf("v%d/c%d/original%t", version, compression, originalVector), func(t *testing.T) {
					editor := compositeEditor(t, colorOverlayVectorEditFixture(t, version, true, originalVector, false))
					for index, step := range []struct {
						operation string
						fraction  float64
						alpha     byte
					}{
						{"AddVectorMask", .5, 16}, {"SetVectorMask", .25, 8},
						{"RemoveVectorMask", 0, 32}, {"AddVectorMask", .5, 16},
					} {
						var err error
						switch step.operation {
						case "AddVectorMask":
							if index == 0 && originalVector {
								err = editor.SetVectorMask(t.Context(), 1, colorOverlayVectorMask(t, step.fraction))
							} else {
								err = editor.AddVectorMask(t.Context(), 1, colorOverlayVectorMask(t, step.fraction))
							}
						case "SetVectorMask":
							err = editor.SetVectorMask(t.Context(), 1, colorOverlayVectorMask(t, step.fraction))
						case "RemoveVectorMask":
							err = editor.RemoveVectorMask(t.Context(), 1)
						}
						if err != nil {
							t.Fatal(err)
						}
						set, _ := effectSourceView(t, editor.source)
						set.Items[0].Color = [3]float64{float64(20+index*20) / 255, 60.0 / 255, 220.0 / 255}
						set.Items[0].Opacity = 1
						if err := editor.SetEffects(t.Context(), 1, *set); err != nil {
							t.Fatalf("SetEffects after %s read stale source masks: %v", step.operation, err)
						}
						expected := color.NRGBA{R: byte(20 + index*20), G: 60, B: 220, A: step.alpha}
						_, saved := colorOverlayVectorRoundTrip(t, editor, compression, index%2 != 0, expected)
						colorOverlayVectorPreserved(t, editor.source, saved, set.Items[0], false)
						count := lifecycleVectorBlocks(saved, 0)
						if step.operation == "RemoveVectorMask" && count != 0 ||
							step.operation != "RemoveVectorMask" && count != 1 {
							t.Fatal("saved current vector-mask lifecycle differs from the edited state")
						}
					}
				})
			}
		}
	}
}

func TestColorOverlayVectorEditRejectsCurrentFeatherAndOtherFamiliesAtomically(t *testing.T) {
	editor := compositeEditor(t, colorOverlayVectorEditFixture(t, 1, true, true, false))
	set, _ := effectSourceView(t, editor.source)
	set.Items[0].Color, set.Items[0].Opacity = [3]float64{0, 0, 1}, .5
	if err := editor.SetEffects(t.Context(), 1, *set); err != nil {
		t.Fatal(err)
	}
	settings, err := editor.PixelMask(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	settings.VectorFeather = new(.5)
	if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
		t.Fatal(err)
	}
	before, _ := editor.Changes()
	if err := editor.SetEffects(t.Context(), 1, *set); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("identical effect parameters bypassed current vector-feather validation: %v", err)
	}
	if after, _ := editor.Changes(); after != before {
		t.Fatal("unsupported current feather changed the effects transaction")
	}
	settings.VectorFeather = new(float64(0))
	if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
		t.Fatal(err)
	}
	set.Items[0].Color = [3]float64{0, 1, 0}
	if err := editor.SetEffects(t.Context(), 1, *set); err != nil {
		t.Fatalf("cleared current vector feather still blocked a supported edit: %v", err)
	}
	settings.Feather = new(.5)
	if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
		t.Fatal(err)
	}
	before, _ = editor.Changes()
	if err := editor.SetEffects(t.Context(), 1, *set); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("active vector plus current pixel feather bypassed qualification: %v", err)
	}
	if after, _ := editor.Changes(); after != before {
		t.Fatal("unsupported current pixel feather changed the effects transaction")
	}
	settings.Feather = new(float64(0))
	if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
		t.Fatal(err)
	}
	set.Items[0].Color = [3]float64{1, 0, 0}
	if err := editor.SetEffects(t.Context(), 1, *set); err != nil {
		t.Fatalf("cleared current pixel feather still blocked a supported edit: %v", err)
	}
	tags := compositeFixtureTag("vmsk", editVectorPayload())
	tags = append(tags, compositeFixtureTag("lfx2", effectEditPayload(t, false))...)
	stroke := compositeEditor(t, compositeFixture(1, 8, 4, []compositeFixtureLayer{{
		color: []byte{200, 100, 40, 255}, tags: tags,
	}}))
	strokeSet, _ := effectSourceView(t, stroke.source)
	strokeSet.Items[0].Color = [3]float64{0, 0, 1}
	before, _ = stroke.Changes()
	if err := stroke.SetEffects(t.Context(), 1, *strokeSet); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("active vector masks unintentionally enabled another effect family: %v", err)
	}
	if after, _ := stroke.Changes(); after != before {
		t.Fatal("unsupported vector/effect family partially committed")
	}
}
