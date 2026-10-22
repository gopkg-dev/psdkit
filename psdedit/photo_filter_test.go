package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func photoFilterEditPayload(density uint32) []byte {
	data := []byte{0, 2, 0, 7, 0x11, 0x94, 7, 8, 0xcf, 0xf4, 0, 0}
	data = binary.BigEndian.AppendUint32(data, density)
	return append(data, 1, 0, 0, 0)
}

func TestPhotoFilterSetAdjustmentCallerPreviewAndPSDPSBPreservation(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		payload := photoFilterEditPayload(25)
		tags := append(compositeFixtureTag("phfl", payload), compositeFixtureTag("zzZZ", []byte{9, 8, 7})...)
		tags = append(tags, compositeFixtureTag("phfl", payload)...)
		input := adjustmentEditFixture(version, tags)
		editor := compositeEditor(t, input)
		id := adjustmentEditLayer(t, editor)
		parameters := &psdadjust.PhotoFilterParams{ColorSpace: 7, Components: [4]uint16{0x1194, 0x708, 0xcff4, 0},
			Density: 25, PreserveLuminosity: true}
		requested := psdadjust.Adjustment{Kind: psdadjust.PhotoFilter, PhotoFilter: parameters}
		if err := editor.SetAdjustment(nil, id, requested); err != nil {
			t.Fatal(err)
		}
		unchanged, _ := editor.Changes()
		if unchanged.Dirty {
			t.Fatal("unchanged photo filter dirtied source")
		}
		parameters.Density, parameters.PreserveLuminosity = 60, false
		if err := editor.SetAdjustment(nil, id, requested); err != nil {
			t.Fatal(err)
		}
		parameters.Density = 99
		if output, err := editor.Composite(nil); output != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatalf("nonzero photo filter automatically approximated: %v", err)
		}
		var destination bytes.Buffer
		if err := editor.Write(nil, &destination); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("nonzero preview without accurate caller artwork accepted: %v", err)
		}
		preview := image.NewNRGBA(image.Rect(0, 0, 1, 1))
		preview.SetNRGBA(0, 0, color.NRGBA{R: 20, G: 70, B: 130, A: 255})
		destination.Reset()
		if err := editor.Write(nil, &destination, SaveOptions{CompositeImage: preview}); err != nil {
			t.Fatal(err)
		}
		saved, err := psdfile.Parse(destination.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		defer saved.Close()
		seen, unknown := 0, false
		for _, block := range saved.Layers[1].AdditionalInfo {
			data, err := saved.ReadSpan(block.Data)
			if err != nil {
				t.Fatal(err)
			}
			if block.Key == "phfl" {
				expected := photoFilterEditPayload(60)
				expected[16] = 0
				if !bytes.Equal(data, expected) {
					t.Fatalf("filter color/padding changed or caller parameter aliased: %x", data)
				}
				seen++
			}
			if block.Key == "zzZZ" && bytes.Equal(data, []byte{9, 8, 7}) {
				unknown = true
			}
		}
		if seen != 2 || !unknown || !bytes.Equal(input, adjustmentEditFixture(version, tags)) {
			t.Fatal("matching filter blocks, unknown block or original input not retained")
		}
		parameters.Density = 0
		if err := editor.SetAdjustment(nil, id, requested); err != nil {
			t.Fatal(err)
		}
		adjustmentEditPreview(t, editor, 150)
		destination.Reset()
		if err := editor.Write(nil, &destination); err != nil {
			t.Fatalf("neutral automatic preview failed: %v", err)
		}
	}
}

func TestPhotoFilterEditorCancellationAndRejectedUpdateAreAtomic(t *testing.T) {
	editor := compositeEditor(t, adjustmentEditFixture(1, compositeFixtureTag("phfl", photoFilterEditPayload(25))))
	id := adjustmentEditLayer(t, editor)
	parameters := &psdadjust.PhotoFilterParams{ColorSpace: 7, Components: [4]uint16{0x1194, 0x708, 0xcff4, 0},
		Density: 60, PreserveLuminosity: true}
	requested := psdadjust.Adjustment{Kind: psdadjust.PhotoFilter, PhotoFilter: parameters}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.SetAdjustment(ctx, id, requested); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation cause lost: %v", err)
	}
	parameters.Density = 101
	if err := editor.SetAdjustment(nil, id, requested); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("invalid density accepted: %v", err)
	}
	changes, _ := editor.Changes()
	if changes.Revision != 0 || changes.Dirty {
		t.Fatalf("rejected filter update committed overlays: %+v", changes)
	}
}

func TestPhotoFilterEditorRetainsOutOfUISourceWithoutNormalizing(t *testing.T) {
	editor := compositeEditor(t, adjustmentEditFixture(1,
		compositeFixtureTag("phfl", photoFilterEditPayload(0xffffffff))))
	id := adjustmentEditLayer(t, editor)
	requested := psdadjust.Adjustment{Kind: psdadjust.PhotoFilter,
		PhotoFilter: &psdadjust.PhotoFilterParams{ColorSpace: 7,
			Components: [4]uint16{0x1194, 0x708, 0xcff4, 0}, Density: 25, PreserveLuminosity: true}}
	if err := editor.SetAdjustment(nil, id, requested); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("out-of-UI source density normalized by editing: %v", err)
	}
	changes, _ := editor.Changes()
	if changes.Revision != 0 || changes.Dirty {
		t.Fatalf("source outside supported UI scope changed: %+v", changes)
	}
	var destination bytes.Buffer
	if err := editor.Write(nil, &destination); err != nil {
		t.Fatalf("unchanged invalid-for-live source bytes could not be copied: %v", err)
	}
	saved, err := psdfile.Parse(destination.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	defer saved.Close()
	for _, block := range saved.Layers[1].AdditionalInfo {
		if block.Key != "phfl" {
			continue
		}
		data, err := saved.ReadSpan(block.Data)
		if err != nil || !bytes.Equal(data, photoFilterEditPayload(0xffffffff)) {
			t.Fatalf("unmodified source filter bytes were lost: %v", err)
		}
	}
}
