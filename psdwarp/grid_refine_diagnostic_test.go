package psdwarp

// This file contains independent diagnostic measurements, not a production rendering candidate.
// It reads only live placement metadata and writes a fresh report only when explicitly requested.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"image"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

type refineRasterObservation struct {
	Name                     string        `json:"name"`
	Input                    string        `json:"metadata_psd"`
	InputSHA                 string        `json:"metadata_psd_sha256"`
	LayerIndex               int           `json:"physical_layer_index"`
	Source                   [2]int        `json:"source_size"`
	Order                    int           `json:"control_order"`
	Lattice                  [2]int        `json:"lattice_cells"`
	SourceStep               [2]float64    `json:"source_pixel_step"`
	MaxInterpolation         float64       `json:"maximum_forward_bilinear_difference_px"`
	RMSInterpolation         float64       `json:"rms_forward_bilinear_difference_px"`
	InterpolationSamples     int           `json:"interpolation_samples"`
	CurvatureBound           float64       `json:"global_second_derivative_interpolation_bound_px"`
	MaximumInverseResidual   float64       `json:"maximum_cell_inverse_algebra_residual_px"`
	MaximumAnalyticResidual  float64       `json:"maximum_analytic_inverse_roundtrip_px"`
	SharedEdgeSamples        int           `json:"shared_edge_samples"`
	SharedEdgeBothCovered    int           `json:"shared_edge_both_covered"`
	SharedEdgeNeitherCovered int           `json:"shared_edge_neither_covered"`
	SharedEdgeUVError        float64       `json:"maximum_shared_edge_whole_source_uv_difference_pixels"`
	InternalCoverageError    float64       `json:"maximum_internal_edge_coverage_error"`
	MarkerNeighborhoodMax    float64       `json:"maximum_7x7_marker_neighborhood_interpolation_error_px"`
	MarkerNeighborhoodRMS    float64       `json:"rms_7x7_marker_neighborhood_interpolation_error_px"`
	MarkerNeighborhoods      int           `json:"fixed_marker_neighborhoods"`
	ROIIdentical             bool          `json:"absolute_roi_identical"`
	ROISourceContract        string        `json:"roi_source_contract"`
	SourceKnots              [2][5]float64 `json:"independent_physical_source_knots"`
	SourceModel              string        `json:"independent_source_parameter_model"`
	ProductionForwardError   float64       `json:"maximum_production_vs_independent_forward_px"`
}

