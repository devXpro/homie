package tunnel

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/devXpro/mcp-gatekeeper/core"
	"github.com/devXpro/mcp-gatekeeper/tunnel/cloudflare"

	"github.com/devXpro/homie/internal/tunnel/tunneltest"
)

const instance = "0123456789abcdef0123456789abcdef"

// hub is a stand-in for homed's listener: /healthz with the instance header, as the public handler answers it.
func hub(t *testing.T, id string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{ReadHeaderTimeout: 5e9, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id != "" {
			w.Header().Set(InstanceHeader, id)
		}
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// rig is a Manager on a fake cloudflared, a fake hub and a homed.yaml kept in memory.
type rig struct {
	m    *Manager
	fake *tunneltest.Fake
	dir  string

	mu     sync.Mutex
	saved  [][2]string // (mode, hostname) of every Save
	states []State     // every OnState
}

func newRig(t *testing.T) *rig {
	t.Helper()
	addr := hub(t, instance)
	r := &rig{fake: tunneltest.New(t), dir: filepath.Join(t.TempDir(), "tunnel")}
	m, err := New(Options{
		Dir: r.dir, Service: "http://" + addr, Binary: r.fake.Bin, InstanceID: instance,
		Save: func(mode, hostname string) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.saved = append(r.saved, [2]string{mode, hostname})
			return nil
		},
		OnState: func(s State) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.states = append(r.states, s)
		},
		ProbeClient: tunneltest.Edge(addr, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		m.Stop()
		cancel()
	})
	m.runCtx = ctx
	r.m = m
	return r
}

func (r *rig) lastState() State {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.states) == 0 {
		return State{}
	}
	return r.states[len(r.states)-1]
}

func (r *rig) savedModes() [][2]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.saved)
}

func (r *rig) connected(t *testing.T) Status {
	t.Helper()
	tunneltest.Eventually(t, "the tunnel connected", func() bool {
		return r.m.Status().State == string(cloudflare.StateConnected)
	})
	return r.m.Status()
}

func TestMain(m *testing.M) { os.Exit(tunneltest.Main(m)) }

// Named connect, as the first-run page runs it: the login link first, then the tunnel, the DNS record, the stored
// mode, the child and the probe through the "edge".
func TestConnectNamed(t *testing.T) {
	r := newRig(t)
	job, err := r.m.Connect("home.example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != JobRunning || job.Hostname != "home.example.com" {
		t.Fatalf("job = %+v", job)
	}
	tunneltest.Eventually(t, "the login link", func() bool {
		j, _ := r.m.Job()
		return strings.HasPrefix(j.LoginURL, "https://dash.cloudflare.com/")
	})
	if _, err := r.m.Connect("home.example.com", false); !errors.Is(err, core.ErrConflict) {
		t.Errorf("a second connect = %v", err)
	}
	r.fake.Set("login-release")
	tunneltest.Eventually(t, "the job to end", func() bool {
		j, _ := r.m.Job()
		return j.State != JobRunning
	})
	if j, _ := r.m.Job(); j.State != JobOK || j.Error != "" || j.EndedAt.IsZero() {
		t.Fatalf("job = %+v", j)
	}
	if got := r.savedModes(); len(got) != 1 || got[0] != [2]string{ModeNamed, "home.example.com"} {
		t.Errorf("saved = %v", got)
	}
	st := r.connected(t)
	if st.Mode != ModeNamed || st.URL != "https://home.example.com" || st.Hostname != "home.example.com" {
		t.Errorf("status = %+v", st)
	}
	if s := r.lastState(); s != (State{Mode: ModeNamed, URL: "https://home.example.com", Host: "home.example.com"}) {
		t.Errorf("state = %+v", s)
	}
	// With a probe the state does not wait for the child's log, so wait for the child to record its start.
	tunneltest.Eventually(t, "the child", func() bool { return len(r.fake.Runs()) > 0 })
	runs := r.fake.Runs()
	if len(runs) != 1 || !slices.Contains(runs[0].Args, filepath.Join(r.dir, cloudflare.ConfigFile)) {
		t.Fatalf("runs = %+v", runs)
	}
	for _, kv := range runs[0].Env {
		if strings.Contains(strings.ToLower(kv), "token") || strings.Contains(kv, "c2VjcmV0") {
			t.Errorf("a secret in the child's environment: %s", kv)
		}
	}
	for _, f := range []string{cloudflare.ConfigFile, cloudflare.CredentialsFile, cloudflare.RecordFile} {
		fi, err := os.Stat(filepath.Join(r.dir, f))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v", f, fi, err)
		}
	}
	if fi, err := os.Stat(r.dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("tunnel dir: %v, %v", fi, err)
	}
}

