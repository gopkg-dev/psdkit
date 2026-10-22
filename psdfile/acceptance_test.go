package psdfile_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/testfixture"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdparse"
)

func acceptanceOpen(t *testing.T, fixture acceptanceFixture, options ...psdfile.LoadOptions) *psdfile.Document {
	t.Helper()
	document, err := psdfile.Parse(fixture.data, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := document.Close(); err != nil {
			t.Error(err)
		}
	})
	return document
}

func acceptanceAssertSpan(
	t *testing.T, document *psdfile.Document, fixture acceptanceFixture, label string, span psdfile.Span,
) {
	t.Helper()
	want, exists := fixture.spans[label]
	if !exists {
		t.Fatalf("missing independent fixture span %q", label)
	}
	if span != want {
		t.Fatalf("%s span=%+v, want %+v", label, span, want)
	}
	data, err := document.ReadSpan(span)
	if err != nil {
		t.Fatal(err)
	}
	wantData := fixture.data[want.Offset : want.Offset+want.Length]
	if !bytes.Equal(data, wantData) {
		t.Fatalf("%s changed original bytes", label)
	}
	var streamed bytes.Buffer
	count, err := document.CopySpan(context.Background(), &streamed, span)
	if err != nil {
		t.Fatal(err)
	}
	if count != span.Length || !bytes.Equal(streamed.Bytes(), wantData) {
		t.Fatalf("%s CopySpan changed original bytes or length", label)
	}
	if len(data) > 0 {
		data[0] ^= 255
		again, err := document.ReadSpan(span)
		if err != nil || !bytes.Equal(again, wantData) {
			t.Fatalf("%s ReadSpan does not return owned bytes: %v", label, err)
		}
	}
}

func acceptanceAssertError(t *testing.T, err, kind error, feature, path string, offset int64) *psdfile.Error {
	t.Helper()
	if !errors.Is(err, kind) {
		t.Fatalf("error=%v, want category %v", err, kind)
	}
	var structured *psdfile.Error
	if !errors.As(err, &structured) {
		t.Fatalf("error has no structured PSD error: %v", err)
	}
	if structured.Feature != feature || structured.Path == "" {
		t.Fatalf("incomplete structured error: %+v", structured)
	}
	if path != "" && structured.Path != path {
		t.Fatalf("path=%q, want %q", structured.Path, path)
	}
	if offset >= 0 && structured.Offset != offset {
		t.Fatalf("offset=%d, want %d", structured.Offset, offset)
	}
	return structured
}

