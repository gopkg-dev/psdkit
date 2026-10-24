package psdedit

import (
	"bytes"
	"encoding/binary"
	"image"
	"math"
	"slices"
	"strconv"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func smartWarpReplacementOffsetFixture(t *testing.T, version uint16, origin psdmodel.Point) []byte {
	t.Helper()
	data := gateSmartFixture(t, version, true, false)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	records, err := source.SmartObjects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		for _, outer := range record.Descriptor.Entries {
			if outer.Key != "warp" {
				continue
			}
			for _, entry := range outer.Value.(*psdfile.Descriptor).Entries {
				switch entry.Key {
				case "bounds":
					for _, edge := range entry.Value.(*psdfile.Descriptor).Entries {
						delta := origin.X
						if edge.Key == "Top " || edge.Key == "Btom" {
							delta = origin.Y
						}
						binary.BigEndian.PutUint64(data[edge.Data.Offset:], math.Float64bits(edge.Value.(float64)+delta))
					}
				case "customEnvelopeWarp":
					for _, envelope := range entry.Value.(*psdfile.Descriptor).Entries {
						if envelope.Key != "meshPoints" {
							continue
						}
						for _, axis := range envelope.Value.(*psdfile.DescriptorObjectArray).Entries {
							array := axis.Value.(psdfile.DescriptorUnitArray)
							delta := origin.X
							if axis.Key == "Vrtc" {
								delta = origin.Y
							}
							for index, coordinate := range array.Numbers {
								binary.BigEndian.PutUint64(data[array.NumbersData.Offset+int64(index*8):],
									math.Float64bits(coordinate+delta))
							}
						}
					}
				}
			}
		}
	}
	return data
}

