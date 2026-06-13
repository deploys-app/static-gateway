package blobstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
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
	mu      sync.RWMutex
	objects map[string]FakeObject
}

// NewFake returns an empty in-memory Store.
func NewFake() *Fake {
	return &Fake{objects: make(map[string]FakeObject)}
}

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
	f.mu.RLock()
	defer f.mu.RUnlock()
	obj, ok := f.objects[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotExist, key)
	}
	return io.NopCloser(bytes.NewReader(obj.Data)), nil
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
