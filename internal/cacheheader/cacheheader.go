// Package cacheheader maps a release's cache class to a Cache-Control value and
// implements the strong-validator (ETag) / conditional-request logic the gateway
// uses to turn must-revalidate HTML requests into cheap 304s (SPEC §4.6).
package cacheheader

import "strings"

// CacheClass identifies how a blob should be cached at the edge.
type CacheClass string

const (
	// ClassImmutable is for fingerprinted/hashed assets whose URL changes when
	// their content changes (Hugo style/main.<hash>.css, fingerprinted images).
	ClassImmutable CacheClass = "immutable"

	// ClassHTML is for HTML / clean-URL documents / index.html / search-index.json
	// / sitemap.xml / RSS — content that lives at a stable URL and must always be
	// revalidated so a pointer swap is visible on the next request.
	ClassHTML CacheClass = "html"
)

const (
	cacheControlImmutable = "public, max-age=31536000, immutable"
	cacheControlHTML      = "public, max-age=0, must-revalidate"
)

// CacheControl returns the Cache-Control header value for a cache class. Any
// unrecognized class is treated as HTML (the conservative, always-revalidate
// choice) so a malformed manifest never accidentally caches dynamic content
// forever.
func CacheControl(c CacheClass) string {
	switch c {
	case ClassImmutable:
		return cacheControlImmutable
	case ClassHTML:
		return cacheControlHTML
	default:
		return cacheControlHTML
	}
}

// ETag returns the strong validator for a blob, derived from its sha256:
// a quoted lower-cased hex digest, e.g. `"deadbeef..."`. The sha is already
// known from the manifest, so this needs no blob read.
func ETag(sha256 string) string {
	return `"` + sha256 + `"`
}

// NotModified reports whether a request carrying the given If-None-Match header
// value should receive a 304 Not Modified for a response whose validator is etag.
//
// It implements the subset of RFC 9110 §13.1.2 that matters here:
//   - If-None-Match: * matches any existing representation -> 304.
//   - a comma-separated list of entity-tags matches if any element equals the
//     ETag. Comparison is weak (a leading W/ on either side is ignored), which is
//     the correct comparison for If-None-Match.
//
// etag is expected to be the already-quoted value returned by ETag. An empty
// ifNoneMatch never matches.
func NotModified(ifNoneMatch, etag string) bool {
	ifNoneMatch = strings.TrimSpace(ifNoneMatch)
	if ifNoneMatch == "" {
		return false
	}
	if ifNoneMatch == "*" {
		return true
	}
	target := normalizeETag(etag)
	for candidate := range strings.SplitSeq(ifNoneMatch, ",") {
		if normalizeETag(candidate) == target {
			return true
		}
	}
	return false
}

// normalizeETag strips surrounding whitespace and a weak-validator W/ prefix so
// two tags can be compared with weak semantics.
func normalizeETag(tag string) string {
	tag = strings.TrimSpace(tag)
	tag = strings.TrimPrefix(tag, "W/")
	tag = strings.TrimPrefix(tag, "w/")
	return strings.TrimSpace(tag)
}
