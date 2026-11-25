// Package tunneltest gives tests a fake cloudflared: internal/tunnel/testdata/fakecloudflared, built once per test
// binary and copied, with its own control directory, for each test. It never talks to Cloudflare.
package tunneltest

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	buildOnce sync.Once
	built     string
	buildErr  error
)

// build compiles the fake once; the binary lives in a temp dir that Main removes when the tests are done.
func build() (string, error) {
	buildOnce.Do(func() {
		_, here, _, ok := runtime.Caller(0)
		if !ok {
			buildErr = os.ErrNotExist
			return
		}
		src := filepath.Join(filepath.Dir(here), "..", "testdata", "fakecloudflared")
		dir, err := os.MkdirTemp("", "homie-fakecloudflared-")
		if err != nil {
			buildErr = err
			return
		}
		built = filepath.Join(dir, "cloudflared")
		cmd := exec.Command("go", "build", "-o", built, ".") //nolint:gosec // G204: fixed arguments
		cmd.Dir = src
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = &buildError{err: err, out: string(out)}
		}
	})
	return built, buildErr
}

// Main runs the tests of a package that uses New and then removes the fake built for them. Call it from TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(tunneltest.Main(m)) }
func Main(m *testing.M) int {
	code := m.Run()
	if built != "" {
		_ = os.RemoveAll(filepath.Dir(built))
	}
	return code
}

type buildError struct {
	err error
	out string
}

func (e *buildError) Error() string {
	return "build the fake cloudflared: " + e.err.Error() + "\n" + e.out
}

// Fake is one copy of the fake cloudflared with its own control directory.
type Fake struct {
	t testing.TB
	// Bin is the fake's path, named cloudflared.
	Bin string
	ctl string
}

// New builds (once) and copies the fake for t.
func New(t testing.TB) *Fake {
	t.Helper()
	src, err := build()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	f := &Fake{t: t, Bin: filepath.Join(dir, "cloudflared"), ctl: filepath.Join(dir, "control")}
	if err := os.Mkdir(f.ctl, 0o700); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(src) //nolint:gosec // G304: the binary built above
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(f.Bin, os.O_CREATE|os.O_WRONLY, 0o700) //nolint:gosec // G302: an executable test binary
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	// A test's last control file (die) must not leave a child running after it.
	t.Cleanup(func() { f.Set("die") })
	return f
}

// Set creates a control file: login-release, login-fail, no-cert, create-fail, dns-exists, die, exit-now, no-metrics,
// no-log-url, no-register (see the fake's doc).
func (f *Fake) Set(name string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.ctl, name), nil, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// Unset removes a control file.
func (f *Fake) Unset(name string) {
	f.t.Helper()
	if err := os.Remove(filepath.Join(f.ctl, name)); err != nil && !os.IsNotExist(err) {
		f.t.Fatal(err)
	}
}

// Run is one start of a tunnel, as the fake recorded it.
type Run struct {
	N           int      `json:"n"`
	Args        []string `json:"args"`
	Env         []string `json:"env"`
	HomeEntries int      `json:"homeEntries"`
	Pid         int      `json:"pid"`
}

// Runs returns the tunnel starts so far, oldest first.
func (f *Fake) Runs() []Run {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.ctl, "runs.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	var out []Run
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var r Run
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// QuickHost is the host the fake gives the quick tunnel of run n (counting from 1).
func QuickHost(n int) string { return "example-" + strconv.Itoa(n) + ".trycloudflare.com" }

// Eventually polls cond until it holds or ten seconds pass.
func Eventually(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Edge is an http.Client that plays the Cloudflare edge for a test: every https://<host>/… request goes, as plain
// HTTP, to the hub's listener at addr, with the public host in Host and the client address in CF-Connecting-IP, the
// way cloudflared forwards it.
func Edge(addr, clientIP string) *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: edge{addr: addr, ip: clientIP, rt: http.DefaultTransport}}
}

type edge struct {
	addr, ip string
	rt       http.RoundTripper
}

func (e edge) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Host = r.URL.Host
	r.URL.Scheme, r.URL.Host = "http", e.addr
	if e.ip != "" {
		r.Header.Set("CF-Connecting-IP", e.ip)
	}
	return e.rt.RoundTrip(r)
}
