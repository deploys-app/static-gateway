// Package resolve implements the net-new Hugo clean-URL resolution the gateway
// performs against a release manifest (SPEC §4.4). It is pure: given a request
// path (already stripped of the /<project>/<name>/<release-sha> prefix) and the
// set of logical paths present in a release, it returns which manifest entry to
// serve, or a not-found signal.
//
// All resolution is a sequence of manifest lookups — the manifest is the
// in-memory index of the release, so negative cases cost no bucket round-trips.
// This package knows nothing about storage, HTTP, or GCS; it operates purely on
// the path set, which makes it exhaustively unit-testable.
package resolve

import (
	"path"
	"strings"
)

// Lookup is the only dependency resolution needs from a manifest: does this
// logical path exist in the release? A *manifest.Manifest satisfies this via a
// trivial adapter, but the package depends only on the function shape so it stays
// pure and decoupled.
type Lookup func(p string) bool

// Kind classifies the outcome of resolution.
type Kind int

const (
	// Hit means Path names an entry that exists in the manifest; serve it with 200.
	Hit Kind = iota
	// NotFound means no entry resolved; the caller serves the release's 404 doc
	// (or the gateway default) with HTTP 404. In SPA mode this kind is never
	// returned for a missing route — SPAFallback is returned instead.
	NotFound
	// SPAFallback means the request missed but the release is a SPA, so the caller
	// serves index.html with HTTP 200 (Path is "index.html").
	SPAFallback
)

// Result is the outcome of resolving a request path against a manifest.
type Result struct {
	Kind Kind
	// Path is the logical manifest path to serve. For Hit it is the matched entry;
	// for SPAFallback it is "index.html". For NotFound it is "".
	Path string
}

// indexHTML is the directory-index document Hugo emits (uglyURLs off).
const indexHTML = "index.html"

// Resolve applies Hugo clean-URL resolution to reqPath using exists to probe the
// manifest. reqPath is the request path with the site prefix already stripped; it
// may or may not have a leading slash. spa selects single-page-app fallback.
//
// Order of resolution (SPEC §4.4):
//  1. root "" or "/" -> index.html
//  2. exact match -> serve it
//  3. trailing slash "/foo/" -> "foo/index.html"
//  4. extensionless "/foo" -> "foo/index.html", then "foo.html"
//  5. miss -> SPAFallback (spa) or NotFound (clean-URL sites)
func Resolve(reqPath string, exists Lookup, spa bool) Result {
	logical := normalize(reqPath)

	// 1. root -> index.html
	if logical == "" {
		if exists(indexHTML) {
			return Result{Kind: Hit, Path: indexHTML}
		}
		return miss(exists, spa)
	}

	// Determine the request's surface shape from the ORIGINAL (pre-normalize) path
	// so a trailing slash is honored: path.Clean strips it, so we look at reqPath.
	trailingSlash := strings.HasSuffix(reqPath, "/")

	if trailingSlash {
		// 3. directory request "/foo/" -> "foo/index.html". A directory request is
		// never served as a bare file, so we do NOT fall back to an exact match of
		// the slash-less form here; if the index is absent it is a miss.
		if cand := joinIndex(logical); exists(cand) {
			return Result{Kind: Hit, Path: cand}
		}
		return miss(exists, spa)
	}

	// 2. exact file match (e.g. "/style/main.css", "/sitemap.xml", "/index.html").
	if exists(logical) {
		return Result{Kind: Hit, Path: logical}
	}

	// 4. extensionless clean URL "/foo" -> "foo/index.html" then "foo.html".
	//    A path WITH an extension that didn't match above is a genuine miss (an
	//    asset request for a file that isn't in the release).
	if path.Ext(logical) == "" {
		if cand := joinIndex(logical); exists(cand) {
			return Result{Kind: Hit, Path: cand}
		}
		if cand := logical + ".html"; exists(cand) {
			return Result{Kind: Hit, Path: cand}
		}
	}

	return miss(exists, spa)
}

// miss returns the not-found outcome, choosing SPA fallback when enabled and an
// index.html exists to fall back to.
func miss(exists Lookup, spa bool) Result {
	if spa && exists(indexHTML) {
		return Result{Kind: SPAFallback, Path: indexHTML}
	}
	return Result{Kind: NotFound}
}

// normalize cleans a request path into a logical manifest key: it strips the
// leading slash, applies path.Clean to collapse "." and resolve any internal
// "..", and yields "" for the root. Traversal that would escape the prefix is
// neutralized here (path.Clean cannot produce a leading "..": rooted cleaning
// drops it); the server layer additionally rejects such inputs defensively.
func normalize(reqPath string) string {
	// Root cleaning: prefix a slash so path.Clean treats it as absolute and any
	// leading ".." is collapsed to root rather than escaping (e.g. "/../x" -> "/x").
	cleaned := path.Clean("/" + reqPath)
	logical := strings.TrimPrefix(cleaned, "/")
	if logical == "." {
		return ""
	}
	return logical
}

// joinIndex returns "<dir>/index.html" for a logical directory path, avoiding a
// double slash when dir is empty.
func joinIndex(dir string) string {
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		return indexHTML
	}
	return dir + "/" + indexHTML
}
