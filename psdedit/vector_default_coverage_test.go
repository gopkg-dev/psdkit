package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func defaultCoverageVectorPayload(bounds image.Rectangle) []byte {
	// Encode source parameters independently of the editor's mask serializer.
	payload := pixelMaskTestPayload(1|4|16, 15, 128)
	payload = binary.BigEndian.AppendUint64(payload, math.Float64bits(3))
	payload = append(payload, 255)
	payload = binary.BigEndian.AppendUint64(payload, math.Float64bits(0))
	payload = append(payload, 0, 0xa1, 0xb2)
	for index, coordinate := range []int{bounds.Min.Y, bounds.Min.X, bounds.Max.Y, bounds.Max.X} {
		binary.BigEndian.PutUint32(payload[index*4:], uint32(int32(coordinate)))
	}
	payload[16] = 64
	return payload
}

func defaultCoverageVectorRoundTrip(
	t *testing.T, editor *Editor, settings PixelMaskSettings, bounds image.Rectangle,
	version, compression uint16, alpha byte, vector bool,
) *Editor {
	t.Helper()
	preview, err := editor.Composite(t.Context())
	if err != nil || preview.NRGBAAt(0, 0).A != alpha {
		t.Fatalf("default-coverage vector preview = %v, want alpha %d, error %v", preview, alpha, err)
	}
	saved := compositeSave(t, editor, SaveOptions{Compression: compression})
	if saved.Header.Version != version || saved.Layers[0].Mask == nil ||
		saved.Layers[0].Mask.Bounds != bounds || saved.Layers[0].Mask.HasReal {
		t.Fatal("vector edit changed the source format, default-mask bounds or user-mask layout")
	}
	for _, channel := range saved.Layers[0].Channels {
		if channel.ID == -2 || channel.ID == -3 {
			t.Fatal("default coverage was replaced with synthesized sample channels")
		}
	}
	if len(saved.Layers[0].Channels) != len(editor.source.Layers[0].Channels) {
		t.Fatal("default-coverage vector edit changed the source channel count")
	}
	count := 0
	if vector {
		count = 1
	}
	if lifecycleVectorBlocks(saved, 0) != count {
		t.Fatal("saved document has the wrong number of vector masks")
	}
	raw, err := saved.ReadSpan(saved.Layers[0].MaskData)
	if err != nil || !bytes.Equal(raw[len(raw)-2:], []byte{0xa1, 0xb2}) {
		t.Fatalf("default-coverage mask lost its opaque tail: %v", err)
	}
	for _, original := range editor.source.Layers[0].Channels {
		found := false
		for _, retained := range saved.Layers[0].Channels {
			if original.ID != retained.ID {
				continue
			}
			found = true
			before, readErr := editor.source.ReadSpan(original.Span)
			if readErr != nil {
				t.Fatal(readErr)
			}
			after, readErr := saved.ReadSpan(retained.Span)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatalf("vector edit changed source channel %d: %v", original.ID, readErr)
			}
		}
		if !found {
			t.Fatalf("vector edit removed source channel %d", original.ID)
		}
	}
	merged, err := saved.DecodeComposite(t.Context())
	if err != nil || len(merged.Channels) != 4 || merged.Channels[3].Data[0] != alpha {
		t.Fatalf("saved preview contains stale default-coverage alpha: %v", err)
	}
	for component, value := range []int{180, 90, 30} {
		white := byte((value*int(alpha) + 255*(255-int(alpha)) + 127) / 255)
		if merged.Channels[component].Data[0] != white {
			t.Fatalf("saved RGB component %d did not use the updated preview over white", component)
		}
	}
	reopened := lifecycleReopen(t, saved)
	actual, err := reopened.PixelMask(t.Context(), 1)
	if err != nil || !reflect.DeepEqual(actual, settings) {
		t.Fatalf("default-coverage user mask lost settings after reopening: %+v, %v", actual, err)
	}
	persisted, err := reopened.Composite(t.Context())
	if err != nil || !bytes.Equal(persisted.Pix, preview.Pix) {
		t.Fatalf("default-coverage vector preview changed after reopening: %v", err)
	}
	return reopened
}

