package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func blendIfFixture(t *testing.T, version uint16, payload []byte) []byte {
	t.Helper()
	data := compositeFixture(version, 8, 3, []compositeFixtureLayer{
		{color: []byte{255, 255, 255, 255}},
		{color: []byte{64, 64, 64, 255}, mask: []byte{255},
			tags: compositeFixtureTag("zzZZ", []byte{9, 31, 73})},
	})
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	record := source.Layers[1]
	for _, prefix := range []int64{source.LayerMaskSection.Offset, source.LayerInfo.Offset} {
		if version == 2 {
			binary.BigEndian.PutUint64(data[prefix:], binary.BigEndian.Uint64(data[prefix:])+uint64(len(payload)))
		} else {
			binary.BigEndian.PutUint32(data[prefix:], binary.BigEndian.Uint32(data[prefix:])+uint32(len(payload)))
		}
	}
	position := record.Extra.Offset - 4
	binary.BigEndian.PutUint32(data[position:], binary.BigEndian.Uint32(data[position:])+uint32(len(payload)))
	binary.BigEndian.PutUint32(data[record.BlendRanges.Offset:], uint32(len(payload)))
	insertion := int(record.BlendRanges.Offset + 4)
	result := append(bytes.Clone(data[:insertion]), payload...)
	result = append(result, data[insertion:]...)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	return result
}

func blendIfIdentity() psdmodel.BlendSlider {
	return psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 255}
}

func TestSetBlendIfRGB8PSDPSBRoundTripAndSelectivePreservation(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			data := blendIfFixture(t, version, nil)
			original := bytes.Clone(data)
			editor := compositeEditor(t, data)
			identity := blendIfIdentity()
			input := []psdmodel.BlendRange{{
				Source: psdmodel.BlendSlider{BlackHigh: 128, WhiteLow: 255, WhiteHigh: 255}, Destination: identity,
			}}
			if err := editor.SetBlendIfContext(nil, 2, input); err != nil {
				t.Fatal(err)
			}
			if editor.revision != 1 || !editor.visualChanged || editor.structureChanged {
				t.Fatal("Blend If change lost visual revision semantics")
			}
			input[0].Source.BlackHigh = 1
			values, err := editor.BlendIf(2)
			if err != nil || len(values) != 1 || values[0].Source.BlackHigh != 128 {
				t.Fatalf("setter retained caller-owned input: %v %v", values, err)
			}
			values[0].Source.BlackHigh = 2
			values, err = editor.BlendIfContext(nil, 2)
			if err != nil || values[0].Source.BlackHigh != 128 {
				t.Fatalf("getter exposed editor-owned input: %v %v", values, err)
			}
			preview := image.NewNRGBA(image.Rect(0, 0, 1, 1))
			editor.nativeTextPreview = preview
			if err := editor.SetBlendIf(2, values); err != nil || editor.revision != 1 ||
				editor.nativeTextPreview != preview {
				t.Fatalf("identical overlay changed revision or preview: %v", err)
			}
			editor.nativeTextPreview = nil
			pixels, err := editor.Composite(nil)
			if err != nil {
				t.Fatal(err)
			}
			p3ExpectRGBA(t, pixels, []byte{159, 159, 159, 255})
			saved := compositeSave(t, editor)
			compositeExpect(t, saved, []byte{159, 159, 159})
			encoded, err := saved.ReadSpan(saved.Layers[1].BlendRanges)
			want := []byte{0, 0, 0, 8, 0, 128, 255, 255, 0, 0, 255, 255}
			if err != nil || !bytes.Equal(encoded, want) || saved.Header.Version != version {
				t.Fatalf("saved split bytes/version differ: %v %v", encoded, err)
			}
			before, after := editor.source.Layers[1], saved.Layers[1]
			for _, spans := range [][2]psdfile.Span{{before.MaskData, after.MaskData}, {before.NameData, after.NameData}} {
				first, firstErr := editor.source.ReadSpan(spans[0])
				second, secondErr := saved.ReadSpan(spans[1])
				if firstErr != nil || secondErr != nil || !bytes.Equal(first, second) {
					t.Fatal("Blend If save changed mask or name bytes")
				}
			}
			for index, block := range before.AdditionalInfo {
				first, firstErr := editor.source.ReadSpan(block.Span)
				second, secondErr := saved.ReadSpan(after.AdditionalInfo[index].Span)
				if firstErr != nil || secondErr != nil || !bytes.Equal(first, second) {
					t.Fatal("Blend If save changed unrelated tagged bytes or padding")
				}
			}
			for index, channel := range before.Channels {
				first, firstErr := editor.source.ReadSpan(channel.Data)
				second, secondErr := saved.ReadSpan(after.Channels[index].Data)
				if firstErr != nil || secondErr != nil || !bytes.Equal(first, second) {
					t.Fatal("Blend If save changed encoded channel bytes")
				}
			}
			reopened, err := New(saved)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			if err := reopened.SetBlendIf(2, values); err != nil || reopened.revision != 0 {
				t.Fatalf("same source ranges were rewritten: %v", err)
			}
			if err := reopened.SetBlendIf(2, nil); err != nil {
				t.Fatal(err)
			}
			cleared := compositeSave(t, reopened)
			compositeExpect(t, cleared, []byte{64, 64, 64})
			if cleared.Layers[1].BlendRanges.Length != 4 || !bytes.Equal(original, data) {
				t.Fatal("range reset retained payload or source bytes changed")
			}
		})
	}
}

