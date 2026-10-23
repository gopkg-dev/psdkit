package psdedit

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func smartFilterAutoExactStack(t *testing.T, source *psdfile.Document) psdfilter.Stack {
	t.Helper()
	placements, err := source.SmartObjects(t.Context())
	if err != nil || len(placements) == 0 {
		t.Fatalf("automatic filter fixture has no placement: %v", err)
	}
	view, err := psdfile.DecodeSmartFilters(t.Context(), placements[0])
	if err != nil || view == nil || len(view.Filters) != 5 {
		t.Fatalf("automatic filter fixture has no known five-filter stack: %v", err)
	}
	stack := psdfilter.Stack{Enabled: view.Enabled}
	for _, item := range view.Filters {
		stack.Filters = append(stack.Filters, item.Operation)
	}
	stack.Filters[0].Radius = 0
	stack.Filters[1].Radius, stack.Filters[1].Threshold = 0, 5
	stack.Filters[2].Radius, stack.Filters[2].Amount = 2, 0
	stack.Filters[3].Adjustment.BrightnessContrast.Brightness = 0
	stack.Filters[3].Adjustment.BrightnessContrast.Contrast = 0
	stack.Filters[4].Adjustment.HueSaturation.Master = psdadjust.HueSaturationValues{}
	return stack
}

func smartFilterAutoAssertStateUnchanged(t *testing.T, editor *Editor, before Changes) {
	t.Helper()
	if after, _ := editor.Changes(); after != before {
		t.Fatalf("failed automatic filter edit changed revision: %+v != %+v", after, before)
	}
	node, _, _, _ := editor.findNodeLocked(1)
	if node.pixels != nil || node.placed != nil || len(node.additional) != 0 {
		t.Fatal("failed automatic filter edit committed a partial metadata or raster overlay")
	}
}

func smartFilterAutoSaved(
	t *testing.T, editor *Editor, compression uint16, expected *image.NRGBA,
	filterOptions psdfilter.Options, disk bool,
) (*Editor, *psdfile.Document) {
	t.Helper()
	current, err := editor.Composite(t.Context())
	if err != nil || current.Rect != expected.Rect || !bytes.Equal(current.Pix, expected.Pix) {
		t.Fatalf("automatic filter artwork differs from the selected source: %v, got %v, want %v", err, current, expected)
	}
	var saved *psdfile.Document
	if disk {
		extension := map[uint16]string{1: ".psd", 2: ".psb"}[editor.source.Header.Version]
		path := filepath.Join(t.TempDir(), "automatic-smart-filter"+extension)
		if err := editor.Save(t.Context(), path, SaveOptions{Compression: compression}); err != nil {
			t.Fatal(err)
		}
		saved, err = psdfile.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = saved.Close() })
	} else {
		saved = compositeSave(t, editor, SaveOptions{Compression: compression})
	}
	if saved.Header.Version != editor.source.Header.Version {
		t.Fatal("automatic smart-filter save changed PSD/PSB version")
	}
	settings := psdscene.Options{SmartFilters: &filterOptions}
	live, err := psdscene.Render(t.Context(), saved, settings)
	if err != nil || live.Rect != expected.Rect || !bytes.Equal(live.Pix, expected.Pix) {
		t.Fatalf("saved filter did not rerender from source metadata: %v, %v", err, live)
	}
	region := image.Rect(expected.Rect.Min.X+1, expected.Rect.Min.Y, expected.Rect.Max.X, expected.Rect.Max.Y)
	tile, err := psdscene.RenderTile(t.Context(), saved, region,
		psdscene.TileOptions{SmartFilters: &filterOptions})
	if err != nil || tile.Rect != region {
		t.Fatalf("saved smart-filter ROI failed: %v", err)
	}
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			if tile.NRGBAAt(x, y) != expected.NRGBAAt(x, y) {
				t.Fatal("saved smart-filter ROI differs from full live rendering")
			}
		}
	}
	var planes []byte
	for component := range int(saved.Header.Channels) {
		for offset := 0; offset < len(expected.Pix); offset += 4 {
			alpha := int(expected.Pix[offset+3])
			value := alpha
			if component < 3 {
				value = (int(expected.Pix[offset+component])*alpha + 255*(255-alpha) + 127) / 255
			}
			planes = append(planes, byte(value))
		}
	}
	compositeExpect(t, saved, planes)
	reopened, err := New(saved)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	return reopened, saved
}

