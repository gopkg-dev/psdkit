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
	"github.com/gopkg-dev/psdkit/psdpattern"
)

func patternEditFixture(id, name string, colors [3][4]byte) []byte {
	var rectangle []byte
	for _, coordinate := range []uint32{0, 0, 2, 2} {
		rectangle = binary.BigEndian.AppendUint32(rectangle, coordinate)
	}
	vm := append(bytes.Clone(rectangle), 0, 0, 0, 3)
	for _, channel := range colors {
		array := binary.BigEndian.AppendUint32(nil, 8)
		array = append(array, rectangle...)
		array = append(array, 0, 8, 0)
		array = append(array, channel[:]...)
		vm = binary.BigEndian.AppendUint32(vm, 1)
		vm = binary.BigEndian.AppendUint32(vm, uint32(len(array)))
		vm = append(vm, array...)
	}
	vm = append(vm, make([]byte, 8)...)
	body := []byte{0, 0, 0, 1, 0, 0, 0, 3, 0, 2, 0, 2}
	body = append(body, writerLinkedUnicode(name)...)
	body = append(body, byte(len(id)))
	body = append(body, id...)
	body = append(body, 0, 0, 0, 3)
	body = binary.BigEndian.AppendUint32(body, uint32(len(vm)))
	body = append(body, vm...)
	result := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	result = append(result, body...)
	return append(result, make([]byte, (4-len(body)%4)%4)...)
}

func patternEditDescriptor() *psdfile.Descriptor {
	return &psdfile.Descriptor{Version: 16, ClassID: "null", Entries: []psdfile.DescriptorEntry{
		{Key: "Ptrn", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "Ptrn", Entries: []psdfile.DescriptorEntry{
			{Key: "Nm  ", Type: "TEXT", Value: "Original pattern"},
			{Key: "Idnt", Type: "TEXT", Value: "first"},
		}}},
	}}
}

func patternEditSource(
	t *testing.T, version uint16, descriptor *psdfile.Descriptor, extra, patterns []byte,
	options ...psdfile.LoadOptions,
) *Editor {
	t.Helper()
	payload, err := psdfile.EncodeDescriptor(context.Background(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, 0, 0)
	tags := append(compositeFixtureTag("PtFl", payload), extra...)
	pixels := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for index := 3; index < len(pixels.Pix); index += 4 {
		pixels.Pix[index] = 255
	}
	record, channels := smartEditLayer(version, "Pattern", 21, pixels.Rect, pixels, tags, false)
	layers := binary.BigEndian.AppendUint16(nil, 1)
	layers = append(layers, record...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	if patterns == nil {
		patterns = patternEditFixture("first", "Original pattern", [3][4]byte{{10, 20, 30, 40}, {50, 60, 70, 80}, {90, 100, 110, 120}})
		patterns = append(patterns, patternEditFixture("second", "Second pattern",
			[3][4]byte{{200, 210, 220, 230}, {40, 30, 20, 10}, {70, 80, 90, 100}})...)
	}
	global := writerLinkedTag("8BIM", "Patt", 4, patterns, 4)
	data := smartEditDocument(version, 4, 4, 3, layers, global, append([]byte{0, 0}, make([]byte, 48)...))
	return compositeEditor(t, data, options...)
}

func patternEditRead(t *testing.T, source *psdfile.Document) *psdfile.PatternFill {
	t.Helper()
	for _, block := range source.Layers[0].AdditionalInfo {
		if block.Key == "PtFl" {
			view, err := source.DecodePatternFill(nil, block)
			if err != nil {
				t.Fatal(err)
			}
			return view
		}
	}
	t.Fatal("pattern descriptor missing")
	return nil
}

func TestSetPatternFillPhaseLocalSwitchAndSavePSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		editor := patternEditSource(t, version, patternEditDescriptor(), nil, nil)
		original := patternEditRead(t, editor.source)
		before, _ := editor.Changes()
		if err := editor.SetPatternFill(nil, 1, *original.Fill); err != nil {
			t.Fatal(err)
		}
		if after, _ := editor.Changes(); before != after {
			t.Fatal("equal original fill changed revision")
		}
		fill := *original.Fill
		fill.Phase = psdpattern.Phase{X: -1, Y: 1}
		if err := editor.SetPatternFill(nil, 1, fill); err != nil {
			t.Fatal(err)
		}
		node, _, _, _ := editor.findNodeLocked(1)
		tile := node.patternTile
		fill.Phase = psdpattern.Phase{X: 1, Y: -1}
		if err := editor.SetPatternFill(nil, 1, fill); err != nil || node.patternTile != tile {
			t.Fatalf("same local pattern unnecessarily replaced its owned tile: %v", err)
		}
		fill.ID, fill.Name = "second", "Second pattern"
		node.setSemanticCost("other", 71)
		if err := editor.SetPatternFill(nil, 1, fill); err != nil {
			t.Fatal(err)
		}
		if node.patternTile == tile || node.patternTile.NRGBAAt(1, 1).R != 230 ||
			node.semanticBytes != node.semanticCosts["pattern"]+71 {
			t.Fatal("local reference switch or independent feature cost was lost")
		}
		owned := editor.ownedBytesLocked(nil)
		if err := editor.SetPatternFill(nil, 1, *node.pattern); err != nil || editor.ownedBytesLocked(nil) != owned {
			t.Fatalf("repeat fill accumulated memory: %v", err)
		}
		live, err := editor.Composite(nil)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := psdpattern.Render(nil, node.patternTile, *node.pattern, image.Rect(0, 0, 4, 4))
		if err != nil || !bytes.Equal(live.Pix, expected.Pix) {
			t.Fatalf("editor overlay did not update live pattern samples: %v", err)
		}
		saved := compositeSave(t, editor)
		view := patternEditRead(t, saved)
		if !reflect.DeepEqual(*view.Fill, *node.pattern) || view.Block.Data.Length%4 != 0 ||
			saved.Header.Version != version {
			t.Fatal("saved descriptor semantics, internal alignment or PSD/PSB version changed")
		}
		oldPatterns, _ := editor.source.ReadSpan(editor.source.AdditionalInfo[0].Span)
		newPatterns, _ := saved.ReadSpan(saved.AdditionalInfo[0].Span)
		if !bytes.Equal(oldPatterns, newPatterns) {
			t.Fatal("phase or reference edit changed original encoded pattern blocks")
		}
		reopened, err := New(saved)
		if err != nil {
			t.Fatal(err)
		}
		preview, err := reopened.Composite(nil)
		if err != nil || !bytes.Equal(live.Pix, preview.Pix) {
			t.Fatalf("saved pattern live pixels differ after reopen: %v", err)
		}
		_ = reopened.Close()
	}
}

