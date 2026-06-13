package blobstore

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestKeys(t *testing.T) {
	if got := ManifestKey("acme", "site", "rel123"); got != "sites/acme/site/releases/rel123" {
		t.Errorf("ManifestKey = %q", got)
	}
	if got := BlobKey("acme", "site", "blobsha"); got != "sites/acme/site/blobs/blobsha" {
		t.Errorf("BlobKey = %q", got)
	}
}

func TestFakeGet(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	f.Put("k", []byte("hello"), "text/plain")

	rc, err := f.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	if string(b) != "hello" {
		t.Errorf("body = %q, want hello", b)
	}

	if _, err := f.Get(ctx, "missing"); !errors.Is(err, ErrNotExist) {
		t.Errorf("Get(missing) err = %v, want ErrNotExist", err)
	}
}

func TestFakeStatAndExists(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	f.Put("k", []byte("hello"), "text/plain")

	a, err := f.Stat(ctx, "k")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if a.Size != 5 || a.ContentType != "text/plain" {
		t.Errorf("attrs = %+v", a)
	}

	if _, err := f.Stat(ctx, "missing"); !errors.Is(err, ErrNotExist) {
		t.Errorf("Stat(missing) err = %v, want ErrNotExist", err)
	}

	ok, _ := f.Exists(ctx, "k")
	if !ok {
		t.Errorf("Exists(k) = false, want true")
	}
	ok, _ = f.Exists(ctx, "missing")
	if ok {
		t.Errorf("Exists(missing) = true, want false")
	}
}

func TestFakeIsolatesData(t *testing.T) {
	// Mutating the caller's slice after Put must not change the stored object.
	f := NewFake()
	data := []byte("abc")
	f.Put("k", data, "text/plain")
	data[0] = 'X'

	rc, _ := f.Get(context.Background(), "k")
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	if string(b) != "abc" {
		t.Errorf("stored data mutated: %q", b)
	}
}

// Fake must satisfy the Store interface.
var _ Store = (*Fake)(nil)
