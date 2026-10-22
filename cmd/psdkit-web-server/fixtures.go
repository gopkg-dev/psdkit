package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"image"
	"image/png"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/gopkg-dev/psdkit/internal/webfixtures"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
)

type browserCase struct {
	ID                                          string `json:"id"`
	LayerIndex                                  int    `json:"layerIndex"`
	Width, Height                               int
	TargetURL                                   string `json:"targetURL"`
	SourceURL                                   string `json:"sourceURL"`
	TargetSHA                                   string `json:"targetSHA"`
	SourceSHA                                   string `json:"sourceSHA"`
	Supported                                   bool   `json:"supported"`
	TargetPath, SourcePath, GoPNG, PhotoshopPNG string `json:"-"`
}

type browserSuite struct {
	mu       sync.Mutex
	cases    []browserCase
	files    map[string]string
	rows     map[string]browserResult
	output   string
	inputSHA map[string]string
}

func readJSON(path string, result any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, result)
}

func fileSHA(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func loadSuite(output string) (*browserSuite, error) {
	if err := os.Mkdir(output, 0755); err != nil {
		return nil, err
	}
	suite := &browserSuite{output: output, files: map[string]string{},
		rows: map[string]browserResult{}, inputSHA: map[string]string{}}
	packets := []string{"canary-v1", "matrix-v1", "transform-canary-v1", "transform-matrix-v1", "dimension-holdout-v1"}
	for _, packet := range packets {
		var native struct{ Cases []struct{ ID, PNG string } }
		nativePath := filepath.Join("output/native-uniform-grid-expanded/authored-v1", packet, "native-report.json")
		if err := readJSON(nativePath, &native); err != nil {
			return nil, err
		}
		nativePNGs := map[string]string{}
		for _, entry := range native.Cases {
			nativePNGs[entry.ID] = entry.PNG
		}
		var predictions []struct {
			ID, PNG     string
			Unsupported bool   `json:"unsupported"`
			PSD         string `json:"native_metadata_psd"`
			Source      string `json:"prediction_source_png"`
			PSDSHA      string `json:"native_metadata_psd_sha256"`
			SourceSHA   string `json:"prediction_source_png_sha256"`
		}
		path := filepath.Join("output/native-uniform-grid-refine-v1/parameter-candidate-v1", packet,
			"independent-go-predictions-v3.json")
		if err := readJSON(path, &predictions); err != nil {
			return nil, err
		}
		for _, prediction := range predictions {
			caseIndex := len(suite.cases)
			entry := browserCase{ID: packet + "--" + prediction.ID, LayerIndex: -1, TargetPath: prediction.PSD,
				SourcePath: prediction.Source, GoPNG: prediction.PNG,
				PhotoshopPNG: nativePNGs[prediction.ID], Supported: !prediction.Unsupported}
			source, err := psdfile.Open(entry.TargetPath)
			if err != nil {
				return nil, err
			}
			entry.Width, entry.Height = int(source.Header.Width), int(source.Header.Height)
			for index := range source.Layers {
				metadata, err := source.ReadLayerMetadata(context.Background(), index)
				if err != nil {
					_ = source.Close()
					return nil, err
				}
				if metadata.Layer.Placed != nil {
					if entry.LayerIndex != -1 {
						_ = source.Close()
						return nil, fmt.Errorf("multiple target placements")
					}
					entry.LayerIndex = index
				}
			}
			_ = source.Close()
			if entry.LayerIndex == -1 {
				return nil, fmt.Errorf("target placement missing")
			}
			entry.TargetSHA, err = fileSHA(entry.TargetPath)
			if err != nil || entry.TargetSHA != prediction.PSDSHA {
				return nil, fmt.Errorf("target source changed: %s", entry.ID)
			}
			entry.SourceSHA, err = fileSHA(entry.SourcePath)
			if err != nil || entry.SourceSHA != prediction.SourceSHA {
				return nil, fmt.Errorf("source artwork changed: %s", entry.ID)
			}
			files := map[string]string{fmt.Sprintf("p%d", caseIndex): entry.TargetPath,
				fmt.Sprintf("i%d", caseIndex): entry.SourcePath}
			maps.Copy(suite.files, files)
			entry.TargetURL = fmt.Sprintf("/__fixtures/file/p%d", caseIndex)
			entry.SourceURL = fmt.Sprintf("/__fixtures/file/i%d", caseIndex)
			for _, file := range []string{entry.TargetPath, entry.SourcePath, entry.GoPNG, entry.PhotoshopPNG} {
				if file == "" {
					continue
				}
				checksum, err := fileSHA(file)
				if err != nil {
					return nil, err
				}
				suite.inputSHA[file] = checksum
			}
			suite.cases = append(suite.cases, entry)
		}
	}
	if len(suite.cases) != 66 {
		return nil, fmt.Errorf("expected 66 frozen cases")
	}
	data, _ := json.Marshal(map[string]any{"cases": suite.cases, "inputSHA256": suite.inputSHA}, jsontext.WithIndent("  "), json.Deterministic(true))
	if err := os.WriteFile(filepath.Join(output, "inputs.json"), data, 0644); err != nil {
		return nil, err
	}
	return suite, nil
}

func (suite *browserSuite) manifest(writer http.ResponseWriter, request *http.Request) {
	rows := make([]map[string]any, 0, len(suite.cases))
	for _, entry := range suite.cases {
		rows = append(rows, map[string]any{"id": entry.ID, "layerIndex": entry.LayerIndex,
			"width": entry.Width, "height": entry.Height, "targetURL": entry.TargetURL, "sourceURL": entry.SourceURL,
			"targetSHA": entry.TargetSHA, "sourceSHA": entry.SourceSHA,
			"supported": entry.Supported})
	}
	sendJSON(writer, map[string]any{"cases": rows}, nil)
}

func (suite *browserSuite) file(writer http.ResponseWriter, request *http.Request) {
	file, valid := suite.files[filepath.Base(request.URL.Path)]
	if !valid {
		http.NotFound(writer, request)
		return
	}
	http.ServeFile(writer, request, file)
}

func (suite *browserSuite) font(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "font/ttf")
	_, _ = writer.Write(webfixtures.Font())
}

