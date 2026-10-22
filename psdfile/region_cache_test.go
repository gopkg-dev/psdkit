package psdfile

import (
	"bytes"
	"context"
	"errors"
	"image"
	"sync"
	"testing"
)

func TestRegionCacheLRUAndOwnedResults(t *testing.T) {
	document := decodeTestDocument([]byte{1, 2, 3, 4, 5, 6}, 3, 2, 8, 0, 2)
	reader := &regionRecordingReader{reader: document.reader}
	document.reader = reader
	cache, err := NewRegionCache(document, RegionCacheOptions{MaxBytes: 2, MaxEntries: 2})
	if err != nil {
		t.Fatal(err)
	}
	first, second, third := image.Rect(0, 0, 1, 1), image.Rect(1, 0, 2, 1), image.Rect(2, 0, 3, 1)
	firstPlane, err := cache.DecodeChannelRegion(nil, 0, 0, first)
	if err != nil {
		t.Fatal(err)
	}
	firstPlane.Data[0] = 255
	if _, err := cache.DecodeChannelRegion(nil, 0, 0, second); err != nil {
		t.Fatal(err)
	}
	firstHit, err := cache.DecodeChannelRegion(nil, 0, 0, first)
	if err != nil || firstHit.Data[0] != 1 || len(reader.reads) != 2 {
		t.Fatalf("cache reused caller-owned mutated samples or reread source: %v", err)
	}
	firstHit.Data[0] = 0
	if _, err := cache.DecodeChannelRegion(nil, 0, 0, third); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.DecodeChannelRegion(nil, 0, 0, first); err != nil {
		t.Fatal(err)
	}
	if len(reader.reads) != 3 {
		t.Fatal("recently accessed first region was incorrectly evicted")
	}
	if _, err := cache.DecodeChannelRegion(nil, 0, 0, second); err != nil {
		t.Fatal(err)
	}
	stats := cache.Stats()
	if len(reader.reads) != 4 || stats.Hits != 2 || stats.Misses != 4 || stats.Entries != 2 || stats.Bytes != 2 {
		t.Fatalf("bounded LRU counters/eviction differ: %+v", stats)
	}
	cache.Clear()
	if stats := cache.Stats(); stats.Entries != 0 || stats.Bytes != 0 || stats.Hits != 2 || stats.Misses != 4 {
		t.Fatalf("Clear failed to release entries or unexpectedly reset counters: %+v", stats)
	}
}

func TestRegionCacheCompositeIdentityAndOversizedTiles(t *testing.T) {
	document := decodeTestDocument([]byte{1, 2, 3, 4, 5, 6}, 3, 2, 8, 0, 2)
	cache, err := NewRegionCache(document, RegionCacheOptions{MaxBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	rectangle := image.Rect(0, 0, 1, 1)
	layer, err := cache.DecodeChannelRegion(nil, 0, 0, rectangle)
	if err != nil {
		t.Fatal(err)
	}
	composite, err := cache.DecodeCompositeChannelRegion(nil, 0, rectangle)
	if err != nil || layer.ID != -1 || composite.ID != 0 {
		t.Fatalf("cache key mixed a layer alpha channel with saved-image channel zero: %v", err)
	}
	if _, err := cache.DecodeChannelRegion(nil, 0, 0, image.Rect(0, 0, 2, 1)); err != nil {
		t.Fatal(err)
	}
	if stats := cache.Stats(); stats.Entries != 1 || stats.Bytes != 1 || stats.Hits != 0 || stats.Misses != 3 {
		t.Fatalf("oversized tile bypass changed retained cache storage: %+v", stats)
	}
}

func TestRegionCacheClosedCanceledAndOptions(t *testing.T) {
	document := decodeTestDocument([]byte{1, 2, 3, 4, 5, 6}, 3, 2, 8, 0, 2)
	for _, options := range []RegionCacheOptions{{MaxBytes: -1}, {MaxEntries: -1}} {
		if _, err := NewRegionCache(document, options); !errors.Is(err, ErrLimit) {
			t.Fatalf("invalid cache budget accepted: %v", err)
		}
	}
	if _, err := NewRegionCache(nil); !errors.Is(err, ErrMalformed) {
		t.Fatalf("nil cache source accepted: %v", err)
	}
	cache, err := NewRegionCache(document)
	if err != nil {
		t.Fatal(err)
	}
	rectangle := image.Rect(0, 0, 1, 1)
	if _, err := cache.DecodeChannelRegion(nil, 0, 0, rectangle); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cache.DecodeChannelRegion(ctx, 0, 0, rectangle); !errors.Is(err, context.Canceled) {
		t.Fatalf("warm cache ignored cancellation: %v", err)
	}
	if stats := cache.Stats(); stats.Hits != 0 || stats.Misses != 1 {
		t.Fatalf("pre-canceled request entered the cache: %+v", stats)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.DecodeChannelRegion(nil, 0, 0, rectangle); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed cache returned retained data: %v", err)
	}
	if _, err := document.DecodeChannelRegion(nil, 0, 0, rectangle); err != nil {
		t.Fatalf("closing cache unexpectedly closed borrowed source: %v", err)
	}
	cache, err = NewRegionCache(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.DecodeChannelRegion(nil, 0, 0, rectangle); err != nil {
		t.Fatal(err)
	}
	if err := document.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.DecodeChannelRegion(nil, 0, 0, rectangle); !errors.Is(err, ErrClosed) {
		t.Fatalf("warm cache outlived its closed source: %v", err)
	}
	if _, err := NewRegionCache(document); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed source cache was created: %v", err)
	}
}

func TestRegionCacheConcurrentOwnedReads(t *testing.T) {
	document := decodeTestDocument(bytes.Repeat([]byte{73}, 100), 10, 10, 8, 0, 2)
	cache, err := NewRegionCache(document, RegionCacheOptions{MaxBytes: 8, MaxEntries: 2})
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for worker := range 32 {
		group.Go(func() {
			for iteration := range 20 {
				left := (worker + iteration) % 4
				plane, err := cache.DecodeChannelRegion(nil, 0, 0, image.Rect(left, 2, left+2, 4))
				if err != nil || !bytes.Equal(plane.Data, []byte{73, 73, 73, 73}) {
					t.Errorf("concurrent cache samples changed: %v", err)
					return
				}
				plane.Data[0] = 0
			}
		})
	}
	group.Wait()
	if stats := cache.Stats(); stats.Bytes > 8 || stats.Entries > 2 {
		t.Fatalf("concurrent inserts exceeded the cache budget: %+v", stats)
	}
}
