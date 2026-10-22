package psdfile

import (
	"context"
	"errors"
	"testing"
)

func TestExternalResolverIsExplicitAndOwnsResult(t *testing.T) {
	data := indexTestDocument(1, 8, 1, nil, nil, []byte{0, 0, 20})
	parent, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	record := LinkedFileRecord{Kind: "liFE", ID: "opaque-source", FileType: "8BPS", HasData: true}
	if _, err := parent.ResolveLinkedDocument(context.Background(), record, nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("external link must not use cached data or guessed paths: %v", err)
	}
	called := 0
	child, err := parent.ResolveLinkedDocument(context.Background(), record,
		func(ctx context.Context, received LinkedFileRecord) (*Document, error) {
			called++
			if received.ID != record.ID || received.Kind != "liFE" {
				t.Fatalf("resolver metadata changed: %+v", received)
			}
			return Parse(data)
		})
	if err != nil || called != 1 {
		t.Fatalf("resolver calls=%d result=%v", called, err)
	}
	defer child.Close()
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	pixels, err := child.DecodeComposite(context.Background())
	if err != nil || pixels.Channels[0].Data[0] != 20 {
		t.Fatalf("resolved child depends on parent lifecycle: %v", err)
	}
}

func TestExternalResolverCancellationAndInvalidResults(t *testing.T) {
	data := indexTestDocument(1, 8, 1, nil, nil, []byte{0, 0, 20})
	parent, _ := Parse(data)
	defer parent.Close()
	record := LinkedFileRecord{Kind: "liFE"}
	ctx, cancel := context.WithCancel(context.Background())
	child, _ := Parse(data)
	_, err := parent.ResolveLinkedDocument(ctx, record, func(context.Context, LinkedFileRecord) (*Document, error) {
		cancel()
		return child, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("resolver cancellation = %v", err)
	}
	if _, err := child.DecodeComposite(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("canceled transferred source leaked: %v", err)
	}
	for _, result := range []*Document{nil, parent} {
		_, err := parent.ResolveLinkedDocument(context.Background(), record,
			func(context.Context, LinkedFileRecord) (*Document, error) { return result, nil })
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("invalid resolver result = %v", err)
		}
	}
	called := false
	_, err = parent.ResolveLinkedDocument(ctx, record,
		func(context.Context, LinkedFileRecord) (*Document, error) { called = true; return nil, nil })
	if called || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup called resolver=%t error=%v", called, err)
	}
}
