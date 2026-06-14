package server

import (
	"container/list"
	"sync"

	"github.com/deploys-app/static-gateway/internal/manifest"
)

// manifestCache is a bounded LRU of parsed manifests keyed by their storage key
// (which embeds project/name/release-sha). A release-sha is immutable
// (sha256(manifest)), so a cached entry is valid forever and never needs
// invalidation — a new release is a new key (SPEC §4.3). The cache only bounds
// memory across the (potentially many preview) releases a gateway has served.
//
// It is bounded by BOTH an entry count and an approximate-bytes budget: entry
// count alone cannot prevent an OOM when resident releases have very different
// manifest sizes (a few thousand-file sites cost far more than the default 1024
// small ones). Eviction is least-recently-used; a single manifest larger than the
// whole byte budget is never self-evicted, so it can still serve.
//
// It is safe for concurrent use.
type manifestCache struct {
	mu       sync.Mutex
	cap      int
	bytesCap int64                    // <=0 means no byte bound (entry count only)
	bytes    int64                    // sum of cached entries' approximate sizes
	ll       *list.List               // front = most-recently-used
	idx      map[string]*list.Element // key -> element
}

type cacheEntry struct {
	key  string
	m    *manifest.Manifest
	size int64 // m.ApproxSize() snapshotted at insert, for byte accounting
}

func newManifestCache(capacity int, bytesCap int64) *manifestCache {
	if capacity < 1 {
		capacity = 1
	}
	if bytesCap < 0 {
		bytesCap = 0
	}
	return &manifestCache{
		cap:      capacity,
		bytesCap: bytesCap,
		ll:       list.New(),
		idx:      make(map[string]*list.Element, capacity),
	}
}

// get returns the cached manifest for key, marking it most-recently-used.
func (c *manifestCache) get(key string) (*manifest.Manifest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.idx[key]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*cacheEntry).m, true
	}
	return nil, false
}

// add inserts (or refreshes) key->m, evicting least-recently-used entries until
// the cache is within both the entry-count and byte budgets.
func (c *manifestCache) add(key string, m *manifest.Manifest) {
	size := int64(m.ApproxSize())
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.idx[key]; ok {
		c.ll.MoveToFront(el)
		e := el.Value.(*cacheEntry)
		c.bytes += size - e.size
		e.size = size
		e.m = m
		c.evictToBound()
		return
	}
	el := c.ll.PushFront(&cacheEntry{key: key, m: m, size: size})
	c.idx[key] = el
	c.bytes += size
	c.evictToBound()
}

// evictToBound drops least-recently-used entries until the cache is within both
// budgets, but never evicts the last remaining entry — so a single manifest
// larger than the byte budget still serves rather than thrashing.
func (c *manifestCache) evictToBound() {
	for c.ll.Len() > 1 && (c.ll.Len() > c.cap || (c.bytesCap > 0 && c.bytes > c.bytesCap)) {
		c.evict()
	}
}

func (c *manifestCache) evict() {
	el := c.ll.Back()
	if el == nil {
		return
	}
	c.ll.Remove(el)
	e := el.Value.(*cacheEntry)
	delete(c.idx, e.key)
	c.bytes -= e.size
}

// len reports the number of cached entries (for tests).
func (c *manifestCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
