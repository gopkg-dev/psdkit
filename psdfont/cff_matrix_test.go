package psdfont

import (
	"encoding/json/v2"
	"errors"
	"math"
	"math/big"
	"os"
	"runtime"
	"testing"
	"unsafe"
)

func TestCFFMatrixFullSixAffineComposition(t *testing.T) {
	top := [6]float64{0.5, 0.25, -0.125, 0.75, 1.5, -0.5}
	fd := [6]float64{1.25, 0.5, -0.25, 0.75, 3, -2}
	actual, err := composeCFFMatrix(top, fd, 2, Span{Offset: 77})
	if err != nil {
		t.Fatal(err)
	}
	// Independent expansion of Top(FD(x,y)) gives these exact dyadic coefficients, including both translations.
	want := [6]float64{1.125, 1.375, -0.4375, 1, 6.5, -2.5}
	if actual != want {
		t.Fatalf("noncommuting full affine composition=%v want=%v", actual, want)
	}
	for _, point := range [][2]float64{{0, 0}, {10, 20}, {-7, 11}, {25.5, -13.25}} {
		x, y := point[0], point[1]
		fdX, fdY := 1.25*x-0.25*y+3, 0.5*x+0.75*y-2
		wantX, wantY := 2*(0.5*fdX-0.125*fdY+1.5), 2*(0.25*fdX+0.75*fdY-0.5)
		actualX := actual[0]*x + actual[2]*y + actual[4]
		actualY := actual[1]*x + actual[3]*y + actual[5]
		if actualX != wantX || actualY != wantY {
			t.Fatal("full six-component matrix did not map every point in the declared order")
		}
	}
}

func TestCFFMatrixCanonicalIdentityAndPresenceProfiles(t *testing.T) {
	for _, units := range []uint16{1000, 1536, 2048, 3000, 4096, 16384} {
		top := cffDict{hasMatrix: true, matrix: [6]float64{1 / float64(units), 0, 0, 1 / float64(units), 0, 0}}
		actual, err := resolvedCFFMatrix(top, nil, units)
		if err != nil || actual != cffIdentityMatrix {
			t.Fatal("canonical old path did not retain literal identity")
		}
	}
	if actual, err := resolvedCFFMatrix(cffDict{}, &cffDict{}, 1000); err != nil || actual != cffIdentityMatrix {
		t.Fatal("both-absent old legal case changed")
	}
	if actual, err := resolvedCFFMatrix(cffDict{}, nil, 2048); err != nil ||
		actual != [6]float64{2.048, 0, 0, 2.048, 0, 0} {
		t.Fatal("omitted Top .001 was replaced by head-identity tool behavior")
	}
	explicitFD := cffDict{hasMatrix: true, matrix: cffIdentityMatrix}
	if actual, err := resolvedCFFMatrix(cffDict{}, &explicitFD, 1000); err != nil ||
		actual != [6]float64{1000, 0, 0, 1000, 0, 0} {
		t.Fatal("explicit FD legacy loading profile did not use effective FD geometry")
	}
	fd := cffDict{hasMatrix: true, matrix: [6]float64{0.001, 0.0002, -0.0001, 0.001, 0.007, -0.013}}
	actual, err := resolvedCFFMatrix(cffDict{}, &fd, 1000)
	if err != nil || actual != [6]float64{1, 0.2, -0.1, 1, 7, -13} {
		t.Fatal("explicit FD legacy full affine lost linear or translation components")
	}
}

func TestCFFMatrixIndependentFractionBinary64Extremes(t *testing.T) {
	data, err := os.ReadFile("testdata/cff-matrix/math.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Cases []struct {
			ID             string
			Top, FD        [6]float64
			Units          uint16
			RawNonsingular bool      `json:"raw_nonsingular"`
			Bits           [6]uint64 `json:"want_bits"`
			Error          string    `json:"want_error"`
		}
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil || len(oracle.Cases) != 133 {
		t.Fatal("133 independent exact-binary controls required")
	}
	var scratch cffMatrixScratch
	var peakRetained, peakAllocated uint64
	for _, row := range oracle.Cases {
		if !row.RawNonsingular {
			continue
		}
		if err := scratch.validate(row.Top, Span{Offset: 77}); err != nil {
			t.Fatal("valid extreme Top failed exact rank")
		}
		if err := scratch.validate(row.FD, Span{Offset: 88}); err != nil {
			t.Fatal("valid extreme FD failed exact rank")
		}
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		actual, err := composeCFFMatrixWithScratch(row.Top, row.FD, row.Units, Span{Offset: 88}, &scratch)
		runtime.ReadMemStats(&after)
		peakAllocated = max(peakAllocated, after.TotalAlloc-before.TotalAlloc)
		retained := uint64(unsafe.Sizeof(scratch))
		for _, value := range []*big.Rat{&scratch.first, &scratch.second, &scratch.left, &scratch.right} {
			retained += uint64(cap(value.Num().Bits())+cap(value.Denom().Bits())) * uint64(unsafe.Sizeof(big.Word(0)))
		}
		peakRetained = max(peakRetained, retained)
		if row.Error != "" {
			if actual != [6]float64{} || !errors.Is(err, ErrLimit) {
				t.Fatalf("case=%s final overflow/rank loss returned partial matrix: %v", row.ID, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("case=%s intermediate overflow/underflow rejected valid final matrix: %v", row.ID, err)
		}
		for index, value := range actual {
			if math.Float64bits(value) != row.Bits[index] {
				t.Fatalf("case=%s coefficient=%d bits=%x want=%x", row.ID, index, math.Float64bits(value), row.Bits[index])
			}
		}
	}
	if peakRetained >= uint64(cffMatrixScratchBytes) {
		t.Fatal("retained rational capacity exceeded its shared scratch reservation")
	}
	t.Logf("extreme controls: retained limb capacity peak=%d bytes; observed per-composition allocation=%d bytes",
		peakRetained, peakAllocated)
}

func TestCFFMatrixRankFiniteOverflowBoundaries(t *testing.T) {
	span := Span{Offset: 77, Length: 19}
	for _, matrix := range [][6]float64{{}, {1, 2, 2, 4, 0, 0}, {math.NaN(), 0, 0, 1, 0, 0},
		{1, 0, 0, 1, math.Inf(1), 0}} {
		if err := validateCFFMatrix(matrix, span); !errors.Is(err, ErrMalformed) {
			t.Fatal("invalid matrix rank or finite operand was accepted")
		}
	}
	for _, matrix := range [][6]float64{{1e-300, 0, 0, 1e-300, 0, 0},
		{1e300, 0, 0, 1e300, 0, 0}, {-1, 0, 0, 1, 0, 0}, {0, 1, -1, 0, 7, -13},
		{1, 1, 1, math.Nextafter(1, 2), 0, 0}} {
		if err := validateCFFMatrix(matrix, span); err != nil {
			t.Fatal("finite nonsingular affine was narrowed by determinant floating-point overflow or underflow")
		}
	}
	_, err := composeCFFMatrix([6]float64{1e308, 0, 0, 1, 0, 0}, cffIdentityMatrix, 1000, span)
	if !errors.Is(err, ErrLimit) {
		t.Fatal("normalized coefficient overflow was not rejected")
	}
}
