package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"

	"github.com/devXpro/homie/internal/auth"
	"github.com/devXpro/homie/internal/config"
	"github.com/devXpro/homie/internal/firstrun"
	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/keys"
	"github.com/devXpro/homie/internal/store"
)

// Plugins built once by TestMain: internal/plugins/testdata/fakeplugin (id "fake") and pluginkit/example (id "echo").
var fakeBin, echoBin string

func TestMain(m *testing.M) {
	os.Exit(testMain(m))
}

func testMain(m *testing.M) int {
	dir, err := os.MkdirTemp("", "homed-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	// A command that misses its --data-dir must never touch the real data dir.
	if err := os.Setenv("HOMED_DATA_DIR", filepath.Join(dir, "default-data")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fakeBin, echoBin = filepath.Join(dir, "fakeplugin"), filepath.Join(dir, "homie-echo")
	for bin, pkg := range map[string]string{fakeBin: "../../internal/plugins/testdata/fakeplugin", echoBin: "../../pluginkit/example"} {
		out, err := exec.Command("go", "build", "-o", bin, pkg).CombinedOutput() //nolint:gosec // fixed test packages
		if err != nil {
			fmt.Fprintf(os.Stderr, "building %s: %v\n%s", pkg, err, out)
			return 1
		}
	}
	return m.Run()
}

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// shortTempDir is a data dir short enough for a unix socket path (t.TempDir on macOS is too long).
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "homed-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// syncBuffer collects the daemon's log or terminal; the log is printed when the test fails.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startDaemon runs serve in the background and returns once it serves; with plugins set, once they listed tools.
func startDaemon(t *testing.T, opts runOptions, plugins bool) (*daemon, func() error) {
	t.Helper()
	logs := &syncBuffer{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("homed log:\n%s", logs.String())
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan *daemon, 1)
	opts.ready = func(d *daemon) { ready <- d }
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, opts, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}()
	stop := sync.OnceValue(func() error {
		cancel()
		return <-done
	})
	var d *daemon
	select {
	case d = <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("serve: %v\n%s", err, logs.String())
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("homed did not start")
	}
	t.Cleanup(func() { _ = stop() })
	if plugins {
		waitTools(t, d, func(tools []hub.Tool) bool { return len(tools) > 0 })
	}
	return d, stop
}

// waitTools waits until the host's admin tool set satisfies cond.
func waitTools(t *testing.T, d *daemon, cond func([]hub.Tool) bool) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		changed := d.host.Changed()
		if cond(d.host.Tools(hub.AudienceAdmin)) {
			return
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("the tools never got there: %+v", d.host.Status())
		}
	}
}

// httpClient follows no redirects, so a test sees them.
var httpClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Timeout:       30 * time.Second,
}

// request sends one request; host overrides the Host header, body (when not nil) goes as JSON.
func request(t *testing.T, method, url, host string, hdr map[string]string, body any) (int, http.Header, []byte) {
	t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("X-Homie-Instance") == "" && resp.StatusCode != http.StatusMisdirectedRequest &&
		!strings.Contains(url, "/api/firstrun/") {
		t.Errorf("%s %s: no instance header", method, url)
	}
	return resp.StatusCode, resp.Header, raw
}

// get is a GET with an optional Host and bearer key.
func get(t *testing.T, url, host, key string) (int, string) {
	t.Helper()
	hdr := map[string]string{}
	if key != "" {
		hdr["Authorization"] = "Bearer " + key
	}
	status, _, body := request(t, http.MethodGet, url, host, hdr, nil)
	return status, string(body)
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %T: %v: %s", v, err, raw)
	}
	return v
}

type manifestJSON struct {
	Instructions string `json:"instructions"`
	Tools        []struct {
		Name string `json:"name"`
	} `json:"tools"`
}

func (m manifestJSON) names() string {
	var out []string
	for _, tl := range m.Tools {
		out = append(out, tl.Name)
	}
	return strings.Join(out, ",")
}

