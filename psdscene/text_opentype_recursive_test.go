package psdscene

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
)

func recursiveTextParentFixture(t *testing.T, child []byte, external bool, canvas image.Point, quad []float64) []byte {
	t.Helper()
	prototype, err := psdfile.Parse(child)
	if err != nil {
		t.Fatal(err)
	}
	defer prototype.Close()
	profile, err := ReadSourceRGBProfile(nil, prototype, 0)
	if err != nil {
		t.Fatal(err)
	}
	var resources []byte
	if profile != nil {
		resources = []byte{'8', 'B', 'I', 'M', 4, 15, 0, 0}
		resources = binary.BigEndian.AppendUint32(resources, uint32(len(profile)))
		resources = append(resources, profile...)
		if len(profile)%2 != 0 {
			resources = append(resources, 0)
		}
	}
	var points []psdfile.DescriptorValue
	for _, value := range quad {
		points = append(points, psdfile.DescriptorValue{Type: "doub", Value: value})
	}
	descriptor := &psdfile.Descriptor{Version: 16, ClassID: "null", Entries: []psdfile.DescriptorEntry{
		{Key: "Idnt", Type: "TEXT", Value: "recursive-text"}, {Key: "Type", Type: "long", Value: int32(2)},
		{Key: "Sz  ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "size", Entries: []psdfile.DescriptorEntry{
			{Key: "Wdth", Type: "doub", Value: float64(prototype.Header.Width)},
			{Key: "Hght", Type: "doub", Value: float64(prototype.Header.Height)},
		}}}, {Key: "Trnf", Type: "VlLs", Value: points},
	}}
	encoded, err := psdfile.EncodeDescriptor(nil, descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(binary.BigEndian.AppendUint32([]byte("soLD"), 4), encoded...)
	extra := append(make([]byte, 8), 1, 'L', 0, 0)
	extra = append(extra, artboardTestBlock("SoLd", payload)...)
	info := binary.BigEndian.AppendUint16(nil, uint16(65535))
	for _, value := range []uint32{0, 0, 1, 2} {
		info = binary.BigEndian.AppendUint32(info, value)
	}
	info = binary.BigEndian.AppendUint16(info, 4)
	for _, id := range []uint16{0, 1, 2, 65535} {
		info = binary.BigEndian.AppendUint16(info, id)
		info = binary.BigEndian.AppendUint32(info, 4)
	}
	info = append(info, []byte("8BIMnorm")...)
	info = append(info, 255, 0, 0, 0)
	info = binary.BigEndian.AppendUint32(info, uint32(len(extra)))
	info = append(info, extra...)
	info = append(info, 0, 0, 255, 255, 0, 0, 0, 0, 0, 0, 255, 255, 0, 0, 255, 255)
	if len(info)%2 != 0 {
		info = append(info, 0)
	}
	kind := "liFD"
	if external {
		kind = "liFE"
	}
	link := binary.BigEndian.AppendUint32([]byte(kind), 1)
	link = append(link, byte(len("recursive-text")))
	link = append(link, "recursive-text"...)
	link = append(link, cachedTestUnicode("original-text.psd")...)
	link = append(link, "8BPS8BIM"...)
	length := uint64(len(child))
	if external {
		length = 0
	}
	link = binary.BigEndian.AppendUint64(link, length)
	link = append(link, 0)
	if external {
		desc, err := psdfile.EncodeDescriptor(nil, &psdfile.Descriptor{ClassID: "null"}, true)
		if err != nil {
			t.Fatal(err)
		}
		link = append(link, desc...)
		link = binary.BigEndian.AppendUint64(link, uint64(len(child)))
	} else {
		link = append(link, child...)
	}
	entry := binary.BigEndian.AppendUint64(nil, uint64(len(link)))
	entry = append(entry, link...)
	for len(entry)%4 != 0 {
		entry = append(entry, 0)
	}
	section := binary.BigEndian.AppendUint32(nil, uint32(len(info)))
	section = append(section, info...)
	section = append(section, 0, 0, 0, 0)
	section = append(section, artboardTestBlock("lnk2", entry)...)
	result := binary.BigEndian.AppendUint16([]byte("8BPS"), 1)
	result = append(result, make([]byte, 6)...)
	result = binary.BigEndian.AppendUint16(result, 4)
	result = binary.BigEndian.AppendUint32(result, uint32(canvas.Y))
	result = binary.BigEndian.AppendUint32(result, uint32(canvas.X))
	result = binary.BigEndian.AppendUint16(result, 8)
	result = binary.BigEndian.AppendUint16(result, 3)
	result = binary.BigEndian.AppendUint32(result, 0)
	result = binary.BigEndian.AppendUint32(result, uint32(len(resources)))
	result = append(result, resources...)
	result = binary.BigEndian.AppendUint32(result, uint32(len(section)))
	result = append(result, section...)
	result = append(result, 0, 0)
	return append(result, bytes.Repeat([]byte{77}, canvas.X*canvas.Y*4)...)
}

