package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func smartFilterAutoSourceFixture(t *testing.T, version uint16, external, peer bool) ([]byte, []byte) {
	t.Helper()
	template, err := psdfile.Parse(exactSmartFilterFixture(t, version))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = template.Close() }()
	var placement []byte
	for _, block := range template.Layers[0].AdditionalInfo {
		if block.Key == "SoLd" {
			placement, err = template.ReadSpan(block.Data)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(placement) == 0 {
		t.Fatal("automatic source fixture has no original placement descriptor")
	}
	artwork := exactSmartFilterGrayPixels([4]byte{0, 64, 128, 255})
	contents := smartEditPrototype(version, artwork)
	kind := "liFD"
	if external {
		kind = "liFE"
	}
	linked := writerLinkedEntry(kind, "fixture-id", 7, contents)
	lengthWidth := 4
	if version == 2 {
		lengthWidth = 8
	}
	global := writerLinkedTag("8BIM", "lnk2", lengthWidth, linked.data, 4)
	tags := writerLinkedTag("8BIM", "SoLd", 4, placement, 2)
	records, channels := smartEditLayer(version, "automatic-target", 20, artwork.Rect,
		exactSmartFilterGrayPixels([4]byte{17, 17, 17, 17}), tags, false)
	count, width := 1, 2
	if peer {
		otherTags := writerLinkedTag("8BIM", "SoLE", 4, smartEditPlacement(t, 4, ""), 2)
		otherRecord, otherPixels := smartEditLayer(version, "unfiltered-peer", 21,
			image.Rect(4, 0, 6, 2), artwork, otherTags, false)
		records, channels = append(records, otherRecord...), append(channels, otherPixels...)
		count, width = 2, 6
	}
	layers := binary.BigEndian.AppendUint16(nil, uint16(count))
	layers = append(layers, records...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	return smartEditDocument(version, width, 2, 3, layers, global,
		append([]byte{0, 0}, bytes.Repeat([]byte{23}, width*2*3)...)), contents
}

func TestSmartFilterAutoSharedPeerAndOriginalContentsRemainUnchanged(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		data, _ := smartFilterAutoSourceFixture(t, version, false, true)
		editor := compositeEditor(t, data)
		stack := smartFilterAutoExactStack(t, editor.source)
		stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
		original := bytes.Clone(data)
		if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); err != nil {
			t.Fatal(err)
		}
		expected := image.NewNRGBA(image.Rect(0, 0, 6, 2))
		left, right := exactSmartFilterGrayPixels([4]byte{130, 161, 193, 255}),
			exactSmartFilterGrayPixels([4]byte{0, 64, 128, 255})
		for y := range 2 {
			for x := range 2 {
				expected.SetNRGBA(x, y, left.NRGBAAt(x, y))
				expected.SetNRGBA(x+4, y, right.NRGBAAt(x, y))
			}
		}
		for compression := range uint16(4) {
			_, saved := smartFilterAutoSaved(t, editor, compression, expected, psdfilter.Options{}, compression%2 != 0)
			filterEditAssertPreserved(t, editor.source, saved)
			if !bytes.Equal(data, original) {
				t.Fatal("per-instance filter replacement changed original shared contents")
			}
		}
	}
}