func TestGridRefineIndependentRasterReview(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	fixtures := []struct {
		name, path string
		index      int
	}{
		{"real-10", "output/native-uniform-grid/v2/real-10-input.psd", 2},
		{"regularized-real-13", "output/native-uniform-grid/v3/regularized-13-half-affine-input.psd", 3},
		{"authored-gentle-c0-10", "output/native-uniform-grid-expanded/authored-v1/matrix-v1/native/" +
			"authored-10-gentle-c0-sparse-markers.psd", 0},
		{"authored-gentle-c0-13", "output/native-uniform-grid-expanded/authored-v1/matrix-v1/native/" +
			"authored-13-gentle-c0-sparse-markers.psd", 0},
	}
	var observations []refineRasterObservation
	for _, fixture := range fixtures {
		path := filepath.Join(root, fixture.path)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			t.Fatal(err)
		}
		source, err := psdfile.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		metadata, err := source.LayerMetadata(nil, fixture.index)
		if err != nil {
			source.Close()
			t.Fatal(err)
		}
		physicalIndex := fixture.index
		if metadata.Placed == nil {
			found := 0
			for index := range source.Layers {
				candidate, err := source.LayerMetadata(nil, index)
				if err != nil {
					source.Close()
					t.Fatal(err)
				}
				if candidate.Placed != nil && candidate.Placed.Warp != nil && candidate.Placed.Warp.Split {
					metadata, physicalIndex = candidate, index
					found++
				}
			}
			if found != 1 {
				source.Close()
				t.Fatalf("%s has %d unique quilt placements", fixture.name, found)
			}
		}
		source.Close()
		if metadata.Placed == nil {
			t.Fatalf("%s has no placement", fixture.name)
		}
		observation := refineMeasureRaster(t, fixture.name, metadata.Placed)
		observation.LayerIndex = physicalIndex
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		observation.Input, observation.InputSHA = path, hex.EncodeToString(digest[:])
		observations = append(observations, observation)
		t.Logf("%s: maximum interpolation=%g bound=%g marker=%g inverse=%g edgeUV=%g",
			observation.Name, observation.MaxInterpolation, observation.CurvatureBound,
			observation.MarkerNeighborhoodMax, observation.MaximumInverseResidual, observation.SharedEdgeUVError)
	}
	if len(observations) == 0 {
		t.Skip("owned optional native fixtures are absent")
	}
	if out := os.Getenv("PSDKIT_GRID_RASTER_REVIEW"); out != "" {
		if err := os.MkdirAll(out, 0755); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(map[string]any{"observations": observations,
			"method": "Fixed metadata-only cell review; no Photoshop pixels, fitting or parameter search.",
			"interpretation": "Maximum forward interpolation error is not a bound on thresholded discrete raster centroid; " +
				"curvature bound certifies the continuous geometric approximation only."}, jsontext.WithIndent("  "), json.Deterministic(true))
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(filepath.Join(out, "report.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(append(encoded, '\n')); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func refineMeasureRaster(t *testing.T, name string, placed *psdmodel.Placed) refineRasterObservation {
	t.Helper()
	analytic := refinePhysicalGrid(placed)
	grid, err := CompileUniformGrid(nil, placed)
	if err != nil {
		t.Fatal(err)
	}
	whole, err := grid.Bounds(image.Rectangle{})
	if err != nil {
		t.Fatal(err)
	}
	width, height := placed.Width, placed.Height
	pixels := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			index := y*pixels.Stride + x*4
			pixels.Pix[index] = byte((x*17 + y*3) % 256)
			pixels.Pix[index+1] = byte((x*5 + y*19) % 256)
			pixels.Pix[index+2] = byte((x*11 + y*13) % 256)
			pixels.Pix[index+3] = byte(17 + (x*7+y*11)%239)
			if (x/11+y/13)%7 == 0 {
				pixels.Pix[index+3] = 0
			}
		}
	}
	plan, err := grid.raster(context.Background(), pixels, whole)
	if err != nil {
		t.Fatal(err)
	}
	nx, ny := max(1, (min(width, whole.Dx())-1)/4+1), max(1, (min(height, whole.Dy())-1)/4+1)
	nx, ny = grid.count*((nx-1)/grid.count+1), grid.count*((ny-1)/grid.count+1)
	observation := refineRasterObservation{Name: name, Source: [2]int{width, height}, Order: placed.Warp.DeformRows,
		Lattice: [2]int{nx, ny}, SourceStep: [2]float64{float64(width) / float64(nx), float64(height) / float64(ny)}}
	observation.ROISourceContract = "Deterministic nonconstant RGB and partial/zero alpha; " +
		"full versus clipped absolute frames"
	observation.SourceKnots = analytic.knots
	observation.SourceModel = "Complete typed slice window normalized to unit endpoints; independent de Casteljau"
	cells := map[[2]int]*rasterCell{}
	squared := 0.0
	for index := range plan.cells {
		cell := &plan.cells[index]
		key := analytic.cellIndex(cell.u+cell.du/2, cell.v+cell.dv/2, nx, ny)
		if cells[key] != nil {
			t.Fatal("physical-source lattice indexing is not unique")
		}
		cells[key] = cell
		for sampleY := 1; sampleY < 8; sampleY++ {
			for sampleX := 1; sampleX < 8; sampleX++ {
				s, v := float64(sampleX)/8, float64(sampleY)/8
				u, w := cell.u+s*cell.du, cell.v+v*cell.dv
				exact := analytic.point(u, w)
				production, err := grid.Forward(u, w)
				if err != nil {
					t.Fatal(err)
				}
				observation.ProductionForwardError = max(observation.ProductionForwardError,
					math.Hypot(exact.X-production.X, exact.Y-production.Y))
				linear := refineCellPoint(cell, s, v)
				distance := math.Hypot(exact.X-linear.X, exact.Y-linear.Y)
				observation.MaxInterpolation = max(observation.MaxInterpolation, distance)
				squared += distance * distance
				observation.InterpolationSamples++
				actualU, actualV, inside := cell.inverse(linear)
				if !inside {
					t.Fatal("own bilinear interior was rejected")
				}
				residual := refineCellPoint(cell, (actualU-cell.u)/cell.du, (actualV-cell.v)/cell.dv)
				observation.MaximumInverseResidual = max(observation.MaximumInverseResidual,
					math.Hypot(residual.X-linear.X, residual.Y-linear.Y))
			}
		}
	}
	observation.RMSInterpolation = math.Sqrt(squared / float64(observation.InterpolationSamples))
	observation.CurvatureBound = analytic.curvatureBound(nx, ny)
	if observation.MaxInterpolation > observation.CurvatureBound+1e-9 {
		t.Fatal("analytic interpolation bound violated")
	}
	for key, cell := range cells {
		for axis := range 2 {
			neighborKey := key
			neighborKey[axis]++
			neighbor, exists := cells[neighborKey]
			if !exists {
				continue
			}
			for sample := 1; sample < 8; sample++ {
				phase := float64(sample) / 8
				s, v := 1.0, phase
				if axis == 1 {
					s, v = phase, 1
				}
				point := refineCellPoint(cell, s, v)
				u0, v0, firstInside := cell.inverse(point)
				u1, v1, secondInside := neighbor.inverse(point)
				observation.SharedEdgeSamples++
				if !firstInside && !secondInside {
					observation.SharedEdgeNeitherCovered++
					continue
				}
				if firstInside && secondInside {
					observation.SharedEdgeBothCovered++
					observation.SharedEdgeUVError = max(observation.SharedEdgeUVError,
						math.Hypot((u0-u1)*float64(width), (v0-v1)*float64(height)))
				}
				if key[0] > 0 && key[0] < nx-1 && key[1] > 0 && key[1] < ny-1 && firstInside {
					observation.InternalCoverageError = max(observation.InternalCoverageError,
						math.Abs(cell.edgeCoverage(point, pixels, u0, v0)-1))
				}
			}
		}
	}
	markerSquared, markerSamples := 0.0, 0
	for row := 1; row <= 5; row++ {
		for column := 1; column <= 5; column++ {
			cx, cy := column*width/6, row*height/6
			observation.MarkerNeighborhoods++
			for sy := cy - 3; sy <= cy+3; sy++ {
				for sx := cx - 3; sx <= cx+3; sx++ {
					u, v := (float64(sx)+.5)/float64(width), (float64(sy)+.5)/float64(height)
					key := analytic.cellIndex(u, v, nx, ny)
					cell := cells[key]
					if cell == nil {
						t.Fatal("marker cell absent")
					}
					exact := analytic.point(u, v)
					linear := refineCellPoint(cell, (u-cell.u)/cell.du, (v-cell.v)/cell.dv)
					distance := math.Hypot(exact.X-linear.X, exact.Y-linear.Y)
					observation.MarkerNeighborhoodMax = max(observation.MarkerNeighborhoodMax, distance)
					markerSquared += distance * distance
					markerSamples++
				}
			}
			u, v := (float64(cx)+.5)/float64(width), (float64(cy)+.5)/float64(height)
			point := analytic.point(u, v)
			actualU, actualV, inside, err := grid.Inverse(nil, point)
			if err != nil || !inside {
				t.Fatalf("analytic inverse: %v", err)
			}
			roundtrip := analytic.point(actualU, actualV)
			observation.MaximumAnalyticResidual = max(observation.MaximumAnalyticResidual,
				math.Hypot(point.X-roundtrip.X, point.Y-roundtrip.Y))
		}
	}
	observation.MarkerNeighborhoodRMS = math.Sqrt(markerSquared / float64(markerSamples))
	full, origin, err := grid.Project(nil, pixels, whole)
	if err != nil {
		t.Fatal(err)
	}
	crop := image.Rect(whole.Min.X+whole.Dx()/3, whole.Min.Y+whole.Dy()/3,
		whole.Min.X+2*whole.Dx()/3, whole.Min.Y+2*whole.Dy()/3)
	regional, regionalOrigin, err := grid.Project(nil, pixels, crop)
	if err != nil {
		t.Fatal(err)
	}
	observation.ROIIdentical = true
	for y := 0; y < regional.Rect.Dy(); y++ {
		for x := 0; x < regional.Rect.Dx(); x++ {
			if regional.NRGBAAt(x, y) != full.NRGBAAt(x+regionalOrigin.X-origin.X, y+regionalOrigin.Y-origin.Y) {
				observation.ROIIdentical = false
			}
		}
	}
	if observation.SharedEdgeNeitherCovered != 0 || observation.SharedEdgeUVError > 1e-6 ||
		observation.MaximumInverseResidual > 1e-6 || !observation.ROIIdentical ||
		observation.ProductionForwardError > 1e-8 {
		t.Fatal("cell inversion, edge continuity or absolute phase invariant failed")
	}
	return observation
}

