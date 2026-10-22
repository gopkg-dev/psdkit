package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func adjustmentEditFixture(version uint16, tags []byte) []byte {
	return compositeFixture(version, 8, 3, []compositeFixtureLayer{
		{color: []byte{150, 150, 150, 255}}, {tags: tags},
	})
}

func adjustmentEditLayer(t *testing.T, editor *Editor) LayerID {
	t.Helper()
	layers, err := editor.Layers()
	if err != nil {
		t.Fatal(err)
	}
	for _, layer := range layers {
		if layer.SourceIndex == 1 {
			return layer.ID
		}
	}
	t.Fatal("adjustment layer missing")
	return 0
}

func adjustmentEditPreview(t *testing.T, editor *Editor, value byte) {
	t.Helper()
	pixels, err := editor.Composite(context.Background())
	if err != nil || pixels == nil || !bytes.Equal(pixels.Pix, []byte{value, value, value, 255}) {
		t.Fatalf("live adjustment pixels differ from expected %d: %v, pixels=%v", value, err, pixels)
	}
}

func TestSetAdjustmentChangesAllMatchingBlocksAndSavesPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		threshold := compositeFixtureTag("thrs", []byte{0, 200, 0, 0})
		tags := append(bytes.Clone(threshold), compositeFixtureTag("zzZZ", []byte{0x99, 0x77})...)
		tags = append(tags, threshold...)
		data := adjustmentEditFixture(version, tags)
		original := bytes.Clone(data)
		editor := compositeEditor(t, data)
		id := adjustmentEditLayer(t, editor)
		if err := editor.SetAdjustment(nil, id, psdadjust.Adjustment{Kind: psdadjust.Threshold, Threshold: 200}); err != nil {
			t.Fatal(err)
		}
		before, _ := editor.Changes()
		if before.Dirty {
			t.Fatal("unchanged adjustment dirtied the editor")
		}
		if err := editor.SetAdjustment(nil, id, psdadjust.Adjustment{Kind: psdadjust.Threshold, Threshold: 100}); err != nil {
			t.Fatal(err)
		}
		adjustmentEditPreview(t, editor, 255)
		changes, _ := editor.Changes()
		if changes.Revision != 1 || !changes.Dirty || !changes.CompositeChanged || changes.StructureChanged {
			t.Fatalf("visual revision differs: %+v", changes)
		}
		saved := compositeSave(t, editor)
		count := 0
		for _, block := range saved.Layers[1].AdditionalInfo {
			encoded, err := saved.ReadSpan(block.Data)
			if err != nil {
				t.Fatal(err)
			}
			switch block.Key {
			case "thrs":
				count++
				if !bytes.Equal(encoded, []byte{0, 100, 0, 0}) {
					t.Fatal("duplicate adjustment parameters or original padding changed")
				}
			case "zzZZ":
				if !bytes.Equal(encoded, []byte{0x99, 0x77}) {
					t.Fatal("unrelated original block changed")
				}
			}
		}
		if count != 2 || saved.Header.Version != version || !bytes.Equal(data, original) {
			t.Fatal("source bytes, PSD/PSB version or duplicate block order changed")
		}
		decoded, err := saved.Decode(context.Background())
		if err != nil || decoded.Layers[1].Adjustment == nil || decoded.Layers[1].Adjustment.Threshold != 100 {
			t.Fatalf("saved live parameters differ: %v", err)
		}
		compositeExpect(t, saved, []byte{255, 255, 255})
	}
}

