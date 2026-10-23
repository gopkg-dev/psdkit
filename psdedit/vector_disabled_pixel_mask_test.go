package psdedit

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func disabledPixelFeatherVector(bounds image.Rectangle) *psdvector.Mask {
	points := []psdvector.Point{{X: float64(bounds.Min.X), Y: float64(bounds.Min.Y)},
		{X: float64(bounds.Max.X), Y: float64(bounds.Min.Y)},
		{X: float64(bounds.Max.X), Y: float64(bounds.Max.Y)},
		{X: float64(bounds.Min.X), Y: float64(bounds.Max.Y)}}
	knots := make([]psdvector.Knot, len(points))
	for index, point := range points {
		knots[index] = psdvector.Knot{Anchor: point, Preceding: point, Leaving: point, RecordIndex: -1}
	}
	return &psdvector.Mask{Version: 3, Width: 32, Height: 16, HasInitialFill: true,
		Subpaths: []psdvector.Subpath{{Closed: true, Operation: 1, Unknown1: 1, Knots: knots, RecordIndex: -1}}}
}

func TestVectorFeatherDisabledPixelMaskEditSaveReopen(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			t.Run(fmt.Sprintf("version%d_compression%d", version, compression), func(t *testing.T) {
				editor, err := Create(t.Context(), 32, 16, CreateOptions{Version: version})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = editor.Close() })
				pixels := image.NewNRGBA(image.Rect(0, 0, 32, 16))
				for y := range 16 {
					for x := range 32 {
						pixels.SetNRGBA(x, y, color.NRGBA{R: 210, G: 67, B: 33, A: 255})
					}
				}
				if err := editor.ReplacePixels(1, pixels); err != nil {
					t.Fatal(err)
				}
				plane := psdfile.Plane{ID: -2, Depth: 8, Width: 32, Height: 16, RowBytes: 32,
					Data: bytes.Repeat([]byte{37}, 32*16)}
				if err := editor.AddPixelMask(t.Context(), 1, pixels.Rect, plane); err != nil {
					t.Fatal(err)
				}
				mask := disabledPixelFeatherVector(image.Rect(4, 3, 26, 13))
				if err := editor.AddVectorMask(t.Context(), 1, mask); err != nil {
					t.Fatal(err)
				}
				settings, err := editor.PixelMask(t.Context(), 1)
				if err != nil {
					t.Fatal(err)
				}
				settings.Disabled, settings.VectorFeather = true, new(1.5)
				if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
					t.Fatal(err)
				}
				first := verifyDisabledPixelFeatherRoundTrip(t, editor, settings, version, compression)
				settings.VectorFeather = new(3.0)
				if err := first.SetPixelMask(t.Context(), 1, settings); err != nil {
					t.Fatal(err)
				}
				if err := first.SetVectorMask(t.Context(), 1,
					disabledPixelFeatherVector(image.Rect(7, 2, 29, 12))); err != nil {
					t.Fatal(err)
				}
				second := verifyDisabledPixelFeatherRoundTrip(t, first, settings, version, compression)
				settings.Disabled = false
				if err := second.SetPixelMask(t.Context(), 1, settings); err != nil {
					t.Fatal(err)
				}
				if pixels, err := second.Composite(t.Context()); pixels != nil || !errors.Is(err, ErrUnsupported) {
					t.Fatalf("active pixel mask unexpectedly combined with vector feather: %v", err)
				}
				var rejected bytes.Buffer
				if err := second.Write(t.Context(), &rejected); !errors.Is(err, ErrUnsupported) || rejected.Len() != 0 {
					t.Fatalf("unsupported active combination wrote an automatic composite: %v", err)
				}
				settings.Disabled = true
				if err := second.SetPixelMask(t.Context(), 1, settings); err != nil {
					t.Fatal(err)
				}
				verifyDisabledPixelFeatherRoundTrip(t, second, settings, version, compression)
			})
		}
	}
}

func verifyDisabledPixelFeatherRoundTrip(
	t *testing.T, editor *Editor, settings PixelMaskSettings, version, compression uint16,
) *Editor {
	t.Helper()
	expected, err := editor.Composite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if expected.NRGBAAt(3, 8).A == 255 || expected.NRGBAAt(15, 8).A == 0 {
		t.Fatal("vector feather was not rendered")
	}
	saved := compositeSave(t, editor, SaveOptions{Compression: compression})
	if saved.Header.Version != version {
		t.Fatal("save changed PSD/PSB version")
	}
	channel, _ := lifecycleMaskChannel(t, saved, 0)
	samples, err := saved.DecodeChannel(t.Context(), 0, channel)
	if err != nil || !bytes.Equal(samples.Data, bytes.Repeat([]byte{37}, 32*16)) {
		t.Fatalf("save altered retained disabled mask samples: %v", err)
	}
	merged, err := saved.DecodeComposite(t.Context())
	if err != nil || len(merged.Channels) != 4 {
		t.Fatalf("saved merged preview is unavailable: %v", err)
	}
	for index, alpha := range merged.Channels[3].Data {
		if alpha != expected.Pix[index*4+3] {
			t.Fatal("saved preview did not use live vector feather")
		}
	}
	reopened, err := New(saved)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	actual, err := reopened.Composite(t.Context())
	if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
		t.Fatalf("saved vector feather did not reconstruct after reopening: %v", err)
	}
	retained, err := reopened.PixelMask(t.Context(), 1)
	if err != nil || !reflect.DeepEqual(retained, settings) {
		t.Fatalf("save changed mask flags or feather: %v, %+v", err, retained)
	}
	return reopened
}