func refineCellPoint(cell *rasterCell, s, v float64) psdmodel.Point {
	weights := [4]float64{(1 - s) * (1 - v), s * (1 - v), s * v, (1 - s) * v}
	var point psdmodel.Point
	for index, weight := range weights {
		point.X += weight * cell.points[index].X
		point.Y += weight * cell.points[index].Y
	}
	return point
}

func (analytic refineAnalyticGrid) curvatureBound(nx, ny int) float64 {
	bound := 0.0
	for rowPiece := 0; rowPiece < analytic.count; rowPiece++ {
		for columnPiece := 0; columnPiece < analytic.count; columnPiece++ {
			maximumU, maximumV := 0.0, 0.0
			var points [16]psdmodel.Point
			for row := range 4 {
				for column := range 4 {
					points[row*4+column] = analytic.controls[(rowPiece*3+row)*analytic.order+columnPiece*3+column]
				}
			}
			for row := range 4 {
				for column := range 2 {
					first, middle, last := points[row*4+column], points[row*4+column+1], points[row*4+column+2]
					maximumU = max(maximumU, 6*math.Hypot(last.X-2*middle.X+first.X, last.Y-2*middle.Y+first.Y))
				}
			}
			for row := range 2 {
				for column := range 4 {
					first, middle, last := points[row*4+column], points[(row+1)*4+column], points[(row+2)*4+column]
					maximumV = max(maximumV, 6*math.Hypot(last.X-2*middle.X+first.X, last.Y-2*middle.Y+first.Y))
				}
			}
			spanU := analytic.knots[0][columnPiece+1] - analytic.knots[0][columnPiece]
			spanV := analytic.knots[1][rowPiece+1] - analytic.knots[1][rowPiece]
			stepU, stepV := spanU*float64(analytic.count)/float64(nx), spanV*float64(analytic.count)/float64(ny)
			bound = max(bound, (maximumU*stepU*stepU/(spanU*spanU)+maximumV*stepV*stepV/(spanV*spanV))/8)
		}
	}
	return bound
}

