package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"sync"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func p3ExpectRGBA(t *testing.T, pixels *image.NRGBA, expected []byte) {
	t.Helper()
	if pixels == nil || pixels.Rect != image.Rect(0, 0, 1, 1) || !bytes.Equal(pixels.Pix, expected) {
		t.Fatalf("edited RGBA = %#v, want %v", pixels, expected)
	}
}

func TestP3EditorMultiplyRebuildsPreviewAndLeavesSourceOwned(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		editor := compositeEditor(t, compositeFixture(version, 8, 3, []compositeFixtureLayer{
			{color: []byte{100, 200, 80, 255}},
			{color: []byte{64, 128, 255, 255}, blend: "mul "},
		}))
		before, err := editor.source.ReadSpan(editor.source.Source)
		if err != nil {
			t.Fatal(err)
		}
		if err := editor.SetOpacity(2, 128); err != nil {
			t.Fatal(err)
		}
		changes, err := editor.Changes()
		if err != nil {
			t.Fatal(err)
		}
		pixels, err := editor.Composite(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		p3ExpectRGBA(t, pixels, []byte{62, 150, 80, 255})
		clear(pixels.Pix)
		pixels, err = editor.Composite(nil)
		if err != nil {
			t.Fatal(err)
		}
		p3ExpectRGBA(t, pixels, []byte{62, 150, 80, 255})
		afterChanges, err := editor.Changes()
		if err != nil || afterChanges != changes {
			t.Fatalf("preview changed editor revision: %#v, %v", afterChanges, err)
		}
		compositeExpect(t, compositeSave(t, editor, SaveOptions{Compression: 3}), []byte{62, 150, 80})
		after, err := editor.source.ReadSpan(editor.source.Source)
		if err != nil || !bytes.Equal(after, before) {
			t.Fatalf("render or save changed source bytes: %v", err)
		}
	}
}

func TestP3EditorIsolatedGroupOpacityMaskAndMove(t *testing.T) {
	editor := compositeEditor(t, compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{color: []byte{0, 0, 255, 255}},
		{divider: 3},
		{color: []byte{255, 0, 0, 255}},
		{color: []byte{0, 255, 0, 128}},
		{color: []byte{0, 0, 0, 0}, divider: 1, blend: "norm", mask: []byte{128}},
	}))
	if err := editor.SetOpacity(5, 128); err != nil {
		t.Fatal(err)
	}
	pixels, err := editor.Composite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p3ExpectRGBA(t, pixels, []byte{32, 32, 191, 255})
	compositeExpect(t, compositeSave(t, editor), []byte{32, 32, 191})
	if err := editor.Move(4, 0, 2); err != nil {
		t.Fatal(err)
	}
	pixels, err = editor.Composite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p3ExpectRGBA(t, pixels, []byte{32, 128, 95, 255})
	compositeExpect(t, compositeSave(t, editor), []byte{32, 128, 95})
}

func p3ClippingFixture(t *testing.T) []byte {
	t.Helper()
	data := compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{color: []byte{255, 0, 0, 128}},
		{color: []byte{0, 255, 0, 128}},
	})
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	record, err := source.ReadSpan(source.Layers[1].Span)
	if err != nil {
		t.Fatal(err)
	}
	signature := bytes.Index(record, []byte("8BIMnorm"))
	if signature < 0 {
		t.Fatal("independent clipping fixture has no blend signature")
	}
	clippingOffset := source.Layers[1].Span.Offset + int64(signature) + 9
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	data[clippingOffset] = 1
	return data
}

func TestP3EditorClippingAndRawOverlayUseBaseCoverageOnce(t *testing.T) {
	editor := compositeEditor(t, p3ClippingFixture(t))
	if err := editor.ReplaceChannel(2, 1, psdfile.Plane{
		ID: 1, Width: 1, Height: 1, Depth: 8, RowBytes: 1, Data: []byte{255},
	}); err != nil {
		t.Fatal(err)
	}
	pixels, err := editor.Composite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p3ExpectRGBA(t, pixels, []byte{127, 128, 0, 128})
	compositeExpect(t, compositeSave(t, editor), []byte{191, 191, 127, 128})
	if editor.roots[1].planes[1].Data[0] != 255 || editor.source.Layers[1].Clipping != 1 {
		t.Fatal("clipping render changed overlays or source metadata")
	}
}

func TestP3EditorEmptyTreeAndHiddenUnsupportedCache(t *testing.T) {
	editor := compositeEditor(t, compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{color: []byte{255, 0, 0, 255}},
		{color: []byte{0, 255, 0, 255}, blend: "abcd"},
	}))
	if err := editor.SetVisible(2, false); err != nil {
		t.Fatal(err)
	}
	pixels, err := editor.Composite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p3ExpectRGBA(t, pixels, []byte{255, 0, 0, 255})
	if err := editor.Remove(1); err != nil {
		t.Fatal(err)
	}
	if err := editor.Remove(2); err != nil {
		t.Fatal(err)
	}
	pixels, err = editor.Composite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p3ExpectRGBA(t, pixels, []byte{0, 0, 0, 0})
}

