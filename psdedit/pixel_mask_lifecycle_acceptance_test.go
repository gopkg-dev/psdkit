package psdedit

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func lifecycleMaskPlane(depth uint16, samples ...byte) psdfile.Plane {
	return psdfile.Plane{
		ID: 7, Width: len(samples) / int(depth/8), Height: 1, Depth: depth,
		RowBytes: len(samples), Data: samples,
	}
}

func lifecycleReopen(t *testing.T, source *psdfile.Document) *Editor {
	t.Helper()
	editor, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = editor.Close() })
	return editor
}

func lifecycleMaskChannel(t *testing.T, source *psdfile.Document, layerIndex int) (int, psdfile.ChannelRecord) {
	t.Helper()
	for index, channel := range source.Layers[layerIndex].Channels {
		if channel.ID == -2 {
			return index, channel
		}
	}
	t.Fatal("saved layer has no user-mask channel")
	return 0, psdfile.ChannelRecord{}
}

func lifecycleExpectMaskRemoved(t *testing.T, source *psdfile.Document, layerIndex int) {
	t.Helper()
	if source.Layers[layerIndex].Mask != nil || source.Layers[layerIndex].MaskData.Length != 4 {
		t.Fatal("removing the mask left mask metadata in the saved layer")
	}
	for _, channel := range source.Layers[layerIndex].Channels {
		if channel.ID == -2 || channel.ID == -3 {
			t.Fatal("removing the mask left its channel in the saved layer")
		}
	}
}

func lifecycleExpectPreserved(t *testing.T, before, after *psdfile.Document, layerIndex int) {
	t.Helper()
	for _, oldChannel := range before.Layers[layerIndex].Channels {
		if oldChannel.ID == -2 || oldChannel.ID == -3 {
			continue
		}
		found := false
		for _, channel := range after.Layers[layerIndex].Channels {
			if channel.ID != oldChannel.ID {
				continue
			}
			found = true
			oldBytes, err := before.ReadSpan(oldChannel.Span)
			if err != nil {
				t.Fatal(err)
			}
			newBytes, err := after.ReadSpan(channel.Span)
			if err != nil || !bytes.Equal(oldBytes, newBytes) {
				t.Fatalf("non-mask channel %d changed: %v", channel.ID, err)
			}
		}
		if !found {
			t.Fatalf("non-mask channel %d disappeared", oldChannel.ID)
		}
	}
	beforeTags, afterTags := before.Layers[layerIndex].AdditionalInfo, after.Layers[layerIndex].AdditionalInfo
	if len(beforeTags) != len(afterTags) {
		t.Fatal("mask edit changed the number of original additional blocks")
	}
	for index, block := range beforeTags {
		oldBytes, err := before.ReadSpan(block.Span)
		if err != nil {
			t.Fatal(err)
		}
		newBytes, err := after.ReadSpan(afterTags[index].Span)
		if err != nil || !bytes.Equal(oldBytes, newBytes) {
			t.Fatalf("original additional block %s changed: %v", block.Key, err)
		}
	}
}

func lifecycleExpectPreview(
	t *testing.T, editor *Editor, saved *psdfile.Document, expected color.NRGBA,
) *Editor {
	t.Helper()
	preview, err := editor.Composite(t.Context())
	if err != nil || preview.NRGBAAt(0, 0) != expected {
		t.Fatalf("edited preview = %v, want %v, error %v", preview, expected, err)
	}
	reopened := lifecycleReopen(t, saved)
	persisted, err := reopened.Composite(t.Context())
	if err != nil || !bytes.Equal(persisted.Pix, preview.Pix) {
		t.Fatalf("saved and reopened layer preview differs from the edit: %v", err)
	}
	merged, err := saved.DecodeComposite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Channels) == 3 && expected.A == 255 {
		return reopened
	}
	if len(merged.Channels) != 4 || merged.Channels[3].Data[0] != expected.A {
		t.Fatalf("saved composite retained a stale mask alpha: %v", err)
	}
	return reopened
}

