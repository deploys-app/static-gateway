package blobstore

import (
	"bytes"
	"container/list"
	"context"
	"io"
	"strings"
	"sync"
)

// DefaultMaxCacheableBlobBytes is the per-blob size ceiling for the in-process
// blob cache. Blobs larger than this stream straight through and are never held
// in memory: large media is immutable and edge-cached for a year so the origin
// rarely re-reads it, whereas the frequently-revalidated objects that actually
// drive origin reads (HTML/CSS/JS/small images) sit well under this.
const DefaultMaxCacheableBlobBytes = 1 << 20 // 1 MiB

// CachingStore wraps a Store with a bounded in-process LRU byte-cache for small
// immutable blobs. Blobs are content-addressed (the storage key embeds the
// sha256), so a cached entry is valid forever and never needs invalidation. Only
// blob objects are cached — manifest reads pass through, since the server already
// caches parsed manifests and the raw bytes would be redundant.
//
// On a cache miss for a cacheable-size blob the body is read fully into memory,
// stored, and served from memory; larger blobs and unknown-size readers stream
// straight through uncached. A cache hit skips the backend — and its GCS
// round-trip — entirely. Safe for concurrent use.
type CachingStore struct {
	inner        Store
	cache        *blobCache
	maxBlobBytes int64
}

// NewCachingStore wraps inner with a byte-bounded blob cache holding up to
// totalBytes of blob data, caching only blobs of at most maxBlobBytes (defaulting
// to DefaultMaxCacheableBlobBytes when <= 0). If totalBytes <= 0 the cache is
// disabled and inner is returned unwrapped.
func NewCachingStore(inner Store, totalBytes, maxBlobBytes int64) Store {
	if totalBytes <= 0 {
		return inner
	}
	if maxBlobBytes <= 0 {
		maxBlobBytes = DefaultMaxCacheableBlobBytes
	}
	return &CachingStore{
		inner:        inner,
		cache:        newBlobCache(totalBytes),
		maxBlobBytes: maxBlobBytes,
	}
}

func (c *CachingStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if !isBlobKey(key) {
		return c.inner.Get(ctx, key)
	}
	if data, ok := c.cache.get(key); ok {
		return newBytesReadCloser(data), nil
	}
	rc, err := c.inner.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	// Only cache when the size is known and within the per-blob ceiling; otherwise
	// stream straight through so a large blob is never buffered into memory.
	sz, ok := rc.(interface{ Size() int64 })
	if !ok {
		return rc, nil
	}
	n := sz.Size()
	if n < 0 || n > c.maxBlobBytes {
		return rc, nil
	}
	// The size is known, so read exactly n bytes into a right-sized buffer: exact
	// cache accounting (no io.ReadAll capacity slack inflating memory past the
	// budget) and one allocation. A short read means the body disagreed with the
	// reported size — surface that rather than caching a truncated blob. A Close
	// error after a complete read is benign (the streaming path ignores it too).
	data := make([]byte, n)
	_, err = io.ReadFull(rc, data)
	_ = rc.Close()
	if err != nil {
		return nil, err
	}
	c.cache.add(key, data)
	return newBytesReadCloser(data), nil
}

func (c *CachingStore) Stat(ctx context.Context, key string) (Attrs, error) {
	return c.inner.Stat(ctx, key)
}

func (c *CachingStore) Exists(ctx context.Context, key string) (bool, error) {
	return c.inner.Exists(ctx, key)
}

// isBlobKey reports whether key addresses a content-addressed blob (rather than a
// manifest or other object). Blob keys are sites/<project>/<name>/blobs/<sha>
// (see BlobKey); manifests live under .../releases/<sha>.
func isBlobKey(key string) bool {
	return strings.Contains(key, "/blobs/")
}

// bytesReadCloser serves cached bytes. It embeds *bytes.Reader so it exposes
// Size() int64 — the gateway sets Content-Length from a reader-side Size(), so a
// cached blob produces the same headers a GCS-backed read would.
type bytesReadCloser struct{ *bytes.Reader }

func (bytesReadCloser) Close() error { return nil }

func newBytesReadCloser(b []byte) *bytesReadCloser {
	return &bytesReadCloser{bytes.NewReader(b)}
}

// blobCache is a byte-bounded LRU of immutable blob bodies keyed by storage key.
// Safe for concurrent use.
type blobCache struct {
	mu       sync.Mutex
	bytesCap int64
	bytes    int64
	ll       *list.List               // front = most-recently-used
	idx      map[string]*list.Element // key -> element
}

type blobEntry struct {
	key  string
	data []byte
}

func newBlobCache(bytesCap int64) *blobCache {
	return &blobCache{
		bytesCap: bytesCap,
		ll:       list.New(),
		idx:      make(map[string]*list.Element),
	}
}

// get returns the cached bytes for key, marking it most-recently-used. The
// returned slice is shared and must be treated as read-only (callers wrap it in a
// fresh bytes.Reader, which never mutates it).
func (c *blobCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.idx[key]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*blobEntry).data, true
	}
	return nil, false
}

// add stores key->data, evicting least-recently-used entries until within budget.
// A blob larger than the whole budget is not retained (the caller serves from the
// bytes it already holds, so nothing is lost).
func (c *blobCache) add(key string, data []byte) {
	size := int64(len(data))
	c.mu.Lock()
	defer c.mu.Unlock()
	if size > c.bytesCap {
		return
	}
	if el, ok := c.idx[key]; ok {
		c.ll.MoveToFront(el)
		e := el.Value.(*blobEntry)
		c.bytes += size - int64(len(e.data))
		e.data = data
	} else {
		el := c.ll.PushFront(&blobEntry{key: key, data: data})
		c.idx[key] = el
		c.bytes += size
	}
	for c.bytes > c.bytesCap {
		c.evict()
	}
}

func (c *blobCache) evict() {
	el := c.ll.Back()
	if el == nil {
		return
	}
	c.ll.Remove(el)
	e := el.Value.(*blobEntry)
	delete(c.idx, e.key)
	c.bytes -= int64(len(e.data))
}

// len reports the number of cached blobs (for tests).
func (c *blobCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