func TestSmartWarpMeshSurvivesSharedContentsReplacementAndSizeRestoration(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			name := strconv.Itoa(int(version)) + "/compression=" + strconv.Itoa(int(compression))
			t.Run(name, func(t *testing.T) {
				origin := psdmodel.Point{X: 10.25, Y: -4.75}
				editor, source := smartEditOpen(t, smartWarpReplacementOffsetFixture(t, version, origin))
				objects, err := editor.SmartObjects(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				view, err := source.ReadLayerMetadata(t.Context(), 0)
				if err != nil {
					t.Fatal(err)
				}
				original := slices.Clone(view.Layer.Placed.Warp.MeshPoints)
				expected := slices.Clone(original)
				expectedPeer := slices.Clone(original)
				expected[5].X += .25
				expected[10].Y -= .125
				if err := editor.SetSmartObjectWarp(t.Context(), objects[0].ID, expected, SmartWarpEditOptions{}); err != nil {
					t.Fatal(err)
				}
				previous := image.Pt(2, 2)
				previousOrigin := origin
				var final []byte
				for round, dimensions := range []image.Point{image.Pt(2, 2), image.Pt(3, 4), image.Pt(2, 2)} {
					pixels := smartDimensionsPixels(dimensions.X, dimensions.Y)
					pixels.Pix[0] += byte(round * 17)
					contents := smartSizedPrototype(version, dimensions, pixels.Rect, pixels)
					if err := editor.ReplaceContents(t.Context(), objects[0].ID, contents); err != nil {
						t.Fatalf("round %d contents update: %v", round, err)
					}
					if dimensions != previous {
						for index := range expected {
							expected[index].X = (expected[index].X - previousOrigin.X) *
								float64(dimensions.X) / float64(previous.X)
							expected[index].Y = (expected[index].Y - previousOrigin.Y) *
								float64(dimensions.Y) / float64(previous.Y)
							expectedPeer[index].X = (expectedPeer[index].X - previousOrigin.X) *
								float64(dimensions.X) / float64(previous.X)
							expectedPeer[index].Y = (expectedPeer[index].Y - previousOrigin.Y) *
								float64(dimensions.Y) / float64(previous.Y)
						}
						previousOrigin = psdmodel.Point{}
					}
					previous = dimensions
					node, _, _, err := editor.findNodeLocked(objects[0].ID)
					if err != nil || node.placed == nil || !slices.Equal(node.placed.Warp.MeshPoints, expected) {
						t.Fatalf("round %d contents update lost current Warp controls: %v", round, err)
					}
					var output bytes.Buffer
					if err := editor.Write(t.Context(), &output, SaveOptions{Compression: compression}); err != nil {
						t.Fatalf("round %d save after replacement: %v", round, err)
					}
					final = bytes.Clone(output.Bytes())
					saved, err := psdfile.Parse(final)
					if err != nil {
						t.Fatal(err)
					}
					for index := range 2 {
						metadata, err := saved.ReadLayerMetadata(t.Context(), index)
						if err != nil {
							t.Fatal(err)
						}
						wanted := expected
						if index == 1 {
							wanted = expectedPeer
						}
						if !slices.Equal(metadata.Layer.Placed.Warp.MeshPoints, wanted) ||
							metadata.Layer.Placed.Width != dimensions.X || metadata.Layer.Placed.Height != dimensions.Y {
							t.Fatalf("round %d instance %d saved unrelated or stale geometry", round, index)
						}
						if index == 0 && (metadata.Layer.Placed.Warp.Bounds.Left != previousOrigin.X ||
							metadata.Layer.Placed.Warp.Bounds.Top != previousOrigin.Y) {
							t.Fatalf("round %d saved Warp coordinate domain differs from current geometry", round)
						}
					}
					decoded, err := saved.Decode(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					physical := flattenSnapshot(decoded.Layers)
					for index := range 2 {
						live, err := psdscene.RenderPlacedLayer(t.Context(), saved, index)
						if err != nil || physical[index].Bounds != live.Bounds ||
							!bytes.Equal(physical[index].Pixels.Pix, live.Pixels.Pix) {
							t.Fatalf("round %d instance %d raw cache differs from current source and Warp: %v", round, index, err)
						}
					}
					if err := saved.Close(); err != nil {
						t.Fatal(err)
					}
				}
				// Restoring the original numeric mesh must not restore its old, independently retained source domain.
				expected = slices.Clone(original)
				if err := editor.SetSmartObjectWarp(t.Context(), objects[0].ID, expected, SmartWarpEditOptions{}); err != nil {
					t.Fatal(err)
				}
				pixels := smartDimensionsPixels(2, 2)
				if err := editor.ReplaceContents(t.Context(), objects[0].ID,
					smartSizedPrototype(version, image.Pt(2, 2), pixels.Rect, pixels)); err != nil {
					t.Fatal(err)
				}
				var restored bytes.Buffer
				if err := editor.Write(t.Context(), &restored, SaveOptions{Compression: compression}); err != nil {
					t.Fatal(err)
				}
				final = bytes.Clone(restored.Bytes())
				reopened, _ := smartEditOpen(t, final)
				objects, err = reopened.SmartObjects(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				retained, err := reopened.source.ReadLayerMetadata(t.Context(), 0)
				if err != nil || retained.Layer.Placed.Warp.Bounds.Left != 0 ||
					retained.Layer.Placed.Warp.Bounds.Top != 0 {
					t.Fatalf("equal numeric mesh reverted the current source coordinate domain: %v", err)
				}
				expected[6].Y += .125
				if err := reopened.SetSmartObjectWarp(t.Context(), objects[0].ID, expected, SmartWarpEditOptions{}); err != nil {
					t.Fatalf("continue mesh editing after replacement/save/reopen: %v", err)
				}
				var continued bytes.Buffer
				if err := reopened.Write(t.Context(), &continued, SaveOptions{Compression: compression}); err != nil {
					t.Fatal(err)
				}
				continuedSource, err := psdfile.Parse(continued.Bytes())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = continuedSource.Close() }()
				metadata, err := continuedSource.ReadLayerMetadata(t.Context(), 0)
				if err != nil || !slices.Equal(metadata.Layer.Placed.Warp.MeshPoints, expected) {
					t.Fatalf("continued mesh edit did not persist: %v", err)
				}
			})
		}
	}
}
