package psdedit

import (
	"bytes"
	"context"
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
	"github.com/gopkg-dev/psdkit/psdvector"
)

func lifecycleVectorMask(t *testing.T) *psdvector.Mask {
	t.Helper()
	mask, err := psdvector.DecodeMask(t.Context(), editVectorPayload(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	halfVector(mask)
	return mask
}

func lifecycleVectorBlocks(source *psdfile.Document, index int) int {
	count := 0
	for _, block := range source.Layers[index].AdditionalInfo {
		if block.Key == "vmsk" || block.Key == "vsms" {
			count++
		}
	}
	return count
}

func TestVectorLifecycleAddSetRemovePSDPSBAllCompressions(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, compression := range []uint16{0, 1, 2, 3} {
			t.Run(fmt.Sprintf("version%d_compression%d", version, compression), func(t *testing.T) {
				data := compositeFixture(version, 8, 4, []compositeFixtureLayer{{
					color: []byte{255, 0, 0, 255}, tags: compositeFixtureTag("pRsv", []byte{1, 2, 3}),
				}})
				original := bytes.Clone(data)
				editor := compositeEditor(t, data)
				mask := lifecycleVectorMask(t)
				if err := editor.AddVectorMask(t.Context(), 1, mask); err != nil {
					t.Fatal(err)
				}
				mask.Subpaths[0].Knots[1].Anchor.X = 0.9
				mask.Records[3].Raw[0] = 99
				if editor.roots[0].vectorMask.Subpaths[0].Knots[1].Anchor.X != 0.5 {
					t.Fatal("addition borrowed caller vector geometry")
				}
				options := SaveOptions{Compression: compression}
				saved := compositeSave(t, editor, options)
				if lifecycleVectorBlocks(saved, 0) != 1 || saved.Header.Version != version {
					t.Fatal("save lost added vector or changed PSD/PSB format")
				}
				lifecycleExpectPreview(t, editor, saved, color.NRGBA{R: 255, A: 128})
				current := lifecycleVectorMask(t)
				before, _ := editor.Changes()
				if err := editor.SetVectorMask(nil, 1, current); err != nil {
					t.Fatal(err)
				}
				after, _ := editor.Changes()
				if before.Revision != after.Revision {
					t.Fatal("identical SetVectorMask on newly added mask marked editor changed")
				}
				current.Disabled = true
				if err := editor.SetVectorMask(nil, 1, current); err != nil {
					t.Fatal(err)
				}
				lifecycleExpectPreview(t, editor, compositeSave(t, editor, options), color.NRGBA{R: 255, A: 255})
				if err := editor.RemoveVectorMask(nil, 1); err != nil {
					t.Fatal(err)
				}
				before, _ = editor.Changes()
				if err := editor.RemoveVectorMask(nil, 1); err != nil {
					t.Fatal(err)
				}
				after, _ = editor.Changes()
				if before.Revision != after.Revision {
					t.Fatal("removing absent vector changed revision")
				}
				removed := compositeSave(t, editor, options)
				if lifecycleVectorBlocks(removed, 0) != 0 {
					t.Fatal("removed vector block survived save")
				}
				lifecycleExpectPreserved(t, editor.source, removed, 0)
				lifecycleExpectPreview(t, editor, removed, color.NRGBA{R: 255, A: 255})
				if err := editor.SetVectorMask(nil, 1, lifecycleVectorMask(t)); !errors.Is(err, ErrUnsupported) {
					t.Fatalf("SetVectorMask resurrected removed geometry: %v", err)
				}
				if err := editor.AddVectorMask(nil, 1, lifecycleVectorMask(t)); err != nil {
					t.Fatal(err)
				}
				lifecycleExpectPreview(t, editor, compositeSave(t, editor, options), color.NRGBA{R: 255, A: 128})
				if !bytes.Equal(data, original) {
					t.Fatal("vector lifecycle mutated source bytes")
				}
			})
		}
	}
}

func TestVectorLifecyclePreservesUserPixelMask(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, originalVector := range []bool{false, true} {
			t.Run(fmt.Sprintf("version%d_original%t", version, originalVector), func(t *testing.T) {
				tags := []byte(nil)
				if originalVector {
					tags = compositeFixtureTag("vsms", editVectorPayload())
				}
				editor := compositeEditor(t, compositeFixture(version, 8, 4, []compositeFixtureLayer{{
					color: []byte{255, 0, 0, 255}, mask: []byte{128}, tags: tags,
				}}))
				settings, err := editor.PixelMask(nil, 1)
				if err != nil {
					t.Fatal(err)
				}
				settings.Density, settings.Feather = new(uint8(128)), new(float64(0))
				settings.VectorDensity, settings.VectorFeather = new(uint8(255)), new(float64(0))
				if err := editor.SetPixelMask(nil, 1, settings); err != nil {
					t.Fatal(err)
				}
				if !originalVector {
					if err := editor.AddVectorMask(nil, 1, lifecycleVectorMask(t)); err != nil {
						t.Fatal(err)
					}
				}
				if err := editor.RemoveVectorMask(nil, 1); err != nil {
					t.Fatal(err)
				}
				settings.VectorDensity, settings.VectorFeather = nil, nil
				actual, err := editor.PixelMask(nil, 1)
				if err != nil || !reflect.DeepEqual(actual, settings) {
					t.Fatalf("vector removal changed user settings: %+v, %v", actual, err)
				}
				saved := compositeSave(t, editor)
				if lifecycleVectorBlocks(saved, 0) != 0 || saved.Layers[0].Mask == nil {
					t.Fatal("vector removal lost user mask or retained vector")
				}
				index, _ := lifecycleMaskChannel(t, saved, 0)
				plane, err := saved.DecodeChannel(nil, 0, index)
				if err != nil || !bytes.Equal(plane.Data, []byte{128}) {
					t.Fatalf("vector removal changed pixel samples: %v", err)
				}
				reopened := lifecycleExpectPreview(t, editor, saved, color.NRGBA{R: 255, A: 191})
				persisted, err := reopened.PixelMask(nil, 1)
				if err != nil || !reflect.DeepEqual(persisted, settings) {
					t.Fatalf("saved removal changed user settings: %+v, %v", persisted, err)
				}
				if err := reopened.AddVectorMask(nil, 1, lifecycleVectorMask(t)); err != nil {
					t.Fatal(err)
				}
				lifecycleExpectPreview(t, reopened, compositeSave(t, reopened), color.NRGBA{R: 255, A: 96})
			})
		}
	}
}