func (suite *browserSuite) fontResult(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, 2<<20)
	if err := request.ParseMultipartForm(2 << 20); err != nil {
		sendJSON(writer, nil, err)
		return
	}
	defer func() { _ = request.MultipartForm.RemoveAll() }()
	var record struct {
		FontName string
		Glyphs   int
	}
	if err := json.Unmarshal([]byte(request.FormValue("record")), &record); err != nil {
		sendJSON(writer, nil, err)
		return
	}
	if record.FontName != "BridgeFixture-Regular" || record.Glyphs != 2 {
		sendJSON(writer, nil, fmt.Errorf("font pipeline incomplete"))
		return
	}
	result, err := compareBrowserFont(request, suite.output)
	sendJSON(writer, result, err)
}

func compareBrowserFont(request *http.Request, output string) (any, error) {
	file, _, err := request.FormFile("png")
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	encoded, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	actual, err := decodePNG(encoded)
	if err != nil {
		return nil, err
	}
	face, err := psdfont.Decode(context.Background(), webfixtures.Font())
	if err != nil {
		return nil, err
	}
	resolve := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { return face, nil })
	run, err := psdfont.Layout(context.Background(), "AV", psdfont.Style{FontName: face.PostScriptName, Size: 48},
		resolve, psdfont.LayoutOptions{OriginY: 60})
	if err != nil {
		return nil, err
	}
	native, err := psdfont.RasterizeRun(context.Background(), run, image.Rect(0, 0, 140, 90))
	if err != nil {
		return nil, err
	}
	var reference bytes.Buffer
	if err := png.Encode(&reference, native); err != nil {
		return nil, err
	}
	expected, err := decodePNG(reference.Bytes())
	if err != nil {
		return nil, err
	}
	metric, err := comparePixels(actual, expected)
	if err != nil || metric.Changed != 0 {
		return nil, fmt.Errorf("font pixels differ: %+v %w", metric, err)
	}
	for name, data := range map[string][]byte{"font-browser.png": encoded, "font-native.png": reference.Bytes()} {
		if err := os.WriteFile(filepath.Join(output, name), data, 0644); err != nil {
			return nil, err
		}
	}
	result := map[string]any{"fontName": face.PostScriptName, "glyphs": len(run.Glyphs), "againstGo": metric}
	data, _ := json.Marshal(result, jsontext.WithIndent("  "), json.Deterministic(true))
	err = os.WriteFile(filepath.Join(output, "font-browser.json"), data, 0644)
	return result, err
}
