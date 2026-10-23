package psdedit

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestVectorRemovalPromotesRealMaskAndPreservesEncodedChannel(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, compression := range []uint16{0, 1, 2, 3} {
			t.Run(fmt.Sprintf("version%d_compression%d", version, compression), func(t *testing.T) {
				payload := append(pixelMaskTestPayload(8, 0, 255), pixelMaskTestPayload(0)[:16]...)
				seed := compositeEditor(t, pixelMaskTestFixture(t, version, payload, true))
				plane := psdfile.Plane{ID: -3, Width: 1, Height: 1, Depth: 8, RowBytes: 1, Data: []byte{128}}
				if err := seed.ReplaceChannel(1, -3, plane); err != nil {
					t.Fatal(err)
				}
				preview := image.NewNRGBA(image.Rect(0, 0, 1, 1))
				preview.SetNRGBA(0, 0, color.NRGBA{R: 180, G: 90, B: 30, A: 128})
				source := compositeSave(t, seed, SaveOptions{Compression: compression, CompositeImage: preview})
				editor := lifecycleReopen(t, source)
				var original psdfile.ChannelRecord
				for _, channel := range source.Layers[0].Channels {
					if channel.ID == -3 {
						original = channel
					}
				}
				encoded, err := source.ReadSpan(original.Span)
				if err != nil {
					t.Fatal(err)
				}
				if err := editor.SetVectorMask(nil, 1, lifecycleVectorMask(t)); err != nil {
					t.Fatal(err)
				}
				if err := editor.RemoveVectorMask(nil, 1); err != nil {
					t.Fatal(err)
				}
				saved := compositeSave(t, editor, SaveOptions{Compression: compression})
				lifecycleExpectPreview(t, editor, saved, color.NRGBA{R: 180, G: 90, B: 30, A: 128})
				if saved.Layers[0].Mask == nil || saved.Layers[0].Mask.HasReal ||
					saved.Layers[0].Mask.Flags&8 != 0 || lifecycleVectorBlocks(saved, 0) != 0 {
					t.Fatal("vector removal did not promote the real mask to ordinary user metadata")
				}
				index, channel := lifecycleMaskChannel(t, saved, 0)
				actual, err := saved.ReadSpan(channel.Span)
				if err != nil || !bytes.Equal(actual, encoded) || channel.Compression != compression {
					t.Fatalf("real user channel was recompressed during metadata migration: %v", err)
				}
				decoded, err := saved.DecodeChannel(nil, 0, index)
				if err != nil || !bytes.Equal(decoded.Data, []byte{128}) {
					t.Fatalf("promoted user samples changed: %v", err)
				}
				for _, item := range saved.Layers[0].Channels {
					if item.ID == -3 {
						t.Fatal("obsolete real-mask channel survived promotion")
					}
				}
				plane.ID, plane.Data = -2, []byte{200}
				if err := editor.ReplaceChannel(1, -2, plane); err != nil {
					t.Fatal(err)
				}
				if err := editor.AddVectorMask(nil, 1, lifecycleVectorMask(t)); err != nil {
					t.Fatal(err)
				}
				lifecycleExpectPreview(t, editor, compositeSave(t, editor), color.NRGBA{R: 180, G: 90, B: 30, A: 100})
				if err := editor.RemoveVectorMask(nil, 1); err != nil {
					t.Fatal(err)
				}
				lifecycleExpectPreview(t, editor, compositeSave(t, editor), color.NRGBA{R: 180, G: 90, B: 30, A: 200})
			})
		}
	}
}

