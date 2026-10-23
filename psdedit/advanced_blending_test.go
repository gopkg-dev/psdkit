package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func advancedTestOptions() AdvancedBlendingOptions {
	return AdvancedBlendingOptions{
		Knockout: new(psdmodel.KnockoutDeep), BlendClipped: new(false), BlendInterior: new(true),
		MaskAsGlobal: new(true), VectorMaskAsGlobal: new(false), TransparencyShapes: new(false),
		ChannelRestrictions: []uint32{0, 2},
	}
}

func advancedTestTags() []byte {
	var tags []byte
	for _, key := range []string{"knko", "clbl", "infx", "lmgm", "vmgm", "tsly"} {
		value := byte(0)
		if key == "knko" || key == "clbl" || key == "vmgm" || key == "tsly" {
			value = 1
		}
		tags = append(tags, compositeFixtureTag(key, []byte{value, 0, 0, 0})...)
	}
	return append(tags, compositeFixtureTag("brst", []byte{0, 0, 0, 1})...)
}

func advancedReopen(t *testing.T, source *psdfile.Document) *Editor {
	t.Helper()
	editor, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = editor.Close() })
	return editor
}

func TestAdvancedBlendingPSDPSBRoundTripAndOwnership(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("version%d/existing%t", version, existing), func(t *testing.T) {
				tags := []byte{}
				if existing {
					tags = advancedTestTags()
				}
				tags = append(tags, compositeFixtureTag("pRsv", []byte{9, 7, 3})...)
				data := compositeFixture(version, 8, 3,
					[]compositeFixtureLayer{{color: []byte{210, 40, 80, 255}, tags: tags}})
				original := bytes.Clone(data)
				editor := compositeEditor(t, data)
				options := advancedTestOptions()
				preview := image.NewNRGBA(image.Rect(0, 0, 1, 1))
				editor.nativeTextPreview = preview
				if err := editor.SetAdvancedBlending(nil, 1, options); err != nil {
					t.Fatal(err)
				}
				if editor.revision != 1 || !editor.visualChanged || editor.structureChanged ||
					editor.nativeTextPreview != nil {
					t.Fatal("advanced settings did not invalidate the preview as a single visual edit")
				}
				*options.Knockout, *options.BlendClipped = psdmodel.KnockoutNone, true
				options.ChannelRestrictions[0] = 1
				current, err := editor.AdvancedBlending(nil, 1)
				if err != nil || !reflect.DeepEqual(current, advancedTestOptions()) {
					t.Fatalf("setter borrowed caller memory: %+v, %v", current, err)
				}
				*current.Knockout, *current.BlendClipped = psdmodel.KnockoutShallow, true
				current.ChannelRestrictions[0] = 1
				current, err = editor.AdvancedBlending(nil, 1)
				if err != nil || !reflect.DeepEqual(current, advancedTestOptions()) {
					t.Fatalf("getter exposed mutable editor state: %+v, %v", current, err)
				}
				if err := editor.SetAdvancedBlending(nil, 1, current); err != nil || editor.revision != 1 {
					t.Fatalf("same-value edit changed revision: %v", err)
				}
				saved := compositeSave(t, editor, SaveOptions{CompositeImage: preview})
				persisted, err := advancedReopen(t, saved).AdvancedBlending(nil, 1)
				if err != nil || !reflect.DeepEqual(persisted, advancedTestOptions()) {
					t.Fatalf("typed settings changed after save/reopen: %+v, %v", persisted, err)
				}
				if saved.Header.Version != version || !bytes.Equal(data, original) {
					t.Fatal("save changed source bytes or format version")
				}
				for _, block := range editor.source.Layers[0].AdditionalInfo {
					if block.Key != "pRsv" {
						continue
					}
					last := saved.Layers[0].AdditionalInfo[len(saved.Layers[0].AdditionalInfo)-1]
					before, firstErr := editor.source.ReadSpan(block.Span)
					after, secondErr := saved.ReadSpan(last.Span)
					if firstErr != nil || secondErr != nil || !bytes.Equal(before, after) {
						t.Fatal("an opaque final source tag was moved or rewritten")
					}
				}
			})
		}
	}
}

