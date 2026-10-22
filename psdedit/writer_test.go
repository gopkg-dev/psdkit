package psdedit_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/gopkg-dev/psdkit/internal/testfixture"
	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestWriterRealNoEditByteExact(t *testing.T) {
	data, err := os.ReadFile(testfixture.Sample(t))
	if err != nil {
		t.Fatal(err)
	}
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	editor, err := psdedit.New(source)
	if err != nil {
		t.Fatal(err)
	}
	defer editor.Close()
	var output bytes.Buffer
	if err := editor.WritePSD(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), data) {
		t.Fatal("no-edit write changed real PSD bytes")
	}
	output.Reset()
	if err := editor.Write(context.Background(), &output,
		psdedit.SaveOptions{MaxOutputBytes: int64(len(data) - 1)}); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("small output limit = %v", err)
	}
	if output.Len() != 0 {
		t.Fatal("output limit did not fail before writing")
	}
}

func TestWriterRenamePreservesUnknownAndPixels(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(map[uint16]string{1: "PSD", 2: "PSB"}[version], func(t *testing.T) {
			data := editAcceptanceDocument(editAcceptanceFixtureOptions{version: version, advanced: true})
			source, err := psdfile.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			editor, err := psdedit.New(source)
			if err != nil {
				t.Fatal(err)
			}
			defer editor.Close()
			layers, err := editor.Layers()
			if err != nil {
				t.Fatal(err)
			}
			name := "名称改变后仍然保留全部原始内容 😀"
			if err := editor.Rename(layers[0].ID, name); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := editor.Write(context.Background(), &output); err != nil {
				t.Fatal(err)
			}
			reread, err := psdfile.Parse(output.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			defer reread.Close()
			if reread.Layers[0].Name != name {
				t.Fatalf("name = %q", reread.Layers[0].Name)
			}
			for _, block := range reread.Layers[0].AdditionalInfo {
				if block.Key != "luni" {
					continue
				}
				encodedName, err := reread.ReadSpan(block.Data)
				if err != nil {
					t.Fatal(err)
				}
				units := utf16.Encode([]rune(name))
				if binary.BigEndian.Uint32(encodedName) != uint32(len(units)) ||
					len(encodedName) != 4+2*len(units) {
					t.Fatalf("luni contains an extra terminal NUL or incorrect count: %x", encodedName)
				}
				for index, unit := range units {
					if binary.BigEndian.Uint16(encodedName[4+index*2:]) != unit {
						t.Fatalf("luni unit %d changed", index)
					}
				}
			}
			for index, original := range source.Resources {
				assertWriterSpanEqual(t, source, original.Span, reread, reread.Resources[index].Span)
			}
			for index, original := range source.AdditionalInfo {
				assertWriterSpanEqual(t, source, original.Span, reread, reread.AdditionalInfo[index].Span)
			}
			for index, original := range source.Layers[0].Channels {
				assertWriterSpanEqual(t, source, original.Span, reread, reread.Layers[0].Channels[index].Span)
			}
			for index, original := range source.Layers[0].AdditionalInfo {
				if original.Key != "luni" {
					assertWriterSpanEqual(t, source, original.Span, reread, reread.Layers[0].AdditionalInfo[index].Span)
				}
			}
			assertWriterSpanEqual(t, source, source.Composite.Span, reread, reread.Composite.Span)
			changes, err := editor.Changes()
			if err != nil || changes.Dirty || changes.Revision != 1 || changes.SavedRevision != 1 {
				t.Fatalf("successful write changes = %+v, %v", changes, err)
			}
			// A saved overlay still differs from its immutable original source.
			var repeated bytes.Buffer
			if err := editor.Write(context.Background(), &repeated); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(output.Bytes(), repeated.Bytes()) {
				t.Fatal("repeated save reverted the saved overlay")
			}
		})
	}
}

func TestWriterAtomicSaveFailuresAndClosed(t *testing.T) {
	data := editAcceptanceDocument(editAcceptanceFixtureOptions{})
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	editor, err := psdedit.New(source)
	if err != nil {
		t.Fatal(err)
	}
	defer editor.Close()
	layers, _ := editor.Layers()
	if err := editor.Rename(layers[0].ID, "changed"); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "document.psd")
	original := []byte("existing destination bytes")
	if err := os.WriteFile(destination, original, 0600); err != nil {
		t.Fatal(err)
	}
	err = editor.Save(context.Background(), destination, psdedit.SaveOptions{MaxOutputBytes: 10})
	if !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("small output limit = %v", err)
	}
	assertWriterFile(t, destination, original)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.Save(ctx, destination); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel save = %v", err)
	}
	assertWriterFile(t, destination, original)
	if err := editor.Write(context.Background(), writerFail{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("failing sink = %v", err)
	}
	if err := editor.Write(context.Background(), writerShort{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short sink = %v", err)
	}
	changes, err := editor.Changes()
	if err != nil || !changes.Dirty || changes.SavedRevision != 0 {
		t.Fatalf("failed writes marked saved: %+v, %v", changes, err)
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(destination), ".psdkit-*.tmp"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary files remain: %v, %v", files, err)
	}
	if err := editor.SavePSD(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("destination mode = %v, %v", info, err)
	}
	if err := editor.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := editor.Write(context.Background(), &output); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed write = %v", err)
	}
	if output.Len() != 0 {
		t.Fatal("closed editor wrote output")
	}
}

