package psdscene_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcolor"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func profileSceneFlat(profile []byte) []byte {
	var resources []byte
	if profile != nil {
		resources = []byte("8BIM")
		resources = binary.BigEndian.AppendUint16(resources, 1039)
		resources = append(resources, 0, 0)
		resources = binary.BigEndian.AppendUint32(resources, uint32(len(profile)))
		resources = append(resources, profile...)
		if len(profile)%2 != 0 {
			resources = append(resources, 0)
		}
	}
	data := binary.BigEndian.AppendUint16([]byte("8BPS"), 1)
	data = append(data, make([]byte, 6)...)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = binary.BigEndian.AppendUint32(data, 1)
	data = binary.BigEndian.AppendUint32(data, 1)
	data = binary.BigEndian.AppendUint16(data, 8)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = binary.BigEndian.AppendUint32(data, 0)
	data = binary.BigEndian.AppendUint32(data, uint32(len(resources)))
	data = append(data, resources...)
	data = binary.BigEndian.AppendUint32(data, 0)
	return append(data, 0, 0, 64, 128, 192)
}

func profileSceneICC(t *testing.T) []byte {
	t.Helper()
	if os.Getenv("PSDKIT_PROFILE_NATIVE") != "1" {
		t.Skip("optional native ICC fixture requires PSDKIT_PROFILE_NATIVE=1")
	}
	source, err := psdfile.Open(filepath.Join("..", "output", "p6-native", "rgb8-srgb.psd"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	profile, err := psdscene.ReadSourceRGBProfile(nil, source, 0)
	if err != nil || len(profile) == 0 {
		t.Fatalf("native ICC was not read: %v", err)
	}
	return profile
}

func TestSceneProfileUntaggedDefaultAndExplicitAssumption(t *testing.T) {
	source, err := psdfile.Read(bytes.NewReader(profileSceneFlat(nil)), int64(len(profileSceneFlat(nil))))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, options := range []psdscene.Options{{}, {OutputProfile: []byte{}},
		{OutputProfile: []byte{}, AssumeUntaggedSRGB: true}} {
		result, err := psdscene.Render(nil, source, options)
		if err != nil || result.NRGBAAt(0, 0) != (color.NRGBA{64, 128, 192, 255}) {
			t.Fatalf("two untagged spaces changed source RGB: %v/%v", result, err)
		}
	}
	// Equal nonempty bytes preserve encoding without interpreting an unused transform.
	opaque := make([]byte, 132)
	data := profileSceneFlat(opaque)
	withOpaque, err := psdfile.Read(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer withOpaque.Close()
	opaqueResult, opaqueErr := psdscene.Render(nil, withOpaque, psdscene.Options{OutputProfile: bytes.Clone(opaque)})
	if opaqueErr != nil || opaqueResult.NRGBAAt(0, 0) != (color.NRGBA{64, 128, 192, 255}) {
		t.Fatalf("identical profile encoding unnecessarily converted: %v/%v", opaqueResult, opaqueErr)
	}
}

func TestNativeSceneMissingOneProfileRequiresExplicitAssumption(t *testing.T) {
	profile := profileSceneICC(t)
	for _, testCase := range []struct {
		name                string
		source, destination []byte
	}{
		{"untagged-source", nil, profile},
		{"untagged-destination", profile, []byte{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			data := profileSceneFlat(testCase.source)
			source, err := psdfile.Read(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			options := psdscene.Options{OutputProfile: testCase.destination}
			if result, err := psdscene.Render(nil, source, options); result != nil ||
				!errors.Is(err, psdfile.ErrUnsupported) {
				t.Fatalf("missing profile silently guessed: %v/%v", result, err)
			}
			options.AssumeUntaggedSRGB = true
			actual, err := psdscene.Render(nil, source, options)
			if err != nil {
				t.Fatal(err)
			}
			input := image.NewNRGBA(image.Rect(0, 0, 1, 1))
			input.Pix = []byte{64, 128, 192, 255}
			expected, err := psdcolor.TransformProfile(nil, input, testCase.source, testCase.destination,
				psdcolor.ProfileTransformOptions{AssumeSRGB: true})
			if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
				t.Fatalf("explicit assumption did not use the color API: %v", err)
			}
		})
	}
}

func TestNativeSceneOutputProfileMatchesRawLayerTransform(t *testing.T) {
	if os.Getenv("PSDKIT_PROFILE_NATIVE") != "1" {
		t.Skip("optional native profile controls require PSDKIT_PROFILE_NATIVE=1")
	}
	for _, pair := range [][2]string{{"rgb8-adobergb", "rgb8-srgb"}, {"rgb8-srgb", "rgb8-adobergb"}} {
		t.Run(pair[0]+"-to-"+pair[1], func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "output", "p6-native", pair[0]+".psd"))
			if err != nil {
				t.Fatal(err)
			}
			source, err := psdfile.Read(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			destination, err := psdfile.Open(filepath.Join("..", "output", "p6-native", pair[1]+".psd"))
			if err != nil {
				t.Fatal(err)
			}
			defer destination.Close()
			sourceICC, err := psdscene.ReadSourceRGBProfile(nil, source, 0)
			if err != nil {
				t.Fatal(err)
			}
			destinationICC, err := psdscene.ReadSourceRGBProfile(nil, destination, 0)
			if err != nil {
				t.Fatal(err)
			}
			beforeData, beforeICC := sha256.Sum256(data), sha256.Sum256(destinationICC)
			layer, err := source.DecodeLayer(nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			input := image.NewNRGBA(layer.Bounds)
			for _, plane := range layer.Channels {
				component := int(plane.ID)
				if component == -1 {
					component = 3
				}
				if component < 0 || component > 3 || plane.Depth != 8 {
					t.Fatalf("unexpected native channel %d/%d", plane.ID, plane.Depth)
				}
				for y := 0; y < plane.Height; y++ {
					for x := 0; x < plane.Width; x++ {
						input.Pix[y*input.Stride+x*4+component] = plane.Data[y*plane.RowBytes+x]
					}
				}
			}
			// A transparent composed backdrop defines zero RGB for alpha zero.
			for position := 0; position < len(input.Pix); position += 4 {
				if input.Pix[position+3] == 0 {
					input.Pix[position], input.Pix[position+1], input.Pix[position+2] = 0, 0, 0
				}
			}
			defaultPixels, err := psdscene.Render(nil, source)
			if err != nil || !bytes.Equal(defaultPixels.Pix, input.Pix) {
				t.Fatalf("default Scene changed encoded source colors: %v", err)
			}
			expected, err := psdcolor.TransformProfile(nil, input, sourceICC, destinationICC)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := psdscene.Render(nil, source, psdscene.Options{OutputProfile: destinationICC})
			if err != nil || actual.Rect != expected.Rect || !bytes.Equal(actual.Pix, expected.Pix) {
				t.Fatalf("Scene profile conversion differs from original layer color API: %v", err)
			}
			if sha256.Sum256(data) != beforeData || sha256.Sum256(destinationICC) != beforeICC {
				t.Fatal("Scene changed original source or caller-owned destination ICC")
			}
			retained, retainedProfile := sha256.Sum256(actual.Pix), sha256.Sum256(sourceICC)
			_ = source.Close()
			_ = destination.Close()
			if sha256.Sum256(actual.Pix) != retained || sha256.Sum256(sourceICC) != retainedProfile {
				t.Fatal("owned Scene image/profile depended on open source")
			}
			if _, err := psdscene.Render(nil, source); !errors.Is(err, psdfile.ErrClosed) {
				t.Fatalf("closed source still rendered: %v", err)
			}
		})
	}
}

type profileSceneReader struct {
	data         []byte
	active       bool
	offset, end  int64
	payloadReads int
	failure      error
	cancel       context.CancelFunc
}

func (reader *profileSceneReader) ReadAt(buffer []byte, offset int64) (int, error) {
	count, err := bytes.NewReader(reader.data).ReadAt(buffer, offset)
	if reader.active && offset < reader.end && offset+int64(len(buffer)) > reader.offset {
		reader.payloadReads++
		if reader.cancel != nil {
			reader.cancel()
		}
		if reader.failure != nil {
			return count, reader.failure
		}
	}
	return count, err
}

func TestSceneSourceProfileLimitsCauseAndCancellation(t *testing.T) {
	// Profile extraction owns raw metadata; interpreting the tags belongs to the color converter.
	profile := make([]byte, 132)
	copy(profile[36:40], "acsp")
	data := profileSceneFlat(profile)
	reader := &profileSceneReader{data: data}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	span := source.Resources[0].Data
	reader.offset, reader.end, reader.active = span.Offset, span.Offset+span.Length, true
	_, profileErr := psdscene.ReadSourceRGBProfile(nil, source, int64(len(profile)-1))
	if !errors.Is(profileErr, psdfile.ErrLimit) || reader.payloadReads != 0 {
		t.Fatalf("profile limit did not reject before payload copy: %d/%v", reader.payloadReads, profileErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := psdscene.ReadSourceRGBProfile(ctx, source, 0); !errors.Is(err, context.Canceled) ||
		reader.payloadReads != 0 {
		t.Fatalf("pre-cancelled profile request performed IO: %d/%v", reader.payloadReads, err)
	}
	cause := errors.New("independent full-count profile read failure")
	reader.failure = cause
	if result, err := psdscene.ReadSourceRGBProfile(nil, source, 0); result != nil || !errors.Is(err, cause) {
		t.Fatalf("full-count ReaderAt cause was discarded: %v/%v", result, err)
	}
	reader.failure = nil
	ctx, cancel = context.WithCancel(context.Background())
	reader.cancel = cancel
	if result, err := psdscene.ReadSourceRGBProfile(ctx, source, 0); result != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation during owned profile read was discarded: %v/%v", result, err)
	}
	reader.cancel = nil
	owned, err := psdscene.ReadSourceRGBProfile(nil, source, 0)
	if err != nil || !bytes.Equal(owned, profile) {
		t.Fatalf("owned source ICC differs: %v", err)
	}
	owned[0] ^= 0xff
	if !bytes.Equal(data[span.Offset:span.Offset+span.Length], profile) {
		t.Fatal("owned source ICC aliases ReaderAt bytes")
	}
	_ = source.Close()
	if _, err := psdscene.ReadSourceRGBProfile(nil, source, 0); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed source profile still read: %v", err)
	}
}

func TestSceneProfileTransformErrorCausesRemainVisible(t *testing.T) {
	data := profileSceneFlat(nil)
	source, err := psdfile.Read(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	profile := make([]byte, 132)
	copy(profile[16:24], "GRAYXYZ ")
	result, err := psdscene.Render(nil, source,
		psdscene.Options{OutputProfile: profile, AssumeUntaggedSRGB: true})
	if result != nil || !errors.Is(err, psdfile.ErrUnsupported) || !errors.Is(err, psdcolor.ErrUnsupported) {
		t.Fatalf("Scene discarded the unsupported ICC cause or fell back: %v/%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, cancelledErr := psdscene.Render(ctx, source, psdscene.Options{OutputProfile: profile})
	if !errors.Is(cancelledErr, context.Canceled) {
		t.Fatalf("Scene profile setup discarded cancellation: %v", cancelledErr)
	}
}
