package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte(`<!doctype html><html><head><script type="module" src="/assets/index-abc.js"></script></head></html>`)},
		"theme.js":             {Data: []byte(`document.documentElement.dataset.theme='dark'`)},
		"manifest.webmanifest": {Data: []byte(`{"name":"Homie"}`)},
		"assets/index-abc.js":  {Data: []byte(`console.log("homie")`)},
	}
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPublicRoutes(t *testing.T) {
	r, err := newRoutes(testFS(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	r.RegisterRoutes(mux)
	for _, path := range []string{"/", "/home", "/login", "/setup", "/join", "/consent", "/connect", "/account/passkeys",
		"/devices", "/devices/tv/items/tv", "/activity", "/people", "/people/per_x", "/settings", "/settings/tunnel"} {
		rec := get(t, mux, path)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<!doctype html>") || rec.Header().Get("Content-Security-Policy") != CSP() {
			t.Errorf("%s: %d %v", path, rec.Code, rec.Header())
		}
	}
	// The consent page runs inside an assistant's window: no COOP there, COOP elsewhere.
	if get(t, mux, "/consent").Header().Get("Cross-Origin-Opener-Policy") != "" ||
		get(t, mux, "/").Header().Get("Cross-Origin-Opener-Policy") != "same-origin" {
		t.Error("COOP")
	}
	if rec := get(t, mux, "/assets/index-abc.js"); rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d %v", rec.Code, rec.Header())
	}
	if rec := get(t, mux, "/assets/index-old.js"); rec.Code != 404 {
		t.Errorf("a missing asset: %d", rec.Code)
	}
	if rec := get(t, mux, "/theme.js"); rec.Code != 200 {
		t.Errorf("public file: %d", rec.Code)
	}
	// v0.1's /admin paths go to the root; the first-run page is not on the public listener.
	for _, path := range []string{"/admin", "/admin/", "/admin/devices/tv"} {
		if rec := get(t, mux, path); rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != "/" {
			t.Errorf("%s: %d %v", path, rec.Code, rec.Header())
		}
	}
	if rec := get(t, mux, "/firstrun"); rec.Code != 404 {
		t.Errorf("/firstrun on the public listener: %d", rec.Code)
	}
}

func TestFirstRunRoutes(t *testing.T) {
	h, err := firstRun(testFS(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(t, h, "/firstrun"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Errorf("/firstrun: %d", rec.Code)
	}
	if rec := get(t, h, "/assets/index-abc.js"); rec.Code != 200 {
		t.Errorf("asset: %d", rec.Code)
	}
	for _, path := range []string{"/", "/login", "/devices"} {
		if rec := get(t, h, path); rec.Code != 404 {
			t.Errorf("%s on the first-run listener: %d", path, rec.Code)
		}
	}
}

// Without a build the pages answer 503 with the hint; the embedded build of the binary loads either way.
func TestNotBuilt(t *testing.T) {
	r, err := newRoutes(fstest.MapFS{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	r.RegisterRoutes(mux)
	if rec := get(t, mux, "/"); rec.Code != 503 || !strings.Contains(rec.Body.String(), "make web") {
		t.Errorf("not built: %d %s", rec.Code, rec.Body)
	}
	if _, err := New(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := FirstRun(nil); err != nil {
		t.Fatal(err)
	}
}
