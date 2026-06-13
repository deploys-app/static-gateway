package resolve

import "testing"

// hugoSite models the logical paths a real Hugo `public/` tree produces
// (uglyURLs off): the root index, directory-index pages, fingerprinted assets,
// SEO artifacts, a client-side search index, and a custom 404. This is the shape
// deploys-app/website and deploys-app/docs ship.
var hugoSite = setOf(
	"index.html",
	"404.html",
	"sitemap.xml",
	"index.xml", // RSS
	"robots.txt",
	"search-index.json",
	"style/main.4f3a9b.css",
	"js/app.8c1d2e.js",
	"img/logo.svg",
	"fonts/inter.woff2",
	"getting-started/index.html",
	"getting-started/introduction/index.html",
	"getting-started/installation/index.html",
	"blog/index.html",
	"blog/2026/my-post/index.html",
	"about/index.html",
)

// uglyURLsSite models a Hugo site with uglyURLs on, where pages are flat .html
// files rather than dir/index.html.
var uglyURLsSite = setOf(
	"index.html",
	"404.html",
	"about.html",
	"contact.html",
	"posts/hello.html",
)

func setOf(paths ...string) Lookup {
	m := make(map[string]bool, len(paths))
	for _, p := range paths {
		m[p] = true
	}
	return func(p string) bool { return m[p] }
}

func TestResolveHugoSite(t *testing.T) {
	tests := []struct {
		name     string
		reqPath  string
		wantKind Kind
		wantPath string
	}{
		// root
		{"root empty", "", Hit, "index.html"},
		{"root slash", "/", Hit, "index.html"},

		// exact file hits (assets + SEO + search index)
		{"exact css", "/style/main.4f3a9b.css", Hit, "style/main.4f3a9b.css"},
		{"exact js", "/js/app.8c1d2e.js", Hit, "js/app.8c1d2e.js"},
		{"exact svg", "/img/logo.svg", Hit, "img/logo.svg"},
		{"exact woff2", "/fonts/inter.woff2", Hit, "fonts/inter.woff2"},
		{"exact sitemap", "/sitemap.xml", Hit, "sitemap.xml"},
		{"exact rss", "/index.xml", Hit, "index.xml"},
		{"exact robots", "/robots.txt", Hit, "robots.txt"},
		{"exact search index", "/search-index.json", Hit, "search-index.json"},
		{"exact index.html addressed directly", "/index.html", Hit, "index.html"},

		// directory-index clean URLs (the docs page shape, with trailing slash)
		{"dir index introduction", "/getting-started/introduction/", Hit, "getting-started/introduction/index.html"},
		{"dir index installation", "/getting-started/installation/", Hit, "getting-started/installation/index.html"},
		{"dir index section", "/getting-started/", Hit, "getting-started/index.html"},
		{"dir index blog", "/blog/", Hit, "blog/index.html"},
		{"dir index deep blog post", "/blog/2026/my-post/", Hit, "blog/2026/my-post/index.html"},
		{"dir index about", "/about/", Hit, "about/index.html"},

		// extensionless clean URLs (no trailing slash) -> dir/index.html
		{"extensionless introduction", "/getting-started/introduction", Hit, "getting-started/introduction/index.html"},
		{"extensionless section", "/getting-started", Hit, "getting-started/index.html"},
		{"extensionless about", "/about", Hit, "about/index.html"},

		// misses -> custom 404 signalled as NotFound (server serves 404.html @ 404)
		{"missing page", "/nope/", NotFound, ""},
		{"missing extensionless", "/does-not-exist", NotFound, ""},
		{"missing asset with ext", "/style/missing.css", NotFound, ""},
		{"missing nested dir", "/getting-started/missing/", NotFound, ""},
		{"missing deep page", "/blog/2099/ghost/", NotFound, ""},

		// directory request whose index is absent is a miss, not a file fallback
		{"trailing slash no index", "/img/", NotFound, ""},

		// traversal is neutralized by normalize, then resolved against the set
		{"dotdot escape collapses to root", "/../index.html", Hit, "index.html"},
		{"dotdot mid path", "/getting-started/../about/", Hit, "about/index.html"},
		{"dot segment", "/./sitemap.xml", Hit, "sitemap.xml"},
		{"double slash collapses", "/getting-started//introduction/", Hit, "getting-started/introduction/index.html"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.reqPath, hugoSite, false)
			if got.Kind != tt.wantKind {
				t.Errorf("Resolve(%q).Kind = %v, want %v", tt.reqPath, got.Kind, tt.wantKind)
			}
			if got.Path != tt.wantPath {
				t.Errorf("Resolve(%q).Path = %q, want %q", tt.reqPath, got.Path, tt.wantPath)
			}
		})
	}
}

