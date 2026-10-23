package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"math"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func lifecycleMaskTestPlane() psdfile.Plane {
	return psdfile.Plane{ID: 123, Width: 1, Height: 1, Depth: 8, RowBytes: 1, Data: []byte{89}}
}

func lifecycleMaskTestEditor(t *testing.T) *Editor {
	t.Helper()
	return compositeEditor(t, compositeFixture(1, 8, 4, []compositeFixtureLayer{
		{color: []byte{180, 90, 30, 255}},
	}))
}

func TestPixelMaskLifecycleOwnsInputsAndSupportsRepeatedSettings(t *testing.T) {
	editor := lifecycleMaskTestEditor(t)
	plane := lifecycleMaskTestPlane()
	settings := PixelMaskSettings{DefaultColor: 113, Density: new(uint8(128)), Feather: new(float64(0))}
	expected := clonePixelMaskSettings(settings)
	if err := editor.AddPixelMask(nil, 1, image.Rect(-1, -2, 0, -1), plane, settings); err != nil {
		t.Fatal(err)
	}
	plane.Data[0], *settings.Density, *settings.Feather = 0, 0, 3
	if editor.roots[0].pixelMaskChange.plane.Data[0] != 89 {
		t.Fatal("AddPixelMask retained caller-owned samples")
	}
	actual, err := editor.PixelMask(nil, 1)
	if err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("AddPixelMask retained caller settings: %+v, %v", actual, err)
	}
	err = editor.AddPixelMask(nil, 1, image.Rect(0, 0, 1, 1), lifecycleMaskTestPlane())
	if !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("adding over an existing user mask = %v", err)
	}
	expected.Disabled = true
	if err := editor.SetPixelMask(nil, 1, expected); err != nil {
		t.Fatal(err)
	}
	if err := editor.RemovePixelMask(nil, 1); err != nil {
		t.Fatal(err)
	}
	state, _ := editor.Changes()
	if err := editor.RemovePixelMask(nil, 1); err != nil {
		t.Fatal(err)
	}
	after, _ := editor.Changes()
	if state.Revision != after.Revision {
		t.Fatal("removing an absent mask changed the revision")
	}
	if _, err := editor.PixelMask(nil, 1); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("removed mask getter = %v", err)
	}
	if err := editor.SetPixelMask(nil, 1, expected); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("removed mask property setter = %v", err)
	}
	if err := editor.AddPixelMask(nil, 1, image.Rect(0, 0, 1, 1), lifecycleMaskTestPlane()); err != nil {
		t.Fatal(err)
	}
	actual, err = editor.PixelMask(nil, 1)
	if err != nil || actual.DefaultColor != 255 || actual.Disabled || actual.Density != nil {
		t.Fatalf("re-added mask inherited deleted user parameters: %+v, %v", actual, err)
	}
}

func TestPixelMaskLifecycleValidationCancellationAndBudgetsAreAtomic(t *testing.T) {
	for _, test := range []struct {
		name     string
		bounds   image.Rectangle
		mutate   func(*psdfile.Plane)
		settings []PixelMaskSettings
	}{
		{name: "negative bounds", bounds: image.Rectangle{Min: image.Pt(2, 0), Max: image.Pt(1, 1)}},
		{name: "large coordinates", bounds: image.Rect(math.MaxInt32, 0, math.MaxInt32+1, 1)},
		{name: "wrong width", mutate: func(plane *psdfile.Plane) { plane.Width = 2 }},
		{name: "wrong depth", mutate: func(plane *psdfile.Plane) { plane.Depth = 16 }},
		{name: "wrong stride", mutate: func(plane *psdfile.Plane) { plane.RowBytes = 2 }},
		{name: "truncated samples", mutate: func(plane *psdfile.Plane) { plane.Data = nil }},
		{name: "invalid feather", settings: []PixelMaskSettings{{Feather: new(math.NaN())}}},
		{name: "duplicate settings", settings: []PixelMaskSettings{{}, {}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			editor := lifecycleMaskTestEditor(t)
			plane, bounds := lifecycleMaskTestPlane(), test.bounds
			if bounds == (image.Rectangle{}) {
				bounds = image.Rect(0, 0, 1, 1)
			}
			if test.mutate != nil {
				test.mutate(&plane)
			}
			if err := editor.AddPixelMask(nil, 1, bounds, plane, test.settings...); !errors.Is(err, ErrInvalidEdit) {
				t.Fatalf("invalid mask edit = %v", err)
			}
			state, _ := editor.Changes()
			if state.Dirty || editor.roots[0].pixelMaskChange != nil || editor.roots[0].maskData != nil {
				t.Fatal("invalid AddPixelMask changed editor state")
			}
		})
	}
	editor := lifecycleMaskTestEditor(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := editor.AddPixelMask(ctx, 1, image.Rect(0, 0, 1, 1), lifecycleMaskTestPlane())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled AddPixelMask = %v", err)
	}
	if err := editor.RemovePixelMask(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled RemovePixelMask = %v", err)
	}
	node := editor.roots[0]
	node.setSemanticCost("other", editor.source.Limits().MaxDecodeBytes-editor.ownedBytesLocked(nil)-660)
	retained := editor.ownedBytesLocked(nil)
	err = editor.AddPixelMask(nil, 1, image.Rect(0, 0, 1, 1), lifecycleMaskTestPlane())
	if !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("AddPixelMask exceeded its peak budget: %v", err)
	}
	state, _ := editor.Changes()
	if state.Dirty || node.pixelMaskChange != nil || node.maskData != nil || editor.ownedBytesLocked(nil) != retained {
		t.Fatal("limited mask addition changed editor state or accounting")
	}
}

