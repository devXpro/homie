package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/devXpro/mcp-gatekeeper/core"

	"github.com/devXpro/homie/internal/config"
	"github.com/devXpro/homie/internal/tunnel/tunneltest"
)

var devSite = core.Site{PublicURL: "http://localhost:5173", RPID: "localhost", ClientIP: core.ClientIPRemote}

// cli runs the homed CLI in-process, with an empty stdin, and returns its exit code, stdout and stderr.
func cli(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

// The installer's check: `homed version` prints "homed <version>".
func TestVersion(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-v"} {
		if code, out, _ := cli(t, arg); code != 0 || out != "homed "+version+"\n" {
			t.Errorf("%s: %d %q", arg, code, out)
		}
	}
	if code, out, _ := cli(t, "help"); code != 0 || !strings.Contains(out, "homed cloudflare connect") {
		t.Errorf("help: %d", code)
	}
}

func TestUnknownCommands(t *testing.T) {
	dir := shortTempDir(t)
	for _, args := range [][]string{{}, {"relay", "connect", "-"}, {"key", "new"}, {"admin", "nope"}, {"bogus"}} {
		code, _, errOut := cli(t, append(args, "--data-dir", dir)...)
		if len(args) == 0 {
			code, _, errOut = cli(t)
		}
		if code != exitUsage || !strings.Contains(errOut, "homed") {
			t.Errorf("%v: %d %q", args, code, errOut)
		}
	}
}

// Every command but run, version and cloudflare connect needs a running homed; `cloudflare status` fails (exit 1)
// until its socket is up, which is what the installer waits on.
func TestCommandsNeedRunningHomed(t *testing.T) {
	dir := shortTempDir(t)
	for _, args := range [][]string{
		{"cloudflare", "status"}, {"cloudflare", "quick"}, {"cloudflare", "disconnect"},
		{"admin", "setup-link"}, {"admin", "reset-access"}, {"admin", "firstrun"},
		{"plugin", "list"}, {"tool", "list"}, {"update"},
	} {
		code, out, errOut := cli(t, append(args, "--data-dir", dir)...)
		if code != 1 || out != "" || !strings.Contains(errOut, "cannot reach the homed") {
			t.Errorf("%v: %d %q %q", args, code, out, errOut)
		}
	}
}

func TestDataDirArg(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		dir  string
		rest []string
	}{
		{[]string{"plugin", "list", "--data-dir", "/d"}, "/d", []string{"plugin", "list"}},
		{[]string{"--data-dir=/d", "tool", "list"}, "/d", []string{"tool", "list"}},
		{[]string{"plugin", "add", "--id", "x", "--", "/bin/x", "--data-dir", "y"}, "",
			[]string{"plugin", "add", "--id", "x", "--", "/bin/x", "--data-dir", "y"}},
	} {
		dir, rest, err := dataDirArg(tc.in)
		if err != nil || dir != tc.dir || !slices.Equal(rest, tc.rest) {
			t.Errorf("%v = %q %v %v", tc.in, dir, rest, err)
		}
	}
	if _, _, err := dataDirArg([]string{"plugin", "list", "--data-dir"}); err == nil {
		t.Error("a --data-dir without a value was accepted")
	}
}

func TestPluginAddArgs(t *testing.T) {
	got, err := pluginAddArgs([]string{"--id", "x", "--", "go", "serve"})
	if err != nil || len(got) != 5 || !filepath.IsAbs(got[3]) || got[4] != "serve" {
		t.Fatalf("= %v, %v", got, err)
	}
	if _, err := pluginAddArgs([]string{"--id", "x"}); err == nil {
		t.Error("no command accepted")
	}
	if _, err := pluginAddArgs([]string{"--id", "x", "--", "no-such-binary-anywhere"}); err == nil {
		t.Error("a missing executable was accepted")
	}
}

