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
	"github.com/gopkg-dev/psdkit/psdgradient"
)

func gradientEditBase() psdgradient.Fill {
	return psdgradient.Fill{Name: "Original gradient", Kind: psdgradient.Linear, Scale: 100,
		Smoothness: 4096, Align: true, Interpolation: "classic", Form: "custom-stops",
		ColorStops: []psdgradient.ColorStop{
			{Midpoint: 50, RGB: [3]float64{.1, .2, .3}},
			{Location: 4096, Midpoint: 50, RGB: [3]float64{.8, .7, .6}},
		},
		OpacityStops: []psdgradient.OpacityStop{{Midpoint: 50, Opacity: 1}, {Location: 4096, Midpoint: 50, Opacity: 1}},
	}
}

func gradientEditDescriptor() *psdfile.Descriptor {
	return &psdfile.Descriptor{Version: 16, ClassID: "null", Entries: []psdfile.DescriptorEntry{
		{Key: "Type", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "GrdT", ValueID: "Lnr "}},
		{Key: "Grad", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "Grdn", Entries: []psdfile.DescriptorEntry{
			{Key: "GrdF", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "GrdF", ValueID: "CstS"}},
			{Key: "Nm  ", Type: "TEXT", Value: "Original gradient"},
			{Key: "Intr", Type: "doub", Value: float64(4096)},
		}}},
	}}
}

func gradientEditSource(t *testing.T, version uint16, extra []byte, options ...psdfile.LoadOptions) *Editor {
	t.Helper()
	descriptor := gradientEditDescriptor()
	if err := patchGradientDescriptor(context.Background(), descriptor, gradientEditBase()); err != nil {
		t.Fatal(err)
	}
	return gradientEditDescriptorSource(t, version, descriptor, extra, options...)
}

