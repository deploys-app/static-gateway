// Package server implements the static-gateway HTTP handler: it parses the
// Ingress-rewritten /<project>/<name>/<release-sha>/<rest> path, loads and caches
// the release manifest, resolves the clean URL against it, and streams the backing
// blob with the correct Content-Type, Cache-Control, ETag/Last-Modified, security
// headers, and (for previews) X-Robots-Tag: noindex (SPEC §4).
//
// The gateway is stateless: storage is the source of truth and the manifest cache
// is rebuildable, so replicas scale horizontally without coordination (§4.7).
package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/deploys-app/static-gateway/internal/blobstore"
	"github.com/deploys-app/static-gateway/internal/cacheheader"
	"github.com/deploys-app/static-gateway/internal/manifest"
	"github.com/deploys-app/static-gateway/internal/metrics"
	"github.com/deploys-app/static-gateway/internal/resolve"
)

// DefaultManifestCacheCap is the default LRU capacity for parsed manifests.
const DefaultManifestCacheCap = 1024

// manifestLoadTimeout bounds a single manifest read+parse. It is the only
// deadline on that work once a load is detached from its triggering request (so
// one disconnecting caller cannot fail the others coalesced behind it), and the
// backend sets no server write deadline.
const manifestLoadTimeout = 10 * time.Second

// copyBufSize is the per-stream buffer used to copy a blob to the response. It is
// larger than net/http's default 32 KiB to cut syscalls on bigger assets while
// staying small enough to pool cheaply.
const copyBufSize = 64 << 10

// copyBufPool recycles blob-streaming buffers. Pooling pointers avoids an
// allocation per Get, and using our own buffer (via readerOnly, below) sidesteps
// gocloud's blob.Reader.WriteTo, which allocates a fresh 1 MiB buffer per call on
// the H2C path where the ResponseWriter is not an io.ReaderFrom.
var copyBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, copyBufSize)
		return &b
	},
}

// readerOnly hides any io.WriterTo a reader implements, so io.CopyBuffer uses the
// buffer we pass instead of the reader's own WriteTo path.
type readerOnly struct{ io.Reader }

