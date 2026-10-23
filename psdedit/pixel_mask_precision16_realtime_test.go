package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdprecision"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func maskPrecision16Fixture(t *testing.T, version uint16, mode string) []byte {
	t.Helper()
	data := compositeFixture(version, 16, 4, []compositeFixtureLayer{
		{color: []byte{10, 20, 30, 128}},
		{color: []byte{100, 110, 120, 255}, mask: []byte{0}, blend: mode,
			tags: compositeFixtureTag("pRsv", []byte{31, 9, 0, 77})},
	})
	indexed, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = indexed.Close() }()
	for index, layer := range indexed.Layers {
		values := [][4]uint16{{32767, 16383, 49151, 32768}, {49151, 32767, 16383, 65535}}[index]
		for _, channel := range layer.Channels {
			component := int(channel.ID)
			if component == -1 {
				component = 3
			}
			if component >= 0 && component <= 3 {
				binary.BigEndian.PutUint16(data[channel.Data.Offset:], values[component])
			}
		}
	}
	return data
}

func maskPrecision16Expected(t *testing.T, mode string) *psdprecision.FloatImage {
	t.Helper()
	scene := &psdprecision.Scene{Width: 1, Height: 1}
	for index, values := range [][4]uint16{{32767, 16383, 49151, 32768}, {49151, 32767, 16383, 65535}} {
		pixels := &psdprecision.FloatImage{Width: 1, Height: 1, SourceDepth: 16, Pix: make([]float64, 4)}
		for component, value := range values {
			pixels.Pix[component] = float64(value) / 65535
		}
		blend := "norm"
		if index == 1 {
			blend = mode
		}
		scene.Layers = append(scene.Layers, &psdprecision.Layer{
			Bounds: image.Rect(0, 0, 1, 1), Pixels: pixels, BlendMode: blend,
		})
	}
	expected, err := psdprecision.RenderRGB16(nil, scene)
	if err != nil || expected.Pix[3] != 1 {
		t.Fatalf("unmasked Q15 reference is not opaque: %v", err)
	}
	return expected
}

func TestPixelMaskDisabledRGB16ExplicitSaveReopenAndContinue(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, mode := range []string{"norm", "diff"} {
				t.Run(fmt.Sprintf("v%d/c%d/%s", version, compression, mode), func(t *testing.T) {
					editor := compositeEditor(t, maskPrecision16Fixture(t, version, mode))
					expected := maskPrecision16Expected(t, mode)
					preview, err := editor.source.DecodeComposite(nil)
					if err != nil {
						t.Fatal(err)
					}
					for component := range preview.Channels {
						binary.BigEndian.PutUint16(preview.Channels[component].Data,
							uint16(math.Round(expected.Pix[component]*65535)))
					}
					for _, invert := range []bool{false, true} {
						settings, err := editor.PixelMask(nil, 2)
						if err != nil {
							t.Fatal(err)
						}
						settings.Disabled, settings.Invert = true, invert
						settings.DefaultColor = 73
						if err := editor.SetPixelMask(nil, 2, settings); err != nil {
							t.Fatal(err)
						}
						var automatic bytes.Buffer
						if err := editor.Write(nil, &automatic); !errors.Is(err, ErrUnsupported) || automatic.Len() != 0 {
							t.Fatalf("RGB16 mask metadata silently enabled an RGB8 automatic preview: %v", err)
						}
						saved := compositeSave(t, editor, SaveOptions{Compression: compression, Composite: preview})
						if saved.Header.Depth != 16 || saved.Header.Version != version ||
							saved.Layers[1].Mask.Flags&2 == 0 || (saved.Layers[1].Mask.Flags&4 != 0) != invert {
							t.Fatal("explicit save lost original depth, format or edited mask flags")
						}
						merged, err := saved.DecodeComposite(nil)
						if err != nil || !reflect.DeepEqual(merged.Channels, preview.Channels) {
							t.Fatalf("explicit source-precision composite samples changed: %v", err)
						}
						live, err := psdscene.RenderPrecision(nil, saved)
						if err != nil || !reflect.DeepEqual(live.Pix, expected.Pix) {
							t.Fatalf("reopened disabled mask did not use calibrated unmasked Q15 values: %v, %v", err, live)
						}
						tile, err := psdscene.RenderPrecisionTile(nil, saved, image.Rect(0, 0, 1, 1))
						if err != nil || !reflect.DeepEqual(tile.Image.Pix, expected.Pix) {
							t.Fatalf("source-precision tile disagrees after mask save: %v", err)
						}
						for _, block := range saved.Layers[1].AdditionalInfo {
							if block.Key == "pRsv" {
								data, err := saved.ReadSpan(block.Data)
								if err != nil || !bytes.Equal(data, []byte{31, 9, 0, 77}) {
									t.Fatalf("mask edit changed unrelated source metadata: %v", err)
								}
							}
						}
						reopened, err := New(saved)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = reopened.Close() })
						editor = reopened
					}
				})
			}
		}
	}
}

func TestPixelMaskDisabledRGB16CanceledEditIsAtomic(t *testing.T) {
	editor := compositeEditor(t, maskPrecision16Fixture(t, 1, "norm"))
	settings, err := editor.PixelMask(nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	settings.Disabled = true
	before, _ := editor.Changes()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.SetPixelMask(ctx, 2, settings); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled RGB16 mask edit was accepted: %v", err)
	}
	after, _ := editor.Changes()
	retained, err := editor.PixelMask(nil, 2)
	if before != after || err != nil || retained.Disabled {
		t.Fatalf("canceled RGB16 mask edit changed persisted metadata: %v", err)
	}
}
