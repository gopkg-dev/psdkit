package psdfont

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"unsafe"
)

type cffMatrixOracleGlyph struct {
	cffOracleGlyph
	Matrix [6]float64
	FD     *int
}

type cffMatrixOracleFont struct {
	cffOracleFont
	Name       string
	Units      uint16 `json:"units_per_em"`
	TopPresent bool   `json:"top_present"`
	Top        [6]float64
	FDs        []*[6]float64
	Glyphs     []cffMatrixOracleGlyph
}

func cffMatrixOracle(t *testing.T) []cffMatrixOracleFont {
	t.Helper()
	data, err := os.ReadFile("testdata/cff-matrix/oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct{ Fonts []cffMatrixOracleFont }
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil || len(oracle.Fonts) != 14 {
		t.Fatalf("fourteen portable affine fonts required: %v", err)
	}
	return oracle.Fonts
}

func cffMatrixFace(t *testing.T, row cffMatrixOracleFont) *Face {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata/cff-matrix/fonts", row.File))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != row.SHA256 {
		t.Fatal("matrix input bytes differ")
	}
	face, err := Decode(nil, data)
	if err != nil || face == nil {
		t.Fatalf("matrix real font decode: %v", err)
	}
	return face
}

func cffMatrixNear(actual, expected float64) bool {
	return math.Abs(actual-expected) <= 1e-9+math.Abs(expected)*1e-13
}

func TestCFFMatrixPortableFractionGeometryMetricsAndRawSpans(t *testing.T) {
	count := 0
	for _, row := range cffMatrixOracle(t) {
		t.Run(row.File, func(t *testing.T) {
			face := cffMatrixFace(t, row)
			if face.UnitsPerEm != row.Units || face.PostScriptName != row.Name ||
				face.cff.topMatrixPresent != row.TopPresent || row.TopPresent && face.cff.rawTopMatrix != row.Top {
				t.Fatal("raw Top presence or head design domain changed")
			}
			if row.TopPresent {
				span := face.cff.topMatrixSpan
				data, err := face.ReadSpan(nil, span)
				if err != nil || len(data) < 8 || data[len(data)-2] != 12 || data[len(data)-1] != 7 {
					t.Fatal("Top FontMatrix original operands/operator span lost provenance")
				}
			}
			for _, want := range row.Glyphs {
				glyph, err := face.Glyph(nil, want.ID)
				if err != nil || glyph == nil {
					t.Fatalf("glyph=%d: %v", want.ID, err)
				}
				count++
				if glyph.AdvanceWidth != want.Advance || glyph.LeftSideBearing != want.Bearing ||
					glyph.CharstringWidth != want.Width || len(glyph.Path) != len(want.Path) {
					t.Fatalf("glyph=%d raw hmtx, width or path count changed", want.ID)
				}
				selected := 0
				if want.FD != nil {
					selected = *want.FD
				}
				private := face.cff.private[selected]
				if private.matrix != want.Matrix {
					t.Fatalf("glyph=%d exact-binary normalized matrix=%v want=%v", want.ID, private.matrix, want.Matrix)
				}
				if want.FD != nil {
					fd := row.FDs[selected]
					if private.fdMatrixPresent != (fd != nil) || fd != nil && private.rawFDMatrix != *fd {
						t.Fatal("FD matrix presence/default was replaced")
					}
					if fd != nil {
						data, err := face.ReadSpan(nil, private.fdMatrixSpan)
						if err != nil || len(data) < 8 || data[len(data)-2] != 12 || data[len(data)-1] != 7 {
							t.Fatal("FD FontMatrix original span lost provenance")
						}
					}
				}
				data, err := face.ReadSpan(nil, glyph.Source)
				if err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(data)
				if hex.EncodeToString(digest[:]) != want.SourceSHA {
					t.Fatal("FontMatrix rewrote the original Type2 SourceSpan")
				}
				for index, command := range glyph.Path {
					expected := want.Path[index]
					op := map[PathOp]string{Move: "moveTo", Line: "lineTo", Cubic: "curveTo", Close: "closePath"}
					if op[command.Op] != expected.Op {
						t.Fatal("matrix changed raw path operator order")
					}
					if command.Op == Close {
						continue
					}
					points := [][2]float64{{command.X, command.Y}}
					if command.Op == Cubic {
						points = [][2]float64{{command.ControlX, command.ControlY},
							{command.Control2X, command.Control2Y}, {command.X, command.Y}}
					}
					for point, actual := range points {
						if !cffMatrixNear(actual[0], expected.Points[point][0]) ||
							!cffMatrixNear(actual[1], expected.Points[point][1]) {
							t.Fatalf("glyph=%d path=%d point=%d differs from Fraction geometry", want.ID, index, point)
						}
					}
				}
				bounds := [4]int16{}
				if len(want.Bounds) != 0 {
					bounds = [4]int16{int16(math.Floor(want.Bounds[0])), int16(math.Floor(want.Bounds[1])),
						int16(math.Ceil(want.Bounds[2])), int16(math.Ceil(want.Bounds[3]))}
				}
				if glyph.Bounds != bounds {
					t.Fatalf("glyph=%d outward FWORD curve bounds=%v want=%v", want.ID, glyph.Bounds, bounds)
				}
			}
		})
	}
	if count != 235 {
		t.Fatal("matrix glyph controls were empty or truncated")
	}
}