func TestPixelMaskLifecyclePSDPSBAllCompressions(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, compression := range []uint16{0, 1, 2, 3} {
			t.Run(fmt.Sprintf("version%d_compression%d", version, compression), func(t *testing.T) {
				tags := compositeFixtureTag("pRsv", []byte{0x96, 0x83, 0xa1})
				tags = append(tags, compositeFixtureTag("pRsv", []byte{0x7b, 0x59})...)
				data := compositeFixture(version, 8, 4, []compositeFixtureLayer{
					{color: []byte{255, 0, 0, 255}, tags: tags},
				})
				original := bytes.Clone(data)
				editor := compositeEditor(t, data)
				plane := lifecycleMaskPlane(8, 11, 64)
				bounds := image.Rect(-1, 0, 1, 1)
				if err := editor.AddPixelMask(t.Context(), 1, bounds, plane); err != nil {
					t.Fatal(err)
				}
				plane.Data[1] = 0
				settings, err := editor.PixelMask(t.Context(), 1)
				if err != nil || settings.DefaultColor != 255 {
					t.Fatalf("new mask defaults = %+v, %v", settings, err)
				}
				state, _ := editor.Changes()
				if err := editor.AddPixelMask(t.Context(), 1, bounds, plane); !errors.Is(err, ErrInvalidEdit) {
					t.Fatalf("adding over an existing mask unexpectedly succeeded: %v", err)
				}
				afterRejected, _ := editor.Changes()
				if afterRejected != state {
					t.Fatal("rejected duplicate addition changed editing state")
				}
				saved := compositeSave(t, editor, SaveOptions{Compression: compression})
				reopened := lifecycleExpectPreview(t, editor, saved, color.NRGBA{R: 255, A: 64})
				compositeExpect(t, saved, []byte{255, 191, 191, 64})
				index, channel := lifecycleMaskChannel(t, saved, 0)
				decoded, err := saved.DecodeChannel(t.Context(), 0, index)
				if err != nil || channel.Compression != compression || channel.Bounds != bounds ||
					decoded.ID != -2 || !bytes.Equal(decoded.Data, []byte{11, 64}) {
					t.Fatalf("new mask samples, geometry or compression did not survive save: %+v, %v", channel, err)
				}
				lifecycleExpectPreserved(t, editor.source, saved, 0)
				if err := reopened.RemovePixelMask(t.Context(), 1); err != nil {
					t.Fatal(err)
				}
				removedState, _ := reopened.Changes()
				if err := reopened.RemovePixelMask(t.Context(), 1); err != nil {
					t.Fatal(err)
				}
				removedAgain, _ := reopened.Changes()
				if removedState != removedAgain {
					t.Fatal("removing an already removed mask was not a no-op")
				}
				removed := compositeSave(t, reopened, SaveOptions{Compression: compression})
				lifecycleExpectMaskRemoved(t, removed, 0)
				lifecycleExpectPreserved(t, editor.source, removed, 0)
				lifecycleExpectPreview(t, reopened, removed, color.NRGBA{R: 255, A: 255})
				compositeExpect(t, removed, []byte{255, 0, 0, 255})
				if err := reopened.AddPixelMask(t.Context(), 1, image.Rect(0, 0, 1, 1),
					lifecycleMaskPlane(8, 96), PixelMaskSettings{DefaultColor: 17}); err != nil {
					t.Fatal(err)
				}
				readded := compositeSave(t, reopened, SaveOptions{Compression: compression})
				lifecycleExpectPreview(t, reopened, readded, color.NRGBA{R: 255, A: 96})
				compositeExpect(t, readded, []byte{255, 159, 159, 96})
				if readded.Layers[0].Mask.DefaultColor != 17 || readded.Header.Version != version {
					t.Fatal("delete/add cycle lost mask metadata or source file format")
				}
				if !bytes.Equal(data, original) {
					t.Fatal("mask lifecycle mutated original source bytes")
				}
			})
		}
	}
}

