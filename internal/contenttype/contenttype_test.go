package contenttype

import "testing"

func TestFromPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"html", "index.html", "text/html; charset=utf-8"},
		{"htm", "page.htm", "text/html; charset=utf-8"},
		{"css", "style/main.abc123.css", "text/css; charset=utf-8"},
		{"js", "app.js", "text/javascript; charset=utf-8"},
		{"mjs", "module.mjs", "text/javascript; charset=utf-8"},
		{"json", "search-index.json", "application/json; charset=utf-8"},
		{"map source map", "app.js.map", "application/json; charset=utf-8"},
		{"svg", "logo.svg", "image/svg+xml"},
		{"xml sitemap", "sitemap.xml", "application/xml; charset=utf-8"},
		{"xml rss", "index.xml", "application/xml; charset=utf-8"},
		{"txt", "robots.txt", "text/plain; charset=utf-8"},
		{"png", "img/hero.png", "image/png"},
		{"jpg", "photo.jpg", "image/jpeg"},
		{"jpeg", "photo.jpeg", "image/jpeg"},
		{"gif", "anim.gif", "image/gif"},
		{"webp", "pic.webp", "image/webp"},
		{"avif", "pic.avif", "image/avif"},
		{"ico", "favicon.ico", "image/x-icon"},
		{"woff2", "fonts/inter.woff2", "font/woff2"},
		{"woff", "fonts/inter.woff", "font/woff"},
		{"ttf", "fonts/inter.ttf", "font/ttf"},
		{"otf", "fonts/inter.otf", "font/otf"},
		{"wasm", "lib.wasm", "application/wasm"},
		{"pdf", "doc.pdf", "application/pdf"},

		// case-insensitivity
		{"uppercase ext", "IMAGE.PNG", "image/png"},
		{"mixed-case ext", "Style.CsS", "text/css; charset=utf-8"},

		// nested + dotted names resolve on the final extension
		{"fingerprinted css", "css/main.4f3a9b.min.css", "text/css; charset=utf-8"},
		{"deep path html", "getting-started/introduction/index.html", "text/html; charset=utf-8"},

		// defaults
		{"unknown ext", "archive.tar.zst", Default},
		{"no ext", "LICENSE", Default},
		{"dotfile no ext", ".nojekyll", Default},
		{"empty", "", Default},
		{"trailing dot", "weird.", Default},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FromPath(tt.path); got != tt.want {
				t.Errorf("FromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestFromExt(t *testing.T) {
	tests := []struct {
		ext  string
		want string
	}{
		{".css", "text/css; charset=utf-8"},
		{"css", "text/css; charset=utf-8"},
		{".PNG", "image/png"},
		{"woff2", "font/woff2"},
		{"", Default},
		{".", Default},
		{".unknown", Default},
	}
	for _, tt := range tests {
		t.Run(tt.ext, func(t *testing.T) {
			if got := FromExt(tt.ext); got != tt.want {
				t.Errorf("FromExt(%q) = %q, want %q", tt.ext, got, tt.want)
			}
		})
	}
}
