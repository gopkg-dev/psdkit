package psdparse

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
)

func TestColorBalanceTypedPSDPSBNeutralAndNonneutralRenderingGate(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, nonneutral := range []bool{false, true} {
			payload := make([]byte, 22)
			payload[18] = 1
			if nonneutral {
				binary.BigEndian.PutUint16(payload, uint16(65436))
				binary.BigEndian.PutUint16(payload[10:], 100)
			}
			document, err := ParseBytes(semanticsDocument(version, nil, nil, semanticsBlock{"blnc", payload}))
			if err != nil || document.Layers[0].Adjustment == nil || len(document.Layers[0].Unsupported) != 0 {
				t.Fatalf("known Color Balance metadata did not become typed: %v", err)
			}
			adjustment := document.Layers[0].Adjustment
			if adjustment.Kind != psdadjust.ColorBalance || !bytes.Equal(adjustment.Raw, payload) ||
				!adjustment.ColorBalance.PreserveLuminosity {
				t.Fatal("typed Color Balance lost raw bytes or luminosity flag")
			}
			transform, err := psdadjust.Compile(*adjustment)
			if nonneutral {
				if !errors.Is(err, psdadjust.ErrUnsupported) || adjustment.ColorBalance.Shadows[0] != -100 ||
					adjustment.ColorBalance.Midtones[2] != 100 {
					t.Fatalf("nonneutral signed metadata bypassed rendering gate: %v", err)
				}
				continue
			}
			if err != nil || transform.RGB([3]float64{.1, .3, .8}) != ([3]float64{.1, .3, .8}) {
				t.Fatalf("neutral Color Balance changed color: %v", err)
			}
		}
	}
	unknown := make([]byte, 20)
	unknown[18] = 2
	document, err := ParseBytes(semanticsDocument(1, nil, nil, semanticsBlock{"blnc", unknown}))
	if err != nil || document.Layers[0].Adjustment != nil || len(document.Layers[0].Unsupported) == 0 {
		t.Fatalf("unknown Color Balance flag was silently treated as supported: %v", err)
	}
}
