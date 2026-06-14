// Package metrics holds the static-gateway's Prometheus instrumentation.
package metrics

import "github.com/prometheus/client_golang/prometheus"

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

func init() {
	prometheus.MustRegister(Requests)
}