func TestVectorLifecycleRemovesGeneratedCoverageAndAllowsReaddition(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		data := compositeFixture(version, 8, 4, []compositeFixtureLayer{{
			color: []byte{255, 0, 0, 255}, mask: []byte{64}, tags: compositeFixtureTag("vmsk", editVectorPayload()),
		}})
		source, err := psdfile.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		data[source.Layers[0].Mask.Span.Offset+17] |= 8
		_ = source.Close()
		editor := compositeEditor(t, data)
		if err := editor.RemoveVectorMask(nil, 1); err != nil {
			t.Fatal(err)
		}
		saved := compositeSave(t, editor)
		lifecycleExpectMaskRemoved(t, saved, 0)
		lifecycleExpectPreview(t, editor, saved, color.NRGBA{R: 255, A: 255})
		if err := editor.AddPixelMask(nil, 1, image.Rect(0, 0, 1, 1), lifecycleMaskPlane(8, 128)); err != nil {
			t.Fatal(err)
		}
		if err := editor.AddVectorMask(nil, 1, lifecycleVectorMask(t)); err != nil {
			t.Fatal(err)
		}
		lifecycleExpectPreview(t, editor, compositeSave(t, editor), color.NRGBA{R: 255, A: 64})
		if err := editor.RemoveVectorMask(nil, 1); err != nil {
			t.Fatal(err)
		}
		lifecycleExpectPreview(t, editor, compositeSave(t, editor), color.NRGBA{R: 255, A: 128})
	}
}