func TestPixelMaskLifecycleReadErrorsAndRemovalBudgetAreAtomic(t *testing.T) {
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
	if err := editor.RemovePixelMask(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-read cancelled RemovePixelMask = %v", err)
	}
	state, _ := editor.Changes()
	if state.Dirty || reader.matches != 1 || editor.roots[0].pixelMaskChange != nil {
		t.Fatal("cancelled removal mutated state or missed the source read")
	}
	failed, indexed, denied := compositeLayerOnlyEditor(t, data)
	denied.failure, denied.failureCause = indexed.Layers[0].MaskData, io.ErrClosedPipe
	if err := failed.RemovePixelMask(nil, 1); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("source read failure cause = %v", err)
	}
	state, _ = failed.Changes()
	if state.Dirty || failed.roots[0].pixelMaskChange != nil {
		t.Fatal("failed source read changed removal state")
	}
	limited := compositeEditor(t, data)
	node := limited.roots[0]
	node.setSemanticCost("other", limited.source.Limits().MaxDecodeBytes-limited.ownedBytesLocked(nil)-650)
	retained := limited.ownedBytesLocked(nil)
	if err := limited.RemovePixelMask(nil, 1); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("limited RemovePixelMask = %v", err)
	}
	state, _ = limited.Changes()
	if state.Dirty || node.pixelMaskChange != nil || node.maskData != nil || limited.ownedBytesLocked(nil) != retained {
		t.Fatal("limited removal changed editor state or accounting")
	}
}

func TestPixelMaskLifecycleRefusesAmbiguousDualMasks(t *testing.T) {
	for _, realFlags := range []byte{0, 2} {
		payload := append(pixelMaskTestPayload(0, realFlags, 255), pixelMaskTestPayload(0)[:16]...)
		editor := compositeEditor(t, pixelMaskTestFixture(t, 1, payload, false, true))
		if err := editor.RemovePixelMask(nil, 1); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("ambiguous dual mask removal = %v", err)
		}
		state, _ := editor.Changes()
		if state.Dirty || editor.roots[0].maskData != nil || editor.roots[0].pixelMaskChange != nil {
			t.Fatal("ambiguous removal partially changed state")
		}
	}
}

func TestPixelMaskLifecycleRejectsChannelLimitBeforeMutation(t *testing.T) {
	editor := lifecycleMaskTestEditor(t)
	record := &editor.source.Layers[0]
	for len(record.Channels) < 56 {
		record.Channels = append(record.Channels, psdfile.ChannelRecord{ID: int16(len(record.Channels))})
	}
	err := editor.AddPixelMask(nil, 1, image.Rect(0, 0, 1, 1), lifecycleMaskTestPlane())
	if !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("mask addition above 56 channels = %v", err)
	}
	state, _ := editor.Changes()
	if state.Dirty || editor.roots[0].pixelMaskChange != nil || editor.roots[0].maskData != nil {
		t.Fatal("over-limit mask addition partially changed the editor")
	}
}

