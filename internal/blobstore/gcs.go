package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"

	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"

	// gcsblob registers the gs:// scheme with gocloud's blob.OpenBucket. It
	// authenticates with Application Default Credentials (Workload Identity in
	// cluster, §6.5). Swapping this blank import for moonrhythm/r2blob's r2://
	// opener moves the gateway to Cloudflare R2 with no other code change (§4.2).
	_ "gocloud.dev/blob/gcsblob"
)

// gcsStore is a read-only Store backed by a gocloud blob bucket (gs:// today).
type gcsStore struct {
	bucket *blob.Bucket
}

// OpenGCS opens the bucket named by bucketName (without scheme) over the gs://
// gocloud provider and returns a read-only Store. Credentials come from ADC. The
// returned closer releases the underlying bucket.
func OpenGCS(ctx context.Context, bucketName string) (Store, func() error, error) {
	if bucketName == "" {
		return nil, nil, errors.New("blobstore: empty bucket name")
	}
	bucket, err := blob.OpenBucket(ctx, "gs://"+bucketName)
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
