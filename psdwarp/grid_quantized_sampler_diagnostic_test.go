package psdwarp

// This diagnostic evaluates one fixed 1/256 phase-quantization hypothesis without changing production sampling.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

type quant256Prediction struct {
	ID         string `json:"id"`
	Source     string `json:"prediction_source_png"`
	SourceSHA  string `json:"prediction_source_png_sha256"`
	SourceRGBA string `json:"prediction_source_rgba_sha256"`
}

type quant256Native struct{ ID, PSD, PNG string }

type quant256Moment struct {
	Samples                   int64 `json:"samples_per_channel"`
	L1, SSE, Maximum          [4]float64
	MAE, RMS                  [4]float64
	RGBAMAE, RGBARMS, RGBAMax float64
}

func (metric *quant256Moment) add(first, second [4]float64) {
	metric.Samples++
	for channel := range first {
		difference := math.Abs(first[channel] - second[channel])
		metric.L1[channel] += difference
		metric.SSE[channel] += difference * difference
		metric.Maximum[channel] = max(metric.Maximum[channel], difference)
	}
}

func (metric *quant256Moment) finish() {
	if metric.Samples == 0 {
		return
	}
	sum, squared := 0.0, 0.0
	for channel := range metric.L1 {
		metric.MAE[channel] = metric.L1[channel] / float64(metric.Samples)
		metric.RMS[channel] = math.Sqrt(metric.SSE[channel] / float64(metric.Samples))
		sum += metric.L1[channel]
		squared += metric.SSE[channel]
		metric.RGBAMax = max(metric.RGBAMax, metric.Maximum[channel])
	}
	metric.RGBAMAE = sum / float64(metric.Samples*4)
	metric.RGBARMS = math.Sqrt(squared / float64(metric.Samples*4))
}

type quant256Metric struct {
	Baseline, Candidate       quant256Moment
	Improved, Worsened, Equal int64
}

type quant256Guard struct {
	file   *os.File
	spans  []psdfile.Span
	denied int
}

func (reader *quant256Guard) ReadAt(target []byte, offset int64) (int, error) {
	for _, span := range reader.spans {
		if offset < span.Offset+span.Length && offset+int64(len(target)) > span.Offset {
			reader.denied++
			return 0, psdfile.ErrUnsupported
		}
	}
	return reader.file.ReadAt(target, offset)
}