func TestResolveUglyURLs(t *testing.T) {
	tests := []struct {
		name     string
		reqPath  string
		wantKind Kind
		wantPath string
	}{
		{"root", "/", Hit, "index.html"},
		{"exact html", "/about.html", Hit, "about.html"},
		{"extensionless -> .html fallback", "/about", Hit, "about.html"},
		{"extensionless contact -> .html", "/contact", Hit, "contact.html"},
		{"nested extensionless -> .html", "/posts/hello", Hit, "posts/hello.html"},
		{"missing -> 404", "/posts/missing", NotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.reqPath, uglyURLsSite, false)
			if got.Kind != tt.wantKind || got.Path != tt.wantPath {
				t.Errorf("Resolve(%q) = {%v %q}, want {%v %q}", tt.reqPath, got.Kind, got.Path, tt.wantKind, tt.wantPath)
			}
		})
	}
}

func TestResolveIndexPreferredOverHTML(t *testing.T) {
	// When both foo/index.html and foo.html exist, the directory index wins for an
	// extensionless request (Hugo's default directory-index shape takes priority).
	site := setOf("index.html", "foo/index.html", "foo.html")
	got := Resolve("/foo", site, false)
	if got.Kind != Hit || got.Path != "foo/index.html" {
		t.Errorf("Resolve(/foo) = {%v %q}, want Hit foo/index.html", got.Kind, got.Path)
	}
}

func TestResolveSPA(t *testing.T) {
	// A Vite/React SPA: only index.html + hashed assets; unknown routes fall back
	// to index.html with 200.
	spaSite := setOf(
		"index.html",
		"assets/index.4f3a9b.js",
		"assets/index.8c1d2e.css",
		"favicon.ico",
	)

	tests := []struct {
		name     string
		reqPath  string
		wantKind Kind
		wantPath string
	}{
		{"root", "/", Hit, "index.html"},
		{"hashed js asset", "/assets/index.4f3a9b.js", Hit, "assets/index.4f3a9b.js"},
		{"favicon", "/favicon.ico", Hit, "favicon.ico"},
		{"client route -> index", "/dashboard/settings", SPAFallback, "index.html"},
		{"client route trailing slash -> index", "/users/42/", SPAFallback, "index.html"},
		{"unknown asset with ext -> index (SPA serves shell)", "/assets/missing.js", SPAFallback, "index.html"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.reqPath, spaSite, true)
			if got.Kind != tt.wantKind || got.Path != tt.wantPath {
				t.Errorf("Resolve(%q, spa) = {%v %q}, want {%v %q}", tt.reqPath, got.Kind, got.Path, tt.wantKind, tt.wantPath)
			}
		})
	}
}

func TestResolveSPAWithoutIndexFallsToNotFound(t *testing.T) {
	// Defensive: a "SPA" release missing index.html cannot fall back, so a miss is
	// a genuine NotFound rather than a broken pointer at a nonexistent index.
	site := setOf("assets/app.js")
	got := Resolve("/whatever", site, true)
	if got.Kind != NotFound {
		t.Errorf("Resolve without index in SPA mode = %v, want NotFound", got.Kind)
	}
}

func TestResolveRootMissingIndex(t *testing.T) {
	site := setOf("style/main.css")
	got := Resolve("/", site, false)
	if got.Kind != NotFound {
		t.Errorf("root with no index.html = %v, want NotFound", got.Kind)
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"/", ""},
		{"//", ""},
		{".", ""},
		{"/index.html", "index.html"},
		{"index.html", "index.html"},
		{"/a/b/c", "a/b/c"},
		{"/a//b", "a/b"},
		{"/../escape", "escape"},
		{"/a/../b", "b"},
		{"/a/./b", "a/b"},
		{"/../../etc/passwd", "etc/passwd"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := normalize(tt.in); got != tt.want {
				t.Errorf("normalize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
