package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func smartContentsFilterSharedFixture(t *testing.T, version uint16, styled bool) []byte {
	t.Helper()
	source, err := psdfile.Parse(exactSmartFilterFixture(t, version))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	placements, err := source.SmartObjects(t.Context())
	if err != nil || len(placements) != 1 {
		t.Fatalf("shared filter fixture placement is absent: %v", err)
	}
	placement := placements[0]
	original, err := source.ReadSpan(placement.Block.Data)
	if err != nil {
		t.Fatal(err)
	}
	firstTags := writerLinkedTag("8BIM", "SoLd", 4, original, 2)
	if styled {
		vector, err := psdvector.EncodeMask(t.Context(), colorOverlayVectorMask(t, 1.0/6))
		if err != nil {
			t.Fatal(err)
		}
		firstTags = append(firstTags, writerLinkedTag("8BIM", "vmsk", 4, vector, 2)...)
	}
	values := []float64{4, 0, 6, 0, 6, 2, 4, 2}
	for _, entry := range placement.Descriptor.Entries {
		if entry.Key != "Trnf" && entry.Key != "nonAffineTransform" {
			continue
		}
		for index := range entry.Value.([]psdfile.DescriptorValue) {
			entry.Value.([]psdfile.DescriptorValue)[index].Value = values[index]
		}
	}
	encoded, err := source.EncodeDescriptor(t.Context(), placement.Descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	second := append(bytes.Clone(original[:8]), encoded...)
	second = append(second, original[len(original)-3:]...)
	secondTags := writerLinkedTag("8BIM", "SoLE", 4, second, 2)
	poisoned := exactSmartFilterGrayPixels([4]byte{17, 17, 17, 17})
	firstRecord, firstPixels := smartEditLayer(version, "filtered-vector-peer", 20,
		image.Rect(0, 0, 2, 2), poisoned, firstTags, false)
	secondRecord, secondPixels := smartEditLayer(version, "filtered-pixel-peer", 21,
		image.Rect(4, 0, 6, 2), poisoned, secondTags, styled)
	layers := binary.BigEndian.AppendUint16(nil, 0xfffe)
	layers = append(layers, firstRecord...)
	layers = append(layers, secondRecord...)
	layers = append(layers, firstPixels...)
	layers = append(layers, secondPixels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	global, err := source.ReadSpan(source.AdditionalInfo[0].Span)
	if err != nil {
		t.Fatal(err)
	}
	return smartEditDocument(version, 6, 2, 4, layers, global,
		append([]byte{0, 0}, bytes.Repeat([]byte{23}, 6*2*4)...))
}

func smartContentsFilterSharedControls(t *testing.T, editor *Editor) (psdfilter.Stack, psdfilter.Stack) {
	t.Helper()
	first := smartFilterAutoExactStack(t, editor.source)
	first.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
	if err := editor.SetSmartFilterParameters(t.Context(), 1, first, SmartFilterEditOptions{}); err != nil {
		t.Fatal(err)
	}
	second := smartFilterAutoExactStack(t, editor.source)
	second.Filters[4].Adjustment.HueSaturation.Master.Lightness = -97
	if err := editor.SetSmartFilterParameters(t.Context(), 2, second, SmartFilterEditOptions{}); err != nil {
		t.Fatal(err)
	}
	return first, second
}

func TestSmartContentsFilterSharedInstancesUseOwnCurrentControlsAndUnbakedMasks(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			t.Run(fmt.Sprintf("v%d/c%d", version, compression), func(t *testing.T) {
				editor := compositeEditor(t, smartContentsFilterSharedFixture(t, version, true))
				first, second := smartContentsFilterSharedControls(t, editor)
				contents := smartEditPrototype(version, exactSmartFilterGrayPixels([4]byte{255, 128, 64, 0}))
				if err := editor.ReplaceContents(t.Context(), 2, contents); err != nil {
					t.Fatalf("shared replacement failed with independently retained instance styling: %v", err)
				}
				for index, node := range editor.roots {
					for x := range 2 {
						for y := range 2 {
							if node.pixels.NRGBAAt(x+index*4, y).A != 255 {
								t.Fatal("parent vector or pixel mask was baked into replacement artwork")
							}
						}
					}
				}
				expected := image.NewNRGBA(image.Rect(0, 0, 6, 2))
				expected.SetNRGBA(0, 0, color.NRGBA{255, 255, 255, 255})
				expected.SetNRGBA(0, 1, color.NRGBA{161, 161, 161, 255})
				expected.SetNRGBA(4, 0, color.NRGBA{8, 8, 8, 255})
				expected.SetNRGBA(5, 0, color.NRGBA{4, 4, 4, 128})
				expected.SetNRGBA(5, 1, color.NRGBA{0, 0, 0, 255})
				reopened, saved := smartFilterAutoSaved(t, editor, compression, expected, psdfilter.Options{}, true)
				smartContentsFilterAssertSource(t, saved, contents)
				placements, err := saved.SmartObjects(t.Context())
				if err != nil || len(placements) != 2 || placements[0].Transform[0] != 0 ||
					placements[1].Transform[0] != 4 {
					t.Fatalf("replacement merged independent placement transforms: %v", err)
				}
				for index, want := range []psdfilter.Stack{first, second} {
					view, err := psdfile.DecodeSmartFilters(t.Context(), placements[index])
					if err != nil || view.Filters[4].Operation.Adjustment.HueSaturation.Master.Lightness !=
						want.Filters[4].Adjustment.HueSaturation.Master.Lightness {
						t.Fatalf("replacement merged independent instance filter controls: %v", err)
					}
				}
				if err := reopened.ReplaceContents(t.Context(), 1, contents); err != nil {
					t.Fatalf("reopened masked shared instances could not continue replacement: %v", err)
				}
				smartFilterAutoSaved(t, reopened, compression, expected, psdfilter.Options{}, false)
			})
		}
	}
}

