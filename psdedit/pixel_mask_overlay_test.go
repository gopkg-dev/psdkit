package psdedit

import (
	"bytes"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestPixelMaskRetainedSamplesBoundLaterPixelEdits(t *testing.T) {
	editor, err := Create(t.Context(), 4, 1, CreateOptions{
		Limits: psdfile.Limits{MaxDecodeBytes: 800},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = editor.Close() })
	plane := psdfile.Plane{ID: -2, Depth: 8, Width: 128, Height: 1, RowBytes: 128,
		Data: bytes.Repeat([]byte{64}, 128)}
	if err := editor.AddPixelMask(t.Context(), 1, image.Rect(0, 0, 128, 1), plane); err != nil {
		t.Fatal(err)
	}
	before, _ := editor.Changes()
	pixels := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	if err := editor.ReplacePixels(1, pixels); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("pixel replacement ignored retained mask samples: %v", err)
	}
	if err := editor.ReplaceChannel(1, -2, plane); err != nil {
		t.Fatalf("same-value mask samples unnecessarily allocated a replacement: %v", err)
	}
	plane.Data[0] = 127
	if err := editor.ReplaceChannel(1, -2, plane); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("mask replacement ignored peak samples: %v", err)
	}
	after, _ := editor.Changes()
	if after != before || editor.roots[0].pixelMaskChange.plane.Data[0] != 64 || editor.roots[0].pixels != nil {
		t.Fatal("failed pixel edits partially modified the mask or layer")
	}
	if err := editor.RemovePixelMask(t.Context(), 1); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("removal ignored temporary metadata budget: %v", err)
	}
	after, _ = editor.Changes()
	if after != before || editor.roots[0].pixelMaskChange.removed {
		t.Fatal("failed mask removal changed retained samples or revision")
	}
}