func TestVectorDefaultCoverageAddEditSaveReopenPSDPSBAllCompressions(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, currentSettings := range []bool{false, true} {
				t.Run(fmt.Sprintf("version%d_compression%d_current%t", version, compression, currentSettings),
					func(t *testing.T) {
						bounds := image.Rect(0, 0, 1, 1)
						data := defaultCoverageMaskFixture(t, version, defaultCoverageVectorPayload(bounds),
							compositeFixtureTag("pRsv", []byte{0x96, 0x83}))
						original := bytes.Clone(data)
						editor := compositeEditor(t, data)
						settings, err := editor.PixelMask(t.Context(), 1)
						if err != nil {
							t.Fatal(err)
						}
						alpha := byte(112)
						if currentSettings {
							settings.DefaultColor, settings.Invert, settings.Density = 96, false, nil
							if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
								t.Fatal(err)
							}
							alpha = 48
						}
						before, err := editor.readPixelMaskDataLocked(t.Context(), editor.roots[0])
						if err != nil {
							t.Fatal(err)
						}
						if err := editor.AddVectorMask(t.Context(), 1, lifecycleVectorMask(t)); err != nil {
							t.Fatal(err)
						}
						after, err := editor.readPixelMaskDataLocked(t.Context(), editor.roots[0])
						if err != nil || !bytes.Equal(before, after) || editor.roots[0].pixelMaskChange != nil {
							t.Fatalf("adding geometry changed the default-only mask representation: %v", err)
						}
						editor = defaultCoverageVectorRoundTrip(t, editor, settings, bounds,
							version, compression, alpha, true)
						for _, step := range []struct {
							settings PixelMaskSettings
							alpha    byte
						}{
							{PixelMaskSettings{DefaultColor: 32, Relative: true, Feather: new(float64(3))}, 16},
							{PixelMaskSettings{DefaultColor: 32, Invert: true, Feather: new(float64(3))}, 112},
							{PixelMaskSettings{DefaultColor: 32, Invert: true, Density: new(uint8(128))}, 120},
							{PixelMaskSettings{DefaultColor: 32, Disabled: true, Feather: new(float64(3))}, 128},
							{PixelMaskSettings{DefaultColor: 0}, 0},
							{PixelMaskSettings{DefaultColor: 255}, 128},
						} {
							if err := editor.SetPixelMask(t.Context(), 1, step.settings); err != nil {
								t.Fatal(err)
							}
							editor = defaultCoverageVectorRoundTrip(t, editor, step.settings, bounds,
								version, compression, step.alpha, true)
							settings = step.settings
						}
						mask := lifecycleVectorMask(t)
						mask.Disabled = true
						if err := editor.SetVectorMask(t.Context(), 1, mask); err != nil {
							t.Fatal(err)
						}
						editor = defaultCoverageVectorRoundTrip(t, editor, settings, bounds,
							version, compression, 255, true)
						if err := editor.RemoveVectorMask(t.Context(), 1); err != nil {
							t.Fatal(err)
						}
						editor = defaultCoverageVectorRoundTrip(t, editor, settings, bounds,
							version, compression, 255, false)
						if err := editor.AddVectorMask(t.Context(), 1, lifecycleVectorMask(t)); err != nil {
							t.Fatal(err)
						}
						defaultCoverageVectorRoundTrip(t, editor, settings, bounds,
							version, compression, 128, true)
						if !bytes.Equal(data, original) {
							t.Fatal("default-coverage vector lifecycle edited immutable source bytes")
						}
					})
			}
		}
	}
}

func TestVectorDefaultCoverageRetainsEmptyAndOffCanvasBounds(t *testing.T) {
	for _, bounds := range []image.Rectangle{image.Rect(7, -3, 7, -3), image.Rect(-7, 2, -3, 4)} {
		t.Run(bounds.String(), func(t *testing.T) {
			editor := compositeEditor(t, defaultCoverageMaskFixture(t, 1,
				defaultCoverageVectorPayload(bounds), nil))
			if err := editor.AddVectorMask(t.Context(), 1, lifecycleVectorMask(t)); err != nil {
				t.Fatal(err)
			}
			settings, err := editor.PixelMask(t.Context(), 1)
			if err != nil || settings.DefaultColor != 64 || !settings.Invert || !settings.Relative {
				t.Fatalf("ordinary default mask lost its source settings: %+v, %v", settings, err)
			}
			editor = defaultCoverageVectorRoundTrip(t, editor, settings, bounds, 1, 3, 112, true)
			settings.DefaultColor, settings.Density = 127, nil
			if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
				t.Fatal(err)
			}
			defaultCoverageVectorRoundTrip(t, editor, settings, bounds, 1, 3, 64, true)
		})
	}
}

