package psdfont

import (
	"context"
	"encoding/json/v2"
	"errors"
	"math"
	"os"
	"testing"
)

func TestCFF2InstanceDynamicPositionsActual(t *testing.T) {
	cff2VerifyPortableFonts(t)
	data, err := os.ReadFile("testdata/cff2/layout-instance-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		File  string
		Cases []struct {
			Text        string
			Coordinates []VariationCoordinate
			Features    []ShapeFeature
			Advances    []int64
			Precise     []float64
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
		run, err := Shape(nil, face, row.Text, ShapeOptions{Features: row.Features})
		if err != nil || run == nil || len(run.Glyphs) != 2 {
			t.Fatalf("dynamic shape: %v", err)
		}
		for index, glyph := range run.Glyphs {
			if glyph.XAdvance != row.Advances[index] || glyph.InstancePosition == nil ||
				math.Float64bits(glyph.InstancePosition.XAdvance) != math.Float64bits(row.Precise[index]) {
				t.Fatalf("coords%v glyph%d got%+v exact%v expected%d/%g", row.Coordinates, index,
					glyph, glyph.InstancePosition, row.Advances[index], row.Precise[index])
			}
		}
		for _, delta := range []int64{-1, 0, 1} {
			bounded, err := Shape(nil, face, row.Text,
				ShapeOptions{Features: row.Features, MaxWorkingBytes: run.WorkingBytes() + delta})
			if delta < 0 {
				if bounded != nil || !errors.Is(err, ErrLimit) {
					t.Fatalf("one-short shape: %v", err)
				}
			} else if bounded == nil || err != nil {
				t.Fatalf("exact shape reservation: %v", err)
			}
		}
	}
	if len(oracle.Cases) != 4 {
		t.Fatal("four nonempty dynamic instances required")
	}
}

func TestCFF2FeatureVariationUnknownConditionAndCancellation(t *testing.T) {
	face := &Face{variation: &fontVariationData{normalized: []int16{0}}}
	ctx, cancel := context.WithCancel(context.Background())
	budget := &shapeBudget{ctx: ctx, maxNodes: 1000, maxBytes: 1 << 20}
	p := otParser{data: []byte{0, 1, 0, 0, 0, 6, 0, 9}, b: budget, face: face, tag: "GSUB"}
	matched, err := p.conditionsMatch(0)
	if err != nil || matched {
		t.Fatalf("unknown condition must fail record: %v", err)
	}
	cancel()
	matched, err = p.conditionsMatch(0)
	if matched || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled condition must not match: %v", err)
	}
}
