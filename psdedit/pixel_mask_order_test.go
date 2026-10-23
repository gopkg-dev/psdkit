package psdedit

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"testing"
)

func TestPixelMaskRealHeaderPrecedesEditedParametersAndPreservesTail(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			payload := pixelMaskTestPayload(8|16, 1, 255)
			payload = append(payload, pixelMaskTestPayload(0)[:16]...)
			payload = append(payload, 3, 128)
			payload = binary.BigEndian.AppendUint64(payload, math.Float64bits(2.5))
			payload = append(payload, 0xa1, 0xb2)
			editor := compositeEditor(t, pixelMaskTestFixture(t, version, payload, true))
			settings, err := editor.PixelMask(t.Context(), 1)
			if err != nil || settings.Density == nil || *settings.Density != 128 ||
				settings.Feather == nil || *settings.Feather != 2.5 || !settings.Relative {
				t.Fatalf("real mask source parameters: %+v, %v", settings, err)
			}
			settings.Density, settings.Feather = new(uint8(210)), new(float64(1.25))
			settings.VectorDensity, settings.VectorFeather = new(uint8(211)), new(float64(0))
			if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
				t.Fatal(err)
			}
			saved := compositeSave(t, editor, SaveOptions{CompositeImage: image.NewNRGBA(image.Rect(0, 0, 1, 1))})
			raw, err := saved.ReadSpan(saved.Layers[0].Mask.Span)
			if err != nil || !bytes.Equal(raw[:36], payload[:36]) ||
				!bytes.Equal(raw[36:len(raw)-2], pixelMaskParameters(settings)) ||
				!bytes.Equal(raw[len(raw)-2:], []byte{0xa1, 0xb2}) {
				t.Fatalf("real header/parameter/tail order changed: %x, %v", raw, err)
			}
			plane, err := saved.DecodeChannel(t.Context(), 0, 5)
			if err != nil || !bytes.Equal(plane.Data, []byte{128}) {
				t.Fatalf("real mask pixels changed: %+v, %v", plane, err)
			}
			reopened := lifecycleReopen(t, saved)
			actual, err := reopened.PixelMask(t.Context(), 1)
			if err != nil || !equalPixelMaskSettings(actual, settings) {
				t.Fatalf("edited parameters did not reopen: %+v, %v", actual, err)
			}
		})
	}
}