func recursiveTextOriginal(t *testing.T, data []byte) (*psdfile.Document, *openTypeSceneReader) {
	t.Helper()
	source, reader := openTypeSceneSource(t, data, nil)
	linked, err := source.LinkedFiles(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range linked {
		if record.Kind != "liFD" {
			continue
		}
		child, err := source.OpenLinkedDocument(nil, record)
		if err != nil {
			t.Fatal(err)
		}
		tableBytes := int64(0)
		if child.Composite.Compression == 1 {
			tableBytes = int64(child.Header.Height) * int64(child.Header.Channels) * int64(child.Composite.RowCountWidth)
		}
		reader.forbidden = append(reader.forbidden, psdfile.Span{
			Offset: record.Data.Offset + child.Composite.Data.Offset + tableBytes,
			Length: child.Composite.Data.Length - tableBytes,
		})
		for _, layer := range child.Layers {
			for _, channel := range layer.Channels {
				if channel.ID >= -1 {
					tableBytes := int64(0)
					if channel.Compression == 1 {
						width := int64(2)
						if child.Header.Version == 2 {
							width = 4
						}
						tableBytes = int64(channel.Bounds.Dy()) * width
					}
					reader.forbidden = append(reader.forbidden, psdfile.Span{
						Offset: record.Data.Offset + channel.Data.Offset + tableBytes,
						Length: channel.Data.Length - tableBytes,
					})
				}
			}
		}
		_ = child.Close()
	}
	return source, reader
}

func recursiveTextPSB(t *testing.T, data []byte) []byte {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if source.Header.Version != 1 {
		t.Fatal("conversion control requires PSD")
	}
	convert := func(compression uint16, span psdfile.Span, rows int) []byte {
		original := data[span.Offset : span.Offset+span.Length]
		result := binary.BigEndian.AppendUint16(nil, compression)
		if compression != 1 {
			return append(result, original...)
		}
		for row := range rows {
			result = binary.BigEndian.AppendUint32(result, uint32(binary.BigEndian.Uint16(original[row*2:])))
		}
		return append(result, original[rows*2:]...)
	}
	blocks := func(start, end int64, items []psdfile.AdditionalBlock) []byte {
		var result []byte
		position := start
		for _, block := range items {
			result = append(result, data[position:block.Span.Offset]...)
			result = append(result, block.Signature...)
			result = append(result, block.Key...)
			wide := block.Signature == "8B64"
			switch block.Key {
			case "LMsk", "Lr16", "Lr32", "Layr", "Mt16", "Mt32", "Mtrn", "Alph", "FMsk", "lnk2", "FEid", "FXid", "PxSD":
				wide = true
			}
			if wide {
				result = binary.BigEndian.AppendUint64(result, uint64(block.Data.Length))
			} else {
				result = binary.BigEndian.AppendUint32(result, uint32(block.Data.Length))
			}
			result = append(result, data[block.Data.Offset:block.Data.Offset+block.Data.Length]...)
			result = append(result, data[block.Padding.Offset:block.Padding.Offset+block.Padding.Length]...)
			position = max(block.Span.Offset+block.Span.Length, block.Padding.Offset+block.Padding.Length)
		}
		return append(result, data[position:end]...)
	}
	info := bytes.Clone(data[source.LayerInfo.Offset+4 : source.LayerInfo.Offset+6])
	var channels []byte
	for _, record := range source.Layers {
		info = append(info, data[record.Span.Offset:record.Span.Offset+16]...)
		info = binary.BigEndian.AppendUint16(info, uint16(len(record.Channels)))
		for _, channel := range record.Channels {
			encoded := convert(channel.Compression, channel.Data, channel.Bounds.Dy())
			info = binary.BigEndian.AppendUint16(info, uint16(channel.ID))
			info = binary.BigEndian.AppendUint64(info, uint64(len(encoded)))
			channels = append(channels, encoded...)
		}
		info = append(info, data[record.Span.Offset+18+int64(len(record.Channels))*6:record.Extra.Offset-4]...)
		extra := blocks(record.Extra.Offset, record.Extra.Offset+record.Extra.Length, record.AdditionalInfo)
		info = binary.BigEndian.AppendUint32(info, uint32(len(extra)))
		info = append(info, extra...)
	}
	info = append(info, channels...)
	if len(info)%2 != 0 {
		info = append(info, 0)
	}
	section := binary.BigEndian.AppendUint64(nil, uint64(len(info)))
	section = append(section, info...)
	global := blocks(source.GlobalMask.Offset, source.LayerMaskSection.Offset+source.LayerMaskSection.Length,
		source.AdditionalInfo)
	section = append(section, global...)
	result := bytes.Clone(data[:source.LayerMaskSection.Offset])
	binary.BigEndian.PutUint16(result[4:], 2)
	result = binary.BigEndian.AppendUint64(result, uint64(len(section)))
	result = append(result, section...)
	footer := convert(source.Composite.Compression, source.Composite.Data,
		int(source.Header.Height)*int(source.Header.Channels))
	return append(result, footer...)
}

func TestOpenTypeRecursiveOriginalSourceProjection(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native=%t", native), func(t *testing.T) {
			var child []byte
			var face *psdfont.Face
			var err error
			if native {
				directory := os.Getenv("PSDKIT_OPENTYPE_RECURSIVE_NATIVE_DIR")
				if directory == "" {
					t.Skip("explicit owned native source control directory is required")
				}
				child, err = os.ReadFile(filepath.Join(directory, "native-child.psd"))
				if err != nil {
					t.Fatal(err)
				}
				face, err = psdfont.Open(nil, filepath.Join(directory, "Trattatello.ttf"))
			} else {
				face, err = psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
				if err == nil {
					child = openTypeTestPSD(t, "ffi AVfi", face.PostScriptName, 20, true)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			resolver := psdfont.ResolverFunc(func(_ context.Context, name string) (*psdfont.Face, error) {
				if name != face.PostScriptName {
					return nil, psdfont.ErrMissingFont
				}
				return face, nil
			})
			childSource, childReader := openTypeSceneSource(t, child, nil)
			policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone,
				Shape: psdfont.ShapeOptions{Features: []psdfont.ShapeFeature{{Tag: "liga", Enabled: false}}}}
			records, err := childSource.TextLayers(nil)
			if err != nil || len(records) != 1 {
				t.Fatal(err)
			}
			glyphs, err := RenderOpenTypeTextLayer(nil, childSource, records[0], resolver, policy)
			if err != nil {
				t.Fatal(err)
			}
			prototype, err := psdcompose.Render(nil, &psdmodel.Document{
				Width: int(childSource.Header.Width), Height: int(childSource.Header.Height),
				Layers: []*psdmodel.Layer{{Bounds: glyphs.Rect, Pixels: glyphs, BlendMode: "normal"}}})
			if err != nil || childReader.forbiddenHit != 0 {
				t.Fatal(err)
			}
			canvas := image.Pt(300, 180)
			quad := []float64{20, 10, 276, 10, 276, 138, 20, 138}
			for _, version := range []uint16{1, 2} {
				original := child
				if version == 2 {
					original = recursiveTextPSB(t, child)
				}
				for _, external := range []bool{false, true} {
					data := recursiveTextParentFixture(t, original, external, canvas, quad)
					parent, reader := recursiveTextOriginal(t, data)
					var externalReader *openTypeSceneReader
					settings := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: resolver,
						CachedPlacedLayers: true, CachedSpecialLayers: true}
					if external {
						settings.ExternalResolver = func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
							owned, r := openTypeSceneSource(t, original, nil)
							externalReader = r
							return owned, nil
						}
					}
					bounds := image.Rect(280, 160, 299, 179)
					parentPolicy := policy
					parentPolicy.Bounds = &bounds
					settings.ApproximateOpenTypeText = &parentPolicy
					view, err := parent.ReadLayerMetadata(nil, 0)
					if err != nil {
						t.Fatal(err)
					}
					plan, err := psdrender.CompileProjectionRegion(view.Layer,
						image.Rectangle{Max: canvas}, image.Rectangle{Max: canvas})
					if err != nil {
						t.Fatal(err)
					}
					pixels, origin, err := psdrender.ProjectPlacedContentsContext(nil, view.Layer, prototype, plan)
					if err != nil {
						t.Fatal(err)
					}
					expected, err := psdcompose.Render(nil, &psdmodel.Document{Width: canvas.X, Height: canvas.Y,
						Layers: []*psdmodel.Layer{
							{Bounds: image.Rectangle{Min: origin, Max: origin.Add(pixels.Rect.Size())}, Pixels: pixels, BlendMode: "normal"},
						}})
					if err != nil {
						t.Fatal(err)
					}
					actual, err := RenderTile(nil, parent, image.Rectangle{Max: canvas}, settings)
					if err != nil {
						t.Fatalf("native=%t external=%t recursive source: %v", native, external, err)
					}
					openTypeSceneEqualWindow(t, actual, expected, expected.Rect)
					if reader.forbiddenHit != 0 || externalReader != nil && externalReader.forbiddenHit != 0 {
						t.Fatal("recursive source used saved parent/child artwork")
					}
					for _, window := range []image.Rectangle{
						image.Rect(20, 20, 80, 100), image.Rect(80, 45, 155, 120), image.Rect(250, 120, 300, 180),
					} {
						actual, err := RenderTile(nil, parent, window, settings)
						if err != nil {
							t.Fatal(err)
						}
						openTypeSceneEqualWindow(t, actual, expected, window)
					}
					visited := 0
					err = VisitTiles(nil, parent, expected.Rect, image.Pt(100, 60), func(pixels *image.NRGBA) error {
						visited++
						openTypeSceneEqualWindow(t, pixels, expected, pixels.Rect)
						return nil
					}, settings)
					if err != nil || visited != 9 {
						t.Fatalf("recursive visitor: count=%d err=%v", visited, err)
					}
				}
			}
		})
	}
}