func TestSetPatternFillRejectsUnknownDuplicateMissingAndInvalid(t *testing.T) {
	fill := psdpattern.Fill{ID: "first", Name: "Original pattern", Scale: 100, Link: true,
		Phase: psdpattern.Phase{X: 1}}
	editor := patternEditSource(t, 1, patternEditDescriptor(), nil, nil)
	for _, change := range []func(*psdpattern.Fill){
		func(value *psdpattern.Fill) { value.ID = "external" },
		func(value *psdpattern.Fill) { value.Scale = 75 },
		func(value *psdpattern.Fill) { value.Angle = 90 },
		func(value *psdpattern.Fill) { value.Phase.X = .5 },
		func(value *psdpattern.Fill) { value.Unsupported = []string{"future"} },
	} {
		candidate := fill
		change(&candidate)
		if err := editor.SetPatternFill(nil, 1, candidate); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported pattern edit accepted: %v", err)
		}
	}
	descriptor := patternEditDescriptor()
	descriptor.Entries = append(descriptor.Entries,
		psdfile.DescriptorEntry{Key: "future", Type: "bool", Value: true})
	patchPatternDescriptor(descriptor, fill)
	if descriptor.Entries[1].Key != "future" || descriptor.Entries[1].Value != true {
		t.Fatal("source-aware patch discarded unknown ordered metadata")
	}
	unknown := patternEditSource(t, 1, descriptor, nil, nil)
	if err := unknown.SetPatternFill(nil, 1, fill); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown source semantics silently replaced: %v", err)
	}
	duplicatePattern := patternEditFixture("first", "Original pattern", [3][4]byte{})
	duplicatePattern = append(duplicatePattern, duplicatePattern...)
	duplicate := patternEditSource(t, 1, patternEditDescriptor(), nil, duplicatePattern)
	if err := duplicate.SetPatternFill(nil, 1, fill); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ambiguous local ID accepted: %v", err)
	}
	for _, extra := range [][]byte{compositeFixtureTag("PtFl", []byte{0}), compositeFixtureTag("SoCo", []byte{0})} {
		ambiguous := patternEditSource(t, 1, patternEditDescriptor(), extra, nil)
		if err := ambiguous.SetPatternFill(nil, 1, fill); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("ambiguous live family accepted: %v", err)
		}
	}
	for _, candidate := range []*Editor{editor, unknown, duplicate} {
		state, _ := candidate.Changes()
		node, _, _, _ := candidate.findNodeLocked(1)
		if state.Dirty || node.pattern != nil || node.patternTile != nil || len(node.additional) != 0 {
			t.Fatal("rejected pattern edit committed partial state")
		}
	}
}

