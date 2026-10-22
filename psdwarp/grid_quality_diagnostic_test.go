//go:build psdkit_grid_diagnostics

package psdwarp

import (
	"context"
	"encoding/json/v2"
	"image"
	"math"
	"os"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestGridQualityNativeDiagnostic(t *testing.T) {
	path := os.Getenv("PSDKIT_GRID_DIAGNOSTIC_INPUT")
	if path == "" {
		t.Skip("requires an explicitly selected read-only native input")
	}
	document, err := psdfile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	metadata, err := document.LayerMetadata(nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	grid, err := CompileUniformGrid(nil, metadata.Placed)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	width, height := metadata.Placed.Width, metadata.Placed.Height
	referenceOrigin := "compiled-source-domain"
	if os.Getenv("PSDKIT_GRID_DIAGNOSTIC_REFERENCE") == "expanded" {
		reference, err := grid.qualityReference(context.Background(), image.Pt(width, height))
		if err != nil {
			gridQualityDiagnosticWrite(t, map[string]any{"source": path, "source_size": [2]int{width, height},
				"reference_origin": "expanded-source-domain", "proposal_error": err.Error(),
				"source_geometry_unchanged": true, "sampling_does_not_prove_positivity": true})
			return
		}
		grid.reference = reference
		referenceOrigin = "expanded-source-domain"
	}
	marginU, marginV := 3.5/float64(width), 3.5/float64(height)
	for row := range grid.count {
		for column := range grid.count {
			lowU, highU, lowV, highV := 0.0, 1.0, 0.0, 1.0
			if column == 0 {
				lowU -= marginU * float64(grid.count)
			}
			if column == grid.count-1 {
				highU += marginU * float64(grid.count)
			}
			if row == 0 {
				lowV -= marginV * float64(grid.count)
			}
			if row == grid.count-1 {
				highV += marginV * float64(grid.count)
			}
			piece := grid.pieces[row*grid.count+column]
			piece.controls = gridReparameterize(piece.controls, lowU, highU, lowV, highV)
			work := 0
			certificate := certifyGridPiece(context.Background(), &piece, grid.reference, grid.count, &work)
			lowestU, lowestV, lowestDet := math.Inf(1), math.Inf(1), math.Inf(1)
			var atU [2]float64
			for y := range 65 {
				for x := range 65 {
					u, v := float64(x)/64, float64(y)/64
					_, du, dv := piece.surface(u, v)
					ux := grid.reference[0]*du.X + grid.reference[1]*du.Y
					uy := grid.reference[2]*du.X + grid.reference[3]*du.Y
					vx := grid.reference[0]*dv.X + grid.reference[1]*dv.Y
					vy := grid.reference[2]*dv.X + grid.reference[3]*dv.Y
					if ux < lowestU {
						lowestU, atU = ux, [2]float64{lowU + u*(highU-lowU), lowV + v*(highV-lowV)}
					}
					lowestV = math.Min(lowestV, vy)
					lowestDet = math.Min(lowestDet, ux*vy-uy*vx)
				}
			}
			message := ""
			if certificate != nil {
				message = certificate.Error()
			}
			rows = append(rows, map[string]any{"column": column, "row": row, "certificate": message,
				"sampled_min_u_principal_minor": lowestU, "sampled_min_v_principal_minor": lowestV,
				"sampled_min_determinant": lowestDet, "minimum_u_local_uv": atU})
		}
	}
	gridQualityDiagnosticWrite(t, map[string]any{"reference": grid.reference, "reference_origin": referenceOrigin,
		"source":      path,
		"source_size": [2]int{width, height}, "sampling_does_not_prove_positivity": true, "pieces": rows})
}

func gridQualityDiagnosticWrite(t *testing.T, value any) {
	t.Helper()
	encoded, err := json.Marshal(value, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	if output := os.Getenv("PSDKIT_GRID_DIAGNOSTIC_OUTPUT"); output != "" {
		if err := os.WriteFile(output, append(encoded, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(encoded))
}
