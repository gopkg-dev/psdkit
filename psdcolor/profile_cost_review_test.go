package psdcolor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"runtime"
	"strconv"
	"testing"
	"unsafe"
)

// costReviewSharedCurve constructs three TRC tags pointing to exactly one stored table.
// Large source tables may have plateaus; destination inversion requires strict increase.
func costReviewSharedCurve(count int) []byte {
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
	curve := make([]byte, 12+count*2)
	copy(curve, "curv")
	binary.BigEndian.PutUint32(curve[8:], uint32(count))
	for index := range count {
		binary.BigEndian.PutUint16(curve[12+index*2:], uint16(uint64(index)*65535/uint64(count-1)))
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

func TestReviewProfileCostSharedLargeSourceCurves(t *testing.T) {
	for _, count := range []int{100_000, 500_000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			ctx := context.Background()
			source := costReviewSharedCurve(count)
			destination := profileRGB(4, profileGamma(1), [3][3]float64{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}})
			before := sha256.Sum256(source)
			input := image.NewNRGBA(image.Rect(0, 0, 1, 1))
			input.SetNRGBA(0, 0, color.NRGBA{R: 64, G: 128, B: 192, A: 127})
			estimate, err := ProfileTransformWorkingBytes(ctx, input, source, destination)
			if err != nil {
				t.Fatal(err)
			}
			settings, _ := profileTransformSettings(nil)
			var beforeParse, afterParse runtime.MemStats
			runtime.ReadMemStats(&beforeParse)
			parsed, err := profileTransformParse(ctx, source, settings)
			runtime.ReadMemStats(&afterParse)
			if err != nil {
				t.Fatal(err)
			}
			for channel := range parsed.curves {
				if len(parsed.curves[channel].table) != count ||
					&parsed.curves[channel].table[0] != &parsed.curves[0].table[0] {
					t.Fatal("exact ICC tag aliases allocated independent per-channel curves")
				}
			}
			retained := int64(cap(parsed.curves[0].table))*2 + int64(unsafe.Sizeof(*parsed)) + int64(len(input.Pix))
			sourceCost, err := profileTransformCost(ctx, source, settings)
			if err != nil || sourceCost < retained {
				t.Fatalf("source estimate %d is below unique retained bytes %d: %v", sourceCost, retained, err)
			}
			allocated := afterParse.TotalAlloc - beforeParse.TotalAlloc
			if allocated > uint64(sourceCost) {
				t.Fatalf("source parser allocated %d bytes, exceeding its %d-byte conservative reservation", allocated, sourceCost)
			}
			result, err := TransformProfile(ctx, input, source, destination,
				ProfileTransformOptions{MaxWorkingBytes: estimate})
			if err != nil || result.NRGBAAt(0, 0) != input.NRGBAAt(0, 0) {
				t.Fatalf("valid aliased source transform failed at its estimate: %v, %v", result, err)
			}
			if _, err := TransformProfile(ctx, input, source, destination,
				ProfileTransformOptions{MaxWorkingBytes: estimate - 1}); !errors.Is(err, ErrLimit) {
				t.Fatalf("one byte below the reported reservation was accepted: %v", err)
			}
			if _, err := TransformProfile(ctx, input, destination, source); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("large plateau destination was silently treated as invertible: %v", err)
			}
			if sha256.Sum256(source) != before || !bytes.Equal(input.Pix, []byte{64, 128, 192, 127}) {
				t.Fatal("ICC conversion changed borrowed profile or pixel bytes")
			}
			t.Logf("samples=%d profile=%d uniqueTable=%d parserAllocated=%d sourceCost=%d aggregateEstimate=%d",
				count, len(source), count*2, allocated, sourceCost, estimate)
			runtime.KeepAlive(parsed)
		})
	}
}

func TestReviewProfileCostZeroBudgetRetainsDocumentedDefault(t *testing.T) {
	input := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	implicit, err := ProfileTransformWorkingBytes(nil, input, nil, nil,
		ProfileTransformOptions{AssumeSRGB: true, MaxWorkingBytes: 0})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := ProfileTransformWorkingBytes(nil, input, nil, nil,
		ProfileTransformOptions{AssumeSRGB: true, MaxWorkingBytes: 256 << 20})
	if err != nil || implicit != explicit || implicit != 2052 {
		t.Fatalf("zero no longer means the documented default: %d/%d/%v", implicit, explicit, err)
	}
	if _, err := TransformProfile(nil, input, nil, nil,
		ProfileTransformOptions{AssumeSRGB: true, MaxWorkingBytes: implicit - 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("an explicit small nonzero budget fell back to the default: %v", err)
	}
}
