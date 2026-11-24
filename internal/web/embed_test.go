package web

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

var bigJS = "console.log(" + strings.Repeat(`"homie",`, 400) + ")"

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":            {Data: []byte(`<!doctype html><html><head><script type="module" src="/admin/assets/index-abc.js"></script></head></html>`)},
		"theme.js":              {Data: []byte(`document.documentElement.dataset.theme='dark'`)},
		"manifest.webmanifest":  {Data: []byte(`{"name":"Homie"}`)},
		"assets/index-abc.js":   {Data: []byte(bigJS)},
		"assets/index-abc.css":  {Data: []byte(`body{margin:0}`)},
		".gitkeep":              {Data: nil},
		"assets/.hidden-secret": {Data: []byte("nope")},
	}
}

func get(t *testing.T, h http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRoutes(t *testing.T) {
	h := New(testFS())
	tests := []struct {
		name, path  string
		status      int
		contentType string
		cache       string
		bodyHas     string
	}{
		{"admin root", "/admin", 200, "text/html; charset=utf-8", "no-cache", "<!doctype html>"},
		{"admin slash", "/admin/", 200, "text/html; charset=utf-8", "no-cache", "<!doctype html>"},
		{"client route", "/admin/devices/tv/items/tv", 200, "text/html; charset=utf-8", "no-cache", "<!doctype html>"},
		{"setup with fragment-less path", "/admin/setup", 200, "text/html; charset=utf-8", "no-cache", "<!doctype html>"},
		{"lan first run", "/firstrun", 200, "text/html; charset=utf-8", "no-cache", "<!doctype html>"},
		{"join", "/join", 200, "text/html; charset=utf-8", "no-cache", "<!doctype html>"},
		{"explicit index", "/admin/index.html", 200, "text/html; charset=utf-8", "no-cache", "<!doctype html>"},
		{"hashed js", "/admin/assets/index-abc.js", 200, "text/javascript; charset=utf-8", "public, max-age=31536000, immutable", "homie"},
		{"hashed css", "/admin/assets/index-abc.css", 200, "text/css; charset=utf-8", "public, max-age=31536000, immutable", "margin"},
		{"missing asset is not html", "/admin/assets/index-old.js", 404, "text/plain; charset=utf-8", "no-store", "not found"},
		{"public file", "/admin/theme.js", 200, "text/javascript; charset=utf-8", "no-cache", "dataset.theme"},
		{"manifest", "/admin/manifest.webmanifest", 200, "application/manifest+json", "no-cache", "Homie"},
		{"dotfile is never served", "/admin/.gitkeep", 200, "text/html; charset=utf-8", "no-cache", "<!doctype html>"},
		{"hidden asset", "/admin/assets/.hidden-secret", 404, "text/plain; charset=utf-8", "no-store", "not found"},
		{"traversal stays inside", "/admin/../../etc/passwd", 200, "text/html; charset=utf-8", "no-cache", "<!doctype html>"},
		{"encoded traversal", "/admin/assets/..%2f..%2f..%2fetc%2fpasswd", 200, "text/html; charset=utf-8", "no-cache", "<!doctype html>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(t, h, http.MethodGet, tt.path, nil)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if got := rec.Header().Get("Content-Type"); got != tt.contentType {
				t.Errorf("Content-Type = %q, want %q", got, tt.contentType)
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.cache {
				t.Errorf("Cache-Control = %q, want %q", got, tt.cache)
			}
			if !strings.Contains(rec.Body.String(), tt.bodyHas) {
				t.Errorf("body %q does not contain %q", rec.Body.String(), tt.bodyHas)
			}
			if rec.Header().Get("Content-Security-Policy") != CSP() {
				t.Errorf("missing CSP header")
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("missing nosniff")
			}
			// HSTS on every answer, the 404s and the not-built 503 included (review L11).
			if rec.Header().Get("Strict-Transport-Security") != "max-age=31536000" {
				t.Errorf("Strict-Transport-Security = %q", rec.Header().Get("Strict-Transport-Security"))
			}
		})
	}
}