// `homed cloudflare connect` while homed is stopped: the flow runs in this process against the fake cloudflared, the
// login link is printed (and opened on a Mac), the tunnel lands in the data dir and the mode in homed.yaml.
func TestCloudflareConnectOffline(t *testing.T) {
	dir := shortTempDir(t)
	fake := tunneltest.New(t)
	fake.Set("login-release")
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte("tunnel:\n  cloudflared: "+fake.Bin+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var opened []string
	openURL = func(u string) error { mu.Lock(); defer mu.Unlock(); opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = origOpenURL })
	code, out, errOut := cli(t, "cloudflare", "connect", "Home.Example.com", "--data-dir", dir)
	if code != 0 || !strings.Contains(out, "https://dash.cloudflare.com/") || !strings.Contains(out, "https://home.example.com/mcp") {
		t.Fatalf("connect: %d\n%s\n%s", code, out, errOut)
	}
	cfg, err := config.Load(dir)
	if err != nil || cfg.Tunnel.Mode != config.TunnelNamed || cfg.Tunnel.Hostname != "home.example.com" || cfg.Tunnel.Cloudflared != fake.Bin {
		t.Fatalf("homed.yaml = %+v, %v", cfg, err)
	}
	if _, err := os.Stat(filepath.Join(dir, config.TunnelDir, "config.yml")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := map[bool]int{true: 1, false: 0}[runtime.GOOS == "darwin"]; len(opened) != want {
		t.Errorf("opened %v", opened)
	}
	// Bad arguments are usage errors, and nothing runs.
	if code, _, _ := cli(t, "cloudflare", "connect", "--data-dir", dir); code != exitUsage {
		t.Errorf("no host: %d", code)
	}
	if code, _, _ := cli(t, "cloudflare", "connect", "a.example.com", "b.example.com", "--data-dir", dir); code != exitUsage {
		t.Errorf("two hosts: %d", code)
	}
}

// The login link is opened once, when it is complete, and only the dash.cloudflare.com one.
func TestLinkOpener(t *testing.T) {
	var opened []string
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = origOpenURL })
	var out bytes.Buffer
	o := &linkOpener{w: &out}
	for _, part := range []string{"see https://evil.example/x and\n", "  https://dash.cloudflare.com/argo", "tunnel?aud=1\n",
		"again https://dash.cloudflare.com/other\n"} {
		if _, err := o.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(out.String(), "argotunnel") {
		t.Fatal("the output was not copied")
	}
	if runtime.GOOS == "darwin" {
		if len(opened) != 1 || opened[0] != "https://dash.cloudflare.com/argotunnel?aud=1" {
			t.Fatalf("opened %v", opened)
		}
	} else if len(opened) != 0 {
		t.Fatalf("opened %v", opened)
	}
}

func TestHostGuard(t *testing.T) {
	var mu sync.Mutex
	current := "home.example.com"
	host := func() string {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	h := hostGuard(host, 8080, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	check := func(host string, want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Host %q: status %d, want %d", host, w.Code, want)
		}
		if want == http.StatusMisdirectedRequest && !strings.Contains(w.Body.String(), `"error":"misdirected"`) {
			t.Errorf("Host %q: body %s", host, w.Body)
		}
	}
	for host, want := range map[string]int{
		"home.example.com":         http.StatusTeapot,
		"HOME.example.com":         http.StatusTeapot,
		"home.example.com:18443":   http.StatusTeapot,
		"home.example.com.":        http.StatusTeapot,
		"127.0.0.1:8080":           http.StatusTeapot,
		"localhost:8080":           http.StatusTeapot,
		"[::1]:8080":               http.StatusTeapot,
		"127.0.0.1:9090":           http.StatusMisdirectedRequest,
		"127.0.0.1":                http.StatusMisdirectedRequest,
		"evil.example.com":         http.StatusMisdirectedRequest,
		"home.example.com.evil.io": http.StatusMisdirectedRequest,
		"":                         http.StatusMisdirectedRequest,
	} {
		check(host, want)
	}
	// A quick tunnel's new host is accepted at once and its old one refused; without one only loopback passes.
	mu.Lock()
	current = "example-2.trycloudflare.com"
	mu.Unlock()
	check("example-2.trycloudflare.com", http.StatusTeapot)
	check("home.example.com", http.StatusMisdirectedRequest)
	mu.Lock()
	current = ""
	mu.Unlock()
	check("example-2.trycloudflare.com", http.StatusMisdirectedRequest)
	check("127.0.0.1:8080", http.StatusTeapot)
}

func TestParseDevOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:5173":     "http://localhost:5173",
		"http://localhost:5173/":    "http://localhost:5173",
		"https://localhost:8443":    "https://localhost:8443",
		"http://localhost":          "http://localhost",
		"http://127.0.0.1:5173":     "",
		"http://home.example:5173":  "",
		"ftp://localhost:21":        "",
		"http://localhost:1/x":      "",
		"http://u@localhost:5173":   "",
		"localhost:5173":            "",
		"http://localhost:5173?q=1": "",
	} {
		site, err := parseDevOrigin(in)
		if want == "" {
			if err == nil {
				t.Errorf("parseDevOrigin(%q) accepted", in)
			}
			continue
		}
		if err != nil || site.PublicURL != want || site.RPID != "localhost" || site.ClientIP != core.ClientIPRemote {
			t.Errorf("parseDevOrigin(%q) = %+v, %v", in, site, err)
		}
	}
}

// siteOf is the Site of a tunnel's URL: its host is the relying party, the client address comes from cloudflared.
func TestSiteOf(t *testing.T) {
	s, err := siteOf("https://example-1.trycloudflare.com")
	if err != nil || s.RPID != "example-1.trycloudflare.com" || s.ClientIP != core.ClientIPCloudflare {
		t.Fatalf("= %+v, %v", s, err)
	}
	if _, err := siteOf("http://home.example.com"); err == nil {
		t.Error("a plain-http public URL was accepted")
	}
}

// origOpenURL is the opener put back after a test replaced it (TestMain makes it one that never opens a browser).
var origOpenURL = openURL
