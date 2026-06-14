package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deploys-app/static-gateway/internal/blobstore"
	"github.com/deploys-app/static-gateway/internal/manifest"
)

const (
	testProject = "acme"
	testName    = "site"
	testRelease = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// fixture seeds a Fake store with a manifest + its blobs and returns a Handler.
// Each entry's blob content is the entry path's bytes (so we can assert bodies).
func fixture(t *testing.T, m *manifest.Manifest, blobs map[string]string) *Handler {
	t.Helper()
	store := blobstore.NewFake()

	mb, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	store.Put(blobstore.ManifestKey(testProject, testName, testRelease), mb, "application/json")

	for sha, body := range blobs {
		store.Put(blobstore.BlobKey(testProject, testName, sha), []byte(body), "")
	}

	h, err := New(Config{Store: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func do(h *Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://gw"+path, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func prefix(rest string) string {
	return "/" + testProject + "/" + testName + "/" + testRelease + rest
}

// hugoManifest is a small but realistic Hugo release.
func hugoManifest(env string) (*manifest.Manifest, map[string]string) {
	m := &manifest.Manifest{
		Release:     testRelease,
		CreatedAt:   "2026-06-13T10:00:00Z",
		Environment: env,
		SPA:         false,
		NotFound:    "404.html",
		Files: map[string]manifest.File{
			"index.html": {Blob: "b_index", ContentType: "text/html; charset=utf-8", Cache: "html"},
			"404.html":   {Blob: "b_404", ContentType: "text/html; charset=utf-8", Cache: "html"},
			"getting-started/introduction/index.html": {Blob: "b_intro", ContentType: "text/html; charset=utf-8", Cache: "html"},
			"style/main.4f3a9b.css":                   {Blob: "b_css", ContentType: "text/css; charset=utf-8", Cache: "immutable"},
			"sitemap.xml":                             {Blob: "b_sitemap", ContentType: "application/xml; charset=utf-8", Cache: "html"},
		},
	}
	blobs := map[string]string{
		"b_index":   "<html>home</html>",
		"b_404":     "<html>custom not found</html>",
		"b_intro":   "<html>introduction</html>",
		"b_css":     "body{color:red}",
		"b_sitemap": "<urlset/>",
	}
	return m, blobs
}

func TestServeCleanURLHit(t *testing.T) {
	m, blobs := hugoManifest("production")
	h := fixture(t, m, blobs)

	w := do(h, http.MethodGet, prefix("/getting-started/introduction/"), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != "<html>introduction</html>" {
		t.Errorf("body = %q", got)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestServeRootIndex(t *testing.T) {
	m, blobs := hugoManifest("production")
	h := fixture(t, m, blobs)

	w := do(h, http.MethodGet, prefix("/"), nil)
	if w.Code != http.StatusOK || w.Body.String() != "<html>home</html>" {
		t.Errorf("root: code=%d body=%q", w.Code, w.Body.String())
	}
}

// TestServeLeadingSlashManifestHeals proves the gateway serves a release whose
// manifest was published with leading-slash keys (the historical
// build-deploy-action defect) — every route, not just root — instead of 404ing the
// whole site. This is the defense-in-depth that heals already-published immutable
// releases without a re-deploy.
func TestServeLeadingSlashManifestHeals(t *testing.T) {
	m := &manifest.Manifest{
		Release:     testRelease,
		Environment: "production",
		NotFound:    "404.html",
		Files: map[string]manifest.File{
			"/index.html":                 {Blob: "b_index", ContentType: "text/html; charset=utf-8", Cache: "html"},
			"/404.html":                   {Blob: "b_404", ContentType: "text/html; charset=utf-8", Cache: "html"},
			"/getting-started/index.html": {Blob: "b_gs", ContentType: "text/html; charset=utf-8", Cache: "html"},
			"/style/main.4f3a9b.css":      {Blob: "b_css", ContentType: "text/css; charset=utf-8", Cache: "immutable"},
		},
	}
	blobs := map[string]string{
		"b_index": "<html>home</html>",
		"b_404":   "<html>custom not found</html>",
		"b_gs":    "<html>getting started</html>",
		"b_css":   "body{color:red}",
	}
	h := fixture(t, m, blobs)

	// Root, a clean-URL sub-page, and a fingerprinted asset all resolve to 200.
	for _, tc := range []struct{ path, body string }{
		{"/", "<html>home</html>"},
		{"/getting-started/", "<html>getting started</html>"},
		{"/style/main.4f3a9b.css", "body{color:red}"},
	} {
		w := do(h, http.MethodGet, prefix(tc.path), nil)
		if w.Code != http.StatusOK || w.Body.String() != tc.body {
			t.Errorf("%s: code=%d body=%q, want 200 %q", tc.path, w.Code, w.Body.String(), tc.body)
		}
	}

	// A genuine miss still serves the (slash-published) custom 404 doc with 404.
	w := do(h, http.MethodGet, prefix("/nope/"), nil)
	if w.Code != http.StatusNotFound || w.Body.String() != "<html>custom not found</html>" {
		t.Errorf("miss: code=%d body=%q, want 404 custom doc", w.Code, w.Body.String())
	}
}

func TestServeImmutableVsHTMLCacheHeaders(t *testing.T) {
	m, blobs := hugoManifest("production")
	h := fixture(t, m, blobs)

	// Immutable asset.
	w := do(h, http.MethodGet, prefix("/style/main.4f3a9b.css"), nil)
	if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("css Cache-Control = %q", cc)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Errorf("css Content-Type = %q", ct)
	}

	// HTML doc.
	w = do(h, http.MethodGet, prefix("/"), nil)
	if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=0, must-revalidate" {
		t.Errorf("html Cache-Control = %q", cc)
	}
}

func TestServeETagAnd304(t *testing.T) {
	m, blobs := hugoManifest("production")
	h := fixture(t, m, blobs)

	w := do(h, http.MethodGet, prefix("/"), nil)
	etag := w.Header().Get("ETag")
	if etag != `"b_index"` {
		t.Fatalf("ETag = %q, want \"b_index\"", etag)
	}

	// Conditional GET with the same validator -> 304, no body.
	w = do(h, http.MethodGet, prefix("/"), map[string]string{"If-None-Match": etag})
	if w.Code != http.StatusNotModified {
		t.Fatalf("conditional code = %d, want 304", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("304 carried a body: %q", w.Body.String())
	}
	if w.Header().Get("ETag") != etag {
		t.Errorf("304 missing ETag")
	}

	// Mismatching validator -> 200 with body.
	w = do(h, http.MethodGet, prefix("/"), map[string]string{"If-None-Match": `"stale"`})
	if w.Code != http.StatusOK || w.Body.Len() == 0 {
		t.Errorf("mismatched If-None-Match: code=%d bodylen=%d", w.Code, w.Body.Len())
	}
}

func TestServeIfModifiedSince(t *testing.T) {
	m, blobs := hugoManifest("production")
	h := fixture(t, m, blobs)

	// createdAt is 2026-06-13T10:00:00Z; a later IMS -> 304.
	w := do(h, http.MethodGet, prefix("/"), map[string]string{
		"If-Modified-Since": "Sat, 13 Jun 2026 11:00:00 GMT",
	})
	if w.Code != http.StatusNotModified {
		t.Errorf("IMS later: code = %d, want 304", w.Code)
	}

	// An earlier IMS -> 200.
	w = do(h, http.MethodGet, prefix("/"), map[string]string{
		"If-Modified-Since": "Sat, 13 Jun 2026 09:00:00 GMT",
	})
	if w.Code != http.StatusOK {
		t.Errorf("IMS earlier: code = %d, want 200", w.Code)
	}
}

func TestServeLastModified(t *testing.T) {
	m, blobs := hugoManifest("production")
	h := fixture(t, m, blobs)

	w := do(h, http.MethodGet, prefix("/"), nil)
	if lm := w.Header().Get("Last-Modified"); lm != "Sat, 13 Jun 2026 10:00:00 GMT" {
		t.Errorf("Last-Modified = %q", lm)
	}
}

func TestServeCustom404(t *testing.T) {
	m, blobs := hugoManifest("production")
	h := fixture(t, m, blobs)

	w := do(h, http.MethodGet, prefix("/does-not-exist/"), nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
	if got := w.Body.String(); got != "<html>custom not found</html>" {
		t.Errorf("custom 404 body = %q", got)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("404 Content-Type = %q", ct)
	}
}

func TestServeDefault404(t *testing.T) {
	// A release without a 404.html (docs today) falls back to the built-in page.
	m := &manifest.Manifest{
		Release:     testRelease,
		Environment: "production",
		Files: map[string]manifest.File{
			"index.html": {Blob: "b_index", ContentType: "text/html; charset=utf-8", Cache: "html"},
		},
	}
	h := fixture(t, m, map[string]string{"b_index": "<html>home</html>"})

	w := do(h, http.MethodGet, prefix("/missing/"), nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "404") {
		t.Errorf("default 404 body lacks '404': %q", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("default 404 Content-Type = %q", ct)
	}
}

func TestServeNoindexOnPreview(t *testing.T) {
	m, blobs := hugoManifest("pr-7")
	h := fixture(t, m, blobs)

	// HTML response on a preview -> noindex.
	w := do(h, http.MethodGet, prefix("/"), nil)
	if got := w.Header().Get("X-Robots-Tag"); got != "noindex" {
		t.Errorf("preview HTML X-Robots-Tag = %q, want noindex", got)
	}

	// Non-HTML (css) on a preview -> NO noindex.
	w = do(h, http.MethodGet, prefix("/style/main.4f3a9b.css"), nil)
	if got := w.Header().Get("X-Robots-Tag"); got != "" {
		t.Errorf("preview css X-Robots-Tag = %q, want empty", got)
	}
}

func TestServeNoNoindexOnProduction(t *testing.T) {
	m, blobs := hugoManifest("production")
	h := fixture(t, m, blobs)

	w := do(h, http.MethodGet, prefix("/"), nil)
	if got := w.Header().Get("X-Robots-Tag"); got != "" {
		t.Errorf("production X-Robots-Tag = %q, want empty", got)
	}
}

func TestServeSecurityHeaders(t *testing.T) {
	m, blobs := hugoManifest("production")
	h := fixture(t, m, blobs)

	w := do(h, http.MethodGet, prefix("/"), nil)
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := w.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q", got)
	}
}

func TestServeSPAFallback(t *testing.T) {
	m := &manifest.Manifest{
		Release:     testRelease,
		Environment: "production",
		SPA:         true,
		Files: map[string]manifest.File{
			"index.html":         {Blob: "b_index", ContentType: "text/html; charset=utf-8", Cache: "html"},
			"assets/app.4f3a.js": {Blob: "b_js", ContentType: "text/javascript; charset=utf-8", Cache: "immutable"},
		},
	}
	h := fixture(t, m, map[string]string{"b_index": "<html>spa</html>", "b_js": "console.log(1)"})

	// Unknown client route -> index.html with 200.
	w := do(h, http.MethodGet, prefix("/dashboard/settings"), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("spa route code = %d, want 200", w.Code)
	}
	if w.Body.String() != "<html>spa</html>" {
		t.Errorf("spa route body = %q", w.Body.String())
	}
}

func TestServeHEAD(t *testing.T) {
	m, blobs := hugoManifest("production")
	h := fixture(t, m, blobs)

	w := do(h, http.MethodHead, prefix("/"), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("HEAD code = %d, want 200", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("HEAD carried a body: %q", w.Body.String())
	}
	if w.Header().Get("ETag") == "" || w.Header().Get("Content-Type") == "" {
		t.Errorf("HEAD missing headers: %+v", w.Header())
	}
}

func TestServeMethodNotAllowed(t *testing.T) {
	m, blobs := hugoManifest("production")
	h := fixture(t, m, blobs)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		w := do(h, method, prefix("/"), nil)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s code = %d, want 405", method, w.Code)
		}
		if allow := w.Header().Get("Allow"); allow != "GET, HEAD" {
			t.Errorf("%s Allow = %q", method, allow)
		}
	}
}

func TestServePathTraversalConfined(t *testing.T) {
	m, blobs := hugoManifest("production")
	h := fixture(t, m, blobs)

	// A traversal attempt collapses within the prefix and can only resolve to a
	// blob the manifest references — never escapes to another site's prefix.
	cases := []string{
		"/../../../../etc/passwd",
		"/getting-started/../../../../../../sitemap.xml",
		"/%2e%2e/%2e%2e/index.html",
	}
	for _, c := range cases {
		w := do(h, http.MethodGet, prefix(c), nil)
		// Whatever it resolves to, it must be one of THIS manifest's entries or a
		// 404 — never a 200 serving foreign content. We assert it never 500s and
		// any 200 body is a known blob.
		if w.Code == http.StatusInternalServerError {
			t.Errorf("traversal %q caused 500", c)
		}
		if w.Code == http.StatusOK {
			body := w.Body.String()
			known := body == "<html>home</html>" || body == "<urlset/>" ||
				body == "<html>introduction</html>" || body == "body{color:red}"
			if !known {
				t.Errorf("traversal %q served unexpected body %q", c, body)
			}
		}
	}
}

func TestServeBadPrefix(t *testing.T) {
	m, blobs := hugoManifest("production")
	h := fixture(t, m, blobs)

	for _, p := range []string{"/", "/onlyone", "/two/segments"} {
		w := do(h, http.MethodGet, p, nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("path %q code = %d, want 400", p, w.Code)
		}
	}
}

func TestServeReleaseNotFound(t *testing.T) {
	store := blobstore.NewFake() // empty: no manifest
	h, _ := New(Config{Store: store})

	w := do(h, http.MethodGet, prefix("/"), nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing release code = %d, want 404", w.Code)
	}
}

func TestManifestCachedAcrossRequests(t *testing.T) {
	m, blobs := hugoManifest("production")
	h := fixture(t, m, blobs)

	_ = do(h, http.MethodGet, prefix("/"), nil)
	_ = do(h, http.MethodGet, prefix("/sitemap.xml"), nil)
	if n := h.cache.len(); n != 1 {
		t.Errorf("cache len = %d, want 1 (manifest cached by release)", n)
	}
}

func TestDanglingBlobReturns404(t *testing.T) {
	// Manifest references a blob that isn't in storage (integrity violation).
	m := &manifest.Manifest{
		Release:     testRelease,
		Environment: "production",
		Files: map[string]manifest.File{
			"index.html": {Blob: "missing_blob", ContentType: "text/html; charset=utf-8", Cache: "html"},
		},
	}
	store := blobstore.NewFake()
	mb, _ := json.Marshal(m)
	store.Put(blobstore.ManifestKey(testProject, testName, testRelease), mb, "application/json")
	h, _ := New(Config{Store: store})

	w := do(h, http.MethodGet, prefix("/"), nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("dangling blob code = %d, want 404", w.Code)
	}
}

func TestParseSite(t *testing.T) {
	tests := []struct {
		path    string
		ok      bool
		project string
		name    string
		release string
		rest    string
	}{
		{"/p/n/r/a/b", true, "p", "n", "r", "/a/b"},
		{"/p/n/r/", true, "p", "n", "r", "/"},
		{"/p/n/r", true, "p", "n", "r", "/"},
		{"/p/n", false, "", "", "", ""},
		{"/", false, "", "", "", ""},
		{"//n/r/x", false, "", "", "", ""},
	}
	for _, tt := range tests {
		s, ok := parseSite(tt.path)
		if ok != tt.ok {
			t.Errorf("parseSite(%q) ok = %v, want %v", tt.path, ok, tt.ok)
			continue
		}
		if !ok {
			continue
		}
		if s.project != tt.project || s.name != tt.name || s.releaseSHA != tt.release || s.rest != tt.rest {
			t.Errorf("parseSite(%q) = %+v", tt.path, s)
		}
	}
}

// drainBody is a tiny helper to ensure bodies are consumed in streaming tests.
func drainBody(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	b, err := io.ReadAll(w.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

var _ = drainBody
