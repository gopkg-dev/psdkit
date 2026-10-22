package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"io"
	"os"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func vectorStrokeTestMask(width, height int, left, top, right, bottom float64) *psdvector.Mask {
	path := psdvector.Subpath{Closed: true, Operation: 1}
	for _, xy := range [][2]float64{{left, top}, {right, top}, {right, bottom}, {left, bottom}} {
		point := psdvector.Point{X: xy[0], Y: xy[1]}
		path.Knots = append(path.Knots, psdvector.Knot{Preceding: point, Anchor: point, Leaving: point})
	}
	return &psdvector.Mask{Width: width, Height: height, Version: 3, Subpaths: []psdvector.Subpath{path}}
}

func vectorStrokeTestTags(t *testing.T, width float64, extra ...artboardTestEntry) []byte {
	t.Helper()
	rgb := func(value float64) []byte {
		return artboardTestDescriptor("RGBC",
			artboardTestEntry{"Rd  ", "doub", artboardTestDouble(value)},
			artboardTestEntry{"Grn ", "doub", artboardTestDouble(value)},
			artboardTestEntry{"Bl  ", "doub", artboardTestDouble(value)})
	}
	fill := append([]byte("SoCo"), artboardTestU32(16)...)
	fill = append(fill, artboardTestDescriptor("null", artboardTestEntry{"Clr ", "Objc", rgb(255)})...)
	entries := []artboardTestEntry{
		{"strokeStyleVersion", "long", artboardTestU32(2)},
		{"strokeEnabled", "bool", []byte{1}}, {"fillEnabled", "bool", []byte{1}},
		{"strokeStyleLineWidth", "UntF", cachedTestUnit("#Pxl", width)},
		{"strokeStyleLineDashOffset", "UntF", cachedTestUnit("#Pnt", 0)},
		{"strokeStyleMiterLimit", "doub", artboardTestDouble(100)},
		{"strokeStyleLineCapType", "enum", cachedTestEnum("strokeStyleLineCapType", "strokeStyleButtCap")},
		{"strokeStyleLineJoinType", "enum", cachedTestEnum("strokeStyleLineJoinType", "strokeStyleMiterJoin")},
		{"strokeStyleLineAlignment", "enum", cachedTestEnum("strokeStyleLineAlignment", "strokeStyleAlignInside")},
		{"strokeStyleScaleLock", "bool", []byte{0}}, {"strokeStyleStrokeAdjust", "bool", []byte{0}},
		{"strokeStyleLineDashSet", "VlLs", artboardTestU32(0)},
		{"strokeStyleBlendMode", "enum", cachedTestEnum("BlnM", "Nrml")},
		{"strokeStyleOpacity", "UntF", cachedTestUnit("#Prc", 100)},
		{"strokeStyleContent", "Objc", artboardTestDescriptor("solidColorLayer",
			artboardTestEntry{"Clr ", "Objc", rgb(199)})},
		{"strokeStyleResolution", "doub", artboardTestDouble(72)},
	}
	stroke := append(artboardTestU32(16), artboardTestDescriptor("strokeStyle", append(entries, extra...)...)...)
	return append(artboardTestBlock("vscg", fill), artboardTestBlock("vstk", stroke)...)
}

func vectorStrokeTestPSD(t *testing.T, width, height int, bounds image.Rectangle, tags []byte) []byte {
	t.Helper()
	var record, pixels bytes.Buffer
	_ = binary.Write(&record, binary.BigEndian, int16(-1))
	for _, coordinate := range []int{bounds.Min.Y, bounds.Min.X, bounds.Max.Y, bounds.Max.X} {
		_ = binary.Write(&record, binary.BigEndian, int32(coordinate))
	}
	_ = binary.Write(&record, binary.BigEndian, uint16(4))
	for component, id := range []int16{0, 1, 2, -1} {
		_ = binary.Write(&record, binary.BigEndian, id)
		_ = binary.Write(&record, binary.BigEndian, uint32(bounds.Dx()*bounds.Dy()+2))
		pixels.Write([]byte{0, 0})
		pixels.Write(bytes.Repeat([]byte{byte(11 + component*20)}, bounds.Dx()*bounds.Dy()))
	}
	record.WriteString("8BIMnorm")
	record.Write([]byte{255, 0, 0, 0})
	extra := append(make([]byte, 8), 1, 'L', 0, 0)
	extra = append(extra, tags...)
	_ = binary.Write(&record, binary.BigEndian, uint32(len(extra)))
	record.Write(extra)
	record.Write(pixels.Bytes())
	if record.Len()%2 != 0 {
		record.WriteByte(0)
	}
	section := append(artboardTestU32(uint32(record.Len())), record.Bytes()...)
	section = append(section, 0, 0, 0, 0)
	data := binary.BigEndian.AppendUint16([]byte("8BPS"), 1)
	data = append(data, make([]byte, 6)...)
	data = binary.BigEndian.AppendUint16(data, 4)
	data = append(data, artboardTestU32(uint32(height))...)
	data = append(data, artboardTestU32(uint32(width))...)
	data = binary.BigEndian.AppendUint16(data, 8)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = append(data, make([]byte, 8)...)
	data = append(data, artboardTestU32(uint32(len(section)))...)
	data = append(data, section...)
	return append(data, append([]byte{0, 0}, bytes.Repeat([]byte{17}, width*height*4)...)...)
}

