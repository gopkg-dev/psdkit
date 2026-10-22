package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/gopkg-dev/psdkit/psdfile"
)

type pixelMetric struct {
	Pixels, Changed   int
	MAE, RMS, Maximum float64
}

type browserRecord struct {
	ID, Browser                      string
	Header, ReopenedHeader           psdfile.Header
	Rendered, Exported, MetadataRead bool
	ProjectionAudit                  projectionProof
	RenderError                      struct {
		Code, Message string
		Audit         *projectionProof
	}
}

type projectionProof struct {
	Verified              bool
	ParentSavedPixelReads int
	SourceRGBASHA256      string
}

type browserResult struct {
	ID                      string            `json:"id"`
	OK                      bool              `json:"ok"`
	Supported               bool              `json:"supported"`
	GoChangedPixels         int               `json:"goChangedPixels"`
	GoMax                   float64           `json:"goMax"`
	PhotoshopMAE            float64           `json:"photoshopMAE"`
	BaselinePhotoshopMAE    float64           `json:"baselinePhotoshopMAE"`
	AgainstGo               pixelMetric       `json:"againstGo"`
	AgainstPhotoshop        pixelMetric       `json:"againstPhotoshop"`
	UnmodifiedPSDBytesExact bool              `json:"unmodifiedPSDBytesExact"`
	PSDStructurePreserved   bool              `json:"psdStructurePreserved"`
	ProjectionAudit         projectionProof   `json:"projectionAudit"`
	Record                  browserRecord     `json:"record"`
	SHA256                  map[string]string `json:"sha256"`
}

func decodePNG(data []byte) (*image.NRGBA, error) {
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if direct, valid := decoded.(*image.NRGBA); valid {
		return direct, nil
	}
	bounds := decoded.Bounds()
	output := image.NewNRGBA(bounds)
	for row := bounds.Min.Y; row < bounds.Max.Y; row++ {
		for column := bounds.Min.X; column < bounds.Max.X; column++ {
			output.SetNRGBA(column, row, color.NRGBAModel.Convert(decoded.At(column, row)).(color.NRGBA))
		}
	}
	return output, nil
}

func comparePixels(first, second *image.NRGBA) (pixelMetric, error) {
	if first.Bounds() != second.Bounds() {
		return pixelMetric{}, fmt.Errorf("PNG frame differs")
	}
	var metric pixelMetric
	var total, square float64
	visible := 0
	for row := range first.Rect.Dy() {
		for column := range first.Rect.Dx() {
			a, b := first.Pix[row*first.Stride+column*4:][:4], second.Pix[row*second.Stride+column*4:][:4]
			changed := false
			for component := range 4 {
				if a[component] != b[component] {
					changed = true
				}
			}
			metric.Pixels++
			if changed {
				metric.Changed++
			}
			if a[3] == 0 && b[3] == 0 {
				continue
			}
			visible++
			for component := range 4 {
				left, right := float64(a[component]), float64(b[component])
				if component < 3 {
					left *= float64(a[3]) / 255
					right *= float64(b[3]) / 255
				}
				delta := math.Abs(left - right)
				total += delta
				square += delta * delta
				metric.Maximum = math.Max(metric.Maximum, delta)
			}
		}
	}
	if visible > 0 {
		metric.MAE = total / float64(visible*4)
		metric.RMS = math.Sqrt(square / float64(visible*4))
	}
	return metric, nil
}

