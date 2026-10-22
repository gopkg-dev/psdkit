package psdfont

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestVariationNormalizeFixedF2DOT14AndOwnedAccessors(t *testing.T) {
	data := variationTestFvar()
	face := &Face{raw: data, tables: map[string]Span{"fvar": {Length: int64(len(data))}}}
	budget := &fontMetadataBudget{face: face, ctx: context.Background(),
		limits: DecodeOptions{MaxBytes: 10000, MaxCFFOperations: 100}}
	variation, err := readFvarMetadata(budget.ctx, face, budget)
	if err != nil {
		t.Fatal(err)
	}
	variation.avar = [][]axisVariationMap{{{-16384, -16384}, {-8192, -4096}, {0, 0}, {16384, 16384}}}
	for _, control := range []struct {
		user     float64
		expected int16
		clamped  int32
	}{
		{100, -16384, 100 << 16}, {250, -4096, 250 << 16}, {400, 0, 400 << 16},
		{650, 8192, 650 << 16}, {900, 16384, 900 << 16}, {1e200, 16384, 900 << 16},
	} {
		request := []VariationCoordinate{{Tag: "wght", Value: control.user}}
		if err := selectVariationCoordinates(variation, request, budget); err != nil {
			t.Fatal(err)
		}
		if variation.normalized[0] != control.expected || variation.coordinates[0].ClampedFixed != control.clamped {
			t.Fatalf("fixed normalized %.17g: %v", control.user, variation.coordinates)
		}
		request[0].Value = 0
		if variation.coordinates[0].Requested != control.user {
			t.Fatal("selected coordinate borrowed caller slice")
		}
	}
	face.variation = variation
	axes, err := face.VariationAxes(nil)
	if err != nil || len(axes) != 1 {
		t.Fatal(err)
	}
	axes[0].Tag = "edit"
	coordinates, err := face.VariationCoordinates(nil)
	if err != nil || len(coordinates) != 1 {
		t.Fatal(err)
	}
	coordinates[0].Tag = "edit"
	named, err := face.NamedVariationInstances(nil)
	if err != nil || len(named) != 1 {
		t.Fatal(err)
	}
	named[0].Coordinates[0] = -1
	if variation.axes[0].axis.Tag != "wght" || variation.coordinates[0].Tag != "wght" ||
		variation.named[0].Coordinates[0] != 650 {
		t.Fatal("public accessors exposed mutable face metadata")
	}
}

func TestVariationNormalizeRecoveryAndInvalidRequests(t *testing.T) {
	budget := &fontMetadataBudget{face: &Face{}, ctx: context.Background(),
		limits: DecodeOptions{MaxBytes: 10000, MaxCFFOperations: 1000}}
	for _, maps := range [][]axisVariationMap{
		{}, {{-16384, -16384}, {16384, 16384}},
		{{-16384, -16384}, {0, 0}, {0, 9000}, {-8192, -8192}, {8192, 8192}, {16384, 16384}},
	} {
		actual, err := normalizedAvar(16384, maps, budget)
		if err != nil || actual != 16384 {
			t.Fatalf("identity/recovered avar profile: %v/%d", err, actual)
		}
	}
	variation := &fontVariationData{axes: []variationAxisRecord{{minimum: 0, defaultValue: 0, maximum: 65536,
		axis: VariationAxis{Tag: "wght", Maximum: 1}}}}
	for _, requests := range [][]VariationCoordinate{
		{{Tag: "nope", Value: 0}}, {{Tag: "wght", Value: math.NaN()}},
		{{Tag: "wght", Value: 0}, {Tag: "wght", Value: 1}},
	} {
		if err := selectVariationCoordinates(variation, requests, budget); !errors.Is(err, ErrMalformed) {
			t.Fatal("invalid user axis request accepted")
		}
	}
	for _, control := range [][3]int64{{5, 2, 3}, {-5, 2, -2}, {7, 4, 2}, {-7, 4, -2}} {
		if fixedNearest(control[0], control[1]) != control[2] {
			t.Fatal("declared intermediate signed-nearest/tie-positive profile changed")
		}
	}
}