func TestAcceptancePreservesOrderedOriginalBlocks(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			fixture := acceptanceDocument(version, 8, 0, 2, "")
			document := acceptanceOpen(t, fixture)
			if document.Header.Version != version || document.Header.Depth != 8 ||
				document.Header.Channels != 4 || document.Header.Width != 2 || document.Header.Height != 2 {
				t.Fatalf("header=%+v", document.Header)
			}
			acceptanceAssertSpan(t, document, fixture, "source", document.Source)
			acceptanceAssertSpan(t, document, fixture, "header", document.HeaderData)
			acceptanceAssertSpan(t, document, fixture, "color", document.ColorModeData)
			acceptanceAssertSpan(t, document, fixture, "resources", document.ResourceSection)
			acceptanceAssertSpan(t, document, fixture, "layerMask", document.LayerMaskSection)
			acceptanceAssertSpan(t, document, fixture, "layers/layerInfo", document.LayerInfo)
			acceptanceAssertSpan(t, document, fixture, "layers/globalMask", document.GlobalMask)
			if len(document.Resources) != 3 {
				t.Fatalf("resources=%d", len(document.Resources))
			}
			for index, resource := range document.Resources {
				if resource.Signature != "8BIM" || resource.ID != []uint16{30000, 30001, 30000}[index] {
					t.Fatalf("resource[%d] lost its signature, ID or physical order", index)
				}
				prefix := fmt.Sprintf("resources/resource%d/", index)
				acceptanceAssertSpan(t, document, fixture, prefix+"span", resource.Span)
				acceptanceAssertSpan(t, document, fixture, prefix+"name", resource.NameData)
				acceptanceAssertSpan(t, document, fixture, prefix+"data", resource.Data)
				acceptanceAssertSpan(t, document, fixture, prefix+"padding", resource.Padding)
			}
			if len(document.Layers) != 2 || len(document.LayerSets) != 1 || !document.GlobalAlpha {
				t.Fatalf("layers=%d sets=%d alpha=%t", len(document.Layers), len(document.LayerSets), document.GlobalAlpha)
			}
			acceptanceAssertSpan(t, document, fixture, "layers/info/span", document.LayerSets[0].Span)
			acceptanceAssertSpan(t, document, fixture, "layers/info/padding", document.LayerSets[0].Padding)
			for index, layer := range document.Layers {
				prefix := fmt.Sprintf("layers/info/layer%d/", index)
				if layer.Name != fmt.Sprintf("图层😀%d", index) || layer.Bounds != image.Rect(0, 0, 2, 2) ||
					layer.BlendSignature != "8BIM" || layer.BlendMode != "norm" ||
					layer.Opacity != 123 || layer.Clipping != 1 || layer.Flags != 10 || layer.Filler != 0 {
					t.Fatalf("layer[%d] lost metadata: %+v", index, layer)
				}
				acceptanceAssertSpan(t, document, fixture, prefix+"span", layer.Span)
				acceptanceAssertSpan(t, document, fixture, prefix+"extra/data", layer.Extra)
				acceptanceAssertSpan(t, document, fixture, prefix+"extra/name", layer.NameData)
				acceptanceAssertSpan(t, document, fixture, prefix+"extra/mask/span", layer.MaskData)
				acceptanceAssertSpan(t, document, fixture, prefix+"extra/mask/data", layer.Mask.Span)
				acceptanceAssertSpan(t, document, fixture, prefix+"extra/ranges/span", layer.BlendRanges)
				acceptanceCheckAdditional(t, document, fixture, prefix+"extra/additional", layer.AdditionalInfo,
					[]string{"U001", "U002", "U001", "luni"})
				if len(layer.Channels) != 5 {
					t.Fatalf("channels=%d", len(layer.Channels))
				}
				for channelIndex, channel := range layer.Channels {
					channelPrefix := prefix + fmt.Sprintf("channel%d/", channelIndex)
					if channel.ID != []int16{0, 1, 2, -1, -2}[channelIndex] || channel.Compression != 0 ||
						channel.LengthWidth != uint8(acceptanceVersionWidth(version)) ||
						channel.DeclaredLength != uint64(fixture.spans[channelPrefix+"span"].Length) {
						t.Fatalf("channel[%d] lost metadata: %+v", channelIndex, channel)
					}
					acceptanceAssertSpan(t, document, fixture, channelPrefix+"span", channel.Span)
					acceptanceAssertSpan(t, document, fixture, channelPrefix+"data", channel.Data)
				}
			}
			acceptanceCheckAdditional(t, document, fixture, "layers/global", document.AdditionalInfo,
				[]string{"G001", "lnk2", "G001"})
			acceptanceAssertSpan(t, document, fixture, "composite/span", document.Composite.Span)
			acceptanceAssertSpan(t, document, fixture, "composite/data", document.Composite.Data)
		})
	}
}

func acceptanceCheckAdditional(
	t *testing.T, document *psdfile.Document, fixture acceptanceFixture, prefix string,
	blocks []psdfile.AdditionalBlock, keys []string,
) {
	t.Helper()
	if len(blocks) != len(keys) {
		t.Fatalf("%s blocks=%d, want %d", prefix, len(blocks), len(keys))
	}
	for index, block := range blocks {
		label := fmt.Sprintf("%s%d/", prefix, index)
		if block.Key != keys[index] {
			t.Fatalf("%s lost physical order or duplicate key: %q", label, block.Key)
		}
		original := fixture.spans[label+"span"]
		if block.Signature != string(fixture.data[original.Offset:original.Offset+4]) ||
			block.LengthWidth != uint8(fixture.spans[label+"length"].Length) {
			t.Fatalf("%s lost signature or encoded length width", label)
		}
		acceptanceAssertSpan(t, document, fixture, label+"span", block.Span)
		acceptanceAssertSpan(t, document, fixture, label+"data", block.Data)
		acceptanceAssertSpan(t, document, fixture, label+"padding", block.Padding)
	}
}

func TestAcceptanceIndexReadsMetadataWithoutPixelOrEmbeddedPayload(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := uint16(0); compression <= 3; compression++ {
			t.Run(fmt.Sprintf("v%d/compression%d", version, compression), func(t *testing.T) {
				fixture := acceptanceDocument(version, 8, compression, 1, "")
				reader := &acceptanceReader{reader: bytes.NewReader(fixture.data)}
				document, err := psdfile.Read(reader, int64(len(fixture.data)))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = document.Close() })
				var forbidden []psdfile.Span
				for label, span := range fixture.spans {
					if strings.Contains(label, "/plane") || strings.HasSuffix(label, "/encoded") ||
						strings.HasSuffix(label, "/embedded") || strings.HasPrefix(label, "resources/") &&
						strings.HasSuffix(label, "/data") {
						forbidden = append(forbidden, span)
					}
				}
				var readBytes int64
				for _, read := range reader.reads {
					readBytes += read.Length
					for _, payload := range forbidden {
						if acceptanceOverlaps(read, payload) {
							t.Fatalf("index read encoded pixels or embedded/resource payload: read=%+v payload=%+v", read, payload)
						}
					}
				}
				if readBytes >= 4096 {
					t.Fatalf("small indexed metadata read %d bytes for an %d-byte source", readBytes, len(fixture.data))
				}
				if compression == 1 {
					for label, table := range fixture.spans {
						if !strings.HasSuffix(label, "/table") {
							continue
						}
						found := slices.ContainsFunc(reader.reads, func(read psdfile.Span) bool {
							return acceptanceOverlaps(read, table)
						})
						if !found {
							t.Fatalf("index did not validate %s", label)
						}
					}
				}
			})
		}
	}
}

