// Package metrics holds the static-gateway's Prometheus instrumentation.
package metrics

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Requests counts HTTP requests served per static site, labeled by the project
// SID and site name parsed from the Ingress-rewritten request path.
//
// A Static deployment has no pod/Service, so it never appears in the
// pod/parapet container metrics the collector normally scrapes. This per-site
// counter is the collector's only attributable signal for static request
// volume — it is summed by (project, name) and recorded against the
// deployment's usage so the console can chart requests for Static deployments.
var Requests = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "static_gateway_requests_total",
		Help: "Total static-gateway requests served, by project SID and site name.",
	},
	[]string{"project", "name"},
)

// ResponseBytes counts response body bytes the gateway streams per static site,
// labeled like Requests (project SID + site name).
//
// A Static deployment has no pod, so the pod-based container_network_transmit
// egress the collector bills never sees it. This per-site byte counter is the
// origin-side egress signal: the collector sums increase(...[1d]) by project and
// records it as the project's static_egress usage — the static analog of pod
// egress. Body bytes only; response headers are negligible and HEAD/304 add
// nothing.
var ResponseBytes = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "static_gateway_response_bytes_total",
		Help: "Total static-gateway response body bytes served, by project SID and site name.",
	},
	[]string{"project", "name"},
)

func init() {
	prometheus.MustRegister(Requests, ResponseBytes)
}

// now is the clock for last-seen bookkeeping; overridable in tests.
var now = time.Now

// siteKey identifies one labeled series shared by both counters.
type siteKey struct{ project, name string }

// lastSeen maps each active site to the last time it served traffic, so the
// evictor can drop the label sets of sites gone quiet (a site's Requests and
// ResponseBytes series share one entry). sync.Map fits the access pattern: a
// mostly-stable key set with a hot read on every request (touch) and a rare
// write only for a genuinely new site, so the common path takes no lock.
var lastSeen sync.Map // siteKey -> *atomic.Int64 (unix nanos)

// touch records that (project, name) served traffic at now().
func touch(project, name string) {
	k := siteKey{project, name}
	n := now().UnixNano()
	if v, ok := lastSeen.Load(k); ok {
		v.(*atomic.Int64).Store(n)
		return
	}
	ts := new(atomic.Int64)
	ts.Store(n)
	// Another goroutine may have created the entry between the Load miss above
	// and here; if so, advance the existing one instead of leaking a duplicate.
	if actual, loaded := lastSeen.LoadOrStore(k, ts); loaded {
		actual.(*atomic.Int64).Store(n)
	}
}

// ObserveRequest counts one served request for (project, name) and marks the
// site active. It is called once per served request, so it is the liveness
// signal the evictor keys on.
func ObserveRequest(project, name string) {
	Requests.WithLabelValues(project, name).Inc()
	touch(project, name)
}

// AddResponseBytes adds n response body bytes for (project, name) and refreshes
// the site's liveness. n <= 0 (HEAD, 304, empty body) is ignored.
func AddResponseBytes(project, name string, n int64) {
	if n <= 0 {
		return
	}
	ResponseBytes.WithLabelValues(project, name).Add(float64(n))
	touch(project, name)
}

// StartEvictor runs a background sweep that drops the Prometheus label sets of
// sites idle longer than ttl, bounding the gateway's metric cardinality as
// ephemeral / TTL'd previews churn (their unique site names would otherwise
// accumulate in the CounterVecs until the process restarts). ttl <= 0 disables
// eviction; the sweep stops when ctx is cancelled.
//
// ttl must comfortably exceed the collector's billing window (1 day) so a site
// with same-day billable bytes is never evicted before increase(...[1d]) reads
// them; the binary defaults ttl to 48h. Eviction is per-replica and needs no
// coordination: a spurious reset (a site re-activating exactly as it is swept)
// is absorbed by increase()'s counter-reset handling.
func StartEvictor(ctx context.Context, ttl, interval time.Duration) {
	if ttl <= 0 {
		return
	}
	if interval <= 0 {
		interval = time.Hour
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				evictIdle(ttl)
			}
		}
	}()
}

// evictIdle deletes the counter label sets and tracking entries of sites whose
// last-seen is older than ttl. The next request to an evicted site re-creates
// its series at 0.
func evictIdle(ttl time.Duration) {
	cutoff := now().Add(-ttl).UnixNano()
	lastSeen.Range(func(key, value any) bool {
		if value.(*atomic.Int64).Load() < cutoff {
			k := key.(siteKey)
			Requests.DeleteLabelValues(k.project, k.name)
			ResponseBytes.DeleteLabelValues(k.project, k.name)
			lastSeen.Delete(k)
		}
		return true
	})
}
