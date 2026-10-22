package psdfile

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/testfixture"
)

func indexTestNumber(target *bytes.Buffer, value any) {
	_ = binary.Write(target, binary.BigEndian, value)
}

func indexTestLength(target *bytes.Buffer, version uint16, length int) {
	if version == 2 {
		indexTestNumber(target, uint64(length))
		return
	}
	indexTestNumber(target, uint32(length))
}

func indexTestDocument(version, depth, mode uint16, resources, layerMask, composite []byte) []byte {
	var data bytes.Buffer
	data.WriteString("8BPS")
	indexTestNumber(&data, version)
	data.Write(make([]byte, 6))
	indexTestNumber(&data, uint16(1))
	indexTestNumber(&data, uint32(1))
	indexTestNumber(&data, uint32(1))
	indexTestNumber(&data, depth)
	indexTestNumber(&data, mode)
	indexTestNumber(&data, uint32(0))
	indexTestNumber(&data, uint32(len(resources)))
	data.Write(resources)
	indexTestLength(&data, version, len(layerMask))
	data.Write(layerMask)
	data.Write(composite)
	return data.Bytes()
}

func indexTestGlobal(version uint16, layers, tags []byte) []byte {
	var data bytes.Buffer
	indexTestLength(&data, version, len(layers))
	data.Write(layers)
	indexTestNumber(&data, uint32(0))
	data.Write(tags)
	return data.Bytes()
}

func indexTestTag(version uint16, signature, key string, payload []byte, alignment int) []byte {
	var data bytes.Buffer
	data.WriteString(signature)
	data.WriteString(key)
	if signature == "8B64" || version == 2 && indexPSBLength64(key) {
		indexTestNumber(&data, uint64(len(payload)))
	} else {
		indexTestNumber(&data, uint32(len(payload)))
	}
	data.Write(payload)
	for padding := (alignment - len(payload)%alignment) % alignment; padding > 0; padding-- {
		data.WriteByte(0xA5)
	}
	return data.Bytes()
}

func indexTestLayerSet(
	version uint16, bounds image.Rectangle, ids []int16, channels [][]byte, mask, tags []byte,
) []byte {
	var data bytes.Buffer
	indexTestNumber(&data, int16(-1))
	for _, value := range []int{bounds.Min.Y, bounds.Min.X, bounds.Max.Y, bounds.Max.X} {
		indexTestNumber(&data, int32(value))
	}
	indexTestNumber(&data, uint16(len(ids)))
	for index, id := range ids {
		indexTestNumber(&data, id)
		indexTestLength(&data, version, len(channels[index]))
	}
	data.WriteString("8BIMnorm")
	data.Write([]byte{255, 0, 0, 0})
	var extra bytes.Buffer
	indexTestNumber(&extra, uint32(len(mask)))
	extra.Write(mask)
	indexTestNumber(&extra, uint32(0))
	extra.Write([]byte{1, 'L', 0, 0})
	extra.Write(tags)
	indexTestNumber(&data, uint32(extra.Len()))
	data.Write(extra.Bytes())
	for _, channel := range channels {
		data.Write(channel)
	}
	if data.Len()%2 != 0 {
		data.WriteByte(0xA4)
	}
	return data.Bytes()
}

func TestIndexLegalHeadersWithoutPixelDecode(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, depth := range []uint16{1, 8, 16, 32} {
			for _, mode := range []uint16{0, 1, 2, 3, 4, 7, 8, 9} {
				data := indexTestDocument(version, depth, mode, nil, nil, []byte{0, 2})
				document, err := Parse(data)
				if err != nil {
					t.Fatalf("version=%d depth=%d mode=%d: %v", version, depth, mode, err)
				}
				if document.Header.Version != version || document.Header.Depth != depth ||
					document.Header.ColorMode != mode || document.ActiveLayerSet != -1 {
					t.Fatalf("header changed: %#v", document.Header)
				}
			}
		}
	}
}