func TestPixelMaskLifecycleSourcePrecisionWithExplicitComposite(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, depth := range []uint16{16, 32} {
			for _, compression := range []uint16{0, 1, 2, 3} {
				t.Run(fmt.Sprintf("version%d_depth%d_compression%d", version, depth, compression), func(t *testing.T) {
					editor := compositeEditor(t, compositeFixture(version, depth, 4, []compositeFixtureLayer{
						{color: []byte{180, 90, 30, 255}},
					}))
					samples := []byte{0x12, 0x34, 0xff, 0xff}
					if depth == 32 {
						samples = binary.BigEndian.AppendUint32(nil, math.Float32bits(0.375))
						samples = binary.BigEndian.AppendUint32(samples, math.Float32bits(1))
					}
					if err := editor.AddPixelMask(t.Context(), 1, image.Rect(-1, 0, 1, 1),
						lifecycleMaskPlane(depth, samples...)); err != nil {
						t.Fatal(err)
					}
					var automatic bytes.Buffer
					if err := editor.Write(t.Context(), &automatic); !errors.Is(err, ErrUnsupported) || automatic.Len() != 0 {
						t.Fatalf("high-depth visual edit silently saved a stale automatic preview: %v", err)
					}
					preview, err := editor.source.DecodeComposite(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					options := SaveOptions{Compression: compression, Composite: preview}
					saved := compositeSave(t, editor, options)
					index, channel := lifecycleMaskChannel(t, saved, 0)
					decoded, err := saved.DecodeChannel(t.Context(), 0, index)
					expectedCompression := compression
					if depth == 32 && compression == 2 {
						expectedCompression = 3
					}
					if err != nil || decoded.Depth != depth || !bytes.Equal(decoded.Data, samples) ||
						channel.Compression != expectedCompression || saved.Header.Depth != depth {
						t.Fatalf("mask samples lost source precision or encoding: %+v, %v", channel, err)
					}
					merged, err := saved.DecodeComposite(t.Context())
					if err != nil || !reflect.DeepEqual(merged, preview) {
						t.Fatalf("explicit composite did not retain source-precision samples: %v", err)
					}
					lifecycleExpectPreserved(t, editor.source, saved, 0)
					reopened := lifecycleReopen(t, saved)
					if _, err := reopened.PixelMask(t.Context(), 1); err != nil {
						t.Fatal(err)
					}
					if err := reopened.RemovePixelMask(t.Context(), 1); err != nil {
						t.Fatal(err)
					}
					removed := compositeSave(t, reopened, options)
					lifecycleExpectMaskRemoved(t, removed, 0)
					lifecycleExpectPreserved(t, editor.source, removed, 0)
					if _, err := lifecycleReopen(t, removed).PixelMask(t.Context(), 1); !errors.Is(err, ErrUnsupported) {
						t.Fatalf("removed high-depth mask was resurrected on reopen: %v", err)
					}
				})
			}
		}
	}
}

