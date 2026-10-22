package psdedit

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func gatePlacement(t *testing.T, left int, custom bool, projective bool) []byte {
	t.Helper()
	quad := []float64{float64(left), 0, float64(left + 2), 0,
		float64(left + 2), 2, float64(left), 2}
	if projective {
		quad[6] += .25
	}
	items := make([]psdfile.DescriptorValue, len(quad))
	for index, value := range quad {
		items[index] = psdfile.DescriptorValue{Type: "doub", Value: value}
	}
	descriptor := &psdfile.Descriptor{ClassID: "null", Entries: []psdfile.DescriptorEntry{
		{Key: "Idnt", Type: "TEXT", Value: "fixture-id"},
		{Key: "Type", Type: "long", Value: int32(2)},
		{Key: "Sz  ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "size", Entries: []psdfile.DescriptorEntry{
			{Key: "Wdth", Type: "doub", Value: float64(2)}, {Key: "Hght", Type: "doub", Value: float64(2)},
		}}},
		{Key: "Trnf", Type: "VlLs", Value: items},
	}}
	if custom {
		var horizontal, vertical []float64
		for row := range 4 {
			for column := range 4 {
				horizontal = append(horizontal, float64(column)*2/3)
				vertical = append(vertical, float64(row)*2/3)
			}
		}
		mesh := &psdfile.DescriptorObjectArray{ClassID: "rationalPoint", Prefix: 16,
			Entries: []psdfile.DescriptorEntry{
				{Key: "Hrzn", Type: "UnFl", Value: psdfile.DescriptorUnitArray{Unit: "#Pxl", Numbers: horizontal}},
				{Key: "Vrtc", Type: "UnFl", Value: psdfile.DescriptorUnitArray{Unit: "#Pxl", Numbers: vertical}},
			}}
		warp := &psdfile.Descriptor{ClassID: "warp", Entries: []psdfile.DescriptorEntry{
			{Key: "warpStyle", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "warpStyle", ValueID: "warpCustom"}},
			{Key: "warpValue", Type: "doub", Value: float64(0)},
			{Key: "warpPerspective", Type: "doub", Value: float64(0)},
			{Key: "warpPerspectiveOther", Type: "doub", Value: float64(0)},
			{Key: "warpRotate", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "Ornt", ValueID: "Hrzn"}},
			{Key: "uOrder", Type: "long", Value: int32(4)}, {Key: "vOrder", Type: "long", Value: int32(4)},
			{Key: "bounds", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "classFloatRect",
				Entries: []psdfile.DescriptorEntry{
					{Key: "Left", Type: "doub", Value: float64(0)}, {Key: "Top ", Type: "doub", Value: float64(0)},
					{Key: "Rght", Type: "doub", Value: float64(2)}, {Key: "Btom", Type: "doub", Value: float64(2)},
				}}},
			{Key: "customEnvelopeWarp", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "customEnvelopeWarp",
				Entries: []psdfile.DescriptorEntry{{Key: "meshPoints", Type: "ObAr", Value: mesh}}}},
		}}
		descriptor.Entries = append(descriptor.Entries, psdfile.DescriptorEntry{Key: "warp", Type: "Objc", Value: warp})
	}
	encoded, err := psdfile.EncodeDescriptor(nil, descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	return append(binary.BigEndian.AppendUint32([]byte("soLD"), 4), encoded...)
}