func TestAcceptanceSourceOwnershipAndClose(t *testing.T) {
	fixture := acceptanceDocument(1, 8, 0, 1, "")
	reader := &acceptanceReader{reader: bytes.NewReader(fixture.data)}
	document, err := psdfile.Read(reader, int64(len(fixture.data)))
	if err != nil {
		t.Fatal(err)
	}
	if reader.closed {
		t.Fatal("Read closed its borrowed source while indexing")
	}
	if err := document.Close(); err != nil {
		t.Fatal(err)
	}
	if err := document.Close(); err != nil || reader.closed {
		t.Fatalf("Close is not idempotent or closed borrowed source: err=%v closed=%t", err, reader.closed)
	}
	acceptanceCheckClosed(t, document)
	filename := filepath.Join(t.TempDir(), "held.psd")
	if err := os.WriteFile(filename, fixture.data, 0o600); err != nil {
		t.Fatal(err)
	}
	owned, err := psdfile.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owned.Close() })
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	// Unlinking leaves the owned file handle usable until Close on every supported platform.
	acceptanceAssertSpan(t, owned, fixture, "source", owned.Source)
	if err := owned.Close(); err != nil {
		t.Fatal(err)
	}
	acceptanceCheckClosed(t, owned)
}

func TestAcceptanceOpenLongSourcePath(t *testing.T) {
	directory := t.TempDir()
	for range 5 {
		directory = filepath.Join(directory, strings.Repeat("nested-", 10))
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(directory, "原始样本.psd")
	fixture := acceptanceDocument(1, 8, 0, 1, "")
	if err := os.WriteFile(filename, fixture.data, 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := psdfile.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = document.Close() })
	acceptanceAssertSpan(t, document, fixture, "source", document.Source)
}

func acceptanceCheckClosed(t *testing.T, document *psdfile.Document) {
	t.Helper()
	checks := []func() error{
		func() error { _, err := document.ReadAt(make([]byte, 1), 0); return err },
		func() error { _, err := document.ReadSpan(document.HeaderData); return err },
		func() error {
			_, err := document.CopySpan(context.Background(), io.Discard, document.Source)
			return err
		},
		func() error { _, err := document.DecodeLayer(context.Background(), 0); return err },
		func() error { _, err := document.DecodeComposite(context.Background()); return err },
	}
	for index, check := range checks {
		if err := check(); !errors.Is(err, psdfile.ErrClosed) {
			t.Fatalf("operation[%d] after Close=%v", index, err)
		}
	}
}