func TestPixelMaskLifecycleGroupsNewAndVirtualLayers(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, kind := range []string{"existing group", "new raster", "new group", "virtual background"} {
			t.Run(fmt.Sprintf("version%d_%s", version, kind), func(t *testing.T) {
				var editor *Editor
				var id LayerID
				expected := color.NRGBA{R: 255, A: 64}
				switch kind {
				case "existing group":
					editor = compositeEditor(t, compositeFixture(version, 8, 4, []compositeFixtureLayer{
						{divider: 3}, {color: []byte{255, 0, 0, 255}}, {divider: 1, blend: "norm"},
					}))
					id = 3
				case "virtual background":
					editor = compositeEditor(t, compositeFixture(version, 8, 3, nil))
					id, expected = 1, color.NRGBA{R: 30, G: 70, B: 110, A: 64}
				default:
					editor = compositeEditor(t, compositeFixture(version, 8, 4, []compositeFixtureLayer{
						{color: []byte{0, 0, 0, 0}},
					}))
					parent := LayerID(0)
					position := 1
					if kind == "new group" {
						var err error
						parent, err = editor.AddGroup(0, 1, "masked group", true)
						if err != nil {
							t.Fatal(err)
						}
						position = 0
					}
					pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
					pixels.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
					var err error
					id, err = editor.AddRaster(parent, position, "masked raster", pixels, image.Point{})
					if err != nil {
						t.Fatal(err)
					}
					if parent != 0 {
						id = parent
					}
				}
				if err := editor.AddPixelMask(t.Context(), id, image.Rect(0, 0, 1, 1),
					lifecycleMaskPlane(8, 64)); err != nil {
					t.Fatal(err)
				}
				if kind == "existing group" || kind == "new group" {
					preview, err := editor.CompositeGroup(t.Context(), id)
					if err != nil || preview.NRGBAAt(0, 0) != expected {
						t.Fatalf("group preview ignored its newly added mask: %v", err)
					}
				}
				saved := compositeSave(t, editor, SaveOptions{Compression: 3})
				lifecycleExpectPreview(t, editor, saved, expected)
				if err := editor.RemovePixelMask(t.Context(), id); err != nil {
					t.Fatal(err)
				}
				expected.A = 255
				removed := compositeSave(t, editor, SaveOptions{Compression: 1})
				lifecycleExpectPreview(t, editor, removed, expected)
				for index := range removed.Layers {
					lifecycleExpectMaskRemoved(t, removed, index)
				}
			})
		}
	}
}

func TestPixelMaskLifecyclePixelAndChannelEditOrder(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, order := range []string{
			"add then pixels", "pixels then add", "color channel then add", "add then color channel",
			"add then mask channel", "source mask channel then remove and add", "remove then pixels and add",
		} {
			t.Run(fmt.Sprintf("version%d_%s", version, order), func(t *testing.T) {
				layer := compositeFixtureLayer{color: []byte{255, 0, 0, 255}}
				if order == "source mask channel then remove and add" || order == "remove then pixels and add" {
					layer.mask = []byte{32}
				}
				editor := compositeEditor(t, compositeFixture(version, 8, 4, []compositeFixtureLayer{layer}))
				add := func() error {
					return editor.AddPixelMask(t.Context(), 1, image.Rect(0, 0, 1, 1), lifecycleMaskPlane(8, 64))
				}
				replacePixels := func() error {
					pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
					pixels.SetNRGBA(0, 0, color.NRGBA{B: 255, A: 255})
					return editor.ReplacePixels(1, pixels)
				}
				replaceChannel := func(channel int16, value byte) error {
					plane := lifecycleMaskPlane(8, value)
					plane.ID = channel
					return editor.ReplaceChannel(1, channel, plane)
				}
				expected := color.NRGBA{R: 255, A: 64}
				var edits []func() error
				switch order {
				case "add then pixels":
					edits, expected = []func() error{add, replacePixels}, color.NRGBA{B: 255, A: 64}
				case "pixels then add":
					edits, expected = []func() error{replacePixels, add}, color.NRGBA{B: 255, A: 64}
				case "color channel then add":
					edits, expected = []func() error{func() error { return replaceChannel(0, 128) }, add},
						color.NRGBA{R: 128, A: 64}
				case "add then color channel":
					edits, expected = []func() error{add, func() error { return replaceChannel(0, 128) }},
						color.NRGBA{R: 128, A: 64}
				case "add then mask channel":
					edits, expected = []func() error{add, func() error { return replaceChannel(-2, 96) }},
						color.NRGBA{R: 255, A: 96}
				case "source mask channel then remove and add":
					edits = []func() error{
						func() error { return replaceChannel(-2, 21) },
						func() error { return editor.RemovePixelMask(t.Context(), 1) }, add,
					}
				case "remove then pixels and add":
					edits = []func() error{
						func() error { return editor.RemovePixelMask(t.Context(), 1) }, replacePixels, add,
					}
					expected = color.NRGBA{B: 255, A: 64}
				}
				for index, edit := range edits {
					if err := edit(); err != nil {
						t.Fatalf("edit %d: %v", index+1, err)
					}
				}
				saved := compositeSave(t, editor, SaveOptions{Compression: 2})
				lifecycleExpectPreview(t, editor, saved, expected)
				if err := editor.RemovePixelMask(t.Context(), 1); err != nil {
					t.Fatal(err)
				}
				before, _ := editor.Changes()
				if err := replaceChannel(-2, 200); !errors.Is(err, ErrInvalidEdit) {
					t.Fatalf("channel replacement resurrected removed mask: %v", err)
				}
				after, _ := editor.Changes()
				if before != after {
					t.Fatal("rejected removed-channel edit changed editing state")
				}
				expected.A = 255
				removed := compositeSave(t, editor)
				lifecycleExpectMaskRemoved(t, removed, 0)
				lifecycleExpectPreview(t, editor, removed, expected)
			})
		}
	}
}

