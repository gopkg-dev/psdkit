package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

// pixelMaskTestFixture replaces independently encoded fixture metadata without invoking the editor writer.
func pixelMaskTestFixture(t *testing.T, version uint16, mask []byte, generated bool, real ...bool) []byte {
	t.Helper()
	layer := compositeFixtureLayer{color: []byte{180, 90, 30, 255}, mask: []byte{64},
		tags: compositeFixtureTag("pRsv", []byte{0x96, 0x83})}
	if generated || len(real) != 0 && real[0] {
		layer.extraID = -3
	}
	if generated {
		layer.tags = append(layer.tags, compositeFixtureTag("vmsk", editVectorPayload())...)
	}
	data := compositeFixture(version, 8, 4, []compositeFixtureLayer{layer})
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	record := source.Layers[0]
	if generated {
		for _, channel := range record.Channels {
			if channel.ID == -3 {
				data[channel.Data.Offset] = 128
			}
		}
	}
	difference := int64(len(mask)) - record.Mask.Span.Length
	if difference%2 != 0 {
		t.Fatal("test fixture requires an even mask payload delta")
	}
	for _, offset := range []int64{source.LayerMaskSection.Offset, source.LayerInfo.Offset} {
		if version == 2 {
			previous := binary.BigEndian.Uint64(data[offset:])
			binary.BigEndian.PutUint64(data[offset:], uint64(int64(previous)+difference))
		} else {
			previous := binary.BigEndian.Uint32(data[offset:])
			binary.BigEndian.PutUint32(data[offset:], uint32(int64(previous)+difference))
		}
	}
	extraLength := binary.BigEndian.Uint32(data[record.Extra.Offset-4:])
	binary.BigEndian.PutUint32(data[record.Extra.Offset-4:], uint32(int64(extraLength)+difference))
	binary.BigEndian.PutUint32(data[record.MaskData.Offset:], uint32(len(mask)))
	result := append([]byte{}, data[:record.Mask.Span.Offset]...)
	result = append(result, mask...)
	return append(result, data[record.Mask.Span.Offset+record.Mask.Span.Length:]...)
}

func pixelMaskTestPayload(flags byte, tail ...byte) []byte {
	data := make([]byte, 16)
	binary.BigEndian.PutUint32(data[8:], 1)
	binary.BigEndian.PutUint32(data[12:], 1)
	return append(append(data, 255, flags), tail...)
}

func TestPixelMaskEditPreviewSaveAndOwnershipPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			data := pixelMaskTestFixture(t, version, pixelMaskTestPayload(0, 0xa1, 0xb2), false)
			original := bytes.Clone(data)
			editor := compositeEditor(t, data)
			settings, err := editor.PixelMask(nil, 1)
			if err != nil || settings.DefaultColor != 255 || settings.Density != nil || settings.Feather != nil {
				t.Fatalf("original mask settings: %+v, %v", settings, err)
			}
			if err := editor.SetPixelMask(nil, 1, settings); err != nil {
				t.Fatal(err)
			}
			changes, _ := editor.Changes()
			if changes.Revision != 0 {
				t.Fatal("unchanged mask created a revision")
			}
			settings.Invert, settings.Relative, settings.DefaultColor = true, true, 137
			settings.Density, settings.Feather = new(uint8(128)), new(float64(0))
			settings.VectorDensity, settings.VectorFeather = new(uint8(211)), new(float64(0))
			if err := editor.SetPixelMask(nil, 1, settings); err != nil {
				t.Fatal(err)
			}
			expected := clonePixelMaskSettings(settings)
			*settings.Density, *settings.Feather = 0, 999
			settings, err = editor.PixelMask(nil, 1)
			if err != nil || !reflect.DeepEqual(settings, expected) {
				t.Fatalf("setter borrowed caller settings: %+v, %v", settings, err)
			}
			*settings.Density = 0
			settings, _ = editor.PixelMask(nil, 1)
			if *settings.Density != 128 {
				t.Fatal("getter exposed retained parameter pointers")
			}
			preview, err := editor.Composite(nil)
			if err != nil || preview.NRGBAAt(0, 0).A != 223 {
				t.Fatalf("invert then density preview = %v, %v", preview, err)
			}
			before := editor.roots[0].semanticBytes
			if err := editor.SetPixelMask(nil, 1, expected); err != nil {
				t.Fatal(err)
			}
			changes, _ = editor.Changes()
			if changes.Revision != 1 || editor.roots[0].semanticBytes != before {
				t.Fatal("identical edited settings changed revision or accounting")
			}
			saved := compositeSave(t, editor)
			reopened, err := New(saved)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			persisted, err := reopened.PixelMask(nil, 1)
			if err != nil || !reflect.DeepEqual(persisted, expected) || saved.Header.Version != version {
				t.Fatalf("saved mask settings = %+v, %v", persisted, err)
			}
			if saved.Layers[0].Mask.Bounds != editor.source.Layers[0].Mask.Bounds {
				t.Fatal("relative flag edit moved the stored document-coordinate mask")
			}
			raw, err := saved.ReadSpan(saved.Layers[0].MaskData)
			if err != nil || !bytes.Equal(raw[len(raw)-2:], []byte{0xa1, 0xb2}) {
				t.Fatalf("source mask padding changed: %v", err)
			}
			for index, channel := range saved.Layers[0].Channels {
				oldBytes, err := editor.source.ReadSpan(editor.source.Layers[0].Channels[index].Span)
				if err != nil {
					t.Fatal(err)
				}
				newBytes, err := saved.ReadSpan(channel.Span)
				if err != nil || !bytes.Equal(newBytes, oldBytes) {
					t.Fatalf("unchanged channel %d was rewritten: %v", channel.ID, err)
				}
			}
			pixels, err := reopened.Composite(nil)
			if err != nil || !bytes.Equal(pixels.Pix, preview.Pix) || !bytes.Equal(data, original) {
				t.Fatalf("reopened preview/source preservation differs: %v", err)
			}
			expected.Density, expected.Feather, expected.VectorDensity, expected.VectorFeather = nil, nil, nil, nil
			if err := editor.SetPixelMask(nil, 1, expected); err != nil {
				t.Fatal(err)
			}
			cleared := compositeSave(t, editor)
			mask := cleared.Layers[0].Mask
			if mask.UserDensity != nil || mask.UserFeather != nil || mask.VectorDensity != nil || mask.VectorFeather != nil {
				t.Fatal("nil parameters did not remove explicit density and feather")
			}
		})
	}
}