func smartFilterAutoOpaqueMetadata(t *testing.T, source *psdfile.Document) [][]byte {
	t.Helper()
	placements, err := source.SmartObjects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var payloads [][]byte
	var visit func(*psdfile.Descriptor)
	visit = func(descriptor *psdfile.Descriptor) {
		for _, entry := range descriptor.Entries {
			if entry.Key == "opaqueItem" || entry.Key == "opaqueStack" || entry.Key == "opaqueMetadata" {
				data, err := source.ReadSpan(entry.Span)
				if err != nil {
					t.Fatal(err)
				}
				payloads = append(payloads, data)
			}
			if child, ok := entry.Value.(*psdfile.Descriptor); ok {
				visit(child)
			}
			if children, ok := entry.Value.([]psdfile.DescriptorValue); ok {
				for _, child := range children {
					if nested, ok := child.Value.(*psdfile.Descriptor); ok {
						visit(nested)
					}
				}
			}
		}
	}
	visit(placements[0].Descriptor)
	if len(payloads) != 7 {
		t.Fatalf("opaque smart-filter fixture is incomplete: %d payloads", len(payloads))
	}
	return payloads
}

func TestSmartFilterAutoExactEditSaveReopenWithoutPixelsPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			t.Run(fmt.Sprintf("v%d/c%d", version, compression), func(t *testing.T) {
				data := exactSmartFilterFixture(t, version)
				original := bytes.Clone(data)
				editor, source, reader := compositeLayerOnlyEditor(t, data)
				metadata := smartFilterAutoOpaqueMetadata(t, source)
				reader.blockFooter = true
				reader.failure, reader.failureCause = source.Layers[0].Channels[0].Data, errCompositeLayerOnlyPixels
				stack := smartFilterAutoExactStack(t, source)
				stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
				if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); err != nil {
					t.Fatalf("exact nil-Pixels edit did not render original linked artwork: %v", err)
				}
				node, _, _, _ := editor.findNodeLocked(1)
				if node.pixels == nil || bytes.Equal(node.pixels.Pix, bytes.Repeat([]byte{17, 17, 17, 255}, 4)) {
					t.Fatal("automatic filtering used the poisoned saved layer cache")
				}
				before, _ := editor.Changes()
				if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); err != nil {
					t.Fatal(err)
				}
				if after, _ := editor.Changes(); after != before {
					t.Fatal("identical source-live filter edit accumulated a revision")
				}
				// The setter must ignore old layer samples; the general editor snapshot may copy them before overlaying.
				reader.failureCause = nil
				stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = -97
				expected := exactSmartFilterGrayPixels([4]byte{130, 161, 193, 255})
				reopened, saved := smartFilterAutoSaved(t, editor, compression, expected, psdfilter.Options{}, false)
				if exactSmartFilterStack(t, saved).Filters[4].Adjustment.HueSaturation.Master.Lightness != 51 ||
					!reflect.DeepEqual(metadata, smartFilterAutoOpaqueMetadata(t, saved)) {
					t.Fatal("automatic edit borrowed controls or discarded unknown descriptor fields")
				}
				filterEditAssertSpan(t, source, saved, source.AdditionalInfo[0].Span, saved.AdditionalInfo[0].Span)
				if reader.footerReads != 0 || !bytes.Equal(data, original) {
					t.Fatal("source-live filtering read saved preview or mutated original source bytes")
				}
				neutral := smartFilterAutoExactStack(t, saved)
				if err := reopened.SetSmartFilterParameters(t.Context(), 1, neutral, SmartFilterEditOptions{}); err != nil {
					t.Fatalf("reopened source did not support continued automatic filtering: %v", err)
				}
				smartFilterAutoSaved(t, reopened, compression, exactSmartFilterGrayPixels([4]byte{0, 64, 128, 255}),
					psdfilter.Options{}, true)
			})
		}
	}
}

