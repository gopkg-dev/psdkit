package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
)

func monoMixerEditFixture(version uint16) []byte {
	raw := mixerEditFixture()
	binary.BigEndian.PutUint16(raw[2:], 1)
	tags := append(compositeFixtureTag("mixr", raw), compositeFixtureTag("pRsv", []byte{0x99, 0x77})...)
	return compositeFixture(version, 8, 3, []compositeFixtureLayer{
		{color: []byte{100, 150, 200, 128}}, {tags: tags},
	})
}

func TestSetAdjustmentMonochromeOwnsInactiveFieldsAlphaAndSavesPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		data := monoMixerEditFixture(version)
		original := bytes.Clone(data)
		editor := compositeEditor(t, data)
		id := adjustmentEditLayer(t, editor)
		parameters := &psdadjust.ChannelMixerParams{Monochrome: true}
		parameters.Rows[0].Inputs[2] = 100
		parameters.Rows[1] = psdadjust.MixerChannel{Inputs: [4]int16{200, -199, 17}, Constant: -83}
		parameters.Rows[2] = psdadjust.MixerChannel{Inputs: [4]int16{-113, 79, 151}, Constant: 61}
		parameters.Rows[3] = psdadjust.MixerChannel{Inputs: [4]int16{29, 47, -181}, Constant: 13}
		expected := *parameters
		if err := editor.SetAdjustment(nil, id,
			psdadjust.Adjustment{Kind: psdadjust.ChannelMixer, ChannelMixer: parameters}); err != nil {
			t.Fatal(err)
		}
		clear(parameters.Rows[:])
		parameters.Monochrome = false
		live, err := editor.Composite(nil)
		if err != nil || !bytes.Equal(live.Pix, []byte{200, 200, 200, 128}) {
			t.Fatalf("monochrome editor lost Gray ownership or source alpha: %v", err)
		}
		saved := compositeSave(t, editor)
		if !bytes.Equal(data, original) || saved.Header.Version != version {
			t.Fatal("monochrome save changed original source or PSD/PSB version")
		}
		found := false
		for _, block := range saved.Layers[1].AdditionalInfo {
			switch block.Key {
			case "mixr":
				adjustment, err := saved.DecodeAdjustment(nil, block)
				if err != nil || *adjustment.ChannelMixer != expected ||
					!bytes.Equal(adjustment.Raw[44:], []byte{0, 0}) {
					t.Fatalf("saved Mono flag, Gray/inactive rows or padding changed: %v", err)
				}
				found = true
			case "pRsv":
				payload, err := saved.ReadSpan(block.Data)
				if err != nil || !bytes.Equal(payload, []byte{0x99, 0x77}) {
					t.Fatal("monochrome edit changed unrelated block bytes")
				}
			}
		}
		if !found {
			t.Fatal("saved monochrome adjustment disappeared")
		}
		reopened, err := New(saved)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		pixels, err := reopened.Composite(nil)
		if err != nil || !bytes.Equal(pixels.Pix, live.Pix) {
			t.Fatalf("reopened PSD/PSB Mono render differs: %v", err)
		}
	}
}

func TestSetAdjustmentMonochromeInvalidLaterFieldAndCancelAreAtomic(t *testing.T) {
	editor := compositeEditor(t, monoMixerEditFixture(1))
	id := adjustmentEditLayer(t, editor)
	parameters := &psdadjust.ChannelMixerParams{Monochrome: true}
	parameters.Rows[0].Inputs[3] = 1
	parameters.Rows[3].Constant = 201
	adjustment := psdadjust.Adjustment{Kind: psdadjust.ChannelMixer, ChannelMixer: parameters}
	if err := editor.SetAdjustment(nil, id, adjustment); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("late invalid field was masked by early CMYK scope: %v", err)
	}
	state, _ := editor.Changes()
	node, _, _, _ := editor.findNodeLocked(id)
	if state.Dirty || node.adjustment != nil || len(node.additional) != 0 {
		t.Fatal("invalid Mono edit partially mutated owned overlays")
	}
	parameters.Rows[0].Inputs[3], parameters.Rows[3].Constant = 0, 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.SetAdjustment(ctx, id, adjustment); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Mono edit changed the cause: %v", err)
	}
	state, _ = editor.Changes()
	if state.Dirty || node.adjustment != nil || len(node.additional) != 0 {
		t.Fatal("cancelled Mono edit partially mutated owned overlays")
	}
}
