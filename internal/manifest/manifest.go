// Package manifest defines the release-manifest types the static-gateway reads
// from object storage (SPEC §5.3) and a loader/canonicalizer for them.
//
// A manifest is the in-memory index of one immutable release: it maps each
// logical file path to the blob that backs it (sha256 + Content-Type + cache
// class) and carries the per-release serving policy (environment, spa, the
// custom notFound document name). Because a release-sha is sha256(canonical
// manifest), a manifest is immutable and self-describing — the gateway needs no
// per-host config push.
package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/deploys-app/static-gateway/internal/cacheheader"
)

// DefaultNotFound is the manifest's notFound document name when the field is
// empty. Hugo sites emit layouts/404.html -> public/404.html.
const DefaultNotFound = "404.html"

// EnvironmentProduction is the manifest environment value that marks a release
// as production (indexable). Any other value (e.g. "pr-7") is a preview and the
// gateway stamps X-Robots-Tag: noindex on its HTML responses (SPEC §4.6).
const EnvironmentProduction = "production"

// File is one entry in the manifest: the blob backing a logical path plus how to
// serve it.
type File struct {
	// Blob is the sha256 (lower-case hex) of the file content; it keys the blob
	// object at sites/<project>/<name>/blobs/<Blob>.
	Blob string `json:"blob"`
	// ContentType is the canonical Content-Type stamped at upload time (§4.5).
	ContentType string `json:"ct"`
	// Cache is the cache class ("immutable" | "html") driving Cache-Control.
	Cache cacheheader.CacheClass `json:"cache"`
}

// Manifest is a parsed release manifest.
type Manifest struct {
	// Release is the release-sha (sha256 of the canonical manifest). It is
	// informational on read; the gateway trusts the storage key it loaded from.
	Release string `json:"release,omitempty"`
	// CreatedAt is an RFC3339 timestamp; surfaced as Last-Modified when present.
	CreatedAt string `json:"createdAt,omitempty"`
	// Environment is "production" or "pr-<n>"; drives noindex (§4.6).
	Environment string `json:"environment"`
	// SPA enables single-page-app fallback to index.html on miss (§4.4).
	SPA bool `json:"spa"`
	// NotFound is the custom 404 document name within the release (e.g. 404.html).
	NotFound string `json:"notFound,omitempty"`
	// Files maps logical path -> backing blob entry.
	Files map[string]File `json:"files"`
}

// IsProduction reports whether this release is the production environment.
// Anything other than exactly "production" is treated as a preview (noindex).
func (m *Manifest) IsProduction() bool {
	return m.Environment == EnvironmentProduction
}

// NotFoundDoc returns the manifest's custom 404 document name, defaulting to
// DefaultNotFound when unset.
func (m *Manifest) NotFoundDoc() string {
	if m.NotFound == "" {
		return DefaultNotFound
	}
	return m.NotFound
}

// Lookup returns the file entry for an exact logical path and whether it exists.
func (m *Manifest) Lookup(p string) (File, bool) {
	f, ok := m.Files[p]
	return f, ok
}

// Load parses a manifest from its JSON bytes and validates the minimum shape the
// gateway needs to serve from it.
func Load(data []byte) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: decode: %w", err)
	}
	if m.Files == nil {
		return nil, errors.New("manifest: missing files")
	}
	for p, f := range m.Files {
		if f.Blob == "" {
			return nil, fmt.Errorf("manifest: file %q has empty blob", p)
		}
	}
	m.Files = canonicalizeFileKeys(m.Files)
	return &m, nil
}

// canonicalizeFileKeys re-keys files to the gateway's canonical logical form: a
// slash-less path (SPEC §5.3). A correct publisher already emits such keys, but
// build-deploy-action historically wrote them with a leading slash ("/index.html").
// Resolution normalizes every request path to a slash-less key (see
// resolve.normalize), so a slash-prefixed manifest misses on every lookup and 404s
// the entire site. Stripping the leading slash here heals those already-published,
// immutable releases without a re-deploy and makes the in-memory index robust to
// that class of publisher drift. When both the canonical and a slash-prefixed
// spelling of a path are present, the already-canonical entry wins.
func canonicalizeFileKeys(files map[string]File) map[string]File {
	needsFix := false
	for k := range files {
		if strings.HasPrefix(k, "/") {
			needsFix = true
			break
		}
	}
	if !needsFix {
		return files
	}
	out := make(map[string]File, len(files))
	// Pass 1: keep already-canonical keys; they take precedence on collision.
	for k, f := range files {
		if !strings.HasPrefix(k, "/") {
			out[k] = f
		}
	}
	// Pass 2: add slash-stripped keys only where the canonical form is absent.
	for k, f := range files {
		if !strings.HasPrefix(k, "/") {
			continue
		}
		ck := strings.TrimLeft(k, "/")
		if _, ok := out[ck]; !ok {
			out[ck] = f
		}
	}
	return out
}

// Canonical serializes a manifest deterministically: object keys sorted, files
// sorted by path, no insignificant whitespace. This is the byte sequence whose
// sha256 is the release-sha (§5.3). The gateway uses it to verify a loaded
// manifest's release-sha when desired; the publish side uses it to mint the sha.
func (m *Manifest) Canonical() ([]byte, error) {
	// Build an ordered representation. encoding/json already emits struct fields
	// in declaration order and sorts map keys, but we additionally normalize by
	// constructing the files map ourselves with sorted keys via json.Marshal of a
	// map (which sorts keys) — so the only nondeterminism to guard is float/int
	// formatting, which we do not use. We also zero the Release field so the
	// canonical form is independent of any release value carried in the body.
	c := canonicalManifest{
		CreatedAt:   m.CreatedAt,
		Environment: m.Environment,
		SPA:         m.SPA,
		NotFound:    m.NotFound,
		Files:       m.Files,
	}
	return json.Marshal(c)
}

// canonicalManifest is the field set and order used to mint the release-sha.
// It deliberately omits Release (the sha cannot depend on itself).
type canonicalManifest struct {
	CreatedAt   string          `json:"createdAt,omitempty"`
	Environment string          `json:"environment"`
	SPA         bool            `json:"spa"`
	NotFound    string          `json:"notFound,omitempty"`
	Files       map[string]File `json:"files"`
}

// ReleaseSHA returns sha256(canonical manifest) as lower-case hex (§5.3).
func (m *Manifest) ReleaseSHA() (string, error) {
	b, err := m.Canonical()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// SortedPaths returns the manifest's logical paths in lexical order. Useful for
// deterministic iteration in tests and tooling.
func (m *Manifest) SortedPaths() []string {
	paths := make([]string, 0, len(m.Files))
	for p := range m.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}