func TestSmartContentsFilterReplacementRetainsOriginalFilterMask(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			t.Run(fmt.Sprintf("v%d/c%d", version, compression), func(t *testing.T) {
				editor := compositeEditor(t, smartFilterAutoMaskFixture(t, version))
				stack := smartFilterAutoExactStack(t, editor.source)
				stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
				if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); err != nil {
					t.Fatal(err)
				}
				contents := smartEditPrototype(version, exactSmartFilterGrayPixels([4]byte{255, 128, 64, 0}))
				if err := editor.ReplaceContents(t.Context(), 1, contents); err != nil {
					t.Fatalf("replacement lost original FXid filter-mask identity: %v", err)
				}
				// Exact lightness followed by original independently known 0/255/128/64 mask weights.
				expected := exactSmartFilterGrayPixels([4]byte{255, 193, 113, 33})
				reopened, saved := smartFilterAutoSaved(t, editor, compression, expected, psdfilter.Options{}, false)
				smartContentsFilterAssertSource(t, saved, contents)
				filterEditAssertSpan(t, editor.source, saved,
					editor.source.AdditionalInfo[1].Span, saved.AdditionalInfo[1].Span)
				stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 0
				if err := reopened.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); err != nil {
					t.Fatalf("reopened source lost editable FXid mask metadata: %v", err)
				}
				smartFilterAutoSaved(t, reopened, compression, exactSmartFilterGrayPixels([4]byte{255, 128, 64, 0}),
					psdfilter.Options{}, true)
			})
		}
	}
}

func TestSmartContentsFilterSharedFailureCancellationAndBudgetAreAtomic(t *testing.T) {
	editor := compositeEditor(t, smartContentsFilterSharedFixture(t, 1, false))
	_, second := smartContentsFilterSharedControls(t, editor)
	contents := smartEditPrototype(1, exactSmartFilterGrayPixels([4]byte{255, 128, 64, 0}))
	if err := editor.ReplaceContents(t.Context(), 1, contents); err != nil {
		t.Fatal(err)
	}
	second.Filters[0].Radius = .7
	if err := editor.SetSmartFilterParameters(t.Context(), 2, second, SmartFilterEditOptions{
		Pixels: exactSmartFilterGrayPixels([4]byte{1, 2, 3, 4}),
	}); err != nil {
		t.Fatal(err)
	}
	unchanged := smartContentsFilterState(t, editor)
	if err := editor.ReplaceContents(t.Context(), 1,
		smartEditPrototype(2, exactSmartFilterGrayPixels([4]byte{0, 64, 128, 255}))); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported second peer did not reject the entire shared source transaction: %v", err)
	}
	unchanged()
	if err := editor.ReplaceContentsWithOptions(t.Context(), 1, contents, SmartContentsOptions{
		SmartFilters: &psdfilter.Options{ApproximateGaussian: true, MaxWorkingBytes: 1},
	}); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("per-filter working budget was not enforced transactionally: %v", err)
	}
	unchanged()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := editor.ReplaceContentsWithOptions(ctx, 1, contents, SmartContentsOptions{
		SmartFilters: &psdfilter.Options{ApproximateGaussian: true},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled shared filter transaction returned %v", err)
	}
	unchanged()
	base, cancelDuring := context.WithCancel(t.Context())
	defer cancelDuring()
	during := &smartEditSamplingContext{Context: base, cancel: cancelDuring, cancelAt: 40}
	if err := editor.ReplaceContentsWithOptions(during, 1, contents, SmartContentsOptions{
		SmartFilters: &psdfilter.Options{ApproximateGaussian: true},
	}); !errors.Is(err, context.Canceled) || during.calls < during.cancelAt {
		t.Fatalf("replacement did not honor mid-transaction cancellation: calls=%d, %v", during.calls, err)
	}
	unchanged()
	limited, _ := smartEditOpen(t, exactSmartFilterFixture(t, 1), psdfile.LoadOptions{
		Limits: psdfile.Limits{MaxDecodeBytes: int64(len(contents)) - 1},
	})
	limitedUnchanged := smartContentsFilterState(t, limited)
	if err := limited.ReplaceContents(t.Context(), 1, contents); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("filtered replacement escaped the configured working/owned budget: %v", err)
	}
	limitedUnchanged()
}

