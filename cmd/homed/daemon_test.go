package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/devXpro/mcp-gatekeeper/adminsock"
	"github.com/devXpro/mcp-gatekeeper/auth"
	"github.com/devXpro/mcp-gatekeeper/core"
	"github.com/devXpro/mcp-gatekeeper/tunnel/cloudflare"

	"github.com/devXpro/homie/internal/config"
	"github.com/devXpro/homie/internal/store"
	"github.com/devXpro/homie/internal/tunnel/tunneltest"
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
	// No test may ever find a real cloudflared (it would talk to Cloudflare): PATH keeps only go and git.
	safe := filepath.Join(dir, "path")
	if err := os.Mkdir(safe, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, tool := range []string{"go", "git"} {
		if p, err := exec.LookPath(tool); err == nil {
			_ = os.Symlink(p, filepath.Join(safe, tool))
		}
	}
	if err := os.Setenv("PATH", safe); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Nor open a browser: tests that check the opener replace it and put this one back.
	origOpenURL = func(string) error { return errors.New("tests never open a browser") }
	openURL = origOpenURL
	return tunneltest.Main(m)
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

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: a test's own file
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
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

// fakeClock is the gatekeeper's time in the daemon: now, until a test moves it on.
type fakeClock struct {
	mu  sync.Mutex
	off time.Duration
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.off)
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.off += d
}

// home is one homed under test: its data dir, its listener and the fake cloudflared it runs.
type home struct {
	t     *testing.T
	dir   string
	addr  string // the public listener, 127.0.0.1:<port>
	fake  *tunneltest.Fake
	clock *fakeClock
	term  *syncBuffer
	logs  *syncBuffer
	d     *daemon
	stop  func() error
}