type refineAnalyticGrid struct {
	knots        [2][5]float64
	controls     []psdmodel.Point
	count, order int
}

func refinePhysicalGrid(placed *psdmodel.Placed) refineAnalyticGrid {
	result := refineAnalyticGrid{order: placed.Warp.DeformColumns, count: (placed.Warp.DeformColumns - 1) / 3}
	for _, slice := range placed.Warp.QuiltSlices {
		axis := 0
		if slice.Axis == "quiltSliceY" {
			axis = 1
		}
		first, last := slice.Values[0], slice.Values[result.count]
		for index := 0; index <= result.count; index++ {
			result.knots[axis][index] = (slice.Values[index] - first) / (last - first)
		}
	}
	hull := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, point := range placed.Warp.MeshPoints {
		hull[0] = min(hull[0], point.X)
		hull[1] = min(hull[1], point.Y)
		hull[2] = max(hull[2], point.X)
		hull[3] = max(hull[3], point.Y)
	}
	quad := placed.NonAffineTransform
	if quad == nil {
		quad = placed.Transform
	}
	for _, point := range placed.Warp.MeshPoints {
		x, y := (point.X-hull[0])/(hull[2]-hull[0]), (point.Y-hull[1])/(hull[3]-hull[1])
		result.controls = append(result.controls, psdmodel.Point{X: quad[0] + x*(quad[2]-quad[0]) + y*(quad[6]-quad[0]),
			Y: quad[1] + x*(quad[3]-quad[1]) + y*(quad[7]-quad[1])})
	}
	return result
}

func (analytic refineAnalyticGrid) parameter(axis int, coordinate float64) (int, float64) {
	piece := 0
	for piece+1 < analytic.count && coordinate >= analytic.knots[axis][piece+1] {
		piece++
	}
	return piece, (coordinate - analytic.knots[axis][piece]) /
		(analytic.knots[axis][piece+1] - analytic.knots[axis][piece])
}

func (analytic refineAnalyticGrid) cellIndex(u, v float64, nx, ny int) [2]int {
	column, localU := analytic.parameter(0, u)
	row, localV := analytic.parameter(1, v)
	perX, perY := nx/analytic.count, ny/analytic.count
	return [2]int{column*perX + min(perX-1, max(0, int(localU*float64(perX)))),
		row*perY + min(perY-1, max(0, int(localV*float64(perY))))}
}

func (analytic refineAnalyticGrid) point(u, v float64) psdmodel.Point {
	column, localU := analytic.parameter(0, u)
	row, localV := analytic.parameter(1, v)
	var intermediate [4]psdmodel.Point
	for y := range 4 {
		var controls [4]psdmodel.Point
		for x := range 4 {
			controls[x] = analytic.controls[(row*3+y)*analytic.order+column*3+x]
		}
		intermediate[y] = refineCasteljau(controls, localU)
	}
	return refineCasteljau(intermediate, localV)
}

func refineCasteljau(points [4]psdmodel.Point, phase float64) psdmodel.Point {
	for count := 3; count > 0; count-- {
		for index := 0; index < count; index++ {
			points[index].X += phase * (points[index+1].X - points[index].X)
			points[index].Y += phase * (points[index+1].Y - points[index].Y)
		}
	}
	return points[0]
}