func rawPixelsSHA(pixels *image.NRGBA) string {
	hash := sha256.New()
	for row := range pixels.Rect.Dy() {
		hash.Write(pixels.Pix[row*pixels.Stride : row*pixels.Stride+pixels.Rect.Dx()*4])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func structureEqual(first, second []byte) (bool, error) {
	a, err := psdfile.Parse(first)
	if err != nil {
		return false, err
	}
	defer func() { _ = a.Close() }()
	b, err := psdfile.Parse(second)
	if err != nil {
		return false, err
	}
	defer func() { _ = b.Close() }()
	if a.Header != b.Header || len(a.Layers) != len(b.Layers) || len(a.Resources) != len(b.Resources) {
		return false, nil
	}
	for index, layer := range a.Layers {
		other := b.Layers[index]
		if layer.Name != other.Name || layer.ID != other.ID || layer.Bounds != other.Bounds || layer.Flags != other.Flags ||
			layer.Opacity != other.Opacity || layer.BlendMode != other.BlendMode || len(layer.Channels) != len(other.Channels) ||
			len(layer.AdditionalInfo) != len(other.AdditionalInfo) {
			return false, nil
		}
		for channel, record := range layer.Channels {
			matched := other.Channels[channel]
			if record.ID != matched.ID || record.Compression != matched.Compression ||
				!equalSpan(first, record.Data, second, matched.Data) {
				return false, nil
			}
		}
		for block, record := range layer.AdditionalInfo {
			matched := other.AdditionalInfo[block]
			if record.Key != matched.Key || record.Signature != matched.Signature ||
				!equalSpan(first, record.Data, second, matched.Data) {
				return false, nil
			}
		}
	}
	for index, record := range a.Resources {
		matched := b.Resources[index]
		if record.ID != matched.ID || record.Name != matched.Name ||
			!equalSpan(first, record.Data, second, matched.Data) {
			return false, nil
		}
	}
	return true, nil
}

func (suite *browserSuite) result(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(writer, "POST required", http.StatusMethodNotAllowed)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 16<<20)
	if err := request.ParseMultipartForm(16 << 20); err != nil {
		sendJSON(writer, nil, err)
		return
	}
	defer func() { _ = request.MultipartForm.RemoveAll() }()
	var record browserRecord
	if err := json.Unmarshal([]byte(request.FormValue("record")), &record); err != nil {
		sendJSON(writer, nil, err)
		return
	}
	suite.mu.Lock()
	defer suite.mu.Unlock()
	var fixture *browserCase
	for index := range suite.cases {
		if suite.cases[index].ID == record.ID {
			fixture = &suite.cases[index]
			break
		}
	}
	if fixture == nil {
		sendJSON(writer, nil, fmt.Errorf("case not in frozen manifest"))
		return
	}
	if _, exists := suite.rows[record.ID]; exists {
		sendJSON(writer, nil, fmt.Errorf("case already recorded"))
		return
	}
	result, err := suite.compareCase(*fixture, record, request)
	if err != nil {
		sendJSON(writer, nil, err)
		return
	}
	suite.rows[record.ID] = result
	path := filepath.Join(suite.output, record.ID+".json")
	data, _ := json.Marshal(result, jsontext.WithIndent("  "), json.Deterministic(true))
	if err := os.WriteFile(path, data, 0644); err != nil {
		sendJSON(writer, nil, err)
		return
	}
	sendJSON(writer, result, nil)
}

func equalSpan(first []byte, a psdfile.Span, second []byte, b psdfile.Span) bool {
	return bytes.Equal(first[a.Offset:a.Offset+a.Length], second[b.Offset:b.Offset+b.Length])
}

func (suite *browserSuite) compareCase(
	fixture browserCase, record browserRecord, request *http.Request,
) (browserResult, error) {
	result := browserResult{ID: fixture.ID, Supported: fixture.Supported, Record: record, SHA256: map[string]string{}}
	if !record.MetadataRead || !record.Exported || record.Header != record.ReopenedHeader {
		return result, fmt.Errorf("metadata/export/reopen incomplete")
	}
	psdFile, _, err := request.FormFile("psd")
	if err != nil {
		return result, err
	}
	defer func() { _ = psdFile.Close() }()
	psd, err := io.ReadAll(psdFile)
	if err != nil {
		return result, err
	}
	original, err := os.ReadFile(fixture.TargetPath)
	if err != nil {
		return result, err
	}
	result.UnmodifiedPSDBytesExact = bytes.Equal(original, psd)
	result.PSDStructurePreserved, err = structureEqual(original, psd)
	if err != nil || !result.PSDStructurePreserved {
		return result, fmt.Errorf("PSD original blocks or channels changed: %w", err)
	}
	path := filepath.Join(suite.output, fixture.ID+".psd")
	if err := os.WriteFile(path, psd, 0644); err != nil {
		return result, err
	}
	result.SHA256[path], _ = fileSHA(path)
	audit := record.ProjectionAudit
	if !record.Rendered && record.RenderError.Audit != nil {
		audit = *record.RenderError.Audit
	}
	result.ProjectionAudit = audit
	sourceBytes, err := os.ReadFile(fixture.SourcePath)
	if err != nil {
		return result, err
	}
	source, err := decodePNG(sourceBytes)
	if err != nil {
		return result, err
	}
	if !audit.Verified || audit.ParentSavedPixelReads != 0 || audit.SourceRGBASHA256 != rawPixelsSHA(source) {
		return result, fmt.Errorf("render source/parent-payload guard not verified")
	}
	if !fixture.Supported {
		if record.Rendered || record.RenderError.Code != "unsupported" {
			return result, fmt.Errorf("unqualified Warp gate changed")
		}
		result.OK = true
		return result, nil
	}
	if !record.Rendered {
		return result, fmt.Errorf("supported projection failed: %s", record.RenderError.Message)
	}
	file, _, err := request.FormFile("png")
	if err != nil {
		return result, err
	}
	defer func() { _ = file.Close() }()
	encoded, err := io.ReadAll(file)
	if err != nil {
		return result, err
	}
	pixels, err := decodePNG(encoded)
	if err != nil {
		return result, err
	}
	baselineBytes, err := os.ReadFile(fixture.GoPNG)
	if err != nil {
		return result, err
	}
	baseline, err := decodePNG(baselineBytes)
	if err != nil {
		return result, err
	}
	nativeBytes, err := os.ReadFile(fixture.PhotoshopPNG)
	if err != nil {
		return result, err
	}
	native, err := decodePNG(nativeBytes)
	if err != nil {
		return result, err
	}
	result.AgainstGo, err = comparePixels(pixels, baseline)
	if err != nil {
		return result, err
	}
	result.AgainstPhotoshop, err = comparePixels(pixels, native)
	if err != nil {
		return result, err
	}
	before, err := comparePixels(baseline, native)
	if err != nil {
		return result, err
	}
	result.GoChangedPixels, result.GoMax = result.AgainstGo.Changed, result.AgainstGo.Maximum
	result.PhotoshopMAE = result.AgainstPhotoshop.MAE
	result.BaselinePhotoshopMAE = before.MAE
	result.OK = result.GoChangedPixels == 0
	path = filepath.Join(suite.output, fixture.ID+".png")
	if err := os.WriteFile(path, encoded, 0644); err != nil {
		return result, err
	}
	result.SHA256[path], _ = fileSHA(path)
	return result, nil
}

func (suite *browserSuite) complete(writer http.ResponseWriter, request *http.Request) {
	suite.mu.Lock()
	defer suite.mu.Unlock()
	if len(suite.rows) != 66 {
		sendJSON(writer, nil, fmt.Errorf("only %d/66 cases recorded", len(suite.rows)))
		return
	}
	var runtimeProof map[string]any
	if err := json.UnmarshalRead(http.MaxBytesReader(writer, request.Body, 64<<10), &runtimeProof); err != nil {
		sendJSON(writer, nil, err)
		return
	}
	rows := []browserResult{}
	supported, rejected, exact, byteExact := 0, 0, 0, 0
	for _, row := range suite.rows {
		rows = append(rows, row)
		if row.Supported {
			supported++
			if row.GoChangedPixels == 0 {
				exact++
			}
		} else {
			rejected++
		}
		if row.UnmodifiedPSDBytesExact {
			byteExact++
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	for path, expected := range suite.inputSHA {
		actual, err := fileSHA(path)
		if err != nil || actual != expected {
			sendJSON(writer, nil, fmt.Errorf("frozen input changed: %s", path))
			return
		}
	}
	manifest, err := os.ReadFile("web/demo/public/wasm/manifest.json")
	if err != nil {
		sendJSON(writer, nil, err)
		return
	}
	result := map[string]any{"version": "psdkit-browser-66-v1", "cases": len(rows), "supported": supported,
		"rejected": rejected, "byteExactAgainstNativeGo": exact, "unmodifiedPSDByteExact": byteExact,
		"allPSDStructurePreserved": true, "renderParentSavedPixelReads": 0, "inputsUnchanged": true,
		"runtime": runtimeProof, "wasmBuild": jsontext.Value(manifest), "rows": rows,
		"scope": "Actual browser Web Worker; Go algorithms unchanged. Photoshop differences remain measured, not waived."}
	data, err := json.Marshal(result, jsontext.WithIndent("  "), json.Deterministic(true))
	if err == nil {
		err = os.WriteFile(filepath.Join(suite.output, "summary.json"), data, 0644)
	}
	sendJSON(writer, result, err)
}