func TestSmartFilterAutoExternalResolverErrorCancellationAndStaleState(t *testing.T) {
	for _, scenario := range []string{"success", "missing", "error", "cancel", "stale", "closed"} {
		t.Run(scenario, func(t *testing.T) {
			data, contents := smartFilterAutoSourceFixture(t, 1, true, false)
			if scenario == "success" {
				// The explicit source differs from the retained external cache, proving resolver precedence.
				contents = smartEditPrototype(1, exactSmartFilterGrayPixels([4]byte{255, 128, 64, 0}))
			}
			editor := compositeEditor(t, data)
			stack := smartFilterAutoExactStack(t, editor.source)
			stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("explicit linked source failed")
			calls := 0
			options := SmartFilterEditOptions{}
			if scenario != "missing" {
				options.Render = &psdscene.Options{ExternalResolver: func(
					callbackContext context.Context, _ psdfile.LinkedFileRecord,
				) (*psdfile.Document, error) {
					calls++
					if _, err := editor.Layers(); err != nil {
						return nil, err
					}
					switch scenario {
					case "error":
						return nil, failure
					case "cancel":
						cancel()
					case "stale":
						if err := editor.SetOpacity(1, 254); err != nil {
							return nil, err
						}
					case "closed":
						if err := editor.source.Close(); err != nil {
							return nil, err
						}
					}
					if err := callbackContext.Err(); err != nil {
						return nil, err
					}
					return psdfile.Parse(contents)
				}}
			}
			before, _ := editor.Changes()
			err := editor.SetSmartFilterParameters(ctx, 1, stack, options)
			if scenario == "success" {
				if err != nil || calls != 1 {
					t.Fatalf("explicit external source did not render outside the editor lock: %v", err)
				}
				expected := exactSmartFilterGrayPixels([4]byte{255, 193, 161, 130})
				pixels, err := editor.Composite(t.Context())
				if err != nil || !bytes.Equal(pixels.Pix, expected.Pix) {
					t.Fatalf("automatic external filter used retained linked or layer cache: %v", err)
				}
				saved := compositeSave(t, editor, SaveOptions{Compression: 3})
				links, err := saved.LinkedFiles(t.Context())
				if err != nil || len(links) != 1 || links[0].Kind != "liFE" {
					t.Fatalf("automatic filter save changed external link ownership: %v", err)
				}
				liveOptions := *options.Render
				liveOptions.SmartFilters = &psdfilter.Options{}
				live, err := psdscene.Render(t.Context(), saved, liveOptions)
				if err != nil || !bytes.Equal(live.Pix, expected.Pix) {
					t.Fatalf("saved external filtered layer did not rerender via explicit source: %v", err)
				}
				tile, err := psdscene.RenderTile(t.Context(), saved, image.Rect(1, 0, 2, 2),
					psdscene.TileOptions{ExternalResolver: liveOptions.ExternalResolver, SmartFilters: liveOptions.SmartFilters})
				if err != nil || tile.NRGBAAt(1, 0) != expected.NRGBAAt(1, 0) ||
					tile.NRGBAAt(1, 1) != expected.NRGBAAt(1, 1) {
					t.Fatalf("reopened external ROI disagrees with resolved original artwork: %v", err)
				}
				reopened, err := New(saved)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = reopened.Close() })
				neutral := smartFilterAutoExactStack(t, saved)
				if err := reopened.SetSmartFilterParameters(t.Context(), 1, neutral, options); err != nil {
					t.Fatalf("reopened external filtered instance could not continue editing: %v", err)
				}
				again := compositeSave(t, reopened, SaveOptions{Compression: 1})
				plain, err := psdscene.Render(t.Context(), again, liveOptions)
				if err != nil || !bytes.Equal(plain.Pix, exactSmartFilterGrayPixels([4]byte{255, 128, 64, 0}).Pix) {
					t.Fatalf("external continuation did not restore unfiltered explicit source: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("failed external source resolution committed automatic filter output")
			}
			if scenario == "error" && !errors.Is(err, failure) ||
				scenario == "cancel" && !errors.Is(err, context.Canceled) ||
				scenario == "closed" && !errors.Is(err, psdfile.ErrClosed) ||
				scenario == "stale" && !errors.Is(err, ErrStaleLinkedEdit) {
				t.Fatalf("explicit external failure cause was lost: %v", err)
			}
			if scenario == "missing" && calls != 0 {
				t.Fatal("missing external resolver unexpectedly ran a callback")
			}
			if scenario == "stale" {
				if editor.revision != before.Revision+1 || editor.roots[0].opacity != 254 {
					t.Fatal("stale auto edit overwrote the concurrent caller change")
				}
			} else if scenario != "closed" {
				smartFilterAutoAssertStateUnchanged(t, editor, before)
			}
			node := editor.roots[0]
			if node.pixels != nil || node.placed != nil || len(node.additional) != 0 {
				t.Fatal("failed callback committed an automatic raster or filter descriptor")
			}
		})
	}
}

