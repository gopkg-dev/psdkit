package psdfile_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

type metadataGuard struct {
	data      []byte
	forbidden []psdfile.Span
	cause     error
}

func (reader *metadataGuard) ReadAt(data []byte, offset int64) (int, error) {
	for _, span := range reader.forbidden {
		if offset < span.Offset+span.Length && offset+int64(len(data)) > span.Offset {
			return 0, errors.New("metadata-only operation attempted a channel or footer read")
		}
	}
	count, err := bytes.NewReader(reader.data).ReadAt(data, offset)
	if reader.cause != nil && count > 0 {
		return count, reader.cause
	}
	return count, err
}

func TestLayerMetadataOwnsSemanticsWithoutReadingPixels(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		fixture := acceptanceDocument(version, 8, 0, 2, "")
		reader := &metadataGuard{data: fixture.data}
		source, err := psdfile.Read(reader, int64(len(fixture.data)))
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range source.Layers {
			for _, channel := range record.Channels {
				reader.forbidden = append(reader.forbidden, channel.Span)
			}
		}
		reader.forbidden = append(reader.forbidden, source.Composite.Span)
		view, err := source.ReadLayerMetadata(nil, 0)
		if err != nil || view.Layer == nil || view.Layer.Pixels != nil ||
			view.Layer.Name != source.Layers[0].Name || view.Layer.Bounds != source.Layers[0].Bounds ||
			view.MetadataBytes <= 0 || view.WorkingBytes < view.MetadataBytes+64<<10 {
			t.Fatalf("version%d metadata-only view failed: %+v / %v", version, view, err)
		}
		if _, err := source.ReadLayerMetadata(nil, 0,
			psdfile.MetadataOptions{MaxBytes: view.WorkingBytes}); err != nil {
			t.Fatalf("exact reservation rejected: %v", err)
		}
		if _, err := source.ReadLayerMetadata(nil, 0,
			psdfile.MetadataOptions{MaxBytes: view.WorkingBytes - 1}); !errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("one-byte deficit ignored: %v", err)
		}
		name := view.Layer.Name
		view.Layer.Name = "caller-owned edited metadata"
		again, err := source.ReadLayerMetadata(nil, 0)
		if err != nil || again.Layer.Name != name {
			t.Fatalf("view aliases original metadata: %v", err)
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
		if view.Layer.Name != "caller-owned edited metadata" {
			t.Fatal("Close invalidated owned semantics")
		}
		if _, err := source.ReadLayerMetadata(nil, 0); !errors.Is(err, psdfile.ErrClosed) {
			t.Fatalf("closed source accepted: %v", err)
		}
	}
}

func TestLayerMetadataPreservesSourceCauseCancellationAndIndexChecks(t *testing.T) {
	fixture := acceptanceDocument(1, 8, 0, 1, "")
	reader := &metadataGuard{data: fixture.data}
	source, err := psdfile.Read(reader, int64(len(fixture.data)))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.ReadLayerMetadata(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if _, err := source.ReadLayerMetadata(nil, len(source.Layers)); !errors.Is(err, psdfile.ErrMalformed) {
		t.Fatalf("invalid index accepted: %v", err)
	}
	reader.cause = errors.New("transport failure accompanying complete metadata bytes")
	if _, err := source.ReadLayerMetadata(nil, 0); !errors.Is(err, reader.cause) {
		t.Fatalf("full read cause lost: %v", err)
	}
	reader.cause = io.EOF
	if _, err := source.ReadLayerMetadata(nil, 0); err != nil {
		t.Fatalf("complete EOF metadata read rejected: %v", err)
	}
}
