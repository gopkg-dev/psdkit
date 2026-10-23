package psdscene

import (
	"bytes"
	"context"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestExternalRasterSkipsCachedContentsAndPreservesResolverContracts(t *testing.T) {
	encoded := externalRasterPNG(t, externalRasterInput(), nil)
	data := externalRasterFixture(t, []externalRasterFixtureLink{{id: "external", name: "explicit.png",
		fileType: "PNGf", cached: bytes.Repeat([]byte{0x99}, 64)}}, 0)
	reader := &tilePlacedSparseReader{prefix: data, length: int64(len(data))}
	source, err := psdfile.Read(reader, reader.length)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	links, err := source.LinkedFiles(t.Context())
	if err != nil || len(links) != 1 || !links[0].HasData {
		t.Fatalf("external cached-content fixture is missing: %v", err)
	}
	reader.forbidden = []psdfile.Span{links[0].Data}
	calls, documentCalls := 0, 0
	options := Options{ExternalRasterResolver: func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
		calls++
		return encoded, nil
	}, ExternalResolver: func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
		documentCalls++
		return psdfile.Parse(cachedTestSource(nil))
	}}
	actual, err := Render(t.Context(), source, options)
	if err != nil || calls != 1 || documentCalls != 0 || reader.reads != 0 ||
		!bytes.Equal(actual.Pix, externalRasterInput().Pix) {
		t.Fatalf("explicit raster source touched cache or wrong resolver: %v", err)
	}
	for _, channel := range source.Layers[0].Channels {
		reader.forbidden = append(reader.forbidden, channel.Data)
	}
	reader.forbidden = append(reader.forbidden, source.Composite.Data)
	_, err = RenderTile(t.Context(), source, image.Rect(0, 0, 2, 1),
		TileOptions{ExternalRasterResolver: options.ExternalRasterResolver})
	if err != nil || reader.reads != 0 {
		t.Fatalf("external tile touched cached encoded/layer/footer contents: %v", err)
	}
	_, err = RenderPlacedLayer(t.Context(), source, 0, options)
	if err != nil || reader.reads != 0 {
		t.Fatalf("single placed artwork touched cached contents: %v", err)
	}
	reader.forbidden = []psdfile.Span{links[0].Data}
	cause := errors.New("caller refused external artwork")
	options.ExternalRasterResolver = func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { return nil, cause }
	if actual, err := Render(t.Context(), source, options); actual != nil || !errors.Is(err, cause) || documentCalls != 0 {
		t.Fatalf("failed explicit raster resolution silently fell back: %v", err)
	}
	options.ExternalRasterResolver = nil
	reader.forbidden = nil
	if _, err := Render(t.Context(), source, options); err != nil || documentCalls != 1 {
		t.Fatalf("existing external document resolver compatibility changed: %v", err)
	}
	if actual, err := Render(t.Context(), source); actual != nil || !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("missing resolver used an external cache: %v", err)
	}
	for _, fileType := range []string{"8BPS", "8BPB", "GIFf"} {
		other := cachedOpen(t, externalRasterFixture(t,
			[]externalRasterFixtureLink{{id: "document", name: "original", fileType: fileType}}, 0))
		options.ExternalRasterResolver = func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
			t.Fatal("raster callback intercepted a document or unknown external file type")
			return nil, cause
		}
		if _, err := Render(t.Context(), other, options); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExternalRasterNestedSameIDsStayDocumentLocal(t *testing.T) {
	first := externalRasterFixture(t,
		[]externalRasterFixtureLink{{id: "same", name: "first.png", fileType: "PNGf"}}, 0)
	second := externalRasterFixture(t,
		[]externalRasterFixtureLink{{id: "same", name: "second.png", fileType: "PNGf"}}, 0)
	parent := cachedOpen(t, externalRasterFixture(t, []externalRasterFixtureLink{
		{id: "first-child", name: "first.psd", fileType: "8BPS", contents: first},
		{id: "second-child", name: "second.psd", fileType: "8BPS", contents: second},
	}, 0, 1))
	firstPixels, secondPixels := externalRasterInput(), externalRasterInput()
	secondPixels.Pix[0], secondPixels.Pix[1] = 7, 240
	encoded := map[string][]byte{"first.png": externalRasterPNG(t, firstPixels, nil),
		"second.png": externalRasterPNG(t, secondPixels, nil)}
	for _, openType := range []bool{false, true} {
		calls := map[string]int{}
		resolver := func(_ context.Context, record psdfile.LinkedFileRecord) ([]byte, error) {
			calls[record.Name]++
			return encoded[record.Name], nil
		}
		options := Options{ExternalRasterResolver: resolver}
		tileOptions := TileOptions{ExternalRasterResolver: resolver}
		if openType {
			policy := &OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone}
			options.ApproximateOpenTypeText, tileOptions.ApproximateOpenTypeText = policy, policy
		}
		actual, err := Render(t.Context(), parent, options)
		if err != nil || calls["first.png"] != 1 || calls["second.png"] != 1 ||
			actual.NRGBAAt(0, 0) != firstPixels.NRGBAAt(0, 0) || actual.NRGBAAt(2, 0) != secondPixels.NRGBAAt(0, 0) {
			t.Fatalf("nested external IDs polluted another parent's prototype: OpenType=%t calls=%v err=%v",
				openType, calls, err)
		}
		regional, err := RenderTile(t.Context(), parent, actual.Rect, tileOptions)
		if err != nil || !bytes.Equal(regional.Pix, actual.Pix) {
			t.Fatalf("nested regional external sources differ: %v", err)
		}
	}
}

