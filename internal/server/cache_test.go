package server

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/deploys-app/static-gateway/internal/manifest"
)

func TestManifestCacheLRU(t *testing.T) {
	c := newManifestCache(2, 0)
	m := func() *manifest.Manifest { return &manifest.Manifest{} }

	c.add("a", m())
	c.add("b", m())
	if _, ok := c.get("a"); !ok {
		t.Fatalf("a evicted too early")
	}
	// Touch a, then add c -> b (least recently used) is evicted.
	c.add("c", m())
	if _, ok := c.get("b"); ok {
		t.Errorf("b should have been evicted")
	}
	if _, ok := c.get("a"); !ok {
		t.Errorf("a should still be present")
	}
	if _, ok := c.get("c"); !ok {
		t.Errorf("c should be present")
	}
	if c.len() != 2 {
		t.Errorf("len = %d, want 2", c.len())
	}
}

func TestManifestCacheReplace(t *testing.T) {
	c := newManifestCache(2, 0)
	m1 := &manifest.Manifest{Environment: "production"}
	m2 := &manifest.Manifest{Environment: "pr-1"}
	c.add("k", m1)
	c.add("k", m2)
	got, ok := c.get("k")
	if !ok || got.Environment != "pr-1" {
		t.Errorf("replace failed: %+v ok=%v", got, ok)
	}
	if c.len() != 1 {
		t.Errorf("len = %d, want 1", c.len())
	}
}

func TestManifestCacheMinCap(t *testing.T) {
	c := newManifestCache(0, 0) // clamped to 1
	c.add("a", &manifest.Manifest{})
	c.add("b", &manifest.Manifest{})
	if c.len() != 1 {
		t.Errorf("len = %d, want 1 (cap clamped)", c.len())
	}
}

// loadManifestN builds and parses a manifest with n files so its ApproxSize is
// non-trivial and identical across calls (the keys/paths are fixed), letting the
// byte-budget tests reason in whole-manifest units.
func loadManifestN(t *testing.T, n int) *manifest.Manifest {
	t.Helper()
	files := make(map[string]any, n)
	for i := range n {
		files[fmt.Sprintf("p/%04d.html", i)] = map[string]any{
			"blob": "deadbeef", "ct": "text/html; charset=utf-8", "cache": "html",
		}
	}
	data, err := json.Marshal(map[string]any{"environment": "production", "files": files})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	m, err := manifest.Load(data)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return m
}

// TestManifestCacheByteBound proves the cache evicts by approximate bytes even
// when the entry count is well under cap.
func TestManifestCacheByteBound(t *testing.T) {
	unit := int64(loadManifestN(t, 20).ApproxSize())
	if unit <= 0 {
		t.Fatalf("ApproxSize = %d, want > 0", unit)
	}
	// Entry cap is generous (100), so only the byte budget can evict. Budget fits
	// exactly two manifests.
	c := newManifestCache(100, 2*unit)
	c.add("a", loadManifestN(t, 20))
	c.add("b", loadManifestN(t, 20))
	c.add("c", loadManifestN(t, 20)) // pushes bytes to 3*unit -> evict LRU "a"

	if _, ok := c.get("a"); ok {
		t.Errorf("a should have been byte-evicted")
	}
	if _, ok := c.get("b"); !ok {
		t.Errorf("b should still be present")
	}
	if _, ok := c.get("c"); !ok {
		t.Errorf("c should be present")
	}
	if c.len() != 2 {
		t.Errorf("len = %d, want 2", c.len())
	}
	if c.bytes != 2*unit {
		t.Errorf("bytes = %d, want %d", c.bytes, 2*unit)
	}
}

// TestManifestCacheByteFloorKeepsOne proves a manifest larger than the entire
// byte budget is never self-evicted: the cache always retains the most recent
// entry so the in-flight request can be served.
func TestManifestCacheByteFloorKeepsOne(t *testing.T) {
	c := newManifestCache(100, 1) // 1-byte budget: every manifest exceeds it
	c.add("a", loadManifestN(t, 20))
	if _, ok := c.get("a"); !ok {
		t.Fatalf("a evicted despite being the only entry")
	}
	if c.len() != 1 {
		t.Fatalf("len = %d, want 1", c.len())
	}
	c.add("b", loadManifestN(t, 20)) // a (LRU) evicted, b kept by the floor
	if _, ok := c.get("a"); ok {
		t.Errorf("a should have been evicted")
	}
	if _, ok := c.get("b"); !ok {
		t.Errorf("b should be retained by the one-entry floor")
	}
	if c.len() != 1 {
		t.Errorf("len = %d, want 1", c.len())
	}
}