func TestCSPIsStrict(t *testing.T) {
	csp := CSP()
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "connect-src 'self'", "frame-ancestors 'none'", "base-uri 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	for _, bad := range []string{"unsafe-inline", "unsafe-eval", "*"} {
		if strings.Contains(csp, bad) {
			t.Errorf("CSP %q contains %q", csp, bad)
		}
	}
}

func TestMethods(t *testing.T) {
	h := New(testFS())
	rec := get(t, h, http.MethodPost, "/admin", nil)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST: status %d, Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
	rec = get(t, h, http.MethodHead, "/admin/assets/index-abc.js", nil)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != strconv.Itoa(len(bigJS)) {
		t.Fatalf("HEAD: status %d, body %d bytes, length %q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Length"))
	}
}

func TestETag(t *testing.T) {
	h := New(testFS())
	first := get(t, h, http.MethodGet, "/admin/theme.js", nil)
	etag := first.Header().Get("ETag")
	if etag == "" || !strings.HasPrefix(etag, `"`) {
		t.Fatalf("ETag = %q", etag)
	}
	for _, header := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
		if rec := get(t, h, http.MethodGet, "/admin/theme.js", map[string]string{"If-None-Match": header}); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("If-None-Match %q: status %d, %d bytes", header, rec.Code, rec.Body.Len())
		}
	}
	if rec := get(t, h, http.MethodGet, "/admin/theme.js", map[string]string{"If-None-Match": `"stale"`}); rec.Code != http.StatusOK {
		t.Errorf("stale ETag: status %d", rec.Code)
	}
}

func TestGzip(t *testing.T) {
	h := New(testFS())
	rec := get(t, h, http.MethodGet, "/admin/assets/index-abc.js", map[string]string{"Accept-Encoding": "br, gzip"})
	if rec.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("not gzipped: %v", rec.Header())
	}
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil || string(plain) != bigJS {
		t.Fatalf("gzip body does not round-trip: %v", err)
	}
	if rec := get(t, h, http.MethodGet, "/admin/assets/index-abc.js", map[string]string{"Accept-Encoding": "gzip;q=0"}); rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("gzip;q=0 still compressed")
	}
	// Small files are sent as they are.
	if rec := get(t, h, http.MethodGet, "/admin/assets/index-abc.css", map[string]string{"Accept-Encoding": "gzip"}); rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("tiny css compressed")
	}
}

func TestAcceptsGzip(t *testing.T) {
	tests := map[string]bool{
		"":                  false,
		"gzip":              true,
		"GZIP":              true,
		"br, gzip, deflate": true,
		"gzip;q=0":          false,
		"gzip; q=0":         false,
		"gzip;q=0.5":        true,
		"gzip;q=1.0":        true,
		"deflate, br":       false,
		"x-gzip":            false,
	}
	for header, want := range tests {
		if got := acceptsGzip(header); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

func TestNotBuilt(t *testing.T) {
	h := New(fstest.MapFS{".gitkeep": {Data: nil}})
	rec := get(t, h, http.MethodGet, "/admin", nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "make web") ||
		rec.Header().Get("Strict-Transport-Security") == "" {
		t.Fatalf("status %d, body %q, headers %v", rec.Code, rec.Body.String(), rec.Header())
	}
}

// TestEmbedded works whether or not `make web` ran before `go test`: the real build or the 503 hint.
func TestEmbedded(t *testing.T) {
	rec := get(t, Handler(), http.MethodGet, "/admin", nil)
	switch rec.Code {
	case http.StatusOK:
		if !strings.Contains(rec.Body.String(), `<div id="root">`) {
			t.Fatalf("embedded index.html is not the SPA: %q", rec.Body.String())
		}
	case http.StatusServiceUnavailable:
	default:
		t.Fatalf("status %d", rec.Code)
	}
}
