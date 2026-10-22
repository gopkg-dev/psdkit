package psdparse

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
)

func TestChannelMixerTypedPSDPSBIndependentRecords(t *testing.T) {
	payload := []byte{0, 1, 0, 0}
	for _, value := range []int16{70, 20, 10, 0, 5, -10, 90, 20, 0, -5, 20, 10, 70, 0, 0, 0, 0, 0, 0, 0} {
		payload = binary.BigEndian.AppendUint16(payload, uint16(value))
	}
	for _, version := range []uint16{1, 2} {
		document, err := ParseBytes(semanticsDocument(version, nil, nil, semanticsBlock{"mixr", payload}))
		if err != nil || document.Layers[0].Adjustment == nil {
			t.Fatalf("version %d did not parse independent mixer metadata: %v", version, err)
		}
		adjustment := document.Layers[0].Adjustment
		if adjustment.Kind != psdadjust.ChannelMixer || adjustment.Version != 1 ||
			adjustment.ChannelMixer.Rows[1].Inputs != ([4]int16{-10, 90, 20, 0}) ||
			adjustment.ChannelMixer.Rows[1].Constant != -5 || !bytes.Equal(adjustment.Raw, payload) ||
			len(document.Layers[0].Unsupported) != 0 {
			t.Fatal("typed matrix, signed coefficients, original encoding or supported family differs")
		}
	}
	unknown := bytes.Clone(payload)
	unknown[1] = 2
	document, err := ParseBytes(semanticsDocument(1, nil, nil, semanticsBlock{"mixr", unknown}))
	if err != nil || document.Layers[0].Adjustment != nil || len(document.Layers[0].Unsupported) == 0 {
		t.Fatalf("unknown mixer version was silently parsed as supported: %v", err)
	}
}