func TestWriterAtomicRenameFailureDoesNotMarkSaved(t *testing.T) {
	data := editAcceptanceDocument(editAcceptanceFixtureOptions{})
	reader := &writerCallbackReader{reader: bytes.NewReader(data)}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	editor, err := psdedit.New(source)
	if err != nil {
		t.Fatal(err)
	}
	defer editor.Close()
	layers, _ := editor.Layers()
	if err := editor.Rename(layers[0].ID, "renamed"); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "destination.psd")
	// Introduce an incompatible destination after Save's initial path check.
	reader.callback = func() error { return os.Mkdir(destination, 0700) }
	if err := editor.Save(context.Background(), destination); err == nil {
		t.Fatal("rename over a newly created directory unexpectedly succeeded")
	}
	changes, err := editor.Changes()
	if err != nil || !changes.Dirty || changes.SavedRevision != 0 {
		t.Fatalf("rename failure marked saved: %+v, %v", changes, err)
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(destination), ".psdkit-*.tmp"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary files remain: %v, %v", files, err)
	}
}

func TestWriterSameSourcePathKeepsOriginalForLaterEdits(t *testing.T) {
	directory := t.TempDir()
	filename := filepath.Join(directory, "original.psd")
	data := editAcceptanceDocument(editAcceptanceFixtureOptions{})
	if err := os.WriteFile(filename, data, 0644); err != nil {
		t.Fatal(err)
	}
	editor, err := psdedit.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer editor.Close()
	layers, _ := editor.Layers()
	if err := editor.Rename(layers[0].ID, strings.Repeat("longer name", 40)); err != nil {
		t.Fatal(err)
	}
	if err := editor.Save(context.Background(), filename); err != nil {
		t.Fatal(err)
	}
	if err := editor.Rename(layers[0].ID, "second revision"); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(directory, "second.psd")
	if err := editor.Save(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	reread, err := psdfile.Open(second)
	if err != nil {
		t.Fatal(err)
	}
	defer reread.Close()
	if reread.Layers[0].Name != "second revision" {
		t.Fatalf("subsequent save used an invalidated source: %q", reread.Layers[0].Name)
	}
	pixels, err := reread.DecodeLayer(context.Background(), 0)
	if err != nil || !bytes.Equal(pixels.Channels[0].Data, []byte{40, 40, 40, 40}) {
		t.Fatalf("original payload was invalidated: %+v, %v", pixels, err)
	}
}

func TestWriterLateSourceEOFLeavesDestinationUnchanged(t *testing.T) {
	data := editAcceptanceDocument(editAcceptanceFixtureOptions{})
	reader := &writerCallbackReader{reader: bytes.NewReader(data)}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	editor, err := psdedit.New(source)
	if err != nil {
		t.Fatal(err)
	}
	defer editor.Close()
	layers, _ := editor.Layers()
	if err := editor.Rename(layers[0].ID, "changed"); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "existing.psd")
	previous := []byte("keep existing bytes")
	if err := os.WriteFile(destination, previous, 0644); err != nil {
		t.Fatal(err)
	}
	reader.reader = bytes.NewReader(data[:len(data)-8])
	if err := editor.Save(context.Background(), destination); !errors.Is(err, psdfile.ErrMalformed) {
		t.Fatalf("late source EOF = %v", err)
	}
	assertWriterFile(t, destination, previous)
	changes, err := editor.Changes()
	if err != nil || !changes.Dirty || changes.SavedRevision != 0 {
		t.Fatalf("source EOF marked saved: %+v, %v", changes, err)
	}
}