func TestSetBlendIfPreservesAllRGBSplitsAndOverlappingRamps(t *testing.T) {
	payload := []byte{
		17, 113, 97, 241, 3, 77, 163, 252,
		29, 139, 93, 224, 19, 99, 177, 249,
		0, 93, 151, 255, 31, 177, 109, 251,
		37, 147, 111, 229, 41, 121, 181, 238,
	}
	editor := compositeEditor(t, blendIfFixture(t, 2, payload))
	ranges, err := editor.BlendIf(2)
	if err != nil || len(ranges) != 4 {
		t.Fatalf("split/overlapping source ranges rejected: %v %v", ranges, err)
	}
	if err := editor.SetBlendIf(2, ranges); err != nil || editor.revision != 0 {
		t.Fatalf("existing byte endpoints normalized: %v", err)
	}
	ranges[2].Destination.WhiteLow++
	if err := editor.SetBlendIf(2, ranges); err != nil {
		t.Fatal(err)
	}
	saved := compositeSave(t, editor)
	data, err := saved.ReadSpan(saved.Layers[1].BlendRanges)
	payload[22]++
	if err != nil || !bytes.Equal(data[4:], payload) {
		t.Fatalf("unselected split endpoints changed: %v %v", data, err)
	}
}

func TestSetBlendIfInputCancellationAndBudgetAreAtomic(t *testing.T) {
	identity := blendIfIdentity()
	valid := []psdmodel.BlendRange{{Source: identity, Destination: identity}}
	data := blendIfFixture(t, 1, nil)
	editor := compositeEditor(t, data)
	before, _ := editor.Changes()
	for _, invalid := range [][]psdmodel.BlendRange{
		make([]psdmodel.BlendRange, 58),
		{{Source: psdmodel.BlendSlider{BlackLow: 1}, Destination: identity}},
		{{Source: identity, Destination: psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 254}}},
	} {
		if err := editor.SetBlendIf(2, invalid); !errors.Is(err, ErrInvalidEdit) {
			t.Fatalf("invalid split/range count accepted: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.SetBlendIfContext(ctx, 2, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("setter ignored cancellation: %v", err)
	}
	if _, err := editor.BlendIfContext(ctx, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("getter ignored cancellation: %v", err)
	}
	if err := editor.SetBlendIf(99, valid); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("invalid layer accepted: %v", err)
	}
	after, _ := editor.Changes()
	if before != after || editor.roots[1].blendRangesChanged || editor.ownedBytesLocked(nil) != 0 {
		t.Fatal("failed Blend If edit changed revision or retained state")
	}
	limited := compositeEditor(t, data, psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 23}})
	if err := limited.SetBlendIf(2, valid); !errors.Is(err, psdfile.ErrLimit) || limited.revision != 0 {
		t.Fatalf("source plus replacement peak working budget ignored: %v", err)
	}
	if err := editor.SetBlendIf(2, valid); err != nil || editor.ownedBytesLocked(nil) != 20 {
		t.Fatalf("typed and encoded Blend If overlays not charged exactly: %v", err)
	}
	if err := editor.SetBlendIf(2, nil); err != nil || editor.ownedBytesLocked(nil) != 4 {
		t.Fatalf("superseded Blend If overlay remains charged: %v", err)
	}
	if err := editor.source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := editor.SetBlendIf(2, valid); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed-source edit category changed: %v", err)
	}
}

type blendIfReader struct {
	*bytes.Reader
	onRead func() error
}

func (reader *blendIfReader) ReadAt(data []byte, offset int64) (int, error) {
	if len(data) != 0 && reader.onRead != nil {
		if err := reader.onRead(); err != nil {
			return 0, err
		}
	}
	return reader.Reader.ReadAt(data, offset)
}

func TestSetBlendIfCancellationDuringReadAndReadFailuresStayAtomic(t *testing.T) {
	data := blendIfFixture(t, 1, nil)
	reader := &blendIfReader{Reader: bytes.NewReader(data)}
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
	values := []psdmodel.BlendRange{{Source: blendIfIdentity(), Destination: blendIfIdentity()}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader.onRead = func() error { cancel(); return nil }
	if err := editor.SetBlendIfContext(ctx, 2, values); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation while reading committed an edit: %v", err)
	}
	reader.onRead = func() error { return io.ErrClosedPipe }
	if err := editor.SetBlendIf(2, values); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("source read failure was swallowed: %v", err)
	}
	if editor.revision != 0 || editor.roots[1].blendRangesChanged || editor.ownedBytesLocked(nil) != 0 {
		t.Fatal("canceled or failed read changed editor state")
	}
}

