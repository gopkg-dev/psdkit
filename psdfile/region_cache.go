package psdfile

import (
	"container/list"
	"context"
	"image"
	"sync"
)

// RegionCacheOptions bounds retained sample bytes and the number of cached rectangles.
// Zero selects defaults; negative values are invalid. Decoder scratch and returned owned planes are separate.
type RegionCacheOptions struct {
	MaxBytes      int64
	MaxEntries    int
	DecodeOptions RegionOptions
}

// RegionCacheStats reports cumulative lookups and currently retained sample storage.
type RegionCacheStats struct {
	Hits, Misses uint64
	Bytes        int64
	Entries      int
}

// RegionCache retains recently used original-depth regions from one immutable borrowed Document.
// Create caches with NewRegionCache. Every result owns its samples; callers may mutate them independently.
// Concurrent misses may decode independently. Close clears the cache without closing the source Document.
type RegionCache struct {
	source  *Document
	options RegionCacheOptions
	mu      sync.Mutex
	closed  bool
	entries map[regionCacheKey]*list.Element
	order   list.List
	stats   RegionCacheStats
}

type regionCacheKey struct {
	layer, channel int
	composite      bool
	rectangle      image.Rectangle
}

type regionCacheEntry struct {
	key   regionCacheKey
	plane Plane
}

// NewRegionCache creates a bounded LRU using 64 MiB and 256 entries by default.
func NewRegionCache(source *Document, options ...RegionCacheOptions) (*RegionCache, error) {
	if source == nil || len(options) > 1 {
		return nil, fileError(ErrMalformed, "pixels.cache", "cache.options", 0,
			"provide a source document and at most one RegionCacheOptions")
	}
	settings := RegionCacheOptions{MaxBytes: 64 << 20, MaxEntries: 256}
	if len(options) == 1 {
		settings.DecodeOptions = options[0].DecodeOptions
		if options[0].MaxBytes < 0 || options[0].MaxEntries < 0 {
			return nil, fileError(ErrLimit, "pixels.cache", "cache.options", 0, "cache limits must not be negative")
		}
		if options[0].MaxBytes != 0 {
			settings.MaxBytes = options[0].MaxBytes
		}
		if options[0].MaxEntries != 0 {
			settings.MaxEntries = options[0].MaxEntries
		}
	}
	decodeSettings, err := source.regionOptions([]RegionOptions{settings.DecodeOptions})
	if err != nil {
		return nil, err
	}
	settings.DecodeOptions = decodeSettings
	if _, err := source.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	return &RegionCache{source: source, options: settings, entries: make(map[regionCacheKey]*list.Element)}, nil
}

// DecodeChannelRegion returns an owned layer-channel region, reusing a retained sample block when present.
func (cache *RegionCache) DecodeChannelRegion(
	ctx context.Context, layerIndex, channelIndex int, rectangle image.Rectangle,
) (*Plane, error) {
	return cache.decode(ctx, regionCacheKey{layer: layerIndex, channel: channelIndex, rectangle: rectangle})
}

// DecodeCompositeChannelRegion returns an owned saved-image region through the same bounded LRU.
func (cache *RegionCache) DecodeCompositeChannelRegion(
	ctx context.Context, channelIndex int, rectangle image.Rectangle,
) (*Plane, error) {
	return cache.decode(ctx, regionCacheKey{channel: channelIndex, composite: true, rectangle: rectangle})
}

func (cache *RegionCache) decode(ctx context.Context, key regionCacheKey) (*Plane, error) {
	ctx = decodeContext(ctx)
	cache.source.mu.RLock()
	if cache.source.closed {
		cache.source.mu.RUnlock()
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		cache.source.mu.RUnlock()
		return nil, err
	}
	cache.mu.Lock()
	if cache.closed {
		cache.mu.Unlock()
		cache.source.mu.RUnlock()
		return nil, ErrClosed
	}
	if element := cache.entries[key]; element != nil {
		cache.stats.Hits++
		cache.order.MoveToFront(element)
		plane, err := cloneRegionPlane(ctx, element.Value.(regionCacheEntry).plane)
		cache.mu.Unlock()
		cache.source.mu.RUnlock()
		return plane, err
	}
	cache.stats.Misses++
	cache.mu.Unlock()
	cache.source.mu.RUnlock()
	var plane *Plane
	var err error
	if key.composite {
		plane, err = cache.source.DecodeCompositeChannelRegion(ctx, key.channel, key.rectangle, cache.options.DecodeOptions)
	} else {
		plane, err = cache.source.DecodeChannelRegion(ctx, key.layer, key.channel, key.rectangle, cache.options.DecodeOptions)
	}
	if err != nil {
		return nil, err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	length := int64(len(plane.Data))
	if length > cache.options.MaxBytes {
		return plane, nil
	}
	if cache.entries[key] != nil {
		return plane, nil
	}
	for len(cache.entries) >= cache.options.MaxEntries || cache.stats.Bytes > cache.options.MaxBytes-length {
		cache.removeOldest()
	}
	retained, err := cloneRegionPlane(ctx, *plane)
	if err != nil {
		return nil, err
	}
	element := cache.order.PushFront(regionCacheEntry{key: key, plane: *retained})
	cache.entries[key] = element
	cache.stats.Bytes += length
	cache.stats.Entries = len(cache.entries)
	return plane, nil
}

func cloneRegionPlane(ctx context.Context, original Plane) (*Plane, error) {
	result := original
	result.Data = make([]byte, len(original.Data))
	for offset := 0; offset < len(original.Data); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := offset + min(64<<10, len(original.Data)-offset)
		copy(result.Data[offset:end], original.Data[offset:end])
		offset = end
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &result, nil
}

func (cache *RegionCache) removeOldest() {
	element := cache.order.Back()
	entry := element.Value.(regionCacheEntry)
	cache.stats.Bytes -= int64(len(entry.plane.Data))
	delete(cache.entries, entry.key)
	cache.order.Remove(element)
	cache.stats.Entries = len(cache.entries)
}

// Stats returns a synchronized snapshot of cache counters and retained storage.
func (cache *RegionCache) Stats() RegionCacheStats {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.stats
}

// Clear releases retained samples while keeping cumulative hit and miss counters.
func (cache *RegionCache) Clear() {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.entries = make(map[regionCacheKey]*list.Element)
	cache.order.Init()
	cache.stats.Bytes, cache.stats.Entries = 0, 0
}

// Close releases retained samples and makes subsequent cache requests return ErrClosed.
func (cache *RegionCache) Close() error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.closed = true
	cache.entries = nil
	cache.order.Init()
	cache.stats.Bytes, cache.stats.Entries = 0, 0
	return nil
}
