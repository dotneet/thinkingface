package modelmeta

import (
	"context"
	"sync"

	"golang.org/x/sync/singleflight"
)

// DefaultCacheEntries is how many inspected checkpoints a Cache keeps.
//
// Every part of an entry that a file controls has a ceiling, so an entry's
// worst case can be worked out rather than hoped for:
//
//   - the listing holds at most maxTensors (4096) tensors, and one tensor is
//     at most maxTensorNameRunes of name and maxDTypeRunes of dtype (512 and
//     128 bytes at four bytes a character) over maxShapeDims dimensions (512
//     bytes), so about 1.2 KiB each -- 5 MiB of listing;
//   - the dtype breakdown is at most maxDTypeStats buckets, a few kilobytes;
//   - the metadata map is at most maxMetadataBytes (256 KiB);
//   - the warnings are a fixed handful of fixed-format lines. None of them
//     quotes a name out of the file, which is what would make their number and
//     their length the file's business rather than ours.
//
// That 5 MiB is what a deliberately hostile file can force, and it is the
// figure to multiply this constant by before raising it. A real checkpoint is
// nowhere near it: a few hundred kilobytes an entry, so a full cache of them
// is well under a hundred megabytes.
const DefaultCacheEntries = 256

// Cache memoises inspections. Keys are content-addressed -- an LFS OID or a
// git blob hash -- so an entry can never go stale: different bytes mean a
// different key.
//
// A Cache is safe for concurrent use.
type Cache struct {
	max   int
	group singleflight.Group

	mu      sync.Mutex
	entries map[string]*Info
	order   []string
}

func NewCache(max int) *Cache {
	if max <= 0 {
		max = DefaultCacheEntries
	}
	return &Cache{max: max, entries: map[string]*Info{}}
}

// Inspect returns the metadata for the object identified by key, parsing it
// through fetch on a miss. Concurrent callers asking for the same key share
// one parse.
func (c *Cache) Inspect(ctx context.Context, key string, format Format, size int64, fetch Fetcher) (*Info, error) {
	key = cacheKey(format, key)
	if info, ok := c.lookup(key); ok {
		return info, nil
	}

	v, err, _ := c.group.Do(key, func() (any, error) {
		// Another caller may have finished while we waited for the slot.
		if info, ok := c.lookup(key); ok {
			return info, nil
		}
		info, err := Inspect(ctx, format, size, fetch)
		if err != nil {
			return nil, err
		}
		c.store(key, info)
		return info, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*Info), nil
}

// cacheKey qualifies a content address with the format it was read as. The
// caller derives the format from the file's name rather than its bytes, so the
// same blob committed as both `model.bin` and `model.safetensors` is one
// content address but two inspections; without the qualifier whichever one ran
// first answered for both, Format field included.
func cacheKey(format Format, key string) string { return string(format) + ":" + key }

func (c *Cache) lookup(key string) (*Info, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	info, ok := c.entries[key]
	return info, ok
}

func (c *Cache) store(key string, info *Info) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; exists {
		return
	}
	c.entries[key] = info
	c.order = append(c.order, key)
	for len(c.order) > c.max {
		delete(c.entries, c.order[0])
		c.order = c.order[1:]
	}
}