func TestExternalRasterRejectsAmbiguousMissingAndMismatchedSources(t *testing.T) {
	duplicate := cachedOpen(t, externalRasterFixture(t, []externalRasterFixtureLink{
		{id: "same", name: "first.png", fileType: "PNGf"}, {id: "same", name: "second.png", fileType: "PNGf"},
	}, 0))
	calls := 0
	resolver := func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
		calls++
		return externalRasterPNG(t, externalRasterInput(), nil), nil
	}
	if pixels, err := Render(t.Context(), duplicate, Options{ExternalRasterResolver: resolver}); pixels != nil || !errors.Is(err, psdfile.ErrUnsupported) || calls != 0 {
		t.Fatalf("ambiguous external record reached resolver: %v", err)
	}
	if pixels, err := RenderTile(t.Context(), duplicate, image.Rect(0, 0, 2, 1),
		TileOptions{ExternalRasterResolver: resolver}); pixels != nil || !errors.Is(err, psdfile.ErrUnsupported) || calls != 0 {
		t.Fatalf("ambiguous regional record reached resolver: %v", err)
	}
	source := cachedOpen(t, externalRasterFixture(t,
		[]externalRasterFixtureLink{{id: "different", name: "different.tif", fileType: "TIFF"}}, 0))
	if pixels, err := Render(t.Context(), source, Options{ExternalRasterResolver: resolver}); pixels != nil || !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("resolved encoding mismatch was accepted: %v", err)
	}
}

func TestExternalRasterUnusedResolverPreservesExistingFullRenderPath(t *testing.T) {
	encoded := externalRasterPNG(t, externalRasterInput(), nil)
	hiddenExternal := externalRasterFixture(t, []externalRasterFixtureLink{
		{id: "hidden", name: "hidden.png", fileType: "PNGf"},
		{id: "embedded", name: "child.png", fileType: "PNGf", contents: encoded},
	}, 0, 1)
	hiddenSource := cachedOpen(t, hiddenExternal)
	hiddenExternal[hiddenSource.Layers[0].Extra.Offset-6] |= 2
	for name, data := range map[string][]byte{
		"no-links":        cachedTestSource(nil),
		"hidden-external": hiddenExternal,
		"embedded-document": externalRasterFixture(t, []externalRasterFixtureLink{{
			id: "embedded", name: "child.psd", fileType: "8BPS", contents: cachedTestSource(nil),
		}}, 0),
		"embedded-raster": externalRasterFixture(t, []externalRasterFixtureLink{{
			id: "embedded", name: "child.png", fileType: "PNGf", contents: encoded,
		}}, 0),
		"unused-external": externalRasterFixture(t, []externalRasterFixtureLink{
			{id: "unused", name: "unused.png", fileType: "PNGf"},
			{id: "embedded", name: "child.png", fileType: "PNGf", contents: encoded},
		}, 1),
	} {
		t.Run(name, func(t *testing.T) {
			source := cachedOpen(t, data)
			for _, budget := range []int64{1, 256, 4096, 64 << 10, 4 << 20} {
				options := Options{MaxWorkingBytes: budget, ApproximateText: &TextRenderOptions{}}
				baseline, baselineErr := Render(t.Context(), source, options)
				options.ExternalRasterResolver = func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
					t.Fatal("unused raster resolver was invoked")
					return nil, nil
				}
				actual, err := Render(t.Context(), source, options)
				if baselineErr != nil {
					if actual != nil || !errors.Is(err, psdfile.ErrLimit) || !errors.Is(baselineErr, psdfile.ErrLimit) {
						t.Fatalf("unused callback changed limit behavior at %d bytes: baseline=%v actual=%v",
							budget, baselineErr, err)
					}
					continue
				}
				if err != nil || !bytes.Equal(actual.Pix, baseline.Pix) {
					t.Fatalf("unused callback changed full rendering or legacy text policy: %v", err)
				}
			}
		})
	}
}

