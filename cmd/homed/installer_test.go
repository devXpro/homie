package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devXpro/homie/internal/admin"
	"github.com/devXpro/homie/internal/config"
	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/install"
	"github.com/devXpro/homie/internal/plugins"
	"github.com/devXpro/homie/internal/store"
)

// fakeGitHub serves releases over TLS the way api.github.com and its asset host do, for any repo published to it.
type fakeGitHub struct {
	srv *httptest.Server

	mu       sync.Mutex
	releases map[string]map[string][]fakeAsset // repo → version → assets
	latest   map[string]string                 // repo → version
}

type fakeAsset struct {
	name string
	body []byte
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{releases: map[string]map[string][]fakeAsset{}, latest: map[string]string{}}
	g.srv = httptest.NewTLSServer(g)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) client() *install.Client {
	return &install.Client{APIBase: g.srv.URL, HTTP: g.srv.Client()}
}

// publish adds a release: the archive with one executable, checksums.txt and, for a plugin, homie-plugin.json.
func (g *fakeGitHub) publish(t *testing.T, repo, version, binary string, exe []byte, manifest string) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: binary, Mode: 0o755, Size: int64(len(exe)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(exe); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	name := install.ArchiveName(binary, version, runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(buf.Bytes())
	assets := []fakeAsset{
		{name, buf.Bytes()},
		{install.ChecksumsAsset, []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")},
	}
	if manifest != "" {
		assets = append(assets, fakeAsset{install.ManifestAsset, []byte(manifest)})
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.releases[repo] == nil {
		g.releases[repo] = map[string][]fakeAsset{}
	}
	g.releases[repo][version] = assets
	g.latest[repo] = version
}

func (g *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if rest, ok := strings.CutPrefix(r.URL.Path, "/dl/"); ok { // /dl/<owner>/<name>/<version>/<asset>
		parts := strings.Split(rest, "/")
		if len(parts) == 4 {
			for _, a := range g.releases[parts[0]+"/"+parts[1]][parts[2]] {
				if a.name == parts[3] {
					_, _ = w.Write(a.body)
					return
				}
			}
		}
		http.NotFound(w, r)
		return
	}
	for repo, versions := range g.releases {
		version := ""
		switch {
		case r.URL.Path == "/repos/"+repo+"/releases/latest":
			version = g.latest[repo]
		case strings.HasPrefix(r.URL.Path, "/repos/"+repo+"/releases/tags/v"):
			version = strings.TrimPrefix(r.URL.Path, "/repos/"+repo+"/releases/tags/v")
		}
		assets, ok := versions[version]
		if !ok {
			continue
		}
		type asset struct {
			Name string `json:"name"`
			Size int    `json:"size"`
			URL  string `json:"browser_download_url"`
		}
		list := []asset{}
		for _, a := range assets {
			list = append(list, asset{a.name, len(a.body), g.srv.URL + "/dl/" + repo + "/" + version + "/" + a.name})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v" + version, "name": "v" + version, "assets": list,
			"html_url": "https://github.com/" + repo + "/releases/tag/v" + version, "published_at": "2026-11-15T12:00:00Z",
		})
		return
	}
	http.NotFound(w, r)
}

// reports collects a job's steps and messages.
type reports struct {
	mu       sync.Mutex
	steps    []string
	messages []string
}

func (r *reports) report(j admin.JobReport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j.Step != "" {
		r.steps = append(r.steps, j.Step)
	}
	if j.Message != "" {
		r.messages = append(r.messages, j.Message)
	}
}

func (r *reports) take() (steps, messages []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	steps, messages, r.steps, r.messages = r.steps, r.messages, nil, nil
	return steps, messages
}

func jobCode(err error) string {
	if je, ok := errors.AsType[*admin.JobError](err); ok {
		return je.Code
	}
	return ""
}

// TestInstallerAdapter runs the admin's installer against a fake GitHub with the real store and plugin host: an
// install that comes up, a second install of the same plugin refused before it touches the files, an update whose
// binary does not start (rolled back), a good update, a removal, and the hub update ending in a restart request.
func TestInstallerAdapter(t *testing.T) {
	dir := t.TempDir()
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(dir, config.DBFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	host, err := plugins.New(nil, dir, slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	host.Start(ctx)
	t.Cleanup(host.Close)
	restarted := make(chan struct{})
	in := newInstaller(dir, st, &pluginRuntime{st: st, host: host}, host, func() { close(restarted) })
	gh := newFakeGitHub(t)
	in.inst.Client = gh.client()
	exe, err := os.ReadFile(fakeBin) //nolint:gosec // the test's own build
	if err != nil {
		t.Fatal(err)
	}
	const repo = "devXpro/homie-fake"
	manifest := `{"id":"fake","title":"Fake","description":"A test plugin.","binary":"fakeplugin","args":["serve"],` +
		`"homepage":"https://example.com/fake"}`
	gh.publish(t, repo, "1.0.0", "fakeplugin", exe, manifest)
	var rep reports
	up := func() bool {
		for _, s := range host.Status() {
			if s.ID == "fake" {
				return s.State == hub.PluginUp
			}
		}
		return false
	}
	row := func() store.Plugin {
		t.Helper()
		p, err := st.Plugins().Get(ctx, "fake")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	binary := func(version string) string {
		return filepath.Join(dir, "plugins", "fake", "bin", version, "fakeplugin")
	}

	pv, err := in.Preview(ctx, repo, "latest")
	if err != nil || pv.ID != "fake" || pv.Title != "Fake" || pv.Version != "1.0.0" || !pv.Compatible || !pv.AssetFound ||
		pv.PublishedAt == nil || pv.Homepage != "https://example.com/fake" {
		t.Fatalf("preview = %+v, %v", pv, err)
	}
	if _, err := in.Preview(ctx, "devXpro/nope", "latest"); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("preview of a missing repo: %v", err)
	}

	id, err := in.Install(ctx, repo, "", "", rep.report)
	if err != nil || id != "fake" {
		t.Fatalf("install = %q, %v", id, err)
	}
	steps, messages := rep.take()
	if !slices.Equal(steps, []string{"resolve", "download", "verify", "unpack", "start"}) || !slices.Contains(messages, "Fake 1.0.0") {
		t.Fatalf("install reports: %v %v", steps, messages)
	}
	if p := row(); p.Source != store.PluginGitHub || p.Repo != repo || p.Version != "1.0.0" || !p.Enabled ||
		strings.Join(p.Command, " ") != binary("1.0.0")+" serve" {
		t.Fatalf("row after install = %+v", p)
	}
	if !up() {
		t.Fatalf("not up after install: %+v", host.Status())
	}

	// The same release again: refused before anything is unpacked, the installed plugin untouched.
	if _, err := in.Install(ctx, repo, "", "", rep.report); jobCode(err) != "conflict" {
		t.Fatalf("second install: %v", err)
	}
	if _, err := os.Stat(binary("1.0.0")); err != nil || !up() {
		t.Fatalf("the second install touched the installed plugin: %v, %+v", err, host.Status())
	}
	rep.take()

	// An update whose binary does not start: the old row and binary come back.
	gh.publish(t, repo, "2.0.0", "fakeplugin", []byte("#!/bin/sh\necho broken >&2\nexit 3\n"), manifest)
	if err := in.Update(ctx, "fake", "", false, rep.report); jobCode(err) != "rolled_back" {
		t.Fatalf("broken update: %v", err)
	}
	if steps, _ := rep.take(); !slices.Contains(steps, "switch") || steps[len(steps)-1] != "rollback" {
		t.Fatalf("broken update steps: %v", steps)
	}
	if p := row(); p.Version != "1.0.0" || p.Command[0] != binary("1.0.0") {
		t.Fatalf("row after the rollback = %+v", p)
	}
	if _, err := os.Stat(binary("2.0.0")); !os.IsNotExist(err) || !up() {
		t.Fatalf("after the rollback: %v, %+v", err, host.Status())
	}

	// A good update switches; the previous version stays on disk for the next rollback.
	gh.publish(t, repo, "3.0.0", "fakeplugin", exe, manifest)
	if err := in.Update(ctx, "fake", "3.0.0", false, rep.report); err != nil {
		t.Fatal(err)
	}
	if steps, _ := rep.take(); !slices.Equal(steps, []string{"resolve", "download", "verify", "unpack", "switch", "start"}) {
		t.Fatalf("update steps: %v", steps)
	}
	if p := row(); p.Version != "3.0.0" || p.PreviousVersion != "1.0.0" || p.Command[0] != binary("3.0.0") || !up() {
		t.Fatalf("row after the update = %+v, %+v", p, host.Status())
	}
	if latest, err := in.LatestVersion(ctx, repo); err != nil || latest != "3.0.0" {
		t.Fatalf("latest = %q, %v", latest, err)
	}

	// An older release is a downgrade: refused unless allowed (the admin allows it only on the socket, with a flag).
	if err := in.Update(ctx, "fake", "1.0.0", false, rep.report); jobCode(err) != "downgrade" {
		t.Fatalf("downgrade: %v", err)
	}
	if p := row(); p.Version != "3.0.0" || !up() {
		t.Fatalf("row after the refused downgrade = %+v", p)
	}
	rep.take()

	// One job per plugin: while another job holds it, an install, an update and a removal answer job_running.
	unlock, err := in.locks.lock("fake")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.locks.lock("fake"); jobCode(err) != "job_running" {
		t.Fatalf("second lock: %v", err)
	}
	if err := in.Update(ctx, "fake", "", false, rep.report); jobCode(err) != "job_running" {
		t.Fatalf("update while locked: %v", err)
	}
	if err := in.Remove(ctx, "fake", false, rep.report); jobCode(err) != "job_running" {
		t.Fatalf("remove while locked: %v", err)
	}
	if _, err := in.Install(ctx, repo, "", "", rep.report); jobCode(err) != "job_running" {
		t.Fatalf("install while locked: %v", err)
	}
	unlock()
	if p := row(); p.Version != "3.0.0" || !up() {
		t.Fatalf("row after the refused jobs = %+v", p)
	}
	rep.take()

	if err := in.Remove(ctx, "fake", false, rep.report); err != nil {
		t.Fatal(err)
	}
	if steps, _ := rep.take(); !slices.Equal(steps, []string{"stop", "remove"}) {
		t.Fatalf("remove steps: %v", steps)
	}
	if _, err := st.Plugins().Get(ctx, "fake"); !errors.Is(err, store.ErrNotFound) || len(host.Status()) != 0 {
		t.Fatalf("after remove: %v, %+v", err, host.Status())
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins", "fake")); !os.IsNotExist(err) {
		t.Fatalf("the plugin's files stayed: %v", err)
	}

	// The hub update replaces the binary, then asks homed to restart.
	exePath := filepath.Join(t.TempDir(), "homed")
	if err := os.WriteFile(exePath, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil { //nolint:gosec // a fake executable
		t.Fatal(err)
	}
	in.hub = &install.HubUpdater{Client: gh.client(), Version: "0.1.0", ExePath: exePath, SkipSmokeTest: true}
	gh.publish(t, install.DefaultHubRepo, "0.2.0", "homed", []byte("#!/bin/sh\necho new\n"), "")
	if rel, err := in.LatestHub(ctx); err != nil || rel.Version != "0.2.0" || !strings.HasSuffix(rel.NotesURL, "/v0.2.0") {
		t.Fatalf("latest hub = %+v, %v", rel, err)
	}
	if err := in.UpdateHub(ctx, "", false, rep.report); err != nil {
		t.Fatal(err)
	}
	if steps, messages := rep.take(); steps[len(steps)-1] != "restart" || !slices.Contains(messages, "0.2.0") {
		t.Fatalf("hub update reports: %v %v", steps, messages)
	}
	if got := readFile(t, exePath); !strings.Contains(got, "new") {
		t.Fatalf("the binary was not replaced: %q", got)
	}
	select {
	case <-restarted:
	case <-time.After(10 * time.Second):
		t.Fatal("no restart request after the hub update")
	}

	// Without a running homed (the CLI's in-process admin) nothing is installed.
	offline := newInstaller(dir, st, &pluginRuntime{st: st, host: host}, host, nil)
	if _, err := offline.Install(ctx, repo, "", "", rep.report); err == nil {
		t.Fatal("install without a running homed")
	}
}
