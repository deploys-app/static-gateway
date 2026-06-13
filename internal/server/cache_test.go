package server

import (
	"testing"

	"github.com/deploys-app/static-gateway/internal/manifest"
)

func TestManifestCacheLRU(t *testing.T) {
	c := newManifestCache(2)
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
	c := newManifestCache(2)
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
	c := newManifestCache(0) // clamped to 1
	c.add("a", &manifest.Manifest{})
	c.add("b", &manifest.Manifest{})
	if c.len() != 1 {
		t.Errorf("len = %d, want 1 (cap clamped)", c.len())
	}
}
