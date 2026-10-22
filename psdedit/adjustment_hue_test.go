package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func hueEditFixture() []byte {
	data := make([]byte, 100)
	binary.BigEndian.PutUint16(data, 2)
	data[3] = 0x73
	for index := range 6 {
		bounds := [4]uint16{uint16((315 + index*60) % 360), uint16((345 + index*60) % 360),
			uint16((15 + index*60) % 360), uint16((45 + index*60) % 360)}
		for bound, value := range bounds {
			binary.BigEndian.PutUint16(data[16+index*14+bound*2:], value)
		}
	}
	return append(data, 0x9a, 0x77, 0x01)
}

func TestSetHueSaturationOwnsParametersAndPreservesIgnoredBytesPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		raw := hueEditFixture()
		tags := append(compositeFixtureTag("hue2", raw), compositeFixtureTag("pRsv", []byte{0x99, 0x77})...)
		data := compositeFixture(version, 8, 3, []compositeFixtureLayer{
			{color: []byte{255, 0, 0, 128}}, {tags: tags},
		})
		original := bytes.Clone(data)
		editor := compositeEditor(t, data)
		id := adjustmentEditLayer(t, editor)
		parameters := &psdadjust.HueSaturationParams{Master: psdadjust.HueSaturationValues{Hue: 60}}
		if err := editor.SetAdjustment(nil, id, psdadjust.Adjustment{
			Kind: psdadjust.HueSaturation, HueSaturation: parameters,
		}); err != nil {
			t.Fatal(err)
		}
		parameters.Master.Hue = -60
		node, _, _, _ := editor.findNodeLocked(id)
		if node.adjustment.HueSaturation.Master.Hue != 60 {
			t.Fatal("editor borrowed caller hue/saturation controls")
		}
		if _, err := editor.Composite(nil); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("editor silently approximated a non-neutral Hue layer: %v", err)
		}
		// The native wave15 master-prospective-60 red/alpha128 witness is [254, 255, 0, 128].
		live, err := editor.Composite(nil, psdcompose.Options{ApproximateHueSaturation: true})
		if err != nil || !bytes.Equal(live.Pix, []byte{254, 255, 0, 128}) {
			t.Fatalf("live hue rotation or exact alpha differs: %v, %v", err, live)
		}
		var rejected bytes.Buffer
		if err := editor.Write(nil, &rejected); !errors.Is(err, ErrUnsupported) || rejected.Len() != 0 {
			t.Fatalf("default automatic preview wrote approximate Hue pixels: %v", err)
		}
		saved := compositeSave(t, editor, SaveOptions{CompositeImage: live})
		if !bytes.Equal(data, original) || saved.Header.Version != version {
			t.Fatal("immutable source or PSD/PSB version changed")
		}
		blocks := saved.Layers[1].AdditionalInfo
		if len(blocks) != 2 || blocks[0].Key != "hue2" || blocks[1].Key != "pRsv" {
			t.Fatal("source block order changed")
		}
		adjustment, err := saved.DecodeAdjustment(nil, blocks[0])
		if err != nil || adjustment.HueSaturation.Master.Hue != 60 || adjustment.Raw[3] != 0x73 ||
			!bytes.Equal(adjustment.Raw[100:], raw[100:]) {
			t.Fatalf("saved parameters, ignored padding or unknown tail changed: %v", err)
		}
		unrelated, err := saved.ReadSpan(blocks[1].Data)
		if err != nil || !bytes.Equal(unrelated, []byte{0x99, 0x77}) {
			t.Fatalf("unrelated retained block changed: %v", err)
		}
		reopened, err := New(saved)
		if err != nil {
			t.Fatal(err)
		}
		preview, err := reopened.Composite(nil, psdcompose.Options{ApproximateHueSaturation: true})
		if err != nil || !bytes.Equal(preview.Pix, live.Pix) {
			t.Fatalf("reopened semantic hue rendering changed: %v", err)
		}
		if _, err := psdscene.Render(nil, saved); !errors.Is(err, psdcompose.ErrUnsupported) {
			t.Fatalf("Scene silently approximated a non-neutral Hue layer: %v", err)
		}
		_, cacheErr := psdscene.Render(nil, saved, psdscene.Options{CachedSpecialLayers: true})
		if !errors.Is(cacheErr, psdcompose.ErrUnsupported) {
			t.Fatalf("cached-special policy bypassed non-neutral Hue rejection: %v", cacheErr)
		}
		sceneOptions := psdscene.Options{Compose: psdcompose.Options{ApproximateHueSaturation: true}}
		scene, err := psdscene.Render(nil, saved, sceneOptions)
		if err != nil || !bytes.Equal(scene.Pix, live.Pix) {
			t.Fatalf("explicit Scene approximation did not use live metadata: %v", err)
		}
		region, err := psdscene.RenderTile(nil, saved, live.Rect, psdscene.TileOptions{Compose: sceneOptions.Compose})
		if err != nil || !bytes.Equal(region.Pix, live.Pix) {
			t.Fatalf("explicit tiled approximation policy or pixels differ: %v", err)
		}
		approximateSaved := compositeSave(t, editor, SaveOptions{ApproximateHueSaturation: true})
		if approximateSaved.Header.Version != version {
			t.Fatal("explicit approximate automatic preview changed PSD/PSB version")
		}
		_ = reopened.Close()
	}
}

func TestSetHueSaturationRejectsMalformedCanceledAndBudgetEditsAtomically(t *testing.T) {
	for _, scenario := range []string{"invalid", "canceled", "budget", "unknown-version"} {
		raw := hueEditFixture()
		if scenario == "unknown-version" {
			raw[1] = 3
		}
		limits := psdfile.DefaultLimits()
		if scenario == "budget" {
			limits.MaxDecodeBytes = 16 << 10
		}
		editor := compositeEditor(t, adjustmentEditFixture(1, compositeFixtureTag("hue2", raw)),
			psdfile.LoadOptions{Limits: limits})
		id := adjustmentEditLayer(t, editor)
		parameters := &psdadjust.HueSaturationParams{Master: psdadjust.HueSaturationValues{Hue: 30}}
		ctx := context.Background()
		want := ErrInvalidEdit
		switch scenario {
		case "invalid":
			parameters.Ranges[0].Bounds = [4]int16{0, 90, 45, 100}
		case "canceled":
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			cancel()
			want = context.Canceled
		case "budget":
			want = psdfile.ErrLimit
		case "unknown-version":
			want = ErrUnsupported
		}
		before, _ := editor.Changes()
		if err := editor.SetAdjustment(ctx, id, psdadjust.Adjustment{
			Kind: psdadjust.HueSaturation, HueSaturation: parameters,
		}); !errors.Is(err, want) {
			t.Fatalf("%s error category differs: %v", scenario, err)
		}
		after, _ := editor.Changes()
		node, _, _, _ := editor.findNodeLocked(id)
		if before != after || node.adjustment != nil || len(node.additional) != 0 {
			t.Fatalf("%s failed edit committed partial overlays", scenario)
		}
	}
}
