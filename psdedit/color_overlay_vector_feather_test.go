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
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func featherOverlayEditor(t *testing.T, version uint16, radius float64, retainPixel bool) *Editor {
	t.Helper()
	pixels := image.NewNRGBA(image.Rect(0, 0, 32, 16))
	for index := 0; index < len(pixels.Pix); index += 4 {
		copy(pixels.Pix[index:], []byte{200, 100, 40, 127})
	}
	tags := compositeFixtureTag("lfx2", colorOverlayPayload(t))
	tags = append(tags, compositeFixtureTag("lrFX", colorOverlayLegacy())...)
	record, payload := smartEditLayer(version, "Feather overlay", 73, pixels.Rect, pixels, tags, false)
	info := binary.BigEndian.AppendUint16(nil, 0xffff)
	info = append(info, record...)
	info = append(info, payload...)
	if len(info)%2 != 0 {
		info = append(info, 0)
	}
	data := smartEditDocument(version, 32, 16, 4, info, nil,
		append([]byte{0, 0}, bytes.Repeat([]byte{0xa5}, 32*16*4)...))
	editor := compositeEditor(t, data)
	mask := disabledPixelFeatherVector(image.Rect(4, 3, 26, 13))
	if err := editor.AddVectorMask(t.Context(), 1, mask); err != nil {
		t.Fatal(err)
	}
	plane := psdfile.Plane{Depth: 8, Width: 32, Height: 16, RowBytes: 32,
		Data: bytes.Repeat([]byte{37}, 32*16)}
	if err := editor.AddPixelMask(t.Context(), 1, pixels.Rect, plane,
		PixelMaskSettings{Disabled: true, Invert: true, Density: new(uint8(137)), Feather: new(float64(1000)),
			VectorFeather: new(radius)}); err != nil {
		t.Fatal(err)
	}
	if !retainPixel {
		if err := editor.RemovePixelMask(t.Context(), 1); err != nil {
			t.Fatal(err)
		}
	}
	return editor
}

func TestColorOverlayVectorFeatherZeroOpacityAndCurrentRadiusSaveReopen(t *testing.T) {
	editor := featherOverlayEditor(t, 1, .5, true)
	for index, step := range []struct {
		radius, opacity float64
		want            color.NRGBA
	}{
		{.5, 0, color.NRGBA{200, 100, 40, 0}},
		{1.5, .5, color.NRGBA{130, 140, 130, 0}},
		{3, 0, color.NRGBA{200, 100, 40, 0}},
		{.5, .5, color.NRGBA{130, 140, 130, 0}},
	} {
		settings, err := editor.PixelMask(t.Context(), 1)
		if err != nil {
			t.Fatal(err)
		}
		settings.VectorFeather = new(step.radius)
		if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
			t.Fatal(err)
		}
		set, _ := effectSourceView(t, editor.source)
		set.Items[0].Color = [3]float64{60.0 / 255, 180.0 / 255, 220.0 / 255}
		set.Items[0].Opacity = step.opacity
		if err := editor.SetEffects(t.Context(), 1, *set); err != nil {
			t.Fatalf("zero-opacity or current radius edit read stale feather settings: %v", err)
		}
		pixels, err := editor.Composite(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		featherOverlayAssertPixels(t, pixels, step.radius, step.want)
		editor = featherOverlaySaveReopen(t, editor, uint16(index), true)
	}
}

func TestColorOverlayVectorFeatherUsesEditedRectangleAndRadiusAfterReopen(t *testing.T) {
	editor := featherOverlayEditor(t, 2, 1.5, true)
	editor = featherOverlaySaveReopen(t, editor, 1, true)
	before, err := editor.Composite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := editor.SetVectorMask(t.Context(), 1,
		disabledPixelFeatherVector(image.Rect(-4, -4, 12, 12))); err != nil {
		t.Fatal(err)
	}
	settings, err := editor.PixelMask(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	settings.VectorFeather = new(float64(3))
	if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
		t.Fatal(err)
	}
	set, _ := effectSourceView(t, editor.source)
	set.Items[0].Color, set.Items[0].Opacity = [3]float64{0, 0, 220.0 / 255}, .5
	if err := editor.SetEffects(t.Context(), 1, *set); err != nil {
		t.Fatalf("edited rectangle/radius read stale source geometry: %v", err)
	}
	actual, err := editor.Composite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(actual.Pix, before.Pix) {
		t.Fatal("owned rectangle/radius did not change the composed pixels")
	}
	// Independent native controls include an integer rectangle that extends beyond the document canvas.
	for index, point := range []image.Point{image.Pt(0, 0), image.Pt(3, 8), image.Pt(4, 8),
		image.Pt(15, 8), image.Pt(9, 7)} {
		want := float64([]byte{223, 223, 223, 28, 189}[index]) * 127 / 255
		pixel := actual.NRGBAAt(point.X, point.Y)
		if math.Abs(float64(pixel.A)-want) > 2 || pixel.R != 100 || pixel.G != 50 || pixel.B != 130 {
			t.Fatalf("edited feather/overlay at %v = %v, native-scaled alpha = %g", point, pixel, want)
		}
	}
	featherOverlaySaveReopen(t, editor, 3, true)
}