// browser is a browser on the admin: the session cookie, the CSRF header and the origin the SPA sends, and the Host
// the relay (or the dev server) gives requests.
type browser struct {
	t      *testing.T
	base   string
	host   string
	origin string
	cookie string
}

const safariMac = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Safari/605.1.15"

func (b *browser) do(method, path string, body any) (int, []byte) {
	b.t.Helper()
	hdr := map[string]string{"X-Homie": "1", "Origin": b.origin, "User-Agent": safariMac}
	if b.cookie != "" {
		hdr["Cookie"] = auth.CookieName + "=" + b.cookie
	}
	status, h, raw := request(b.t, method, b.base+path, b.host, hdr, body)
	for _, line := range h.Values("Set-Cookie") {
		if c, err := http.ParseSetCookie(line); err == nil && c.Name == auth.CookieName {
			b.cookie = c.Value
			if c.MaxAge < 0 {
				b.cookie = ""
			}
		}
	}
	return status, raw
}

// device is a phone or a Mac with one passkey (a virtual authenticator).
type device struct {
	rp   virtualwebauthn.RelyingParty
	auth virtualwebauthn.Authenticator
	cred virtualwebauthn.Credential
}

func newDevice(rpID, origin string) *device {
	return &device{
		rp: virtualwebauthn.RelyingParty{ID: rpID, Name: "Homie", Origin: origin},
		auth: virtualwebauthn.NewAuthenticatorWithOptions(virtualwebauthn.AuthenticatorOptions{
			BackupEligible: true, BackupState: true,
		}),
		cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
	}
}

// register runs the setup link's registration in the browser with the device, as the first-run wizard does.
func (b *browser) register(d *device, token string, names map[string]string) (int, []byte) {
	b.t.Helper()
	body := map[string]string{"token": token}
	for k, v := range names {
		body[k] = v
	}
	status, raw := b.do(http.MethodPost, "/api/admin/auth/register/begin", body)
	if status != http.StatusOK {
		b.t.Fatalf("register/begin: %d %s", status, raw)
	}
	opts, err := virtualwebauthn.ParseAttestationOptions(string(raw))
	if err != nil {
		b.t.Fatalf("attestation options: %v\n%s", err, raw)
	}
	d.auth.Options.UserHandle = []byte(opts.UserID)
	cred := json.RawMessage(virtualwebauthn.CreateAttestationResponse(d.rp, d.auth, d.cred, *opts))
	ceremony := decode[struct {
		CeremonyID string `json:"ceremonyId"`
	}](b.t, raw)
	return b.do(http.MethodPost, "/api/admin/auth/register/finish",
		map[string]any{"ceremonyId": ceremony.CeremonyID, "credential": cred, "passkeyName": "MacBook Pro"})
}

// linkToken runs `homed admin setup-link` and returns the link and its token.
func linkToken(t *testing.T, dir, wantPrefix string) string {
	t.Helper()
	out := strings.TrimSpace(mustCLI(t, "admin", "setup-link", "--data-dir", dir))
	token, ok := strings.CutPrefix(out, wantPrefix+"/admin/setup#t=")
	if !ok || token == "" {
		t.Fatalf("setup link %q, want %s/admin/setup#t=…", out, wantPrefix)
	}
	return token
}

var pinRe = regexp.MustCompile(`enter the PIN (\d{4} \d{4})`)

// firstRunPIN waits for the PIN homed prints to its terminal.
func firstRunPIN(t *testing.T, term *syncBuffer) string {
	t.Helper()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(10 * time.Second)
	for {
		if m := pinRe.FindStringSubmatch(term.String()); m != nil {
			return m[1]
		}
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatalf("no PIN on the terminal:\n%s", term.String())
		}
	}
}

