package psdedit

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestSmartTransformFloat32RequiresExactCoordinates(t *testing.T) {
	payload := smartEditPlacement(t, 0, "")
	bridge, _, err := smartFilterEditSource(t.Context(), payload, "SoLd", psdfile.DefaultLimits(), 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	placements, err := bridge.SmartObjects(nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := placements[0].Descriptor
	for index := range descriptor.Entries {
		entry := &descriptor.Entries[index]
		if entry.Key != "Trnf" && entry.Key != "nonAffineTransform" {
			continue
		}
		values := entry.Value.([]psdfile.DescriptorValue)
		for coordinate := range values {
			number := values[coordinate].Value.(float64)
			values[coordinate].Type = "UnFl"
			values[coordinate].Value = psdfile.DescriptorUnit{Unit: "#Pxl", Width: 32, Number: float32(number)}
		}
	}
	encoded, err := bridge.EncodeDescriptor(t.Context(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	encoded = append([]byte{'s', 'o', 'L', 'D', 0, 0, 0, 4}, encoded...)
	pixels := smartEditImage(smartEditColors())
	linked := writerLinkedEntry("liFD", "fixture-id", 7, smartEditPrototype(1, pixels))
	global := writerLinkedTag("8BIM", "lnk2", 4, linked.data, 4)
	tags := writerLinkedTag("8BIM", "SoLd", 4, encoded, 2)
	record, channels := smartEditLayer(1, "float32 placement", 21, image.Rect(0, 0, 2, 2), pixels, tags, false)
	layers := binary.BigEndian.AppendUint16(nil, 1)
	layers = append(layers, record...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	data := smartEditDocument(1, 6, 4, 3, layers, global, append([]byte{0, 0}, make([]byte, 72)...))
	editor, _ := smartEditOpen(t, data)
	before, _ := editor.Changes()
	if err := editor.SetSmartObjectTransform(nil, 1, smartTransformCorners(1.1, 0, 2, 2)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("inexact float32 coordinate accepted: %v", err)
	}
	after, _ := editor.Changes()
	if before != after || len(editor.roots[0].additional) != 0 {
		t.Fatal("rejected float32 edit partially changed placement state")
	}
	corners := smartTransformCorners(1.5, 0, 2, 2)
	if err := editor.SetSmartObjectTransform(nil, 1, corners); err != nil {
		t.Fatal(err)
	}
	saved := smartTransformRoundTrip(t, editor, 1)
	reopened, err := saved.SmartObjects(nil)
	if err != nil {
		t.Fatal(err)
	}
	for index, corner := range corners {
		if reopened[0].Transform[index*2] != corner.X || reopened[0].Transform[index*2+1] != corner.Y {
			t.Fatal("accepted float32 coordinates changed after saving and reopening")
		}
	}
}

func TestSmartTransformResolverPanicDoesNotCorruptEditorLock(t *testing.T) {
	editor, _ := smartEditOpen(t, linkedEditFixture(t, 1, 7, "lnkE"))
	before, _ := editor.Changes()
	panicValue := errors.New("controlled resolver panic")
	func() {
		defer func() {
			failure, valid := recover().(error)
			if !valid || !errors.Is(failure, panicValue) {
				t.Fatalf("resolver panic changed: %v", failure)
			}
		}()
		_ = editor.SetSmartObjectTransformWithOptions(t.Context(), 1, smartTransformCorners(1, 0, 2, 2),
			func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { panic(panicValue) },
			SmartTransformOptions{})
	}()
	after, err := editor.Changes()
	if err != nil || before != after {
		t.Fatalf("resolver panic changed state or left an unusable editor: %+v %v", after, err)
	}
	if err := editor.Rename(1, "still editable"); err != nil {
		t.Fatal(err)
	}
}
