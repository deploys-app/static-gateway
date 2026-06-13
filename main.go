// Command static-gateway is the in-cluster origin that serves immutable static
// site releases directly from object storage (SPEC §4). It is reached through the
// existing edge -> parapet core -> Ingress -> Service path; the Ingress rewrites
// the request to /<project>/<name>/<release-sha>/<rest> via the upstream-path
// annotation, and this service resolves and streams the backing blob.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/moonrhythm/parapet"
	"github.com/moonrhythm/parapet/pkg/healthz"
	"github.com/moonrhythm/parapet/pkg/logger"

	"github.com/deploys-app/static-gateway/internal/blobstore"
	"github.com/deploys-app/static-gateway/internal/server"
)

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
		Store:  store,
		Logger: logger,
	})
	if err != nil {
		slog.Error("build server", "error", err)
		os.Exit(1)
	}

	// parapet.NewBackend(): H2C-enabled, trusts X-Forwarded-* (it sits behind the
	// in-cluster parapet core), matching the dropbox/ipfs-gateway pattern.
	srv := parapet.NewBackend()
	srv.Addr = ":" + port
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
