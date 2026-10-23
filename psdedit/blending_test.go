package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestBlendModeEditPixelsAndRoundTrip(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		data := compositeFixture(version, 8, 3, []compositeFixtureLayer{
			{color: []byte{100, 200, 80, 255}},
			{color: []byte{64, 128, 255, 255}, tags: compositeFixtureTag("zzZZ", []byte{9, 7, 5})},
		})
		original := bytes.Clone(data)
		editor := compositeEditor(t, data)
		if err := editor.SetBlendMode(2, "multiply"); err != nil {
			t.Fatal(err)
		}
		info, err := editor.Layer(2)
		if err != nil || info.BlendMode != "mul " || info.Clipping {
			t.Fatalf("blend query = %+v, %v", info, err)
		}
		pixels, err := editor.Composite(nil)
		if err != nil {
			t.Fatal(err)
		}
		p3ExpectRGBA(t, pixels, []byte{25, 100, 80, 255})
		saved := compositeSave(t, editor)
		compositeExpect(t, saved, []byte{25, 100, 80})
		if saved.Layers[1].BlendMode != "mul " || saved.Header.Version != version {
			t.Fatal("blend edit or source version was lost")
		}
		before, err := editor.source.ReadSpan(editor.source.Layers[1].Extra)
		if err != nil {
			t.Fatal(err)
		}
		after, err := saved.ReadSpan(saved.Layers[1].Extra)
		if err != nil || !bytes.Equal(before, after) || !bytes.Equal(data, original) {
			t.Fatal("simple blend edit rewrote source bytes or unrelated extra data")
		}
		for index, channel := range editor.source.Layers[1].Channels {
			before, err := editor.source.ReadSpan(channel.Span)
			if err != nil {
				t.Fatal(err)
			}
			after, err := saved.ReadSpan(saved.Layers[1].Channels[index].Span)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("blend edit changed saved layer channels")
			}
		}
		if err := editor.SetBlendMode(2, "mul "); err != nil || editor.revision != 1 {
			t.Fatalf("same blend key changed revision: %v", err)
		}
	}
}

func TestClippingEditChangesCoverageAndRoundTrips(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		editor := compositeEditor(t, compositeFixture(version, 8, 3, []compositeFixtureLayer{
			{color: []byte{255, 0, 0, 128}}, {color: []byte{0, 255, 0, 128}},
		}))
		if err := editor.SetClipping(2, true); err != nil {
			t.Fatal(err)
		}
		pixels, err := editor.Composite(nil)
		if err != nil {
			t.Fatal(err)
		}
		p3ExpectRGBA(t, pixels, []byte{127, 128, 0, 128})
		saved := compositeSave(t, editor)
		compositeExpect(t, saved, []byte{191, 191, 127, 128})
		if saved.Layers[1].Clipping != 1 || editor.source.Layers[1].Clipping != 0 {
			t.Fatal("clip edit did not round-trip or mutated source")
		}
		if err := editor.SetClipping(2, true); err != nil || editor.revision != 1 {
			t.Fatalf("same clip value changed revision: %v", err)
		}
		if err := editor.SetClipping(2, false); err != nil {
			t.Fatal(err)
		}
		if info, err := editor.Layer(2); err != nil || info.Clipping {
			t.Fatalf("clip reset query is stale: %+v %v", info, err)
		}
	}
}

func TestGroupBlendUpdatesAllOverridesAndPreservesOpaqueTail(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		override := binary.BigEndian.AppendUint32(nil, 1)
		override = append(override, []byte("8BIMnorm")...)
		override = append(override, 0, 0, 0, 7)
		tags := compositeFixtureTag("lsdk", override)
		tags = append(tags, compositeFixtureTag("zzZZ", []byte{9, 7})...)
		editor := compositeEditor(t, compositeFixture(version, 8, 3, []compositeFixtureLayer{
			{color: []byte{100, 200, 80, 255}}, {divider: 3},
			{color: []byte{64, 128, 255, 255}}, {divider: 1, blend: "pass", tags: tags},
		}))
		info, err := editor.Layer(4)
		if err != nil || info.BlendMode != "norm" {
			t.Fatalf("group effective override was ignored: %+v %v", info, err)
		}
		if err := editor.SetBlendMode(4, "multiply"); err != nil {
			t.Fatal(err)
		}
		pixels, err := editor.Composite(nil)
		if err != nil {
			t.Fatal(err)
		}
		p3ExpectRGBA(t, pixels, []byte{25, 100, 80, 255})
		saved := compositeSave(t, editor)
		if saved.Layers[3].BlendMode != "mul " || saved.Layers[1].BlendMode != "norm" {
			t.Fatal("group blend record or end marker is wrong")
		}
		for _, block := range saved.Layers[3].AdditionalInfo {
			if block.Key != "lsct" && block.Key != "lsdk" {
				continue
			}
			payload, err := saved.ReadSpan(block.Data)
			if err != nil || string(payload[8:12]) != "mul " {
				t.Fatalf("retained group override is stale: %v", err)
			}
			if block.Key == "lsdk" && !bytes.Equal(payload[12:], override[12:]) {
				t.Fatal("opaque group subtype was changed")
			}
		}
		reopened, err := New(saved)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		pixels, err = reopened.Composite(nil)
		if err != nil {
			t.Fatal(err)
		}
		p3ExpectRGBA(t, pixels, []byte{25, 100, 80, 255})
	}
}