func gradientEditDescriptorSource(
	t *testing.T, version uint16, descriptor *psdfile.Descriptor, extra []byte, options ...psdfile.LoadOptions,
) *Editor {
	t.Helper()
	payload, err := psdfile.EncodeDescriptor(context.Background(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, 0, 0)
	tags := compositeFixtureTag("GdFl", payload)
	tags = append(tags, extra...)
	pixels := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for index := 3; index < len(pixels.Pix); index += 4 {
		pixels.Pix[index] = 255
	}
	record, channels := smartEditLayer(version, "Gradient", 20, pixels.Rect, pixels, tags, false)
	layers := binary.BigEndian.AppendUint16(nil, 1)
	layers = append(layers, record...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	data := smartEditDocument(version, 4, 4, 3, layers, nil, append([]byte{0, 0}, make([]byte, 48)...))
	return compositeEditor(t, data, options...)
}

func TestGradientDescriptorPreservesUnknownStopFieldsAndRejectsSourceUnsupported(t *testing.T) {
	descriptor := gradientEditDescriptor()
	if err := patchGradientDescriptor(context.Background(), descriptor, gradientEditBase()); err != nil {
		t.Fatal(err)
	}
	gradient := gradientObject(descriptor, "Grad")
	colors := gradientFind(gradient, "Clrs").Value.([]psdfile.DescriptorValue)
	stop := colors[0].Value.(*psdfile.Descriptor)
	stop.Entries = append(stop.Entries, psdfile.DescriptorEntry{Key: "futureMetadata", Type: "bool", Value: true})
	keys := descriptorKeys(stop)
	fill := gradientEditBase()
	fill.ColorStops = append(fill.ColorStops, psdgradient.ColorStop{Location: 3072, Midpoint: 50, RGB: [3]float64{1, 0, 0}})
	if err := patchGradientDescriptor(context.Background(), descriptor, fill); err != nil ||
		!reflect.DeepEqual(descriptorKeys(stop), keys) || stop.Entries[len(stop.Entries)-1].Value != true {
		t.Fatalf("known-list reconstruction discarded an original stop's unknown metadata: %v", err)
	}
	editor := gradientEditDescriptorSource(t, 1, descriptor, nil)
	if err := editor.SetGradientFill(nil, 1, fill); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("source Unsupported was silently eliminated by editable parameters: %v", err)
	}
	state, _ := editor.Changes()
	node, _, _, _ := editor.findNodeLocked(1)
	if state.Dirty || node.gradient != nil || len(node.additional) != 0 {
		t.Fatal("unsupported source was partially replaced")
	}
}

func gradientEditRead(t *testing.T, source *psdfile.Document) *psdfile.GradientFill {
	t.Helper()
	for _, block := range source.Layers[0].AdditionalInfo {
		if block.Key == "GdFl" {
			view, err := source.DecodeGradientFill(nil, block)
			if err != nil {
				t.Fatal(err)
			}
			return view
		}
	}
	t.Fatal("gradient descriptor missing")
	return nil
}

func TestSetGradientFillOwnsParametersAndSavesPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		editor := gradientEditSource(t, version, compositeFixtureTag("pRsv", []byte{0x99, 0x77}))
		original := gradientEditRead(t, editor.source)
		originalBytes, _ := editor.source.ReadSpan(original.Block.Data)
		before, _ := editor.Changes()
		if err := editor.SetGradientFill(nil, 1, *original.Fill); err != nil {
			t.Fatal(err)
		}
		if state, _ := editor.Changes(); state != before {
			t.Fatal("identical source parameters changed revision")
		}
		fill := *original.Fill
		fill.Angle = 90
		fill.ColorStops[0].RGB = [3]float64{.9, .1, .2}
		if err := editor.SetGradientFill(nil, 1, fill); err != nil {
			t.Fatal(err)
		}
		node, _, _, _ := editor.findNodeLocked(1)
		expected := *node.gradient
		expected.ColorStops = append([]psdgradient.ColorStop(nil), expected.ColorStops...)
		fill.ColorStops[0].RGB = [3]float64{0, 1, 0}
		if node.gradient.ColorStops[0].RGB != expected.ColorStops[0].RGB {
			t.Fatal("semantic gradient retained caller-owned stop slices")
		}
		owned := editor.ownedBytesLocked(nil)
		if err := editor.SetGradientFill(nil, 1, expected); err != nil || editor.ownedBytesLocked(nil) != owned {
			t.Fatalf("repeat no-op accumulated retained memory: %v", err)
		}
		live, err := editor.Composite(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		saved := compositeSave(t, editor)
		view := gradientEditRead(t, saved)
		if !reflect.DeepEqual(*view.Fill, expected) || view.Tail.Length != original.Tail.Length ||
			saved.Header.Version != version {
			t.Fatal("saved parameters, original descriptor tail or version differs")
		}
		if !reflect.DeepEqual(descriptorKeys(view.Descriptor), descriptorKeys(original.Descriptor)) &&
			len(view.Descriptor.Entries) != len(original.Descriptor.Entries)+1 {
			t.Fatal("unmodified descriptor entry order changed")
		}
		for _, block := range saved.Layers[0].AdditionalInfo {
			if block.Key == "pRsv" {
				data, _ := saved.ReadSpan(block.Data)
				if !bytes.Equal(data, []byte{0x99, 0x77}) {
					t.Fatal("unrelated source block changed")
				}
			}
		}
		unchanged, _ := editor.source.ReadSpan(original.Block.Data)
		if !bytes.Equal(unchanged, originalBytes) {
			t.Fatal("source descriptor bytes changed")
		}
		reopened, err := New(saved)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		preview, err := reopened.Composite(context.Background())
		if err != nil || !bytes.Equal(preview.Pix, live.Pix) {
			t.Fatalf("live saved gradient pixels differ: %v", err)
		}
	}
}

func descriptorKeys(descriptor *psdfile.Descriptor) []string {
	var keys []string
	for _, entry := range descriptor.Entries {
		keys = append(keys, entry.Key)
	}
	return keys
}

