package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSpaHandlerPathTraversal asserts that the SPA static handler does not
// serve files outside its served root, even when the URL contains traversal
// sequences. The default gorilla/mux + http.FileServer chain already cleans
// most of these, but we want defense-in-depth so a future SkipClean(true)
// (or a different router) cannot accidentally expose the filesystem.
//
// Audit ref: audit-input-vulns-20260603 (LOW — SPA static handler depends on
// default mux path-cleaning).
func TestSpaHandlerPathTraversal(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Dir(root)

	// Place a sentinel file OUTSIDE the served root. If traversal works the
	// response body will contain the sentinel.
	secretPath := filepath.Join(parent, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("CORESCOPE_SECRET_SENTINEL"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Cleanup(func() { os.Remove(secretPath) })

	// Minimal SPA root: index.html + an asset with a dot in the filename to
	// prove legit names aren't false-positived.
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>SPA</html>"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.js"), []byte("console.log('ok')"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "customize-v2.js"), []byte("// v2"), 0644); err != nil {
		t.Fatal(err)
	}
	themes := filepath.Join(root, "themes")
	os.Mkdir(themes, 0755)
	if err := os.WriteFile(filepath.Join(themes, "dark.css"), []byte("body{}"), 0644); err != nil {
		t.Fatal(err)
	}

	fs := http.FileServer(http.Dir(root))
	handler := spaHandler(root, fs)

	traversal := []string{
		"/../secret.txt",
		"/..%2fsecret.txt",
		"/%2e%2e/secret.txt",
		"/foo/../../secret.txt",
		"/..\\secret.txt",
		"/static/..%5csecret.txt",
	}
	for _, p := range traversal {
		t.Run("blocks "+p, func(t *testing.T) {
			req := httptest.NewRequest("GET", p, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			body := w.Body.String()
			if strings.Contains(body, "CORESCOPE_SECRET_SENTINEL") {
				t.Fatalf("traversal succeeded — secret leaked for path %q: %s", p, body)
			}
			// Defense-in-depth: explicit traversal sequences must be
			// rejected with a 4xx, not silently routed through the FS
			// layer's own cleaning. This is what the green commit adds.
			if w.Code < 400 || w.Code >= 500 {
				t.Fatalf("path %q: expected explicit 4xx rejection, got %d (body=%q)", p, w.Code, body)
			}
		})
	}

	// Legit asset names with dots must still work.
	legit := []string{"/app.js", "/customize-v2.js", "/themes/dark.css"}
	for _, p := range legit {
		t.Run("serves "+p, func(t *testing.T) {
			req := httptest.NewRequest("GET", p, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("legit asset %q returned %d, expected 200", p, w.Code)
			}
		})
	}
}