func TestOpenTypeRecursiveLimitsCyclesAndLiveCauses(t *testing.T) {
	face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	child := openTypeTestPSD(t, "AVffi", face.PostScriptName, 20, true)
	quad := []float64{20, 10, 276, 10, 276, 138, 20, 138}
	parentBytes := recursiveTextParentFixture(t, child, true, image.Pt(300, 180), quad)
	policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone}
	fontCalls := 0
	font := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { fontCalls++; return face, nil })
	for _, limit := range []string{"documents", "depth"} {
		parent, reader := recursiveTextOriginal(t, parentBytes)
		calls := 0
		settings := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: font, CachedPlacedLayers: true,
			ExternalResolver: func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
				calls++
				return psdfile.Parse(child)
			}}
		if limit == "documents" {
			settings.MaxDocuments = 1
		} else {
			settings.MaxDepth = 1
		}
		pixels, err := RenderTile(nil, parent, image.Rect(20, 20, 100, 100), settings)
		if pixels != nil || !errors.Is(err, psdfile.ErrLimit) || calls != 0 || fontCalls != 0 || reader.forbiddenHit != 0 {
			t.Fatalf("recursive %s limit occurred after callback: external=%d fonts=%d err=%v", limit, calls, fontCalls, err)
		}
	}
	parent, reader := recursiveTextOriginal(t, parentBytes)
	cause := errors.New("explicit recursive external source cause")
	settings := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: font,
		CachedPlacedLayers: true, CachedSpecialLayers: true,
		ExternalResolver: func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) { return nil, cause }}
	if pixels, err := RenderTile(nil, parent, image.Rect(20, 20, 100, 100), settings); pixels != nil ||
		!errors.Is(err, cause) || reader.forbiddenHit != 0 {
		t.Fatal("external live cause lost or fell back")
	}
	settings.ExternalResolver = func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
		return psdfile.Parse(child)
	}
	settings.FontResolver = nil
	if pixels, err := RenderTile(nil, parent, image.Rect(20, 20, 100, 100), settings); pixels != nil ||
		!errors.Is(err, psdfont.ErrMissingFont) || reader.forbiddenHit != 0 {
		t.Fatal("nested missing font lost or fell back")
	}
	settings.FontResolver = font
	settings.ExternalResolver = func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
		document, err := psdfile.Parse(child)
		if err != nil {
			return nil, err
		}
		_ = document.Close()
		return document, nil
	}
	if pixels, err := RenderTile(nil, parent, image.Rect(20, 20, 100, 100), settings); pixels != nil ||
		!errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed child not rejected: %v", err)
	}
	nestedBytes := recursiveTextParentFixture(t, child, true, image.Pt(300, 180), quad)
	grandparentBytes := recursiveTextParentFixture(t, nestedBytes, true, image.Pt(300, 180), quad)
	grandparent, grandReader := recursiveTextOriginal(t, grandparentBytes)
	calls := 0
	settings.ExternalResolver = func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
		calls++
		if calls == 1 {
			return psdfile.Parse(nestedBytes)
		}
		return grandparent, nil
	}
	if pixels, err := RenderTile(nil, grandparent, image.Rect(20, 20, 100, 100), settings); pixels != nil ||
		!errors.Is(err, psdfile.ErrMalformed) || calls != 2 || grandReader.forbiddenHit != 0 {
		t.Fatalf("ancestor cycle not rejected: calls=%d err=%v", calls, err)
	}
	if _, err := grandparent.ReadAt(nil, 0); err != nil {
		t.Fatal("cycle rejection closed ancestor")
	}
	for _, limit := range []int{2, 3} {
		calls = 0
		settings.MaxDocuments = limit
		settings.ExternalResolver = func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
			calls++
			if calls == 1 {
				return psdfile.Parse(nestedBytes)
			}
			return psdfile.Parse(child)
		}
		pixels, err := RenderTile(nil, grandparent, image.Rect(20, 20, 100, 100), settings)
		if limit == 2 {
			if pixels != nil || !errors.Is(err, psdfile.ErrLimit) || calls != 1 {
				t.Fatalf("grandchild cardinality bypassed: %v calls=%d", err, calls)
			}
		} else if pixels == nil || err != nil || calls != 2 {
			t.Fatalf("three-document recursion failed: %v calls=%d", err, calls)
		}
	}
}

