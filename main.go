// Command static-gateway is the in-cluster origin that serves immutable static
// site releases directly from object storage (SPEC §4). It is reached through the
// existing edge -> parapet core -> Ingress -> Service path; the Ingress rewrites
// the request to /<project>/<name>/<release-sha>/<rest> via the upstream-path
// annotation, and this service resolves and streams the backing blob.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/moonrhythm/parapet"
	"github.com/moonrhythm/parapet/pkg/healthz"
	"github.com/moonrhythm/parapet/pkg/logger"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/deploys-app/static-gateway/internal/blobstore"
	"github.com/deploys-app/static-gateway/internal/server"
)

// defaultManifestCacheBytes bounds the parsed-manifest cache by approximate
// retained memory in addition to entry count, so a burst of many large preview
// manifests cannot OOM the replica. Tune via MANIFEST_CACHE_BYTES.
const defaultManifestCacheBytes = 256 << 20 // 256 MiB

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	port := getenv("PORT", "8080")
	bucket := os.Getenv("SITE_BUCKET")
	if bucket == "" {
		slog.Error("SITE_BUCKET is required")
		os.Exit(1)
	}

	ctx := context.Background()

	// Read-only object storage. Credentials come from ADC / Workload Identity
	// (§6.5). gs:// today; swap the gocloud opener for r2:// to move to R2 (§4.2).
	store, closeStore, err := blobstore.OpenGCS(ctx, bucket)
	if err != nil {
		slog.Error("open bucket", "bucket", bucket, "error", err)
		os.Exit(1)
	}
	defer func() { _ = closeStore() }()

	h, err := server.New(server.Config{
		Store:              store,
		Logger:             logger,
		ManifestCacheBytes: getenvInt64("MANIFEST_CACHE_BYTES", defaultManifestCacheBytes),
	})
	if err != nil {
		slog.Error("build server", "error", err)
		os.Exit(1)
	}

	// Prometheus metrics on a separate port so the per-site request counter
	// (static_gateway_requests_total) can be scraped without colliding with the
	// site-serving handler, which consumes every path. Scrape this port.
	go func() {
		metricsAddr := ":" + getenv("METRICS_PORT", "9090")
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		slog.Info("static-gateway metrics listening", "addr", metricsAddr)
		if err := http.ListenAndServe(metricsAddr, mux); err != nil {
			slog.Error("metrics listen and serve", "error", err)
		}
	}()

	// parapet.NewBackend(): H2C-enabled, trusts X-Forwarded-* (it sits behind the
	// in-cluster parapet core), matching the dropbox/ipfs-gateway pattern.
	srv := parapet.NewBackend()
	srv.Addr = ":" + port
	// Read-side timeouts guard the origin against slow-client / stuck-connection
	// resource exhaustion (NewBackend leaves these at zero). No WriteTimeout: it is
	// an absolute deadline over the whole response and would truncate large or slow
	// blob streams; the manifest load has its own deadline and the GET/HEAD-only
	// surface carries no meaningful request body.
	srv.ReadHeaderTimeout = 10 * time.Second
	srv.ReadTimeout = 30 * time.Second
	srv.Use(healthz.New())
	srv.Use(logger2())
	srv.Handler = h

	slog.Info("static-gateway listening", "addr", srv.Addr, "bucket", bucket)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("listen and serve", "error", err)
		os.Exit(1)
	}
}

// logger2 returns the parapet request logger middleware. Named to avoid shadowing
// the slog logger variable.
func logger2() parapet.Middleware {
	return logger.Stdout()
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt64(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		slog.Warn("invalid int64 env, using default", "key", key, "value", v, "default", def)
		return def
	}
	return n
}