func TestPixelMaskLifecycleVectorCoverageAndSharedParameters(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, generated := range []bool{false, true} {
			t.Run(fmt.Sprintf("version%d_generated%t", version, generated), func(t *testing.T) {
				data := compositeFixture(version, 8, 4, []compositeFixtureLayer{
					{color: []byte{180, 90, 30, 255}, tags: compositeFixtureTag("vmsk", editVectorPayload())},
				})
				if generated {
					payload := pixelMaskTestPayload(8|16, 0, 255)
					payload = append(payload, pixelMaskTestPayload(0)[:16]...)
					payload = append(payload, 12, 211)
					payload = binary.BigEndian.AppendUint64(payload, math.Float64bits(0))
					payload = append(payload, 0xa1, 0xb2)
					data = pixelMaskTestFixture(t, version, payload, true)
				}
				editor := compositeEditor(t, data)
				var originalGenerated []byte
				if generated {
					_, channel := lifecycleMaskChannel(t, editor.source, 0)
					var err error
					originalGenerated, err = editor.source.ReadSpan(channel.Span)
					if err != nil {
						t.Fatal(err)
					}
					if err := editor.RemovePixelMask(t.Context(), 1); err != nil {
						t.Fatal(err)
					}
					removed := compositeSave(t, editor)
					if removed.Layers[0].Mask.HasReal || removed.Layers[0].Mask.Flags&8 == 0 {
						t.Fatal("removing the real user mask removed generated vector metadata")
					}
					for _, channel := range removed.Layers[0].Channels {
						if channel.ID == -3 {
							t.Fatal("removed real user-mask channel remained in the file")
						}
					}
					editor = lifecycleExpectPreview(t, editor, removed, color.NRGBA{R: 180, G: 90, B: 30, A: 255})
					if _, err := editor.PixelMask(t.Context(), 1); !errors.Is(err, ErrUnsupported) {
						t.Fatalf("generated-only vector coverage was treated as a user mask: %v", err)
					}
				}
				if err := editor.AddPixelMask(t.Context(), 1, image.Rect(0, 0, 1, 1),
					lifecycleMaskPlane(8, 96)); err != nil {
					t.Fatal(err)
				}
				saved := compositeSave(t, editor, SaveOptions{Compression: 3})
				reopened := lifecycleExpectPreview(t, editor, saved, color.NRGBA{R: 180, G: 90, B: 30, A: 96})
				mask := saved.Layers[0].Mask
				if mask.Flags&8 == 0 || !mask.HasReal {
					t.Fatal("new mask on a vector layer did not use the real user-mask record")
				}
				realCount := 0
				for index, channel := range saved.Layers[0].Channels {
					if channel.ID == -3 {
						realCount++
						plane, err := saved.DecodeChannel(t.Context(), 0, index)
						if err != nil || !bytes.Equal(plane.Data, []byte{96}) || channel.Compression != 3 {
							t.Fatalf("saved real mask samples or compression differ: %v", err)
						}
					}
				}
				if realCount != 1 {
					t.Fatal("new user mask did not select exactly one real -3 channel")
				}
				if generated {
					_, channel := lifecycleMaskChannel(t, saved, 0)
					preserved, err := saved.ReadSpan(channel.Span)
					if err != nil || !bytes.Equal(preserved, originalGenerated) {
						t.Fatalf("generated vector-coverage channel was rewritten: %v", err)
					}
					settings, err := reopened.PixelMask(t.Context(), 1)
					if err != nil || settings.VectorDensity == nil || *settings.VectorDensity != 211 ||
						settings.VectorFeather == nil || *settings.VectorFeather != 0 {
						t.Fatalf("delete/add lost shared vector density or feather: %+v, %v", settings, err)
					}
					raw, err := saved.ReadSpan(mask.Span)
					if err != nil || !bytes.Equal(raw[len(raw)-2:], []byte{0xa1, 0xb2}) {
						t.Fatalf("generated-mask opaque trailing bytes changed: %v", err)
					}
				}
				lifecycleExpectPreserved(t, editor.source, saved, 0)
			})
		}
	}
}