func TestGridQuant256Diagnostic(t *testing.T) {
	out := os.Getenv("PSDKIT_GRID_QUANT256_DIAGNOSTIC")
	if out == "" {
		t.Skip("explicit fixed-candidate diagnostic output directory is required")
	}
	if err := os.Mkdir(out, 0755); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	var results []map[string]any
	bindings := map[string]string{}
	for _, packet := range []struct {
		path, predictions string
		opaque            bool
	}{
		{"output/native-uniform-grid-refine-v1/opaque-causal-v2", "predictions-normalized-sampling-v1.json", true},
		{"output/native-uniform-grid-expanded/authored-v1/dimension-holdout-v1",
			"independent-go-predictions-v1.json", false},
	} {
		var predictions []quant256Prediction
		quant256ReadJSON(t, filepath.Join(root, packet.path, packet.predictions), &predictions, bindings)
		var native struct{ Cases []quant256Native }
		quant256ReadJSON(t, filepath.Join(root, packet.path, "native-report.json"), &native, bindings)
		for _, entry := range predictions {
			if packet.opaque && entry.ID != "causal-10-identity" && entry.ID != "causal-13-identity" {
				continue
			}
			if !packet.opaque && !strings.HasSuffix(entry.ID, "rgba-ramp") {
				continue
			}
			var observed quant256Native
			for _, candidate := range native.Cases {
				if candidate.ID == entry.ID {
					observed = candidate
					break
				}
			}
			if observed.ID == "" {
				t.Fatal("native identity absent")
			}
			pixels := quant256ReadPNG(t, entry.Source, bindings)
			if bindings[entry.Source] != entry.SourceSHA || quant256RGBAHash(pixels) != entry.SourceRGBA {
				t.Fatal("frozen unwarped source binding differs")
			}
			grid, canvas, denied := quant256LoadGrid(t, observed.PSD)
			quant256Bind(t, observed.PSD, bindings)
			baseline, origin, err := grid.Project(nil, pixels, canvas)
			if err != nil {
				t.Fatal(err)
			}
			candidate, candidateOrigin := quant256Project(t, grid, pixels, canvas)
			if candidateOrigin != origin || candidate.Rect != baseline.Rect {
				t.Fatal("quantized sampler changed geometry")
			}
			fullBase, fullCandidate := image.NewNRGBA(canvas), image.NewNRGBA(canvas)
			draw.Draw(fullBase, baseline.Rect.Add(origin), baseline, image.Point{}, draw.Src)
			draw.Draw(fullCandidate, candidate.Rect.Add(origin), candidate, image.Point{}, draw.Src)
			quant256WritePNG(t, filepath.Join(out, entry.ID+"-baseline.png"), fullBase)
			quant256WritePNG(t, filepath.Join(out, entry.ID+"-quant256.png"), fullCandidate)
			// Parent pixels are read for metrics only after both predictions are complete.
			oracle := quant256ReadPNG(t, observed.PNG, bindings)
			if oracle.Rect != canvas {
				t.Fatal("oracle absolute canvas differs")
			}
			full, inner := quant256Compare(t, grid, oracle, fullBase, fullCandidate, pixels.Rect.Size())
			role := "heldout"
			if entry.ID == "causal-10-identity" {
				role = "fixed-training"
			}
			results = append(results, map[string]any{"id": entry.ID, "role": role, "full_visible_union": full,
				"inner_8_source_pixels": inner, "denied_saved_parent_pixel_reads": denied,
				"source_png": entry.Source, "native_metadata_psd": observed.PSD, "native_png": observed.PNG})
			t.Logf("%s %s: RGBA MAE %g -> %g; inner %g -> %g; max %g -> %g",
				role, entry.ID, full.Baseline.RGBAMAE, full.Candidate.RGBAMAE,
				inner.Baseline.RGBAMAE, inner.Candidate.RGBAMAE, full.Baseline.RGBAMax, full.Candidate.RGBAMax)
		}
	}
	if len(results) != 6 {
		t.Fatalf("fixed training/heldout matrix differs: %d", len(results))
	}
	data, err := json.Marshal(map[string]any{"fixed_candidate": "RoundToEven(fx*256)/256 and fy analog; " +
		"same premultiplied bilinear arithmetic, edge coverage and final byte rounding",
		"no_production_sampler_change": true, "cases": results, "input_sha256": bindings}, jsontext.WithIndent("  "), json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "report.json"), append(data, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}

func quant256LoadGrid(t *testing.T, path string) (*UniformGrid, image.Rectangle, int) {
	t.Helper()
	source, err := psdfile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	spans := []psdfile.Span{}
	for _, layer := range source.Layers {
		for _, channel := range layer.Channels {
			span := channel.Data
			if channel.Compression == 1 {
				width := int64(2)
				if source.Header.Version == 2 {
					width = 4
				}
				length := width * int64(channel.Bounds.Dy())
				span.Offset += length
				span.Length -= length
			}
			spans = append(spans, span)
		}
	}
	footer := source.Composite.Data
	if source.Composite.Compression == 1 {
		length := int64(source.Header.Height) * int64(source.Header.Channels) *
			int64(source.Composite.RowCountWidth)
		footer.Offset += length
		footer.Length -= length
	}
	spans = append(spans, footer)
	source.Close()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	guard := &quant256Guard{file: file, spans: spans}
	owned, err := psdfile.Read(guard, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	var placed *psdmodel.Placed
	found := 0
	for index := range owned.Layers {
		metadata, err := owned.LayerMetadata(nil, index)
		if err != nil {
			t.Fatal(err)
		}
		if metadata.Placed != nil && metadata.Placed.Warp != nil && metadata.Placed.Warp.Split {
			placed = metadata.Placed
			found++
		}
	}
	if found != 1 {
		t.Fatal("unique live quilt placement is absent")
	}
	grid, err := CompileUniformGrid(nil, placed)
	if err != nil {
		t.Fatal(err)
	}
	if guard.denied != 0 {
		t.Fatal("saved parent pixels entered geometry")
	}
	return grid, image.Rect(0, 0, int(owned.Header.Width), int(owned.Header.Height)), guard.denied
}

func quant256Project(
	t *testing.T, grid *UniformGrid, source *image.NRGBA, canvas image.Rectangle,
) (*image.NRGBA, image.Point) {
	t.Helper()
	bounds, err := grid.Bounds(canvas)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := grid.raster(context.Background(), source, bounds)
	if err != nil {
		t.Fatal(err)
	}
	result := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := 0; y < result.Rect.Dy(); y++ {
		for x := 0; x < result.Rect.Dx(); x++ {
			point := psdmodel.Point{X: float64(bounds.Min.X+x) + .5, Y: float64(bounds.Min.Y+y) + .5}
			var value [4]float64
			for _, index := range plan.indices[plan.offsets[y]:plan.offsets[y+1]] {
				cell := &plan.cells[index]
				if point.X < float64(cell.box.Min.X) || point.X >= float64(cell.box.Max.X) {
					continue
				}
				u, v, covered := cell.inverse(point)
				if !covered {
					continue
				}
				value = quant256Sample(source, u, v)
				value[3] *= cell.edgeCoverage(point, source, u, v)
				break
			}
			for channel := range value {
				result.Pix[y*result.Stride+x*4+channel] =
					uint8(math.RoundToEven(max(0, min(255, value[channel]))))
			}
		}
	}
	return result, bounds.Min
}

func quant256Sample(source *image.NRGBA, u, v float64) [4]float64 {
	x, y := u*float64(source.Rect.Dx())-.5, v*float64(source.Rect.Dy())-.5
	left, top := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := math.RoundToEven((x-float64(left))*256)/256, math.RoundToEven((y-float64(top))*256)/256
	var result [4]float64
	for row := range 2 {
		for column := range 2 {
			sx, sy := left+column, top+row
			if sx < 0 || sy < 0 || sx >= source.Rect.Dx() || sy >= source.Rect.Dy() {
				continue
			}
			wx, wy := 1-fx, 1-fy
			if column != 0 {
				wx = fx
			}
			if row != 0 {
				wy = fy
			}
			offset := sy*source.Stride + sx*4
			weight := wx * wy * float64(source.Pix[offset+3])
			result[3] += weight
			for channel := range 3 {
				result[channel] += float64(source.Pix[offset+channel]) * weight
			}
		}
	}
	if result[3] > 0 {
		for channel := range 3 {
			result[channel] /= result[3]
		}
	}
	return result
}

func quant256Compare(
	t *testing.T, grid *UniformGrid, native, baseline, candidate *image.NRGBA, size image.Point,
) (quant256Metric, quant256Metric) {
	t.Helper()
	var full, inner quant256Metric
	for y := 0; y < native.Rect.Dy(); y++ {
		for x := 0; x < native.Rect.Dx(); x++ {
			first, second, third := native.NRGBAAt(x, y), baseline.NRGBAAt(x, y), candidate.NRGBAAt(x, y)
			if first.A == 0 && second.A == 0 && third.A == 0 {
				continue
			}
			values := [3][4]float64{}
			for index, pixel := range []struct{ r, g, b, a uint8 }{{first.R, first.G, first.B, first.A},
				{second.R, second.G, second.B, second.A}, {third.R, third.G, third.B, third.A}} {
				values[index] = [4]float64{float64(pixel.r) * float64(pixel.a) / 255,
					float64(pixel.g) * float64(pixel.a) / 255, float64(pixel.b) * float64(pixel.a) / 255, float64(pixel.a)}
			}
			quant256Accumulate(&full, values)
			u, v, inside, err := grid.Inverse(nil, psdmodel.Point{X: float64(x) + .5, Y: float64(y) + .5})
			if err != nil || !inside {
				continue
			}
			sx, sy := u*float64(size.X)-.5, v*float64(size.Y)-.5
			if sx >= 8 && sy >= 8 && sx <= float64(size.X-9) && sy <= float64(size.Y-9) {
				quant256Accumulate(&inner, values)
			}
		}
	}
	full.Baseline.finish()
	full.Candidate.finish()
	inner.Baseline.finish()
	inner.Candidate.finish()
	return full, inner
}

func quant256Accumulate(metric *quant256Metric, values [3][4]float64) {
	metric.Baseline.add(values[0], values[1])
	metric.Candidate.add(values[0], values[2])
	first, second := 0.0, 0.0
	for channel := range 4 {
		first += math.Abs(values[0][channel] - values[1][channel])
		second += math.Abs(values[0][channel] - values[2][channel])
	}
	if second < first {
		metric.Improved++
	} else if second > first {
		metric.Worsened++
	} else {
		metric.Equal++
	}
}

func quant256ReadJSON(t *testing.T, path string, target any, bindings map[string]string) {
	t.Helper()
	data := quant256Bind(t, path, bindings)
	if err := json.Unmarshal(data, target, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
}

func quant256Bind(t *testing.T, path string, bindings map[string]string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := quant256BytesSHA(data)
	if previous, exists := bindings[path]; exists && previous != hash {
		t.Fatal("input changed during diagnostic")
	}
	bindings[path] = hash
	return data
}

func quant256BytesSHA(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func quant256RGBAHash(pixels *image.NRGBA) string {
	digest := sha256.New()
	fmt.Fprintf(digest, "%d,%d,%d,%d:", pixels.Rect.Min.X, pixels.Rect.Min.Y, pixels.Rect.Max.X, pixels.Rect.Max.Y)
	for y := pixels.Rect.Min.Y; y < pixels.Rect.Max.Y; y++ {
		start := pixels.PixOffset(pixels.Rect.Min.X, y)
		digest.Write(pixels.Pix[start : start+pixels.Rect.Dx()*4])
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func quant256ReadPNG(t *testing.T, path string, bindings map[string]string) *image.NRGBA {
	t.Helper()
	data := quant256Bind(t, path, bindings)
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if pixels, ok := decoded.(*image.NRGBA); ok {
		return pixels
	}
	pixels := image.NewNRGBA(decoded.Bounds())
	draw.Draw(pixels, pixels.Rect, decoded, decoded.Bounds().Min, draw.Src)
	return pixels
}

func quant256WritePNG(t *testing.T, path string, pixels *image.NRGBA) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, pixels); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestQuant256PhaseBoundAndExactGridAgreement(t *testing.T) {
	for index := 0; index <= 997; index++ {
		phase := float64(index) / 997
		quantized := math.RoundToEven(phase*256) / 256
		if math.Abs(quantized-phase) > 1.0/512+1e-15 {
			t.Fatal("nearest 1/256 phase exceeds half-step bound")
		}
	}
	source := image.NewNRGBA(image.Rect(-8, 11, 8, 27))
	for y := source.Rect.Min.Y; y < source.Rect.Max.Y; y++ {
		for x := source.Rect.Min.X; x < source.Rect.Max.X; x++ {
			index := source.PixOffset(x, y)
			for channel := range 4 {
				source.Pix[index+channel] = uint8((x*31 + y*13 + channel*53) % 256)
			}
		}
	}
	for _, phaseX := range []float64{0, 1.0 / 256, 17.0 / 256, .5, 255.0 / 256, 1} {
		for _, phaseY := range []float64{0, 19.0 / 256, .5, 237.0 / 256, 1} {
			u, v := (4.5+phaseX)/16, (7.5+phaseY)/16
			if quant256Sample(source, u, v) != rasterSample(source, u, v) {
				t.Fatal("quantizer changed premultiplied arithmetic at an exact 1/256 source phase")
			}
		}
	}
}
