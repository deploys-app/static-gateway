package blobstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// FakeObject is a stored object in the in-memory Fake store.
type FakeObject struct {
	Data         []byte
	ContentType  string
	CacheControl string
	ModTime      time.Time
	ETag         string
}

// Fake is an in-memory Store for tests. It is safe for concurrent use.
type Fake struct {
	mu       sync.RWMutex
	objects  map[string]FakeObject
	getCount atomic.Int64 // total Get calls, for stampede/coalescing tests
}

// NewFake returns an empty in-memory Store.
func NewFake() *Fake {
	return &Fake{objects: make(map[string]FakeObject)}
}

// GetCount reports how many times Get has been called, letting tests assert that
// caching/coalescing collapsed concurrent loads into a single backend read.
func (f *Fake) GetCount() int64 { return f.getCount.Load() }

// fakeReadCloser is the Fake's reader. It embeds *bytes.Reader so it exposes
// Size() int64 (like gocloud's *blob.Reader), exercising the gateway's
// reader-side Content-Length path against the Fake.
type fakeReadCloser struct{ *bytes.Reader }

func (fakeReadCloser) Close() error { return nil }

// Put stores raw bytes at key with the given Content-Type (other attrs default).
func (f *Fake) Put(key string, data []byte, contentType string) {
	f.PutObject(key, FakeObject{
		Data:        data,
		ContentType: contentType,
		ModTime:     time.Unix(0, 0).UTC(),
	})
}

// PutObject stores a fully-specified object at key.
func (f *Fake) PutObject(key string, obj FakeObject) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(obj.Data))
	copy(cp, obj.Data)
	obj.Data = cp
	f.objects[key] = obj
}

func (f *Fake) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f.getCount.Add(1)
	f.mu.RLock()
	defer f.mu.RUnlock()
	obj, ok := f.objects[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotExist, key)
	}
	return fakeReadCloser{bytes.NewReader(obj.Data)}, nil
}

func (f *Fake) Stat(_ context.Context, key string) (Attrs, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	obj, ok := f.objects[key]
	if !ok {
		return Attrs{}, fmt.Errorf("%w: %s", ErrNotExist, key)
	}
	return Attrs{
		Size:         int64(len(obj.Data)),
		ContentType:  obj.ContentType,
		CacheControl: obj.CacheControl,
		ModTime:      obj.ModTime,
		ETag:         obj.ETag,
	}, nil
}

func (f *Fake) Exists(_ context.Context, key string) (bool, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	_, ok := f.objects[key]
	return ok, nil
}
