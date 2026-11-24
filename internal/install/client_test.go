package install

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestClientTokenGoesToTheAPIHostOnly(t *testing.T) {
	f := newFakeGitHub(t)
	f.add("devXpro/homie-tv", tvRelease(t, "0.2.0"))
	inst := newTestInstaller(t, f)
	inst.Client = f.client(testToken)
	if _, err := inst.Install(t.Context(), "devXpro/homie-tv", "", "tv", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.apiAuth) == 0 {
		t.Fatal("no API requests")
	}
	for _, a := range f.apiAuth {
		if a != "Bearer "+testToken {
			t.Fatalf("API request without the token: %q", a)
		}
	}
	if len(f.dlAuth) == 0 {
		t.Fatal("no download requests")
	}
	for _, a := range f.dlAuth {
		if a != "" {
			t.Fatalf("token sent to the asset host: %q", a)
		}
	}
}

func TestClientUnauthenticatedByDefault(t *testing.T) {
	f := newFakeGitHub(t)
	f.add("devXpro/homie-tv", tvRelease(t, "0.2.0"))
	if _, err := f.client("").Latest(t.Context(), "devXpro/homie-tv"); err != nil {
		t.Fatal(err)
	}
	if f.apiAuth[0] != "" {
		t.Fatalf("Authorization = %q", f.apiAuth[0])
	}
}

func TestClientErrorsNeverContainTheToken(t *testing.T) {
	f := newFakeGitHub(t)
	c := f.client(testToken)
	for name, call := range map[string]func() error{
		"not found": func() error { _, err := c.Latest(t.Context(), "devXpro/missing"); return err },
		"rate limit": func() error {
			f.rateLim = true
			_, err := c.Latest(t.Context(), "devXpro/homie-tv")
			f.rateLim = false
			return err
		},
		"unreachable": func() error {
			c2 := &Client{APIBase: "https://127.0.0.1:1", Token: testToken}
			_, err := c2.Latest(t.Context(), "devXpro/homie-tv")
			return err
		},
	} {
		err := call()
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		if strings.Contains(err.Error(), testToken) {
			t.Fatalf("%s: error leaks the token: %v", name, err)
		}
	}
}

func TestClientStatusMapping(t *testing.T) {
	f := newFakeGitHub(t)
	c := f.client("")
	if _, err := c.Latest(t.Context(), "devXpro/missing"); !errors.Is(err, ErrNotFound) || Code(err) != "not_found" {
		t.Fatalf("404: %v", err)
	}
	f.rateLim = true
	_, err := c.Latest(t.Context(), "devXpro/homie-tv")
	if !errors.Is(err, ErrRateLimited) || !errors.Is(err, ErrUpstream) || Code(err) != "upstream" {
		t.Fatalf("rate limit: %v", err)
	}
	if _, err := c.Latest(t.Context(), "not a repo"); err == nil {
		t.Fatal("invalid repo accepted")
	}
	if _, err := c.Latest(t.Context(), "a/../b"); err == nil {
		t.Fatal("invalid repo accepted")
	}
}

func TestClientPrereleaseAndDraftAreNotLatest(t *testing.T) {
	f := newFakeGitHub(t)
	f.add("devXpro/homie-tv", tvRelease(t, "0.1.0"))
	pre := tvRelease(t, "0.2.0-rc.1")
	pre.prerel = true
	f.add("devXpro/homie-tv", pre)
	rel, err := f.client("").Latest(t.Context(), "devXpro/homie-tv")
	if err != nil || rel.Version != "0.1.0" {
		t.Fatalf("latest = %+v %v", rel, err)
	}
	rel, err = f.client("").ReleaseByVersion(t.Context(), "devXpro/homie-tv", "v0.2.0-rc.1")
	if err != nil || !rel.Prerelease {
		t.Fatalf("explicit prerelease: %+v %v", rel, err)
	}
}

func TestClientRefusesPlainHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) }))
	t.Cleanup(plain.Close)
	if _, err := (&Client{APIBase: plain.URL}).Latest(t.Context(), "devXpro/homie-tv"); !errors.Is(err, ErrInsecureURL) {
		t.Fatalf("http API base: %v", err)
	}
}

func TestClientRefusesRedirectToHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("payload")) }))
	t.Cleanup(plain.Close)
	f := newFakeGitHub(t)
	redir := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/x", http.StatusFound)
	}))
	t.Cleanup(redir.Close)
	c := &Client{APIBase: f.srv.URL, HTTP: redir.Client()}
	_, err := c.Download(t.Context(), Asset{Name: "a", URL: redir.URL + "/a"}, t.TempDir(), 1<<20, nil)
	if !errors.Is(err, ErrInsecureURL) {
		t.Fatalf("err = %v, want ErrInsecureURL", err)
	}
}

func TestDownloadSizeLimits(t *testing.T) {
	f := newFakeGitHub(t)
	r := tvRelease(t, "0.2.0")
	f.add("devXpro/homie-tv", r)
	inst := newTestInstaller(t, f)
	inst.Limits = Limits{MaxArchiveBytes: 10}
	_, err := inst.Install(t.Context(), "devXpro/homie-tv", "", "tv", nil)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	// The advertised size passes (the fake advertises the real one) but the server streams more.
	big := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	t.Cleanup(big.Close)
	c := &Client{HTTP: big.Client()}
	dir := t.TempDir()
	if _, err := c.Download(t.Context(), Asset{Name: "a", URL: big.URL, Size: 1}, dir, 1000, nil); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("streamed overrun: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("partial download left behind: %v", entries)
	}
}

func TestTokenFromEnv(t *testing.T) {
	t.Setenv("HOMIE_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "  second ")
	if got := TokenFromEnv(); got != "second" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("HOMIE_GITHUB_TOKEN", "first")
	if got := TokenFromEnv(); got != "first" {
		t.Fatalf("got %q", got)
	}
}
