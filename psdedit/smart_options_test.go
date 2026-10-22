package psdedit

import (
	"bytes"
	"errors"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestSmartContentsExplicitPolicyPreservesDefaultGate(t *testing.T) {
	original := gateSmartFixture(t, 1, true, true)
	source, err := psdfile.Parse(original)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	editor, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	defer editor.Close()
	objects, err := editor.SmartObjects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	contents := smartEditPrototype(1, smartEditImage(smartEditColors()))
	revision := editor.revision
	if err := editor.ReplaceContentsWithOptions(t.Context(), objects[0].ID, contents, SmartContentsOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Fatal("default gate changed", err)
	}
	if editor.revision != revision {
		t.Fatal("rejected edit changed revision")
	}
	if err := editor.ReplaceContentsWithOptions(t.Context(), objects[0].ID, contents, SmartContentsOptions{ApproximateCustomWarp: true}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := editor.WritePSD(t.Context(), &output); err != nil {
		t.Fatal(err)
	}
	if _, err := psdfile.Parse(output.Bytes()); err != nil {
		t.Fatal(err)
	}
}