func TestVectorDefaultCoverageRemovalDoesNotCreateUserMaskFromGeneratedCarrier(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, originallyVector := range []bool{false, true} {
			t.Run(fmt.Sprintf("version%d_vector%t", version, originallyVector), func(t *testing.T) {
				payload := pixelMaskTestPayload(8|16, 4, 137, 0, 0)
				copy(payload[:16], make([]byte, 16))
				var tags []byte
				if originallyVector {
					tags = compositeFixtureTag("vmsk", editVectorPayload())
				}
				editor := compositeEditor(t, defaultCoverageMaskFixture(t, version, payload, tags))
				hasVector := originallyVector
				for range 2 {
					state, err := editor.pixelMaskStateLocked(t.Context(), editor.roots[0])
					if err != nil || state.hasUser {
						t.Fatalf("generated carrier acquired user-mask ownership: %+v, %v", state, err)
					}
					if _, err := editor.PixelMask(t.Context(), 1); !errors.Is(err, ErrUnsupported) {
						t.Fatalf("generated carrier became an editable default-coverage mask: %v", err)
					}
					before, _ := editor.Changes()
					if err := editor.RemovePixelMask(t.Context(), 1); err != nil {
						t.Fatal(err)
					}
					if after, _ := editor.Changes(); after != before {
						t.Fatal("removing absent user coverage changed generated carrier metadata")
					}
					if hasVector {
						break
					}
					if err := editor.AddVectorMask(t.Context(), 1, lifecycleVectorMask(t)); err != nil {
						t.Fatal(err)
					}
					hasVector = true
				}
				if err := editor.SetVectorMask(t.Context(), 1, lifecycleVectorMask(t)); err != nil {
					t.Fatal(err)
				}
				saved := compositeSave(t, editor)
				reopened := lifecycleExpectPreview(t, editor, saved,
					color.NRGBA{R: 180, G: 90, B: 30, A: 187})
				if _, err := reopened.PixelMask(t.Context(), 1); !errors.Is(err, ErrUnsupported) {
					t.Fatalf("reopened generated carrier became a user mask: %v", err)
				}
				if err := reopened.RemoveVectorMask(t.Context(), 1); err != nil {
					t.Fatal(err)
				}
				removed := compositeSave(t, reopened)
				lifecycleExpectMaskRemoved(t, removed, 0)
			})
		}
	}
	for _, version := range []uint16{1, 2} {
		editor := compositeEditor(t, defaultCoverageMaskFixture(t, version,
			defaultCoverageVectorPayload(image.Rect(0, 0, 1, 1)), nil))
		if err := editor.AddVectorMask(t.Context(), 1, lifecycleVectorMask(t)); err != nil {
			t.Fatal(err)
		}
		if err := editor.RemovePixelMask(t.Context(), 1); err != nil {
			t.Fatal(err)
		}
		saved := compositeSave(t, editor)
		reopened := lifecycleExpectPreview(t, editor, saved, color.NRGBA{R: 180, G: 90, B: 30, A: 128})
		if _, err := reopened.PixelMask(t.Context(), 1); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("removed default-coverage user mask reappeared from generated metadata: %v", err)
		}
		if err := reopened.AddPixelMask(t.Context(), 1, image.Rect(0, 0, 1, 1), lifecycleMaskPlane(8, 96)); err != nil {
			t.Fatal(err)
		}
		lifecycleExpectPreview(t, reopened, compositeSave(t, reopened), color.NRGBA{R: 180, G: 90, B: 30, A: 48})
	}
}