func TestAcceptanceCopySpanBudgetsCancellationAndInvalidSpans(t *testing.T) {
	fixture := acceptanceDocument(1, 8, 0, 1, "")
	document := acceptanceOpen(t, fixture, psdfile.LoadOptions{Limits: psdfile.Limits{MaxReadBytes: 32}})
	_, err := document.ReadSpan(document.Source)
	acceptanceAssertError(t, err, psdfile.ErrLimit, "source", "span", 0)
	checksum := sha256.New()
	count, err := document.CopySpan(context.Background(), checksum, document.Source)
	if err != nil || count != int64(len(fixture.data)) || !bytes.Equal(checksum.Sum(nil), acceptanceSHA(fixture.data)) {
		t.Fatalf("bounded streaming changed source: count=%d err=%v", count, err)
	}
	for _, span := range []psdfile.Span{
		{Offset: -1, Length: 1}, {Offset: 1, Length: -1}, {Offset: math.MaxInt64, Length: 1},
		{Offset: 1, Length: math.MaxInt64}, {Offset: int64(len(fixture.data)), Length: 1},
	} {
		_, err := document.ReadSpan(span)
		acceptanceAssertError(t, err, psdfile.ErrMalformed, "source", "span", span.Offset)
		_, err = document.CopySpan(context.Background(), io.Discard, span)
		acceptanceAssertError(t, err, psdfile.ErrMalformed, "source", "span", span.Offset)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	count, err = document.CopySpan(ctx, io.Discard, document.Source)
	if count != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("already canceled copy: count=%d err=%v", count, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	writer := &acceptanceCancelWriter{cancel: cancel}
	count, err = document.CopySpan(ctx, writer, document.Source)
	if !errors.Is(err, context.Canceled) || count <= 0 || count >= document.Source.Length {
		t.Fatalf("stream cancellation did not stop between chunks: count=%d err=%v", count, err)
	}
	_, err = document.CopySpan(context.Background(), nil, document.Source)
	acceptanceAssertError(t, err, psdfile.ErrMalformed, "source", "span", 0)
	writerFailure := errors.New("fixture writer failure")
	_, err = document.CopySpan(context.Background(), acceptanceFailWriter{err: writerFailure}, document.Source)
	if !errors.Is(err, writerFailure) {
		t.Fatalf("copy lost writer cause: %v", err)
	}
}

func acceptanceSHA(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

type acceptanceCancelWriter struct{ cancel context.CancelFunc }

func (w *acceptanceCancelWriter) Write(data []byte) (int, error) {
	w.cancel()
	return len(data), nil
}

type acceptanceFailWriter struct{ err error }

func (w acceptanceFailWriter) Write([]byte) (int, error) { return 0, w.err }

func TestAcceptanceHeaderModesAndSourcePrecision(t *testing.T) {
	cases := []struct {
		depth, mode, channels uint16
		color                 []byte
	}{
		{1, 0, 1, nil}, {8, 1, 1, nil}, {16, 1, 1, nil}, {32, 1, 1, nil},
		{8, 2, 1, bytes.Repeat([]byte{0x91}, 768)},
		{8, 3, 3, nil}, {16, 3, 3, nil}, {32, 3, 3, nil},
		{8, 4, 4, nil}, {16, 4, 4, nil}, {8, 7, 5, nil},
		{8, 8, 1, []byte{0x72, 0x89, 0x18, 0}}, {8, 9, 3, nil}, {16, 9, 3, nil},
	}
	for _, version := range []uint16{1, 2} {
		for _, testCase := range cases {
			t.Run(fmt.Sprintf("v%d/depth%d/mode%d", version, testCase.depth, testCase.mode), func(t *testing.T) {
				fixture := acceptanceFlat(version, testCase.depth, testCase.mode, testCase.channels, testCase.color)
				document := acceptanceOpen(t, fixture)
				if document.Header.Depth != testCase.depth || document.Header.ColorMode != testCase.mode ||
					document.Header.Channels != testCase.channels || len(document.Layers) != 0 {
					t.Fatalf("index rejected or changed supported structural fields: %+v", document.Header)
				}
				acceptanceAssertSpan(t, document, fixture, "color", document.ColorModeData)
				pixels, err := document.DecodeComposite(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if pixels.Depth != testCase.depth || pixels.ColorMode != testCase.mode ||
					len(pixels.Channels) != int(testCase.channels) {
					t.Fatalf("decode silently converted original sample depth/color mode: %+v", pixels)
				}
				for channel, plane := range pixels.Channels {
					span := fixture.spans[fmt.Sprintf("composite/plane%d", channel)]
					if !bytes.Equal(plane.Data, fixture.data[span.Offset:span.Offset+span.Length]) {
						t.Fatalf("plane%d lost source precision", channel)
					}
				}
			})
		}
	}
}

func TestAcceptanceAlternateLayerSets(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, testCase := range []struct {
			key   string
			depth uint16
		}{{"Layr", 8}, {"Lr16", 16}, {"Lr32", 32}} {
			t.Run(fmt.Sprintf("v%d/%s", version, testCase.key), func(t *testing.T) {
				fixture := acceptanceDocument(version, testCase.depth, 0, 1, testCase.key)
				document := acceptanceOpen(t, fixture)
				if len(document.LayerSets) != 1 || document.ActiveLayerSet != 0 || len(document.Layers) != 1 ||
					document.LayerSets[0].Key != testCase.key || document.LayerSets[0].Depth != testCase.depth {
					t.Fatalf("alternate layer information was not indexed/selected: %+v", document.LayerSets)
				}
				acceptanceAssertSpan(t, document, fixture, "layers/alternate/data", document.LayerSets[0].Span)
				block := document.AdditionalInfo[len(document.AdditionalInfo)-1]
				if block.LengthWidth != uint8(acceptanceVersionWidth(version)) {
					t.Fatalf("%s uses length width %d", testCase.key, block.LengthWidth)
				}
				pixels, err := document.DecodeLayer(context.Background(), 0)
				if err != nil {
					t.Fatal(err)
				}
				for index, plane := range pixels.Channels {
					if plane.Depth != testCase.depth || !bytes.Equal(plane.Data, acceptancePlanes(testCase.depth)[index]) {
						t.Fatalf("%s plane%d lost source depth or bytes", testCase.key, index)
					}
				}
			})
		}
	}
}

func TestAcceptanceAdditionalLengthForms(t *testing.T) {
	keys := []string{
		"LMsk", "Lr16", "Lr32", "Layr", "Mt16", "Mt32", "Mtrn", "Alph", "FMsk", "lnk2",
		"FEid", "FXid", "PxSD", "Z999",
	}
	for _, version := range []uint16{1, 2} {
		for _, signature := range []string{"8BIM", "8B64"} {
			t.Run(fmt.Sprintf("v%d/%s", version, signature), func(t *testing.T) {
				fixture := acceptanceDocument(version, 8, 0, 1, "")
				var tags []byte
				for _, key := range keys {
					width := 4
					if signature == "8B64" || version == 2 && key != "Z999" {
						width = 8
					}
					block := acceptanceAdditional(signature, key, width, 4, acceptancePayload(nil))
					tags = append(tags, block.data...)
				}
				composite := fixture.spans["composite/span"]
				data := append(bytes.Clone(fixture.data[:composite.Offset]), tags...)
				data = append(data, fixture.data[composite.Offset:]...)
				field := fixture.spans["layerMaskLength"]
				if version == 2 {
					binary.BigEndian.PutUint64(data[field.Offset:],
						uint64(fixture.spans["layerMask"].Length-field.Length+int64(len(tags))))
				} else {
					binary.BigEndian.PutUint32(data[field.Offset:],
						uint32(fixture.spans["layerMask"].Length-field.Length+int64(len(tags))))
				}
				document, err := psdfile.Parse(data)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = document.Close() })
				if len(document.AdditionalInfo) != len(keys)+3 {
					t.Fatalf("additional blocks=%d, want %d", len(document.AdditionalInfo), len(keys)+3)
				}
				for index, block := range document.AdditionalInfo[3:] {
					width := uint8(4)
					if signature == "8B64" || version == 2 && keys[index] != "Z999" {
						width = 8
					}
					if block.Key != keys[index] || block.Signature != signature || block.LengthWidth != width {
						t.Fatalf("tag%d encoded form changed: %+v", index, block)
					}
				}
			})
		}
	}
}

func TestAcceptancePSBReadDoesNotApplyLegacyCanvasDecodeLimit(t *testing.T) {
	fixture := acceptanceDocument(2, 32, 2, 1, "")
	binary.BigEndian.PutUint32(fixture.data[fixture.spans["width"].Offset:], 300_000)
	binary.BigEndian.PutUint32(fixture.data[fixture.spans["height"].Offset:], 300_000)
	document := acceptanceOpen(t, fixture)
	if document.Header.Width != 300_000 || document.Header.Height != 300_000 {
		t.Fatalf("PSB format dimensions changed: %+v", document.Header)
	}
	_, err := document.DecodeComposite(context.Background())
	acceptanceAssertError(t, err, psdfile.ErrLimit, "pixels", "composite", -1)
	for _, version := range []uint16{1, 2} {
		invalid := acceptanceFlat(version, 8, 3, 3, nil)
		limit := uint32(30_001)
		if version == 2 {
			limit = 300_001
		}
		binary.BigEndian.PutUint32(invalid.data[invalid.spans["width"].Offset:], limit)
		_, err := psdfile.Parse(invalid.data)
		acceptanceAssertError(t, err, psdfile.ErrMalformed, "index", "header.width", 18)
	}
}

func TestAcceptanceLimitsAndTypedStructureErrors(t *testing.T) {
	fixture := acceptanceDocument(2, 8, 0, 2, "")
	for name, limits := range map[string]psdfile.Limits{
		"file": {MaxFileBytes: int64(len(fixture.data) - 1)}, "metadata": {MaxMetadataBytes: 1},
		"blocks": {MaxBlocks: 1}, "layers": {MaxLayers: 1}, "negative": {MaxReadBytes: -1},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := psdfile.Parse(fixture.data, psdfile.LoadOptions{Limits: limits})
			feature := "index"
			if name == "file" {
				feature = "source"
			}
			if name == "negative" {
				feature = "options"
			}
			acceptanceAssertError(t, err, psdfile.ErrLimit, feature, "", -1)
		})
	}
	for _, label := range []string{
		"layerMaskLength", "layers/layerInfoLength", "layers/info/layer0/channel0/length",
		"layers/global1/length", "layers/global2/length",
	} {
		t.Run("overflow/"+label, func(t *testing.T) {
			data := bytes.Clone(fixture.data)
			field := fixture.spans[label]
			binary.BigEndian.PutUint64(data[field.Offset:field.Offset+8], math.MaxUint64)
			_, err := psdfile.Parse(data)
			acceptanceAssertError(t, err, psdfile.ErrMalformed, "index", "", field.Offset)
		})
	}
	for _, length := range []int{0, 25, 26, 28, 34, len(fixture.data) - 1} {
		t.Run(fmt.Sprintf("truncated/%d", length), func(t *testing.T) {
			_, err := psdfile.Parse(fixture.data[:length])
			feature := "index"
			if length < 26 {
				feature = "source"
			}
			acceptanceAssertError(t, err, psdfile.ErrMalformed, feature, "", -1)
		})
	}
	for _, testCase := range []struct {
		offset int
		bytes  []byte
		path   string
	}{
		{0, []byte("bad!"), "header.signature"}, {4, []byte{0, 3}, "header.version"},
		{6, []byte{1}, "header.reserved"}, {12, []byte{0, 57}, "header.channels"},
		{14, []byte{0, 0, 0, 0}, "header.height"}, {18, []byte{0, 0, 0, 0}, "header.width"},
		{22, []byte{0, 7}, "header.depth"}, {24, []byte{0, 6}, "header.colorMode"},
	} {
		t.Run(testCase.path, func(t *testing.T) {
			data := bytes.Clone(fixture.data)
			copy(data[testCase.offset:], testCase.bytes)
			_, err := psdfile.Parse(data)
			acceptanceAssertError(t, err, psdfile.ErrMalformed, "index", testCase.path, int64(testCase.offset))
		})
	}
}

