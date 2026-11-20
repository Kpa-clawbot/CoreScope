package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const siteURLTestPage = `<html><head>` +
	`<meta property="og:url" content="__CORESCOPE_URL__/">` +
	`<meta property="og:image" content="__CORESCOPE_URL__/og-image.png">` +
	`<script src="app.js?v=__BUST__"></script></head></html>`

func newSiteURLHandler(t *testing.T, siteURL string) http.Handler {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(siteURLTestPage), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "area-map.html"), []byte(`<a href="__CORESCOPE_URL__/">x</a>`), 0644); err != nil {
		t.Fatal(err)
	}
	return spaHandler(dir, http.FileServer(http.Dir(dir)), siteURL)
}

func serveSiteURL(h http.Handler, path string, mod func(*http.Request)) string {
	req := httptest.NewRequest("GET", path, nil)
	if mod != nil {
		mod(req)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Body.String()
}

func TestSiteURLConfigured(t *testing.T) {
	h := newSiteURLHandler(t, "https://analyzer.example.org/")
	for _, path := range []string{"/", "/index.html", "/some/spa/route", "/area-map.html"} {
		body := serveSiteURL(h, path, func(r *http.Request) {
			// A configured URL wins over whatever the request says.
			r.Host = "other.example.net"
			r.Header.Set("X-Forwarded-Host", "evil.example.com")
		})
		if strings.Contains(body, siteURLPlaceholder) {
			t.Errorf("%s: placeholder left: %s", path, body)
		}
		if strings.Contains(body, "other.example.net") || strings.Contains(body, "evil.example.com") {
			t.Errorf("%s: request host used despite CORESCOPE_URL: %s", path, body)
		}
		if !strings.Contains(body, `"https://analyzer.example.org/`) {
			t.Errorf("%s: configured URL missing (trailing slash trimmed): %s", path, body)
		}
	}
	body := serveSiteURL(h, "/", nil)
	if !strings.Contains(body, `content="https://analyzer.example.org/og-image.png"`) {
		t.Errorf("og:image not absolute on the configured URL: %s", body)
	}
	if strings.Contains(body, "__BUST__") {
		t.Errorf("__BUST__ no longer replaced: %s", body)
	}
}

func TestSiteURLFromRequest(t *testing.T) {
	h := newSiteURLHandler(t, "")
	cases := []struct {
		name string
		mod  func(*http.Request)
		want string
	}{
		{"plain host", func(r *http.Request) { r.Host = "mesh.example.org:8080" }, "http://mesh.example.org:8080"},
		{"host lowercased", func(r *http.Request) { r.Host = "Mesh.Example.ORG" }, "http://mesh.example.org"},
		{"direct TLS", func(r *http.Request) { r.Host = "mesh.example.org"; r.TLS = &tls.ConnectionState{} }, "https://mesh.example.org"},
		{"proxy headers", func(r *http.Request) {
			r.Host = "localhost:3000"
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("X-Forwarded-Host", "mesh.example.org")
		}, "https://mesh.example.org"},
		{"proxy chain: first value", func(r *http.Request) {
			r.Host = "localhost:3000"
			r.Header.Set("X-Forwarded-Proto", "HTTPS, http")
			r.Header.Set("X-Forwarded-Host", "mesh.example.org, inner:3000")
		}, "https://mesh.example.org"},
		{"unknown proto ignored", func(r *http.Request) {
			r.Host = "mesh.example.org"
			r.Header.Set("X-Forwarded-Proto", "javascript")
		}, "http://mesh.example.org"},
		{"IPv6 host", func(r *http.Request) { r.Host = "[::1]:3000" }, "http://[::1]:3000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := serveSiteURL(h, "/", c.mod)
			if !strings.Contains(body, `content="`+c.want+`/"`) || !strings.Contains(body, `content="`+c.want+`/og-image.png"`) {
				t.Errorf("want %s, got %s", c.want, body)
			}
		})
	}
	// Tool pages are filled in the same way, and the page served to one
	// request is not left holding the previous request's origin.
	if body := serveSiteURL(h, "/area-map.html", func(r *http.Request) { r.Host = "a.example.org" }); !strings.Contains(body, `href="http://a.example.org/"`) {
		t.Errorf("area-map.html: %s", body)
	}
	if body := serveSiteURL(h, "/", func(r *http.Request) { r.Host = "b.example.org" }); !strings.Contains(body, "http://b.example.org/") || strings.Contains(body, "a.example.org") {
		t.Errorf("origin leaked between requests: %s", body)
	}
}

func TestSiteURLForgedHostCannotInjectMarkup(t *testing.T) {
	h := newSiteURLHandler(t, "")
	body := serveSiteURL(h, "/", func(r *http.Request) {
		r.Host = "mesh.example.org"
		r.Header.Set("X-Forwarded-Host", `x"><script>alert(1)</script>`)
	})
	if strings.Contains(body, "<script>alert") || strings.Contains(body, `x">`) {
		t.Fatalf("forged X-Forwarded-Host reached the page: %s", body)
	}
	if !strings.Contains(body, `content="http://mesh.example.org/"`) {
		t.Errorf("forged X-Forwarded-Host should fall back to Host: %s", body)
	}
	// Neither header usable: root-relative URLs, never the raw value.
	body = serveSiteURL(h, "/", func(r *http.Request) { r.Host = `bad"host` })
	if strings.Contains(body, siteURLPlaceholder) || strings.Contains(body, `bad"host`) {
		t.Fatalf("invalid Host reached the page: %s", body)
	}
	if !strings.Contains(body, `content="/og-image.png"`) {
		t.Errorf("want root-relative fallback, got %s", body)
	}
}

func TestResolveConfiguredSiteURL(t *testing.T) {
	cases := map[string]string{
		"":                               "",
		"   ":                            "",
		"https://analyzer.example.org":   "https://analyzer.example.org",
		"https://analyzer.example.org/":  "https://analyzer.example.org",
		" http://localhost:3000 ":        "http://localhost:3000",
		"https://example.org/corescope/": "https://example.org/corescope",
		"analyzer.example.org":           "",
		"ftp://analyzer.example.org":     "",
		`https://example.org/"><script>`: "",
		"https://example.org/?q=1":       "",
		"https://exa mple.org":           "",
	}
	for in, want := range cases {
		if got := resolveConfiguredSiteURL(in); got != want {
			t.Errorf("resolveConfiguredSiteURL(%q) = %q, want %q", in, got, want)
		}
	}
	// An invalid CORESCOPE_URL falls back to the request's origin.
	h := newSiteURLHandler(t, "not a url")
	if body := serveSiteURL(h, "/", func(r *http.Request) { r.Host = "mesh.example.org" }); !strings.Contains(body, `content="http://mesh.example.org/"`) {
		t.Errorf("invalid CORESCOPE_URL: want request origin, got %s", body)
	}
}

// The real index.html carries the placeholder in og:url, og:image and
// twitter:image, and no hardcoded instance or GitHub URL.
func TestIndexHTMLUsesSiteURLPlaceholder(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "public", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	for _, tag := range []string{
		`<meta property="og:url" content="__CORESCOPE_URL__/">`,
		`<meta property="og:image" content="__CORESCOPE_URL__/og-image.png">`,
		`<meta name="twitter:image" content="__CORESCOPE_URL__/og-image.png">`,
	} {
		if !strings.Contains(html, tag) {
			t.Errorf("index.html is missing %s", tag)
		}
	}
	if strings.Contains(html, "raw.githubusercontent.com") {
		t.Error("index.html still links raw.githubusercontent.com")
	}
}
