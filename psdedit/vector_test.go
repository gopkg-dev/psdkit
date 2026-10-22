package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func editVectorPayload() []byte {
	data := binary.BigEndian.AppendUint32(nil, 3)
	data = binary.BigEndian.AppendUint32(data, 0)
	var record [26]byte
	binary.BigEndian.PutUint16(record[:2], 6)
	data = append(data, record[:]...)
	clear(record[:])
	binary.BigEndian.PutUint16(record[:2], 8)
	data = append(data, record[:]...)
	clear(record[:])
	binary.BigEndian.PutUint16(record[2:4], 4)
	binary.BigEndian.PutUint16(record[4:6], 1)
	binary.BigEndian.PutUint16(record[6:8], 1)
	data = append(data, record[:]...)
	for _, point := range [][2]uint32{{0, 0}, {1 << 24, 0}, {1 << 24, 1 << 24}, {0, 1 << 24}} {
		clear(record[:])
		binary.BigEndian.PutUint16(record[:2], 2)
		for offset := 2; offset < 26; offset += 8 {
			binary.BigEndian.PutUint32(record[offset:], point[1])
			binary.BigEndian.PutUint32(record[offset+4:], point[0])
		}
		data = append(data, record[:]...)
	}
	return append(data, 0, 0)
}

func editVectorMask(t *testing.T, source *psdfile.Document) *psdvector.Mask {
	t.Helper()
	for _, block := range source.Layers[0].AdditionalInfo {
		if block.Key == "vmsk" || block.Key == "vsms" {
			mask, err := source.DecodeVectorMaskBlock(nil, block)
			if err != nil {
				t.Fatal(err)
			}
			return mask
		}
	}
	t.Fatal("independent fixture has no vector-mask block")
	return nil
}

func halfVector(mask *psdvector.Mask) {
	for _, index := range []int{1, 2} {
		knot := &mask.Subpaths[0].Knots[index]
		knot.Preceding.X, knot.Anchor.X, knot.Leaving.X = 0.5, 0.5, 0.5
	}
}

func TestSetVectorMaskUpdatesOwnedGeometryPreviewAndSavedBlock(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, key := range []string{"vmsk", "vsms"} {
			editor := compositeEditor(t, compositeFixture(version, 8, 3, []compositeFixtureLayer{
				{color: []byte{255, 0, 0, 255}, tags: compositeFixtureTag(key, editVectorPayload())},
			}))
			original, err := editor.source.ReadSpan(editor.source.Source)
			if err != nil {
				t.Fatal(err)
			}
			mask := editVectorMask(t, editor.source)
			if err := editor.SetVectorMask(nil, 1, mask); err != nil {
				t.Fatal(err)
			}
			changes, err := editor.Changes()
			if err != nil || changes.Revision != 0 || changes.Dirty {
				t.Fatalf("unchanged typed mask marked the editor dirty: %#v, %v", changes, err)
			}
			halfVector(mask)
			if err := editor.SetVectorMask(context.Background(), 1, mask); err != nil {
				t.Fatal(err)
			}
			mask.Subpaths[0].Knots[1].Anchor.X = 0.9
			mask.Records[3].Raw[0] = 99
			if editor.roots[0].vectorMask.Subpaths[0].Knots[1].Anchor.X != 0.5 {
				t.Fatal("editor borrowed caller vector metadata")
			}
			preview, err := editor.Composite(nil)
			if err != nil || !bytes.Equal(preview.Pix, []byte{255, 0, 0, 128}) {
				t.Fatalf("edited vector geometry did not affect the preview: %v, %v", preview, err)
			}
			saved := compositeSave(t, editor, SaveOptions{Compression: 3})
			compositeExpect(t, saved, []byte{255, 127, 127, 128})
			persisted := editVectorMask(t, saved)
			if persisted.Subpaths[0].Knots[1].Anchor.X != 0.5 || saved.Header.Version != version {
				t.Fatal("PSD/PSB save lost typed vector changes or format")
			}
			after, err := editor.source.ReadSpan(editor.source.Source)
			if err != nil || !bytes.Equal(original, after) {
				t.Fatalf("source bytes changed during vector editing: %v", err)
			}
		}
	}
}