type acceptanceBrokenReader struct {
	cause error
	short bool
}

func (r acceptanceBrokenReader) ReadAt(data []byte, _ int64) (int, error) {
	if r.short {
		return max(0, len(data)-1), nil
	}
	return 0, r.cause
}

func TestAcceptanceReaderAtFailuresKeepOriginalCause(t *testing.T) {
	for _, cause := range []error{io.ErrUnexpectedEOF, errors.New("fixture I/O failure")} {
		_, err := psdfile.Read(acceptanceBrokenReader{cause: cause}, 100)
		acceptanceAssertError(t, err, psdfile.ErrMalformed, "index", "header.data", 0)
		if !errors.Is(err, cause) {
			t.Fatalf("lost source I/O cause: %v", err)
		}
	}
	_, err := psdfile.Read(acceptanceBrokenReader{short: true}, 100)
	acceptanceAssertError(t, err, psdfile.ErrMalformed, "index", "header.data", 0)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short ReaderAt not reported as truncation: %v", err)
	}
}

func TestAcceptanceDecodeOnDemandReturnsOwnedPlanes(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := uint16(0); compression <= 3; compression++ {
			t.Run(fmt.Sprintf("v%d/compression%d", version, compression), func(t *testing.T) {
				fixture := acceptanceDocument(version, 8, compression, 1, "")
				document := acceptanceOpen(t, fixture)
				pixels, err := document.DecodeLayer(context.Background(), 0)
				if err != nil {
					t.Fatal(err)
				}
				planes := acceptancePlanes(8)
				for index, plane := range pixels.Channels {
					if !bytes.Equal(plane.Data, planes[index]) {
						t.Fatalf("plane%d=%v want=%v", index, plane.Data, planes[index])
					}
				}
				composite, err := document.DecodeComposite(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				for index, plane := range composite.Channels {
					if !bytes.Equal(plane.Data, planes[index]) {
						t.Fatalf("composite plane%d differs", index)
					}
				}
				pixels.Channels[0].Data[0] ^= 255
				composite.Channels[0].Data[0] ^= 255
				again, err := document.DecodeChannel(context.Background(), 0, 0)
				if err != nil || !bytes.Equal(again.Data, planes[0]) {
					t.Fatalf("layer decode borrowed mutable pixels: %v", err)
				}
				saved, err := document.DecodeCompositeChannel(context.Background(), 0)
				if err != nil || !bytes.Equal(saved.Data, planes[0]) {
					t.Fatalf("composite decode borrowed mutable pixels: %v", err)
				}
			})
		}
	}
}

