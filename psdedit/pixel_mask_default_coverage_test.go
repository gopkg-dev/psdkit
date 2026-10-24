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

// defaultCoverageMaskFixture adds independent mask metadata without encoding a sample channel.
func defaultCoverageMaskFixture(t *testing.T, version uint16, payload, tags []byte, depths ...uint16) []byte {
	t.Helper()
	depth := uint16(8)
	if len(depths) != 0 {
		depth = depths[0]
	}
	data := compositeFixture(version, depth, 4, []compositeFixtureLayer{
		{color: []byte{180, 90, 30, 255}, tags: tags},
	})
	return defaultCoverageLayerMaskFixture(t, data, 0, payload)
}

func defaultCoverageLayerMaskFixture(t *testing.T, data []byte, layerIndex int, payload []byte) []byte {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if len(payload)%2 != 0 {
		t.Fatal("test fixture requires an even mask payload length")
	}
	for _, offset := range []int64{source.LayerMaskSection.Offset, source.LayerInfo.Offset} {
		if source.Header.Version == 2 {
			binary.BigEndian.PutUint64(data[offset:], binary.BigEndian.Uint64(data[offset:])+uint64(len(payload)))
		} else {
			binary.BigEndian.PutUint32(data[offset:], binary.BigEndian.Uint32(data[offset:])+uint32(len(payload)))
		}
	}
	record := source.Layers[layerIndex]
	offset := record.Extra.Offset - 4
	binary.BigEndian.PutUint32(data[offset:], binary.BigEndian.Uint32(data[offset:])+uint32(len(payload)))
	binary.BigEndian.PutUint32(data[record.MaskData.Offset:], uint32(len(payload)))
	insertion := record.MaskData.Offset + 4
	result := append([]byte{}, data[:insertion]...)
	result = append(result, payload...)
	return append(result, data[insertion:]...)
}

func TestPixelMaskDefaultCoverageEditPreviewSaveAndReopen(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, compression := range []uint16{0, 1, 2, 3} {
			t.Run(fmt.Sprintf("version%d_compression%d", version, compression), func(t *testing.T) {
				payload := pixelMaskTestPayload(0, 0xa1, 0xb2)
				payload[16] = 64
				data := defaultCoverageMaskFixture(t, version, payload,
					compositeFixtureTag("pRsv", []byte{0x96, 0x83}))
				original := bytes.Clone(data)
				editor := compositeEditor(t, data)
				settings, err := editor.PixelMask(t.Context(), 1)
				if err != nil || settings.DefaultColor != 64 {
					t.Fatalf("default-coverage getter = %+v, %v", settings, err)
				}
				if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
					t.Fatal(err)
				}
				state, _ := editor.Changes()
				if state.Dirty {
					t.Fatal("unchanged default coverage dirtied the editor")
				}
				for _, test := range []struct {
					name     string
					settings PixelMaskSettings
					alpha    byte
				}{
					{"default color", PixelMaskSettings{DefaultColor: 96}, 96},
					{"invert", PixelMaskSettings{DefaultColor: 64, Invert: true}, 191},
					{"density", PixelMaskSettings{DefaultColor: 64, Invert: true, Density: new(uint8(128))}, 223},
					{"feather", PixelMaskSettings{DefaultColor: 64, Feather: new(float64(3)), Relative: true}, 64},
					{"disabled", PixelMaskSettings{DefaultColor: 64, Disabled: true}, 255},
					{"zero default", PixelMaskSettings{DefaultColor: 0}, 0},
				} {
					if err := editor.SetPixelMask(t.Context(), 1, test.settings); err != nil {
						t.Fatalf("%s: %v", test.name, err)
					}
					preview, err := editor.Composite(t.Context())
					if err != nil || preview.NRGBAAt(0, 0).A != test.alpha {
						t.Fatalf("%s preview = %v, want alpha %d, error %v", test.name, preview, test.alpha, err)
					}
					saved := compositeSave(t, editor, SaveOptions{Compression: compression})
					if saved.Header.Version != version || len(saved.Layers[0].Channels) != 4 {
						t.Fatal("editing default coverage changed the document version or added a mask channel")
					}
					if saved.Layers[0].Mask.Bounds != editor.source.Layers[0].Mask.Bounds {
						t.Fatal("editing default coverage moved the mask bounds")
					}
					lifecycleExpectPreserved(t, editor.source, saved, 0)
					raw, err := saved.ReadSpan(saved.Layers[0].MaskData)
					if err != nil || !bytes.Equal(raw[len(raw)-2:], []byte{0xa1, 0xb2}) {
						t.Fatalf("%s changed the opaque mask tail: %v", test.name, err)
					}
					reopened := lifecycleReopen(t, saved)
					persisted, err := reopened.PixelMask(t.Context(), 1)
					if err != nil || !reflect.DeepEqual(persisted, test.settings) {
						t.Fatalf("%s reopened settings = %+v, %v", test.name, persisted, err)
					}
					pixels, err := reopened.Composite(t.Context())
					if err != nil || !bytes.Equal(pixels.Pix, preview.Pix) {
						t.Fatalf("%s changed after saving and reopening: %v", test.name, err)
					}
					merged, err := saved.DecodeComposite(t.Context())
					if err != nil || merged.Channels[3].Data[0] != test.alpha {
						t.Fatalf("%s saved a stale composite alpha: %v", test.name, err)
					}
					// Continue editing the reopened document so parameter removal is also exercised after persistence.
					editor = reopened
				}
				if !bytes.Equal(data, original) {
					t.Fatal("editing default coverage changed source bytes")
				}
			})
		}
	}
}