func TestCFFMatrixMetadataBudgetsCancellationAndSharedScratch(t *testing.T) {
	for _, row := range cffMatrixOracle(t) {
		face := cffMatrixFace(t, row)
		if actual, err := Decode(nil, face.raw, DecodeOptions{MaxBytes: face.reservation}); err != nil || actual == nil {
			t.Fatalf("font=%s decode peak must pass: %v", row.File, err)
		}
		if actual, err := Decode(nil, face.raw, DecodeOptions{MaxBytes: face.reservation - 1}); actual != nil ||
			!errors.Is(err, ErrLimit) {
			t.Fatalf("font=%s decode peak-1 must reject: %v", row.File, err)
		}
	}
	for _, index := range []int{1, 9, 13} {
		face := cffMatrixFace(t, cffMatrixOracle(t)[index])
		base, cancel := context.WithCancel(context.Background())
		counted := &fontCancelContext{Context: base, cancel: cancel, after: int(^uint(0) >> 1)}
		if actual, err := Decode(counted, face.raw); err != nil || actual == nil {
			t.Fatal(err)
		}
		checks := counted.calls
		cancel()
		for stop := 1; stop <= checks; stop++ {
			base, cancel := context.WithCancel(context.Background())
			ctx := &fontCancelContext{Context: base, cancel: cancel, after: stop}
			actual, err := Decode(ctx, face.raw)
			cancel()
			if actual != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("every metadata cancellation check=%d/%d: %v", stop, checks, err)
			}
		}
	}
	top := cffDict{hasMatrix: true, matrix: [6]float64{.001, .00025, -.000125, .00075, .015625, -.0078125}}
	fd := cffDict{hasMatrix: true, matrix: [6]float64{1.25, .5, -.25, .75, 3, -2}}
	p := cffParser{ctx: context.Background(), face: &Face{UnitsPerEm: 1000},
		limits: DecodeOptions{MaxBytes: cffMatrixScratchBytes, MaxCFFOperations: 2048}}
	for range 256 {
		if _, err := p.resolveMatrix(top, &fd); err != nil {
			t.Fatal(err)
		}
	}
	if p.face.reservation != cffMatrixScratchBytes || p.matrixScratch == nil || p.operations != 512 {
		t.Fatal("scratch must be reserved once and matrix metadata traversal must remain bounded")
	}
	retained := int64(unsafe.Sizeof(*p.matrixScratch))
	for _, rat := range []*big.Rat{&p.matrixScratch.first, &p.matrixScratch.second,
		&p.matrixScratch.left, &p.matrixScratch.right} {
		retained += int64(cap(rat.Num().Bits())+cap(rat.Denom().Bits())) * int64(unsafe.Sizeof(big.Word(0)))
	}
	if retained > cffMatrixScratchBytes {
		t.Fatal("scratch reservation does not cover its fixed struct")
	}
	for stop := 1; stop <= 3; stop++ {
		base, cancel := context.WithCancel(context.Background())
		ctx := &fontCancelContext{Context: base, cancel: cancel, after: stop}
		p := cffParser{ctx: ctx, face: &Face{UnitsPerEm: 1000},
			limits: DecodeOptions{MaxBytes: cffMatrixScratchBytes, MaxCFFOperations: 10}}
		matrix, err := p.resolveMatrix(top, &fd)
		cancel()
		if matrix != [6]float64{} || !errors.Is(err, context.Canceled) {
			t.Fatal("matrix resolution returned partial metadata at a cancellation boundary")
		}
	}
	canonical := cffParser{ctx: context.Background(), face: &Face{UnitsPerEm: 1000},
		limits: DecodeOptions{MaxBytes: 1, MaxCFFOperations: 1}}
	if matrix, err := canonical.resolveMatrix(cffDict{}, nil); err != nil || matrix != cffIdentityMatrix ||
		canonical.matrixScratch != nil || canonical.face.reservation != 0 || canonical.operations != 0 {
		t.Fatal("canonical old identity allocated general arithmetic scratch or changed traversal")
	}
}