func vectorStrokeTestSource(t *testing.T, left, top, right, bottom, width float64) (*psdfile.Document, []byte) {
	t.Helper()
	mask := vectorStrokeTestMask(20, 18, left, top, right, bottom)
	vector, err := psdvector.EncodeMask(nil, mask)
	if err != nil {
		t.Fatal(err)
	}
	tags := append(vectorStrokeTestTags(t, width), artboardTestBlock("vsms", vector)...)
	// Deliberately unrelated cached geometry and pixels must never constrain the live path.
	data := vectorStrokeTestPSD(t, 20, 18, image.Rect(7, 7, 9, 9), tags)
	return cachedOpen(t, data), data
}

func TestApproximateVectorStrokeSolidRectangleAndDefaultRefusal(t *testing.T) {
	source, data := vectorStrokeTestSource(t, 2, 2, 18, 16, 2)
	original := sha256.Sum256(data)
	if _, err := Render(nil, source); !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("default live stroke unexpectedly accepted: %v", err)
	}
	pixels, err := Render(nil, source, Options{ApproximateVectorStroke: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, point := range []image.Point{{2, 2}, {3, 8}, {16, 8}, {10, 15}} {
		if got := pixels.NRGBAAt(point.X, point.Y); got != (color.NRGBA{199, 199, 199, 255}) {
			t.Fatalf("inside edge at %v: got %v", point, got)
		}
	}
	if got := pixels.NRGBAAt(9, 9); got != (color.NRGBA{255, 255, 255, 255}) {
		t.Fatalf("fill center: %v", got)
	}
	if pixels.NRGBAAt(1, 8).A != 0 || pixels.NRGBAAt(18, 8).A != 0 || sha256.Sum256(data) != original {
		t.Fatal("stroke expanded outside its vector path or changed source bytes")
	}
	cached, err := Render(nil, source, Options{ApproximateVectorStroke: true, CachedSpecialLayers: true})
	if err != nil || !bytes.Equal(pixels.Pix, cached.Pix) {
		t.Fatalf("explicit live vector was replaced by saved special cache: %v", err)
	}
}

func TestApproximateVectorStrokeFractionalFullAndTiles(t *testing.T) {
	source, _ := vectorStrokeTestSource(t, 2.25, 2.25, 17.75, 15.75, 2.5)
	full, err := Render(nil, source, Options{ApproximateVectorStroke: true})
	if err != nil {
		t.Fatal(err)
	}
	// Pixel (2, 2) has 6/8 by 6/8 outer samples: the expected single coverage is 143, not its square.
	if got := full.NRGBAAt(2, 2); got != (color.NRGBA{199, 199, 199, 143}) {
		t.Fatalf("vector outline was applied more than once: %v", got)
	}
	for _, rectangle := range []image.Rectangle{image.Rect(0, 0, 4, 5), image.Rect(8, 7, 13, 10),
		image.Rect(16, 12, 20, 18), image.Rect(3, 3, 17, 15)} {
		tile, err := RenderTile(nil, source, rectangle, TileOptions{ApproximateVectorStroke: true})
		if err != nil {
			t.Fatal(err)
		}
		for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
			for x := rectangle.Min.X; x < rectangle.Max.X; x++ {
				if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
					t.Fatalf("full/ROI seam at (%d, %d)", x, y)
				}
			}
		}
	}
}