func TestIndexHeaderAndUnsignedLengthValidation(t *testing.T) {
	valid := indexTestDocument(2, 8, 3, nil, nil, []byte{0, 0, 9})
	cases := []struct {
		name   string
		mutate func([]byte)
		path   string
	}{
		{"reserved", func(data []byte) { data[7] = 1 }, "header.reserved"},
		{"channels", func(data []byte) { binary.BigEndian.PutUint16(data[12:], 57) }, "header.channels"},
		{"height", func(data []byte) { binary.BigEndian.PutUint32(data[14:], 300_001) }, "header.height"},
		{"depth", func(data []byte) { binary.BigEndian.PutUint16(data[22:], 24) }, "header.depth"},
		{"mode", func(data []byte) { binary.BigEndian.PutUint16(data[24:], 5) }, "header.colorMode"},
		{"unsigned length", func(data []byte) { binary.BigEndian.PutUint64(data[34:], math.MaxUint64) },
			"document.layerMask.length"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			data := bytes.Clone(valid)
			test.mutate(data)
			_, err := Parse(data)
			var indexedError *Error
			if !errors.Is(err, ErrMalformed) || !errors.As(err, &indexedError) || indexedError.Path != test.path {
				t.Fatalf("got %v, want malformed %s", err, test.path)
			}
		})
	}
}

func TestIndexPSBKeysAndOriginalGlobalPadding(t *testing.T) {
	keys := []string{"LMsk", "Lr16", "Lr32", "Layr", "Mt16", "Mt32", "Mtrn", "Alph", "FMsk", "lnk2",
		"FEid", "FXid", "PxSD", "zzzz"}
	for _, version := range []uint16{1, 2} {
		for _, signature := range []string{"8BIM", "8B64"} {
			var tags []byte
			for _, key := range keys {
				payload := []byte{5}
				if key == "Layr" || key == "Lr16" || key == "Lr32" {
					payload = []byte{0, 0}
				}
				tags = append(tags, indexTestTag(version, signature, key, payload, 4)...)
			}
			data := indexTestDocument(version, 8, 1, nil, indexTestGlobal(version, nil, tags), []byte{0, 0, 9})
			document, err := Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			if len(document.AdditionalInfo) != len(keys) {
				t.Fatalf("got %d tags", len(document.AdditionalInfo))
			}
			for index, key := range keys {
				block := document.AdditionalInfo[index]
				width := uint8(4)
				if signature == "8B64" || version == 2 && key != "zzzz" {
					width = 8
				}
				if block.Key != key || block.LengthWidth != width || block.Padding.Length != 4-block.Data.Length {
					t.Fatalf("unexpected block: %#v", block)
				}
				if !bytes.Equal(data[block.Padding.Offset:block.Padding.Offset+block.Padding.Length],
					bytes.Repeat([]byte{0xA5}, int(block.Padding.Length))) {
					t.Fatal("global padding was not preserved")
				}
			}
		}
	}
}

func TestIndexAlternateLayersSelectSourceDepth(t *testing.T) {
	main := indexTestLayerSet(2, image.Rectangle{}, nil, nil, nil, nil)
	alternate := indexTestLayerSet(2, image.Rect(-1, -2, 0, -1), []int16{0}, [][]byte{{0, 0, 12, 34}}, nil, nil)
	tags := indexTestTag(2, "8BIM", "Lr16", alternate, 4)
	data := indexTestDocument(2, 16, 1, nil, indexTestGlobal(2, main, tags), []byte{0, 0, 5, 6})
	document, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.LayerSets) != 2 || document.ActiveLayerSet != 1 || !document.GlobalAlpha ||
		len(document.Layers) != 1 || document.Layers[0].Channels[0].Bounds != image.Rect(-1, -2, 0, -1) {
		t.Fatalf("alternate set not selected: %#v", document.LayerSets)
	}
	if document.LayerInfo.Length != 8+int64(len(main)) || document.LayerSets[1].Span.Length != int64(len(alternate)) {
		t.Fatal("length prefix included in an alternate layer-set payload")
	}
}