func TestSetAdjustmentOwnsCurveParametersAndKeepsOtherSemanticCosts(t *testing.T) {
	curve := []byte{0, 0, 1, 0, 0, 0, 1, 0, 2, 0, 0, 0, 0, 0, 255, 0, 255}
	editor := compositeEditor(t, adjustmentEditFixture(1, compositeFixtureTag("curv", curve)))
	id := adjustmentEditLayer(t, editor)
	node, _, _, err := editor.findNodeLocked(id)
	if err != nil {
		t.Fatal(err)
	}
	node.setSemanticCost("other", 37)
	points := []psdadjust.CurvePoint{{Input: 0, Output: 0}, {Input: 150, Output: 220}, {Input: 255, Output: 255}}
	desired := psdadjust.Adjustment{Kind: psdadjust.Curves,
		Curves: []psdadjust.CurveChannel{{Channel: 0, Points: points}}}
	if err := editor.SetAdjustment(nil, id, desired); err != nil {
		t.Fatal(err)
	}
	clear(points)
	desired.Curves[0].Channel = 3
	adjustmentEditPreview(t, editor, 220)
	if node.semanticCosts["other"] != 37 || node.semanticBytes != node.semanticCosts["adjustment"]+37 {
		t.Fatal("adjustment budget replaced another semantic feature")
	}
	owned := editor.ownedBytesLocked(nil)
	current := *node.adjustment
	if err := editor.SetAdjustment(nil, id, current); err != nil {
		t.Fatal(err)
	}
	changes, _ := editor.Changes()
	if changes.Revision != 1 || editor.ownedBytesLocked(nil) != owned {
		t.Fatal("unchanged edited parameters advanced revision or accumulated owned bytes")
	}
	for _, output := range []uint16{210, 220, 210, 220} {
		current.Curves = []psdadjust.CurveChannel{{Channel: 0, Points: []psdadjust.CurvePoint{
			{Input: 0, Output: 0}, {Input: 150, Output: output}, {Input: 255, Output: 255},
		}}}
		if err := editor.SetAdjustment(nil, id, current); err != nil {
			t.Fatal(err)
		}
		if editor.ownedBytesLocked(nil) != owned {
			t.Fatal("repeated same-size edits accumulated previous overlays")
		}
	}
}