func TestBlendIfSourceValidationAndOwnedQueryBudget(t *testing.T) {
	for _, payload := range [][]byte{
		{1, 0, 255, 255, 0, 0, 255, 255},
		bytes.Repeat([]byte{0, 0, 255, 255, 0, 0, 255, 255}, 58),
	} {
		editor := compositeEditor(t, blendIfFixture(t, 1, payload))
		if _, err := editor.BlendIf(2); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unknown source range representation accepted: %v", err)
		}
		if err := editor.SetBlendIf(2, nil); !errors.Is(err, ErrUnsupported) || editor.revision != 0 {
			t.Fatalf("unknown source representation silently repaired: %v", err)
		}
	}
	payload := []byte{0, 128, 255, 255, 0, 0, 255, 255}
	limited := compositeEditor(t, blendIfFixture(t, 1, payload),
		psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 19}})
	if _, err := limited.BlendIf(2); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("owned getter ignored simultaneous raw and typed allocation: %v", err)
	}
	editor := compositeEditor(t, blendIfFixture(t, 1, nil))
	identity := psdmodel.BlendRange{Source: blendIfIdentity(), Destination: blendIfIdentity()}
	values := make([]psdmodel.BlendRange, 57)
	for index := range values {
		values[index] = identity
	}
	if err := editor.SetBlendIf(2, values); err != nil {
		t.Fatalf("57 valid composite/channel ranges rejected: %v", err)
	}
	actual, err := editor.BlendIf(2)
	if err != nil || !slices.Equal(values, actual) {
		t.Fatalf("extra-channel defaults lost: %v", err)
	}
	compositeSave(t, editor)
	values[4].Source.BlackHigh = 64
	if err := editor.SetBlendIf(2, values); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := editor.Write(nil, &output); !errors.Is(err, ErrUnsupported) || output.Len() != 0 {
		t.Fatalf("custom extra-channel Blend If escaped automatic-preview gate: %v", err)
	}
	preview, err := editor.source.DecodeComposite(nil)
	if err != nil {
		t.Fatal(err)
	}
	saved := compositeSave(t, editor, SaveOptions{Composite: preview})
	if saved.Layers[1].BlendRanges.Length != 4+57*8 {
		t.Fatal("accurate external composite did not permit exact preservation of unsupported ranges")
	}
}

func TestSetBlendIfNewLayersGroupsAndHighDepthSaving(t *testing.T) {
	values := []psdmodel.BlendRange{{
		Source:      psdmodel.BlendSlider{BlackHigh: 128, WhiteLow: 255, WhiteHigh: 255},
		Destination: blendIfIdentity(),
	}}
	for _, version := range []uint16{1, 2} {
		editor := compositeEditor(t, compositeFixture(version, 8, 3, nil))
		if err := editor.SetBlendIf(editor.roots[0].id, values); err != nil {
			t.Fatalf("virtual layer rejected Blend If: %v", err)
		}
		group, err := editor.AddGroup(0, 1, "Blend If group", true)
		if err != nil {
			t.Fatal(err)
		}
		if err := editor.SetBlendIf(group, values); err != nil {
			t.Fatalf("new group rejected Blend If: %v", err)
		}
		preview, err := editor.source.DecodeComposite(nil)
		if err != nil {
			t.Fatal(err)
		}
		saved := compositeSave(t, editor, SaveOptions{Composite: preview})
		for index, record := range saved.Layers {
			want := int64(12)
			if record.Divider == 3 {
				want = 4
			}
			if record.BlendRanges.Length != want {
				t.Fatalf("new layer %d has ranges length %d, want %d", index, record.BlendRanges.Length, want)
			}
		}
		for _, depth := range []uint16{16, 32} {
			high := compositeEditor(t, compositeFixture(version, depth, 3,
				[]compositeFixtureLayer{{color: []byte{64, 64, 64, 255}}}))
			if err := high.SetBlendIf(1, values); err != nil {
				t.Fatalf("high-depth metadata edit refused: %v", err)
			}
			preview, err := high.source.DecodeComposite(nil)
			if err != nil {
				t.Fatal(err)
			}
			result := compositeSave(t, high, SaveOptions{Composite: preview})
			if result.Header.Version != version || result.Header.Depth != depth ||
				result.Layers[0].BlendRanges.Length != 12 {
				t.Fatal("high-depth save failed to retain Blend If metadata or precision")
			}
		}
	}
}