func TestIndexEmptyChannelAndMaskGeometry(t *testing.T) {
	for _, payload := range [][]byte{nil, {0, 0}} {
		layers := indexTestLayerSet(1, image.Rectangle{}, []int16{0}, [][]byte{payload}, nil, nil)
		_, err := Parse(indexTestDocument(1, 8, 1, nil, indexTestGlobal(1, layers, nil), []byte{0, 0, 6}))
		if err != nil {
			t.Fatal(err)
		}
	}
	var mask bytes.Buffer
	for _, value := range []int32{0, 0, 1, 2} {
		indexTestNumber(&mask, value)
	}
	mask.Write([]byte{255, 17, 15, 200})
	indexTestNumber(&mask, float64(2.5))
	mask.WriteByte(100)
	indexTestNumber(&mask, float64(1.5))
	mask.Write([]byte{1, 0})
	for _, value := range []int32{3, 4, 4, 7} {
		indexTestNumber(&mask, value)
	}
	layers := indexTestLayerSet(1, image.Rect(8, 9, 9, 10), []int16{-2, -3},
		[][]byte{{0, 0, 10, 11}, {0, 0, 12, 13, 14}}, mask.Bytes(), nil)
	document, err := Parse(indexTestDocument(1, 8, 1, nil, indexTestGlobal(1, layers, nil), []byte{0, 0, 6}))
	if err != nil {
		t.Fatal(err)
	}
	indexedMask := document.Layers[0].Mask
	if !indexedMask.HasReal || indexedMask.Flags != 17 || *indexedMask.UserDensity != 200 ||
		*indexedMask.UserFeather != 2.5 || *indexedMask.VectorDensity != 100 || *indexedMask.VectorFeather != 1.5 {
		t.Fatalf("mask metadata changed: %#v", indexedMask)
	}
	if document.Layers[0].Channels[0].Bounds != image.Rect(0, 0, 2, 1) ||
		document.Layers[0].Channels[1].Bounds != image.Rect(4, 3, 7, 4) {
		t.Fatal("mask channel geometry lost")
	}
}

type indexForbiddenReader struct {
	data      []byte
	forbidden []Span
	reads     []Span
}

func (r *indexForbiddenReader) ReadAt(data []byte, offset int64) (int, error) {
	span := Span{Offset: offset, Length: int64(len(data))}
	r.reads = append(r.reads, span)
	for _, forbidden := range r.forbidden {
		if offset < forbidden.Offset+forbidden.Length && forbidden.Offset < offset+int64(len(data)) {
			return 0, errors.New("index read a deferred payload")
		}
	}
	return bytes.NewReader(r.data).ReadAt(data, offset)
}

func TestIndexNeverReadsUnknownOrPixelPayloads(t *testing.T) {
	var resource bytes.Buffer
	resource.WriteString("8BIM")
	indexTestNumber(&resource, uint16(4001))
	resource.Write([]byte{0, 0})
	indexTestNumber(&resource, uint32(1024))
	resource.Write(make([]byte, 1024))
	layers := indexTestLayerSet(2, image.Rect(0, 0, 1, 1), []int16{0}, [][]byte{{0, 0, 9}}, nil, nil)
	tags := indexTestTag(2, "8B64", "lnk2", make([]byte, 2<<20), 4)
	data := indexTestDocument(2, 8, 3, resource.Bytes(), indexTestGlobal(2, layers, tags), []byte{0, 0, 5})
	document, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	reader := &indexForbiddenReader{data: data, forbidden: []Span{
		document.Resources[0].Data, document.AdditionalInfo[0].Data,
		document.Layers[0].Channels[0].Data, document.Composite.Data,
	}}
	_, err = Read(reader, int64(len(data)), LoadOptions{Limits: Limits{MaxMetadataBytes: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	for _, span := range reader.reads {
		if span.Length > 26 {
			t.Fatalf("unexpected structural read-ahead: %#v", span)
		}
	}
}

func TestIndexRLETableAndSourceShortReads(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		var composite bytes.Buffer
		indexTestNumber(&composite, uint16(1))
		if version == 2 {
			indexTestNumber(&composite, uint32(2))
		} else {
			indexTestNumber(&composite, uint16(2))
		}
		composite.Write([]byte{0, 45})
		data := indexTestDocument(version, 8, 1, nil, nil, composite.Bytes())
		document, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		countWidth := int64(document.Composite.RowCountWidth)
		reader := &indexForbiddenReader{data: data, forbidden: []Span{{
			Offset: document.Composite.Data.Offset + countWidth, Length: 2,
		}}}
		if _, err := Read(reader, int64(len(data))); err != nil {
			t.Fatal(err)
		}
		truncated := data[:document.Composite.Data.Offset+countWidth-1]
		if _, err := Parse(truncated); !errors.Is(err, ErrMalformed) || !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("truncated table cause not preserved: %v", err)
		}
		short := &indexShortReader{data: data}
		if _, err := Read(short, int64(len(data))); !errors.Is(err, ErrMalformed) || !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("short ReaderAt cause not preserved: %v", err)
		}
	}
}

type indexShortReader struct{ data []byte }