func TestAcceptanceDecodeBudgetsAreDeferredAndScoped(t *testing.T) {
	for compression := uint16(0); compression <= 3; compression++ {
		t.Run(fmt.Sprintf("compression%d", compression), func(t *testing.T) {
			fixture := acceptanceDocument(2, 8, compression, 1, "")
			pixelLimited := acceptanceOpen(t, fixture,
				psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodePixels: 3}})
			_, err := pixelLimited.DecodeLayer(context.Background(), 0)
			acceptanceAssertError(t, err, psdfile.ErrLimit, "pixels", "layers[0].channels[0]", -1)
			_, err = pixelLimited.DecodeComposite(context.Background())
			acceptanceAssertError(t, err, psdfile.ErrLimit, "pixels", "composite", -1)
			byteLimited := acceptanceOpen(t, fixture,
				psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodePixels: 4, MaxDecodeBytes: 4}})
			plane, err := byteLimited.DecodeChannel(context.Background(), 0, 0)
			if err != nil || !bytes.Equal(plane.Data, acceptancePlanes(8)[0]) {
				t.Fatalf("single-channel budget charged unrelated planes: %v", err)
			}
			_, err = byteLimited.DecodeLayer(context.Background(), 0)
			acceptanceAssertError(t, err, psdfile.ErrLimit, "pixels", "layers[0].channels[1]", -1)
			_, err = byteLimited.DecodeComposite(context.Background())
			acceptanceAssertError(t, err, psdfile.ErrLimit, "pixels", "composite", -1)
			plane, err = byteLimited.DecodeCompositeChannel(context.Background(), 0)
			if compression < 2 {
				if err != nil || !bytes.Equal(plane.Data, acceptancePlanes(8)[0]) {
					t.Fatalf("independent composite plane budget charged unrelated channels: %v", err)
				}
			} else {
				acceptanceAssertError(t, err, psdfile.ErrLimit, "pixels", "composite.channels[0]", -1)
			}
			allPlanes := acceptanceOpen(t, fixture,
				psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodePixels: 4, MaxDecodeBytes: 20}})
			if _, err := allPlanes.DecodeLayer(context.Background(), 0); err != nil {
				t.Fatalf("pixel budget multiplied canvas area by channel count: %v", err)
			}
		})
	}
}

