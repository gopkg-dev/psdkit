package psdfile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
)

func TestDecodeAdjustmentAdapterOwnedDataErrorsAndClose(t *testing.T) {
	data := indexTestDocument(1, 8, 3, nil, indexTestGlobal(1, nil,
		indexTestTag(1, "8BIM", "thrs", []byte{0, 128, 0, 0}, 4)), []byte{0, 0, 0})
	reader := &linkedTestReader{reader: bytes.NewReader(data)}
	document, err := Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = document.Close() })
	block := document.AdditionalInfo[0]
	adjustment, err := document.DecodeAdjustment(nil, block)
	if err != nil || adjustment.Kind != psdadjust.Threshold || adjustment.Threshold != 128 ||
		adjustment.Span.Offset != block.Data.Offset {
		t.Fatalf("adapter returned incorrect typed fields: %v", err)
	}
	clear(adjustment.Raw)
	if again, err := document.DecodeAdjustment(nil, block); err != nil || again.Raw[1] != 128 {
		t.Fatalf("adapter output mutated the source: %v", err)
	}
	reader.fullEOF = true
	if _, err := document.DecodeAdjustment(nil, block); err != nil {
		t.Fatalf("complete ReaderAt with EOF rejected: %v", err)
	}
	reader.fullEOF = false
	wantIO := errors.New("fixture payload read failure")
	reader.failure = wantIO
	if _, err := document.DecodeAdjustment(nil, block); !errors.Is(err, ErrMalformed) || !errors.Is(err, wantIO) {
		t.Fatalf("payload read failure lost its cause: %v", err)
	}
	reader.failure, reader.zero = nil, true
	if _, err := document.DecodeAdjustment(nil, block); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("non-progressing ReaderAt accepted: %v", err)
	}
	reader.zero = false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader.reads = 0
	if _, err := document.DecodeAdjustment(ctx, block); !errors.Is(err, context.Canceled) || reader.reads != 0 {
		t.Fatalf("pre-canceled adapter performed I/O: %v", err)
	}
	if err := document.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := document.DecodeAdjustment(nil, block); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed source adjustment: %v", err)
	}
}

func TestDecodeAdjustmentAdapterMapsUnsupportedMalformedAndLimits(t *testing.T) {
	for _, test := range []struct {
		key  string
		data []byte
		kind error
	}{
		{"CgEd", []byte{0, 0, 0, 16}, ErrUnsupported},
		{"expA", []byte{0, 1, 0}, ErrMalformed},
	} {
		data := indexTestDocument(2, 8, 3, nil, indexTestGlobal(2, nil,
			indexTestTag(2, "8BIM", test.key, test.data, 4)), []byte{0, 0, 0})
		document, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = document.Close() })
		if _, err := document.DecodeAdjustment(nil, document.AdditionalInfo[0]); !errors.Is(err, test.kind) {
			t.Fatalf("adapter error category changed: %v", err)
		}
		document.limits.MaxReadBytes = 1
		if _, err := document.DecodeAdjustment(nil, document.AdditionalInfo[0]); !errors.Is(err, ErrLimit) {
			t.Fatalf("adapter metadata/read limit ignored: %v", err)
		}
	}
}