func TestNewAndShortGroupBlendRepeatedEdits(t *testing.T) {
	editor, err := Create(nil, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = editor.Close() })
	group, err := editor.AddGroup(0, 0, "group", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := editor.SetBlendMode(group, "normal"); err != nil {
		t.Fatal(err)
	}
	saved := compositeSave(t, editor)
	if saved.Layers[1].BlendMode != "norm" {
		t.Fatal("new group blend edit was lost")
	}
	if err := editor.SetBlendMode(group, "pass"); err != nil {
		t.Fatal(err)
	}
	// An untouched new group carries a four-byte divider without an override.
	untouched, err := editor.AddGroup(0, 1, "short", true)
	if err != nil {
		t.Fatal(err)
	}
	_ = untouched
	short, err := New(compositeSave(t, editor))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = short.Close() })
	layers, err := short.Layers()
	if err != nil {
		t.Fatal(err)
	}
	id := layers[1].ID
	for _, mode := range []string{"normal", "multiply", "pass"} {
		if err := short.SetBlendMode(id, mode); err != nil {
			t.Fatalf("repeated short-divider edit failed: %v", err)
		}
	}
	compositeSave(t, short)
}

func TestBlendEditsRejectInvalidInputsAtomicallyAndGatePreview(t *testing.T) {
	editor := compositeEditor(t, compositeFixture(1, 8, 3,
		[]compositeFixtureLayer{{color: []byte{255, 0, 0, 255}}}))
	for _, mode := range []string{"", "unknown", "pass", "normal\x00"} {
		if err := editor.SetBlendMode(1, mode); !errors.Is(err, ErrInvalidEdit) {
			t.Fatalf("invalid blend %q accepted: %v", mode, err)
		}
	}
	if err := editor.SetClipping(99, true); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("invalid clipping handle accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.SetBlendModeContext(ctx, 1, "multiply"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled blend edit accepted: %v", err)
	}
	if editor.revision != 0 {
		t.Fatal("invalid edits changed revision")
	}
	if err := editor.SetBlendMode(1, "dissolve"); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := editor.Write(nil, &output); !errors.Is(err, ErrUnsupported) || output.Len() != 0 {
		t.Fatalf("unsupported blend produced a misleading or partial preview: %v", err)
	}
	pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	copy(pixels.Pix, []byte{255, 0, 0, 255})
	saved := compositeSave(t, editor, SaveOptions{CompositeImage: pixels})
	if saved.Layers[0].BlendMode != "diss" {
		t.Fatal("explicit preview did not permit lossless dissolve metadata save")
	}
}

func TestBlendGroupBudgetAndMalformedOverridesAreAtomic(t *testing.T) {
	for _, payload := range [][]byte{{0, 0, 0, 1, 9}, append([]byte{0, 0, 0, 1}, []byte("NOPEpass")...)} {
		editor := compositeEditor(t, compositeFixture(1, 8, 3,
			[]compositeFixtureLayer{{divider: 3}, {divider: 1, tags: compositeFixtureTag("lsdk", payload)}}))
		if err := editor.SetBlendMode(2, "multiply"); !errors.Is(err, ErrUnsupported) || editor.revision != 0 {
			t.Fatalf("malformed source accepted or mutated: %v", err)
		}
	}
	data := compositeFixture(1, 8, 3, []compositeFixtureLayer{{divider: 3}, {divider: 1}})
	editor := compositeEditor(t, data, psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 64}})
	if err := editor.SetBlendMode(2, "multiply"); !errors.Is(err, psdfile.ErrLimit) || editor.revision != 0 ||
		len(editor.roots[0].additional) != 0 {
		t.Fatalf("blend metadata exceeded working budget or mutated state: %v", err)
	}
}