func TestAdvancedBlendingAbsentDefaultsAndPartialPatches(t *testing.T) {
	editor := compositeEditor(t, compositeFixture(1, 8, 3,
		[]compositeFixtureLayer{{color: []byte{10, 20, 30, 255}}}))
	defaults := AdvancedBlendingOptions{Knockout: new(psdmodel.KnockoutNone), BlendClipped: new(true),
		TransparencyShapes: new(true), ChannelRestrictions: []uint32{}}
	for _, options := range []AdvancedBlendingOptions{{}, defaults} {
		if err := editor.SetAdvancedBlending(nil, 1, options); err != nil || editor.revision != 0 {
			t.Fatalf("default no-op materialized absent metadata: %v", err)
		}
	}
	current, err := editor.AdvancedBlending(nil, 1)
	if err != nil || !reflect.DeepEqual(current, AdvancedBlendingOptions{}) {
		t.Fatalf("absent source state was normalized: %+v, %v", current, err)
	}
	defaults.MaskAsGlobal = new(false)
	if err := editor.SetAdvancedBlending(nil, 1, defaults); err != nil {
		t.Fatal(err)
	}
	current, err = editor.AdvancedBlending(nil, 1)
	if err != nil || current.MaskAsGlobal == nil || *current.MaskAsGlobal || current.Knockout != nil ||
		current.BlendClipped != nil || current.TransparencyShapes != nil || current.ChannelRestrictions != nil {
		t.Fatalf("one effective patch leaked no-op absent defaults: %+v, %v", current, err)
	}
	if err := editor.SetAdvancedBlending(nil, 1, AdvancedBlendingOptions{
		BlendInterior: new(true), ChannelRestrictions: []uint32{1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := editor.SetAdvancedBlending(nil, 1, AdvancedBlendingOptions{
		BlendInterior: new(false), ChannelRestrictions: []uint32{},
	}); err != nil {
		t.Fatal(err)
	}
	if editor.revision != 3 {
		t.Fatal("partial patches did not each commit one revision")
	}
	preview := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	saved := compositeSave(t, editor, SaveOptions{CompositeImage: preview})
	current, err = advancedReopen(t, saved).AdvancedBlending(nil, 1)
	wanted := AdvancedBlendingOptions{MaskAsGlobal: new(false), BlendInterior: new(false),
		ChannelRestrictions: []uint32{}}
	if err != nil || !reflect.DeepEqual(current, wanted) {
		t.Fatalf("partial updates or empty restriction payload did not persist: %+v, %v", current, err)
	}
}

func TestAdvancedBlendingRenderAndUnsupportedRemainExplicit(t *testing.T) {
	editor := compositeEditor(t, compositeFixture(2, 8, 3, []compositeFixtureLayer{
		{color: []byte{10, 20, 30, 255}}, {color: []byte{200, 220, 240, 255}},
	}))
	for _, item := range []struct {
		channel uint32
		pixel   []byte
	}{{0, []byte{10, 220, 240, 255}}, {1, []byte{200, 20, 240, 255}}} {
		if err := editor.SetAdvancedBlending(nil, 2,
			AdvancedBlendingOptions{ChannelRestrictions: []uint32{item.channel}}); err != nil {
			t.Fatal(err)
		}
		pixels, err := editor.Composite(nil)
		if err != nil || !bytes.Equal(pixels.Pix, item.pixel) {
			t.Fatalf("current channel restriction did not affect rendering: %v, %v", pixels, err)
		}
		compositeExpect(t, compositeSave(t, editor), item.pixel[:3])
	}
	if err := editor.SetAdvancedBlending(nil, 2,
		AdvancedBlendingOptions{Knockout: new(psdmodel.KnockoutDeep)}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := editor.Write(nil, &output); !errors.Is(err, ErrUnsupported) || output.Len() != 0 {
		t.Fatalf("uncalibrated Knockout/restrictions escaped renderer guard: %v, %d", err, output.Len())
	}
	preview := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	saved := compositeSave(t, editor, SaveOptions{CompositeImage: preview})
	current, err := advancedReopen(t, saved).AdvancedBlending(nil, 2)
	if err != nil || current.Knockout == nil || *current.Knockout != psdmodel.KnockoutDeep ||
		!reflect.DeepEqual(current.ChannelRestrictions, []uint32{1}) {
		t.Fatalf("explicit preview did not preserve unsupported render settings: %+v, %v", current, err)
	}
}

func TestAdvancedBlendingMalformedAndDuplicateSourcesAreAtomic(t *testing.T) {
	for _, key := range []string{"knko", "clbl", "infx", "lmgm", "vmgm", "tsly", "brst"} {
		for _, duplicate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/duplicate%t", key, duplicate), func(t *testing.T) {
				data := []byte{0, 0, 0, 0}
				if !duplicate {
					data = []byte{0, 0, 0}
				}
				tags := compositeFixtureTag(key, data)
				if duplicate {
					tags = append(tags, compositeFixtureTag(key, data)...)
				}
				editor := compositeEditor(t, compositeFixture(1, 8, 3,
					[]compositeFixtureLayer{{color: []byte{20, 30, 40, 255}, tags: tags}}))
				before, _ := editor.Changes()
				preview := image.NewNRGBA(image.Rect(0, 0, 1, 1))
				editor.nativeTextPreview = preview
				if _, err := editor.AdvancedBlending(nil, 1); !errors.Is(err, ErrUnsupported) {
					t.Fatalf("getter normalized unknown source layout: %v", err)
				}
				if err := editor.SetAdvancedBlending(nil, 1, AdvancedBlendingOptions{}); !errors.Is(err, ErrUnsupported) {
					t.Fatalf("empty patch bypassed unknown layout: %v", err)
				}
				after, _ := editor.Changes()
				if before != after || editor.roots[0].advancedBlending != nil ||
					len(editor.roots[0].additional) != 0 || editor.nativeTextPreview != preview {
					t.Fatal("source-validation failure partially committed overlays")
				}
			})
		}
	}
	for _, item := range []struct {
		key  string
		data []byte
	}{
		{"knko", []byte{3, 0, 0, 0}}, {"clbl", []byte{2, 0, 0, 0}},
		{"tsly", []byte{1, 0, 2, 0}}, {"brst", make([]byte, 8)},
		{"brst", make([]byte, 57*4)},
	} {
		editor := compositeEditor(t, compositeFixture(2, 8, 3, []compositeFixtureLayer{
			{color: []byte{20, 30, 40, 255}, tags: compositeFixtureTag(item.key, item.data)},
		}))
		if err := editor.SetAdvancedBlending(nil, 1, advancedTestOptions()); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unknown enum, reserved bytes or invalid restrictions accepted: %v", err)
		}
	}
}

