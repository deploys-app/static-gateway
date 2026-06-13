// Package contenttype provides the canonical extension -> Content-Type table
// used to stamp blobs at upload time and to echo on serve.
//
// SINGLE SOURCE OF TRUTH CANDIDATE: this table MUST match what the publish side
// (build-deploy-action / the publish endpoint) stamps onto each blob and records
// in the manifest. The gateway echoes the manifest entry's stored Content-Type at
// serve time (see SPEC §4.5), so a divergence here would only ever surface on
// freshly-uploaded releases whose manifest was produced by a different table. To
// avoid that drift, the publish side and the gateway SHOULD ultimately import this
// table from one shared module rather than re-implement it. Until that shared
// module exists, treat this file as the reference table both sides copy.
package contenttype

import (
	"path"
	"strings"
)

// Default is returned for any extension not in the table.
const Default = "application/octet-stream"

// table maps a lower-cased file extension (without the leading dot) to its
// canonical Content-Type. Text types carry charset=utf-8 (SPEC §4.5).
var table = map[string]string{
	// markup / documents
	"html": "text/html; charset=utf-8",
	"htm":  "text/html; charset=utf-8",
	"css":  "text/css; charset=utf-8",
	"js":   "text/javascript; charset=utf-8",
	"mjs":  "text/javascript; charset=utf-8",
	"json": "application/json; charset=utf-8",
	"xml":  "application/xml; charset=utf-8", // sitemap.xml / RSS index.xml
	"txt":  "text/plain; charset=utf-8",
	"map":  "application/json; charset=utf-8", // source maps

	// images
	"svg":  "image/svg+xml",
	"png":  "image/png",
	"jpg":  "image/jpeg",
	"jpeg": "image/jpeg",
	"gif":  "image/gif",
	"webp": "image/webp",
	"avif": "image/avif",
	"ico":  "image/x-icon",

	// fonts
	"woff2": "font/woff2",
	"woff":  "font/woff",
	"ttf":   "font/ttf",
	"otf":   "font/otf",
	"eot":   "application/vnd.ms-fontobject",

	// other
	"wasm": "application/wasm",
	"pdf":  "application/pdf",
}

// FromPath returns the canonical Content-Type for the given logical path based on
// its file extension, falling back to Default for unknown or extensionless paths.
func FromPath(p string) string {
	ext := path.Ext(p)
	if ext == "" {
		return Default
	}
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	if ct, ok := table[ext]; ok {
		return ct
	}
	return Default
}

// FromExt returns the canonical Content-Type for a bare extension, which may be
// given with or without a leading dot (e.g. ".css" or "css"). Unknown extensions
// return Default.
func FromExt(ext string) string {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	if ext == "" {
		return Default
	}
	if ct, ok := table[ext]; ok {
		return ct
	}
	return Default
}