func (r *indexShortReader) ReadAt(data []byte, offset int64) (int, error) {
	if len(data) > 0 {
		return bytes.NewReader(r.data).ReadAt(data[:len(data)-1], offset)
	}
	return 0, nil
}

func TestIndexAllocationAndCountBudgets(t *testing.T) {
	layers := indexTestLayerSet(1, image.Rectangle{}, nil, nil, nil, nil)
	tags := append(indexTestTag(1, "8BIM", "zzzz", nil, 4), indexTestTag(1, "8BIM", "zzzz", nil, 4)...)
	data := indexTestDocument(1, 8, 1, nil, indexTestGlobal(1, layers, tags), []byte{0, 0, 5})
	for _, limits := range []Limits{{MaxMetadataBytes: 1}, {MaxBlocks: 1}, {MaxFileBytes: int64(len(data) - 1)}} {
		if _, err := Parse(data, LoadOptions{Limits: limits}); !errors.Is(err, ErrLimit) {
			t.Fatalf("budget not enforced: %+v, err=%v", limits, err)
		}
	}
	alternate := indexTestTag(1, "8BIM", "Layr", layers, 4)
	data = indexTestDocument(1, 8, 1, nil, indexTestGlobal(1, layers, alternate), []byte{0, 0, 5})
	if _, err := Parse(data, LoadOptions{Limits: Limits{MaxLayers: 1}}); !errors.Is(err, ErrLimit) {
		t.Fatalf("alternate layers bypassed total MaxLayers: %v", err)
	}
}

func TestIndexRealSample(t *testing.T) {
	document, err := Open(testfixture.Sample(t))
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	if len(document.Layers) == 0 || len(document.Resources) == 0 || len(document.AdditionalInfo) == 0 {
		t.Fatal("real document structure was not indexed")
	}
	for _, layer := range document.Layers {
		for _, channel := range layer.Channels {
			if !document.validSpan(channel.Span) || !document.validSpan(channel.Data) {
				t.Fatal("channel span outside original sample")
			}
		}
	}
}

func TestIndexRejectsTruncatedTaggedHeadersAndLayerTails(t *testing.T) {
	for _, tail := range [][]byte{[]byte("8B"), []byte("8BI"), []byte("8BIM"), []byte("8BIMxxxx"),
		[]byte("8BIMxxxx\x00\x00\x00")} {
		data := indexTestDocument(1, 8, 1, nil, indexTestGlobal(1, nil, tail), []byte{0, 0, 9})
		if _, err := Parse(data); !errors.Is(err, ErrMalformed) || !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("truncated tag %q accepted: %v", tail, err)
		}
	}
	layers := []byte{0, 0, 0, 0, 0, 0}
	data := indexTestDocument(1, 8, 1, nil, indexTestGlobal(1, layers, nil), []byte{0, 0, 9})
	if _, err := Parse(data); !errors.Is(err, ErrMalformed) {
		t.Fatalf("unconsumed layer payload accepted: %v", err)
	}
}

func TestIndexRejectsUnboundedAlignmentWithFewSourceReads(t *testing.T) {
	for _, global := range []bool{false, true} {
		tags := indexTestTag(1, "8BIM", "zzzz", nil, 4)
		tags = append(tags, make([]byte, 100_000)...)
		tags = append(tags, indexTestTag(1, "8BIM", "zzzz", nil, 4)...)
		var layerMask []byte
		if global {
			layerMask = indexTestGlobal(1, nil, tags)
		} else {
			layers := indexTestLayerSet(1, image.Rectangle{}, nil, nil, nil, tags)
			layerMask = indexTestGlobal(1, layers, nil)
		}
		data := indexTestDocument(1, 8, 1, nil, layerMask, []byte{0, 0, 9})
		reader := &indexForbiddenReader{data: data}
		_, err := Read(reader, int64(len(data)), LoadOptions{Limits: Limits{MaxMetadataBytes: 4096}})
		var indexedError *Error
		if !errors.Is(err, ErrMalformed) || !errors.As(err, &indexedError) ||
			indexedError.Detail != "additional information gap exceeds three alignment bytes" {
			t.Fatalf("global=%t: expected invalid alignment gap, got %v", global, err)
		}
		if len(reader.reads) > 30 {
			t.Fatalf("global=%t: gap required %d source reads", global, len(reader.reads))
		}
	}
}