func TestVectorDefaultCoverageReadCancellationBudgetAndUnknownLayoutsAreAtomic(t *testing.T) {
	data := defaultCoverageMaskFixture(t, 1, defaultCoverageVectorPayload(image.Rect(0, 0, 1, 1)), nil)
	assertUnchanged := func(t *testing.T, editor *Editor, before Changes, retained int64) {
		t.Helper()
		after, _ := editor.Changes()
		if after != before || retained != editor.ownedBytesLocked(nil) ||
			editor.roots[0].vectorMaskChange != nil || editor.roots[0].vectorMask != nil ||
			editor.roots[0].pixelMaskChange != nil {
			t.Fatal("failed default-coverage vector addition changed overlays, revision or retained memory")
		}
	}
	failed, indexed, denied := compositeLayerOnlyEditor(t, data)
	before, _ := failed.Changes()
	retained := failed.ownedBytesLocked(nil)
	denied.failure, denied.failureCause = indexed.Layers[0].MaskData, io.ErrClosedPipe
	if err := failed.AddVectorMask(t.Context(), 1, lifecycleVectorMask(t)); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("default-coverage vector read lost its error cause: %v", err)
	}
	assertUnchanged(t, failed, before, retained)
	reader := &smartEditCancelReader{data: data}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	editor := lifecycleReopen(t, source)
	before, _ = editor.Changes()
	retained = editor.ownedBytesLocked(nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader.trigger, reader.cancel = source.Layers[0].MaskData, cancel
	if err := editor.AddVectorMask(ctx, 1, lifecycleVectorMask(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("default-coverage vector mid-read cancellation: %v", err)
	}
	if reader.matches != 1 {
		t.Fatal("cancellation did not exercise the mask source-read boundary")
	}
	assertUnchanged(t, editor, before, retained)
	for _, currentSettings := range []bool{false, true} {
		editor := compositeEditor(t, data)
		if currentSettings {
			if err := editor.SetPixelMask(t.Context(), 1, PixelMaskSettings{DefaultColor: 96}); err != nil {
				t.Fatal(err)
			}
		}
		node := editor.roots[0]
		node.setSemanticCost("other", editor.source.Limits().MaxDecodeBytes-editor.ownedBytesLocked(nil)-750)
		before, _ := editor.Changes()
		retained := editor.ownedBytesLocked(nil)
		if err := editor.AddVectorMask(t.Context(), 1, lifecycleVectorMask(t)); !errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("default-coverage vector addition ignored peak memory: %v", err)
		}
		assertUnchanged(t, editor, before, retained)
	}
	for _, payload := range [][]byte{pixelMaskTestPayload(8, 0, 0), pixelMaskTestPayload(16, 0x80, 0)} {
		editor := compositeEditor(t, defaultCoverageMaskFixture(t, 1, payload, nil))
		before, _ := editor.Changes()
		retained := editor.ownedBytesLocked(nil)
		if err := editor.AddVectorMask(t.Context(), 1, lifecycleVectorMask(t)); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unrecognized default-coverage layout acquired vector ownership: %v", err)
		}
		assertUnchanged(t, editor, before, retained)
	}
}

func TestVectorDefaultCoverageHighDepthMetadataRemainsEditableWithoutFakeSamples(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, depth := range []uint16{16, 32} {
			for compression := range uint16(4) {
				t.Run(fmt.Sprintf("version%d_depth%d_compression%d", version, depth, compression), func(t *testing.T) {
					bounds := image.Rect(-4, -3, 0, 0)
					editor := compositeEditor(t, defaultCoverageMaskFixture(t, version,
						defaultCoverageVectorPayload(bounds), nil, depth))
					if err := editor.AddVectorMask(t.Context(), 1, lifecycleVectorMask(t)); err != nil {
						t.Fatal(err)
					}
					settings := PixelMaskSettings{DefaultColor: 0}
					if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
						t.Fatal(err)
					}
					var automatic bytes.Buffer
					if err := editor.Write(t.Context(), &automatic); !errors.Is(err, ErrUnsupported) || automatic.Len() != 0 {
						t.Fatalf("high-depth default-coverage mask invented an automatic RGB8 preview: %v", err)
					}
					// Default coverage zero makes this edit fully transparent at every depth and vector position.
					preview, err := editor.source.DecodeComposite(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					for component := range 4 {
						value := []byte{0, 0}
						if depth == 32 {
							value = make([]byte, 4)
						}
						if component < 3 {
							if depth == 16 {
								value = []byte{255, 255}
							} else {
								binary.BigEndian.PutUint32(value, math.Float32bits(1))
							}
						}
						preview.Channels[component].Data = value
					}
					options := SaveOptions{Compression: compression, Composite: preview}
					for pass := range 3 {
						saved := compositeSave(t, editor, options)
						if saved.Header.Version != version || saved.Header.Depth != depth ||
							saved.Layers[0].Mask.Bounds != bounds || len(saved.Layers[0].Channels) != 4 {
							t.Fatal("high-depth vector addition changed format, default-mask bounds or channels")
						}
						for _, channel := range saved.Layers[0].Channels {
							if channel.ID == -2 || channel.ID == -3 {
								t.Fatal("high-depth default coverage acquired synthesized samples")
							}
						}
						merged, err := saved.DecodeComposite(t.Context())
						if err != nil || !reflect.DeepEqual(merged, preview) {
							t.Fatalf("high-depth save altered the exact transparent replacement preview: %v", err)
						}
						editor = lifecycleReopen(t, saved)
						actual, err := editor.PixelMask(t.Context(), 1)
						if err != nil || !reflect.DeepEqual(actual, settings) {
							t.Fatalf("high-depth default coverage did not reopen as an editable user mask: %+v, %v", actual, err)
						}
						switch pass {
						case 0:
							mask := lifecycleVectorMask(t)
							mask.Disabled = true
							if err := editor.SetVectorMask(t.Context(), 1, mask); err != nil {
								t.Fatal(err)
							}
						case 1:
							if err := editor.RemoveVectorMask(t.Context(), 1); err != nil {
								t.Fatal(err)
							}
						}
					}
				})
			}
		}
	}
}

