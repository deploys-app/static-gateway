package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// reset clears both counters and the tracker for a site so each test starts from
// a known state (the counters are process-global, registered once in init).
func reset(keys ...siteKey) {
	for _, k := range keys {
		Requests.DeleteLabelValues(k.project, k.name)
		ResponseBytes.DeleteLabelValues(k.project, k.name)
		lastSeen.Delete(k)
	}
}

func TestObserveRequestCountsAndTracks(t *testing.T) {
	k := siteKey{"obs-p", "obs-s"}
	reset(k)

	ObserveRequest(k.project, k.name)
	ObserveRequest(k.project, k.name)

	if got := testutil.ToFloat64(Requests.WithLabelValues(k.project, k.name)); got != 2 {
		t.Fatalf("Requests = %v, want 2", got)
	}
	if _, ok := lastSeen.Load(k); !ok {
		t.Fatal("ObserveRequest did not record last-seen")
	}
}

func TestAddResponseBytesSumsAndIgnoresNonPositive(t *testing.T) {
	k := siteKey{"byt-p", "byt-s"}
	reset(k)

	AddResponseBytes(k.project, k.name, 100)
	AddResponseBytes(k.project, k.name, 50)
	if got := testutil.ToFloat64(ResponseBytes.WithLabelValues(k.project, k.name)); got != 150 {
		t.Fatalf("ResponseBytes = %v, want 150", got)
	}

	// Non-positive counts (HEAD / 304 / empty body) add nothing and do not
	// register the site as active.
	empty := siteKey{"byt-empty-p", "byt-empty-s"}
	reset(empty)
	AddResponseBytes(empty.project, empty.name, 0)
	AddResponseBytes(empty.project, empty.name, -5)
	if _, ok := lastSeen.Load(empty); ok {
		t.Fatal("non-positive AddResponseBytes must not record last-seen")
	}
}

func TestEvictIdleDropsIdleKeepsActive(t *testing.T) {
	const ttl = 48 * time.Hour
	t0 := time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)
	restore := now
	defer func() { now = restore }()

	idle := siteKey{"ev-p", "idle"}
	active := siteKey{"ev-p", "active"}
	reset(idle, active)

	// idle served traffic at t0 and then went quiet.
	now = func() time.Time { return t0 }
	ObserveRequest(idle.project, idle.name)
	AddResponseBytes(idle.project, idle.name, 100)

	// active served traffic 49h later — past idle's ttl, but it is fresh.
	now = func() time.Time { return t0.Add(49 * time.Hour) }
	ObserveRequest(active.project, active.name)

	// cutoff = (t0+49h) - 48h = t0+1h: idle (t0) is older, active (t0+49h) is not.
	evictIdle(ttl)

	// Evicted: WithLabelValues re-creates the series at 0.
	if got := testutil.ToFloat64(Requests.WithLabelValues(idle.project, idle.name)); got != 0 {
		t.Fatalf("idle Requests = %v, want 0 (evicted, re-created at 0)", got)
	}
	if got := testutil.ToFloat64(ResponseBytes.WithLabelValues(idle.project, idle.name)); got != 0 {
		t.Fatalf("idle ResponseBytes = %v, want 0 (evicted)", got)
	}
	if _, ok := lastSeen.Load(idle); ok {
		t.Fatal("idle site still tracked after eviction")
	}

	// Survived: still carries its count.
	if got := testutil.ToFloat64(Requests.WithLabelValues(active.project, active.name)); got != 1 {
		t.Fatalf("active Requests = %v, want 1 (survived eviction)", got)
	}
	if _, ok := lastSeen.Load(active); !ok {
		t.Fatal("active site dropped from tracking")
	}
}