func TestWriterBatchRenameMetadataBudgetFailsBeforeOutput(t *testing.T) {
	data := editAcceptanceDocument(editAcceptanceFixtureOptions{count: 5})
	source, err := psdfile.Parse(data, psdfile.LoadOptions{
		Limits: psdfile.Limits{MaxMetadataBytes: 20_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	editor, err := psdedit.New(source)
	if err != nil {
		t.Fatal(err)
	}
	defer editor.Close()
	layers, _ := editor.Layers()
	for _, layer := range layers {
		if err := editor.Rename(layer.ID, strings.Repeat("A", 4500)); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := editor.Write(context.Background(), &output); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("metadata budget = %v", err)
	}
	if output.Len() != 0 {
		t.Fatal("metadata budget emitted partial output")
	}
}

func TestWriter32BitZIPUsesPhotoshopPredictionPolicy(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(map[uint16]string{1: "PSD", 2: "PSB"}[version], func(t *testing.T) {
			data := editAcceptanceDocument(editAcceptanceFixtureOptions{version: version, depth: 32})
			source, err := psdfile.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			editor, err := psdedit.New(source)
			if err != nil {
				t.Fatal(err)
			}
			defer editor.Close()
			var untouched bytes.Buffer
			if err := editor.Write(context.Background(), &untouched, psdedit.SaveOptions{Compression: 2}); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(untouched.Bytes(), data) {
				t.Fatal("ZIP policy normalized unmodified source channels")
			}
			layers, err := editor.Layers()
			if err != nil {
				t.Fatal(err)
			}
			plane, err := source.DecodeChannel(context.Background(), 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			// Include ordinary floats, a negative value and subnormal bits without numeric conversion.
			plane.Data = []byte{
				0x3f, 0x20, 0, 0, 0xbe, 0x80, 0, 0,
				0x3f, 0x70, 0, 0, 0, 0, 0, 1,
			}
			if err := editor.ReplaceChannel(layers[0].ID, plane.ID, *plane); err != nil {
				t.Fatal(err)
			}
			composite, err := source.DecodeComposite(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			composite.Channels[0].Data = bytes.Clone(plane.Data)
			var output bytes.Buffer
			if err := editor.Write(context.Background(), &output,
				psdedit.SaveOptions{Compression: 2, Composite: composite}); err != nil {
				t.Fatal(err)
			}
			saved, err := psdfile.Parse(output.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			defer saved.Close()
			if saved.Layers[0].Channels[0].Compression != 3 {
				t.Fatalf("changed 32-bit ZIP channel word = %d", saved.Layers[0].Channels[0].Compression)
			}
			decoded, err := saved.DecodeChannel(context.Background(), 0, 0)
			if err != nil || !bytes.Equal(decoded.Data, plane.Data) {
				t.Fatalf("32-bit source float bits changed: %+v, %v", decoded, err)
			}
			for index := 1; index < len(source.Layers[0].Channels); index++ {
				assertWriterSpanEqual(t, source, source.Layers[0].Channels[index].Span,
					saved, saved.Layers[0].Channels[index].Span)
			}
		})
	}
}

func TestWriterChangedGlobalLayerSetAlwaysPadsToFourBytes(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(map[uint16]string{1: "PSD", 2: "PSB"}[version], func(t *testing.T) {
			data := editAcceptanceDocument(editAcceptanceFixtureOptions{
				version: version, depth: 32, alternate: "Lr32",
			})
			source, err := psdfile.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			editor, err := psdedit.New(source)
			if err != nil {
				t.Fatal(err)
			}
			defer editor.Close()
			layers, err := editor.Layers()
			if err != nil {
				t.Fatal(err)
			}
			var original psdfile.AdditionalBlock
			for _, block := range source.AdditionalInfo {
				if block.Key == "Lr32" {
					original = block
				}
			}
			if original.Key != "Lr32" || original.Data.Length%4 != 0 || original.Padding.Length != 0 {
				t.Fatalf("fixture must begin with a global tag needing no padding: %+v", original)
			}
			if err := editor.Rename(layers[0].ID, "A"); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := editor.Write(context.Background(), &output); err != nil {
				t.Fatal(err)
			}
			saved, err := psdfile.Parse(output.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			defer saved.Close()
			found := false
			for _, block := range saved.AdditionalInfo {
				if block.Key != "Lr32" {
					continue
				}
				found = true
				if block.Data.Length%4 != 2 || block.Padding.Length != 2 ||
					(block.Data.Length+block.Padding.Length)%4 != 0 {
					t.Fatalf("changed global tag did not gain four-byte padding: %+v", block)
				}
				padding, err := saved.ReadSpan(block.Padding)
				if err != nil || !bytes.Equal(padding, []byte{0, 0}) {
					t.Fatalf("new global padding = %x, %v", padding, err)
				}
			}
			if !found {
				t.Fatal("changed global Lr32 block disappeared")
			}
		})
	}
}

func assertWriterSpanEqual(
	t *testing.T, source *psdfile.Document, original psdfile.Span, saved *psdfile.Document, actual psdfile.Span,
) {
	t.Helper()
	var before, after bytes.Buffer
	if _, err := source.CopySpan(context.Background(), &before, original); err != nil {
		t.Fatal(err)
	}
	if _, err := saved.CopySpan(context.Background(), &after, actual); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before.Bytes(), after.Bytes()) {
		t.Fatalf("preserved span changed at source offset %d", original.Offset)
	}
}

func assertWriterFile(t *testing.T, filename string, expected []byte) {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil || !bytes.Equal(data, expected) {
		t.Fatalf("destination changed: %q, %v", data, err)
	}
}

type writerFail struct{}

func (writerFail) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type writerShort struct{}

func (writerShort) Write(data []byte) (int, error) { return len(data) - 1, nil }

type writerCallbackReader struct {
	reader   *bytes.Reader
	callback func() error
}

func (r *writerCallbackReader) ReadAt(data []byte, offset int64) (int, error) {
	callback := r.callback
	r.callback = nil
	if callback != nil {
		if err := callback(); err != nil {
			return 0, err
		}
	}
	return r.reader.ReadAt(data, offset)
}