func featherOverlayAssertPixels(t *testing.T, pixels *image.NRGBA, radius float64, expected color.NRGBA) {
	t.Helper()
	// Native alpha controls calibrate the existing rectangle field; the partial source scales that field.
	points := []image.Point{image.Pt(0, 0), image.Pt(3, 8), image.Pt(4, 8), image.Pt(15, 8), image.Pt(9, 7)}
	controls := map[float64][]byte{
		.5: {0, 56, 199, 255, 255}, 1.5: {0, 95, 160, 255, 255}, 3: {7, 101, 130, 231, 224},
	}
	for index, point := range points {
		want := float64(controls[radius][index]) * 127 / 255
		if actual := pixels.NRGBAAt(point.X, point.Y).A; math.Abs(float64(actual)-want) > 2 {
			t.Fatalf("feather %g at %v: alpha = %d, native-scaled control = %g", radius, point, actual, want)
		}
	}
	for offset := 0; offset < len(pixels.Pix); offset += 4 {
		if pixels.Pix[offset+3] == 0 {
			continue
		}
		if !bytes.Equal(pixels.Pix[offset:offset+3], []byte{expected.R, expected.G, expected.B}) {
			t.Fatalf("interior overlay changed the feather coverage or RGB at sample %d", offset/4)
		}
	}
}

func featherOverlaySaveReopen(
	t *testing.T, editor *Editor, compression uint16, retainPixel bool,
) *Editor {
	t.Helper()
	expected, err := editor.Composite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state, err := editor.pixelMaskStateLocked(t.Context(), editor.roots[0])
	if err != nil {
		t.Fatal(err)
	}
	extension := map[uint16]string{1: ".psd", 2: ".psb"}[editor.source.Header.Version]
	path := filepath.Join(t.TempDir(), "feather-overlay"+extension)
	if err := editor.Save(t.Context(), path, SaveOptions{Compression: compression}); err != nil {
		t.Fatalf("automatic feather/overlay save requires an external composite: %v", err)
	}
	saved, err := psdfile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = saved.Close() })
	if saved.Header.Version != editor.source.Header.Version {
		t.Fatal("feather/overlay save changed the PSD/PSB format")
	}
	maskData, err := saved.ReadSpan(saved.Layers[0].MaskData)
	if err != nil || !bytes.Equal(maskData, state.data) {
		t.Fatalf("effect save rewrote current shared mask metadata: %v", err)
	}
	if retainPixel {
		channelIndex := -1
		for index, channel := range saved.Layers[0].Channels {
			if channel.ID == state.channelID {
				channelIndex = index
			}
		}
		if channelIndex < 0 {
			t.Fatal("saved feather/overlay lost the retained disabled user-mask channel")
		}
		samples, err := saved.DecodeChannel(t.Context(), 0, channelIndex)
		if err != nil || !bytes.Equal(samples.Data, bytes.Repeat([]byte{37}, 32*16)) {
			t.Fatalf("feather/overlay save changed the disabled user-mask samples: %v", err)
		}
	} else {
		for _, channel := range saved.Layers[0].Channels {
			if channel.ID == -2 || channel.ID == -3 {
				t.Fatal("feather/overlay save restored a removed user mask")
			}
		}
	}
	merged, err := saved.DecodeComposite(t.Context())
	if err != nil || len(merged.Channels) != 4 {
		t.Fatalf("feather/overlay save has no generated RGBA preview: %v", err)
	}
	for index, alpha := range merged.Channels[3].Data {
		if alpha != expected.Pix[index*4+3] {
			t.Fatal("saved preview retained stale vector feather alpha")
		}
		for component := range 3 {
			want := byte(math.RoundToEven(float64(expected.Pix[index*4+component])*float64(alpha)/255 + 255 - float64(alpha)))
			if merged.Channels[component].Data[index] != want {
				t.Fatal("saved preview retained stale overlay color")
			}
		}
	}
	reopened := lifecycleReopen(t, saved)
	actual, err := reopened.Composite(t.Context())
	if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
		t.Fatalf("saved feather/overlay metadata no longer reconstructs the edited pixels: %v", err)
	}
	return reopened
}