func TestAdvancedBlendingInputBudgetsAndCancellationAreAtomic(t *testing.T) {
	base := compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{color: []byte{20, 30, 40, 255}, tags: advancedTestTags()},
	})
	editor := compositeEditor(t, base)
	if err := editor.SetAdvancedBlending(nil, 1, advancedTestOptions()); err != nil {
		t.Fatal(err)
	}
	before, _ := editor.Changes()
	state, _ := editor.AdvancedBlending(nil, 1)
	ownedBytes := editor.ownedBytesLocked(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, operation := range []func() error{
		func() error { return editor.SetAdvancedBlending(ctx, 1, advancedTestOptions()) },
		func() error { _, err := editor.AdvancedBlending(ctx, 1); return err },
	} {
		if err := operation(); !errors.Is(err, context.Canceled) {
			t.Fatalf("context cancellation category changed: %v", err)
		}
	}
	for _, options := range []AdvancedBlendingOptions{
		{Knockout: new(psdmodel.Knockout(255))}, {ChannelRestrictions: []uint32{1, 1}},
		{ChannelRestrictions: make([]uint32, 57)},
	} {
		if err := editor.SetAdvancedBlending(nil, 1, options); !errors.Is(err, ErrInvalidEdit) {
			t.Fatalf("invalid patch accepted: %v", err)
		}
	}
	if err := editor.SetAdvancedBlending(nil, 100, AdvancedBlendingOptions{}); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("invalid handle accepted: %v", err)
	}
	after, _ := editor.Changes()
	current, _ := editor.AdvancedBlending(nil, 1)
	if before != after || !reflect.DeepEqual(state, current) || ownedBytes != editor.ownedBytesLocked(nil) {
		t.Fatal("rejected patch mutated existing advanced overlay")
	}
	limited := compositeEditor(t, base, psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 256}})
	if err := limited.SetAdvancedBlending(nil, 1, advancedTestOptions()); !errors.Is(err, psdfile.ErrLimit) ||
		limited.revision != 0 || limited.roots[0].advancedBlending != nil {
		t.Fatalf("working-budget failure was not atomic: %v", err)
	}
	if err := editor.source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := editor.SetAdvancedBlending(nil, 1, advancedTestOptions()); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed source error was hidden by an owned overlay: %v", err)
	}
}