func TestAcceptanceCompressedFailuresAreDeferredToRequestedDecode(t *testing.T) {
	for _, compression := range []uint16{1, 2, 3} {
		t.Run(fmt.Sprintf("compression%d", compression), func(t *testing.T) {
			fixture := acceptanceDocument(2, 8, compression, 1, "")
			prefix := "layers/info/layer0/channel0/"
			feature := "pixels.zip"
			if compression == 1 {
				fixture.data[fixture.spans[prefix+"plane0"].Offset] = 127
				feature = "pixels.rle"
			} else {
				span := fixture.spans[prefix+"encoded"]
				fixture.data[span.Offset+span.Length-1] ^= 255
			}
			document := acceptanceOpen(t, fixture)
			if _, err := document.DecodeChannel(context.Background(), 0, 1); err != nil {
				t.Fatalf("unrelated channel decode examined broken payload: %v", err)
			}
			_, err := document.DecodeChannel(context.Background(), 0, 0)
			acceptanceAssertError(t, err, psdfile.ErrMalformed, feature, "layers[0].channels[0]", -1)
		})
	}
}

func TestAcceptanceSampleMetadataAndDeferredPixels(t *testing.T) {
	filename := testfixture.Sample(t)
	source, err := os.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	info, err := source.Stat()
	if err != nil {
		t.Fatal(err)
	}
	reader := &acceptanceReader{reader: source}
	document, err := psdfile.Read(reader, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = document.Close() })
	if len(document.Layers) != 16 || len(document.Resources) == 0 || len(document.AdditionalInfo) == 0 {
		t.Fatalf("real sample lost metadata: layers=%d resources=%d additional=%d",
			len(document.Layers), len(document.Resources), len(document.AdditionalInfo))
	}
	var indexedReadBytes int64
	compositePayload := document.Composite.Data
	if document.Composite.Compression == 1 {
		tableBytes := int64(document.Header.Height) * int64(document.Header.Channels) * 2
		compositePayload.Offset += tableBytes
		compositePayload.Length -= tableBytes
	}
	for _, read := range reader.reads {
		indexedReadBytes += read.Length
		if acceptanceOverlaps(read, compositePayload) {
			t.Fatalf("real-sample indexing read encoded composite pixels: %+v", read)
		}
		for _, block := range document.AdditionalInfo {
			if strings.HasPrefix(block.Key, "lnk") && acceptanceOverlaps(read, block.Data) {
				t.Fatalf("real-sample indexing read linked object payload: %+v", read)
			}
		}
		for _, layer := range document.Layers {
			for _, channel := range layer.Channels {
				payload := channel.Data
				if channel.Compression == 1 {
					tableBytes := int64(channel.Bounds.Dy()) * 2
					payload.Offset += tableBytes
					payload.Length -= tableBytes
				}
				if acceptanceOverlaps(read, payload) {
					t.Fatalf("real-sample indexing read encoded layer pixels: %+v", read)
				}
			}
		}
	}
	if indexedReadBytes > 1<<20 {
		t.Fatalf("metadata-only opening read %d bytes", indexedReadBytes)
	}
	t.Logf("sourceBytes=%d indexedBytes=%d resources=%d layerRecords=%d documentExtensions=%d",
		info.Size(), indexedReadBytes, len(document.Resources), len(document.Layers), len(document.AdditionalInfo))
	t.Logf("metadata-only sample: source=%d bytes, structural reads=%d bytes", info.Size(), indexedReadBytes)
	wantHash := sha256.New()
	if _, err := io.Copy(wantHash, io.NewSectionReader(source, 0, info.Size())); err != nil {
		t.Fatal(err)
	}
	gotHash := sha256.New()
	count, err := document.CopySpan(context.Background(), gotHash, document.Source)
	if err != nil || count != info.Size() || !bytes.Equal(wantHash.Sum(nil), gotHash.Sum(nil)) {
		t.Fatalf("real sample source was not preserved byte-for-byte: count=%d err=%v", count, err)
	}
	oracle, err := psdparse.ParseFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var oracleLayers []*psdmodel.Layer
	var flatten func([]*psdmodel.Layer)
	flatten = func(layers []*psdmodel.Layer) {
		for _, layer := range layers {
			oracleLayers = append(oracleLayers, layer)
			flatten(layer.Children)
		}
	}
	flatten(oracle.Layers)
	comparedLayer := false
	for index, layer := range document.Layers {
		if layer.Bounds.Empty() || len(layer.Channels) < 3 {
			continue
		}
		match := slices.IndexFunc(oracleLayers, func(candidate *psdmodel.Layer) bool {
			return candidate.Name == layer.Name && candidate.Bounds == layer.Bounds && candidate.Pixels != nil
		})
		if match < 0 {
			continue
		}
		pixels, err := document.DecodeLayer(context.Background(), index)
		if err != nil {
			t.Fatal(err)
		}
		for _, plane := range pixels.Channels {
			if plane.ID < 0 || plane.ID > 2 {
				continue
			}
			for y := range plane.Height {
				for x := range plane.Width {
					want := oracleLayers[match].Pixels.Pix[y*oracleLayers[match].Pixels.Stride+x*4+int(plane.ID)]
					if plane.Data[y*plane.RowBytes+x] != want {
						t.Fatalf("sample layer%d channel%d differs from eager oracle", index, plane.ID)
					}
				}
			}
		}
		comparedLayer = true
		break
	}
	if !comparedLayer {
		t.Fatal("real-sample test did not compare any layer against the independent eager oracle")
	}
	composite, err := document.DecodeComposite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if composite.Width != oracle.Width || composite.Height != oracle.Height || len(composite.Channels) != 4 {
		t.Fatalf("sample composite geometry/channels differ: %+v", composite)
	}
	// The eager renderer removes the saved white matte, while source planes retain it.
	for y := range composite.Height {
		for x := range composite.Width {
			if composite.Channels[3].Data[y*composite.Channels[3].RowBytes+x] !=
				oracle.Composite.Pix[y*oracle.Composite.Stride+x*4+3] {
				t.Fatal("sample composite alpha differs from eager oracle")
			}
		}
	}
}