func TestVectorDefaultCoverageUnknownFlagsRemainUnsupportedForAutomaticPixels(t *testing.T) {
	payload := pixelMaskTestPayload(0x80, 0xa1, 0xb2)
	payload[16] = 64
	editor := compositeEditor(t, defaultCoverageMaskFixture(t, 1, payload, nil))
	if err := editor.AddVectorMask(t.Context(), 1, lifecycleVectorMask(t)); err != nil {
		t.Fatal(err)
	}
	if pixels, err := editor.Composite(t.Context()); pixels != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("vector addition hid an unknown pixel-mask rendering flag: %v", err)
	}
	var output bytes.Buffer
	if err := editor.Write(t.Context(), &output); !errors.Is(err, ErrUnsupported) || output.Len() != 0 {
		t.Fatalf("unknown pixel-mask flags wrote invented automatic pixels: %v", err)
	}
}

func TestVectorDefaultCoverageExistingGroupDiskSaveAndContinuedEditing(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprintf("version%d", version), func(t *testing.T) {
			data := compositeFixture(version, 8, 4, []compositeFixtureLayer{
				{divider: 3}, {color: []byte{255, 0, 0, 255}}, {divider: 1, blend: "norm"},
			})
			payload := pixelMaskTestPayload(0, 0xa1, 0xb2)
			payload[16] = 64
			editor := compositeEditor(t, defaultCoverageLayerMaskFixture(t, data, 2, payload))
			if err := editor.AddVectorMask(t.Context(), 3, lifecycleVectorMask(t)); err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			for pass, coverage := range []byte{64, 128} {
				settings := PixelMaskSettings{DefaultColor: coverage}
				if err := editor.SetPixelMask(t.Context(), 3, settings); err != nil {
					t.Fatal(err)
				}
				for _, groupOnly := range []bool{false, true} {
					preview, err := editor.Composite(t.Context())
					if groupOnly {
						preview, err = editor.CompositeGroup(t.Context(), 3)
					}
					if err != nil || preview.NRGBAAt(0, 0) != (color.NRGBA{R: 255, A: coverage / 2}) {
						t.Fatalf("group-only %t default-coverage preview = %v, %v", groupOnly, preview, err)
					}
				}
				path := filepath.Join(directory, fmt.Sprintf("group-%d.psd", pass))
				if err := editor.Save(t.Context(), path, SaveOptions{Compression: 3}); err != nil {
					t.Fatal(err)
				}
				saved, err := psdfile.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = saved.Close() })
				if saved.Header.Version != version || saved.Layers[2].Mask == nil ||
					len(saved.Layers[2].Channels) != 0 || lifecycleVectorBlocks(saved, 2) != 1 {
					t.Fatal("group default-coverage vector edit changed format, geometry or empty channel ownership")
				}
				merged, err := saved.DecodeComposite(t.Context())
				if err != nil || merged.Channels[3].Data[0] != coverage/2 {
					t.Fatalf("saved group default-coverage composite was stale: %v", err)
				}
				editor = lifecycleReopen(t, saved)
				actual, err := editor.PixelMask(t.Context(), 3)
				if err != nil || !reflect.DeepEqual(actual, settings) {
					t.Fatalf("group default-coverage mask did not remain editable: %+v, %v", actual, err)
				}
			}
			if err := editor.RemoveVectorMask(t.Context(), 3); err != nil {
				t.Fatal(err)
			}
			preview, err := editor.CompositeGroup(t.Context(), 3)
			if err != nil || preview.NRGBAAt(0, 0) != (color.NRGBA{R: 255, A: 128}) {
				t.Fatalf("removing vector geometry lost the group's default-only user mask: %v, %v", preview, err)
			}
		})
	}
}
