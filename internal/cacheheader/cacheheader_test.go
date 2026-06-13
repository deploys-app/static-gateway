package cacheheader

import "testing"

func TestCacheControl(t *testing.T) {
	tests := []struct {
		name  string
		class CacheClass
		want  string
	}{
		{"immutable", ClassImmutable, "public, max-age=31536000, immutable"},
		{"html", ClassHTML, "public, max-age=0, must-revalidate"},
		{"unknown falls back to html", CacheClass("weird"), "public, max-age=0, must-revalidate"},
		{"empty falls back to html", CacheClass(""), "public, max-age=0, must-revalidate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CacheControl(tt.class); got != tt.want {
				t.Errorf("CacheControl(%q) = %q, want %q", tt.class, got, tt.want)
			}
		})
	}
}

func TestETag(t *testing.T) {
	got := ETag("abc123")
	want := `"abc123"`
	if got != want {
		t.Errorf("ETag = %q, want %q", got, want)
	}
}

func TestNotModified(t *testing.T) {
	const sha = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	etag := ETag(sha)

	tests := []struct {
		name        string
		ifNoneMatch string
		etag        string
		want        bool
	}{
		{"empty header -> not 304", "", etag, false},
		{"exact match -> 304", etag, etag, true},
		{"wildcard -> 304", "*", etag, true},
		{"different tag -> not 304", `"deadbeef"`, etag, false},
		{"weak prefix on header matches", "W/" + etag, etag, true},
		{"weak prefix on etag matches", etag, "W/" + etag, true},
		{"both weak match", "W/" + etag, "W/" + etag, true},
		{"list with match -> 304", `"deadbeef", ` + etag + `, "cafe"`, etag, true},
		{"list without match -> not 304", `"deadbeef", "cafe"`, etag, false},
		{"whitespace tolerated", "   " + etag + "   ", etag, true},
		{"lowercase weak prefix", "w/" + etag, etag, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NotModified(tt.ifNoneMatch, tt.etag); got != tt.want {
				t.Errorf("NotModified(%q, %q) = %v, want %v", tt.ifNoneMatch, tt.etag, got, tt.want)
			}
		})
	}
}