func TestExternalRasterOpenTypePrototypeHonorsPixelBudget(t *testing.T) {
	encoded := externalRasterPNG(t, externalRasterInput(), nil)
	for _, external := range []bool{false, true} {
		link := externalRasterFixtureLink{id: "prototype", name: "prototype.png", fileType: "PNGf"}
		if !external {
			link.contents = encoded
		}
		source := cachedOpen(t, externalRasterFixture(t, []externalRasterFixtureLink{link}, 0))
		options := TileOptions{MaxPixels: 1,
			ExternalRasterResolver: func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
				return encoded, nil
			}}
		region := image.Rect(0, 0, 1, 1)
		if _, err := RenderTile(t.Context(), source, region, options); err != nil {
			t.Fatalf("ordinary tile pixel budget unexpectedly bounded a complete prototype: %v", err)
		}
		options.ApproximateOpenTypeText = &OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone}
		if actual, err := RenderTile(t.Context(), source, region, options); actual != nil || !errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("OpenType pixel budget did not bound the complete raster prototype: external=%t error=%v",
				external, err)
		}
		options.MaxPixels = 2
		if _, err := RenderTile(t.Context(), source, region, options); err != nil {
			t.Fatalf("exact complete prototype pixel budget failed: %v", err)
		}
	}
}

func TestExternalRasterLimitsCancellationAndBorrowedInputPeak(t *testing.T) {
	encoded := externalRasterPNG(t, externalRasterInput(), nil)
	decoded, err := psdfile.DecodeRasterContents(t.Context(), encoded)
	if err != nil {
		t.Fatal(err)
	}
	source := cachedOpen(t, cachedTestSource(nil))
	record := psdfile.LinkedFileRecord{Kind: "liFE", FileType: "PNGf"}
	calls := 0
	resolver := func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { calls++; return encoded, nil }
	budget := int64(len(encoded)) + decoded.WorkingBytes
	options := Options{MaxWorkingBytes: budget, MaxDepth: 2, MaxDocuments: 2, ExternalRasterResolver: resolver}
	engine := renderer{options: options, documents: 1}
	if result, err := engine.renderExternalRaster(t.Context(), source, record, &sceneProfile{}, 2); err != nil || !bytes.Equal(result.Pix, decoded.Pixels.Pix) {
		t.Fatalf("exact encoded plus decoder peak budget was rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*Options)
		calls  int
	}{
		{"encoded-peak", func(options *Options) { options.MaxWorkingBytes-- }, 1},
		{"no-working", func(options *Options) { options.MaxWorkingBytes = 0 }, 0},
		{"depth", func(options *Options) { options.MaxDepth = 1 }, 0},
		{"documents", func(options *Options) { options.MaxDocuments = 1 }, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := options
			test.change(&settings)
			engine := renderer{options: settings, documents: 1}
			calls = 0
			if result, err := engine.renderExternalRaster(t.Context(), source, record, &sceneProfile{}, 2); result != nil || !errors.Is(err, psdfile.ErrLimit) || calls != test.calls {
				t.Fatalf("limit check did not bound resolver or decoder: calls=%d error=%v", calls, err)
			}
		})
	}
	for _, limits := range []psdfile.Limits{{MaxDecodePixels: 1}, {MaxDecodeBytes: budget - 1}} {
		limited, err := psdfile.Parse(cachedTestSource(nil), psdfile.LoadOptions{Limits: limits})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = limited.Close() })
		engine := renderer{options: options, documents: 1}
		if result, err := engine.renderExternalRaster(t.Context(), limited, record, &sceneProfile{}, 2); result != nil || !errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("external callback bypassed parent decode limits: %v", err)
		}
	}
	parentBytes := cachedTestSource(nil)
	fileLimited, err := psdfile.Parse(parentBytes,
		psdfile.LoadOptions{Limits: psdfile.Limits{MaxFileBytes: int64(len(parentBytes))}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fileLimited.Close() })
	fileOptions := options
	fileOptions.ExternalRasterResolver = func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
		return make([]byte, len(parentBytes)+1), nil
	}
	engine = renderer{options: fileOptions, documents: 1}
	if result, err := engine.renderExternalRaster(t.Context(), fileLimited, record, &sceneProfile{}, 2); result != nil || !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("external callback bypassed the parent encoded file limit: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls = 0
	engine = renderer{options: options, documents: 1}
	if result, err := engine.renderExternalRaster(ctx, source, record, &sceneProfile{}, 2); result != nil || !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("pre-canceled external render reached callback: %v", err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	options.ExternalRasterResolver = func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
		cancel()
		return encoded, nil
	}
	engine = renderer{options: options, documents: 1}
	if result, err := engine.renderExternalRaster(ctx, source, record, &sceneProfile{}, 2); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("callback cancellation was ignored: %v", err)
	}
	options.ExternalRasterResolver = func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
		_ = source.Close()
		return encoded, nil
	}
	engine = renderer{options: options, documents: 1}
	if result, err := engine.renderExternalRaster(t.Context(), source, record, &sceneProfile{}, 2); result != nil || !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("callback closed its source but rendering continued: %v", err)
	}
}
