package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcolor"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func sceneCostReviewProfile(samples int) []byte {
	keys := []string{"wtpt", "rXYZ", "gXYZ", "bXYZ", "rTRC", "gTRC", "bTRC"}
	data := make([]byte, 132+len(keys)*12)
	data[8] = 4
	copy(data[12:], "mntrRGB XYZ ")
	copy(data[36:], "acsp")
	for index, value := range []uint32{63190, 65536, 54061} {
		binary.BigEndian.PutUint32(data[68+index*4:], value)
	}
	binary.BigEndian.PutUint32(data[128:], uint32(len(keys)))
	for index, key := range keys {
		copy(data[132+index*12:], key)
	}
	for index := range 4 {
		tag := make([]byte, 20)
		copy(tag, "XYZ ")
		if index == 0 {
			for component, value := range []uint32{63190, 65536, 54061} {
				binary.BigEndian.PutUint32(tag[8+component*4:], value)
			}
		} else {
			binary.BigEndian.PutUint32(tag[8+(index-1)*4:], 65536)
		}
		binary.BigEndian.PutUint32(data[132+index*12+4:], uint32(len(data)))
		binary.BigEndian.PutUint32(data[132+index*12+8:], uint32(len(tag)))
		data = append(data, tag...)
	}
	position := len(data)
	curve := make([]byte, 12+samples*2)
	copy(curve, "curv")
	binary.BigEndian.PutUint32(curve[8:], uint32(samples))
	for index := range samples {
		value := uint16(256)
		if samples > 1 {
			value = uint16(uint64(index) * 65535 / uint64(samples-1))
		}
		binary.BigEndian.PutUint16(curve[12+index*2:], value)
	}
	data = append(data, curve...)
	for len(data)%4 != 0 {
		data = append(data, 0)
	}
	for index := 4; index < len(keys); index++ {
		binary.BigEndian.PutUint32(data[132+index*12+4:], uint32(position))
		binary.BigEndian.PutUint32(data[132+index*12+8:], uint32(len(curve)))
	}
	binary.BigEndian.PutUint32(data, uint32(len(data)))
	return data
}

func sceneCostReviewSource(t *testing.T, profile []byte) (*psdfile.Document, []byte) {
	t.Helper()
	resource := []byte("8BIM")
	resource = binary.BigEndian.AppendUint16(resource, 1039)
	resource = append(resource, 0, 0)
	resource = binary.BigEndian.AppendUint32(resource, uint32(len(profile)))
	resource = append(resource, profile...)
	if len(profile)%2 != 0 {
		resource = append(resource, 0)
	}
	data := binary.BigEndian.AppendUint16([]byte("8BPS"), 1)
	data = append(data, make([]byte, 6)...)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = binary.BigEndian.AppendUint32(data, 1)
	data = binary.BigEndian.AppendUint32(data, 1)
	data = binary.BigEndian.AppendUint16(data, 8)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = binary.BigEndian.AppendUint32(data, 0)
	data = binary.BigEndian.AppendUint32(data, uint32(len(resource)))
	data = append(data, resource...)
	data = binary.BigEndian.AppendUint32(data, 0)
	data = append(data, 0, 0, 64, 128, 192)
	source, err := psdfile.Read(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	return source, data
}

func TestReviewSceneProfilesChargeBothOwnedInputCopies(t *testing.T) {
	sourceProfile, destination := sceneCostReviewProfile(500_000), sceneCostReviewProfile(1)
	source, data := sceneCostReviewSource(t, sourceProfile)
	before, beforeDestination := sha256.Sum256(data), sha256.Sum256(destination)
	input := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	input.SetNRGBA(0, 0, color.NRGBA{R: 64, G: 128, B: 192, A: 255})
	estimate, err := psdcolor.ProfileTransformWorkingBytes(nil, input, sourceProfile, destination)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := source.RGB8SnapshotMetadataBytesContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The conversion estimate excludes borrowed inputs, but Scene owns both profile buffers and input pixels.
	budget := metadata + 4 + int64(len(sourceProfile)+len(destination)) + estimate
	result, err := Render(nil, source, Options{OutputProfile: destination, MaxWorkingBytes: budget})
	if err != nil || result.NRGBAAt(0, 0) != input.NRGBAAt(0, 0) {
		t.Fatalf("exact scene budget with both input profile copies failed: %v/%v", result, err)
	}
	for _, missing := range []int64{1, int64(len(sourceProfile)), int64(len(destination)),
		int64(len(sourceProfile) + len(destination))} {
		if _, err := Render(nil, source, Options{OutputProfile: destination,
			MaxWorkingBytes: budget - missing}); !errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("scene accepted budget missing %d bytes of retained input/transform reservation: %v", missing, err)
		}
	}
	if sha256.Sum256(data) != before || sha256.Sum256(destination) != beforeDestination {
		t.Fatal("scene changed original PSD or borrowed destination profile")
	}
	engine := renderer{options: Options{MaxWorkingBytes: budget}, working: int64(len(destination))}
	profile, err := engine.readProfile(context.Background(), source)
	if err != nil || engine.working != int64(len(sourceProfile)+len(destination)) {
		t.Fatalf("source profile copy was not added to existing output-profile reservation: %d/%v", engine.working, err)
	}
	profile.data[0] ^= 0xff
	if sha256.Sum256(data) != before {
		t.Fatal("owned source profile view aliases original PSD bytes")
	}
	t.Logf("sourceCopy=%d outputCopy=%d conversionReservation=%d sceneExactBudget=%d",
		len(sourceProfile), len(destination), estimate, budget)
}

func TestReviewSceneExhaustedProfileBudgetDoesNotRestoreDefaults(t *testing.T) {
	input := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	source, destination := sceneCostReviewProfile(2), sceneCostReviewProfile(1)
	for _, retained := range []int64{1, 2} {
		engine := renderer{options: Options{MaxWorkingBytes: 1}, working: retained}
		if result, err := engine.convertProfile(context.Background(), input,
			&sceneProfile{data: source}, &sceneProfile{data: destination}); result != nil ||
			!errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("exhausted scene budget fell back to 256 MiB: %v/%v", result, err)
		}
	}
}