// A DNS record that exists stops the connect with the way out in the error; --overwrite-dns goes through.
func TestConnectExistingDNSRecord(t *testing.T) {
	r := newRig(t)
	r.fake.Set("dns-exists")
	r.fake.Set("login-release")
	if _, err := r.m.Connect("home.example.com", false); err != nil {
		t.Fatal(err)
	}
	tunneltest.Eventually(t, "the job to fail", func() bool { j, _ := r.m.Job(); return j.State == JobFailed })
	j, _ := r.m.Job()
	if j.Step != cloudflare.StepDNS || !strings.Contains(j.Error, "--overwrite-dns") {
		t.Errorf("job = %+v", j)
	}
	if len(r.savedModes()) != 0 || r.m.Status().Mode != ModeOff {
		t.Errorf("a failed connect changed the mode: %v %+v", r.savedModes(), r.m.Status())
	}
	if _, err := r.m.Connect("home.example.com", true); err != nil {
		t.Fatal(err)
	}
	tunneltest.Eventually(t, "the job to end", func() bool { j, _ := r.m.Job(); return j.State != JobRunning })
	if j, _ := r.m.Job(); j.State != JobOK {
		t.Errorf("job = %+v", j)
	}
	r.connected(t)
}

func TestConnectRefusesABadHost(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Connect("https://home.example.com", false); !errors.Is(err, cloudflare.ErrHostname) {
		t.Errorf("= %v", err)
	}
	if _, ok := r.m.Job(); ok {
		t.Error("a refused connect left a job")
	}
}

// Quick mode: the host comes from cloudflared, every restart brings a new one, and OnState hears every change (the
// old host first goes away).
func TestQuick(t *testing.T) {
	r := newRig(t)
	if err := r.m.Quick(); err != nil {
		t.Fatal(err)
	}
	first := "https://" + tunneltest.QuickHost(1)
	tunneltest.Eventually(t, "the quick host", func() bool { return r.lastState().URL == first })
	if st := r.connected(t); st.Mode != ModeQuick || st.URL != first {
		t.Errorf("status = %+v", st)
	}
	if got := r.savedModes(); len(got) != 1 || got[0] != [2]string{ModeQuick, ""} {
		t.Errorf("saved = %v", got)
	}
	if err := r.m.Quick(); err != nil { // already quick: nothing happens
		t.Fatal(err)
	}
	if err := r.m.Restart(); err != nil {
		t.Fatal(err)
	}
	second := "https://" + tunneltest.QuickHost(2)
	tunneltest.Eventually(t, "the new quick host", func() bool { return r.lastState().URL == second })
	r.connected(t)
	r.mu.Lock()
	states := slices.Clone(r.states)
	r.mu.Unlock()
	want := []State{
		{Mode: ModeQuick, URL: first, Host: tunneltest.QuickHost(1)},
		{Mode: ModeQuick},
		{Mode: ModeQuick, URL: second, Host: tunneltest.QuickHost(2)},
	}
	if !slices.Equal(states, want) {
		t.Errorf("states = %+v", states)
	}
	// A domain cannot be replaced by a quick tunnel without a disconnect.
	if _, err := r.m.Connect("home.example.com", false); err != nil {
		t.Fatal(err)
	}
	r.fake.Set("login-release")
	tunneltest.Eventually(t, "the connect", func() bool { j, _ := r.m.Job(); return j.State == JobOK })
	tunneltest.Eventually(t, "the named URL", func() bool { return r.lastState().URL == "https://home.example.com" })
	if err := r.m.Quick(); !errors.Is(err, core.ErrConflict) {
		t.Errorf("quick over a domain = %v", err)
	}
}

// A dying child is restarted; a quick one comes back with a new host.
func TestQuickChildDies(t *testing.T) {
	r := newRig(t)
	if err := r.m.Quick(); err != nil {
		t.Fatal(err)
	}
	r.connected(t)
	r.fake.Set("die")
	second := "https://" + tunneltest.QuickHost(2)
	tunneltest.Eventually(t, "the next host", func() bool { return r.lastState().URL == second })
}

func TestDisconnect(t *testing.T) {
	r := newRig(t)
	if err := r.m.Quick(); err != nil {
		t.Fatal(err)
	}
	r.connected(t)
	if err := r.m.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if st := r.m.Status(); st.Mode != ModeOff || st.State != string(cloudflare.StateStopped) || st.URL != "" {
		t.Errorf("status = %+v", st)
	}
	if s := r.lastState(); s != (State{Mode: ModeOff}) {
		t.Errorf("state = %+v", s)
	}
	if got := r.savedModes(); got[len(got)-1] != [2]string{ModeOff, ""} {
		t.Errorf("saved = %v", got)
	}
	if err := r.m.Restart(); !errors.Is(err, core.ErrConflict) {
		t.Errorf("restart with no tunnel = %v", err)
	}
}

