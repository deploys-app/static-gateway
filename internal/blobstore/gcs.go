package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"gocloud.dev/blob"
	// gcsblob authenticates with Application Default Credentials (Workload Identity
	// in cluster, §6.5). Swapping gcsblob.OpenBucket for moonrhythm/r2blob's r2://
	// opener moves the gateway to Cloudflare R2 with no other code change (§4.2).
	"gocloud.dev/blob/gcsblob"
	"gocloud.dev/gcerrors"
	"gocloud.dev/gcp"
)

// gcsStore is a read-only Store backed by a gocloud blob bucket (gs:// today).
type gcsStore struct {
	bucket *blob.Bucket
}

// OpenGCS opens the bucket named by bucketName (without scheme) and returns a
// read-only Store. Credentials come from ADC. The returned closer releases the
// underlying bucket.
//
// It does NOT use blob.OpenBucket("gs://...") because that wires the GCS client
// to http.DefaultTransport, which keeps only DefaultMaxIdleConnsPerHost (2) idle
// connections. Every read targets one host (storage.googleapis.com), so under any
// concurrency the gateway constantly opens fresh TLS connections — adding tens of
// ms of handshake latency to a large fraction of reads. We build the client with
// a transport whose connection pool is sized for a read-heavy origin instead.
func OpenGCS(ctx context.Context, bucketName string) (Store, func() error, error) {
	if bucketName == "" {
		return nil, nil, errors.New("blobstore: empty bucket name")
	}
	creds, err := gcp.DefaultCredentials(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("blobstore: default credentials: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 200
	transport.MaxIdleConnsPerHost = 100
	transport.IdleConnTimeout = 120 * time.Second
	client, err := gcp.NewHTTPClient(transport, creds.TokenSource)
	if err != nil {
		return nil, nil, fmt.Errorf("blobstore: build http client: %w", err)
	}
	bucket, err := gcsblob.OpenBucket(ctx, client, bucketName, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("blobstore: open bucket %q: %w", bucketName, err)
	}
	s := &gcsStore{bucket: bucket}
	return s, bucket.Close, nil
}

func (s *gcsStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	r, err := s.bucket.NewReader(ctx, key, nil)
	if err != nil {
		return nil, mapErr(err, key)
	}
	return r, nil
}

func (s *gcsStore) Stat(ctx context.Context, key string) (Attrs, error) {
	a, err := s.bucket.Attributes(ctx, key)
	if err != nil {
		return Attrs{}, mapErr(err, key)
	}
	return Attrs{
		Size:         a.Size,
		ContentType:  a.ContentType,
		CacheControl: a.CacheControl,
		ModTime:      a.ModTime,
		ETag:         a.ETag,
	}, nil
}

func (s *gcsStore) Exists(ctx context.Context, key string) (bool, error) {
	ok, err := s.bucket.Exists(ctx, key)
	if err != nil {
		return false, mapErr(err, key)
	}
	return ok, nil
}

// mapErr converts a gocloud not-found into ErrNotExist and wraps everything else.
func mapErr(err error, key string) error {
	if gcerrors.Code(err) == gcerrors.NotFound {
		return fmt.Errorf("%w: %s", ErrNotExist, key)
	}
	return fmt.Errorf("blobstore: %q: %w", key, err)
}
