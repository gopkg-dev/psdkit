package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func exactSmartFilterFixture(t *testing.T, version uint16) []byte {
	t.Helper()
	payload := smartEditPlacement(t, 0, "")
	bridge, block, err := smartFilterEditSource(context.Background(), payload, "SoLd", psdfile.DefaultLimits(), 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close() }()
	descriptor, err := bridge.DecodeDescriptor(psdfile.Span{
		Offset: block.Data.Offset + 8, Length: block.Data.Length - 8,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.Entries = append(descriptor.Entries,
		psdfile.DescriptorEntry{Key: "filterFX", Type: "Objc", Value: filterEditStackDescriptor()})
	encoded, err := bridge.EncodeDescriptor(nil, descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	placement := append(bytes.Clone(payload[:8]), encoded...)
	placement = append(placement, payload[len(payload)-3:]...)
	pixels := exactSmartFilterGrayPixels([4]byte{0, 64, 128, 255})
	linked := writerLinkedEntry("liFD", "fixture-id", 7, smartEditPrototype(version, pixels))
	width := 4
	if version == 2 {
		width = 8
	}
	global := writerLinkedTag("8BIM", "lnk2", width, linked.data, 4)
	poisoned := exactSmartFilterGrayPixels([4]byte{17, 17, 17, 17})
	tags := writerLinkedTag("8BIM", "SoLd", 4, placement, 2)
	record, channels := smartEditLayer(version, "Exact smart-filter controls", 20, pixels.Rect, poisoned, tags, false)
	layers := binary.BigEndian.AppendUint16(nil, 1)
	layers = append(layers, record...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	return smartEditDocument(version, 2, 2, 3, layers, global, append([]byte{0, 0}, bytes.Repeat([]byte{23}, 12)...))
}

func exactSmartFilterGrayPixels(gray [4]byte) *image.NRGBA {
	pixels := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for index, value := range gray {
		pixels.SetNRGBA(index%2, index/2, color.NRGBA{R: value, G: value, B: value, A: 255})
	}
	return pixels
}

func exactSmartFilterStack(t *testing.T, source *psdfile.Document) psdfilter.Stack {
	t.Helper()
	placements, err := source.SmartObjects(nil)
	if err != nil || len(placements) != 1 {
		t.Fatalf("single smart-filter placement: %v", err)
	}
	view, err := psdfile.DecodeSmartFilters(nil, placements[0])
	if err != nil || len(view.Filters) != 5 {
		t.Fatalf("known filter stack: %v", err)
	}
	stack := psdfilter.Stack{Enabled: view.Enabled}
	for _, filter := range view.Filters {
		stack.Filters = append(stack.Filters, filter.Operation)
	}
	return stack
}

func TestSmartFilterIdentityAndLightnessEditSaveReopenWithoutApproximation(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			t.Run(fmt.Sprintf("v%d/c%d", version, compression), func(t *testing.T) {
				editor, source := smartEditOpen(t, exactSmartFilterFixture(t, version))
				stack := exactSmartFilterStack(t, source)
				stack.Filters[0].Radius = 0
				stack.Filters[3].Adjustment.BrightnessContrast.Brightness = 0
				stack.Filters[3].Adjustment.BrightnessContrast.Contrast = 0
				for _, step := range []struct {
					lightness int16
					gray      [4]byte
				}{
					{51, [4]byte{130, 161, 193, 255}},
					{0, [4]byte{0, 64, 128, 255}},
				} {
					stack.Filters[1].Radius, stack.Filters[1].Threshold = 0, 5
					stack.Filters[2].Radius, stack.Filters[2].Amount = 2, 0
					if step.lightness == 0 {
						stack.Filters[1].Radius, stack.Filters[1].Threshold = 3, 0
						stack.Filters[2].Radius, stack.Filters[2].Amount = 0, 1.5
					}
					stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = step.lightness
					pixels := exactSmartFilterGrayPixels(step.gray)
					if err := editor.SetSmartFilterParameters(nil, 1, stack, SmartFilterEditOptions{Pixels: pixels}); err != nil {
						t.Fatal(err)
					}
					preview, err := editor.Composite(nil)
					if err != nil || !bytes.Equal(preview.Pix, pixels.Pix) {
						t.Fatalf("edited accurate smart-filter cache was not composited: %v", err)
					}
					saved := compositeSave(t, editor, SaveOptions{Compression: compression})
					if saved.Header.Version != version {
						t.Fatal("automatic smart-filter save changed file format")
					}
					planes := append(bytes.Clone(step.gray[:]), step.gray[:]...)
					planes = append(planes, step.gray[:]...)
					compositeExpect(t, saved, planes)
					if actual := exactSmartFilterStack(t, saved); !reflect.DeepEqual(actual, stack) {
						t.Fatalf("reopened filter controls changed: %+v", actual)
					}
					settings := psdscene.Options{SmartFilters: &psdfilter.Options{}}
					live, err := psdscene.Render(nil, saved, settings)
					if err != nil || !bytes.Equal(live.Pix, pixels.Pix) {
						t.Fatalf("Scene did not rerender saved contents without approximation: %v, %v", err, live)
					}
					region := image.Rect(1, 0, 2, 2)
					tile, err := psdscene.RenderTile(nil, saved, region,
						psdscene.TileOptions{SmartFilters: &psdfilter.Options{}})
					if err != nil || tile.NRGBAAt(1, 0) != pixels.NRGBAAt(1, 0) ||
						tile.NRGBAAt(1, 1) != pixels.NRGBAAt(1, 1) {
						t.Fatalf("ROI filter rerender disagrees with full scene: %v", err)
					}
					filterEditAssertSpan(t, source, saved, source.AdditionalInfo[0].Span, saved.AdditionalInfo[0].Span)
					reopened, err := New(saved)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = reopened.Close() })
					editor = reopened
				}
			})
		}
	}
}

func TestSmartFilterIdentityEditFailuresStayAtomic(t *testing.T) {
	editor, source := smartEditOpen(t, exactSmartFilterFixture(t, 1))
	stack := exactSmartFilterStack(t, source)
	stack.Filters[0].Radius = 0
	pixels := exactSmartFilterGrayPixels([4]byte{0, 64, 128, 255})
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		want := psdfile.ErrMalformed
		stack.Filters[1].Threshold = -1
		if canceled {
			stack.Filters[1].Threshold = 0
			cancel()
			want = context.Canceled
		}
		before, _ := editor.Changes()
		err := editor.SetSmartFilterParameters(ctx, 1, stack, SmartFilterEditOptions{Pixels: pixels})
		cancel()
		if !errors.Is(err, want) {
			t.Fatalf("identity parameter failure category differs: %v", err)
		}
		after, _ := editor.Changes()
		node, _, _, _ := editor.findNodeLocked(1)
		if before != after || node.pixels != nil || len(node.additional) != 0 {
			t.Fatal("failed identity edit committed a partial parameter/cache overlay")
		}
	}
}