func FuzzAcceptanceIndex(f *testing.F) {
	for _, version := range []uint16{1, 2} {
		f.Add(acceptanceFlat(version, 8, 3, 3, nil).data)
		f.Add(acceptanceFlat(version, 16, 1, 1, nil).data)
	}
	f.Add(acceptanceDocument(1, 8, 1, 1, "").data)
	f.Add(acceptanceDocument(2, 16, 0, 1, "Lr16").data)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256<<10 {
			t.Skip()
		}
		options := psdfile.LoadOptions{Limits: psdfile.Limits{
			MaxFileBytes: 256 << 10, MaxMetadataBytes: 128 << 10, MaxLayers: 32, MaxBlocks: 256,
			MaxDecodePixels: 4096, MaxDecodeBytes: 256 << 10,
		}}
		document, err := psdfile.Parse(data, options)
		if err != nil {
			if !errors.Is(err, psdfile.ErrMalformed) && !errors.Is(err, psdfile.ErrLimit) {
				t.Fatalf("index returned unclassified input error: %v", err)
			}
			return
		}
		defer document.Close()
		check := func(span psdfile.Span) {
			if span.Offset < 0 || span.Length < 0 || span.Offset > int64(len(data)) ||
				span.Length > int64(len(data))-span.Offset {
				t.Fatalf("index exposed span outside source: %+v", span)
			}
		}
		for _, span := range []psdfile.Span{
			document.Source, document.HeaderData, document.ColorModeData, document.ResourceSection,
			document.LayerMaskSection, document.LayerInfo, document.GlobalMask, document.Composite.Span, document.Composite.Data,
		} {
			check(span)
		}
		for _, resource := range document.Resources {
			for _, span := range []psdfile.Span{resource.Span, resource.NameData, resource.Data, resource.Padding} {
				check(span)
			}
		}
		for _, set := range document.LayerSets {
			check(set.Span)
			check(set.Padding)
			for _, layer := range set.Layers {
				for _, span := range []psdfile.Span{layer.Span, layer.Extra, layer.NameData, layer.MaskData, layer.BlendRanges} {
					check(span)
				}
				for _, channel := range layer.Channels {
					check(channel.Span)
					check(channel.Data)
				}
				for _, block := range layer.AdditionalInfo {
					check(block.Span)
					check(block.Data)
					check(block.Padding)
				}
			}
		}
		for _, block := range document.AdditionalInfo {
			check(block.Span)
			check(block.Data)
			check(block.Padding)
		}
	})
}