// firstRunSignIn enters the PIN on the first-run page and returns its cookie header.
func firstRunSignIn(t *testing.T, setup, pin string) string {
	t.Helper()
	status, h, raw := request(t, http.MethodPost, setup+"/api/firstrun/pin", "", map[string]string{"X-Homie": "1"},
		map[string]string{"pin": pin})
	if status != http.StatusNoContent {
		t.Fatalf("PIN: %d %s", status, raw)
	}
	for _, line := range h.Values("Set-Cookie") {
		if c, err := http.ParseSetCookie(line); err == nil && c.Name == firstrun.CookieName {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatal("no first-run cookie")
	return ""
}

type callJSONWeb struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Progress *struct {
		Message     string            `json:"message"`
		MessageI18n map[string]string `json:"messageI18n"`
	} `json:"progress"`
	Result *struct {
		Text string `json:"text"`
	} `json:"result"`
}

// waitCall polls an admin call until cond holds.
func waitCall(t *testing.T, b *browser, id string, cond func(callJSONWeb) bool) callJSONWeb {
	t.Helper()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(15 * time.Second)
	for {
		status, raw := b.do(http.MethodGet, "/api/admin/calls/"+id, nil)
		if status != http.StatusOK {
			t.Fatalf("call %s: %d %s", id, status, raw)
		}
		c := decode[callJSONWeb](t, raw)
		if cond(c) {
			return c
		}
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatalf("call %s never got there: %s", id, raw)
		}
	}
}

