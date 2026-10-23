package psdedit

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestReadSmartObjectContentsOwnsCurrentSharedSource(t *testing.T) {
	editor, source := smartEditOpen(t, smartEditFixture(t, 1, ""))
	objects, err := editor.SmartObjects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	records, err := source.LinkedFiles(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var expected []byte
	for _, record := range records {
		if record.ID == objects[0].SourceID {
			expected, err = source.ReadSpan(record.Data)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	read, err := editor.ReadSmartObjectContents(t.Context(), objects[0].ID, nil)
	if err != nil || !bytes.Equal(read, expected) {
		t.Fatalf("embedded source bytes differ: %v", err)
	}
	clear(read)
	read, err = editor.ReadSmartObjectContents(t.Context(), objects[1].ID, nil)
	if err != nil || !bytes.Equal(read, expected) {
		t.Fatalf("source query exposed shared storage: %v", err)
	}
	encoded, _, _ := smartFormatsEncoded(t, "png", 2, 2)
	if err := editor.ReplaceContents(t.Context(), objects[0].ID, encoded); err != nil {
		t.Fatal(err)
	}
	read, err = editor.ReadSmartObjectContents(t.Context(), objects[1].ID, nil)
	if err != nil || !bytes.Equal(read, encoded) {
		t.Fatalf("shared raster source is not the current original-format bytes: %v", err)
	}
	clear(read)
	read, err = editor.ReadSmartObjectContents(t.Context(), objects[0].ID, nil)
	if err != nil || !bytes.Equal(read, encoded) {
		t.Fatalf("overlay query exposed editor storage: %v", err)
	}
	if _, err := editor.OpenSmartObjectSource(t.Context(), objects[0].ID, nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("raster contents were misrepresented as a PSD document: %v", err)
	}
	if err := editor.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read, encoded) {
		t.Fatal("owned source bytes did not survive editor close")
	}
}

func TestReadSmartObjectContentsExternalAccessAndStaleQueries(t *testing.T) {
	for _, scenario := range []string{"success", "missing", "error", "cancel", "edit", "close"} {
		t.Run(scenario, func(t *testing.T) {
			editor, _ := smartEditOpen(t, linkedEditFixture(t, 2, 7, "lnkE"))
			objects, err := editor.SmartObjects(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			encoded, _, _ := smartFormatsEncoded(t, "jpeg", 2, 2)
			callerError := errors.New("controlled resolver read error")
			resolver := LinkedContentsResolver(func(ctx context.Context, record psdfile.LinkedFileRecord) ([]byte, error) {
				if record.Kind != "liFE" {
					t.Fatal("resolver did not receive the selected external record")
				}
				if _, err := editor.Changes(); err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "error":
					return nil, callerError
				case "cancel":
					cancel()
				case "edit":
					if err := editor.Rename(objects[0].ID, "changed during read"); err != nil {
						t.Fatal(err)
					}
				case "close":
					if err := editor.Close(); err != nil {
						t.Fatal(err)
					}
				}
				return encoded, nil
			})
			if scenario == "missing" {
				resolver = nil
			}
			read, err := editor.ReadSmartObjectContents(ctx, objects[0].ID, resolver)
			want := map[string]error{"missing": ErrUnsupported, "error": callerError,
				"cancel": context.Canceled, "edit": ErrStaleLinkedEdit, "close": psdfile.ErrClosed}[scenario]
			if !errors.Is(err, want) {
				t.Fatalf("source query error = %v, want %v", err, want)
			}
			if scenario == "success" {
				expected := bytes.Clone(encoded)
				clear(encoded)
				if !bytes.Equal(read, expected) || editor.revision != 0 || len(editor.linkedOverlays) != 0 {
					t.Fatal("external read borrowed resolver bytes or changed the editor")
				}
			} else if read != nil {
				t.Fatal("failed query returned partial source bytes")
			}
		})
	}
}

func TestReadSmartObjectContentsBudgetAndCancellation(t *testing.T) {
	editor := compositeEditor(t, linkedEditFixture(t, 1, 7, "lnkE"),
		psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 64 << 10}})
	objects, err := editor.SmartObjects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	resolver := func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
		return make([]byte, 64<<10), nil
	}
	if _, err := editor.ReadSmartObjectContents(t.Context(), objects[0].ID, resolver); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("owned query ignored metadata and copied source peak budget: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := editor.ReadSmartObjectContents(ctx, objects[0].ID, resolver); !errors.Is(err, context.Canceled) {
		t.Fatalf("source query ignored cancellation: %v", err)
	}
	if editor.revision != 0 || len(editor.linkedOverlays) != 0 {
		t.Fatal("failed source queries changed editing state")
	}
}
