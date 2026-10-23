package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func artboardAutoRejected(t *testing.T, editor *Editor, cause error, explicit bool) {
	t.Helper()
	before, _ := editor.Changes()
	if pixels, err := editor.Composite(t.Context()); pixels != nil || !errors.Is(err, cause) {
		t.Fatalf("unsupported artboard rendering returned pixels or wrong error: %v", err)
	}
	options := SaveOptions{}
	if explicit {
		options.CompositeImage = image.NewNRGBA(image.Rect(0, 0,
			int(editor.source.Header.Width), int(editor.source.Header.Height)))
	}
	var output bytes.Buffer
	if err := editor.Write(t.Context(), &output, options); !errors.Is(err, cause) || output.Len() != 0 {
		t.Fatalf("rejected artboard save wrote bytes or lost error: %v", err)
	}
	path := filepath.Join(t.TempDir(), "preserve-existing.psd")
	marker := []byte("keep original destination")
	if err := os.WriteFile(path, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := editor.Save(t.Context(), path, options); !errors.Is(err, cause) {
		t.Fatalf("invalid artboard Save returned wrong error: %v", err)
	}
	retained, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(retained, marker) {
		t.Fatalf("failed artboard Save replaced its destination: %v", err)
	}
	if after, _ := editor.Changes(); after != before {
		t.Fatal("failed Composite/Write/Save changed the committed editor revision")
	}
}

func TestArtboardAutoRejectsReactivatedOwnStateWithoutPartialOutput(t *testing.T) {
	for _, cause := range []string{"pixel-mask", "vector-mask", "effects"} {
		t.Run(cause, func(t *testing.T) {
			data := editableDisabledArtboardFixture(t, 1)
			source, err := psdfile.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			switch cause {
			case "pixel-mask":
				data[source.Layers[2].Mask.Span.Offset+17] &^= 2
			case "vector-mask":
				for _, block := range source.Layers[2].AdditionalInfo {
					if block.Key == "vmsk" {
						binary.BigEndian.PutUint32(data[block.Data.Offset+4:], 0)
					}
				}
			case "effects":
				passRangeEffectMaster(t, data, true)
			}
			_ = source.Close()
			editor := compositeEditor(t, data)
			if err := editor.SetOpacity(2, 128); err != nil {
				t.Fatal(err)
			}
			artboardAutoRejected(t, editor, ErrUnsupported, false)
		})
	}
}

func TestArtboardAutoRejectsUnsupportedBoardLifecycleEvenWithExplicitPreview(t *testing.T) {
	for _, cause := range []string{"removed", "nested", "board-opacity", "board-blend"} {
		t.Run(cause, func(t *testing.T) {
			editor := compositeEditor(t, artboardAutoFixture(t, 2, 1, 7))
			board := artboardAutoLayer(t, editor, "left-board")
			switch cause {
			case "removed":
				if err := editor.Remove(board); err != nil {
					t.Fatal(err)
				}
			case "nested":
				container, err := editor.AddGroup(0, 3, "parent", true)
				if err != nil {
					t.Fatal(err)
				}
				if err := editor.Move(board, container, 0); err != nil {
					t.Fatal(err)
				}
			case "board-opacity":
				if err := editor.SetOpacity(board, 128); err != nil {
					t.Fatal(err)
				}
			case "board-blend":
				if err := editor.SetBlendMode(board, "mul "); err != nil {
					t.Fatal(err)
				}
			}
			artboardAutoRejected(t, editor, ErrUnsupported, cause == "removed" || cause == "nested")
		})
	}
}

func TestArtboardAutoCancellationBudgetsAndSourceReadFailureAreAtomic(t *testing.T) {
	editor, source, reader := compositeLayerOnlyEditor(t, artboardAutoFixture(t, 1, 1, 9))
	if err := editor.SetOpacity(artboardAutoLayer(t, editor, "left-child"), 127); err != nil {
		t.Fatal(err)
	}
	before, _ := editor.Changes()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if pixels, err := editor.Composite(ctx); pixels != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled artboard Composite returned pixels: %v", err)
	}
	var output bytes.Buffer
	if err := editor.Write(ctx, &output); !errors.Is(err, context.Canceled) || output.Len() != 0 {
		t.Fatalf("canceled artboard Write published bytes: %v", err)
	}
	if pixels, err := editor.Composite(t.Context(), psdcompose.Options{MaxWorkingBytes: 1}); pixels != nil || !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("automatic artboard Composite ignored memory budget: %v", err)
	}
	if err := editor.Write(t.Context(), &output, SaveOptions{MaxOutputBytes: 1}); !errors.Is(err, psdfile.ErrLimit) || output.Len() != 0 {
		t.Fatalf("artboard output limit published a partial file: %v", err)
	}
	if after, _ := editor.Changes(); after != before {
		t.Fatal("canceled or over-budget automatic render changed editor state")
	}
	reader.failure, reader.failureCause = source.Layers[1].Channels[0].Data, io.ErrClosedPipe
	artboardAutoRejected(t, editor, io.ErrClosedPipe, false)
	reader.failureCause = nil
	if _, err := editor.Composite(t.Context()); err != nil {
		t.Fatalf("editor did not recover after source access resumed: %v", err)
	}
}