func TestSmartFilterAutoCurrentLinkedReplacementIsTheLiveSource(t *testing.T) {
	editor := compositeEditor(t, exactSmartFilterFixture(t, 1))
	stack := smartFilterAutoExactStack(t, editor.source)
	stack.Enabled = false
	if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); err != nil {
		t.Fatal(err)
	}
	updated := exactSmartFilterGrayPixels([4]byte{255, 128, 64, 0})
	view, err := editor.source.ReadLinkedFileView(t.Context())
	if err != nil || len(view.Records) != 1 {
		t.Fatalf("current-source fixture has no unique linked record: %v", err)
	}
	// Establish an owned current-content overlay directly; ReplaceContents still rejects filtered source layers.
	// This isolates the automatic filter's overlay precedence without expanding that separate editing API.
	editor.mu.Lock()
	editor.linkedRecords, editor.linkedRecordsBytes = view.Records, view.MetadataBytes
	editor.linkedOverlays = map[string]*linkedReplacement{view.Records[0].ID: {
		data: bytes.Clone(smartEditPrototype(1, updated)), fileType: "8BPS",
	}}
	editor.markChangedLocked(false)
	editor.mu.Unlock()
	stack.Enabled = true
	stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
	if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); err != nil {
		t.Fatalf("automatic filtering ignored or rejected current linked-source overlay: %v", err)
	}
	expected := exactSmartFilterGrayPixels([4]byte{255, 193, 161, 130})
	_, saved := smartFilterAutoSaved(t, editor, 3, expected, psdfilter.Options{}, true)
	if actual := exactSmartFilterStack(t, saved); !reflect.DeepEqual(actual, stack) {
		t.Fatal("filter metadata changed while rebasing onto current linked contents")
	}
}

func TestSmartFilterAutoUsesCurrentPlacementDescriptorOverlay(t *testing.T) {
	data, _ := smartFilterAutoSourceFixture(t, 1, false, true)
	editor := compositeEditor(t, data)
	placements, err := editor.source.SmartObjects(t.Context())
	if err != nil || len(placements) != 2 {
		t.Fatalf("placement-overlay fixture is incomplete: %v", err)
	}
	placement := placements[0]
	values := []float64{1, 0, 3, 0, 3, 2, 1, 2}
	for _, entry := range placement.Descriptor.Entries {
		if entry.Key == "Trnf" || entry.Key == "nonAffineTransform" {
			for index := range entry.Value.([]psdfile.DescriptorValue) {
				entry.Value.([]psdfile.DescriptorValue)[index].Value = values[index]
			}
		}
	}
	encoded, err := editor.source.EncodeDescriptor(t.Context(), placement.Descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	original, err := editor.source.ReadSpan(placement.Block.Data)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(bytes.Clone(original[:8]), encoded...)
	payload = append(payload, original[len(original)-3:]...)
	placed, cost, err := smartFilterEditedPlacement(t.Context(), payload, placement.Block.Key,
		editor.source.Limits(), 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	// Establish a current encoded placement without expanding SetSmartObjectTransform's filtered-layer scope.
	editor.mu.Lock()
	node, _, _, _ := editor.findNodeLocked(1)
	node.additional = map[int64][]byte{placement.Block.Span.Offset: payload}
	node.placed = placed
	node.setSemanticCost("placed", cost)
	editor.markChangedLocked(false)
	editor.mu.Unlock()
	stack := smartFilterAutoExactStack(t, editor.source)
	stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
	if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); err != nil {
		t.Fatalf("automatic filters could not use current placement metadata: %v", err)
	}
	expected := image.NewNRGBA(image.Rect(0, 0, 6, 2))
	left, right := exactSmartFilterGrayPixels([4]byte{130, 161, 193, 255}),
		exactSmartFilterGrayPixels([4]byte{0, 64, 128, 255})
	for y := range 2 {
		for x := range 2 {
			expected.SetNRGBA(x+1, y, left.NRGBAAt(x, y))
			expected.SetNRGBA(x+4, y, right.NRGBAAt(x, y))
		}
	}
	_, saved := smartFilterAutoSaved(t, editor, 3, expected, psdfilter.Options{}, true)
	current, err := saved.SmartObjects(t.Context())
	if err != nil || !reflect.DeepEqual(current[0].Transform, values) || current[1].Transform[0] != 4 {
		t.Fatalf("automatic filter save reverted placement overlay or changed a shared peer: %v", err)
	}
}
