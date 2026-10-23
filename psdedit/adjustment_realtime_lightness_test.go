package psdedit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func TestAdjustmentRealtimeLightnessEditSaveReopenAllCompressionsPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, alpha := range []byte{128, 255} {
				t.Run(fmt.Sprintf("v%d/c%d/a%d", version, compression, alpha), func(t *testing.T) {
					raw := hueEditFixture()
					tags := append(compositeFixtureTag("hue2", raw), compositeFixtureTag("pRsv", []byte{0x99, 7})...)
					input := compositeFixture(version, 8, 4, []compositeFixtureLayer{
						{color: []byte{255, 128, 0, alpha}}, {tags: tags},
					})
					original := bytes.Clone(input)
					editor := compositeEditor(t, input)
					for _, step := range []struct {
						lightness int16
						pixels    []byte
						merged128 []byte
					}{
						{51, []byte{255, 193, 130, alpha}, []byte{255, 224, 192, 128}},
						{-97, []byte{8, 4, 0, alpha}, []byte{131, 129, 127, 128}},
						{0, []byte{255, 128, 0, alpha}, []byte{255, 191, 127, 128}},
					} {
						parameters := &psdadjust.HueSaturationParams{
							Master: psdadjust.HueSaturationValues{Lightness: step.lightness},
						}
						if err := editor.SetAdjustment(nil, adjustmentEditLayer(t, editor), psdadjust.Adjustment{
							Kind: psdadjust.HueSaturation, HueSaturation: parameters,
						}); err != nil {
							t.Fatal(err)
						}
						parameters.Master.Lightness = 99
						live, err := editor.Composite(nil)
						if err != nil || !bytes.Equal(live.Pix, step.pixels) {
							t.Fatalf("default live lightness %d: %v, %v", step.lightness, err, live)
						}
						saved := compositeSave(t, editor, SaveOptions{Compression: compression})
						if saved.Header.Version != version || !bytes.Equal(input, original) {
							t.Fatal("automatic save changed input storage or file format")
						}
						merged := step.pixels
						if alpha == 128 {
							merged = step.merged128
						}
						compositeExpect(t, saved, merged)
						blocks := saved.Layers[1].AdditionalInfo
						if len(blocks) != 2 || blocks[0].Key != "hue2" || blocks[1].Key != "pRsv" {
							t.Fatal("saving lightness changed source block order")
						}
						adjustment, err := saved.DecodeAdjustment(nil, blocks[0])
						if err != nil || adjustment.HueSaturation.Master.Lightness != step.lightness ||
							adjustment.Raw[3] != raw[3] || !bytes.Equal(adjustment.Raw[100:], raw[100:]) {
							t.Fatalf("reopened lightness controls or ignored bytes changed: %v", err)
						}
						scene, err := psdscene.Render(nil, saved)
						if err != nil || !bytes.Equal(scene.Pix, step.pixels) {
							t.Fatalf("default Scene did not rerender saved lightness: %v, %v", err, scene)
						}
						tile, err := psdscene.RenderTile(nil, saved, live.Rect)
						if err != nil || !bytes.Equal(tile.Pix, step.pixels) {
							t.Fatalf("default tile did not rerender saved lightness: %v, %v", err, tile)
						}
						reopened, err := New(saved)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = reopened.Close() })
						preview, err := reopened.Composite(nil)
						if err != nil || !bytes.Equal(preview.Pix, step.pixels) {
							t.Fatalf("reopened editor did not rerender lightness: %v", err)
						}
						editor = reopened
					}
				})
			}
		}
	}
}

func TestAdjustmentRealtimeLightnessFailuresDoNotPublishPartialState(t *testing.T) {
	editor := compositeEditor(t, adjustmentEditFixture(1, compositeFixtureTag("hue2", hueEditFixture())))
	id := adjustmentEditLayer(t, editor)
	valid := psdadjust.Adjustment{Kind: psdadjust.HueSaturation, HueSaturation: &psdadjust.HueSaturationParams{
		Master: psdadjust.HueSaturationValues{Lightness: 51},
	}}
	if err := editor.SetAdjustment(nil, id, valid); err != nil {
		t.Fatal(err)
	}
	before, _ := editor.Changes()
	live, err := editor.Composite(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, canceled := range []bool{false, true} {
		parameters := *valid.HueSaturation
		parameters.Master.Lightness = 101
		ctx, cancel := context.WithCancel(context.Background())
		want := ErrInvalidEdit
		if canceled {
			parameters.Master.Lightness = -53
			cancel()
			want = context.Canceled
		}
		err := editor.SetAdjustment(ctx, id, psdadjust.Adjustment{Kind: psdadjust.HueSaturation,
			HueSaturation: &parameters})
		cancel()
		if !errors.Is(err, want) {
			t.Fatalf("invalid or canceled edit category differs: %v", err)
		}
		after, _ := editor.Changes()
		current, err := editor.Composite(nil)
		if before != after || err != nil || !bytes.Equal(current.Pix, live.Pix) {
			t.Fatalf("failed lightness edit changed committed live state: %v", err)
		}
	}
	parameters := *valid.HueSaturation
	parameters.Ranges[0].Values.Lightness = 1
	if err := editor.SetAdjustment(nil, id, psdadjust.Adjustment{Kind: psdadjust.HueSaturation,
		HueSaturation: &parameters}); err != nil {
		t.Fatal(err)
	}
	if output, err := editor.Composite(nil); output != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("local lightness silently escaped the approximation gate: %v", err)
	}
	var rejected bytes.Buffer
	if err := editor.Write(nil, &rejected); !errors.Is(err, ErrUnsupported) || rejected.Len() != 0 {
		t.Fatalf("unsupported local lightness wrote a partial save: %v", err)
	}
	if err := editor.SetAdjustment(nil, id, valid); err != nil {
		t.Fatal(err)
	}
	saved := compositeSave(t, editor)
	if rendered, err := psdscene.Render(nil, saved); err != nil || !bytes.Equal(rendered.Pix, live.Pix) {
		t.Fatalf("editor could not recover from rejected automatic save: %v", err)
	}
}