func TestPixelMaskGeneratedRealChannelEditsOnlyUserMetadata(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		primary := pixelMaskTestPayload(8)
		realMask := pixelMaskTestPayload(0)
		payload := append(primary, 0, 255)
		payload = append(payload, realMask[:16]...)
		editor := compositeEditor(t, pixelMaskTestFixture(t, version, payload, true))
		settings, err := editor.PixelMask(nil, 1)
		if err != nil {
			t.Fatal(err)
		}
		settings.Disabled, settings.Invert, settings.Relative = true, true, true
		settings.DefaultColor, settings.Density = 111, new(uint8(210))
		if err := editor.SetPixelMask(nil, 1, settings); err != nil {
			t.Fatal(err)
		}
		preview, err := editor.Composite(nil)
		if err != nil || preview.NRGBAAt(0, 0).A != 255 {
			t.Fatalf("real mask disable did not affect preview: %v, %v", preview, err)
		}
		saved := compositeSave(t, editor)
		mask := saved.Layers[0].Mask
		if mask.Flags != 8|16 || mask.DefaultColor != 255 || mask.RealFlags != 7 || mask.RealDefaultColor != 111 ||
			mask.RealBounds != image.Rect(0, 0, 1, 1) || *mask.UserDensity != 210 {
			t.Fatalf("generated or real metadata changed incorrectly: %+v", mask)
		}
		reopened, err := New(saved)
		if err != nil {
			t.Fatal(err)
		}
		persisted, err := reopened.PixelMask(nil, 1)
		_ = reopened.Close()
		if err != nil || !reflect.DeepEqual(persisted, settings) {
			t.Fatalf("real user mask settings did not reopen: %+v, %v", persisted, err)
		}
	}
}