func TestCFFMatrixDefault2048EquivalentRawMetrics(t *testing.T) {
	rows := cffMatrixOracle(t)
	a, b := cffMatrixFace(t, rows[4]), cffMatrixFace(t, rows[5])
	ga, err := a.Glyph(nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	gb, err := b.Glyph(nil, 1)
	if err != nil || !reflect.DeepEqual(ga.Path, gb.Path) || !reflect.DeepEqual(ga.Contours, gb.Contours) ||
		ga.AdvanceWidth != 1280 || gb.AdvanceWidth != 1280 || ga.LeftSideBearing != 256 || gb.LeftSideBearing != 256 ||
		ga.CharstringWidth != 625 || gb.CharstringWidth != 625 || a.UnitsPerEm != 2048 || b.UnitsPerEm != 2048 {
		t.Fatal("explicit .001 and data-default .001 diverged or hmtx/head/Type2 width was renormalized")
	}
}

func TestCFFMatrixCollectionGeometryAndAbsoluteSource(t *testing.T) {
	data, err := os.ReadFile("testdata/cff-matrix/oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Collection struct {
			File, SHA256 string
			Fonts        []string
		}
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	collection, err := os.ReadFile(filepath.Join("testdata/cff-matrix/fonts", oracle.Collection.File))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(collection)
	if hex.EncodeToString(digest[:]) != oracle.Collection.SHA256 || len(oracle.Collection.Fonts) != 2 {
		t.Fatal("matrix collection input changed")
	}
	fonts := cffMatrixOracle(t)
	count := 0
	for index, filename := range oracle.Collection.Fonts {
		var row cffMatrixOracleFont
		for _, candidate := range fonts {
			if candidate.File == filename {
				row = candidate
			}
		}
		standalone := cffMatrixFace(t, row)
		selected, err := Decode(nil, collection, DecodeOptions{FaceIndex: index})
		if err != nil || selected == nil {
			t.Fatal(err)
		}
		for _, expected := range row.Glyphs {
			actual, err := selected.Glyph(nil, expected.ID)
			if err != nil {
				t.Fatal(err)
			}
			want, err := standalone.Glyph(nil, expected.ID)
			if err != nil || !reflect.DeepEqual(actual.Path, want.Path) || !reflect.DeepEqual(actual.Contours, want.Contours) ||
				actual.Bounds != want.Bounds || actual.AdvanceWidth != want.AdvanceWidth ||
				actual.LeftSideBearing != want.LeftSideBearing || actual.CharstringWidth != want.CharstringWidth {
				t.Fatal("matrix collection selected face geometry/metrics differs")
			}
			raw, err := selected.ReadSpan(nil, actual.Source)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(raw)
			if hex.EncodeToString(digest[:]) != expected.SourceSHA {
				t.Fatal("matrix selected collection Type2 original absolute span differs")
			}
			count++
		}
	}
	if count != 33 {
		t.Fatal("matrix collection controls were truncated")
	}
}

func TestCFFMatrixExistingFWORDAndCoordinateLimits(t *testing.T) {
	for _, translate := range []float64{-32768, 32767} {
		face := cffTestProgram([]byte{139, 139, 21, 14}, nil)
		face.cff.private[0].matrix = [6]float64{1, 0, 0, 1, translate, translate}
		glyph, err := face.Glyph(nil, 0)
		if err != nil || glyph == nil || glyph.Bounds != [4]int16{
			int16(translate), int16(translate), int16(translate), int16(translate)} {
			t.Fatal("representable FWORD endpoint was narrowed by FontMatrix")
		}
	}
	for _, translate := range []float64{-32769, 32768, 1<<30 + 1, -(1 << 30) - 1, math.Inf(1)} {
		face := cffTestProgram([]byte{139, 139, 21, 14}, nil)
		face.cff.private[0].matrix = [6]float64{1, 0, 0, 1, translate, 0}
		glyph, err := face.Glyph(nil, 0)
		if glyph != nil || !errors.Is(err, ErrLimit) {
			t.Fatal("unrepresentable transformed FWORD/coordinate returned partial glyph")
		}
	}
}