// TestDaemon is the T2.6 acceptance in Go: a phase 1 data dir (relay, the fake plugin, Roman with a key) starts as a
// phase 2 homed. The import, the REST façade with the old key, the CLI over the socket, the first-run page, the owner's
// passkey from a setup link, keys and an admin tool call with progress through the admin API, the home name reaching
// the gateway, plugins added and removed at run time, the audit, and a clean shutdown.
func TestDaemon(t *testing.T) {
	dir := shortTempDir(t)
	port, relayPort := freePort(t), freePort(t)
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	oldKey, oldHash := keys.Generate()
	writeFile(t, filepath.Join(dir, config.FileName), fmt.Sprintf(`home_name: "Мой шалаш"
listen: 127.0.0.1:%d
relay:
  domain: homie.test
plugins:
  - id: fake
    command: [%q, "serve"]
people:
  - id: roman
    name: Roman
    plugins: ["*"]
    keys:
      - sha256: "%s"
        label: "ChatGPT Site"
        created: 2026-11-15T12:00:00Z
`, port, fakeBin, oldHash))
	if err := config.WriteRelayToken(dir, testToken); err != nil {
		t.Fatal(err)
	}
	term := &syncBuffer{}
	// The relay points at a closed local port: the client keeps retrying, which is all this test needs from it.
	d, stop := startDaemon(t, runOptions{
		dataDir: dir, setupListen: "127.0.0.1:0", serverAddr: "127.0.0.1:" + strconv.Itoa(relayPort), terminal: term,
	}, true)

	// The import: homed.yaml keeps listen and the relay; the rest lives in homed.db, the old file in the backup.
	if cfg, err := config.Load(dir); err != nil || cfg.HasPhase1() || cfg.Relay.Domain != "homie.test" {
		t.Fatalf("homed.yaml after the import: %+v, %v", cfg, err)
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, config.Phase1Backup)), oldHash) {
		t.Fatal("no phase 1 backup")
	}
	if st, err := os.Stat(filepath.Join(dir, config.SocketFile)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v, %v", st, err)
	}

	// The REST façade takes Roman's phase 1 key, with the relay's Host too; a foreign Host gets 421.
	if code, body := get(t, base+"/healthz", "", ""); code != http.StatusOK || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("healthz: %d %s", code, body)
	}
	if code, _ := get(t, base+"/api/v1/tools", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("tools without a key: %d", code)
	}
	code, body := get(t, base+"/api/v1/tools", "homie.test", oldKey)
	if code != http.StatusOK {
		t.Fatalf("tools with the phase 1 key: %d %s", code, body)
	}
	m := decode[manifestJSON](t, []byte(body))
	if !strings.Contains(m.names(), "fake_echo") || strings.Contains(m.names(), "fake_add") ||
		!strings.Contains(m.Instructions, `"Мой шалаш"`) || !strings.Contains(m.Instructions, "Fake instructions.") {
		t.Fatalf("manifest = %s", body)
	}
	if code, _ := get(t, base+"/healthz", "evil.test", ""); code != http.StatusMisdirectedRequest {
		t.Fatalf("foreign Host: %d", code)
	}
	// An argument named like a secret is redacted in the audit (checked at the end).
	request(t, http.MethodPost, base+"/api/v1/tools/fake_echo", "", map[string]string{"Authorization": "Bearer " + oldKey},
		map[string]string{"text": "hi", "pin": "1234"})

	// The admin SPA and its API on the same listener.
	if code, h, _ := request(t, http.MethodGet, base+"/", "", nil, nil); code != http.StatusFound || h.Get("Location") != "/admin/" {
		t.Fatalf("GET /: %d %v", code, h)
	}
	if code, _, raw := request(t, http.MethodGet, base+"/admin/", "", nil, nil); code != http.StatusOK && code != http.StatusServiceUnavailable {
		t.Fatalf("GET /admin/: %d %s", code, raw) // 503 is the hint before `make web`
	}
	code, _, raw := request(t, http.MethodGet, base+"/api/admin/bootstrap", "homie.test", nil, nil)
	if boot := decode[struct {
		HomeName   string `json:"homeName"`
		OwnerReady bool   `json:"ownerReady"`
	}](t, raw); code != http.StatusOK || boot.HomeName != "Мой шалаш" || boot.OwnerReady {
		t.Fatalf("bootstrap: %d %s", code, raw)
	}
	if code, _ := get(t, base+"/api/admin/session", "homie.test", ""); code != http.StatusUnauthorized {
		t.Fatalf("session without a cookie: %d", code)
	}

	// The CLI over the socket: keys, tools, plugins, relay.
	if list := mustCLI(t, "key", "list", "--data-dir", dir); !strings.Contains(list, "ChatGPT Site") || !strings.Contains(list, "Roman (roman)") {
		t.Fatalf("key list:\n%s", list)
	}
	cliKey := strings.TrimSpace(mustCLI(t, "key", "new", "--label", "CLI", "--data-dir", dir))
	if code, _ := get(t, base+"/api/v1/tools", "", cliKey); code != http.StatusOK {
		t.Fatalf("a key from the CLI is not accepted at once: %d", code)
	}
	if list := mustCLI(t, "tool", "list", "--data-dir", dir); !strings.Contains(list, "fake_echo") || !strings.Contains(list, "fake_add") {
		t.Fatalf("tool list:\n%s", list)
	}
	out := mustCLI(t, "tool", "call", "fake_echo", `{"text":"hello"}`, "--data-dir", dir)
	if !strings.Contains(out, "hello") || !strings.Contains(out, `"via": "cli"`) || !strings.Contains(out, `"caller": "Roman"`) {
		t.Fatalf("tool call output:\n%s", out)
	}
	// "-" reads the arguments from stdin, so a secret never reaches argv (review L10).
	if out, _, err := cli(t, `{"text":"from stdin"}`, "tool", "call", "fake_echo", "-", "--data-dir", dir); err != nil ||
		!strings.Contains(out, "from stdin") {
		t.Fatalf("tool call with stdin arguments: %v\n%s", err, out)
	}
	if _, _, err := cli(t, `[1]`, "tool", "call", "fake_echo", "-", "--data-dir", dir); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("tool call with a stdin array: %v", err)
	}
	if _, errOut, err := cli(t, "", "tool", "call", "fake_fail", "--data-dir", dir); !errors.Is(err, errToolFailed) ||
		!strings.Contains(errOut, "unreachable") {
		t.Fatalf("failing tool: %v %q", err, errOut)
	}
	if _, _, err := cli(t, "", "tool", "call", "fake_nope", "--data-dir", dir); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("unknown tool: %v", err)
	}
	if out := mustCLI(t, "plugin", "list", "--data-dir", dir); !strings.Contains(out, "up") || !strings.Contains(out, "1.0.0") {
		t.Fatalf("plugin list:\n%s", out)
	}
	if out := mustCLI(t, "relay", "status", "--data-dir", dir); !strings.Contains(out, "homie.test") || !strings.Contains(out, "self-check") {
		t.Fatalf("relay status:\n%s", out)
	}

	// The first-run page: the PIN from the terminal; the relay is configured, so it waits for the connection.
	if d.setupAddr == nil {
		t.Fatal("no first-run page while the owner has no passkey")
	}
	setup := "http://" + d.setupAddr.String()
	if code, _, raw := request(t, http.MethodGet, setup+"/api/firstrun/state", "", nil, nil); code != http.StatusOK || !strings.Contains(string(raw), `"pin"`) {
		t.Fatalf("first-run state: %d %s", code, raw)
	}
	frCookie := firstRunSignIn(t, setup, firstRunPIN(t, term))
	code, _, raw = request(t, http.MethodGet, setup+"/api/firstrun/state", "", map[string]string{"Cookie": frCookie}, nil)
	if code != http.StatusOK || !strings.Contains(string(raw), `"step":"connecting"`) || !strings.Contains(string(raw), "homie.test") {
		t.Fatalf("first-run state after the PIN: %d %s", code, raw)
	}
	code, _, raw = request(t, http.MethodPost, setup+"/api/firstrun/link", "", map[string]string{"Cookie": frCookie, "X-Homie": "1"}, struct{}{})
	if code != http.StatusConflict || !strings.Contains(string(raw), "relay_not_ready") {
		t.Fatalf("link before the relay works: %d %s", code, raw)
	}

	// The owner's passkey from `homed admin setup-link`, registered through the relay's origin.
	b := &browser{t: t, base: base, host: "homie.test", origin: "https://homie.test"}
	token := linkToken(t, dir, "https://homie.test")
	if code, raw := b.do(http.MethodPost, "/api/admin/auth/token", map[string]string{"token": token}); code != http.StatusOK ||
		!strings.Contains(string(raw), `"personName":"Roman"`) {
		t.Fatalf("auth/token: %d %s", code, raw)
	}
	if code, raw := b.register(newDevice("homie.test", "https://homie.test"), token,
		map[string]string{"personName": "Роман", "homeName": "Дача"}); code != http.StatusCreated || b.cookie == "" {
		t.Fatalf("register: %d %s", code, raw)
	}
	code, raw = b.do(http.MethodGet, "/api/admin/session", nil)
	if code != http.StatusOK || !strings.Contains(string(raw), `"id":"roman"`) || !strings.Contains(string(raw), `"role":"owner"`) ||
		!strings.Contains(string(raw), `"name":"Дача"`) || !strings.Contains(string(raw), "stepUpUntil") {
		t.Fatalf("session: %d %s", code, raw)
	}
	// The new home name reached the gateway at once, and the first-run page is done.
	if _, body := get(t, base+"/api/v1/tools", "", oldKey); !strings.Contains(body, `\"Дача\"`) {
		t.Fatalf("instructions after the setup: %s", body)
	}
	if code, _, raw := request(t, http.MethodGet, setup+"/api/firstrun/state", "", nil, nil); code != http.StatusGone {
		t.Fatalf("first-run page after the setup: %d %s", code, raw)
	}

	// A key through the admin (the registration counts as step-up), used on the REST façade, then revoked.
	code, raw = b.do(http.MethodPost, "/api/admin/people/roman/keys", map[string]string{"label": "Web"})
	if code != http.StatusCreated {
		t.Fatalf("create a key: %d %s", code, raw)
	}
	created := decode[struct {
		Key struct {
			ID string `json:"id"`
		} `json:"key"`
		Secret string `json:"secret"`
	}](t, raw)
	if code, _ := get(t, base+"/api/v1/tools", "homie.test", created.Secret); code != http.StatusOK {
		t.Fatalf("the new key: %d", code)
	}
	if code, raw := b.do(http.MethodDelete, "/api/admin/keys/"+created.Key.ID, nil); code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", code, raw)
	}
	if code, _ := get(t, base+"/api/v1/tools", "homie.test", created.Secret); code != http.StatusUnauthorized {
		t.Fatalf("the revoked key: %d", code)
	}
	// The key list shows when a key was last used.
	if code, raw := b.do(http.MethodGet, "/api/admin/keys?person=roman", nil); code != http.StatusOK || !strings.Contains(string(raw), "lastUsedAt") {
		t.Fatalf("keys: %d %s", code, raw)
	}

	// An admin tool call with progress: it waits for fake_release, which the CLI sends.
	code, raw = b.do(http.MethodPost, "/api/admin/tools/fake_progress/calls", map[string]any{"args": map[string]any{}})
	if code != http.StatusAccepted {
		t.Fatalf("start a call: %d %s", code, raw)
	}
	call := decode[callJSONWeb](t, raw)
	waitCall(t, b, call.ID, func(c callJSONWeb) bool { return c.Progress != nil && c.Progress.Message != "" })
	mustCLI(t, "tool", "call", "fake_release", "--data-dir", dir)
	done := waitCall(t, b, call.ID, func(c callJSONWeb) bool { return c.State != "running" })
	if done.State != "ok" || done.Result == nil || done.Result.Text != "released" {
		t.Fatalf("finished call: %+v", done)
	}

	// The owner renames the home: the gateway follows at once.
	if code, raw := b.do(http.MethodPatch, "/api/admin/settings", map[string]string{"homeName": "Дача 2"}); code != http.StatusOK {
		t.Fatalf("rename: %d %s", code, raw)
	}
	if _, body := get(t, base+"/api/v1/tools", "", oldKey); !strings.Contains(body, `\"Дача 2\"`) {
		t.Fatalf("instructions after the rename: %s", body)
	}

	// Plugins added and removed while homed runs.
	mustCLI(t, "plugin", "add", "--id", "echo", "--data-dir", dir, "--", echoBin)
	waitTools(t, d, func(tools []hub.Tool) bool { return strings.Contains(fmt.Sprint(tools), "echo_say") })
	if out := mustCLI(t, "tool", "call", "echo_say", `{"text":"hi"}`, "--data-dir", dir); !strings.Contains(out, "hi") {
		t.Fatalf("echo_say: %s", out)
	}
	mustCLI(t, "plugin", "remove", "echo", "--data-dir", dir)
	if st := d.host.Status(); len(st) != 1 || st[0].ID != "fake" {
		t.Fatalf("plugins after remove: %+v", st)
	}

	// A second homed on the same data dir is refused and leaves the first one's socket alone.
	err := serve(context.Background(), runOptions{dataDir: dir, listen: "127.0.0.1:" + strconv.Itoa(freePort(t))}, discardLog())
	if !errors.Is(err, config.ErrAlreadyRunning) {
		t.Fatalf("second homed: %v", err)
	}
	if out := mustCLI(t, "plugin", "list", "--data-dir", dir); !strings.Contains(out, "fake") {
		t.Fatalf("the first homed's socket after the refused second start:\n%s", out)
	}

	if err := stop(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, config.SocketFile)); !os.IsNotExist(err) {
		t.Fatalf("socket left behind: %v", err)
	}
	checkAudit(t, dir)
}

