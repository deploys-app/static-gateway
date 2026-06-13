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
// It is safe for concurrent use.
type manifestCache struct {
	mu  sync.Mutex
	cap int
	ll  *list.List               // front = most-recently-used
	idx map[string]*list.Element // key -> element
}

type cacheEntry struct {
	key string
	m   *manifest.Manifest
}

func newManifestCache(capacity int) *manifestCache {
	if capacity < 1 {
		capacity = 1
	}
	return &manifestCache{
		cap: capacity,
		ll:  list.New(),
		idx: make(map[string]*list.Element, capacity),
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

// add inserts (or refreshes) key->m, evicting the least-recently-used entry when
// over capacity.
func (c *manifestCache) add(key string, m *manifest.Manifest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.idx[key]; ok {
		c.ll.MoveToFront(el)
		el.Value.(*cacheEntry).m = m
		return
	}
	el := c.ll.PushFront(&cacheEntry{key: key, m: m})
	c.idx[key] = el
	if c.ll.Len() > c.cap {
		c.evict()
	}
}

func (c *manifestCache) evict() {
	el := c.ll.Back()
	if el == nil {
		return
	}
	c.ll.Remove(el)
	delete(c.idx, el.Value.(*cacheEntry).key)
}

// len reports the number of cached entries (for tests).
func (c *manifestCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