func TestVectorLifecycleValidatesBeforeMutation(t *testing.T) {
	editor := lifecycleMaskTestEditor(t)
	if err := editor.RemoveVectorMask(nil, 1); err != nil || editor.revision != 0 {
		t.Fatalf("absent-vector removal: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	mask := lifecycleVectorMask(t)
	for _, operation := range []func() error{
		func() error { return editor.AddVectorMask(ctx, 1, mask) },
		func() error { return editor.RemoveVectorMask(ctx, 1) },
	} {
		if err := operation(); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled lifecycle operation: %v", err)
		}
	}
	for _, invalid := range []*psdvector.Mask{nil, {Width: 2, Height: 1}} {
		if err := editor.AddVectorMask(nil, 1, invalid); !errors.Is(err, ErrInvalidEdit) {
			t.Fatalf("invalid vector canvas: %v", err)
		}
	}
	mask.Subpaths[0].Knots[0].Anchor.X = math.NaN()
	if err := editor.AddVectorMask(nil, 1, mask); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("nonfinite vector: %v", err)
	}
	if editor.revision != 0 || editor.roots[0].vectorMaskChange != nil || editor.roots[0].vectorMask != nil {
		t.Fatal("failed edit changed state")
	}
	limited := compositeEditor(t, compositeFixture(1, 8, 4, []compositeFixtureLayer{{
		color: []byte{255, 0, 0, 255},
	}}), psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 200}})
	if err := limited.AddVectorMask(nil, 1, lifecycleVectorMask(t)); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("addition ignored retained working budget: %v", err)
	}
	if limited.revision != 0 || limited.roots[0].vectorMaskChange != nil {
		t.Fatal("budget failure changed state")
	}
	if err := editor.AddVectorMask(nil, 1, lifecycleVectorMask(t)); err != nil {
		t.Fatal(err)
	}
	if err := editor.AddVectorMask(nil, 1, lifecycleVectorMask(t)); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("adding over existing vector: %v", err)
	}
}

func TestVectorLifecycleRejectsDualUserMasksAndDuplicateBlocks(t *testing.T) {
	payload := append(pixelMaskTestPayload(0, 0, 255), pixelMaskTestPayload(0)[:16]...)
	editor := compositeEditor(t, pixelMaskTestFixture(t, 1, payload, true))
	if err := editor.RemoveVectorMask(nil, 1); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("real -3 user mask was silently migrated: %v", err)
	}
	if editor.revision != 0 || editor.roots[0].maskData != nil || editor.roots[0].vectorMaskChange != nil {
		t.Fatal("refused dual-mask removal changed state")
	}
	ambiguous := compositeEditor(t, compositeFixture(1, 8, 4, []compositeFixtureLayer{{
		color: []byte{255, 0, 0, 255}, tags: append(compositeFixtureTag("vmsk", editVectorPayload()),
			compositeFixtureTag("vsms", editVectorPayload())...),
	}}))
	if err := ambiguous.RemoveVectorMask(nil, 1); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("duplicate vector removal: %v", err)
	}
	if err := ambiguous.AddVectorMask(nil, 1, lifecycleVectorMask(t)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("duplicate vector addition: %v", err)
	}
	if ambiguous.revision != 0 {
		t.Fatal("ambiguous vector edit changed state")
	}
}

func TestVectorLifecycleNewRasterAndGroupDiskSave(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, group := range []bool{false, true} {
			t.Run(fmt.Sprintf("version%d_group%t", version, group), func(t *testing.T) {
				editor := compositeEditor(t, compositeFixture(version, 8, 4, []compositeFixtureLayer{{
					color: []byte{255, 0, 0, 255},
				}}))
				if err := editor.Remove(1); err != nil {
					t.Fatal(err)
				}
				parent := LayerID(0)
				if group {
					var err error
					parent, err = editor.AddGroup(0, 0, "masked group", true)
					if err != nil {
						t.Fatal(err)
					}
				}
				pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
				pixels.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
				id, err := editor.AddRaster(parent, 0, "masked raster", pixels, image.Point{})
				if err != nil {
					t.Fatal(err)
				}
				if group {
					id = parent
				}
				if err := editor.AddVectorMask(nil, id, lifecycleVectorMask(t)); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "vector.psd")
				if err := editor.Save(nil, path); err != nil {
					t.Fatal(err)
				}
				saved, err := psdfile.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = saved.Close() })
				lifecycleExpectPreview(t, editor, saved, color.NRGBA{R: 255, A: 128})
			})
		}
	}
}