func TestApproximateVectorStrokeWithOpenTypeScenePlanning(t *testing.T) {
	source, _ := vectorStrokeTestSource(t, 2.25, 2.25, 17.75, 15.75, 2.5)
	baseline, err := Render(nil, source, Options{ApproximateVectorStroke: true})
	if err != nil {
		t.Fatal(err)
	}
	policy := &OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone}
	for _, cached := range []bool{false, true} {
		actual, err := Render(nil, source, Options{ApproximateVectorStroke: true,
			ApproximateOpenTypeText: policy, CachedSpecialLayers: cached})
		if err != nil || !bytes.Equal(actual.Pix, baseline.Pix) {
			t.Fatalf("OpenType planning rejected or selected saved vector cache: %v", err)
		}
	}
}

func TestApproximateVectorStrokeMetadataRejectsUnknownAndDuplicate(t *testing.T) {
	for _, entry := range []artboardTestEntry{{"futureStrokeStyle", "bool", []byte{0}},
		{"strokeStyleVersion", "long", artboardTestU32(2)}} {
		data := cachedTestSource(vectorStrokeTestTags(t, 5, entry))
		source := cachedOpen(t, data)
		var fill, stroke psdfile.AdditionalBlock
		for _, block := range source.Layers[0].AdditionalInfo {
			switch block.Key {
			case "vscg":
				fill = block
			case "vstk":
				stroke = block
			}
		}
		_, _, err := decodeVectorStroke(context.Background(), source, fill, stroke, 1<<20)
		if !errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatalf("unknown or duplicate field %s accepted: %v", entry.key, err)
		}
	}
}

type vectorStrokeForbiddenReader struct {
	reader    *bytes.Reader
	forbidden []psdfile.Span
	reads     int
}

func (reader *vectorStrokeForbiddenReader) ReadAt(data []byte, offset int64) (int, error) {
	for _, span := range reader.forbidden {
		if offset < span.Offset+span.Length && offset+int64(len(data)) > span.Offset {
			reader.reads++
			return 0, errors.New("saved vector raster or merged preview read is forbidden")
		}
	}
	return reader.reader.ReadAt(data, offset)
}

func TestApproximateVectorStrokeNeverReadsSavedLayerOrMergedSamples(t *testing.T) {
	indexed, data := vectorStrokeTestSource(t, 2.25, 2.25, 17.75, 15.75, 5)
	reader := &vectorStrokeForbiddenReader{reader: bytes.NewReader(data)}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, channel := range indexed.Layers[0].Channels {
		reader.forbidden = append(reader.forbidden, channel.Data)
	}
	reader.forbidden = append(reader.forbidden, indexed.Composite.Span)
	for _, rectangle := range []image.Rectangle{image.Rect(0, 0, 20, 18), image.Rect(8, 7, 13, 10)} {
		_, err := RenderTile(nil, source, rectangle, TileOptions{ApproximateVectorStroke: true,
			CachedSpecialLayers: true})
		if err != nil || reader.reads != 0 {
			t.Fatalf("live stroke read saved samples: %v (%d forbidden reads)", err, reader.reads)
		}
	}
	_, err = Render(nil, source, Options{ApproximateVectorStroke: true})
	if err != nil || reader.reads != 0 {
		t.Fatalf("full live stroke read saved samples: %v (%d forbidden reads)", err, reader.reads)
	}
	var probe [1]byte
	if _, err := reader.ReadAt(probe[:], reader.forbidden[0].Offset); err == nil || errors.Is(err, io.EOF) {
		t.Fatal("sample guard is not actually active")
	}
}