func TestSetVectorMaskPreservesOtherSemanticCostsAndMaskMetadata(t *testing.T) {
	data := compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{color: []byte{255, 0, 0, 255}, mask: []byte{255}, tags: compositeFixtureTag("vmsk", editVectorPayload())},
	})
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	record := source.Layers[0]
	maskOffset := record.Mask.Span.Offset
	prefixes := []int64{source.LayerMaskSection.Offset, source.LayerInfo.Offset, record.Extra.Offset - 4}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	// Reuse the two standard padding bytes for vector parameter flags/density and append a feather value.
	data[maskOffset+17], data[maskOffset+18], data[maskOffset+19] = 16, 12, 201
	binary.BigEndian.PutUint32(data[record.MaskData.Offset:], 28)
	for _, prefix := range prefixes {
		length := binary.BigEndian.Uint32(data[prefix:])
		binary.BigEndian.PutUint32(data[prefix:], length+8)
	}
	insertion := maskOffset + 20
	withParameters := append([]byte{}, data[:insertion]...)
	withParameters = binary.BigEndian.AppendUint64(withParameters, math.Float64bits(0.5))
	withParameters = append(withParameters, data[insertion:]...)
	editor := compositeEditor(t, withParameters)
	parameters := editor.source.Layers[0].Mask
	if parameters.VectorDensity == nil || *parameters.VectorDensity != 201 ||
		parameters.VectorFeather == nil || *parameters.VectorFeather != 0.5 {
		t.Fatal("independent mask parameter fixture did not contain density and feather")
	}
	node := editor.roots[0]
	node.setSemanticCost("other", 123)
	mask := editVectorMask(t, editor.source)
	halfVector(mask)
	originalMask, err := editor.source.ReadSpan(editor.source.Layers[0].MaskData)
	if err != nil {
		t.Fatal(err)
	}
	if err := editor.SetVectorMask(nil, 1, mask); err != nil {
		t.Fatal(err)
	}
	if node.semanticCosts["other"] != 123 || node.semanticCosts["vector"] == 0 ||
		node.semanticBytes != 123+node.semanticCosts["vector"] {
		t.Fatal("vector replacement overwrote another semantic overlay's retained cost")
	}
	firstCost := node.semanticCosts["vector"]
	mask.Disabled = true
	if err := editor.SetVectorMask(nil, 1, mask); err != nil {
		t.Fatal(err)
	}
	if node.semanticBytes != 123+firstCost {
		t.Fatal("replacing the vector twice double-counted semantic metadata")
	}
	preview := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	copy(preview.Pix, []byte{255, 0, 0, 255})
	saved := compositeSave(t, editor, SaveOptions{CompositeImage: preview})
	currentMask, err := saved.ReadSpan(saved.Layers[0].MaskData)
	if err != nil || !bytes.Equal(currentMask, originalMask) {
		t.Fatalf("vector replacement changed retained mask-density or feather encoding: %v", err)
	}
}

func TestSetVectorMaskRejectsAmbiguityLimitsAndCancellationBeforeMutation(t *testing.T) {
	base := compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{color: []byte{255, 0, 0, 255}, tags: compositeFixtureTag("vmsk", editVectorPayload())},
	})
	limited := compositeEditor(t, base, psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 200}})
	mask, err := psdvector.DecodeMask(nil, editVectorPayload(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	halfVector(mask)
	if err := limited.SetVectorMask(nil, 1, mask); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("vector edit ignored total owned metadata budget: %v", err)
	}
	if limited.revision != 0 || limited.roots[0].vectorMask != nil || len(limited.roots[0].additional) != 0 {
		t.Fatal("failed vector budget left a partial overlay")
	}
	editor := compositeEditor(t, base)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.SetVectorMask(ctx, 1, mask); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled vector edit: %v", err)
	}
	mask.Subpaths[0].Knots[0].Anchor.X = math.NaN()
	if err := editor.SetVectorMask(nil, 1, mask); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("nonfinite vector geometry was not classified as invalid input: %v", err)
	}
	if editor.revision != 0 || editor.roots[0].vectorMask != nil || len(editor.roots[0].additional) != 0 {
		t.Fatal("failed vector encoding or cancellation changed the editor")
	}
	noMask := compositeEditor(t, compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{color: []byte{255, 0, 0, 255}},
	}))
	mask.Subpaths[0].Knots[0].Anchor.X = 0
	if err := noMask.SetVectorMask(nil, 1, mask); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("vector geometry was added to a layer without an original mask: %v", err)
	}
	ambiguous := compositeEditor(t, compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{color: []byte{255, 0, 0, 255}, tags: append(compositeFixtureTag("vmsk", editVectorPayload()),
			compositeFixtureTag("vsms", editVectorPayload())...)},
	}))
	if err := ambiguous.SetVectorMask(nil, 1, mask); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ambiguous multiple vector-mask blocks were edited: %v", err)
	}
	mask.Width = 2
	if err := editor.SetVectorMask(nil, 1, mask); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("vector canvas dimensions differed silently: %v", err)
	}
}

func TestSetVectorMaskReplacementIncludesPreviousOverlayInPeakBudget(t *testing.T) {
	base := compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{color: []byte{255, 0, 0, 255}, tags: compositeFixtureTag("vmsk", editVectorPayload())},
	})
	editor := compositeEditor(t, base, psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 4000}})
	mask := editVectorMask(t, editor.source)
	halfVector(mask)
	if err := editor.SetVectorMask(nil, 1, mask); err != nil {
		t.Fatal(err)
	}
	node := editor.roots[0]
	previous, previousCost := node.vectorMask, node.semanticBytes
	var retained []byte
	for _, data := range node.additional {
		retained = append([]byte(nil), data...)
	}
	// An equally sized final overlay fits, but the old and replacement copies cannot coexist.
	mask.Disabled = true
	if err := editor.SetVectorMask(nil, 1, mask); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("replacement ignored peak residency of the original overlay: %v", err)
	}
	if editor.revision != 1 || node.vectorMask != previous || node.semanticBytes != previousCost {
		t.Fatal("peak budget failure changed semantic geometry, retained costs or revision")
	}
	for _, data := range node.additional {
		if !bytes.Equal(data, retained) {
			t.Fatal("peak budget failure changed the encoded vector block")
		}
	}
}
