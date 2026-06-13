// Package blobstore abstracts the object storage the gateway reads from, so the
// serving logic can be tested against an in-memory fake without real GCS.
//
// Storage layout (SPEC §5.2):
//
//	sites/<project>/<name>/releases/<release-sha>   # manifest object (JSON)
//	sites/<project>/<name>/blobs/<sha256>           # one object per unique file
//
// The interface is deliberately tiny — the gateway only ever reads. Writes are
// the publish endpoint's job (a separate, write-scoped identity, §6.5).
package blobstore

import (
	"context"
	"errors"
	"io"
	"path"
	"time"
)

// ErrNotExist is returned by Get/Stat when the key has no object. Implementations
// MUST map their backend's not-found error to this sentinel so callers can branch
// on it with errors.Is.
var ErrNotExist = errors.New("blobstore: object does not exist")

// Attrs carries the object metadata the gateway surfaces on responses. Backends
// populate what they have; zero values mean "unknown" and the gateway falls back
// to manifest-derived values (Content-Type) or omits the header (Last-Modified).
type Attrs struct {
	Size         int64
	ContentType  string
	CacheControl string
	ModTime      time.Time
	ETag         string
}

// Store is the read-only object-storage interface the gateway depends on.
type Store interface {
	// Get opens the object at key for streaming. The caller must Close the reader.
	// Returns ErrNotExist when the object is absent.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Stat returns object metadata without opening the body. Returns ErrNotExist
	// when the object is absent.
	Stat(ctx context.Context, key string) (Attrs, error)
	// Exists reports whether the object at key is present.
	Exists(ctx context.Context, key string) (bool, error)
}

// ManifestKey returns the storage key for a release manifest (§5.2).
func ManifestKey(project, name, releaseSHA string) string {
	return path.Join("sites", project, name, "releases", releaseSHA)
}

// BlobKey returns the storage key for a content-addressed blob (§5.2). blobSHA
// always comes from the manifest, never from the request path, so a crafted URL
// can only ever resolve to a blob the requested release already references (§4.7).
func BlobKey(project, name, blobSHA string) string {
	return path.Join("sites", project, name, "blobs", blobSHA)
}