func TestPixelMaskDefaultCoverageOwnsParametersAndPreservesUnknownFlags(t *testing.T) {
	payload := pixelMaskTestPayload(0x80, 0xa1, 0xb2)
	editor := compositeEditor(t, defaultCoverageMaskFixture(t, 1, payload, nil))
	settings, err := editor.PixelMask(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	settings.Disabled, settings.Density, settings.Feather = true, new(uint8(123)), new(float64(2))
	expected := clonePixelMaskSettings(settings)
	if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
		t.Fatal(err)
	}
	*settings.Density, *settings.Feather = 0, 99
	actual, err := editor.PixelMask(t.Context(), 1)
	if err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("setter borrowed default-coverage parameters: %+v, %v", actual, err)
	}
	*actual.Density, *actual.Feather = 0, 99
	actual, err = editor.PixelMask(t.Context(), 1)
	if err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("getter exposed default-coverage parameters: %+v, %v", actual, err)
	}
	preview := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	copy(preview.Pix, []byte{180, 90, 30, 255})
	saved := compositeSave(t, editor, SaveOptions{CompositeImage: preview})
	raw, err := saved.ReadSpan(saved.Layers[0].Mask.Span)
	if err != nil || raw[17] != 0x92 || !bytes.Equal(raw[len(raw)-2:], []byte{0xa1, 0xb2}) {
		t.Fatalf("unknown flags or opaque tail changed: %x, %v", raw, err)
	}
	actual, err = lifecycleReopen(t, saved).PixelMask(t.Context(), 1)
	if err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("preserved default-coverage settings did not reopen: %+v, %v", actual, err)
	}
}

