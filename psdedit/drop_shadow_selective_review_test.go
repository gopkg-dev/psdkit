package psdedit

import (
	"bytes"
	"errors"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
)

type shadowSelectiveReviewReader struct {
	data      []byte
	forbidden []psdfile.Span
	hits      int
}

func (reader *shadowSelectiveReviewReader) ReadAt(target []byte, offset int64) (int, error) {
	for _, span := range reader.forbidden {
		if len(target) != 0 && offset < span.Offset+span.Length && offset+int64(len(target)) > span.Offset {
			reader.hits++
			return 0, errors.New("placed shadow preview accessed saved parent pixels")
		}
	}
	return bytes.NewReader(reader.data).ReadAt(target, offset)
}

func TestIndependentDropShadowEditorSelectiveSourceGuard(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		data := placedShadowEditFixture(t, version)
		reader := &shadowSelectiveReviewReader{data: data}
		source, err := psdfile.Read(reader, int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		editor, err := New(source)
		if err != nil {
			t.Fatal(err)
		}
		defer editor.Close()
		reader.forbidden = []psdfile.Span{source.Composite.Data}
		for _, channel := range source.Layers[0].Channels {
			if channel.ID >= -1 {
				reader.forbidden = append(reader.forbidden, channel.Data)
			}
		}
		infos, err := editor.Layers()
		if err != nil {
			t.Fatal(err)
		}
		if len(infos) != 1 {
			t.Fatal("one placed leaf required")
		}
		id, desired, _ := maskedOverlaySourceSet(t, editor)
		desired.Items[0].Distance, desired.Items[0].Size = .25, .5
		if err := editor.SetEffectsWithOptions(nil, id, *desired,
			EffectsEditOptions{ApproximateDropShadow: true}); err != nil {
			t.Fatal(err)
		}
		if err := editor.SetFillOpacity(infos[0].ID, 151); err != nil {
			t.Fatal(err)
		}
		live, err := editor.Composite(nil, psdcompose.Options{ApproximateDropShadow: true})
		if err != nil || reader.hits != 0 {
			t.Fatalf("live preview read parent cache/footer: %d / %v", reader.hits, err)
		}
		// Serialization must legitimately copy original encoded channels; the guard covers painting only.
		reader.forbidden = nil
		var saved bytes.Buffer
		if err := editor.WritePSD(nil, &saved, SaveOptions{ApproximateDropShadow: true}); err != nil {
			t.Fatal(err)
		}
		reopened, err := psdfile.Parse(saved.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		copyEditor, err := New(reopened)
		if err != nil {
			t.Fatal(err)
		}
		defer copyEditor.Close()
		actual, err := copyEditor.Composite(nil, psdcompose.Options{ApproximateDropShadow: true})
		if err != nil || !bytes.Equal(actual.Pix, live.Pix) {
			t.Fatalf("save/reopen changed live projection: %v", err)
		}
	}
}
