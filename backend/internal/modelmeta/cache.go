package modelmeta

import (
	"context"
	"sync"

	"golang.org/x/sync/singleflight"
)

// DefaultCacheEntries is how many inspected checkpoints a Cache keeps. Each
// entry is a few hundred kilobytes at most (the tensor listing is capped), so
// this stays well inside a few hundred megabytes even when full.
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
