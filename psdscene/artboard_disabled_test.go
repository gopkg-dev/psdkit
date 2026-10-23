package psdscene

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func disabledArtboardEffects(masterEnabled bool) []byte {
	master := byte(0)
	if masterEnabled {
		master = 1
	}
	future := artboardTestDescriptor("futureFx",
		artboardTestEntry{"enab", "bool", []byte{1}},
		artboardTestEntry{"opaque", "tdta", append(artboardTestU32(3), 9, 87, 253)})
	descriptor := artboardTestDescriptor("Lefx",
		artboardTestEntry{"masterFXSwitch", "bool", []byte{master}},
		artboardTestEntry{"futureInactive", "Objc", future})
	return append(append(make([]byte, 4), artboardTestU32(16)...), descriptor...)
}

// disabledArtboardAddMetadata inserts independently encoded group metadata and black mask samples.
func disabledArtboardAddMetadata(t *testing.T, data []byte, index int, globalHidden bool) []byte {
	t.Helper()
	source := cachedOpen(t, data)
	group := source.Layers[index]
	if len(group.Channels) != 0 || group.MaskData.Length != 4 {
		t.Fatal("expected an unmasked source group")
	}
	mask := make([]byte, 34)
	binary.BigEndian.PutUint32(mask, 30)
	binary.BigEndian.PutUint32(mask[12:], 1)
	binary.BigEndian.PutUint32(mask[16:], 4)
	mask[21], mask[22], mask[23] = 2|16, 4|8, 79
	binary.BigEndian.PutUint64(mask[24:], math.Float64bits(3))
	vector := binary.BigEndian.AppendUint32(nil, 3)
	vector = binary.BigEndian.AppendUint32(vector, 4)
	tags := append(artboardTestBlock("vmsk", vector),
		artboardTestBlock("lfx2", disabledArtboardEffects(globalHidden))...)
	channel := binary.BigEndian.AppendUint16(nil, uint16(65534))
	if source.Header.Version == 2 {
		channel = binary.BigEndian.AppendUint64(channel, 6)
	} else {
		channel = binary.BigEndian.AppendUint32(channel, 6)
	}
	channelOffset := int64(0)
	for layerIndex, record := range source.Layers {
		for _, plane := range record.Channels {
			if layerIndex < index || channelOffset == 0 {
				channelOffset = plane.Span.Offset + plane.Span.Length
			}
		}
		if layerIndex > index && channelOffset != 0 {
			break
		}
	}
	if channelOffset == 0 {
		t.Fatal("source has no ordinary pixel channel insertion point")
	}
	delta := len(channel) + len(mask) - 4 + len(tags) + 6
	data = bytes.Clone(data)
	for _, position := range []int64{source.LayerMaskSection.Offset, source.LayerInfo.Offset} {
		if source.Header.Version == 2 {
			binary.BigEndian.PutUint64(data[position:], binary.BigEndian.Uint64(data[position:])+uint64(delta))
		} else {
			binary.BigEndian.PutUint32(data[position:], binary.BigEndian.Uint32(data[position:])+uint32(delta))
		}
	}
	binary.BigEndian.PutUint16(data[group.Span.Offset+16:], 1)
	binary.BigEndian.PutUint32(data[group.Extra.Offset-4:], uint32(group.Extra.Length)+uint32(len(mask)-4+len(tags)))
	type insertion struct {
		start, end int64
		data       []byte
	}
	changes := []insertion{
		{channelOffset, channelOffset, make([]byte, 6)},
		{group.Extra.Offset + group.Extra.Length, group.Extra.Offset + group.Extra.Length, tags},
		{group.MaskData.Offset, group.MaskData.Offset + group.MaskData.Length, mask},
		{group.Span.Offset + 18, group.Span.Offset + 18, channel},
	}
	slices.SortFunc(changes, func(left, right insertion) int {
		return cmp.Compare(right.start, left.start)
	})
	for _, change := range changes {
		updated := append(bytes.Clone(data[:change.start]), change.data...)
		data = append(updated, data[change.end:]...)
	}
	return data
}

func disabledArtboardFixture(t *testing.T, version uint16, globalHidden bool) []byte {
	t.Helper()
	data := sourceArtboardMultipleData(t, version)
	data = disabledArtboardAddMetadata(t, data, 2, globalHidden)
	data = disabledArtboardAddMetadata(t, data, 5, globalHidden)
	if globalHidden {
		resource := binary.BigEndian.AppendUint16([]byte("8BIM"), 1042)
		resource = append(resource, 0, 0)
		resource = binary.BigEndian.AppendUint32(resource, 1)
		resource = append(resource, 0, 0)
		binary.BigEndian.PutUint32(data[30:], uint32(len(resource)))
		data = append(append(bytes.Clone(data[:34]), resource...), data[34:]...)
	}
	return data
}