func TestPixelMaskDefaultCoverageRefusesAmbiguousMetadataAtomically(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload []byte
		tags    []byte
		real    bool
	}{
		{name: "generated", payload: pixelMaskTestPayload(8, 0, 0)},
		{name: "generated vector", payload: pixelMaskTestPayload(8, 0, 0),
			tags: compositeFixtureTag("vmsk", editVectorPayload())},
		{name: "active real metadata", payload: append(pixelMaskTestPayload(0, 0, 255), make([]byte, 16)...), real: true},
		{name: "disabled real metadata", payload: append(pixelMaskTestPayload(0, 2, 255), make([]byte, 16)...), real: true},
		{name: "orphan real channel", payload: pixelMaskTestPayload(0, 0, 0), real: true},
		{name: "unknown parameters", payload: pixelMaskTestPayload(16, 0x80, 0)},
	} {
		t.Run(test.name, func(t *testing.T) {
			editor := compositeEditor(t, defaultCoverageMaskFixture(t, 1, test.payload, test.tags))
			if test.real {
				editor.source.Layers[0].Channels = append(editor.source.Layers[0].Channels,
					psdfile.ChannelRecord{ID: -3})
			}
			if _, err := editor.PixelMask(t.Context(), 1); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("ambiguous default-coverage getter = %v", err)
			}
			if err := editor.SetPixelMask(t.Context(), 1, PixelMaskSettings{Disabled: true}); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("ambiguous default-coverage setter = %v", err)
			}
			state, _ := editor.Changes()
			if state.Dirty || editor.roots[0].maskData != nil || editor.roots[0].pixelMask != nil {
				t.Fatal("unsupported metadata changed the editor")
			}
		})
	}
}

func TestPixelMaskDefaultCoverageFailuresCancellationAndBudgetAreAtomic(t *testing.T) {
	data := defaultCoverageMaskFixture(t, 1, pixelMaskTestPayload(0, 0, 0), nil)
	editor := compositeEditor(t, data)
	for _, radius := range []float64{-1, math.NaN(), math.Inf(1)} {
		if err := editor.SetPixelMask(t.Context(), 1, PixelMaskSettings{Feather: new(radius)}); !errors.Is(err, ErrInvalidEdit) {
			t.Fatalf("invalid default-coverage feather = %v", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := editor.PixelMask(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled default-coverage getter = %v", err)
	}
	if err := editor.SetPixelMask(ctx, 1, PixelMaskSettings{Disabled: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled default-coverage setter = %v", err)
	}
	node := editor.roots[0]
	node.setSemanticCost("other", editor.source.Limits().MaxDecodeBytes-editor.ownedBytesLocked(nil)-550)
	retained := editor.ownedBytesLocked(nil)
	if err := editor.SetPixelMask(t.Context(), 1, PixelMaskSettings{Disabled: true}); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("default-coverage replacement peak budget = %v", err)
	}
	state, _ := editor.Changes()
	if state.Dirty || node.maskData != nil || node.pixelMask != nil || editor.ownedBytesLocked(nil) != retained {
		t.Fatal("failed default-coverage edit changed retained state")
	}
	failed, indexed, denied := compositeLayerOnlyEditor(t, data)
	denied.failure, denied.failureCause = indexed.Layers[0].MaskData, io.ErrClosedPipe
	if err := failed.SetPixelMask(t.Context(), 1, PixelMaskSettings{Disabled: true}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("default-coverage read failure lost its cause: %v", err)
	}
	state, _ = failed.Changes()
	if state.Dirty || failed.roots[0].maskData != nil || failed.roots[0].pixelMask != nil {
		t.Fatal("failed default-coverage source read changed the editor")
	}
	reader := &smartEditCancelReader{data: data}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	midRead := lifecycleReopen(t, source)
	reading, stopReading := context.WithCancel(t.Context())
	defer stopReading()
	reader.trigger, reader.cancel = source.Layers[0].MaskData, stopReading
	if err := midRead.SetPixelMask(reading, 1, PixelMaskSettings{Disabled: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-read default-coverage cancellation = %v", err)
	}
	state, _ = midRead.Changes()
	if reader.matches != 1 || state.Dirty || midRead.roots[0].maskData != nil || midRead.roots[0].pixelMask != nil {
		t.Fatal("default-coverage cancellation did not reach the read boundary or mutated the editor")
	}
}