// defaultNotFoundBody is the gateway's built-in 404 page, served when a release
// has no custom notFound document (e.g. docs today, §4.4).
const defaultNotFoundBody = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>404 Not Found</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>body{font-family:system-ui,sans-serif;margin:0;display:grid;place-items:center;min-height:100vh;color:#1a1a1a}main{text-align:center}h1{font-size:3rem;margin:0}p{color:#666}</style></head>
<body><main><h1>404</h1><p>The page you requested was not found.</p></main></body>
</html>
`

// Config configures a Handler.
type Config struct {
	Store            blobstore.Store
	ManifestCacheCap int
	// ManifestCacheBytes bounds the manifest cache by approximate retained bytes
	// in addition to entry count. Zero (the default) means no byte bound; the
	// binary wires this from MANIFEST_CACHE_BYTES so memory can be tuned per
	// deployment without a code change.
	ManifestCacheBytes int64
	Logger             *slog.Logger
}

// Handler is the static-gateway http.Handler.
type Handler struct {
	store   blobstore.Store
	cache   *manifestCache
	mfGroup singleflight.Group // collapses concurrent first-loads of one manifest
	logger  *slog.Logger
}

// New builds a Handler from cfg. Store is required.
func New(cfg Config) (*Handler, error) {
	if cfg.Store == nil {
		return nil, errors.New("server: nil store")
	}
	cap := cfg.ManifestCacheCap
	if cap <= 0 {
		cap = DefaultManifestCacheCap
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		store:  cfg.Store,
		cache:  newManifestCache(cap, cfg.ManifestCacheBytes),
		logger: logger,
	}, nil
}

// site identifies a release prefix parsed from the request path.
type site struct {
	project    string
	name       string
	releaseSHA string
	rest       string // the remaining request path (with leading slash), pre-clean
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Method gate: only GET and HEAD are meaningful for a read-only origin.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s, ok := parseSite(r.URL.Path)
	if !ok {
		// The Ingress always rewrites a valid 3-segment prefix; a request that
		// reaches the gateway without one is either a probe or misconfiguration.
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Count every request to a resolved site (the collector sums this per
	// project+name to attribute request volume to the Static deployment).
	metrics.Requests.WithLabelValues(s.project, s.name).Inc()

	m, err := h.loadManifest(r.Context(), s)
	if err != nil {
		if errors.Is(err, blobstore.ErrNotExist) {
			// The release prefix names no manifest — nothing to serve.
			http.Error(w, "release not found", http.StatusNotFound)
			return
		}
		h.logger.Error("load manifest", "project", s.project, "name", s.name, "release", s.releaseSHA, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	res := resolve.Resolve(s.rest, func(p string) bool {
		_, ok := m.Lookup(p)
		return ok
	}, m.SPA)

	switch res.Kind {
	case resolve.Hit:
		entry, _ := m.Lookup(res.Path)
		h.serveBlob(w, r, s, m, entry, http.StatusOK)
	case resolve.SPAFallback:
		entry, _ := m.Lookup(res.Path)
		h.serveBlob(w, r, s, m, entry, http.StatusOK)
	case resolve.NotFound:
		h.serveNotFound(w, r, s, m)
	}
}

// loadManifest returns the parsed manifest for s, caching it by storage key.
//
// A freshly-published release is cold at every edge POP at once, so its first
// requests fan in to this origin on one uncached manifest key during the brief
// window before the LRU is populated. singleflight collapses that burst into a
// single GCS read + parse per replica. The release-sha is immutable, so every
// waiter that receives the leader's result gets the one true manifest for the key
// — coalescing can never serve stale content.
func (h *Handler) loadManifest(ctx context.Context, s site) (*manifest.Manifest, error) {
	key := blobstore.ManifestKey(s.project, s.name, s.releaseSHA)
	if m, ok := h.cache.get(key); ok {
		return m, nil
	}
	v, err, _ := h.mfGroup.Do(key, func() (any, error) {
		// A concurrent leader may have populated the cache between our miss above
		// and acquiring this call slot.
		if m, ok := h.cache.get(key); ok {
			return m, nil
		}
		// Detach from the triggering request's context so one caller disconnecting
		// does not cancel the shared load and fail every waiter behind it; bound it
		// with a dedicated timeout instead.
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), manifestLoadTimeout)
		defer cancel()
		rc, err := h.store.Get(loadCtx, key)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			return nil, err
		}
		m, err := manifest.Load(data)
		if err != nil {
			return nil, err
		}
		h.cache.add(key, m)
		return m, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*manifest.Manifest), nil
}

// serveBlob streams the blob backing entry with status, stamping Content-Type,
// Cache-Control, ETag, Last-Modified, security headers, and (for previews)
// noindex. It honors conditional requests with a 304 and handles HEAD.
func (h *Handler) serveBlob(w http.ResponseWriter, r *http.Request, s site, m *manifest.Manifest, entry manifest.File, status int) {
	etag := cacheheader.ETag(entry.Blob)
	hdr := w.Header()

	// Content type from the manifest entry (echoes the upload-time stamp, §4.5).
	if entry.ContentType != "" {
		hdr.Set("Content-Type", entry.ContentType)
	}
	hdr.Set("Cache-Control", cacheheader.CacheControl(entry.Cache))
	hdr.Set("ETag", etag)

	isHTML := strings.HasPrefix(entry.ContentType, "text/html")
	h.setSecurityHeaders(hdr, m, isHTML)

	// Last-Modified from the manifest createdAt, parsed and formatted once at load
	// (a release is immutable, so all of its blobs share the release's creation
	// time as a sane validator, §4.6).
	modTime := m.ModTime()
	if lm := m.LastModified(); lm != "" {
		hdr.Set("Last-Modified", lm)
	}

	// Conditional request: 304 when the validator matches. A successful 304 must
	// not carry a body; ETag/Cache-Control are already set above.
	if status == http.StatusOK && conditionalNotModified(r, etag, modTime) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	if r.Method == http.MethodHead {
		w.WriteHeader(status)
		return
	}

	key := blobstore.BlobKey(s.project, s.name, entry.Blob)
	rc, err := h.store.Get(r.Context(), key)
	if err != nil {
		if errors.Is(err, blobstore.ErrNotExist) {
			// Manifest references a blob that isn't in storage. The publish path
			// verifies blobs exist before a manifest goes live (§6.3), so this is
			// an integrity violation, not a routine miss.
			h.logger.Error("dangling blob", "key", key, "release", s.releaseSHA)
			http.Error(w, "blob not found", http.StatusNotFound)
			return
		}
		h.logger.Error("read blob", "key", key, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	// Content-Length lets the edge and clients size the response and avoids chunked
	// transfer encoding on the HTTP/1.1 hop. gocloud's reader exposes the size from
	// already-fetched attributes, so this costs no extra round-trip; a Store whose
	// reader doesn't expose Size() simply omits the header.
	if sz, ok := rc.(interface{ Size() int64 }); ok {
		if n := sz.Size(); n >= 0 {
			hdr.Set("Content-Length", strconv.FormatInt(n, 10))
		}
	}

	w.WriteHeader(status)

	// Stream through a pooled buffer. readerOnly hides the reader's WriteTo so the
	// copy uses our buffer instead of gocloud's per-call 1 MiB allocation.
	buf := copyBufPool.Get().(*[]byte)
	_, err = io.CopyBuffer(w, readerOnly{rc}, *buf)
	copyBufPool.Put(buf)
	if err != nil {
		// Client disconnects are normal; log at debug to avoid noise.
		h.logger.Debug("stream blob", "key", key, "error", err)
	}
}

// serveNotFound serves the release's custom notFound document with HTTP 404, or
// the gateway's built-in default 404 when the release has none (§4.4).
func (h *Handler) serveNotFound(w http.ResponseWriter, r *http.Request, s site, m *manifest.Manifest) {
	doc := m.NotFoundDoc()
	if entry, ok := m.Lookup(doc); ok {
		h.serveBlob(w, r, s, m, entry, http.StatusNotFound)
		return
	}
	// Built-in default 404.
	hdr := w.Header()
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	hdr.Set("Cache-Control", cacheheader.CacheControl(cacheheader.ClassHTML))
	h.setSecurityHeaders(hdr, m, true)
	w.WriteHeader(http.StatusNotFound)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.WriteString(w, defaultNotFoundBody)
}

// setSecurityHeaders reproduces the website's _headers intent that a non-Cloudflare
// origin would otherwise drop (§4.6). noindex is applied to HTML responses of any
// non-production (preview) release.
func (h *Handler) setSecurityHeaders(hdr http.Header, m *manifest.Manifest, isHTML bool) {
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("X-Frame-Options", "DENY")
	if isHTML && !m.IsProduction() {
		hdr.Set("X-Robots-Tag", "noindex")
	}
}

// parseSite splits an Ingress-rewritten path /<project>/<name>/<release-sha>/<rest>
// into its components. rest retains a leading slash (or is "/") and is NOT cleaned
// here — resolution's normalize step confines it. Returns ok=false when fewer than
// three prefix segments are present.
func parseSite(p string) (site, bool) {
	trimmed := strings.TrimPrefix(p, "/")
	// SplitN into the three prefix segments plus the remainder.
	parts := strings.SplitN(trimmed, "/", 4)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return site{}, false
	}
	rest := "/"
	if len(parts) == 4 {
		rest = "/" + parts[3]
	}
	return site{
		project:    parts[0],
		name:       parts[1],
		releaseSHA: parts[2],
		rest:       rest,
	}, true
}

// conditionalNotModified reports whether a 304 should be returned, honoring
// If-None-Match (preferred) then If-Modified-Since (§4.6).
func conditionalNotModified(r *http.Request, etag string, modTime time.Time) bool {
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		return cacheheader.NotModified(inm, etag)
	}
	if ims := r.Header.Get("If-Modified-Since"); ims != "" && !modTime.IsZero() {
		if t, err := http.ParseTime(ims); err == nil {
			// Truncate to second precision (HTTP-date granularity) before compare.
			return !modTime.Truncate(time.Second).After(t)
		}
	}
	return false
}
