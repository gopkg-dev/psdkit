package psdedit

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
)

func mixerEditFixture() []byte {
	data := []byte{0, 1, 0, 0}
	for output := range 4 {
		for input := range 5 {
			value := uint16(0)
			if output < 3 && output == input {
				value = 100
			}
			data = binary.BigEndian.AppendUint16(data, value)
		}
	}
	return append(data, 0, 0)
}

func TestSetAdjustmentChannelMixerOwnsParametersAndSavesPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		raw := mixerEditFixture()
		tags := append(compositeFixtureTag("mixr", raw), compositeFixtureTag("pRsv", []byte{0x99, 0x77})...)
		data := compositeFixture(version, 8, 3, []compositeFixtureLayer{
			{color: []byte{100, 150, 200, 255}}, {tags: tags},
		})
		original := bytes.Clone(data)
		editor := compositeEditor(t, data)
		id := adjustmentEditLayer(t, editor)
		parameters := &psdadjust.ChannelMixerParams{}
		for output, input := range []int{2, 1, 0} {
			parameters.Rows[output].Inputs[input] = 100
		}
		desired := psdadjust.Adjustment{Kind: psdadjust.ChannelMixer, ChannelMixer: parameters}
		if err := editor.SetAdjustment(nil, id, desired); err != nil {
			t.Fatal(err)
		}
		node, _, _, _ := editor.findNodeLocked(id)
		expected := *node.adjustment.ChannelMixer
		clear(parameters.Rows[:])
		if *node.adjustment.ChannelMixer != expected {
			t.Fatal("editor retained caller-owned mixer parameter storage")
		}
		live, err := editor.Composite(nil)
		if err != nil || !bytes.Equal(live.Pix, []byte{200, 150, 100, 255}) {
			t.Fatalf("live mixer overlay did not apply the source-independent swap: %v", err)
		}
		saved := compositeSave(t, editor)
		if !bytes.Equal(data, original) || saved.Header.Version != version {
			t.Fatal("source data or PSD/PSB version changed")
		}
		found := false
		for _, block := range saved.Layers[1].AdditionalInfo {
			switch block.Key {
			case "mixr":
				adjustment, err := saved.DecodeAdjustment(nil, block)
				if err != nil || *adjustment.ChannelMixer != expected ||
					!bytes.Equal(adjustment.Raw[44:], raw[44:]) {
					t.Fatalf("saved typed matrix or retained padding differs: %v", err)
				}
				found = true
			case "pRsv":
				payload, err := saved.ReadSpan(block.Data)
				if err != nil || !bytes.Equal(payload, []byte{0x99, 0x77}) {
					t.Fatalf("unrelated source block changed: %v", err)
				}
			}
		}
		if !found {
			t.Fatal("saved mixer block disappeared")
		}
		reopened, err := New(saved)
		if err != nil {
			t.Fatal(err)
		}
		preview, err := reopened.Composite(nil)
		if err != nil || !bytes.Equal(preview.Pix, live.Pix) {
			t.Fatalf("reopened live matrix differs from the editor: %v", err)
		}
		_ = reopened.Close()
	}
}

func TestSetAdjustmentChannelMixerDoesNotEraseUnsupportedSourceModes(t *testing.T) {
	desired := psdadjust.Adjustment{Kind: psdadjust.ChannelMixer, ChannelMixer: &psdadjust.ChannelMixerParams{}}
	for _, mutate := range []func([]byte){
		func(data []byte) { data[3] = 2 },
		func(data []byte) { binary.BigEndian.PutUint16(data[10:], 20) },
		func(data []byte) { binary.BigEndian.PutUint16(data[34:], 100) },
	} {
		raw := mixerEditFixture()
		mutate(raw)
		editor := compositeEditor(t, adjustmentEditFixture(1, compositeFixtureTag("mixr", raw)))
		id := adjustmentEditLayer(t, editor)
		if err := editor.SetAdjustment(nil, id, desired); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported source mixer mode silently removed: %v", err)
		}
		state, _ := editor.Changes()
		node, _, _, _ := editor.findNodeLocked(id)
		if state.Dirty || node.adjustment != nil || len(node.additional) != 0 {
			t.Fatal("unsupported mixer replacement left partial overlays")
		}
	}
}
