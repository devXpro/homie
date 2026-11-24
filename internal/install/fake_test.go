package install

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testToken = /* #nosec G101 */ "ghp_TESTTOKEN_must_never_leak"

type tarEntry struct {
	name   string
	typ    byte // 0 means a regular file
	body   string
	mode   int64
	target string // link target
}

func makeTarGz(t testing.TB, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: mode, Linkname: e.target}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fakeRelease describes one release served by fakeGitHub.
type fakeRelease struct {
	tag      string
	prerel   bool
	manifest string            // homie-plugin.json; "" means the release has none
	files    map[string][]byte // asset name -> bytes (served under /dl/)
	sums     map[string]string // overrides for checksums.txt lines (name -> sha); default = real sha of files
	noSums   bool
	omit     string // asset name left out of checksums.txt
}

// fakeGitHub fakes the Releases API and the asset host on one TLS server.
type fakeGitHub struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	releases map[string][]*fakeRelease // repo -> releases, newest first
	apiAuth  []string                  // Authorization headers seen on /repos/
	dlAuth   []string                  // Authorization headers seen on /dl/
	rateLim  bool
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{t: t, releases: map[string][]*fakeRelease{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) client(token string) *Client {
	return &Client{APIBase: f.srv.URL, Token: token, HTTP: f.srv.Client()}
}

func (f *fakeGitHub) add(repo string, r *fakeRelease) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases[repo] = append([]*fakeRelease{r}, f.releases[repo]...)
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasPrefix(r.URL.Path, "/repos/"):
		f.apiAuth = append(f.apiAuth, r.Header.Get("Authorization"))
		f.serveAPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/dl/"):
		f.dlAuth = append(f.dlAuth, r.Header.Get("Authorization"))
		f.serveDownload(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGitHub) serveAPI(w http.ResponseWriter, r *http.Request) {
	if f.rateLim {
		w.Header().Set("X-RateLimit-Remaining", "0")
		http.Error(w, "rate limited", http.StatusForbidden)
		return
	}
	// /repos/{owner}/{name}/releases/latest | /releases/tags/{tag}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
	if len(parts) < 4 || parts[2] != "releases" {
		http.NotFound(w, r)
		return
	}
	repo := parts[0] + "/" + parts[1]
	var rel *fakeRelease
	switch {
	case parts[3] == "latest":
		for _, c := range f.releases[repo] {
			if !c.prerel {
				rel = c
				break
			}
		}
	case parts[3] == "tags" && len(parts) == 5:
		for _, c := range f.releases[repo] {
			if c.tag == parts[4] {
				rel = c
			}
		}
	}
	if rel == nil {
		http.NotFound(w, r)
		return
	}
	type asset struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
		URL  string `json:"browser_download_url"`
	}
	out := map[string]any{
		"tag_name": rel.tag, "name": "release " + rel.tag, "html_url": "https://github.com/" + repo + "/releases/tag/" + rel.tag,
		"published_at": "2026-11-15T10:00:00Z", "prerelease": rel.prerel,
	}
	var assets []asset
	add := func(name string, b []byte) {
		assets = append(assets, asset{Name: name, Size: int64(len(b)), URL: f.srv.URL + "/dl/" + repo + "/" + rel.tag + "/" + name})
	}
	if rel.manifest != "" {
		add(ManifestAsset, []byte(rel.manifest))
	}
	for n, b := range rel.files {
		add(n, b)
	}
	if !rel.noSums {
		add(ChecksumsAsset, []byte(rel.checksums()))
	}
	out["assets"] = assets
	_ = json.NewEncoder(w).Encode(out)
}

func (r *fakeRelease) checksums() string {
	var sb strings.Builder
	for n, b := range r.files {
		if n == r.omit {
			continue
		}
		s := sha(b)
		if o, ok := r.sums[n]; ok {
			s = o
		}
		fmt.Fprintf(&sb, "%s  %s\n", s, n)
	}
	return sb.String()
}

func (f *fakeGitHub) serveDownload(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/dl/"), "/") // owner, name, tag, asset
	if len(parts) != 4 {
		http.NotFound(w, r)
		return
	}
	repo := parts[0] + "/" + parts[1]
	for _, rel := range f.releases[repo] {
		if rel.tag != parts[2] {
			continue
		}
		switch parts[3] {
		case ManifestAsset:
			_, _ = w.Write([]byte(rel.manifest))
		case ChecksumsAsset:
			_, _ = w.Write([]byte(rel.checksums()))
		default:
			if b, ok := rel.files[parts[3]]; ok {
				_, _ = w.Write(b)
				return
			}
			http.NotFound(w, r)
		}
		return
	}
	http.NotFound(w, r)
}

const tvManifest = `{"id":"tv","title":"TV","description":"LG TVs","binary":"homie-tv","args":["serve"],"minHubVersion":"0.1.0","homepage":"https://github.com/devXpro/homie-tv"}`

const script = "#!/bin/sh\nexit 0\n"

// tvRelease builds a homie-tv release for linux/arm64 whose binary content carries the version.
func tvRelease(t *testing.T, version string) *fakeRelease {
	t.Helper()
	arch := makeTarGz(t, []tarEntry{
		{name: "homie-tv", body: script + "# " + version + "\n", mode: 0o755},
		{name: "README.md", body: "readme"},
	})
	return &fakeRelease{
		tag: "v" + version, manifest: tvManifest,
		files: map[string][]byte{ArchiveName("homie-tv", version, "linux", "arm64"): arch},
	}
}

func newTestInstaller(t *testing.T, f *fakeGitHub) *Installer {
	t.Helper()
	return &Installer{Client: f.client(""), DataDir: t.TempDir(), HubVersion: "0.2.0", GOOS: "linux", GOARCH: "arm64"}
}
