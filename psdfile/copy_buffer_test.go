package psdfile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

type bufferTestWriter struct {
	target io.Writer
	cancel context.CancelFunc
	short  bool
}

func (w *bufferTestWriter) Write(data []byte) (int, error) {
	if w.short {
		return len(data) - 1, nil
	}
	n, err := w.target.Write(data)
	if w.cancel != nil {
		w.cancel()
	}
	return n, err
}

type bufferTestReadError struct {
	data  []byte
	cause error
}

func (r bufferTestReadError) ReadAt(data []byte, offset int64) (int, error) {
	n, _ := bytes.NewReader(r.data).ReadAt(data, offset)
	return n, r.cause
}

type bufferTestFrom struct {
	bytes.Buffer
	calls int
}

func (w *bufferTestFrom) ReadFrom(reader io.Reader) (int64, error) {
	w.calls++
	return w.Buffer.ReadFrom(reader)
}

func bufferTestDocument(data []byte, reader io.ReaderAt) *Document {
	return &Document{
		reader: checkedReaderAt{reader: reader},
		size:   int64(len(data)),
		Source: Span{Length: int64(len(data))},
	}
}

func TestCopySpanBufferOwnedPrefixAndReaderFromDispatch(t *testing.T) {
	raw := bytes.Repeat([]byte("owned buffer data"), 10000)
	d := bufferTestDocument(raw, bytes.NewReader(raw))
	for _, size := range []int{1, 7, 64 << 10, 128 << 10} {
		scratch := bytes.Repeat([]byte{0x9d}, size)
		var output bytes.Buffer
		writer := &bufferTestWriter{target: &output}
		n, err := d.CopySpanBuffer(nil, writer, d.Source, scratch)
		if err != nil || n != int64(len(raw)) || !bytes.Equal(output.Bytes(), raw) {
			t.Fatal(size, n, err)
		}
		if size > 64<<10 && !bytes.Equal(scratch[64<<10:], bytes.Repeat([]byte{0x9d}, size-(64<<10))) {
			t.Fatal("borrowed suffix was modified")
		}
	}
	for _, useBuffer := range []bool{false, true} {
		writer := &bufferTestFrom{}
		var n int64
		var err error
		if useBuffer {
			n, err = d.CopySpanBuffer(nil, writer, d.Source, make([]byte, 7))
		} else {
			n, err = d.CopySpan(nil, writer, d.Source)
		}
		if err != nil || n != int64(len(raw)) || writer.calls != 1 || !bytes.Equal(writer.Bytes(), raw) {
			t.Fatal("ReaderFrom dispatch changed", useBuffer, n, err, writer.calls)
		}
	}
}

func TestCopySpanBufferErrorsCancellationAndClosedPriority(t *testing.T) {
	raw := []byte("causal stream")
	d := bufferTestDocument(raw, bytes.NewReader(raw))
	if n, err := d.CopySpanBuffer(nil, io.Discard, d.Source, []byte{}); n != 0 || !errors.Is(err, ErrMalformed) {
		t.Fatal("empty scratch did not return typed failure", n, err)
	}
	if n, err := d.CopySpanBuffer(nil, io.Discard, Span{}, nil); n != 0 || err != nil {
		t.Fatal("empty span", n, err)
	}
	if n, err := d.CopySpanBuffer(
		nil, &bufferTestWriter{short: true}, d.Source, make([]byte, 7),
	); n != 6 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short writer", n, err)
	}
	cause := errors.New("full reader causal error")
	faulty := bufferTestDocument(raw, bufferTestReadError{data: raw, cause: cause})
	var target bytes.Buffer
	if _, err := faulty.CopySpanBuffer(
		nil, &bufferTestWriter{target: &target}, faulty.Source, make([]byte, len(raw)),
	); !errors.Is(err, cause) || !errors.Is(err, ErrMalformed) {
		t.Fatal("reader cause lost", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if n, err := d.CopySpanBuffer(
		ctx, io.Discard, d.Source, make([]byte, 7),
	); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatal("precancel", n, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	target.Reset()
	n, err := d.CopySpanBuffer(ctx, &bufferTestWriter{target: &target, cancel: cancel}, d.Source, make([]byte, len(raw)))
	if n != int64(len(raw)) || !errors.Is(err, context.Canceled) {
		t.Fatal("final write cancel", n, err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CopySpanBuffer(ctx, nil, Span{Offset: -1}, []byte{}); !errors.Is(err, ErrClosed) {
		t.Fatal("closed precedence changed", err)
	}
}