func TestSetGradientFillAddsStopsAndRetainsOtherSemanticCosts(t *testing.T) {
	editor := gradientEditSource(t, 1, nil)
	fill := *gradientEditRead(t, editor.source).Fill
	fill.ColorStops = append(fill.ColorStops[:1:1], psdgradient.ColorStop{
		Location: 2048, Midpoint: 50, RGB: [3]float64{.5, .1, .8}}, fill.ColorStops[1])
	node, _, _, _ := editor.findNodeLocked(1)
	node.setSemanticCost("other", 73)
	if err := editor.SetGradientFill(nil, 1, fill); err != nil {
		t.Fatal(err)
	}
	saved := compositeSave(t, editor)
	view := gradientEditRead(t, saved)
	if len(view.Fill.ColorStops) != 3 || view.Fill.ColorStops[1].Location != 2048 ||
		node.semanticCosts["other"] != 73 || node.semanticBytes != node.semanticCosts["gradient"]+73 {
		t.Fatal("new stops or another feature's owned budget lost")
	}
}

func TestSetGradientFillInvalidAmbiguousCanceledAndPeakFailuresAreTransactional(t *testing.T) {
	editor := gradientEditSource(t, 1, nil)
	fill := gradientEditBase()
	invalid := fill
	invalid.Scale = -1
	if err := editor.SetGradientFill(nil, 1, invalid); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("invalid scale accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.SetGradientFill(ctx, 1, fill); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled edit accepted: %v", err)
	}
	for _, extra := range [][]byte{compositeFixtureTag("GdFl", nil), compositeFixtureTag("SoCo", nil),
		compositeFixtureTag("thrs", []byte{0, 128})} {
		candidate := gradientEditSource(t, 1, extra)
		if err := candidate.SetGradientFill(nil, 1, fill); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("ambiguous layer family accepted: %v", err)
		}
	}
	limits := psdfile.DefaultLimits()
	limits.MaxDecodeBytes = 32 << 10
	limited := gradientEditSource(t, 1, nil, psdfile.LoadOptions{Limits: limits})
	fill.Angle = 90
	if err := limited.SetGradientFill(nil, 1, fill); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("descriptor peak budget ignored: %v", err)
	}
	for _, candidate := range []*Editor{editor, limited} {
		state, _ := candidate.Changes()
		node, _, _, _ := candidate.findNodeLocked(1)
		if state.Dirty || len(node.additional) != 0 || node.gradient != nil {
			t.Fatal("rejected edit committed partial overlays")
		}
	}
}

func TestSetGradientFillRetainsOldOverlayAtPeakBudgetFailure(t *testing.T) {
	limits := psdfile.DefaultLimits()
	limits.MaxDecodeBytes = 64 << 10
	editor := gradientEditSource(t, 1, nil, psdfile.LoadOptions{Limits: limits})
	fill := *gradientEditRead(t, editor.source).Fill
	fill.Angle = 90
	if err := editor.SetGradientFill(nil, 1, fill); err != nil {
		t.Fatal(err)
	}
	node, _, _, _ := editor.findNodeLocked(1)
	old := node.gradient
	before, _ := editor.Changes()
	node.setSemanticCost("other", limits.MaxDecodeBytes-editor.ownedBytesLocked(nil)-1024)
	owned := editor.ownedBytesLocked(nil)
	fill.Angle = 0
	if err := editor.SetGradientFill(nil, 1, fill); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("old overlay and other feature memory was excluded from the peak: %v", err)
	}
	after, _ := editor.Changes()
	if before != after || node.gradient != old || editor.ownedBytesLocked(nil) != owned {
		t.Fatal("failed replacement changed the prior successful overlay or another feature")
	}
}

func TestSetGradientFillCancellationDuringSourceReadIsTransactional(t *testing.T) {
	fixture := gradientEditSource(t, 1, nil)
	data, err := fixture.source.ReadSpan(fixture.source.Source)
	if err != nil {
		t.Fatal(err)
	}
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
	view := gradientEditRead(t, source)
	fill := *view.Fill
	fill.Angle = 90
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader.trigger, reader.cancel = view.Block.Data, cancel
	if err := editor.SetGradientFill(ctx, 1, fill); !errors.Is(err, context.Canceled) {
		t.Fatalf("source-read cancellation was lost: %v", err)
	}
	state, _ := editor.Changes()
	node, _, _, _ := editor.findNodeLocked(1)
	if state.Dirty || node.gradient != nil || len(node.additional) != 0 {
		t.Fatal("canceled source decode left partial overlays")
	}
}
