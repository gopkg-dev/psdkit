package psdtext

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unsafe"
)

type documentCheckpointContext struct {
	context.Context
	calls    int
	cancelAt int
	cancel   context.CancelCauseFunc
	cause    error
}

func (ctx *documentCheckpointContext) Err() error {
	ctx.calls++
	if ctx.cancel != nil && ctx.calls == ctx.cancelAt {
		ctx.cancel(ctx.cause)
	}
	return ctx.Context.Err()
}

func TestDecodeDocumentImplicitEveryCancellationCheckpoint(t *testing.T) {
	for _, prefix := range []string{"", "  \n\t", "%" + strings.Repeat("x", 2051) + "\n  "} {
		data := []byte(prefix + "/A 1 /B << /C [true false (raw)] >>")
		probe := &documentCheckpointContext{Context: context.Background()}
		engine, err := DecodeDocument(probe, data, DecodeOptions{SourceOffset: 1234})
		if err != nil || engine == nil || engine.Root == nil || probe.calls < 3 {
			t.Fatalf("cannot establish successful implicit parse checkpoints: %v", err)
		}
		for checkpoint := 1; checkpoint <= probe.calls; checkpoint++ {
			base, cancel := context.WithCancelCause(context.Background())
			cause := errors.New("document parse causal cancellation")
			ctx := &documentCheckpointContext{Context: base, cancel: cancel,
				cancelAt: checkpoint, cause: cause}
			output, err := DecodeDocument(ctx, data, DecodeOptions{SourceOffset: 1234})
			cancel(cause)
			if output != nil || !errors.Is(err, context.Canceled) || errors.Is(err, ErrLimit) ||
				!errors.Is(context.Cause(ctx), cause) {
				t.Fatalf("prefix%d checkpoint%d/%d canceled parse was masked: output%v error%v",
					len(prefix), checkpoint, probe.calls, output != nil, err)
			}
		}
		t.Logf("leading bytes=%d successful checkpoints=%d; each canceled path returned nil", len(prefix), probe.calls)
	}
}

func TestDecodeDocumentImplicitRootReserveAndActualLimits(t *testing.T) {
	data := []byte("  /A 1")
	const offset int64 = 901
	rootShort := int64(len(data)) + 1024 + int64(unsafe.Sizeof(Node{})) - 1
	engine, err := DecodeDocument(nil, data, DecodeOptions{SourceOffset: offset, MaxBytes: rootShort})
	var detail *Error
	if engine != nil || !errors.Is(err, ErrLimit) || !errors.As(err, &detail) || detail.Offset != offset+2 ||
		detail.Path != "engineData" || detail.Detail != "owned syntax or decoded values exceed MaxBytes" {
		t.Fatalf("actual root byte limit category/location changed unexpectedly: %v", err)
	}
	base, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("reserve canceled before byte failure")
	ctx := &documentCheckpointContext{Context: base, cancel: cancel, cancelAt: 3, cause: cause}
	engine, err = DecodeDocument(ctx, data, DecodeOptions{SourceOffset: offset, MaxBytes: rootShort})
	cancel(cause)
	if engine != nil || !errors.Is(err, context.Canceled) || errors.Is(err, ErrLimit) ||
		!errors.Is(context.Cause(ctx), cause) {
		t.Fatalf("root reserve masked cancellation with real short budget: %v", err)
	}
	for _, limits := range []DecodeOptions{{SourceOffset: offset, MaxNodes: 1},
		{SourceOffset: offset, MaxBytes: int64(len(data)) + 1023}} {
		engine, err := DecodeDocument(nil, data, limits)
		if engine != nil || !errors.Is(err, ErrLimit) || !errors.As(err, &detail) {
			t.Fatalf("actual resource limit no longer fails closed: %v", err)
		}
		if limits.MaxNodes == 1 && detail.Offset != offset+5 || limits.MaxNodes == 0 && detail.Offset != offset {
			t.Fatalf("actual node/initial-byte limit offset changed: %v", err)
		}
	}
}