func TestAdvancedBlendingExistingEmptyRestrictionAndNonRGBID(t *testing.T) {
	tags := compositeFixtureTag("brst", binary.BigEndian.AppendUint32(nil, 55))
	editor := compositeEditor(t, compositeFixture(2, 16, 3,
		[]compositeFixtureLayer{{color: []byte{20, 30, 40, 255}, tags: tags}}))
	current, err := editor.AdvancedBlending(nil, 1)
	if err != nil || !reflect.DeepEqual(current.ChannelRestrictions, []uint32{55}) {
		t.Fatalf("source channel IDs were normalized: %+v, %v", current, err)
	}
	if err := editor.SetAdvancedBlending(nil, 1,
		AdvancedBlendingOptions{ChannelRestrictions: []uint32{}}); err != nil {
		t.Fatal(err)
	}
	preview, err := editor.source.DecodeComposite(nil)
	if err != nil {
		t.Fatal(err)
	}
	saved := compositeSave(t, editor, SaveOptions{Composite: preview})
	current, err = advancedReopen(t, saved).AdvancedBlending(nil, 1)
	if err != nil || current.ChannelRestrictions == nil || len(current.ChannelRestrictions) != 0 ||
		saved.Header.Depth != 16 {
		t.Fatalf("empty existing brst or source precision was lost: %+v, %v", current, err)
	}
}

func TestAdvancedBlendingCancellationDuringReadAndCommit(t *testing.T) {
	base := compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{color: []byte{20, 30, 40, 255}, tags: advancedTestTags()},
	})
	for _, after := range []int{3, 9, 16, 17} {
		editor := compositeEditor(t, base)
		ctx := &rasterCancelContext{Context: context.Background(), after: after}
		if err := editor.SetAdvancedBlending(ctx, 1, advancedTestOptions()); !errors.Is(err, context.Canceled) ||
			editor.revision != 0 || editor.roots[0].advancedBlending != nil || len(editor.roots[0].additional) != 0 {
			t.Fatalf("in-progress cancellation at check %d was not atomic: %v", after, err)
		}
	}
}

func TestAdvancedBlendingNewGroupAndVirtualLayers(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		editor := compositeEditor(t, compositeFixture(version, 8, 3, nil))
		id := editor.roots[0].id
		if err := editor.SetAdvancedBlending(nil, id,
			AdvancedBlendingOptions{TransparencyShapes: new(false)}); err != nil {
			t.Fatal(err)
		}
		group, err := editor.AddGroup(0, 1, "advanced group", true)
		if err != nil {
			t.Fatal(err)
		}
		if err := editor.SetAdvancedBlending(nil, group,
			AdvancedBlendingOptions{BlendClipped: new(false), BlendInterior: new(false)}); err != nil {
			t.Fatal(err)
		}
		preview := image.NewNRGBA(image.Rect(0, 0, 1, 1))
		saved := compositeSave(t, editor, SaveOptions{CompositeImage: preview})
		reopened := advancedReopen(t, saved)
		layers, err := reopened.Layers()
		if err != nil || len(layers) != 2 {
			t.Fatalf("new virtual/group layers did not survive save: %v", err)
		}
		for _, layer := range layers {
			settings, err := reopened.AdvancedBlending(nil, layer.ID)
			if err != nil {
				t.Fatal(err)
			}
			if layer.Group {
				if settings.BlendClipped == nil || *settings.BlendClipped ||
					settings.BlendInterior == nil || *settings.BlendInterior {
					t.Fatal("new group advanced tags were lost")
				}
				continue
			}
			if settings.TransparencyShapes == nil || *settings.TransparencyShapes {
				t.Fatal("materialized virtual layer advanced tag was lost")
			}
		}
	}
}