func TestPixelMaskLifecycleEmptyBoundsUsesDefaultCoverage(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			editor := compositeEditor(t, compositeFixture(version, 8, 4, []compositeFixtureLayer{
				{color: []byte{255, 0, 0, 255}},
			}))
			if err := editor.AddPixelMask(t.Context(), 1, image.Rectangle{}, psdfile.Plane{Depth: 8},
				PixelMaskSettings{DefaultColor: 64}); err != nil {
				t.Fatal(err)
			}
			saved := compositeSave(t, editor, SaveOptions{Compression: 1})
			lifecycleExpectPreview(t, editor, saved, color.NRGBA{R: 255, A: 64})
			index, channel := lifecycleMaskChannel(t, saved, 0)
			plane, err := saved.DecodeChannel(t.Context(), 0, index)
			if err != nil || !channel.Bounds.Empty() || len(plane.Data) != 0 {
				t.Fatalf("empty mask acquired spurious sample data: %v", err)
			}
		})
	}
}

func TestPixelMaskLifecycleAtomicFileSaveAndOpen(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			editor := compositeEditor(t, compositeFixture(version, 8, 4, []compositeFixtureLayer{
				{color: []byte{255, 0, 0, 255}},
			}))
			if err := editor.AddPixelMask(t.Context(), 1, image.Rect(0, 0, 1, 1), lifecycleMaskPlane(8, 64)); err != nil {
				t.Fatal(err)
			}
			extension := ".psd"
			if version == 2 {
				extension = ".psb"
			}
			filename := filepath.Join(t.TempDir(), "mask-lifecycle"+extension)
			if err := editor.SavePSD(t.Context(), filename, SaveOptions{Compression: 3}); err != nil {
				t.Fatal(err)
			}
			if err := editor.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(filename)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			preview, err := reopened.Composite(t.Context())
			if err != nil || preview.NRGBAAt(0, 0) != (color.NRGBA{R: 255, A: 64}) {
				t.Fatalf("disk reopening lost the added mask: %v", err)
			}
			if err := reopened.RemovePixelMask(t.Context(), 1); err != nil {
				t.Fatal(err)
			}
			if err := reopened.SavePSD(t.Context(), filename, SaveOptions{Compression: 1}); err != nil {
				t.Fatal(err)
			}
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
			removed, err := Open(filename)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = removed.Close() })
			lifecycleExpectMaskRemoved(t, removed.source, 0)
			preview, err = removed.Composite(t.Context())
			if err != nil || preview.NRGBAAt(0, 0) != (color.NRGBA{R: 255, A: 255}) {
				t.Fatalf("replacing and reopening the same disk file resurrected its removed mask: %v", err)
			}
		})
	}
}
