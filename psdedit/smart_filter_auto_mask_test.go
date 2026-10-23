package psdedit

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
)

func smartFilterAutoMaskPayload() []byte {
	var channels []byte
	for _, value := range []uint32{0, 0, 2, 2, 8, 0, 0, 0} {
		channels = binary.BigEndian.AppendUint32(channels, value)
	}
	identifier := "auto-mask"
	record := append([]byte{byte(len(identifier))}, []byte(identifier)...)
	record = binary.BigEndian.AppendUint32(record, 1)
	record = binary.BigEndian.AppendUint64(record, uint64(len(channels)))
	record = append(record, channels...)
	record = append(record, 1)
	for _, value := range []uint32{0, 0, 2, 2} {
		record = binary.BigEndian.AppendUint32(record, value)
	}
	record = binary.BigEndian.AppendUint64(record, 6)
	record = append(record, 0, 0, 0, 255, 128, 64)
	payload := binary.BigEndian.AppendUint32(nil, 3)
	payload = binary.BigEndian.AppendUint64(payload, uint64(len(record)))
	payload = append(payload, record...)
	return append(payload, make([]byte, (4-len(record)%4)%4)...)
}

func smartFilterAutoMaskFixture(t *testing.T, version uint16) []byte {
	t.Helper()
	source, err := psdfile.Parse(exactSmartFilterFixture(t, version))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	placements, err := source.SmartObjects(t.Context())
	if err != nil || len(placements) != 1 {
		t.Fatalf("mask fixture placement: %v", err)
	}
	descriptor := placements[0].Descriptor
	descriptor.Entries = append(descriptor.Entries,
		psdfile.DescriptorEntry{Key: "placed", Type: "TEXT", Value: "auto-mask"})
	stack := gradientObject(descriptor, "filterFX")
	gradientFind(stack, "filterMaskEnable").Value = true
	encoded, err := source.EncodeDescriptor(t.Context(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	original, err := source.ReadSpan(placements[0].Block.Data)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(bytes.Clone(original[:8]), encoded...)
	payload = append(payload, original[len(original)-3:]...)
	poisoned := exactSmartFilterGrayPixels([4]byte{17, 17, 17, 17})
	record, channels := smartEditLayer(version, "masked-auto-filter", 20, poisoned.Rect, poisoned,
		writerLinkedTag("8BIM", "SoLd", 4, payload, 2), false)
	layers := binary.BigEndian.AppendUint16(nil, 1)
	layers = append(layers, record...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	global, err := source.ReadSpan(source.AdditionalInfo[0].Span)
	if err != nil {
		t.Fatal(err)
	}
	lengthWidth := 4
	if version == 2 {
		lengthWidth = 8
	}
	global = append(global, writerLinkedTag("8BIM", "FXid", lengthWidth, smartFilterAutoMaskPayload(), 4)...)
	return smartEditDocument(version, 2, 2, 3, layers, global,
		append([]byte{0, 0}, bytes.Repeat([]byte{23}, 12)...))
}

func TestSmartFilterAutoRetainsAndAppliesOriginalFilterMask(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			editor := compositeEditor(t, smartFilterAutoMaskFixture(t, version))
			stack := smartFilterAutoExactStack(t, editor.source)
			original := editor.source
			for step, lightness := range []int16{51, -97} {
				stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = lightness
				if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); err != nil {
					t.Fatalf("automatic edit did not resolve retained filter-mask identity: %v", err)
				}
				// Exact source-gray transfer followed by the mask's independently known 0/255/128/64 weights.
				values := [4]byte{0, 161, 161, 255}
				if step != 0 {
					values = [4]byte{0, 2, 66, 193}
				}
				reopened, saved := smartFilterAutoSaved(t, editor, compression,
					exactSmartFilterGrayPixels(values), psdfilter.Options{}, step != 0)
				if len(saved.AdditionalInfo) != len(original.AdditionalInfo) {
					t.Fatal("automatic filter edit changed original global block count")
				}
				for index, block := range original.AdditionalInfo {
					filterEditAssertSpan(t, original, saved, block.Span, saved.AdditionalInfo[index].Span)
				}
				editor = reopened
			}
		}
	}
}