// newHome prepares a data dir: homed.yaml with the listener and the fake cloudflared, plus yaml, and the plugins fake
// and echo in homie.db.
func newHome(t *testing.T, yaml string) *home {
	t.Helper()
	h := &home{t: t, dir: shortTempDir(t), fake: tunneltest.New(t), clock: &fakeClock{}, term: &syncBuffer{}, logs: &syncBuffer{}}
	h.addr = "127.0.0.1:" + strconv.Itoa(freePort(t))
	cfg := "listen: " + h.addr + "\ntunnel:\n  cloudflared: " + h.fake.Bin + "\n" + yaml
	if err := os.WriteFile(filepath.Join(h.dir, config.FileName), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return h
}

// withPlugins stores the plugins fake and echo, which homed starts.
func (h *home) withPlugins() *home {
	h.t.Helper()
	st, err := store.Open(context.Background(), store.Options{Path: filepath.Join(h.dir, config.DBFile)})
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	for id, bin := range map[string]string{"fake": fakeBin, "echo": echoBin} {
		if err := st.Plugins().Create(context.Background(), store.Plugin{ID: id, Source: store.PluginLocal,
			Command: []string{bin, "serve"}, Enabled: true}); err != nil {
			h.t.Fatal(err)
		}
	}
	return h
}

// connected writes a named tunnel for host into the tunnel directory, as `homed cloudflare connect` leaves it.
func (h *home) connected(host string) *home {
	h.t.Helper()
	dir := filepath.Join(h.dir, config.TunnelDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		h.t.Fatal(err)
	}
	const id = "6ff42ae2-765d-4adf-8112-31c55c1551ef"
	files := map[string]string{
		cloudflare.ConfigFile:      "tunnel: " + id + "\ningress:\n  - hostname: " + host + "\n    service: http://" + h.addr + "\n  - service: http_status:404\n",
		cloudflare.CredentialsFile: `{"TunnelID":"` + id + `"}`,
		cloudflare.RecordFile:      `{"name":"homie-test","id":"` + id + `","hostname":"` + host + `"}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			h.t.Fatal(err)
		}
	}
	return h
}

// start runs serve and returns once it serves. The first-run page takes free loopback ports, never 8090 or the home
// network: a test tells its two defaults apart by the port.
func (h *home) start(extra ...func(*runOptions)) *home {
	h.t.Helper()
	opts := runOptions{dataDir: h.dir, terminal: h.term, clock: h.clock.now, probeClient: tunneltest.Edge(h.addr, ""),
		listenLAN: "127.0.0.1:0", listenLocal: "127.0.0.1:0"}
	for _, f := range extra {
		f(&opts)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan *daemon, 1)
	opts.ready = func(d *daemon) { ready <- d }
	finished := make(chan struct{})
	var serveErr error
	logs := h.logs
	go func() {
		defer close(finished)
		serveErr = serve(ctx, opts, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}()
	h.stop = sync.OnceValue(func() error {
		cancel()
		<-finished
		return serveErr
	})
	stop := h.stop
	h.t.Cleanup(func() {
		_ = stop()
		if h.t.Failed() {
			h.t.Logf("homed log:\n%s", logs.String())
		}
	})
	select {
	case h.d = <-ready:
	case <-finished:
		h.t.Fatalf("serve: %v\n%s", serveErr, logs.String())
	case <-time.After(30 * time.Second):
		h.t.Fatal("homed did not start")
	}
	return h
}

// waitFor polls cond for up to 30 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// browser is a browser at https://<host>, reaching homed the way Cloudflare does.
type browser struct {
	t       *testing.T
	origin  string
	client  *http.Client
	cookies map[string]string
	consent map[string]any // the last consent screen connect saw (GET /api/oauth/txn/{txn})
}

func (h *home) browser(host string) *browser {
	c := tunneltest.Edge(h.addr, "198.51.100.7")
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &browser{t: h.t, origin: "https://" + host, client: c, cookies: map[string]string{}}
}

func (b *browser) do(method, path string, body any) (int, http.Header, []byte) {
	b.t.Helper()
	var rd io.Reader
	contentType := "application/json"
	switch v := body.(type) {
	case nil:
	case url.Values:
		rd, contentType = strings.NewReader(v.Encode()), "application/x-www-form-urlencoded"
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			b.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, b.origin+path, rd)
	if err != nil {
		b.t.Fatal(err)
	}
	if rd != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if method != http.MethodGet {
		req.Header.Set("X-Homie", "1")
		req.Header.Set("Origin", b.origin)
	}
	var cookies []string
	for name, value := range b.cookies {
		cookies = append(cookies, name+"="+value)
	}
	if len(cookies) > 0 {
		req.Header.Set("Cookie", strings.Join(cookies, "; "))
	}
	res, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	for _, c := range res.Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c.Value
		}
	}
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, raw
}

func (b *browser) json(method, path string, body any, want int) map[string]any {
	b.t.Helper()
	code, _, raw := b.do(method, path, body)
	if code != want {
		b.t.Fatalf("%s %s: %d %s", method, path, code, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// device is a phone or a Mac with one passkey of host.
type device struct {
	rp   virtualwebauthn.RelyingParty
	auth virtualwebauthn.Authenticator
	cred virtualwebauthn.Credential
}

func newDevice(host string) *device {
	return &device{
		rp:   virtualwebauthn.RelyingParty{ID: host, Name: "Homie", Origin: "https://" + host},
		auth: virtualwebauthn.NewAuthenticatorWithOptions(virtualwebauthn.AuthenticatorOptions{BackupEligible: true, BackupState: true}),
		cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
	}
}

// register registers the device with a setup link or invite token, as name, and signs the browser in.
func (b *browser) register(d *device, token, name, accountName string) {
	b.t.Helper()
	begun := b.json("POST", "/api/auth/register/begin", map[string]string{"token": token, "name": name,
		"account_name": accountName}, 200)
	options, _ := json.Marshal(begun["options"])
	opts, err := virtualwebauthn.ParseAttestationOptions(string(options))
	if err != nil {
		b.t.Fatal(err)
	}
	d.auth.Options.UserHandle = []byte(opts.UserID)
	cred := json.RawMessage(virtualwebauthn.CreateAttestationResponse(d.rp, d.auth, d.cred, *opts))
	b.json("POST", "/api/auth/register/finish", map[string]any{"credential": cred}, 200)
}

// tokenOf is the #t= token of a setup link or invite.
func tokenOf(t *testing.T, link string) string {
	t.Helper()
	_, tok, ok := strings.Cut(link, "#t=")
	if !ok {
		t.Fatalf("no token in %q", link)
	}
	return tok
}

// setupLink is `homed admin setup-link`: the link alone on stdout.
func (h *home) setupLink() string {
	h.t.Helper()
	code, out, errOut := cli(h.t, "admin", "setup-link", "--data-dir", h.dir)
	link := strings.TrimSpace(out)
	if code != 0 || strings.Count(out, "\n") != 1 || !strings.Contains(link, "/setup#t=") || !strings.Contains(errOut, "works once") {
		h.t.Fatalf("setup-link: %d %q %q", code, out, errOut)
	}
	return link
}

// claudeCallback is the redirect URI of a Claude connector.
const claudeCallback = "https://claude.ai/api/mcp/auth_callback"

// connector is an assistant's connector, as its OAuth client.
type connector struct {
	clientID, access, refresh string
}

// connect walks an assistant's OAuth flow for the signed-in browser b (DCR with Claude's callback, authorize, the
// consent, the code exchange).
func (b *browser) connect(resource string) connector {
	b.t.Helper()
	callback := claudeCallback
	reg := b.json("POST", "/oauth/register", map[string]any{"redirect_uris": []string{callback},
		"token_endpoint_auth_method": "none", "client_name": "Claude"}, http.StatusCreated)
	clientID, _ := reg["client_id"].(string)
	verifier := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("v", 40)))
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {callback}, "state": {"st"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"scope": {"mcp offline_access"}, "resource": {resource}}
	code, h, raw := b.do("GET", "/oauth/authorize?"+q.Encode(), nil)
	txn, ok := strings.CutPrefix(h.Get("Location"), "/consent?txn=")
	if code != http.StatusFound || !ok {
		b.t.Fatalf("authorize: %d %v %s", code, h, raw)
	}
	b.consent = b.json("GET", "/api/oauth/txn/"+txn, nil, 200)
	back := b.json("POST", "/api/oauth/txn/"+txn+"/approve", map[string]any{}, 200)
	u, err := url.Parse(back["redirect_url"].(string))
	if err != nil {
		b.t.Fatal(err)
	}
	tok := b.json("POST", "/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {u.Query().Get("code")},
		"redirect_uri": {callback}, "code_verifier": {verifier}, "client_id": {clientID}}, 200)
	c := connector{clientID: clientID}
	c.access, _ = tok["access_token"].(string)
	c.refresh, _ = tok["refresh_token"].(string)
	if c.access == "" || c.refresh == "" {
		b.t.Fatalf("no tokens: %v", tok)
	}
	return c
}

// refreshed is the connector's refresh grant: the status and, on success, the new tokens.
func (b *browser) refreshed(c connector) (int, connector) {
	b.t.Helper()
	code, _, raw := b.do("POST", "/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.refresh},
		"client_id": {c.clientID}})
	var tok map[string]any
	_ = json.Unmarshal(raw, &tok)
	c.access, _ = tok["access_token"].(string)
	c.refresh, _ = tok["refresh_token"].(string)
	return code, c
}

// e2eHost is the public host of the end-to-end test.
const e2eHost = "home.example.com"

// mcpSession connects an MCP client to https://<e2eHost>/mcp with token.
func (h *home) mcpSession(token string) (*mcp.ClientSession, error) {
	h.t.Helper()
	edge := tunneltest.Edge(h.addr, "198.51.100.8")
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:             "https://" + e2eHost + "/mcp",
		HTTPClient:           &http.Client{Transport: bearer{token: token, base: edge.Transport}},
		DisableStandaloneSSE: true, MaxRetries: -1,
	}
	cs, err := client.Connect(h.t.Context(), transport, nil)
	if err == nil {
		h.t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tool := range res.Tools {
		out = append(out, tool.Name)
	}
	return out
}

// TestGuestEndToEnd is H2's acceptance in one process: the setup link → a virtual passkey → an invite of a guest
// with plugin:fake and a 1-hour term → the guest's OAuth flow with a fake Claude client → tools/list shows only the
// fake plugin's tools → a call reaches the plugin → after the term, 401. The owner sees every tool; a token cannot be
// asked for another resource; no /api/v1 route is left.
func TestGuestEndToEnd(t *testing.T) {
	const host = e2eHost
	h := newHome(t, "  mode: named\n  hostname: "+host+"\n").withPlugins().connected(host).start()
	waitFor(t, "the tunnel's self-check", func() bool { return h.d.tun.Status().State == "connected" })
	waitFor(t, "the plugins", func() bool { return len(h.d.host.Tools("admin")) > 0 })

	// The owner: the setup link from the home box, then a passkey.
	alice := h.browser(host)
	if b := alice.json("GET", "/api/home/bootstrap", nil, 200); b["ownerReady"] != false {
		t.Fatalf("bootstrap before the owner: %v", b)
	}
	alice.register(newDevice(host), tokenOf(t, h.setupLink()), "Alice", "Дом")
	if b := alice.json("GET", "/api/home/bootstrap", nil, 200); b["ownerReady"] != true || b["homeName"] != "Дом" {
		t.Fatalf("bootstrap: %v", b)
	}
	acct := alice.json("GET", "/api/account", nil, 200)
	res := acct["resources"].([]any)[0].(map[string]any)
	if res["mcp_url"] != "https://"+host+"/mcp" || len(acct["devices"].([]any)) != 2 {
		t.Fatalf("account: %v", acct)
	}
	// "Delete everything" is not for the home: its data goes with an uninstall on the home box.
	if code, _, raw := alice.do("DELETE", "/api/account", map[string]any{"confirm": "Дом"}); code != http.StatusForbidden ||
		!strings.Contains(string(raw), "uninstall homed") {
		t.Fatalf("DELETE /api/account: %d %s", code, raw)
	}
	// The permissions an invite can give besides "all": one per plugin.
	if code, _, raw := alice.do("GET", "/api/account/permissions", nil); code != 200 || !strings.Contains(string(raw), `"plugin:fake"`) ||
		!strings.Contains(string(raw), `"plugin:echo"`) || strings.Contains(string(raw), `"all"`) {
		t.Fatalf("permissions: %d %s", code, raw)
	}

	// A guest: the fake plugin, for an hour.
	until := h.clock.now().Add(time.Hour).UTC().Format(time.RFC3339)
	inv := alice.json("POST", "/api/account/invites", map[string]any{"name": "Bella", "permissions": []string{"plugin:fake"},
		"access_until": until}, 200)
	bella := h.browser(host)
	bella.register(newDevice(host), tokenOf(t, inv["url"].(string)), "Bella", "")
	if me := bella.json("GET", "/api/me", nil, 200); me["person"].(map[string]any)["role"] != "member" {
		t.Fatalf("me: %v", me)
	}
	if devs := bella.json("GET", "/api/account", nil, 200)["devices"].([]any); len(devs) != 1 ||
		devs[0].(map[string]any)["id"] != "fake" {
		t.Fatalf("the guest's devices: %v", devs)
	}

	// The guest's connector sees the fake plugin only, and a call reaches it with the guest's name.
	guest := bella.connect("https://" + host + "/mcp")
	// The guest's consent screen names only the fake plugin's tools (review R-4).
	if tools := consentTools(t, bella); len(tools) == 0 || slices.ContainsFunc(tools, func(n string) bool { return !strings.HasPrefix(n, "fake_") }) {
		t.Fatalf("the guest's consent screen lists %v", tools)
	}
	cs, err := h.mcpSession(guest.access)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range toolNames(t, cs) {
		if !strings.HasPrefix(name, "fake_") {
			t.Fatalf("the guest sees %s", name)
		}
	}
	call, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "fake_echo", Arguments: map[string]any{"text": "hi"}})
	if err != nil || call.IsError {
		t.Fatalf("fake_echo: %v %+v", err, call)
	}
	if b, _ := json.Marshal(call.StructuredContent); !strings.Contains(string(b), `"caller":"Bella"`) || !strings.Contains(string(b), `"via":"claude"`) {
		t.Fatalf("the plugin saw %s", b)
	}
	if res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo_say", Arguments: map[string]any{"text": "x"}}); err == nil && !res.IsError {
		t.Fatal("the guest called a tool outside their permissions")
	}

	// The owner sees every tool.
	owner := alice.connect("https://" + host + "/mcp")
	if tools := consentTools(t, alice); !slices.ContainsFunc(tools, func(n string) bool { return strings.HasPrefix(n, "echo_") }) {
		t.Fatalf("the owner's consent screen lists %v", tools)
	}
	ocs, err := h.mcpSession(owner.access)
	if err != nil {
		t.Fatal(err)
	}
	names := toolNames(t, ocs)
	if !slices.ContainsFunc(names, func(n string) bool { return strings.HasPrefix(n, "echo_") }) ||
		!slices.ContainsFunc(names, func(n string) bool { return strings.HasPrefix(n, "fake_") }) {
		t.Fatalf("the owner sees %v", names)
	}

	// A token for another resource is never handed out, and refused at /mcp; /mcp/<anything> is not the home.
	for want, q := range map[string]url.Values{
		"invalid_target": {"resource": {"https://" + host + "/mcp/other"}},
		"invalid_scope":  {"resource": {"https://" + host + "/mcp"}, "scope": {"mcp:other"}},
	} {
		if got := alice.authorizeError(owner.clientID, q); got != want {
			t.Errorf("authorize %v: %q, want %q", q, got, want)
		}
	}
	other := h.tokenForAnotherResource(owner.access)
	if _, err := h.mcpSession(other); err == nil {
		t.Fatal("a token for another resource was accepted at /mcp")
	}
	if code := mcpStatus(t, h, host, "/mcp/other", owner.access); code != http.StatusNotFound {
		t.Fatalf("/mcp/other: %d", code)
	}
	// The REST façade of the ChatGPT Sites is gone.
	if code, _, _ := alice.do("GET", "/api/v1/tools", nil); code != http.StatusNotFound {
		t.Fatalf("/api/v1/tools: %d", code)
	}

	// The term ends: the guest's token is refused with 401, and so are their refresh and their session. The owner's
	// access token ran out too (an hour); the refresh gives a new one.
	h.clock.add(time.Hour + time.Second)
	if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "fake_echo", Arguments: map[string]any{"text": "late"}}); err == nil {
		t.Fatal("the guest's call went through after the term")
	}
	if code := mcpStatus(t, h, host, "/mcp", guest.access); code != http.StatusUnauthorized {
		t.Fatalf("the guest after the term: %d", code)
	}
	if code, _ := bella.refreshed(guest); code != http.StatusBadRequest {
		t.Fatalf("the guest's refresh after the term: %d", code)
	}
	if code, _, _ := bella.do("GET", "/api/me", nil); code != http.StatusUnauthorized {
		t.Fatalf("the guest's session after the term: %d", code)
	}
	code, owner := alice.refreshed(owner)
	if code != http.StatusOK {
		t.Fatalf("the owner's refresh: %d", code)
	}
	if ocs, err = h.mcpSession(owner.access); err != nil {
		t.Fatal(err)
	}
	if res, err := ocs.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo_say", Arguments: map[string]any{"text": "x"}}); err != nil || res.IsError {
		t.Fatalf("the owner after the guest's term: %v %+v", err, res)
	}
	// Every assistant call is in the activity log, with its surface.
	waitFor(t, "the activity", func() bool {
		recs, _ := h.d.st.Activity().Query(t.Context(), store.ActivityFilter{Via: []string{"claude"}})
		return len(recs) >= 2
	})
}

// authorizeError starts an authorization for client with q over the usual parameters and returns the error the
// gatekeeper sends back to the client's callback ("" when it went on to the consent).
func (b *browser) authorizeError(clientID string, q url.Values) string {
	b.t.Helper()
	sum := sha256.Sum256([]byte(strings.Repeat("v", 43)))
	base := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {claudeCallback},
		"state": {"st"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"}, "scope": {"mcp offline_access"}}
	for k, v := range q {
		base[k] = v
	}
	code, h, raw := b.do("GET", "/oauth/authorize?"+base.Encode(), nil)
	loc, err := url.Parse(h.Get("Location"))
	if code != http.StatusFound || err != nil {
		b.t.Fatalf("authorize: %d %v %s", code, h, raw)
	}
	if strings.HasPrefix(loc.Path, "/consent") {
		return ""
	}
	return loc.Query().Get("error")
}

// tokenForAnotherResource stores an access token like access (the same person, account and client) but for the
// resource "other": the OAuth server never issues one, a bug or a second resource could.
func (h *home) tokenForAnotherResource(access string) string {
	h.t.Helper()
	ctx := h.t.Context()
	sum := sha256.Sum256([]byte(access))
	tok, err := h.d.st.OAuth().GetToken(ctx, sum[:])
	if err != nil {
		h.t.Fatal(err)
	}
	g, err := h.d.st.OAuth().GetGrant(ctx, tok.GrantID)
	if err != nil {
		h.t.Fatal(err)
	}
	g.ID, g.ResourceID = "grt_other", "other"
	if err := h.d.st.OAuth().PutGrant(ctx, g); err != nil {
		h.t.Fatal(err)
	}
	const other = "access-token-for-another-resource"
	sum = sha256.Sum256([]byte(other))
	now := h.clock.now()
	if err := h.d.st.OAuth().PutToken(ctx, core.Token{Hash: sum[:], GrantID: g.ID, Kind: core.TokenAccess, Family: "other",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		h.t.Fatal(err)
	}
	return other
}

// mcpStatus is the status of an MCP tools/list at path with token, as an assistant sends it.
func mcpStatus(t *testing.T, h *home, host, path, token string) int {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://"+host+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err := tunneltest.Edge(h.addr, "").Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized && !strings.Contains(res.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("a 401 without a Bearer challenge: %v", res.Header)
	}
	return res.StatusCode
}

// A v0.1 data dir moves over at the first start: homed.yaml loses the relay (kept as homed.yaml.v0.1), the relay
// token goes, the old database is imported (plugins, home name, language) and kept as homed.db.v0.1; the plugins run;
// the first-run page stays off the home network; nobody can sign in until the setup link.
func TestMigrationFromV01(t *testing.T) {
	h := newHome(t, "")
	v01 := "listen: " + h.addr + "\nrelay:\n  domain: relay.example.com\n"
	if err := os.WriteFile(filepath.Join(h.dir, config.FileName), []byte(v01), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "relay-token"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeV01DB(t, filepath.Join(h.dir, config.OldDBFile))
	h.start(func(o *runOptions) { o.setupListen = "127.0.0.1:0" }) // homed.yaml now says 127.0.0.1:8090

	if _, err := os.Stat(filepath.Join(h.dir, "relay-token")); !os.IsNotExist(err) {
		t.Errorf("relay-token: %v", err)
	}
	cfg, err := config.Load(h.dir)
	if err != nil || cfg.HasRelay() || cfg.Tunnel.Mode != config.TunnelOff || cfg.SetupListen != "127.0.0.1:8090" {
		t.Fatalf("homed.yaml: %+v %v", cfg, err)
	}
	for _, f := range []string{config.V01Backup, config.OldDBFile + store.V01Suffix} {
		fi, err := os.Stat(filepath.Join(h.dir, f))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", f, fi, err)
		}
	}
	waitFor(t, "the imported plugins", func() bool { return len(h.d.host.Tools("admin")) > 0 })
	if hs, _ := h.d.st.Settings().Get(t.Context()); hs.HomeName != "Дом" || hs.Language != "en" {
		t.Fatalf("settings: %+v", hs)
	}
	// A second start finds nothing to move.
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	h.logs = &syncBuffer{}
	h.start(func(o *runOptions) { o.setupListen = "127.0.0.1:0" }) // homed.yaml now says 127.0.0.1:8090
	if strings.Contains(h.logs.String(), "imported the v0.1 database") || strings.Contains(h.logs.String(), "moved homed.yaml") {
		t.Fatalf("a second start migrated again:\n%s", h.logs.String())
	}
	// The first-run page: on loopback (the home had a relay), with nothing to connect yet.
	h.d.mu.Lock()
	urls := h.d.firstRun.urls
	h.d.mu.Unlock()
	if len(urls) != 1 || !strings.HasPrefix(urls[0], "http://127.0.0.1:") {
		t.Fatalf("first-run page: %v", urls)
	}
}

// writeV01DB makes a v0.1.1 homed.db: the schema of internal/store/testdata/homed-v0.1.1.sql with neutral rows, its
// plugins the test builds.
func writeV01DB(t *testing.T, path string) {
	t.Helper()
	schema, err := os.ReadFile(filepath.Join("..", "..", "internal", "store", "testdata", "homed-v0.1.1.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	cmd := func(bin string) string { b, _ := json.Marshal([]string{bin, "serve"}); return string(b) }
	ms := time.Now().UnixMilli()
	for _, s := range []string{
		string(schema),
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL) STRICT`,
		fmt.Sprintf(`INSERT INTO schema_migrations VALUES (1, '0001_init.sql', %d)`, ms),
		fmt.Sprintf(`INSERT INTO people VALUES ('alice', 'Alice', 'owner', '["*"]', '', X'01', %d, %d)`, ms, ms),
		fmt.Sprintf(`INSERT INTO plugins VALUES ('fake', 'local', '', '', '', '%s', 1, %d, %d)`, cmd(fakeBin), ms, ms),
		fmt.Sprintf(`INSERT INTO settings VALUES (1, 'Дом', 'en', '', 0, %d)`, ms),
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
}

// An unknown tunnel mode stops homed at start.
func TestUnknownTunnelModeRefusesToStart(t *testing.T) {
	h := newHome(t, "  mode: relay\n")
	err := serve(t.Context(), runOptions{dataDir: h.dir}, discardLog())
	if err == nil || !strings.Contains(err.Error(), `mode "relay"`) {
		t.Fatalf("serve = %v", err)
	}
}

// firstRunPIN asks `homed admin firstrun` for the page and its PIN.
func (h *home) firstRunPIN() (pageURL, pin string) {
	h.t.Helper()
	code, out, errOut := cli(h.t, "admin", "firstrun", "--data-dir", h.dir)
	if code != 0 {
		h.t.Fatalf("admin firstrun: %d %s %s", code, out, errOut)
	}
	for _, line := range strings.Split(out, "\n") {
		if u, ok := strings.CutPrefix(strings.TrimSpace(line), "http://"); ok {
			pageURL = "http://" + strings.TrimSuffix(u, "/firstrun")
		}
		if p, ok := strings.CutPrefix(line, "PIN: "); ok {
			pin = strings.ReplaceAll(strings.Fields(p)[0]+strings.Fields(p)[1], " ", "")
		}
	}
	if pageURL == "" || len(pin) != 8 {
		h.t.Fatalf("admin firstrun:\n%s", out)
	}
	return pageURL, pin
}

// page is a browser on the first-run page.
type page struct {
	t      *testing.T
	base   string
	cookie string
}

func (p *page) do(method, path string, body any) (int, map[string]any) {
	p.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequestWithContext(p.t.Context(), method, p.base+path, rd)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Homie", "1")
	}
	if p.cookie != "" {
		req.Header.Set("Cookie", "homie_firstrun="+p.cookie)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer res.Body.Close()
	for _, c := range res.Cookies() {
		if c.Name == "homie_firstrun" {
			p.cookie = c.Value
		}
	}
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (h *home) openPage() *page {
	h.t.Helper()
	base, pin := h.firstRunPIN()
	p := &page{t: h.t, base: base}
	if code, out := p.do("POST", "/api/firstrun/pin", map[string]string{"pin": pin}); code != http.StatusNoContent {
		h.t.Fatalf("PIN: %d %v", code, out)
	}
	return p
}

// "My domain" from the first-run page: the login link, the connect, the self-check, the setup link; the page leaves
// the home network once the tunnel is configured, keeping its PIN and cookie.
func TestNamedConnectFromThePage(t *testing.T) {
	h := newHome(t, "").start()
	pageURLs := func() []string {
		h.d.mu.Lock()
		defer h.d.mu.Unlock()
		if h.d.firstRun == nil {
			return nil
		}
		return h.d.firstRun.urls
	}
	first := pageURLs()
	if h.d.firstRun == nil || !h.d.firstRun.moves || len(first) != 1 {
		t.Fatalf("without a tunnel the page is not on its home-network default: %v", first)
	}
	p := h.openPage()
	if code, st := p.do("GET", "/api/firstrun/state", nil); code != 200 || st["step"] != "tunnel" {
		t.Fatalf("state: %d %v", code, st)
	}
	h.fake.Set("login-release")
	if code, out := p.do("POST", "/api/firstrun/tunnel", map[string]any{"mode": "named", "hostname": "home.example.com"}); code != http.StatusAccepted {
		t.Fatalf("connect: %d %v", code, out)
	}
	// The page moves to its loopback default (here another free port), with its PIN and cookie.
	waitFor(t, "the page to move", func() bool { u := pageURLs(); return len(u) == 1 && u[0] != first[0] })
	p.base = strings.TrimSuffix(pageURLs()[0], "/firstrun")
	waitFor(t, "the link step", func() bool { _, st := p.do("GET", "/api/firstrun/state", nil); return st["step"] == "link" })
	code, link := p.do("POST", "/api/firstrun/link", map[string]any{})
	if code != 200 || !strings.HasPrefix(link["url"].(string), "https://home.example.com/setup#t=") {
		t.Fatalf("link: %d %v", code, link)
	}
	cfg, _ := config.Load(h.dir)
	if cfg.Tunnel.Mode != config.TunnelNamed || cfg.Tunnel.Hostname != "home.example.com" {
		t.Fatalf("homed.yaml: %+v", cfg.Tunnel)
	}
	b := h.browser("home.example.com")
	b.register(newDevice("home.example.com"), tokenOf(t, link["url"].(string)), "Alice", "Дом")
	waitFor(t, "the owner", func() bool { r, _ := h.d.ownerReady(t.Context()); return r })
}

// Quick mode: the owner signs in at the first URL; a restart of cloudflared brings a new URL, where the old passkeys
// no longer count: the first-run page opens again by itself, its link reconnects the same owner, and the old host is
// refused.
func TestQuickHostChange(t *testing.T) {
	h := newHome(t, "  mode: quick\n").start()
	first := tunneltest.QuickHost(1)
	waitFor(t, "the quick URL", func() bool { return h.d.publicHost() == first })
	waitFor(t, "the self-check", func() bool { return h.d.tun.Status().State == "connected" })
	p := h.openPage()
	_, st := p.do("GET", "/api/firstrun/state", nil)
	if st["step"] != "link" || st["ownerExists"] != false {
		t.Fatalf("state: %v", st)
	}
	_, link := p.do("POST", "/api/firstrun/link", map[string]any{})
	alice := h.browser(first)
	alice.register(newDevice(first), tokenOf(t, link["url"].(string)), "Alice", "Дом")
	me := alice.json("GET", "/api/me", nil, 200)
	aliceID := me["person"].(map[string]any)["id"]
	// Quick mode is the owner's alone: its address changes on every restart, and every passkey with it, so no invites.
	if inv := alice.json("POST", "/api/account/invites", map[string]any{"name": "Bella"}, http.StatusConflict); inv["error"] != "quick_mode" {
		t.Fatalf("an invite in quick mode: %v", inv)
	}
	closing := func() bool {
		h.d.mu.Lock()
		defer h.d.mu.Unlock()
		return h.d.firstRun != nil && h.d.firstRunTimer != nil
	}
	waitFor(t, "the page to start closing", closing)

	// The admin's reconnect (here the tunnel itself): a new URL.
	if err := h.d.tun.Restart(); err != nil {
		t.Fatal(err)
	}
	second := tunneltest.QuickHost(2)
	waitFor(t, "the new quick URL", func() bool { return h.d.publicHost() == second })
	if ready, err := h.d.ownerReady(t.Context()); err != nil || ready {
		t.Fatalf("the old URL's passkey counts at the new one (ready %v, err %v)", ready, err)
	}
	if code, _, _ := alice.do("GET", "/api/me", nil); code != http.StatusMisdirectedRequest {
		t.Fatalf("the old host: %d", code)
	}
	// The page that was about to close stays open.
	waitFor(t, "the page to stay", func() bool {
		h.d.mu.Lock()
		defer h.d.mu.Unlock()
		return h.d.firstRun != nil && h.d.firstRunTimer == nil
	})
	p = h.openPage()
	_, st = p.do("GET", "/api/firstrun/state", nil)
	if st["step"] != "link" || st["ownerExists"] != true {
		t.Fatalf("state after the change: %v", st)
	}
	_, link = p.do("POST", "/api/firstrun/link", map[string]any{})
	if !strings.HasPrefix(link["url"].(string), "https://"+second+"/setup#t=") {
		t.Fatalf("link: %v", link)
	}
	again := h.browser(second)
	again.register(newDevice(second), tokenOf(t, link["url"].(string)), "", "")
	if me := again.json("GET", "/api/me", nil, 200); me["person"].(map[string]any)["id"] != aliceID {
		t.Fatalf("the reconnect made another person: %v", me)
	}
	if code, _, out := again.do("POST", "/api/account/invites", map[string]any{"name": "Bella", "permissions": []string{"all"}}); code != http.StatusConflict || !strings.Contains(string(out), `"error":"quick_mode"`) {
		t.Fatalf("an invite in quick mode after the reconnect: %d %s", code, out)
	}

	// Later, the page closed (its two minutes passed, here at once): the next new URL opens it again by itself.
	waitFor(t, "the page to start closing again", closing)
	h.d.mu.Lock()
	h.d.firstRunTimer.Reset(time.Millisecond)
	h.d.mu.Unlock()
	pageOpen := func() bool {
		h.d.mu.Lock()
		defer h.d.mu.Unlock()
		return h.d.firstRun != nil
	}
	waitFor(t, "the page to close", func() bool { return !pageOpen() })
	if err := h.d.tun.Restart(); err != nil {
		t.Fatal(err)
	}
	third := tunneltest.QuickHost(3)
	waitFor(t, "the third quick URL", func() bool { return h.d.publicHost() == third })
	waitFor(t, "the page to reopen", pageOpen)
	p = h.openPage()
	_, link = p.do("POST", "/api/firstrun/link", map[string]any{})
	last := h.browser(third)
	last.register(newDevice(third), tokenOf(t, link["url"].(string)), "", "")
	if me := last.json("GET", "/api/me", nil, 200); me["person"].(map[string]any)["id"] != aliceID {
		t.Fatalf("the second reconnect made another person: %v", me)
	}
}

// Development: --dev-origin serves the gatekeeper at the dev origin without a tunnel.
func TestDevMode(t *testing.T) {
	h := newHome(t, "").start(func(o *runOptions) { o.devOrigin = "http://localhost:5173" })
	if link := h.setupLink(); !strings.HasPrefix(link, "http://localhost:5173/setup#t=") {
		t.Fatalf("link %q", link)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+h.addr+"/api/home/bootstrap", http.NoBody)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap: %v %v", res, err)
	}
	_ = res.Body.Close()
	// A tunnel in homed.yaml and --dev-origin do not go together.
	other := newHome(t, "  mode: quick\n")
	if err := serve(t.Context(), runOptions{dataDir: other.dir, devOrigin: "http://localhost:5173"}, discardLog()); err == nil ||
		!strings.Contains(err.Error(), "--dev-origin") {
		t.Fatalf("dev with a tunnel: %v", err)
	}
}

// Without a public URL the hub answers the bootstrap and the health check, and 503 for what needs the gatekeeper;
// the CLI's setup-link says to connect first.
func TestNoTunnel(t *testing.T) {
	h := newHome(t, "").start()
	get := func(path string) (int, http.Header, string) {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+h.addr+path, http.NoBody)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header, string(raw)
	}
	if code, hd, _ := get("/healthz"); code != 200 || hd.Get("X-Homie-Instance") == "" {
		t.Fatalf("healthz: %d %v", code, hd)
	}
	if code, _, body := get("/api/home/bootstrap"); code != 200 || !strings.Contains(body, `"mode":"off"`) {
		t.Fatalf("bootstrap: %d %s", code, body)
	}
	for _, p := range []string{"/api/me", "/mcp", "/.well-known/oauth-authorization-server", "/api/account"} {
		if code, _, body := get(p); code != http.StatusServiceUnavailable || !strings.Contains(body, "home_not_ready") {
			t.Errorf("%s: %d %s", p, code, body)
		}
	}
	if code, _, errOut := cli(t, "admin", "setup-link", "--data-dir", h.dir); code != 1 || !strings.Contains(errOut, "connect it first") {
		t.Fatalf("setup-link: %d %s", code, errOut)
	}
	if code, out, _ := cli(t, "cloudflare", "status", "--data-dir", h.dir); code != 0 || !strings.Contains(out, "mode:  off") {
		t.Fatalf("status: %d %s", code, out)
	}
	_ = core.ErrNotFound
}

// consentTools are the tool names on the last consent screen b saw.
func consentTools(t *testing.T, b *browser) []string {
	t.Helper()
	res, _ := b.consent["resource"].(map[string]any)
	tools, _ := res["tools"].([]any)
	var out []string
	for _, tool := range tools {
		name, _ := tool.(map[string]any)["name"].(string)
		out = append(out, name)
	}
	return out
}

// A move to another address leaves the owner without a passkey there: `homed cloudflare connect` says how to get one
// and the first-run page opens again by itself, in named mode too (review R-10). A switch to a quick tunnel revokes
// every open member and owner invite made on the domain, audited (review R-11).
func TestAddressChangesAndInvites(t *testing.T) {
	const host, other = "home.example.com", "other.example.com"
	h := newHome(t, "  mode: named\n  hostname: "+host+"\n").connected(host).start()
	waitFor(t, "the tunnel's self-check", func() bool { return h.d.tun.Status().State == "connected" })
	alice := h.browser(host)
	alice.register(newDevice(host), tokenOf(t, h.setupLink()), "Alice", "Дом")
	member := alice.json("POST", "/api/account/invites", map[string]any{"name": "Bella"}, 200)
	owner := alice.json("POST", "/api/account/invites", map[string]any{"name": "Carol", "role": "owner"}, 200)

	// The page closes once the owner can sign in (its two minutes, here at once).
	pageOpen := func() bool {
		h.d.mu.Lock()
		defer h.d.mu.Unlock()
		return h.d.firstRun != nil
	}
	waitFor(t, "the page to start closing", func() bool {
		h.d.mu.Lock()
		defer h.d.mu.Unlock()
		return h.d.firstRun != nil && h.d.firstRunTimer != nil
	})
	h.d.mu.Lock()
	h.d.firstRunTimer.Reset(time.Millisecond)
	h.d.mu.Unlock()
	waitFor(t, "the page to close", func() bool { return !pageOpen() })

	// Another domain: the CLI says to create a passkey there, and the page opens again.
	h.fake.Set("login-release")
	code, out, errOut := cli(t, "cloudflare", "connect", other, "--data-dir", h.dir)
	if code != 0 || !strings.Contains(out, "The owner has no passkey for https://"+other+" yet") ||
		!strings.Contains(out, "homed admin setup-link") {
		t.Fatalf("connect %s: %d\n%s\n%s", other, code, out, errOut)
	}
	waitFor(t, "the page to reopen for the new domain", pageOpen)

	// To a quick tunnel: the open invites go.
	for _, args := range [][]string{{"cloudflare", "disconnect"}, {"cloudflare", "quick"}} {
		if code, out, errOut := cli(t, append(args, "--data-dir", h.dir)...); code != 0 {
			t.Fatalf("%v: %d\n%s\n%s", args, code, out, errOut)
		}
	}
	ctx := t.Context()
	accs, err := h.d.st.Accounts().List(ctx)
	if err != nil || len(accs) != 1 {
		t.Fatalf("accounts %v %v", accs, err)
	}
	// The store lists open invites only: none is left.
	if invites, err := h.d.st.Invites().ListByAccount(ctx, accs[0].ID); err != nil || len(invites) != 0 {
		t.Fatalf("open invites after the switch to quick: %+v %v", invites, err)
	}
	revoked := map[string]bool{}
	entries, err := h.d.st.Audit().ListByAccount(ctx, accs[0].ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.Action == "account.invite.revoke" && e.Detail["reason"] == "tunnel.quick" {
			revoked[e.Target] = true
			n++
		}
	}
	if n != 2 || !revoked[member["id"].(string)] || !revoked[owner["id"].(string)] {
		t.Fatalf("%d revokes audited, want 2: %+v", n, entries)
	}
}

// The access-reset alert names the programs that ran the homed CLI when the gatekeeper recorded them (review R-5),
// and only the peer otherwise.
func TestAccessResetNamesWhoRanIt(t *testing.T) {
	h := newHome(t, "").start()
	ctx := t.Context()
	peer := adminsock.Peer{PID: 4242, Exe: "/home/user/.local/bin/homed"}
	at := h.clock.now()
	if err := h.d.st.Audit().Append(ctx, core.AuditEntry{At: at, Action: "admin.reset-access", Detail: map[string]string{
		"peer_pid": "4242", "peer_exe": peer.Exe, "peer_chain": "/bin/zsh (pid 4241) < /usr/bin/login (pid 4240)"}}); err != nil {
		t.Fatal(err)
	}
	h.d.accessReset(ctx, auth.AccessReset{At: at, Peer: peer})
	hs, err := h.d.st.Settings().Get(ctx)
	if want := "/home/user/.local/bin/homed (pid 4242), run from /bin/zsh (pid 4241) < /usr/bin/login (pid 4240)"; err != nil ||
		hs.AccessResetBy != want {
		t.Fatalf("by = %q (%v), want %q", hs.AccessResetBy, err, want)
	}
	// A reset whose entry carries no chain (an older gatekeeper): the peer alone, as before.
	later := at.Add(time.Minute)
	if err := h.d.st.Audit().Append(ctx, core.AuditEntry{At: later, Action: "admin.reset-access",
		Detail: map[string]string{"peer_pid": "4242", "peer_exe": peer.Exe}}); err != nil {
		t.Fatal(err)
	}
	h.d.accessReset(ctx, auth.AccessReset{At: later, Peer: peer})
	if hs, err := h.d.st.Settings().Get(ctx); err != nil || hs.AccessResetBy != "/home/user/.local/bin/homed (pid 4242)" {
		t.Fatalf("by without a chain = %q (%v)", hs.AccessResetBy, err)
	}
}