func recursiveTextDuplicateParent(t *testing.T, data []byte) []byte {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	record := source.Layers[0]
	info := binary.BigEndian.AppendUint16(nil, uint16(65534))
	for range 2 {
		info = append(info, data[record.Span.Offset:record.Span.Offset+record.Span.Length]...)
	}
	for range 2 {
		for _, channel := range record.Channels {
			info = append(info, data[channel.Span.Offset:channel.Span.Offset+channel.Span.Length]...)
		}
	}
	if len(info)%2 != 0 {
		info = append(info, 0)
	}
	section := binary.BigEndian.AppendUint32(nil, uint32(len(info)))
	section = append(section, info...)
	globals := data[source.GlobalMask.Offset : source.LayerMaskSection.Offset+source.LayerMaskSection.Length]
	section = append(section, globals...)
	result := bytes.Clone(data[:source.LayerMaskSection.Offset])
	result = binary.BigEndian.AppendUint32(result, uint32(len(section)))
	result = append(result, section...)
	return append(result, data[source.Composite.Span.Offset:]...)
}

func TestOpenTypeRecursiveSharedContentsOptionsAndCancellation(t *testing.T) {
	face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	child := openTypeTestPSD(t, "ffi AVfi", face.PostScriptName, 20, true)
	quad := []float64{20, 10, 276, 10, 276, 138, 20, 138}
	for _, external := range []bool{false, true} {
		fixture := recursiveTextDuplicateParent(t, recursiveTextParentFixture(t, child, external, image.Pt(300, 180), quad))
		parent, reader := recursiveTextOriginal(t, fixture)
		calls, fonts := 0, 0
		policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone,
			Shape: psdfont.ShapeOptions{Features: []psdfont.ShapeFeature{{Tag: "liga", Enabled: true}}}}
		font := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { fonts++; return face, nil })
		settings := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: font, MaxDocuments: 2}
		if external {
			settings.ExternalResolver = func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
				calls++
				source, _ := openTypeSceneSource(t, child, nil)
				return source, nil
			}
		}
		baseline, err := RenderTile(nil, parent, image.Rect(0, 0, 300, 180), settings)
		if baseline == nil || err != nil || fonts != 1 || external && calls != 1 {
			t.Fatalf("shared content not memoized: external=%t sources=%d fonts=%d err=%v", external, calls, fonts, err)
		}
		bounds := image.Rect(280, 160, 299, 179)
		policy.Bounds = &bounds
		mutate := func() { policy.Shape.Features[0].Enabled = false; bounds = image.Rect(0, 0, 0, 0) }
		reader.hook = mutate
		settings.FontResolver = psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
			mutate()
			return face, nil
		})
		if external {
			settings.ExternalResolver = func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
				mutate()
				source, _ := openTypeSceneSource(t, child, nil)
				return source, nil
			}
		}
		actual, err := RenderTile(nil, parent, baseline.Rect, settings)
		if actual == nil || err != nil || !bytes.Equal(actual.Pix, baseline.Pix) {
			t.Fatalf("recursive callback changed feature/bounds policy: %v", err)
		}
		reader.hook = nil
		policy.Shape.Features[0].Enabled = true
		policy.Bounds = nil
		settings.FontResolver = font
		if external {
			settings.ExternalResolver = func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
				source, _ := openTypeSceneSource(t, child, nil)
				return source, nil
			}
		}
		counter := &textSourceCountContext{}
		actual, err = RenderTile(counter, parent, baseline.Rect, settings)
		if actual == nil || err != nil {
			t.Fatal(err)
		}
		for _, before := range []int64{0, 1, 2, 50, 1000} {
			pixels, err := RenderTile(&textSourceCountContext{cancelAt: counter.calls.Load() - before},
				parent, baseline.Rect, settings)
			if pixels != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("recursive late cancellation returned partial image at poll-%d: %v", before, err)
			}
		}
		settings.MaxPixels = 300 * 180
		settings.MaxWorkingBytes = 1 << 20
		pixels, err := RenderTile(nil, parent, baseline.Rect, settings)
		if pixels != nil || (!errors.Is(err, psdfile.ErrLimit) && !errors.Is(err, psdfont.ErrLimit)) {
			t.Fatalf("parent retained/child glyph budget was bypassed: %v", err)
		}
		settings.MaxWorkingBytes = 0
		settings.MaxPixels = 1
		pixels, err = RenderTile(nil, parent, image.Rect(60, 60, 61, 61), settings)
		if pixels != nil || !errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("child fullframe pixel ceiling widened: %v", err)
		}
		if reader.forbiddenHit != 0 {
			t.Fatal("shared recursive content read saved parent/child pixels")
		}
		_ = parent.Close()
		pixels, err = RenderTile(nil, parent, image.Rect(60, 60, 61, 61), settings)
		if pixels != nil || !errors.Is(err, psdfile.ErrClosed) {
			t.Fatalf("closed parent not propagated: %v", err)
		}
	}
}

