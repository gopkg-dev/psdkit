package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func rasterContentsInput() *image.NRGBA {
	return &image.NRGBA{Rect: image.Rect(-3, 7, 1, 9), Stride: 20, Pix: []byte{
		23, 71, 190, 0, 81, 9, 204, 255, 77, 151, 20, 64, 240, 70, 31, 128, 9, 8, 7, 6,
		250, 0, 255, 128, 70, 120, 160, 255, 33, 22, 11, 1, 90, 55, 31, 0,
	}}
}

// rasterContentsProfile constructs independent ICC wire fields without using the production profile encoder.
func rasterContentsProfile() []byte {
	data := make([]byte, 132+7*12)
	data[8] = 4
	copy(data[12:24], "mntrRGB XYZ ")
	copy(data[36:40], "acsp")
	white := [3]float64{0.9642, 1, 0.8249}
	for index, value := range white {
		binary.BigEndian.PutUint32(data[68+index*4:], uint32(int32(math.Round(value*65536))))
	}
	binary.BigEndian.PutUint32(data[128:132], 7)
	xyz := func(values [3]float64) []byte {
		payload := make([]byte, 20)
		copy(payload, "XYZ ")
		for index, value := range values {
			binary.BigEndian.PutUint32(payload[8+index*4:], uint32(int32(math.Round(value*65536))))
		}
		return payload
	}
	curve := make([]byte, 14)
	copy(curve, "curv")
	binary.BigEndian.PutUint32(curve[8:], 1)
	binary.BigEndian.PutUint16(curve[12:], 256)
	keys := []string{"wtpt", "rXYZ", "gXYZ", "bXYZ", "rTRC", "gTRC", "bTRC"}
	values := [][]byte{xyz(white), xyz([3]float64{1, 0, 0}), xyz([3]float64{0, 1, 0}),
		xyz([3]float64{0, 0, 1}), curve, curve, curve}
	for index, key := range keys {
		position := 132 + index*12
		copy(data[position:], key)
		binary.BigEndian.PutUint32(data[position+4:], uint32(len(data)))
		binary.BigEndian.PutUint32(data[position+8:], uint32(len(values[index])))
		data = append(data, values[index]...)
		for len(data)%4 != 0 {
			data = append(data, 0)
		}
	}
	binary.BigEndian.PutUint32(data, uint32(len(data)))
	return data
}