func TestSetAdjustmentInvalidAndCanceledEditsAreTransactional(t *testing.T) {
	threshold := compositeFixtureTag("thrs", []byte{0, 128})
	editor := compositeEditor(t, adjustmentEditFixture(1, threshold))
	id := adjustmentEditLayer(t, editor)
	before, _ := editor.Changes()
	for _, adjustment := range []psdadjust.Adjustment{
		{Kind: psdadjust.Threshold, Threshold: 256}, {Kind: psdadjust.Posterize, Posterize: 4},
	} {
		err := editor.SetAdjustment(nil, id, adjustment)
		if !errors.Is(err, ErrInvalidEdit) && !errors.Is(err, ErrUnsupported) {
			t.Fatalf("invalid parameters or family change accepted: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.SetAdjustment(ctx, id,
		psdadjust.Adjustment{Kind: psdadjust.Threshold, Threshold: 100}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled edit accepted: %v", err)
	}
	after, _ := editor.Changes()
	node, _, _, _ := editor.findNodeLocked(id)
	if before != after || len(node.additional) != 0 || node.adjustment != nil || node.semanticBytes != 0 {
		t.Fatal("rejected edit left partial state")
	}
	for _, tags := range [][]byte{
		append(bytes.Clone(threshold), compositeFixtureTag("CgEd", nil)...),
		append(bytes.Clone(threshold), compositeFixtureTag("hue2", []byte{0, 3})...),
		append(bytes.Clone(threshold), compositeFixtureTag("thrs", []byte{0, 128, 0x99})...),
		append(bytes.Clone(threshold), compositeFixtureTag("post", []byte{0, 4})...),
	} {
		candidate := compositeEditor(t, adjustmentEditFixture(1, tags))
		candidateID := adjustmentEditLayer(t, candidate)
		if err := candidate.SetAdjustment(nil, candidateID,
			psdadjust.Adjustment{Kind: psdadjust.Threshold, Threshold: 100}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unknown or ambiguous source semantics accepted: %v", err)
		}
		node, _, _, _ := candidate.findNodeLocked(candidateID)
		state, _ := candidate.Changes()
		if state.Dirty || len(node.additional) != 0 || node.adjustment != nil {
			t.Fatal("failure after first block left partial overlays")
		}
	}
}

func TestSetAdjustmentRepeatedEditsChargeOldAndNewMetadataPeak(t *testing.T) {
	curve := binary.BigEndian.AppendUint32([]byte{1, 0, 1}, 15)
	var identity []byte
	for value := range 256 {
		identity = append(identity, byte(value))
	}
	for range 4 {
		curve = append(curve, identity...)
	}
	curve = append(curve, "Crv "...)
	curve = binary.BigEndian.AppendUint16(curve, 4)
	curve = binary.BigEndian.AppendUint32(curve, 4)
	for channel := range 4 {
		curve = binary.BigEndian.AppendUint16(curve, uint16(channel))
		curve = append(curve, identity...)
	}
	limits := psdfile.DefaultLimits()
	limits.MaxDecodeBytes = 36 << 10
	editor := compositeEditor(t, adjustmentEditFixture(1, compositeFixtureTag("curv", curve)),
		psdfile.LoadOptions{Limits: limits})
	id := adjustmentEditLayer(t, editor)
	lut := bytes.Clone(identity)
	for index := range lut {
		lut[index] = 255 - lut[index]
	}
	adjustment := psdadjust.Adjustment{Kind: psdadjust.Curves,
		Curves: []psdadjust.CurveChannel{{Channel: 0, LUT: lut}}}
	if err := editor.SetAdjustment(nil, id, adjustment); err != nil {
		t.Fatalf("first edit should fit before an old overlay exists: %v", err)
	}
	before, _ := editor.Changes()
	node, _, _, _ := editor.findNodeLocked(id)
	owned, oldSemantic := editor.ownedBytesLocked(nil), node.adjustment
	adjustment.Curves[0].LUT = identity
	if err := editor.SetAdjustment(nil, id, adjustment); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("old and proposed metadata together escaped the peak budget: %v", err)
	}
	after, _ := editor.Changes()
	if before != after || node.adjustment != oldSemantic || editor.ownedBytesLocked(nil) != owned {
		t.Fatal("peak budget failure changed the existing successful edit")
	}
}

func TestSetAdjustmentCancellationDuringSourceReadKeepsEditorUnchanged(t *testing.T) {
	data := adjustmentEditFixture(1, compositeFixtureTag("thrs", []byte{0, 128}))
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
	id := adjustmentEditLayer(t, editor)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader.trigger, reader.cancel = source.Layers[1].AdditionalInfo[0].Data, cancel
	if err := editor.SetAdjustment(ctx, id,
		psdadjust.Adjustment{Kind: psdadjust.Threshold, Threshold: 100}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation during metadata read was lost: %v", err)
	}
	state, _ := editor.Changes()
	node, _, _, _ := editor.findNodeLocked(id)
	if state.Dirty || len(node.additional) != 0 || node.adjustment != nil {
		t.Fatal("canceled read committed partial metadata")
	}
}

func TestSetAdjustmentWorkingBudgetRejectsBeforeMutation(t *testing.T) {
	limits := psdfile.DefaultLimits()
	limits.MaxDecodeBytes = 16 << 10
	editor := compositeEditor(t, adjustmentEditFixture(1, compositeFixtureTag("thrs", []byte{0, 128})),
		psdfile.LoadOptions{Limits: limits})
	id := adjustmentEditLayer(t, editor)
	if err := editor.SetAdjustment(nil, id,
		psdadjust.Adjustment{Kind: psdadjust.Threshold, Threshold: 100}); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("metadata scratch escaped the working budget: %v", err)
	}
	state, _ := editor.Changes()
	node, _, _, _ := editor.findNodeLocked(id)
	if state.Dirty || len(node.additional) != 0 || node.adjustment != nil {
		t.Fatal("budget failure committed partial edits")
	}
}