func TestApproximateVectorStrokeRejectsUnimplementedStyleParameters(t *testing.T) {
	source := cachedOpen(t, cachedTestSource(vectorStrokeTestTags(t, 5)))
	var descriptor *psdfile.Descriptor
	for _, block := range source.Layers[0].AdditionalInfo {
		if block.Key == "vstk" {
			var err error
			descriptor, err = source.DecodeDescriptor(block.Data, true)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	mutations := []psdfile.DescriptorEntry{
		{Key: "strokeStyleVersion", Type: "long", Value: int32(1)},
		{Key: "strokeEnabled", Type: "bool", Value: false},
		{Key: "fillEnabled", Type: "bool", Value: false},
		{Key: "strokeStyleScaleLock", Type: "bool", Value: true},
		{Key: "strokeStyleStrokeAdjust", Type: "bool", Value: true},
		{Key: "strokeStyleResolution", Type: "doub", Value: float64(96)},
		{Key: "strokeStyleMiterLimit", Type: "doub", Value: float64(4)},
		{Key: "strokeStyleLineWidth", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Pnt", Number: float64(5)}},
		{Key: "strokeStyleOpacity", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Prc", Number: float64(50)}},
		{Key: "strokeStyleLineDashSet", Type: "VlLs", Value: []psdfile.DescriptorValue{{Type: "doub", Value: float64(1)}}},
		{Key: "strokeStyleLineDashOffset", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Pnt", Number: float64(1)}},
		{Key: "strokeStyleLineAlignment", Type: "enum", Value: psdfile.DescriptorEnum{
			TypeID: "strokeStyleLineAlignment", ValueID: "strokeStyleAlignCenter"}},
		{Key: "strokeStyleBlendMode", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "BlnM", ValueID: "Mltp"}},
		{Key: "strokeStyleLineCapType", Type: "enum", Value: psdfile.DescriptorEnum{
			TypeID: "strokeStyleLineCapType", ValueID: "strokeStyleRoundCap"}},
		{Key: "strokeStyleLineJoinType", Type: "enum", Value: psdfile.DescriptorEnum{
			TypeID: "strokeStyleLineJoinType", ValueID: "strokeStyleRoundJoin"}},
		{Key: "strokeStyleContent", Type: "Objc", Value: (*psdfile.Descriptor)(nil)},
	}
	for _, mutation := range mutations {
		t.Run(mutation.Key, func(t *testing.T) {
			modified := *descriptor
			modified.Entries = append([]psdfile.DescriptorEntry{}, descriptor.Entries...)
			for index, entry := range modified.Entries {
				if entry.Key == mutation.Key {
					modified.Entries[index] = mutation
				}
			}
			if _, _, err := vectorStrokeParameters(&modified, 0); !errors.Is(err, psdfile.ErrUnsupported) {
				t.Fatalf("unimplemented style accepted: %v", err)
			}
		})
	}
	if _, err := vectorStrokeColor(&psdfile.Descriptor{ClassID: "null", Entries: []psdfile.DescriptorEntry{
		{Key: "Clr ", Type: "Objc", Value: (*psdfile.Descriptor)(nil)},
	}}, "null", 0); !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("typed nil RGB descriptor accepted: %v", err)
	}
}

func TestApproximateVectorStrokeBudgetCancellationAndOutsideCanvas(t *testing.T) {
	mask := vectorStrokeTestMask(20, 18, -4, -4, 15, 15)
	style := vectorStrokeStyle{fill: [3]float64{255, 255, 255}, stroke: [3]float64{199, 199, 199}, width: 5}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := rasterVectorStroke(ctx, mask, image.Rect(0, 0, 10, 10), style, 1<<20); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	_, err := rasterVectorStroke(context.Background(), mask, image.Rect(0, 0, 10, 10), style, 400)
	if !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("stroke coverage scratch bypassed output budget: %v", err)
	}
	full, err := rasterVectorStroke(context.Background(), mask, image.Rect(-4, -4, 15, 15), style, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	tile, err := rasterVectorStroke(context.Background(), mask, image.Rect(0, 0, 5, 5), style, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for y := range 5 {
		for x := range 5 {
			if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
				t.Fatalf("off-canvas source dependency was clipped at (%d, %d)", x, y)
			}
		}
	}
}

func TestApproximateVectorStrokeRealTemplateLayer12(t *testing.T) {
	path := os.Getenv("PSDKIT_VECTOR_STROKE_TEST_PSD")
	if path == "" {
		t.Skip("explicit real template path is required")
	}
	source, err := psdfile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	view, err := source.ReadLayerMetadata(nil, 12)
	if err != nil {
		t.Fatal(err)
	}
	layer := view.Layer
	engine := rgbTileRenderer{source: source, settings: TileOptions{MaxWorkingBytes: 32 << 20,
		ApproximateVectorStroke: true}, window: source.Layers[12].Bounds, live: make(map[*psdmodel.Layer]bool)}
	if ok, err := engine.prepareVectorStroke(context.Background(), source.Layers[12], layer); !ok || err != nil {
		t.Fatalf("real original solid inside stroke rejected: %v", err)
	}
}