// checkAudit expects the calls and the admin actions of TestDaemon in homed.db, with secrets redacted.
func checkAudit(t *testing.T, dir string) {
	t.Helper()
	recs, err := openDB(t, dir).Audit().Query(context.Background(), store.AuditFilter{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range recs {
		seen[string(r.Kind)+" "+r.Via+" "+r.Tool+r.Action] = true
		if r.Tool == "fake_echo" && r.Via == "site" && (strings.Contains(string(r.Args), "1234") || !strings.Contains(string(r.Args), "[redacted]")) {
			t.Errorf("args not redacted: %s", r.Args)
		}
	}
	for _, want := range []string{
		"tool site fake_echo", "tool cli fake_echo", "tool cli fake_fail", "tool admin fake_progress", "tool cli echo_say",
		"admin cli setup.link", "admin admin auth.setup", "admin cli key.create", "admin admin key.create",
		"admin admin key.revoke", "admin cli plugin.install", "admin cli plugin.remove", "admin admin settings.update",
	} {
		if !seen[want] {
			t.Errorf("audit lacks %q; has %v", want, seen)
		}
	}
}

// A relay that fails before it ever connects (here: the CA file is missing) shows why in `homed relay status`.
func TestRelayConfigErrorInStatus(t *testing.T) {
	dir := shortTempDir(t)
	connectRelay(t, dir)
	d, _ := startDaemon(t, runOptions{
		dataDir: dir, listen: "127.0.0.1:" + strconv.Itoa(freePort(t)), setupListen: "127.0.0.1:0",
		caFile: filepath.Join(dir, "missing-ca.pem"),
	}, false)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(10 * time.Second)
	for {
		out := mustCLI(t, "relay", "status", "--data-dir", dir)
		if strings.Contains(out, "last error: read relay CA file") && d.relay.Status().LastError != "" {
			return
		}
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatalf("relay status never showed the config error:\n%s", out)
		}
	}
}

// A connect code given to a running homed (first-run page, admin, CLI) switches the relay at once: the Host guard,
// the passkey relying party and the admin's origin follow without a restart.
func TestRelayConnectAtRunTime(t *testing.T) {
	dir := shortTempDir(t)
	port := freePort(t)
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	d, _ := startDaemon(t, runOptions{
		dataDir: dir, listen: "127.0.0.1:" + strconv.Itoa(port), setupListen: "127.0.0.1:0",
		serverAddr: "127.0.0.1:" + strconv.Itoa(freePort(t)),
	}, false)
	b := &browser{t: t, base: base, host: "homie.test", origin: "https://homie.test"}
	if code, _ := get(t, base+"/healthz", "homie.test", ""); code != http.StatusMisdirectedRequest {
		t.Fatalf("Host before the relay: %d", code)
	}
	if code, raw := (&browser{t: t, base: base, origin: "https://homie.test"}).do(http.MethodPost, "/api/admin/auth/login/begin", struct{}{}); code != http.StatusForbidden {
		t.Fatalf("a write with an origin before the relay: %d %s", code, raw)
	}

	out := connectRelay(t, dir)
	if !strings.Contains(out, "connecting") || d.relay.Domain() != "homie.test" {
		t.Fatalf("relay connect: %s (domain %q)", out, d.relay.Domain())
	}
	if cfg, err := config.Load(dir); err != nil || cfg.Relay.Domain != "homie.test" {
		t.Fatalf("homed.yaml: %+v, %v", cfg, err)
	}
	if tok, err := config.ReadRelayToken(dir); err != nil || tok != testToken {
		t.Fatalf("relay-token: %v", err)
	}
	if code, _ := get(t, base+"/healthz", "homie.test", ""); code != http.StatusOK {
		t.Fatalf("Host after the relay: %d", code)
	}
	if code, raw := b.do(http.MethodPost, "/api/admin/auth/login/begin", struct{}{}); code != http.StatusOK || !strings.Contains(string(raw), `"rpId":"homie.test"`) {
		t.Fatalf("login/begin after the relay: %d %s", code, raw)
	}
	if out := mustCLI(t, "relay", "status", "--data-dir", dir); !strings.Contains(out, "relay:      homie.test") {
		t.Fatalf("relay status:\n%s", out)
	}
	// The first-run page sees the relay too: the link waits for the connection and the self-check.
	if d.setupAddr == nil {
		t.Fatal("no first-run page")
	}
}

// --dev-origin: passkeys and setup links for localhost over plain http, and no relay at all.
func TestDevMode(t *testing.T) {
	relayDir := shortTempDir(t)
	connectRelay(t, relayDir)
	err := serve(context.Background(), runOptions{
		dataDir: relayDir, listen: "127.0.0.1:" + strconv.Itoa(freePort(t)), devOrigin: "http://localhost:5173",
	}, discardLog())
	if err == nil || !strings.Contains(err.Error(), "--dev-origin") {
		t.Fatalf("dev origin with a relay: %v", err)
	}
	if err := serve(context.Background(), runOptions{dataDir: shortTempDir(t), devOrigin: "http://evil.test"}, discardLog()); err == nil {
		t.Fatal("a dev origin that is not localhost was accepted")
	}

	dir := shortTempDir(t)
	port := freePort(t)
	origin := "http://localhost:" + strconv.Itoa(port)
	term := &syncBuffer{}
	d, _ := startDaemon(t, runOptions{
		dataDir: dir, listen: "127.0.0.1:" + strconv.Itoa(port), setupListen: "127.0.0.1:0", devOrigin: origin, terminal: term,
	}, false)
	if !strings.Contains(term.String(), "homed admin setup-link") {
		t.Fatalf("terminal:\n%s", term.String())
	}
	// The first-run page has no relay to connect in development mode.
	setup := "http://" + d.setupAddr.String()
	frCookie := firstRunSignIn(t, setup, firstRunPIN(t, term))
	code, _, raw := request(t, http.MethodPost, setup+"/api/firstrun/relay", "",
		map[string]string{"Cookie": frCookie, "X-Homie": "1"}, map[string]string{"code": testCode(t, "homie.test")})
	if code != http.StatusConflict || !strings.Contains(string(raw), "development mode") {
		t.Fatalf("first-run relay in development mode: %d %s", code, raw)
	}

	b := &browser{t: t, base: "http://127.0.0.1:" + strconv.Itoa(port), host: "localhost:" + strconv.Itoa(port), origin: origin}
	token := linkToken(t, dir, origin)
	if code, raw := b.register(newDevice("localhost", origin), token, map[string]string{"personName": "Dev", "homeName": "Dev home"}); code != http.StatusCreated {
		t.Fatalf("register on localhost: %d %s", code, raw)
	}
	if code, raw := b.do(http.MethodGet, "/api/admin/relay", nil); code != http.StatusOK || !strings.Contains(string(raw), "not_configured") {
		t.Fatalf("relay in development mode: %d %s", code, raw)
	}
	if code, raw := b.do(http.MethodPut, "/api/admin/relay", map[string]string{"code": testCode(t, "homie.test")}); code != http.StatusServiceUnavailable {
		t.Fatalf("relay change in development mode: %d %s", code, raw)
	}
	if _, _, err := cli(t, testCode(t, "homie.test")+"\n", "relay", "connect", "-", "--data-dir", dir); err == nil {
		t.Fatal("relay connect in development mode succeeded")
	}
}

// After a hub update homed shuts down cleanly and serve returns errRestart; main exits with 75 for the service manager.
func TestRestartAfterHubUpdate(t *testing.T) {
	dir := shortTempDir(t)
	ready := make(chan *daemon, 1)
	done := make(chan error, 1)
	go func() {
		done <- serve(context.Background(), runOptions{
			dataDir: dir, listen: "127.0.0.1:" + strconv.Itoa(freePort(t)), setupListen: "127.0.0.1:0",
			ready: func(d *daemon) { ready <- d },
		}, discardLog())
	}()
	var d *daemon
	select {
	case d = <-ready:
	case err := <-done:
		t.Fatalf("serve: %v", err)
	}
	d.requestRestart()
	d.requestRestart() // only the first counts
	select {
	case err := <-done:
		if !errors.Is(err, errRestart) {
			t.Fatalf("serve after a restart request: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("homed did not stop")
	}
	if _, err := os.Stat(filepath.Join(dir, config.SocketFile)); !os.IsNotExist(err) {
		t.Fatalf("socket left behind: %v", err)
	}
}
