package blobstore

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

var _ Store = (*CachingStore)(nil)

func blobKey(sha string) string { return BlobKey("acme", "site", sha) }

// readAllClose reads and closes rc, failing the test on error.
func readAllClose(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

func TestCachingStoreHitSkipsBackend(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	key := blobKey("h1")
	f.Put(key, []byte("hello"), "text/html")

	cs := NewCachingStore(f, 1<<20, 1<<20)

	// First read populates the cache (one backend Get).
	if got := readAllClose(t, mustGet(t, cs, ctx, key)); got != "hello" {
		t.Fatalf("first body = %q", got)
	}
	// Second read is served from cache: no additional backend Get.
	if got := readAllClose(t, mustGet(t, cs, ctx, key)); got != "hello" {
		t.Fatalf("second body = %q", got)
	}
	if n := f.GetCount(); n != 1 {
		t.Errorf("backend Get count = %d, want 1 (second read should hit cache)", n)
	}
}

func TestCachingStoreSkipsManifestKeys(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	mkey := ManifestKey("acme", "site", "rel1")
	f.Put(mkey, []byte(`{"manifest":true}`), "application/json")

	cs := NewCachingStore(f, 1<<20, 1<<20)
	_ = readAllClose(t, mustGet(t, cs, ctx, mkey))
	_ = readAllClose(t, mustGet(t, cs, ctx, mkey))
	// Manifest reads must pass through (the server caches parsed manifests).
	if n := f.GetCount(); n != 2 {
		t.Errorf("manifest backend Get count = %d, want 2 (manifests not byte-cached)", n)
	}
	if c := cs.(*CachingStore).cache.len(); c != 0 {
		t.Errorf("cache len = %d, want 0 (manifest not cached)", c)
	}
}

func TestCachingStoreStreamsOversizedUncached(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	key := blobKey("big")
	f.Put(key, []byte(strings.Repeat("x", 100)), "application/octet-stream")

	cs := NewCachingStore(f, 1<<20, 8) // per-blob cap 8 bytes; the 100-byte blob is too big

	if got := readAllClose(t, mustGet(t, cs, ctx, key)); len(got) != 100 {
		t.Fatalf("body len = %d, want 100", len(got))
	}
	if got := readAllClose(t, mustGet(t, cs, ctx, key)); len(got) != 100 {
		t.Fatalf("body len = %d, want 100", len(got))
	}
	if n := f.GetCount(); n != 2 {
		t.Errorf("backend Get count = %d, want 2 (oversized blob streams uncached)", n)
	}
	if c := cs.(*CachingStore).cache.len(); c != 0 {
		t.Errorf("cache len = %d, want 0", c)
	}
}

func TestCachingStoreByteEviction(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	const sz = 10
	for _, sha := range []string{"a", "b", "c"} {
		f.Put(blobKey(sha), []byte(strings.Repeat(sha, sz)), "text/plain")
	}
	cs := NewCachingStore(f, 2*sz, 1<<20).(*CachingStore) // budget fits two blobs

	_ = readAllClose(t, mustGet(t, cs, ctx, blobKey("a")))
	_ = readAllClose(t, mustGet(t, cs, ctx, blobKey("b")))
	_ = readAllClose(t, mustGet(t, cs, ctx, blobKey("c"))) // evicts LRU "a"

	if c := cs.cache.len(); c != 2 {
		t.Errorf("cache len = %d, want 2", c)
	}
	if cs.cache.bytes != 2*sz {
		t.Errorf("cache bytes = %d, want %d", cs.cache.bytes, 2*sz)
	}
	// "a" was evicted -> a re-read hits the backend again; "c" is served from cache.
	before := f.GetCount()
	_ = readAllClose(t, mustGet(t, cs, ctx, blobKey("a")))
	_ = readAllClose(t, mustGet(t, cs, ctx, blobKey("c")))
	if got := f.GetCount() - before; got != 1 {
		t.Errorf("backend Gets on re-read = %d, want 1 (only evicted 'a' refetched)", got)
	}
}

func TestCachingStoreDisabled(t *testing.T) {
	f := NewFake()
	if cs := NewCachingStore(f, 0, 1<<20); cs != Store(f) {
		t.Errorf("totalBytes<=0 should return the inner store unwrapped")
	}
}

func TestCachingStorePropagatesNotFound(t *testing.T) {
	cs := NewCachingStore(NewFake(), 1<<20, 1<<20)
	if _, err := cs.Get(context.Background(), blobKey("missing")); !errors.Is(err, ErrNotExist) {
		t.Errorf("Get(missing) err = %v, want ErrNotExist", err)
	}
}

// TestCachingStoreConcurrent exercises the cache under concurrent readers (run
// with -race) mixing hits, misses, and evictions on a tight budget.
func TestCachingStoreConcurrent(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	const n = 12
	for i := 0; i < n; i++ {
		f.Put(blobKey(string(rune('a'+i))), []byte(strings.Repeat("y", 10)), "text/plain")
	}
	cs := NewCachingStore(f, 50, 1<<20) // budget holds ~5 blobs -> constant eviction

	done := make(chan struct{})
	for g := 0; g < 16; g++ {
		go func(g int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 200; i++ {
				key := blobKey(string(rune('a' + (g+i)%n)))
				rc, err := cs.Get(ctx, key)
				if err != nil {
					t.Errorf("Get: %v", err)
					return
				}
				if got := readAllClose(t, rc); len(got) != 10 {
					t.Errorf("body len = %d, want 10", len(got))
					return
				}
			}
		}(g)
	}
	for g := 0; g < 16; g++ {
		<-done
	}
	if b := cs.(*CachingStore).cache.bytes; b > 50 {
		t.Errorf("cache bytes = %d, want <= 50 (budget never exceeded)", b)
	}
}

func mustGet(t *testing.T, s Store, ctx context.Context, key string) io.ReadCloser {
	t.Helper()
	rc, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get(%s): %v", key, err)
	}
	return rc
}