func TestPixelMaskLifecyclePreservesSharedParametersThroughRemovalAndReaddition(t *testing.T) {
	editor := compositeEditor(t, compositeFixture(1, 8, 4, []compositeFixtureLayer{
		{color: []byte{180, 90, 30, 255}, mask: []byte{89}, tags: compositeFixtureTag("vmsk", editVectorPayload())},
	}))
	settings, err := editor.PixelMask(nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	settings.VectorDensity, settings.VectorFeather = new(uint8(123)), new(float64(0))
	settings.Density, settings.Feather = new(uint8(77)), new(float64(1))
	if err := editor.SetPixelMask(nil, 1, settings); err != nil {
		t.Fatal(err)
	}
	if err := editor.RemovePixelMask(nil, 1); err != nil {
		t.Fatal(err)
	}
	header, err := decodePixelMaskHeader(editor.roots[0].maskData, false)
	if err != nil || !equalPixelMaskValue(header.settings.VectorDensity, settings.VectorDensity) ||
		!equalPixelMaskValue(header.settings.VectorFeather, settings.VectorFeather) ||
		header.settings.Density != nil || header.settings.Feather != nil {
		t.Fatalf("shared metadata after removal: %+v, %v", header.settings, err)
	}
	if err := editor.AddPixelMask(nil, 1, image.Rect(0, 0, 1, 1), lifecycleMaskTestPlane()); err != nil {
		t.Fatal(err)
	}
	actual, err := editor.PixelMask(nil, 1)
	if err != nil || !equalPixelMaskValue(actual.VectorDensity, settings.VectorDensity) ||
		!equalPixelMaskValue(actual.VectorFeather, settings.VectorFeather) ||
		actual.Density != nil || actual.Feather != nil {
		t.Fatalf("re-added mask did not inherit only shared vector parameters: %+v, %v", actual, err)
	}
	if editor.roots[0].pixelMaskChange.channelID != -2 ||
		!bytes.Equal(editor.roots[0].pixelMaskChange.plane.Data, []byte{89}) {
		t.Fatal("re-added mask stopped suppressing its original source channel")
	}
}

func TestPixelMaskLifecycleRemovalDropsParametersWithoutVectorGeometry(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		editor := compositeEditor(t, compositeFixture(version, 8, 4, []compositeFixtureLayer{
			{color: []byte{180, 90, 30, 255}},
		}))
		settings := PixelMaskSettings{
			DefaultColor: 255, VectorDensity: new(uint8(100)), VectorFeather: new(float64(0.5)),
		}
		if err := editor.AddPixelMask(nil, 1, image.Rect(0, 0, 1, 1), lifecycleMaskTestPlane(), settings); err != nil {
			t.Fatal(err)
		}
		if err := editor.RemovePixelMask(nil, 1); err != nil {
			t.Fatal(err)
		}
		before, err := editor.Composite(nil)
		if err != nil {
			t.Fatal(err)
		}
		saved := compositeSave(t, editor)
		if saved.Layers[0].Mask != nil {
			t.Fatal("removal created a mask carrier without vector geometry")
		}
		view, err := saved.ReadLayerMetadata(nil, 0)
		if err != nil || view.Layer.Mask != nil || view.Layer.VectorDensity != nil || view.Layer.VectorFeather != nil {
			t.Fatalf("removed mask retained semantic metadata: %+v, %v", view.Layer, err)
		}
		reopened, err := New(saved)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		after, err := reopened.Composite(nil)
		if err != nil || !bytes.Equal(before.Pix, after.Pix) {
			t.Fatalf("removed mask preview changed after reopen: %v", err)
		}
	}
}

func TestPixelMaskLifecycleRemovesDefaultCoverageWithoutSampleChannel(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		data := compositeFixture(version, 8, 4, []compositeFixtureLayer{{color: []byte{180, 90, 30, 255}}})
		source, err := psdfile.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		record := source.Layers[0]
		for _, offset := range []int64{source.LayerMaskSection.Offset, source.LayerInfo.Offset} {
			if version == 2 {
				binary.BigEndian.PutUint64(data[offset:], binary.BigEndian.Uint64(data[offset:])+20)
			} else {
				binary.BigEndian.PutUint32(data[offset:], binary.BigEndian.Uint32(data[offset:])+20)
			}
		}
		offset := record.Extra.Offset - 4
		binary.BigEndian.PutUint32(data[offset:], binary.BigEndian.Uint32(data[offset:])+20)
		payload := pixelMaskTestPayload(0, 0xa1, 0xb2)
		payload[16] = 0
		binary.BigEndian.PutUint32(data[record.MaskData.Offset:], uint32(len(payload)))
		insertion := record.MaskData.Offset + 4
		fixture := append([]byte{}, data[:insertion]...)
		fixture = append(fixture, payload...)
		fixture = append(fixture, data[insertion:]...)
		_ = source.Close()
		editor := compositeEditor(t, fixture)
		before, err := editor.Composite(nil)
		if err != nil || before.NRGBAAt(0, 0).A != 0 {
			t.Fatalf("metadata-only default coverage was ignored: %v, %v", before, err)
		}
		err = editor.AddPixelMask(nil, 1, image.Rect(0, 0, 1, 1), lifecycleMaskTestPlane())
		if !errors.Is(err, ErrInvalidEdit) {
			t.Fatalf("addition overwrote an existing metadata-only user mask: %v", err)
		}
		unchanged, _ := editor.Changes()
		if unchanged.Dirty {
			t.Fatal("rejected addition changed a metadata-only user mask")
		}
		if err := editor.RemovePixelMask(nil, 1); err != nil {
			t.Fatal(err)
		}
		after, err := editor.Composite(nil)
		if err != nil || after.NRGBAAt(0, 0).A != 255 {
			t.Fatalf("deleting metadata-only mask did not restore full coverage: %v, %v", after, err)
		}
		saved := compositeSave(t, editor)
		if saved.Layers[0].Mask != nil || len(saved.Layers[0].Channels) != len(record.Channels) {
			t.Fatal("metadata-only removal changed raster channels or retained a mask")
		}
		reopened, err := New(saved)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		pixels, err := reopened.Composite(nil)
		if err != nil || !bytes.Equal(pixels.Pix, after.Pix) {
			t.Fatalf("metadata-only mask removal changed after saving and reopening: %v", err)
		}
	}
}