func TestSetPatternFillContextAndPeakBudgetAreTransactional(t *testing.T) {
	limits := psdfile.DefaultLimits()
	limits.MaxDecodeBytes = 512 << 10
	editor := patternEditSource(t, 1, patternEditDescriptor(), nil, nil, psdfile.LoadOptions{Limits: limits})
	fill := *patternEditRead(t, editor.source).Fill
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.SetPatternFill(ctx, 1, fill); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled pattern edit accepted: %v", err)
	}
	fill.Phase.X = 1
	if err := editor.SetPatternFill(nil, 1, fill); err != nil {
		t.Fatal(err)
	}
	node, _, _, _ := editor.findNodeLocked(1)
	oldFill, oldTile := node.pattern, node.patternTile
	before, _ := editor.Changes()
	node.setSemanticCost("other", limits.MaxDecodeBytes-editor.ownedBytesLocked(nil)-1024)
	owned := editor.ownedBytesLocked(nil)
	fill.Phase.X = 0
	if err := editor.SetPatternFill(nil, 1, fill); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("prior overlays escaped replacement peak budget: %v", err)
	}
	after, _ := editor.Changes()
	if before != after || node.pattern != oldFill || node.patternTile != oldTile ||
		editor.ownedBytesLocked(nil) != owned {
		t.Fatal("failed pattern replacement lost previous owned data or revision")
	}
}

func TestSetPatternFillPreservesFloat32UnitsAndRejectsRoundedPhase(t *testing.T) {
	descriptor := patternEditDescriptor()
	descriptor.Entries = append(descriptor.Entries, psdfile.DescriptorEntry{
		Key: "phase", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "Pnt ", Entries: []psdfile.DescriptorEntry{
			{Key: "Hrzn", Type: "UnFl", Value: psdfile.DescriptorUnit{Unit: "#Pxl", Width: 32, Number: float32(0)}},
			{Key: "Vrtc", Type: "doub", Value: float64(0)},
		}},
	})
	editor := patternEditSource(t, 1, descriptor, nil, nil)
	fill := *patternEditRead(t, editor.source).Fill
	fill.Phase.X = 1
	if err := editor.SetPatternFill(nil, 1, fill); err != nil {
		t.Fatal(err)
	}
	saved := compositeSave(t, editor)
	phase := gradientObject(patternEditRead(t, saved).Descriptor, "phase")
	if entry := gradientFind(phase, "Hrzn"); entry.Type != "UnFl" ||
		entry.Value.(psdfile.DescriptorUnit).Width != 32 {
		t.Fatal("phase editing replaced the original unit encoding")
	}
	node, _, _, _ := editor.findNodeLocked(1)
	old := node.pattern
	before, _ := editor.Changes()
	fill.Phase.X = (1 << 24) + 1
	if err := editor.SetPatternFill(nil, 1, fill); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("desired integer silently rounded by the source encoding: %v", err)
	}
	if after, _ := editor.Changes(); after != before || node.pattern != old {
		t.Fatal("unrepresentable source phase committed an approximate edit")
	}
}

func TestSetPatternFillCancellationDuringSourceRead(t *testing.T) {
	fixture := patternEditSource(t, 1, patternEditDescriptor(), nil, nil)
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
	view := patternEditRead(t, source)
	fill := *view.Fill
	fill.Phase.X = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader.trigger, reader.cancel = view.Block.Data, cancel
	if err := editor.SetPatternFill(ctx, 1, fill); !errors.Is(err, context.Canceled) {
		t.Fatalf("source-read cancellation lost its category: %v", err)
	}
	state, _ := editor.Changes()
	node, _, _, _ := editor.findNodeLocked(1)
	if state.Dirty || node.pattern != nil || node.patternTile != nil || len(node.additional) != 0 {
		t.Fatal("source cancellation left a partial owned pattern overlay")
	}
}
