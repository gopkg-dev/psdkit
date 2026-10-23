package psdedit

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdscene"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func passRangeMaskSettings(kind string) PixelMaskSettings {
	settings := PixelMaskSettings{Disabled: true, Invert: true, DefaultColor: 31,
		Density: new(uint8(193)), Feather: new(99.0)}
	if kind == "both" {
		settings.VectorDensity, settings.VectorFeather = new(uint8(137)), new(99.0)
	}
	return settings
}

func passRangeMaskVector() *psdvector.Mask {
	return &psdvector.Mask{Version: 3, Width: 1, Height: 1, Disabled: true, HasInitialFill: true}
}

func passRangeMaskMetadata(t *testing.T, source *psdfile.Document, ids []LayerID) [][]byte {
	t.Helper()
	var metadata [][]byte
	for _, id := range ids {
		layer := source.Layers[int(id)-1]
		data, err := source.ReadSpan(layer.MaskData)
		if err != nil {
			t.Fatal(err)
		}
		metadata = append(metadata, data)
		for _, block := range layer.AdditionalInfo {
			if block.Key == "vmsk" || block.Key == "vsms" {
				data, err := source.ReadSpan(block.Span)
				if err != nil {
					t.Fatal(err)
				}
				metadata = append(metadata, data)
			}
		}
	}
	return metadata
}

func verifyPassRangeDisabledMasks(
	t *testing.T, editor, baseline *Editor, ids []LayerID, kind string, version, compression uint16,
) (*Editor, *psdfile.Document) {
	t.Helper()
	expected, err := baseline.Composite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	live, err := editor.Composite(t.Context())
	if err != nil || !bytes.Equal(live.Pix, expected.Pix) {
		t.Fatalf("disabled masks differ from an independently edited unmasked source: %v", err)
	}
	saved := compositeSave(t, editor, SaveOptions{Compression: compression})
	if saved.Header.Version != version {
		t.Fatal("disabled-mask save changed PSD/PSB version")
	}
	reopened := lifecycleExpectPreview(t, editor, saved, expected.NRGBAAt(0, 0))
	scene, err := psdscene.Render(t.Context(), saved)
	if err != nil || !bytes.Equal(scene.Pix, expected.Pix) {
		t.Fatalf("saved pass-through range scene did not reconstruct disabled coverage: %v", err)
	}
	region, err := psdscene.RenderTile(t.Context(), saved, image.Rect(0, 0, 1, 1))
	if err != nil || !bytes.Equal(region.Pix, expected.Pix) {
		t.Fatalf("saved pass-through range tile changed inactive-mask coverage: %v", err)
	}
	pixel := expected.NRGBAAt(0, 0)
	merged := []byte{0, 0, 0, pixel.A}
	for index, value := range []byte{pixel.R, pixel.G, pixel.B} {
		merged[index] = byte((int(value)*int(pixel.A) + 255*(255-int(pixel.A)) + 127) / 255)
	}
	compositeExpect(t, saved, merged)
	for _, id := range ids {
		view, err := saved.ReadLayerMetadata(t.Context(), int(id)-1)
		if err != nil {
			t.Fatal(err)
		}
		if kind != "vector" {
			settings, err := reopened.PixelMask(t.Context(), id)
			if err != nil || !reflect.DeepEqual(settings, passRangeMaskSettings(kind)) ||
				view.Layer.Mask == nil || !view.Layer.Mask.Disabled {
				t.Fatalf("reopened pixel mask lost flags, density or feather: %v", err)
			}
			channel, _ := lifecycleMaskChannel(t, saved, int(id)-1)
			samples, err := saved.DecodeChannel(t.Context(), int(id)-1, channel)
			if err != nil || !bytes.Equal(samples.Data, []byte{73}) {
				t.Fatalf("disabled pixel-mask channel was altered: %v", err)
			}
		}
		if kind != "pixel" && (view.Layer.VectorMask == nil || !view.Layer.VectorMask.Disabled) {
			t.Fatal("reopened vector mask lost its disabled flag")
		}
	}
	return reopened, saved
}

