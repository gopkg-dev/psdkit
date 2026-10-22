package psdfont

import (
	"encoding/json/v2"
	"errors"
	"os"
	"testing"
)

func TestCFF2RawBaselineRSBAndSharedContourQuotas(t *testing.T) {
	cff2VerifyPortableFonts(t)
	data, err := os.ReadFile("testdata/cff2/rsb-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		File  string
		Cases []struct {
			Coordinates              []VariationCoordinate
			Advance, LSB, RSB, Width float64
			Wrong                    float64 `json:"wrong_mixed_domain"`
		}
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	for _, row := range oracle.Cases {
		face, err := Open(nil, oracle.File, DecodeOptions{Variations: row.Coordinates})
		if err != nil {
			t.Fatal(err)
		}
		glyph, err := face.Glyph(nil, 1, GlyphOptions{MaxContours: 2})
		if err != nil || glyph == nil || glyph.InstanceMetrics == nil {
			t.Fatal(err)
		}
		metrics := glyph.InstanceMetrics
		if metrics.AdvanceWidth != row.Advance || metrics.LeftSideBearing != row.LSB ||
			metrics.RightSideBearing != row.RSB || metrics.RightSideBearing == row.Wrong ||
			metrics.OutlineBounds[2]-metrics.OutlineBounds[0] != row.Width {
			t.Fatalf("raw-baseline domain: %+v expected %+v", metrics, row)
		}
		short, err := face.Glyph(nil, 1, GlyphOptions{MaxContours: 1})
		if short != nil || !errors.Is(err, ErrLimit) {
			t.Fatalf("two-VM contour sum must fail one-short: %v", err)
		}
	}
	if len(oracle.Cases) != 7 {
		t.Fatal("seven shared independent RSB controls required")
	}
}

func TestCFF2RawBaselineDiffersFromNormalizedZeroConstantRegion(t *testing.T) {
	cff2VerifyPortableFonts(t)
	face, err := Open(nil, "testdata/cff2/fonts/ConstantBaseRSB.otf")
	if err != nil {
		t.Fatal(err)
	}
	glyph, err := face.Glyph(nil, 1, GlyphOptions{MaxContours: 2})
	if err != nil || glyph == nil || glyph.InstanceMetrics == nil {
		t.Fatal(err)
	}
	metrics := glyph.InstanceMetrics
	width := metrics.OutlineBounds[2] - metrics.OutlineBounds[0]
	if metrics.AdvanceWidth != 612.5 || metrics.LeftSideBearing != 72.5 || width != 265 ||
		metrics.RightSideBearing != 275 || metrics.RightSideBearing == 215 ||
		metrics.RightSideBearing != metrics.AdvanceWidth-metrics.LeftSideBearing-width {
		t.Fatalf("constant region must vary current outline but not raw-baseline operands: %+v", metrics)
	}
	for _, delta := range []int64{-1, 0, 1} {
		bounded, err := face.Glyph(nil, 1, GlyphOptions{MaxWorkingBytes: glyph.WorkingBytes() + delta})
		if delta < 0 {
			if bounded != nil || !errors.Is(err, ErrLimit) {
				t.Fatalf("one-short raw baseline workspace: %v", err)
			}
		} else if bounded == nil || err != nil {
			t.Fatalf("exact raw baseline workspace: %v", err)
		}
	}
}

func TestCFF2DecodeActualWholeBytesOperationsAndOwnership(t *testing.T) {
	cff2VerifyPortableFonts(t)
	data, err := os.ReadFile("testdata/cff2/fonts/InstancePair-v2.otf")
	if err != nil {
		t.Fatal(err)
	}
	options := DecodeOptions{Variations: []VariationCoordinate{{Tag: "wght", Value: .5}}}
	face, err := Decode(nil, data, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, delta := range []int64{-1, 0, 1} {
		boundedOptions := options
		boundedOptions.MaxBytes = face.reservation + delta
		bounded, err := Decode(nil, data, boundedOptions)
		if delta < 0 {
			if bounded != nil || !errors.Is(err, ErrLimit) {
				t.Fatalf("one-short total Decode workspace: %v", err)
			}
		} else if bounded == nil || err != nil {
			t.Fatalf("exact total Decode workspace: %v", err)
		}
	}
	low, high := 1, 10000
	for low < high {
		middle := low + (high-low)/2
		probe := options
		probe.MaxCFFOperations = middle
		bounded, err := Decode(nil, data, probe)
		if errors.Is(err, ErrLimit) {
			if bounded != nil {
				t.Fatal("limited shared metadata returned partial face")
			}
			low = middle + 1
		} else if err != nil || bounded == nil {
			t.Fatalf("unexpected search error: %v", err)
		} else {
			high = middle
		}
	}
	for _, delta := range []int{-1, 0, 1} {
		probe := options
		probe.MaxCFFOperations = low + delta
		bounded, err := Decode(nil, data, probe)
		if delta < 0 {
			if bounded != nil || !errors.Is(err, ErrLimit) {
				t.Fatalf("one-short combined request/CFF/fvar/HVAR/GDEF work: %v", err)
			}
		} else if bounded == nil || err != nil {
			t.Fatalf("exact combined metadata work: %v", err)
		}
	}
	data[0] ^= 255
	options.Variations[0].Value = -.5
	coords, err := face.VariationCoordinates(nil)
	if err != nil || coords[0].Normalized != 8192 {
		t.Fatalf("caller input mutation changed owned instance: %+v %v", coords, err)
	}
	run, err := Shape(nil, face, "AB", ShapeOptions{Features: []ShapeFeature{{Tag: "kern", Enabled: true}}})
	if err != nil || run == nil || run.Glyphs[0].XAdvance != 501 {
		t.Fatalf("owned raw/scalars changed after caller mutation: %v", err)
	}
	t.Logf("shared metadata minimum=%d operations, total Decode reservation=%d", low, face.reservation)
}