func TestSmartContentsFilterInactiveStacksAndUnfilteredPeersKeepOriginalArtwork(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, inactive := range []string{"stack-disabled", "filters-disabled", "zero-opacity"} {
				t.Run(fmt.Sprintf("v%d/c%d/%s", version, compression, inactive), func(t *testing.T) {
					data, _ := smartFilterAutoSourceFixture(t, version, false, true)
					editor := compositeEditor(t, data)
					stack := exactSmartFilterStackForLayer(t, editor.source, 0)
					switch inactive {
					case "stack-disabled":
						stack.Enabled = false
					case "filters-disabled":
						for index := range stack.Filters {
							stack.Filters[index].Enabled = false
						}
					case "zero-opacity":
						for index := range stack.Filters {
							stack.Filters[index].Opacity = 0
						}
					}
					if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); err != nil {
						t.Fatal(err)
					}
					contents := smartEditPrototype(version, exactSmartFilterGrayPixels([4]byte{255, 128, 64, 0}))
					if err := editor.ReplaceContents(t.Context(), 2, contents); err != nil {
						t.Fatalf("inactive controls or unfiltered peer blocked shared replacement: %v", err)
					}
					expected := image.NewNRGBA(image.Rect(0, 0, 6, 2))
					artwork := exactSmartFilterGrayPixels([4]byte{255, 128, 64, 0})
					for y := range 2 {
						for x := range 2 {
							expected.SetNRGBA(x, y, artwork.NRGBAAt(x, y))
							expected.SetNRGBA(x+4, y, artwork.NRGBAAt(x, y))
						}
					}
					reopened, saved := smartFilterAutoSaved(t, editor, compression, expected, psdfilter.Options{}, true)
					if !reflect.DeepEqual(exactSmartFilterStackForLayer(t, saved, 0), stack) {
						t.Fatal("inactive non-neutral control values were reset while replacing contents")
					}
					if err := reopened.ReplaceContents(t.Context(), 1, contents); err != nil {
						t.Fatalf("reopened inactive shared source could not continue editing: %v", err)
					}
					smartFilterAutoSaved(t, reopened, compression, expected, psdfilter.Options{}, false)
				})
			}
		}
	}
}

func exactSmartFilterStackForLayer(t *testing.T, source *psdfile.Document, index int) psdfilter.Stack {
	t.Helper()
	placements, err := source.SmartObjects(t.Context())
	if err != nil || index >= len(placements) {
		t.Fatalf("requested instance filter controls are absent: %v", err)
	}
	view, err := psdfile.DecodeSmartFilters(t.Context(), placements[index])
	if err != nil || view == nil {
		t.Fatalf("requested instance has no retained filter stack: %v", err)
	}
	stack := psdfilter.Stack{Enabled: view.Enabled}
	for _, filter := range view.Filters {
		stack.Filters = append(stack.Filters, filter.Operation)
	}
	return stack
}

func TestSmartContentsFilterDimensionChangeKeepsControlsAndDestinationCorners(t *testing.T) {
	editor := compositeEditor(t, exactSmartFilterFixture(t, 2))
	stack := smartFilterAutoExactStack(t, editor.source)
	stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
	if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); err != nil {
		t.Fatal(err)
	}
	pixels := image.NewNRGBA(image.Rect(0, 0, 4, 3))
	for offset := 0; offset < len(pixels.Pix); offset += 4 {
		copy(pixels.Pix[offset:], []byte{100, 100, 100, 255})
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, pixels); err != nil {
		t.Fatal(err)
	}
	if err := editor.ReplaceContents(t.Context(), 1, encoded.Bytes()); err != nil {
		t.Fatalf("filtered dimension change rejected supported replacement geometry: %v", err)
	}
	expected := exactSmartFilterGrayPixels([4]byte{179, 179, 179, 179})
	reopened, saved := smartFilterAutoSaved(t, editor, 3, expected, psdfilter.Options{}, true)
	smartContentsFilterAssertSource(t, saved, encoded.Bytes())
	placements, err := saved.SmartObjects(t.Context())
	if err != nil || placements[0].Width != 4 || placements[0].Height != 3 ||
		!reflect.DeepEqual(placements[0].Transform, []float64{0, 0, 2, 0, 2, 2, 0, 2}) {
		t.Fatalf("dimension update lost the current filter controls or fixed destination geometry: %v", err)
	}
	if err := reopened.ReplaceContents(t.Context(), 1,
		smartEditPrototype(1, exactSmartFilterGrayPixels([4]byte{0, 64, 128, 255}))); err != nil {
		t.Fatalf("reopened changed-size source could not return to another editable content format: %v", err)
	}
	smartFilterAutoSaved(t, reopened, 3, exactSmartFilterGrayPixels([4]byte{130, 161, 193, 255}),
		psdfilter.Options{}, false)
}