func smartFilterAutoColorFixture(t *testing.T, version uint16) ([]byte, *image.NRGBA) {
	t.Helper()
	pixels := smartEditImage([4]color.NRGBA{{30, 80, 130, 255}, {70, 120, 170, 255},
		{110, 160, 210, 255}, {150, 200, 250, 255}})
	data := exactSmartFilterFixture(t, version)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	links, err := source.LinkedFiles(t.Context())
	if err != nil || len(links) != 1 {
		t.Fatalf("color fixture has no unique embedded contents: %v", err)
	}
	child := smartEditPrototype(version, pixels)
	if int64(len(child)) != links[0].Data.Length {
		t.Fatal("independent color source does not fit its retained content span")
	}
	copy(data[links[0].Data.Offset:], child)
	return data, pixels
}

func TestSmartFilterAutoApproximationPoliciesAreExplicitAcrossSaveReopen(t *testing.T) {
	cases := []struct {
		name   string
		edit   func(*psdfilter.Stack)
		policy psdfilter.Options
	}{
		{"gaussian", func(stack *psdfilter.Stack) { stack.Filters[0].Radius = .7 },
			psdfilter.Options{ApproximateGaussian: true}},
		{"surface", func(stack *psdfilter.Stack) { stack.Filters[1].Radius, stack.Filters[1].Threshold = 2, 100 },
			psdfilter.Options{ApproximateSurfaceBlur: true}},
		{"sharpen", func(stack *psdfilter.Stack) { stack.Filters[2].Radius, stack.Filters[2].Amount = .7, 1.2 },
			psdfilter.Options{ApproximateSmartSharpen: true}},
		{"brightness", func(stack *psdfilter.Stack) { stack.Filters[3].Adjustment.BrightnessContrast.Brightness = 15 },
			psdfilter.Options{ApproximateBrightnessContrast: true}},
		{"hue", func(stack *psdfilter.Stack) { stack.Filters[4].Adjustment.HueSaturation.Master.Hue = 37 },
			psdfilter.Options{ApproximateHueSaturation: true}},
	}
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, test := range cases {
				t.Run(fmt.Sprintf("v%d/c%d/%s", version, compression, test.name), func(t *testing.T) {
					data, artwork := smartFilterAutoColorFixture(t, version)
					editor := compositeEditor(t, data)
					stack := smartFilterAutoExactStack(t, editor.source)
					test.edit(&stack)
					before, _ := editor.Changes()
					if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); !errors.Is(err, ErrUnsupported) {
						t.Fatalf("non-exact %s silently enabled approximation: %v", test.name, err)
					}
					smartFilterAutoAssertStateUnchanged(t, editor, before)
					expected, err := psdfilter.Apply(t.Context(), artwork, stack, test.policy)
					if err != nil || bytes.Equal(expected.Pix, artwork.Pix) {
						t.Fatalf("selected approximation did not exercise a nonidentity filter: %v", err)
					}
					options := SmartFilterEditOptions{Render: &psdscene.Options{SmartFilters: &test.policy}}
					if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, options); err != nil {
						t.Fatalf("explicit source-live %s policy failed: %v", test.name, err)
					}
					reopened, saved := smartFilterAutoSaved(t, editor, compression, expected, test.policy, false)
					if actual := exactSmartFilterStack(t, saved); !reflect.DeepEqual(actual, stack) {
						t.Fatalf("saved %s controls differ: %+v", test.name, actual)
					}
					neutral := smartFilterAutoExactStack(t, saved)
					if err := reopened.SetSmartFilterParameters(t.Context(), 1, neutral, SmartFilterEditOptions{}); err != nil {
						t.Fatalf("reopened approximate %s could not return to exact controls: %v", test.name, err)
					}
					smartFilterAutoSaved(t, reopened, compression, artwork, psdfilter.Options{}, true)
				})
			}
		}
	}
}
