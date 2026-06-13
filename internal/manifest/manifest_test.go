package manifest

import (
	"testing"

	"github.com/deploys-app/static-gateway/internal/cacheheader"
)

func TestLoad(t *testing.T) {
	data := []byte(`{
		"release": "abc",
		"createdAt": "2026-06-13T00:00:00Z",
		"environment": "production",
		"spa": false,
		"notFound": "404.html",
		"files": {
			"index.html": { "blob": "h1", "ct": "text/html; charset=utf-8", "cache": "html" },
			"style/main.abc.css": { "blob": "h2", "ct": "text/css; charset=utf-8", "cache": "immutable" }
		}
	}`)

	m, err := Load(data)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !m.IsProduction() {
		t.Errorf("IsProduction = false, want true")
	}
	if m.NotFoundDoc() != "404.html" {
		t.Errorf("NotFoundDoc = %q, want 404.html", m.NotFoundDoc())
	}
	f, ok := m.Lookup("index.html")
	if !ok {
		t.Fatalf("Lookup(index.html) not found")
	}
	if f.Blob != "h1" || f.Cache != cacheheader.ClassHTML {
		t.Errorf("index.html entry = %+v", f)
	}
	if _, ok := m.Lookup("missing"); ok {
		t.Errorf("Lookup(missing) should be false")
	}
}

func TestLoadDefaults(t *testing.T) {
	m, err := Load([]byte(`{"environment":"pr-7","files":{"index.html":{"blob":"h1","ct":"text/html","cache":"html"}}}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.IsProduction() {
		t.Errorf("pr-7 should not be production")
	}
	if m.NotFoundDoc() != DefaultNotFound {
		t.Errorf("NotFoundDoc = %q, want default %q", m.NotFoundDoc(), DefaultNotFound)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"invalid json", `{`},
		{"missing files", `{"environment":"production"}`},
		{"empty blob", `{"environment":"production","files":{"a.html":{"blob":"","ct":"","cache":"html"}}}`},
		{"unknown field", `{"environment":"production","files":{},"bogus":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Load([]byte(tt.data)); err == nil {
				t.Errorf("Load(%s) expected error, got nil", tt.data)
			}
		})
	}
}

func TestCanonicalDeterministic(t *testing.T) {
	// Two manifests with the same content but different file-insertion order and a
	// different (irrelevant) Release field must produce identical canonical bytes
	// and identical release-shas.
	a := &Manifest{
		Release:     "ignore-me",
		Environment: "production",
		Files: map[string]File{
			"index.html": {Blob: "h1", ContentType: "text/html", Cache: cacheheader.ClassHTML},
			"a/b/c.css":  {Blob: "h2", ContentType: "text/css", Cache: cacheheader.ClassImmutable},
			"z.txt":      {Blob: "h3", ContentType: "text/plain", Cache: cacheheader.ClassHTML},
		},
	}
	b := &Manifest{
		Release:     "different-ignored",
		Environment: "production",
		Files: map[string]File{
			"z.txt":      {Blob: "h3", ContentType: "text/plain", Cache: cacheheader.ClassHTML},
			"index.html": {Blob: "h1", ContentType: "text/html", Cache: cacheheader.ClassHTML},
			"a/b/c.css":  {Blob: "h2", ContentType: "text/css", Cache: cacheheader.ClassImmutable},
		},
	}

	ca, err := a.Canonical()
	if err != nil {
		t.Fatalf("a.Canonical: %v", err)
	}
	cb, err := b.Canonical()
	if err != nil {
		t.Fatalf("b.Canonical: %v", err)
	}
	if string(ca) != string(cb) {
		t.Errorf("canonical bytes differ:\n a=%s\n b=%s", ca, cb)
	}

	sa, _ := a.ReleaseSHA()
	sb, _ := b.ReleaseSHA()
	if sa != sb {
		t.Errorf("release shas differ: a=%s b=%s", sa, sb)
	}
	if len(sa) != 64 {
		t.Errorf("release sha not 64 hex chars: %q", sa)
	}
}

func TestReleaseSHAChangesWithEnvironment(t *testing.T) {
	// A production build and a preview build of identical bytes must have
	// different release-shas (§5.3).
	files := map[string]File{
		"index.html": {Blob: "h1", ContentType: "text/html", Cache: cacheheader.ClassHTML},
	}
	prod := &Manifest{Environment: "production", Files: files}
	prev := &Manifest{Environment: "pr-7", Files: files}

	ps, _ := prod.ReleaseSHA()
	vs, _ := prev.ReleaseSHA()
	if ps == vs {
		t.Errorf("production and preview release-shas should differ, both %s", ps)
	}
}

func TestSortedPaths(t *testing.T) {
	m := &Manifest{Files: map[string]File{"b": {Blob: "x"}, "a": {Blob: "y"}, "c": {Blob: "z"}}}
	got := m.SortedPaths()
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SortedPaths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