func TestPixelMaskPreservesReservedFlagsAndOpaqueBytes(t *testing.T) {
	editor := compositeEditor(t, pixelMaskTestFixture(t, 1, pixelMaskTestPayload(0x80, 0xa1, 0xb2), false))
	settings, err := editor.PixelMask(nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	settings.Disabled = true
	if err := editor.SetPixelMask(nil, 1, settings); err != nil {
		t.Fatal(err)
	}
	preview := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	copy(preview.Pix, []byte{180, 90, 30, 255})
	saved := compositeSave(t, editor, SaveOptions{CompositeImage: preview})
	raw, err := saved.ReadSpan(saved.Layers[0].Mask.Span)
	if err != nil || raw[17] != 0x82 || !bytes.Equal(raw[18:], []byte{0xa1, 0xb2}) {
		t.Fatalf("reserved flags or opaque bytes were erased: %x, %v", raw, err)
	}
}

func TestPixelMaskGroupEditAppliesBeforeGroupTraversal(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		editor := compositeEditor(t, compositeFixture(version, 8, 4, []compositeFixtureLayer{
			{divider: 3},
			{color: []byte{180, 90, 30, 255}},
			{color: []byte{0, 0, 0, 0}, divider: 1, blend: "norm", mask: []byte{64}},
		}))
		settings, err := editor.PixelMask(nil, 3)
		if err != nil {
			t.Fatal(err)
		}
		settings.Disabled = true
		if err := editor.SetPixelMask(nil, 3, settings); err != nil {
			t.Fatal(err)
		}
		for _, preview := range []func() (*image.NRGBA, error){
			func() (*image.NRGBA, error) { return editor.Composite(nil) },
			func() (*image.NRGBA, error) { return editor.CompositeGroup(nil, 3) },
		} {
			pixels, err := preview()
			if err != nil || pixels.NRGBAAt(0, 0).A != 255 {
				t.Fatalf("group mask edit did not affect preview: %v, %v", pixels, err)
			}
		}
		saved := compositeSave(t, editor)
		if saved.Layers[2].Mask.Flags&2 == 0 {
			t.Fatal("saved group metadata lost the mask edit")
		}
	}
}

func TestPixelMaskMetadataReadFailuresAndMidReadCancellationAreAtomic(t *testing.T) {
	data := pixelMaskTestFixture(t, 1, pixelMaskTestPayload(0, 0, 0), false)
	reader := &smartEditCancelReader{data: data}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	editor, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = editor.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader.trigger, reader.cancel = source.Layers[0].MaskData, cancel
	if err := editor.SetPixelMask(ctx, 1, PixelMaskSettings{Disabled: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-read cancellation cause: %v", err)
	}
	state, _ := editor.Changes()
	if state.Dirty || reader.matches != 1 {
		t.Fatal("cancelled mask read mutated state or did not reach the read boundary")
	}
	failed, indexed, denied := compositeLayerOnlyEditor(t, data)
	denied.failure, denied.failureCause = indexed.Layers[0].MaskData, io.ErrClosedPipe
	if err := failed.SetPixelMask(nil, 1, PixelMaskSettings{Disabled: true}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("source read failure lost its cause: %v", err)
	}
	state, _ = failed.Changes()
	if state.Dirty || failed.roots[0].maskData != nil {
		t.Fatal("failed mask read mutated state")
	}
}

func TestPixelMaskRejectsAmbiguousLayoutsInvalidValuesCancellationAndBudgetAtomically(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload []byte
		real    bool
	}{
		{"unknown parameters", pixelMaskTestPayload(16, 0x80, 0), false},
		{"generated without real user mask", pixelMaskTestPayload(8, 0, 0), false},
		{"two active masks", append(pixelMaskTestPayload(0, 0, 255), pixelMaskTestPayload(0)[:16]...), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			editor := compositeEditor(t, pixelMaskTestFixture(t, 1, test.payload, false, test.real))
			if err := editor.SetPixelMask(nil, 1, PixelMaskSettings{Disabled: true}); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("ambiguous metadata accepted: %v", err)
			}
			state, _ := editor.Changes()
			if state.Dirty || editor.roots[0].maskData != nil {
				t.Fatal("unsupported edit partially committed")
			}
		})
	}
	editor := compositeEditor(t, pixelMaskTestFixture(t, 1, pixelMaskTestPayload(0, 0, 0), false))
	settings, err := editor.PixelMask(nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, radius := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		settings.Feather = new(radius)
		if err := editor.SetPixelMask(nil, 1, settings); !errors.Is(err, ErrInvalidEdit) {
			t.Fatalf("invalid feather accepted: %v", err)
		}
	}
	settings.Feather, settings.Disabled = nil, true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.SetPixelMask(ctx, 1, settings); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled edit cause: %v", err)
	}
	if _, err := editor.PixelMask(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled getter cause: %v", err)
	}
	node := editor.roots[0]
	node.setSemanticCost("other", editor.source.Limits().MaxDecodeBytes-editor.ownedBytesLocked(nil)-530)
	retained := editor.ownedBytesLocked(nil)
	if err := editor.SetPixelMask(nil, 1, settings); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("mask peak budget ignored: %v", err)
	}
	state, _ := editor.Changes()
	if state.Dirty || node.maskData != nil || node.pixelMask != nil || editor.ownedBytesLocked(nil) != retained {
		t.Fatal("failed edit changed revision, overlay or budget accounting")
	}
}