func TestP3EditorBlendIfAndMaskDensityKeepTypedSourceSemantics(t *testing.T) {
	data := compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{color: []byte{255, 255, 255, 255}},
		{color: []byte{64, 64, 64, 255}},
	})
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	record := source.Layers[1]
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []int64{source.LayerMaskSection.Offset, source.LayerInfo.Offset, record.Extra.Offset - 4} {
		length := binary.BigEndian.Uint32(data[prefix:])
		binary.BigEndian.PutUint32(data[prefix:], length+8)
	}
	binary.BigEndian.PutUint32(data[record.BlendRanges.Offset:], 8)
	insertion := record.BlendRanges.Offset + 4
	withRanges := append([]byte{}, data[:insertion]...)
	withRanges = append(withRanges, 0, 128, 255, 255, 0, 0, 255, 255)
	withRanges = append(withRanges, data[insertion:]...)
	editor := compositeEditor(t, withRanges)
	if err := editor.ReplaceChannel(2, 0, psdfile.Plane{
		ID: 0, Width: 1, Height: 1, Depth: 8, RowBytes: 1, Data: []byte{64},
	}); err != nil {
		t.Fatal(err)
	}
	pixels, err := editor.Composite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Photoshop's inclusive split ramp gives coverage 65/129 at gray 64, so 255-191*(65/129) rounds to 159.
	p3ExpectRGBA(t, pixels, []byte{159, 159, 159, 255})
	saved := compositeSave(t, editor)
	compositeExpect(t, saved, []byte{159, 159, 159})
	ranges, err := saved.ReadSpan(saved.Layers[1].BlendRanges)
	if err != nil || !bytes.Equal(ranges[4:], []byte{0, 128, 255, 255, 0, 0, 255, 255}) {
		t.Fatalf("raw Blend If sliders changed: %v, %v", ranges, err)
	}

	maskData := compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{color: []byte{255, 0, 0, 255}, mask: []byte{64}},
	})
	maskSource, err := psdfile.Parse(maskData)
	if err != nil {
		t.Fatal(err)
	}
	maskOffset := maskSource.Layers[0].Mask.Span.Offset
	if err := maskSource.Close(); err != nil {
		t.Fatal(err)
	}
	// Reuse the two standard padding bytes as parameter flags and user density.
	maskData[maskOffset+17], maskData[maskOffset+18], maskData[maskOffset+19] = 20, 1, 128
	masked := compositeEditor(t, maskData)
	if err := masked.ReplaceChannel(1, -2, psdfile.Plane{
		ID: -2, Width: 1, Height: 1, Depth: 8, RowBytes: 1, Data: []byte{64},
	}); err != nil {
		t.Fatal(err)
	}
	pixels, err = masked.Composite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p3ExpectRGBA(t, pixels, []byte{255, 0, 0, 223})
	compositeExpect(t, compositeSave(t, masked), []byte{255, 32, 32, 223})
}

type p3CountingReader struct {
	reader *bytes.Reader
	calls  int
}

func (reader *p3CountingReader) ReadAt(data []byte, offset int64) (int, error) {
	if len(data) != 0 {
		reader.calls++
	}
	return reader.reader.ReadAt(data, offset)
}

func TestP3EditorPreviewMapsFeatureAndBudgetErrorsAndCancellation(t *testing.T) {
	data := compositeFixture(1, 8, 3, []compositeFixtureLayer{{color: []byte{255, 0, 0, 255}}})
	reader := &p3CountingReader{reader: bytes.NewReader(data)}
	source, err := psdfile.Read(reader, int64(len(data)), psdfile.LoadOptions{
		Limits: psdfile.Limits{MaxDecodeBytes: 64},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	editor, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = editor.Close() })
	reader.calls = 0
	if _, err := editor.Composite(context.Background()); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("composite ignored aggregate source and render working budget: %v", err)
	}
	if reader.calls != 0 {
		t.Fatal("known insufficient composition budget triggered source decoding")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := editor.Composite(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preview: %v", err)
	}
	unsupported := compositeEditor(t, compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{color: []byte{255, 0, 0, 255}, blend: "abcd"},
	}))
	if err := unsupported.SetOpacity(1, 128); err != nil {
		t.Fatal(err)
	}
	_, err = unsupported.Composite(context.Background())
	if !errors.Is(err, ErrUnsupported) || !errors.Is(err, psdcompose.ErrUnsupported) {
		t.Fatalf("composite lost editor or renderer error category: %v", err)
	}
	var output bytes.Buffer
	if err := unsupported.Write(context.Background(), &output); !errors.Is(err, ErrUnsupported) || output.Len() != 0 {
		t.Fatalf("unsupported preview wrote partial file: %v, bytes=%d", err, output.Len())
	}
	if err := unsupported.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := unsupported.Composite(context.Background()); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed editor preview: %v", err)
	}
}

func TestP3EditorConcurrentPreviewsReturnIndependentBuffers(t *testing.T) {
	editor := compositeEditor(t, compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{color: []byte{100, 200, 80, 255}},
		{color: []byte{64, 128, 255, 255}, blend: "mul "},
	}))
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			pixels, err := editor.Composite(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			if !bytes.Equal(pixels.Pix, []byte{25, 100, 80, 255}) {
				t.Errorf("concurrent preview = %v", pixels.Pix)
			}
			clear(pixels.Pix)
		})
	}
	group.Wait()
}
