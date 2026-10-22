package psdcompose

import (
	"bytes"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func TestHueApproximationPolicyIsExplicitRegionalAndBudgeted(t *testing.T) {
	pixels := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	copy(pixels.Pix, []byte{255, 0, 0, 128, 50, 50, 50, 255})
	parameters := &psdadjust.HueSaturationParams{}
	adjustment := &psdmodel.Layer{BlendMode: "norm", Adjustment: &psdadjust.Adjustment{
		Kind: psdadjust.HueSaturation, HueSaturation: parameters,
	}}
	document := &psdmodel.Document{Width: 2, Height: 1, Layers: []*psdmodel.Layer{
		{Bounds: pixels.Rect, Pixels: pixels, BlendMode: "norm"}, adjustment,
	}}
	neutral, err := Render(nil, document)
	if err != nil || !bytes.Equal(neutral.Pix, pixels.Pix) {
		t.Fatalf("neutral Hue changed borrowed RGB or alpha: %v", err)
	}
	parameters.Master.Hue = 60
	for _, check := range []func() error{
		func() error { _, err := Render(nil, document); return err },
		func() error { return ValidateRegion(document, image.Rect(1, 0, 2, 1)) },
	} {
		if err := check(); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("default composition or regional validation silently approximated Hue: %v", err)
		}
	}
	settings := Options{ApproximateHueSaturation: true}
	// The native wave15 master-prospective-60 red/alpha128 witness is [254, 255, 0, 128].
	result, err := Render(nil, document, settings)
	if err != nil || !bytes.Equal(result.Pix, []byte{254, 255, 0, 128, 50, 50, 50, 255}) {
		t.Fatalf("explicit Hue approximation changed gray or exact alpha: %v", err)
	}
	regional, err := RenderRegion(nil, document, image.Rect(1, 0, 2, 1), settings)
	if err != nil || !bytes.Equal(regional.Pix, result.Pix[4:]) {
		t.Fatalf("regional renderer lost explicit Hue policy: %v", err)
	}
	settings.MaxWorkingBytes = 16 << 10
	if _, err := Render(nil, document, settings); !errors.Is(err, ErrLimit) {
		t.Fatalf("explicit approximation bypassed transform budget: %v", err)
	}
	adjustment.Hidden = true
	if _, err := Render(nil, document, Options{MaxWorkingBytes: 2048}); err != nil {
		t.Fatalf("hidden Hue adjustment required a visible approximation policy: %v", err)
	}
}