func gateSmartFixture(t *testing.T, version uint16, custom bool, projective bool) []byte {
	t.Helper()
	pixels := smartEditImage(smartEditColors())
	prototype := smartEditPrototype(version, pixels)
	linked := writerLinkedEntry("liFD", "fixture-id", 7, prototype)
	width := 4
	if version == 2 {
		width = 8
		copy(linked.body[linked.sizeOffset-8:linked.sizeOffset-4], "8BPB")
		copy(linked.data[linked.sizeOffset:linked.sizeOffset+4], "8BPB")
	}
	global := writerLinkedTag("8BIM", "lnk2", width, linked.data, 4)
	var records, channels []byte
	for index, left := range []int{0, 4} {
		tags := writerLinkedTag("8BIM", "SoLd", 4, gatePlacement(t, left, custom, projective && index == 1), 2)
		record, payloads := smartEditLayer(version, "Instance", uint32(10+index),
			image.Rect(left, 0, left+2, 2), pixels, tags, false)
		records, channels = append(records, record...), append(channels, payloads...)
	}
	layers := binary.BigEndian.AppendUint16(nil, 2)
	layers = append(layers, records...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	composite := append([]byte{0, 0}, bytes.Repeat([]byte{7}, 6*2*3)...)
	return smartEditDocument(version, 6, 2, 3, layers, global, composite)
}

func TestCustomProjectiveGateReplaceContentsAtomicAndOriginalSave(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			original := gateSmartFixture(t, version, true, true)
			digest := sha256.Sum256(original)
			editor, _ := smartEditOpen(t, original)
			var untouched bytes.Buffer
			if err := editor.Write(nil, &untouched); err != nil || !bytes.Equal(original, untouched.Bytes()) {
				t.Fatalf("unmodified projective metadata save changed source bytes: %v", err)
			}
			infos, err := editor.SmartObjects(nil)
			if err != nil || len(infos) != 2 {
				t.Fatalf("projective source metadata became unreadable: %v", err)
			}
			if err := editor.Rename(infos[0].ID, "Kept rename"); err != nil {
				t.Fatal(err)
			}
			var prior bytes.Buffer
			if err := editor.Write(nil, &prior); err != nil {
				t.Fatal(err)
			}
			revision, owned := editor.revision, editor.ownedBytesLocked(nil)
			err = editor.ReplaceContents(nil, infos[0].ID,
				smartEditPrototype(version, smartEditImage(smartEditColors())))
			if !errors.Is(err, ErrUnsupported) || !errors.Is(err, psdwarp.ErrUnsupported) {
				t.Fatalf("replacement bypassed the default projective gate: %v", err)
			}
			if editor.revision != revision || editor.ownedBytesLocked(nil) != owned {
				t.Fatal("rejected shared replacement changed revision or retained ownership")
			}
			var after bytes.Buffer
			if err := editor.Write(nil, &after); err != nil || !bytes.Equal(prior.Bytes(), after.Bytes()) {
				t.Fatalf("rejected replacement changed previous rename transaction: %v", err)
			}
			if sha256.Sum256(original) != digest {
				t.Fatal("replacement or save changed caller-owned source bytes")
			}
		})
	}
}

func TestCustomProjectiveGateSceneAndOrdinaryPerspectiveRoutes(t *testing.T) {
	for _, test := range []struct {
		name       string
		custom     bool
		projective bool
		wantReject bool
	}{
		{"customProjective", true, true, true},
		{"customAffine", true, false, false},
		{"ordinaryProjectiveWithoutWarp", false, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := gateSmartFixture(t, 1, test.custom, test.projective)
			editor, source := smartEditOpen(t, original)
			full, err := psdscene.Render(nil, source)
			if test.wantReject {
				if full != nil || !errors.Is(err, psdwarp.ErrUnsupported) {
					t.Fatalf("scene rendered unverified Warp perspective: %v", err)
				}
			} else if full == nil || err != nil {
				t.Fatalf("supported route was rejected: %v", err)
			}
			tile, err := psdscene.RenderTile(nil, source, image.Rect(0, 0, 6, 2))
			if test.wantReject {
				if tile != nil || !errors.Is(err, psdwarp.ErrUnsupported) {
					t.Fatalf("tile rendered unverified Warp perspective: %v", err)
				}
			} else if tile == nil || err != nil {
				t.Fatalf("supported tile route was rejected: %v", err)
			}
			if !test.wantReject {
				infos, err := editor.SmartObjects(nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := editor.ReplaceContents(nil, infos[0].ID,
					smartEditPrototype(1, smartEditImage(smartEditColors()))); err != nil {
					t.Fatalf("supported content replacement route was rejected: %v", err)
				}
			}
		})
	}
}