func TestColorOverlayCalibratedVectorFeatherEditSaveReopenContinuePSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, radius := range []float64{.5, 1.5, 3} {
				for _, retainPixel := range []bool{false, true} {
					t.Run(fmt.Sprintf("v%d/c%d/feather%g/pixel%t", version, compression, radius, retainPixel),
						func(t *testing.T) {
							editor := featherOverlayEditor(t, version, radius, retainPixel)
							for _, step := range []struct {
								rgb     [3]float64
								opacity float64
								want    color.NRGBA
							}{
								{[3]float64{60.0 / 255, 180.0 / 255, 220.0 / 255}, .5, color.NRGBA{130, 140, 130, 0}},
								{[3]float64{20.0 / 255, 220.0 / 255, 100.0 / 255}, .25, color.NRGBA{155, 130, 55, 0}},
							} {
								set, _ := effectSourceView(t, editor.source)
								set.Items[0].Color, set.Items[0].Opacity = step.rgb, step.opacity
								if err := editor.SetEffects(t.Context(), 1, *set); err != nil {
									t.Fatalf("calibrated current feather blocked a pure overlay edit: %v", err)
								}
								pixels, err := editor.Composite(t.Context())
								if err != nil {
									t.Fatal(err)
								}
								featherOverlayAssertPixels(t, pixels, radius, step.want)
								editor = featherOverlaySaveReopen(t, editor, compression, retainPixel)
								persisted, _ := effectSourceView(t, editor.source)
								if persisted.Items[0].Color != set.Items[0].Color || persisted.Items[0].Opacity != step.opacity {
									t.Fatal("saved overlay parameters differ from the accepted edit")
								}
							}
						})
				}
			}
		}
	}
}

func featherOverlayReject(t *testing.T, editor *Editor, ctx context.Context, set *psdeffects.Set, want error) {
	t.Helper()
	node := editor.roots[0]
	before, _ := editor.Changes()
	owned, effects := editor.ownedBytesLocked(nil), node.effects
	additional := make(map[int64][]byte, len(node.additional))
	for offset, data := range node.additional {
		additional[offset] = bytes.Clone(data)
	}
	if err := editor.SetEffects(ctx, 1, *set); !errors.Is(err, want) {
		t.Fatalf("rejected feather/overlay edit = %v, want %v", err, want)
	}
	if after, _ := editor.Changes(); after != before || editor.ownedBytesLocked(nil) != owned ||
		node.effects != effects || len(node.additional) != len(additional) {
		t.Fatal("rejected feather/overlay edit changed revision, owned memory or effect overlays")
	}
	for offset, data := range additional {
		if !bytes.Equal(node.additional[offset], data) {
			t.Fatal("rejected feather/overlay edit rewrote an existing additional block")
		}
	}
}

func TestColorOverlayVectorFeatherCurrentQualificationsAndFailuresAreAtomic(t *testing.T) {
	for _, scenario := range []string{"active-pixel", "radius", "fraction", "curve", "constant", "multiple",
		"unknown-source", "cancel", "budget"} {
		t.Run(scenario, func(t *testing.T) {
			editor := featherOverlayEditor(t, 1, 1.5, true)
			set, _ := effectSourceView(t, editor.source)
			set.Items[0].Color, set.Items[0].Opacity = [3]float64{0, 0, 1}, .5
			if err := editor.SetEffects(t.Context(), 1, *set); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := ErrUnsupported
			switch scenario {
			case "active-pixel", "radius":
				settings, err := editor.PixelMask(t.Context(), 1)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "active-pixel" {
					settings.Disabled = false
				} else {
					settings.VectorFeather = new(float64(2))
				}
				if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
					t.Fatal(err)
				}
			case "fraction", "curve", "constant", "multiple":
				mask := disabledPixelFeatherVector(image.Rect(4, 3, 26, 13))
				switch scenario {
				case "fraction":
					mask.Subpaths[0].Knots[0].Anchor.X = 4.5
					mask.Subpaths[0].Knots[0].Preceding.X = 4.5
					mask.Subpaths[0].Knots[0].Leaving.X = 4.5
				case "curve":
					mask.Subpaths[0].Knots[0].Leaving.X = 5
				case "constant":
					mask.Subpaths = nil
				case "multiple":
					mask.Subpaths = append(mask.Subpaths, mask.Subpaths[0])
				}
				if err := editor.SetVectorMask(t.Context(), 1, mask); err != nil {
					t.Fatal(err)
				}
			case "unknown-source":
				// A clean owned rectangle must not hide an immutable source with unknown path semantics.
				data := colorOverlayVectorEditFixture(t, 1, true, true, false)
				source, err := psdfile.Parse(data)
				if err != nil {
					t.Fatal(err)
				}
				for _, block := range source.Layers[0].AdditionalInfo {
					if block.Key == "vmsk" {
						binary.BigEndian.PutUint16(data[block.Data.Offset+8:], 42)
					}
				}
				_ = source.Close()
				editor = compositeEditor(t, data)
				mask, err := psdvector.DecodeMask(t.Context(), editVectorPayload(), 1, 1)
				if err != nil {
					t.Fatal(err)
				}
				if err := editor.SetVectorMask(t.Context(), 1, mask); err != nil {
					t.Fatal(err)
				}
				set, _ = effectSourceView(t, editor.source)
				set.Items[0].Color = [3]float64{0, 0, 1}
			case "cancel":
				cancel()
				want = context.Canceled
			case "budget":
				editor.roots[0].setSemanticCost("featherBudget", editor.source.Limits().MaxDecodeBytes-1024)
				want = psdfile.ErrLimit
			}
			featherOverlayReject(t, editor, ctx, set, want)
		})
	}
}