func TestBuildRasterContentsPreservesRGBAHiddenRGBStrideAndEditableCanvas(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, tagged := range []bool{false, true} {
			input := rasterContentsInput()
			original := bytes.Clone(input.Pix)
			var profile []byte
			if tagged {
				profile = rasterContentsProfile()
			}
			profileBefore := bytes.Clone(profile)
			encoded, err := BuildRasterContents(nil, input, RasterContentsOptions{
				Version: version, LayerName: "设计稿 😀", ICCProfile: profile,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(input.Pix, original) || !bytes.Equal(profile, profileBefore) {
				t.Fatal("building raster contents changed borrowed pixels or profile")
			}
			clear(input.Pix)
			clear(profile)
			source, err := psdfile.Parse(encoded)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			if source.Header.Version != version || source.Header.Width != 4 || source.Header.Height != 2 ||
				!source.GlobalAlpha || len(source.Layers) != 1 || source.Layers[0].Name != "设计稿 😀" ||
				source.Layers[0].Bounds != image.Rect(0, 0, 4, 2) {
				t.Fatal("raster prototype lost version, editable canvas, name or transparency")
			}
			for channelIndex, record := range source.Layers[0].Channels {
				component := int(record.ID)
				if record.ID == -1 {
					component = 3
				}
				plane, err := source.DecodeChannel(nil, 0, channelIndex)
				if err != nil || record.Compression != 0 {
					t.Fatalf("raster layer channel was not bounded raw data: %v", err)
				}
				var expected []byte
				for row := range 2 {
					for column := range 4 {
						expected = append(expected, original[row*20+column*4+component])
					}
				}
				if !bytes.Equal(plane.Data, expected) {
					t.Fatal("raster contents cropped the canvas, changed hidden RGB, or copied row padding")
				}
			}
			var savedProfile []byte
			for _, resource := range source.Resources {
				if resource.ID == 1039 {
					savedProfile, err = source.ReadSpan(resource.Data)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if !bytes.Equal(savedProfile, profileBefore) {
				t.Fatal("ICC declaration was inferred, discarded or aliased borrowed profile bytes")
			}
			reopened, err := New(source)
			if err != nil {
				t.Fatal(err)
			}
			if err := reopened.Rename(1, "Editable after reopen"); err != nil {
				t.Fatal(err)
			}
			var saved bytes.Buffer
			if err := reopened.Write(nil, &saved); err != nil {
				t.Fatalf("raster prototype was not editable: %v", err)
			}
			_ = reopened.Close()
		}
	}
}

func TestBuildRasterContentsBudgetAndFixedOutputBoundaries(t *testing.T) {
	input := smartEditImage(smartEditColors())
	peak, err := RasterContentsWorkingBytes(nil, input)
	if err != nil {
		t.Fatal(err)
	}
	if encoded, err := BuildRasterContents(nil, input,
		RasterContentsOptions{MaxWorkingBytes: peak - 1}); !errors.Is(err, psdfile.ErrLimit) || encoded != nil {
		t.Fatal("raster builder ignored the combined internal peak reservation")
	}
	first, err := BuildRasterContents(nil, input, RasterContentsOptions{MaxWorkingBytes: peak})
	if err != nil {
		t.Fatalf("the declared conservative reservation was insufficient: %v", err)
	}
	second, err := BuildRasterContents(nil, input, RasterContentsOptions{MaxEncodedBytes: int64(len(first))})
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("exact encoded byte budget did not produce deterministic output: %v", err)
	}
	if encoded, err := BuildRasterContents(nil, input,
		RasterContentsOptions{MaxEncodedBytes: int64(len(first)) - 1}); !errors.Is(err, psdfile.ErrLimit) || encoded != nil {
		t.Fatal("short encoded budget returned partial or over-budget bytes")
	}
	first[0] = 0
	if second[0] != '8' {
		t.Fatal("independent raster builds shared returned encoded storage")
	}
	buffer := &rasterContentsBuffer{ctx: context.Background(), capacity: 4}
	if count, err := buffer.Write([]byte{1, 2, 3}); err != nil || count != 3 || cap(buffer.data) != 4 {
		t.Fatal("fixed buffer did not allocate its bounded storage exactly once")
	}
	if count, err := buffer.Write([]byte{4, 5}); !errors.Is(err, psdfile.ErrLimit) || count != 0 ||
		buffer.position != 3 || !bytes.Equal(buffer.data[:3], []byte{1, 2, 3}) {
		t.Fatal("over-capacity streaming write changed retained output")
	}
}

func TestBuildRasterContentsRejectsMalformedInputsAndCancellation(t *testing.T) {
	for _, input := range []*image.NRGBA{nil, {}, {Rect: image.Rect(0, 0, 2, 2), Pix: []byte{1}, Stride: 8},
		{Rect: image.Rect(0, 0, 1, 1), Pix: []byte{1, 2, 3, 4}, Stride: -1},
		{Rect: image.Rect(math.MinInt32, 0, math.MaxInt32, 1), Stride: 4, Pix: []byte{1, 2, 3, 4}}} {
		if result, err := BuildRasterContents(nil, input); err == nil || result != nil {
			t.Fatal("malformed image returned raster contents")
		}
	}
	for _, options := range []RasterContentsOptions{{Version: 3}, {MaxPixels: 1}, {MaxPixels: -1},
		{MaxWorkingBytes: -1}, {MaxEncodedBytes: -1}, {LayerName: "bad\x00name"}, {LayerName: string([]byte{0xff})}} {
		if result, err := BuildRasterContents(nil, smartEditImage(smartEditColors()), options); err == nil || result != nil {
			t.Fatal("malformed options returned raster contents")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := BuildRasterContents(ctx, smartEditImage(smartEditColors()))
	if !errors.Is(err, context.Canceled) || result != nil {
		t.Fatal("canceled build returned raster contents")
	}
	for _, after := range []int{8, 20, 35} {
		ctx := &rasterCancelContext{Context: context.Background(), after: after}
		result, err := BuildRasterContents(ctx, image.NewNRGBA(image.Rect(0, 0, 16, 16)))
		if !errors.Is(err, context.Canceled) || result != nil {
			t.Fatalf("in-progress cancellation at %d returned bytes: %v", after, err)
		}
	}
}

type rasterCancelContext struct {
	context.Context
	calls, after int
}

func (ctx *rasterCancelContext) Err() error {
	ctx.calls++
	if ctx.calls >= ctx.after {
		return context.Canceled
	}
	return nil
}

func TestCreateAndBuildRasterContentsRequireValidExplicitRGBProfiles(t *testing.T) {
	valid := rasterContentsProfile()
	for _, condition := range []string{"truncated", "gray", "lut", "bad-curve", "nonfinite-matrix"} {
		profile := bytes.Clone(valid)
		switch condition {
		case "truncated":
			profile = profile[:100]
		case "gray":
			copy(profile[16:20], "GRAY")
		case "lut":
			copy(profile[132:136], "A2B0")
		case "bad-curve":
			position := int(binary.BigEndian.Uint32(profile[132+4*12+4:]))
			binary.BigEndian.PutUint16(profile[position+12:], 0)
		case "nonfinite-matrix":
			position := int(binary.BigEndian.Uint32(profile[132+1*12+4:]))
			clear(profile[position+8 : position+20])
		}
		if editor, err := Create(nil, 1, 1, CreateOptions{ICCProfile: profile}); err == nil || editor != nil {
			t.Fatalf("invalid ICC %s was copied into a new source", condition)
		}
		if result, err := BuildRasterContents(nil, smartEditImage(smartEditColors()),
			RasterContentsOptions{ICCProfile: profile}); err == nil || result != nil {
			t.Fatalf("invalid ICC %s was copied into raster contents", condition)
		}
	}
	if editor, err := Create(nil, 1, 1, CreateOptions{ICCProfile: valid,
		Limits: psdfile.Limits{MaxFileBytes: 256}}); !errors.Is(err, psdfile.ErrLimit) || editor != nil {
		t.Fatal("profile creation raised the caller's source file limit")
	}
	if editor, err := Create(nil, 1, 1, CreateOptions{ICCProfile: valid,
		Limits: psdfile.Limits{MaxDecodeBytes: 2048}}); !errors.Is(err, psdfile.ErrLimit) || editor != nil {
		t.Fatal("profile validation/copy escaped the aggregate creation budget")
	}
}

func TestBuildRasterContentsFeedsSharedPSDPSBReplacement(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		wanted := smartEditImage(smartEditColors())
		encoded, err := BuildRasterContents(nil, wanted, RasterContentsOptions{Version: version})
		if err != nil {
			t.Fatal(err)
		}
		editor, source := smartEditOpen(t, smartEditFixture(t, version, ""))
		infos, _ := editor.SmartObjects(nil)
		if err := editor.ReplaceContents(nil, infos[0].ID, encoded); err != nil {
			t.Fatal(err)
		}
		smartEditAssertCaches(t, editor, wanted)
		var saved bytes.Buffer
		if err := editor.Write(nil, &saved, SaveOptions{Compression: 1}); err != nil {
			t.Fatal(err)
		}
		parsed, err := psdfile.Parse(saved.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		defer parsed.Close()
		smartEditAssertPreservedMetadata(t, source, parsed)
		links, _ := parsed.LinkedFiles(nil)
		data, _ := parsed.ReadSpan(links[0].Data)
		if !bytes.Equal(data, encoded) {
			t.Fatal("shared replacement did not embed the complete editable raster prototype")
		}
	}
}

func TestBuildRasterContentsTransparentBorderRemainsCanvasSized(t *testing.T) {
	input := image.NewNRGBA(image.Rect(0, 0, 12, 6))
	input.SetNRGBA(7, 2, color.NRGBA{R: 210, G: 50, B: 73, A: 255})
	encoded, err := BuildRasterContents(nil, input)
	if err != nil {
		t.Fatal(err)
	}
	source, err := psdfile.Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if source.Header.Width != 12 || source.Header.Height != 6 || source.Layers[0].Bounds != input.Rect {
		t.Fatal("transparent edges were automatically cropped from raster contents")
	}
}

func TestCreateExplicitProfileOwnershipAndSourceLimits(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		profile := rasterContentsProfile()
		wanted := bytes.Clone(profile)
		editor, err := Create(nil, 2, 2, CreateOptions{Version: version, ICCProfile: profile})
		if err != nil {
			t.Fatal(err)
		}
		clear(profile)
		if err := editor.ReplacePixels(1, smartEditImage(smartEditColors())); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		if err := editor.Write(nil, &output); err != nil {
			t.Fatal(err)
		}
		_ = editor.Close()
		source, err := psdfile.Parse(output.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		count := 0
		for _, resource := range source.Resources {
			if resource.ID != 1039 {
				continue
			}
			count++
			actual, err := source.ReadSpan(resource.Data)
			if err != nil || !bytes.Equal(actual, wanted) {
				t.Fatalf("created source ICC depended on cleared caller bytes or closed editor: %v", err)
			}
		}
		if count != 1 || source.Header.Version != version {
			t.Fatal("explicit creation did not preserve exactly one ICC resource and format version")
		}
	}
	profile := rasterContentsProfile()
	if editor, err := Create(nil, 1, 1, CreateOptions{ICCProfile: profile,
		Limits: psdfile.Limits{MaxDecodeBytes: 4 << 10}}); !errors.Is(err, psdfile.ErrLimit) || editor != nil {
		t.Fatal("ICC semantic validation exceeded its remaining creation storage")
	}
}