func TestVectorRemovalRetainsOwnedRealMaskAndItsProperties(t *testing.T) {
	for _, sourceOverlay := range []bool{false, true} {
		var editor *Editor
		if sourceOverlay {
			payload := append(pixelMaskTestPayload(8, 0, 255), pixelMaskTestPayload(0)[:16]...)
			editor = compositeEditor(t, pixelMaskTestFixture(t, 1, payload, true))
			if err := editor.ReplaceChannel(1, -3, psdfile.Plane{
				ID: -3, Width: 1, Height: 1, Depth: 8, RowBytes: 1, Data: []byte{96},
			}); err != nil {
				t.Fatal(err)
			}
		} else {
			// A fresh user mask receives -3 next to the existing vector-only source.
			editor = compositeEditor(t, compositeFixture(1, 8, 4, []compositeFixtureLayer{{
				color: []byte{180, 90, 30, 255}, tags: compositeFixtureTag("vmsk", editVectorPayload()),
			}}))
			if err := editor.AddPixelMask(nil, 1, image.Rect(0, 0, 1, 1), lifecycleMaskPlane(8, 96)); err != nil {
				t.Fatal(err)
			}
		}
		settings, err := editor.PixelMask(nil, 1)
		if err != nil {
			t.Fatal(err)
		}
		settings.Density, settings.VectorDensity = new(uint8(255)), new(uint8(0))
		if err := editor.SetPixelMask(nil, 1, settings); err != nil {
			t.Fatal(err)
		}
		if err := editor.RemoveVectorMask(nil, 1); err != nil {
			t.Fatal(err)
		}
		settings, err = editor.PixelMask(nil, 1)
		if err != nil || settings.VectorDensity != nil || settings.Density == nil || *settings.Density != 255 {
			t.Fatalf("real-user parameter ownership was lost during vector removal: %v", err)
		}
		lifecycleExpectPreview(t, editor, compositeSave(t, editor), color.NRGBA{R: 180, G: 90, B: 30, A: 96})
	}
}

func TestVectorRemovalPromotesSourcePrecisionUserSamples(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, depth := range []uint16{16, 32} {
			t.Run(fmt.Sprintf("version%d_depth%d", version, depth), func(t *testing.T) {
				data := compositeFixture(version, depth, 4, []compositeFixtureLayer{{
					color: []byte{180, 90, 30, 255}, mask: []byte{64}, extraID: -3,
					tags: compositeFixtureTag("vmsk", editVectorPayload()),
				}})
				indexed, err := psdfile.Parse(data)
				if err != nil {
					t.Fatal(err)
				}
				record := indexed.Layers[0]
				payload := append(pixelMaskTestPayload(8, 0, 255), pixelMaskTestPayload(0)[:16]...)
				difference := len(payload) - int(record.Mask.Span.Length)
				for _, offset := range []int64{indexed.LayerMaskSection.Offset, indexed.LayerInfo.Offset} {
					if version == 2 {
						binary.BigEndian.PutUint64(data[offset:], binary.BigEndian.Uint64(data[offset:])+uint64(difference))
					} else {
						binary.BigEndian.PutUint32(data[offset:], binary.BigEndian.Uint32(data[offset:])+uint32(difference))
					}
				}
				offset := record.Extra.Offset - 4
				binary.BigEndian.PutUint32(data[offset:], binary.BigEndian.Uint32(data[offset:])+uint32(difference))
				binary.BigEndian.PutUint32(data[record.MaskData.Offset:], uint32(len(payload)))
				updated := append([]byte{}, data[:record.Mask.Span.Offset]...)
				updated = append(updated, payload...)
				updated = append(updated, data[record.Mask.Span.Offset+record.Mask.Span.Length:]...)
				if err := indexed.Close(); err != nil {
					t.Fatal(err)
				}
				editor := compositeEditor(t, updated)
				original, err := editor.source.DecodeChannel(nil, 0, 5)
				if err != nil {
					t.Fatal(err)
				}
				preview, err := editor.source.DecodeComposite(nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := editor.RemoveVectorMask(nil, 1); err != nil {
					t.Fatal(err)
				}
				saved := compositeSave(t, editor, SaveOptions{Composite: preview})
				index, channel := lifecycleMaskChannel(t, saved, 0)
				actual, err := saved.DecodeChannel(nil, 0, index)
				if err != nil || actual.Depth != depth || !bytes.Equal(actual.Data, original.Data) ||
					channel.Bounds != record.Mask.Bounds || saved.Layers[0].Mask.HasReal {
					t.Fatalf("real user-mask promotion lost source-precision samples or bounds: %v", err)
				}
			})
		}
	}
}