func TestVectorLifecycleRemovalReadFailureCancellationAndBudgetAreAtomic(t *testing.T) {
	data := compositeFixture(1, 8, 4, []compositeFixtureLayer{{
		color: []byte{255, 0, 0, 255}, mask: []byte{128}, tags: compositeFixtureTag("vmsk", editVectorPayload()),
	}})
	reader := &smartEditCancelReader{data: data}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	editor := lifecycleReopen(t, source)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader.trigger, reader.cancel = source.Layers[0].MaskData, cancel
	if err := editor.RemoveVectorMask(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-read removal cancellation: %v", err)
	}
	if editor.revision != 0 || editor.roots[0].vectorMaskChange != nil || reader.matches != 1 {
		t.Fatal("cancelled removal changed editor or missed source read")
	}
	failed, indexed, denied := compositeLayerOnlyEditor(t, data)
	denied.failure, denied.failureCause = indexed.Layers[0].MaskData, io.ErrClosedPipe
	if err := failed.RemoveVectorMask(nil, 1); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("removal read failure lost cause: %v", err)
	}
	if failed.revision != 0 || failed.roots[0].maskData != nil {
		t.Fatal("failed removal changed mask state")
	}
	limited := compositeEditor(t, data)
	node := limited.roots[0]
	node.setSemanticCost("other", limited.source.Limits().MaxDecodeBytes-limited.ownedBytesLocked(nil)-650)
	retained := limited.ownedBytesLocked(nil)
	if err := limited.RemoveVectorMask(nil, 1); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("removal ignored peak budget: %v", err)
	}
	if limited.revision != 0 || node.vectorMaskChange != nil || limited.ownedBytesLocked(nil) != retained {
		t.Fatal("failed removal changed state or budget accounting")
	}
}

func TestVectorLifecycleClearsRemovedVectorParametersBeforeReaddition(t *testing.T) {
	for _, generated := range []bool{false, true} {
		flags := byte(16)
		if generated {
			flags |= 8
		}
		payload := pixelMaskTestPayload(flags, 4, 0)
		data := defaultCoverageMaskFixture(t, 1, payload, compositeFixtureTag("vmsk", editVectorPayload()))
		editor := compositeEditor(t, data)
		if err := editor.RemoveVectorMask(nil, 1); err != nil {
			t.Fatal(err)
		}
		if err := editor.AddVectorMask(nil, 1, lifecycleVectorMask(t)); err != nil {
			t.Fatal(err)
		}
		lifecycleExpectPreview(t, editor, compositeSave(t, editor), color.NRGBA{R: 180, G: 90, B: 30, A: 128})
	}
}

func TestVectorLifecycleRemovedRealMaskCannotReappearWithoutVector(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		payload := append(pixelMaskTestPayload(8, 0, 255), pixelMaskTestPayload(0)[:16]...)
		editor := compositeEditor(t, pixelMaskTestFixture(t, version, payload, true))
		if err := editor.RemovePixelMask(nil, 1); err != nil {
			t.Fatal(err)
		}
		if err := editor.RemoveVectorMask(nil, 1); err != nil {
			t.Fatal(err)
		}
		saved := compositeSave(t, editor)
		lifecycleExpectMaskRemoved(t, saved, 0)
		lifecycleExpectPreview(t, editor, saved, color.NRGBA{R: 180, G: 90, B: 30, A: 255})
		if err := editor.AddPixelMask(nil, 1, image.Rect(0, 0, 1, 1), lifecycleMaskPlane(8, 128)); err != nil {
			t.Fatalf("re-adding an ordinary user mask after removing both masks failed: %v", err)
		}
		lifecycleExpectPreview(t, editor, compositeSave(t, editor), color.NRGBA{R: 180, G: 90, B: 30, A: 128})
		if err := editor.AddVectorMask(nil, 1, lifecycleVectorMask(t)); err != nil {
			t.Fatal(err)
		}
		lifecycleExpectPreview(t, editor, compositeSave(t, editor), color.NRGBA{R: 180, G: 90, B: 30, A: 64})
	}
}

func TestVectorLifecyclePreservesOpaqueUserMaskBytes(t *testing.T) {
	data := compositeFixture(1, 8, 4, []compositeFixtureLayer{{
		color: []byte{255, 0, 0, 255}, mask: []byte{128}, tags: compositeFixtureTag("vmsk", editVectorPayload()),
	}})
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	offset := source.Layers[0].MaskData.Offset
	data[offset+21], data[offset+22], data[offset+23] = 0x80, 0xa1, 0xb2
	_ = source.Close()
	editor := compositeEditor(t, data)
	original, err := editor.source.ReadSpan(editor.source.Layers[0].MaskData)
	if err != nil {
		t.Fatal(err)
	}
	if err := editor.RemoveVectorMask(nil, 1); err != nil {
		t.Fatal(err)
	}
	preview := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	preview.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 128})
	if _, err := editor.Composite(nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown retained user mask flags lost unsupported status: %v", err)
	}
	saved := compositeSave(t, editor, SaveOptions{CompositeImage: preview})
	persisted, err := saved.ReadSpan(saved.Layers[0].MaskData)
	if err != nil || !bytes.Equal(persisted, original) {
		t.Fatalf("vector removal changed opaque user mask header or tail: %v", err)
	}
}