func TestPassRangesDisabledMasksEditSaveReopen(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, mode := range []string{"scrn", "div "} {
				for _, kind := range []string{"pixel", "vector", "both"} {
					for _, location := range []string{"group", "child", "both"} {
						t.Run(fmt.Sprintf("v%d/c%d/%s/%s/%s", version, compression, mode, kind, location), func(t *testing.T) {
							data := compositeFixture(version, 8, 4, []compositeFixtureLayer{
								{divider: 3}, {color: []byte{203, 224, 154, 230}, blend: mode},
								{divider: 1, blend: "pass"}, {color: []byte{136, 4, 39, 74}},
							})
							original := bytes.Clone(data)
							editor, baseline := compositeEditor(t, data), compositeEditor(t, data)
							ids := []LayerID{3}
							switch location {
							case "child":
								ids = []LayerID{2}
							case "both":
								ids = []LayerID{3, 2}
							}
							for _, id := range ids {
								if kind != "vector" {
									if err := editor.AddPixelMask(t.Context(), id, image.Rect(0, 0, 1, 1),
										lifecycleMaskPlane(8, 73), passRangeMaskSettings(kind)); err != nil {
										t.Fatal(err)
									}
								}
								if kind != "pixel" {
									if err := editor.AddVectorMask(t.Context(), id, passRangeMaskVector()); err != nil {
										t.Fatal(err)
									}
								}
							}
							var previousMetadata [][]byte
							for _, groupOpacity := range []uint8{161, 121} {
								ranges := []psdmodel.BlendRange{{
									Source: psdmodel.BlendSlider{BlackLow: 24, BlackHigh: groupOpacity - 61,
										WhiteLow: 175, WhiteHigh: 230},
									Destination: psdmodel.BlendSlider{BlackLow: 10, BlackHigh: 75, WhiteLow: 165, WhiteHigh: 245},
								}}
								for _, current := range []*Editor{editor, baseline} {
									if err := current.SetBlendIf(3, ranges); err != nil {
										t.Fatal(err)
									}
									if err := current.SetOpacity(3, groupOpacity); err != nil {
										t.Fatal(err)
									}
									if err := current.SetOpacity(2, 191); err != nil {
										t.Fatal(err)
									}
								}
								reopened, saved := verifyPassRangeDisabledMasks(t, editor, baseline, ids, kind, version, compression)
								metadata := passRangeMaskMetadata(t, saved, ids)
								if previousMetadata != nil && !reflect.DeepEqual(metadata, previousMetadata) {
									t.Fatal("continued range editing rewrote disabled mask metadata")
								}
								previousMetadata, editor = metadata, reopened
							}
							for _, id := range ids {
								if kind != "vector" {
									settings := passRangeMaskSettings(kind)
									settings.Disabled = false
									if err := editor.SetPixelMask(t.Context(), id, settings); err != nil {
										t.Fatal(err)
									}
									assertPassRangeActiveMaskSaveRejected(t, editor)
									if err := editor.SetPixelMask(t.Context(), id, passRangeMaskSettings(kind)); err != nil {
										t.Fatal(err)
									}
								}
								if kind != "pixel" {
									vector := passRangeMaskVector()
									vector.Disabled = false
									if err := editor.SetVectorMask(t.Context(), id, vector); err != nil {
										t.Fatal(err)
									}
									assertPassRangeActiveMaskSaveRejected(t, editor)
									if err := editor.SetVectorMask(t.Context(), id, passRangeMaskVector()); err != nil {
										t.Fatal(err)
									}
								}
							}
							verifyPassRangeDisabledMasks(t, editor, baseline, ids, kind, version, compression)
							if !bytes.Equal(data, original) {
								t.Fatal("disabled-mask editing changed source bytes")
							}
						})
					}
				}
			}
		}
	}
}

func assertPassRangeActiveMaskSaveRejected(t *testing.T, editor *Editor) {
	t.Helper()
	before, _ := editor.Changes()
	var output bytes.Buffer
	if err := editor.Write(t.Context(), &output); !errors.Is(err, ErrUnsupported) || output.Len() != 0 {
		t.Fatalf("reactivated mask wrote a partial unsupported pass-group result: %v", err)
	}
	if after, _ := editor.Changes(); before != after {
		t.Fatal("failed automatic mask preview changed editor revision")
	}
}