func TestArtboardDisabledOwnMasksAndEffectsRetainIndependentSourceRendering(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, globalHidden := range []bool{false, true} {
			t.Run(fmt.Sprintf("v%d_global%t", version, globalHidden), func(t *testing.T) {
				baselineSource := cachedOpen(t, sourceArtboardMultipleData(t, version))
				expected, err := RenderArtboards(t.Context(), baselineSource)
				if err != nil {
					t.Fatal(err)
				}
				data := disabledArtboardFixture(t, version, globalHidden)
				before := sha256.Sum256(data)
				source := cachedOpen(t, data)
				actual, err := RenderArtboards(t.Context(), source)
				if err != nil || len(actual) != 2 {
					t.Fatalf("disabled board metadata prevented independent rendering: %v", err)
				}
				for index, board := range actual {
					if !bytes.Equal(board.Pixels.Pix, expected[index].Pixels.Pix) || board.Bounds != expected[index].Bounds {
						t.Fatal("disabled board state changed pixels, cropping or board order")
					}
					selected := board.LayerIndex
					pixels, err := RenderArtboard(t.Context(), source, ArtboardRenderOptions{LayerIndex: &selected})
					if err != nil || !bytes.Equal(pixels.Pix, board.Pixels.Pix) {
						t.Fatalf("single-board selection differs: %v", err)
					}
				}
				fullExpected, err := Render(t.Context(), baselineSource)
				if err != nil {
					t.Fatal(err)
				}
				guarded, reader := sourceArtboardOpen(t, data)
				for _, index := range []int{2, 5} {
					reader.forbidden = append(reader.forbidden, guarded.Layers[index].Channels[0].Data)
				}
				full, err := Render(t.Context(), guarded)
				if err != nil || !bytes.Equal(full.Pix, fullExpected.Pix) || reader.reads != 0 {
					t.Fatalf("source artboard clip was overwritten by retained mask samples: %v", err)
				}
				if err := VisitTiles(t.Context(), guarded, full.Rect, image.Pt(1, 1), func(tile *image.NRGBA) error {
					if tile.NRGBAAt(tile.Rect.Min.X, 0) != fullExpected.NRGBAAt(tile.Rect.Min.X, 0) {
						t.Fatal("regional board clipping or outside sibling pixels changed")
					}
					return nil
				}); err != nil || reader.reads != 0 {
					t.Fatalf("regional board rendering read disabled mask samples: %v", err)
				}
				if sha256.Sum256(data) != before || source.Layers[2].Mask.Flags&2 == 0 {
					t.Fatal("rendering modified source mask metadata or encoded bytes")
				}
				_, err = RenderArtboards(t.Context(), source, ArtboardRenderOptions{MaxWorkingBytes: 1})
				if !errors.Is(err, psdfile.ErrLimit) {
					t.Fatalf("disabled artboard metadata bypassed the working budget: %v", err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				pixels, err := RenderArtboard(ctx, source, ArtboardRenderOptions{LayerIndex: new(2)})
				if pixels != nil || !errors.Is(err, context.Canceled) {
					t.Fatalf("disabled artboard metadata bypassed cancellation: %v", err)
				}
			})
		}
	}
}

func TestArtboardDisabledOwnStateReactivationRejectsBeforeRegionalSamples(t *testing.T) {
	for _, field := range []string{"pixel-mask", "vector-mask", "effects"} {
		t.Run(field, func(t *testing.T) {
			data := disabledArtboardFixture(t, 1, false)
			index := cachedOpen(t, data)
			group := index.Layers[2]
			if field == "pixel-mask" {
				data[group.Mask.Span.Offset+17] &^= 2
			} else {
				for _, block := range group.AdditionalInfo {
					if field == "vector-mask" && block.Key == "vmsk" {
						binary.BigEndian.PutUint32(data[block.Data.Offset+4:], 0)
					}
					if field == "effects" && block.Key == "lfx2" {
						set, err := index.DecodeEffects(t.Context(), block)
						if err != nil {
							t.Fatal(err)
						}
						for _, entry := range set.Descriptor.Entries {
							if entry.Key == "masterFXSwitch" {
								data[entry.Data.Offset] = 1
							}
						}
					}
				}
			}
			source := cachedOpen(t, data)
			if _, err := RenderArtboards(t.Context(), source); !errors.Is(err, psdfile.ErrUnsupported) {
				t.Fatalf("reactivated artboard state was accepted: %v", err)
			}
			_, err := RenderArtboard(t.Context(), source, ArtboardRenderOptions{LayerIndex: new(2)})
			if !errors.Is(err, psdfile.ErrUnsupported) {
				t.Fatalf("single board accepted reactivated metadata: %v", err)
			}
			guarded, reader := sourceArtboardOpen(t, data)
			for _, layer := range guarded.Layers {
				for _, channel := range layer.Channels {
					reader.forbidden = append(reader.forbidden, channel.Data)
				}
			}
			result, err := RenderTile(t.Context(), guarded, image.Rect(0, 0, 5, 1))
			if result != nil || !errors.Is(err, psdfile.ErrUnsupported) || reader.reads != 0 {
				t.Fatalf("reactivated artboard state reached regional samples: reads=%d error=%v", reader.reads, err)
			}
		})
	}
}

func TestArtboardOwnStateDoesNotHideActiveOrAmbiguousSemantics(t *testing.T) {
	for _, change := range []func(*psdmodel.Layer){
		func(layer *psdmodel.Layer) { layer.Mask.Disabled = false },
		func(layer *psdmodel.Layer) { layer.Mask.Unsupported = []string{"unknown mask semantics"} },
		func(layer *psdmodel.Layer) { layer.VectorMask.Disabled = false },
		func(layer *psdmodel.Layer) { layer.VectorMask.Unsupported = []string{"unknown vector semantics"} },
		func(layer *psdmodel.Layer) { layer.Effects.Enabled = true },
		func(layer *psdmodel.Layer) { layer.Effects.Unsupported = []string{"ambiguous effects master switch"} },
		func(layer *psdmodel.Layer) { layer.VectorMask = nil },
	} {
		layer := &psdmodel.Layer{Group: true, BlendMode: "norm",
			Mask: &psdmodel.Mask{Disabled: true}, VectorMask: &psdvector.Mask{Disabled: true},
			VectorFeather: new(3.0), Effects: &psdeffects.Set{EffectsVisible: true}}
		if err := artboardRenderLayer(layer, 0); err != nil {
			t.Fatal(err)
		}
		change(layer)
		if err := artboardRenderLayer(layer, 0); !errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatalf("active or unknown artboard metadata was accepted: %v", err)
		}
	}
}