// Start refuses a named tunnel whose directory serves another host, or none, and says so in the status.
func TestStartNamedNeedsTheConnectedHost(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.m.Start(ctx, ModeNamed, "home.example.com"); err == nil {
		t.Fatal("a named tunnel started without a connect")
	}
	if st := r.m.Status(); st.State != string(cloudflare.StateFailing) || st.LastError == "" ||
		st.URL != "https://home.example.com" {
		t.Errorf("status = %+v", st)
	}
	// A connect of another host: Start with the old one refuses.
	r.fake.Set("login-release")
	if _, err := r.m.Connect("other.example.com", false); err != nil {
		t.Fatal(err)
	}
	tunneltest.Eventually(t, "the connect", func() bool { j, _ := r.m.Job(); return j.State == JobOK })
	r.m.Stop()
	if err := r.m.Start(ctx, ModeNamed, "home.example.com"); err == nil || !strings.Contains(err.Error(), "other.example.com") {
		t.Errorf("= %v", err)
	}
	if err := r.m.Start(ctx, ModeNamed, "other.example.com"); err != nil {
		t.Fatal(err)
	}
	r.connected(t)
}

func TestStartOff(t *testing.T) {
	r := newRig(t)
	if err := r.m.Start(context.Background(), ModeOff, ""); err != nil {
		t.Fatal(err)
	}
	if st := r.m.Status(); st.Mode != ModeOff || st.State != string(cloudflare.StateStopped) {
		t.Errorf("status = %+v", st)
	}
	if len(r.fake.Runs()) != 0 {
		t.Error("mode off ran cloudflared")
	}
}

func TestProbe(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.m.Probe(ctx, "https://home.example.com"); err != nil {
		t.Errorf("own hub: %v", err)
	}
	other := hub(t, "ffffffffffffffffffffffffffffffff")
	r.m.probe = tunneltest.Edge(other, "")
	if err := r.m.Probe(ctx, "https://home.example.com"); err == nil || !strings.Contains(err.Error(), "another Homie hub") {
		t.Errorf("another hub: %v", err)
	}
	none := hub(t, "")
	r.m.probe = tunneltest.Edge(none, "")
	if err := r.m.Probe(ctx, "https://home.example.com"); err == nil || !strings.Contains(err.Error(), "something else") {
		t.Errorf("no instance header: %v", err)
	}
}

func TestConnectOffline(t *testing.T) {
	fake := tunneltest.New(t)
	fake.Set("login-release")
	dir := filepath.Join(t.TempDir(), "tunnel")
	var out bytes.Buffer
	rec, err := ConnectOffline(context.Background(), Options{Dir: dir, Service: "http://127.0.0.1:8080", Binary: fake.Bin},
		"home.example.com", false, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if rec.Hostname != "home.example.com" || !strings.HasPrefix(rec.Name, NamePrefix) {
		t.Errorf("record = %+v", rec)
	}
	if !strings.Contains(out.String(), "https://dash.cloudflare.com/") {
		t.Errorf("output without the login link:\n%s", out.String())
	}
}

func TestFindCloudflared(t *testing.T) {
	fake := tunneltest.New(t)
	if got, err := FindCloudflared(fake.Bin); err != nil || got != fake.Bin {
		t.Errorf("configured = %q, %v", got, err)
	}
	t.Setenv("PATH", filepath.Dir(fake.Bin))
	if got, err := FindCloudflared(""); err != nil || got != fake.Bin {
		t.Errorf("PATH = %q, %v", got, err)
	}
	if _, err := FindCloudflared("no-such-cloudflared"); err == nil {
		t.Error("a missing configured binary was accepted")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := FindCloudflared(""); err == nil {
		t.Error("found a cloudflared where there is none")
	}
}

func TestChildLogRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "cloudflared.log")
	l := NewChildLog(path, nil)
	l.maxSize = 10
	for _, line := range []string{"aaaaaa\n", "bbbbbb\n", "cccccc\n", "dddddd\n"} {
		if _, err := l.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{path: "dddddd\n", path + ".1": "cccccc\n", path + ".2": "bbbbbb\n"} {
		b, err := os.ReadFile(name) //nolint:gosec // G304: the test's temp dir
		if err != nil || string(b) != want {
			t.Errorf("%s = %q, %v", filepath.Base(name), b, err)
		}
		if fi, err := os.Stat(name); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", filepath.Base(name), fi)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("a fourth file was kept: %v", err)
	}
}