func TestOpenTypeRecursiveTransferredIndexBudgetAndClosure(t *testing.T) {
	face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	child := openTypeTestPSD(t, "A", face.PostScriptName, 20, true)
	source, err := psdfile.Parse(child)
	if err != nil {
		t.Fatal(err)
	}
	resourceData := child[source.ResourceSection.Offset+4 : source.ResourceSection.Offset+source.ResourceSection.Length]
	resources := bytes.Clone(resourceData)
	for range 20000 {
		resources = append(resources, '8', 'B', 'I', 'M', 7, 208, 0, 0, 0, 0, 0, 0)
	}
	large := bytes.Clone(child[:source.ResourceSection.Offset])
	large = binary.BigEndian.AppendUint32(large, uint32(len(resources)))
	large = append(large, resources...)
	large = append(large, child[source.ResourceSection.Offset+source.ResourceSection.Length:]...)
	_ = source.Close()
	quad := []float64{20, 10, 276, 10, 276, 138, 20, 138}
	parent, reader := recursiveTextOriginal(t, recursiveTextParentFixture(t, child, true, image.Pt(300, 180), quad))
	policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone}
	fonts, calls := 0, 0
	var transferred *psdfile.Document
	settings := TileOptions{MaxWorkingBytes: 1 << 20, ApproximateOpenTypeText: &policy,
		FontResolver: psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
			fonts++
			return face, nil
		}),
		ExternalResolver: func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
			calls++
			var err error
			transferred, err = psdfile.Parse(large)
			return transferred, err
		}}
	pixels, err := RenderTile(nil, parent, image.Rect(20, 20, 100, 100), settings)
	if pixels != nil || !errors.Is(err, psdfile.ErrLimit) || calls != 1 || fonts != 0 || reader.forbiddenHit != 0 {
		t.Fatalf("external index charge did not precede child decoding: calls=%d fonts=%d err=%v", calls, fonts, err)
	}
	var detail *psdfile.Error
	if !errors.As(err, &detail) || detail.Path != "tile.text.child.index" {
		t.Fatalf("control missed transferred index limit: %v", err)
	}
	if _, err := transferred.ReadAt(nil, 0); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatal("rejected transferred child remained open")
	}
}
