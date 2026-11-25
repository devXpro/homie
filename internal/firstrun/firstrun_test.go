package firstrun

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devXpro/mcp-gatekeeper/core"

	"github.com/devXpro/homie/internal/tunnel"
	"github.com/devXpro/homie/internal/tunnel/tunneltest"
)

const (
	lanHostPort = "192.0.2.10:8090"
	instanceID  = "0123456789abcdef0123456789abcdef"
)

func TestMain(m *testing.M) { os.Exit(tunneltest.Main(m)) }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeHome is homed as the page sees it: the owner, the setup links and the audit.
type fakeHome struct {
	mu       sync.Mutex
	ready    bool
	exists   bool
	conflict bool // SetupLink answers that the owner can sign in already
	links    []time.Duration
	audit    []core.AuditEntry
	name     string
}

func (h *fakeHome) OwnerReady(context.Context) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ready, nil
}

func (h *fakeHome) OwnerExists(context.Context) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.exists, nil
}

func (h *fakeHome) SetupLink(_ context.Context, ttl time.Duration) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conflict {
		return "", core.ErrConflict
	}
	h.links = append(h.links, ttl)
	return "https://home.example.com/setup#t=" + strconv.Itoa(len(h.links)), nil
}

func (h *fakeHome) HomeName(context.Context) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.name
}

func (h *fakeHome) Audit(_ context.Context, e core.AuditEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.audit = append(h.audit, e)
}

func (h *fakeHome) set(fn func(h *fakeHome)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(h)
}

func (h *fakeHome) actions() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, e := range h.audit {
		out = append(out, e.Action)
	}
	return out
}

// terminal is a goroutine-safe stand-in for homed's terminal.
type terminal struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (t *terminal) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.Write(p)
}

func (t *terminal) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}

var pinRe = regexp.MustCompile(`PIN (?:is now )?(\d{4} \d{4})`)

// pins returns every PIN printed so far, in order.
func (t *terminal) pins() []string {
	var out []string
	for _, m := range pinRe.FindAllStringSubmatch(t.String(), -1) {
		out = append(out, m[1])
	}
	return out
}

// hub stands in for homed's public listener: /healthz with the instance header, which the tunnel's probe wants.
func hub(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(tunnel.InstanceHeader, instanceID)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

type env struct {
	t     *testing.T
	clock *clock
	home  *fakeHome
	tun   *tunnel.Manager
	fake  *tunneltest.Fake
	term  *terminal
	s     *Server
	h     http.Handler

	mu    sync.Mutex
	saved []string
}

// newEnv is a page over a real tunnel manager that runs the fake cloudflared.
func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, clock: &clock{t: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)}, home: &fakeHome{name: "Дом"},
		fake: tunneltest.New(t), term: &terminal{}}
	addr := hub(t)
	m, err := tunnel.New(tunnel.Options{
		Dir: filepath.Join(t.TempDir(), "tunnel"), Service: "http://" + addr, Binary: e.fake.Bin, InstanceID: instanceID,
		ProbeClient: tunneltest.Edge(addr, ""),
		Save: func(mode, host string) error {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.saved = append(e.saved, mode+" "+host)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := m.Start(ctx, tunnel.ModeOff, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Stop(); cancel() })
	e.tun = m
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Path", r.URL.Path)
		_, _ = io.WriteString(w, "spa")
	})
	s, err := New(Options{Tunnel: m, Home: e.home, UI: ui, Terminal: e.term, Now: e.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	s.pollEvery, s.pingEvery = 5*time.Millisecond, 20*time.Millisecond
	e.s, e.h = s, s.Handler()
	// The PIN is printed by Serve; tests that use the handler directly print it here.
	_, _ = io.WriteString(e.term, "test: PIN "+FormatPIN(s.pin)+"\n")
	return e
}

func (e *env) pin() string {
	pins := e.term.pins()
	return pins[len(pins)-1]
}

type browser struct {
	e      *env
	host   string
	remote string // the device's address on the LAN
	cookie string
	header http.Header
}

func (e *env) browser() *browser {
	return &browser{e: e, host: lanHostPort, remote: "192.0.2.20:50000", header: http.Header{}}
}

func (b *browser) do(method, path string, body any) *httptest.ResponseRecorder {
	b.e.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			b.e.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, "http://"+b.host+path, rd)
	req.Host = b.host
	req.RemoteAddr = b.remote
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Homie", "1")
	req.Header.Set("Origin", "http://"+b.host)
	for k, v := range b.header {
		req.Header[k] = v
	}
	if b.cookie != "" {
		req.Header.Set("Cookie", CookieName+"="+b.cookie)
	}
	rec := httptest.NewRecorder()
	b.e.h.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	for _, c := range res.Cookies() {
		if c.Name == CookieName {
			b.cookie = c.Value
		}
	}
	return rec
}

// signIn enters the current PIN.
func (b *browser) signIn() {
	b.e.t.Helper()
	wantStatus(b.e.t, b.do(http.MethodPost, "/api/firstrun/pin", map[string]string{"pin": b.e.pin()}), http.StatusNoContent)
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
}

// wantError checks the gatekeeper's error form {"error":"<code>","message":"…"}.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	wantStatus(t, rec, status)
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error != code || body.Message == "" {
		t.Fatalf("error %q, want %q: %s", body.Error, code, rec.Body)
	}
}

func decodeAs[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body)
	}
	return v
}

type state struct {
	Step        string `json:"step"`
	HomeName    string `json:"homeName"`
	OwnerExists *bool  `json:"ownerExists"`
	Tunnel      *struct {
		Mode      string `json:"mode"`
		State     string `json:"state"`
		URL       string `json:"url"`
		MCPURL    string `json:"mcpUrl"`
		LastError string `json:"lastError"`
	} `json:"tunnel"`
	Connect *struct {
		Hostname string `json:"hostname"`
		State    string `json:"state"`
		Step     string `json:"step"`
		LoginURL string `json:"loginUrl"`
		Error    string `json:"error"`
	} `json:"connect"`
}

func (b *browser) state() state {
	b.e.t.Helper()
	return decodeAs[state](b.e.t, b.do(http.MethodGet, "/api/firstrun/state", nil))
}

func TestPIN(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	if st := b.state(); st.Step != "pin" || st.Tunnel != nil || st.HomeName != "" {
		t.Fatalf("state %+v", st)
	}
	// The cookie-level endpoints are closed before the PIN.
	wantError(t, b.do(http.MethodPost, "/api/firstrun/link", map[string]any{}), http.StatusUnauthorized, "pin_required")
	wantError(t, b.do(http.MethodPost, "/api/firstrun/tunnel", map[string]any{"mode": "quick"}), http.StatusUnauthorized, "pin_required")
	wantError(t, b.do(http.MethodGet, "/api/firstrun/events", nil), http.StatusUnauthorized, "pin_required")

	wrong := "0000 0000"
	if e.pin() == wrong {
		wrong = "1111 1111"
	}
	wantError(t, b.do(http.MethodPost, "/api/firstrun/pin", map[string]string{"pin": wrong}), http.StatusUnauthorized, "wrong_pin")
	// Six digits are never enough: the PIN has eight.
	wantError(t, b.do(http.MethodPost, "/api/firstrun/pin", map[string]string{"pin": e.pin()[:6]}), http.StatusUnauthorized, "wrong_pin")
	rec := b.do(http.MethodPost, "/api/firstrun/pin", map[string]string{"pin": " " + e.pin() + " "})
	wantStatus(t, rec, http.StatusNoContent)
	c := rec.Result().Cookies()[0]
	if c.Name != CookieName || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Secure || c.Path != "/" ||
		c.MaxAge != int(cookieTTL/time.Second) || len(c.Value) != 43 {
		t.Fatalf("cookie %+v", c)
	}
	if st := b.state(); st.Step != "tunnel" || st.HomeName != "Дом" || st.OwnerExists == nil || *st.OwnerExists ||
		st.Tunnel == nil || st.Tunnel.Mode != "off" || st.Tunnel.State != "stopped" {
		t.Fatalf("state %+v", st)
	}
	// The cookie lives 30 minutes; the server keeps only its hash.
	if _, ok := e.s.cookies[b.cookie]; ok {
		t.Fatal("the cookie is kept in clear")
	}
	e.clock.Add(cookieTTL)
	if st := b.state(); st.Step != "pin" {
		t.Fatalf("state after 30 minutes %+v", st)
	}
	if strings.Contains(e.term.String(), b.cookie) {
		t.Fatal("the cookie reached the terminal")
	}
}

// wrongPIN returns a PIN that is not the current one.
func (e *env) wrongPIN() string {
	for _, p := range []string{"0000 0000", "1111 1111"} {
		if p != e.pin() {
			return p
		}
	}
	return ""
}

// One device that keeps guessing waits; the other devices on the LAN do not.
func TestPINLockoutPerClient(t *testing.T) {
	e := newEnv(t)
	guesser := e.browser()
	guesser.remote = "192.0.2.99:40000"
	wrong := map[string]string{"pin": e.wrongPIN()}
	for range maxWrongClient - 1 {
		wantError(t, guesser.do(http.MethodPost, "/api/firstrun/pin", wrong), http.StatusUnauthorized, "wrong_pin")
		e.clock.Add(time.Minute)
	}
	// The fifth wrong PIN locks this device until its first wrong one is 10 minutes old.
	rec := guesser.do(http.MethodPost, "/api/firstrun/pin", wrong)
	wantError(t, rec, http.StatusTooManyRequests, "rate_limited")
	if got := rec.Header().Get("Retry-After"); got != strconv.Itoa(int((clientWindow - 4*time.Minute).Seconds())) {
		t.Fatalf("Retry-After %q", got)
	}
	// Locked: even the right PIN waits on this device, until Retry-After…
	wantError(t, guesser.do(http.MethodPost, "/api/firstrun/pin", map[string]string{"pin": e.pin()}), http.StatusTooManyRequests, "rate_limited")
	// …while the owner's phone signs in at once, and the PIN did not change under them.
	if pins := e.term.pins(); len(pins) != 1 {
		t.Fatalf("printed PINs %v", pins)
	}
	e.browser().signIn()
	e.clock.Add(clientWindow - 4*time.Minute)
	guesser.signIn()
}

// 20 wrong PINs from anywhere close the page for good; only the CLI path is left.
func TestPINFailureCapClosesThePage(t *testing.T) {
	e := newEnv(t)
	owner := e.browser()
	owner.signIn()
	wrong := map[string]string{"pin": e.wrongPIN()}
	for i := range maxWrongTotal - 1 {
		b := e.browser()
		b.remote = "192.0.2." + strconv.Itoa(100+i) + ":40000" // a new address for every guess
		wantError(t, b.do(http.MethodPost, "/api/firstrun/pin", wrong), http.StatusUnauthorized, "wrong_pin")
	}
	last := e.browser()
	last.remote = "192.0.2.200:40000"
	wantError(t, last.do(http.MethodPost, "/api/firstrun/pin", wrong), http.StatusGone, "setup_closed")
	if !e.s.Closed() {
		t.Fatal("the page is still open")
	}
	// Closed for everybody: the right PIN, a signed-in browser, the UI.
	wantError(t, e.browser().do(http.MethodPost, "/api/firstrun/pin", map[string]string{"pin": e.pin()}), http.StatusGone, "setup_closed")
	wantError(t, owner.do(http.MethodGet, "/api/firstrun/state", nil), http.StatusGone, "setup_closed")
	rec := owner.do(http.MethodGet, "/firstrun", nil)
	if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), "homed admin firstrun") {
		t.Fatalf("closed page: %d %s", rec.Code, rec.Body)
	}
}

// "My domain" from the page: the Cloudflare login link as soon as cloudflared prints it, then the tunnel, the self-check
// and the setup link, which lives 3 minutes and changes the PIN.
func TestNamedConnectFromThePage(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	b.signIn()
	wantError(t, b.do(http.MethodPost, "/api/firstrun/link", map[string]any{}), http.StatusConflict, "tunnel_not_ready")
	wantError(t, b.do(http.MethodPost, "/api/firstrun/tunnel", map[string]any{"mode": "named", "hostname": "https://x"}),
		http.StatusBadRequest, "invalid_args")
	wantError(t, b.do(http.MethodPost, "/api/firstrun/tunnel", map[string]any{"mode": "relay"}), http.StatusBadRequest, "invalid_args")

	rec := b.do(http.MethodPost, "/api/firstrun/tunnel", map[string]any{"mode": "named", "hostname": " Home.Example.com "})
	wantStatus(t, rec, http.StatusAccepted)
	ans := decodeAs[struct {
		Tunnel  struct{ Mode string } `json:"tunnel"`
		Connect struct {
			Hostname, State string
		} `json:"connect"`
	}](t, rec)
	if ans.Connect.Hostname != "home.example.com" || ans.Connect.State != "running" {
		t.Fatalf("answer %s", rec.Body)
	}
	tunneltest.Eventually(t, "the login link", func() bool {
		st := b.state()
		return st.Step == "connecting" && st.Connect != nil && strings.HasPrefix(st.Connect.LoginURL, "https://dash.cloudflare.com/")
	})
	wantError(t, b.do(http.MethodPost, "/api/firstrun/tunnel", map[string]any{"mode": "named", "hostname": "home.example.com"}),
		http.StatusConflict, "conflict")
	e.fake.Set("login-release")
	tunneltest.Eventually(t, "the link step", func() bool { return b.state().Step == "link" })
	st := b.state()
	if st.Tunnel.Mode != "named" || st.Tunnel.URL != "https://home.example.com" || st.Tunnel.MCPURL != "https://home.example.com/mcp" ||
		st.Connect.State != "ok" {
		t.Fatalf("state %+v", st)
	}
	e.mu.Lock()
	saved := strings.Join(e.saved, ",")
	e.mu.Unlock()
	if saved != "named home.example.com" {
		t.Errorf("saved %q", saved)
	}

	pinBefore := e.pin()
	rec = b.do(http.MethodPost, "/api/firstrun/link", map[string]any{})
	wantStatus(t, rec, http.StatusOK)
	link := decodeAs[map[string]string](t, rec)
	if !strings.HasPrefix(link["url"], "https://home.example.com/setup#t=") || link["expiresAt"] != "2026-11-15T12:03:00.000Z" {
		t.Fatalf("link %v", link)
	}
	if e.home.links[0] != LinkTTL {
		t.Errorf("link TTL %v", e.home.links)
	}
	if e.pin() == pinBefore {
		t.Error("the PIN did not change after the link")
	}
	if got := strings.Join(e.home.actions(), ","); got != "tunnel.connect,firstrun.link" {
		t.Errorf("audit %s", got)
	}
	// A connected domain is never replaced from this page.
	wantError(t, b.do(http.MethodPost, "/api/firstrun/tunnel", map[string]any{"mode": "named", "hostname": "other.example.com"}),
		http.StatusConflict, "conflict")
	wantError(t, b.do(http.MethodPost, "/api/firstrun/tunnel", map[string]any{"mode": "quick"}), http.StatusConflict, "conflict")
	// Once the owner has a passkey of this URL, the page is done.
	e.home.set(func(h *fakeHome) { h.ready = true })
	wantError(t, b.do(http.MethodGet, "/api/firstrun/state", nil), http.StatusGone, "setup_done")
}

// "Try without a domain": a quick tunnel; and the owner who exists already gets the link step labelled as a reconnect.
func TestQuickFromThePage(t *testing.T) {
	e := newEnv(t)
	e.home.set(func(h *fakeHome) { h.exists = true })
	b := e.browser()
	b.signIn()
	wantStatus(t, b.do(http.MethodPost, "/api/firstrun/tunnel", map[string]any{"mode": "quick"}), http.StatusAccepted)
	tunneltest.Eventually(t, "the link step", func() bool { return b.state().Step == "link" })
	st := b.state()
	if st.Tunnel.Mode != "quick" || st.Tunnel.URL != "https://"+tunneltest.QuickHost(1) || !*st.OwnerExists {
		t.Fatalf("state %+v", st)
	}
	wantStatus(t, b.do(http.MethodPost, "/api/firstrun/link", map[string]any{}), http.StatusOK)
	// The owner signed in meanwhile on another device: the link answers that the page is done.
	e.home.set(func(h *fakeHome) { h.conflict = true })
	wantError(t, b.do(http.MethodPost, "/api/firstrun/link", map[string]any{}), http.StatusGone, "setup_done")
	// A domain may follow a quick tunnel ("move to my domain").
	wantStatus(t, b.do(http.MethodPost, "/api/firstrun/tunnel", map[string]any{"mode": "named", "hostname": "home.example.com"}),
		http.StatusAccepted)
}

func TestHostGuard(t *testing.T) {
	e := newEnv(t)
	for host, ok := range map[string]bool{
		lanHostPort:             true,
		"192.0.2.10":            true,
		"[fe80::1]:8090":        true,
		"localhost:8090":        true,
		"homebox.local:8090":    true,
		"homebox:8090":          true,
		"homebox.lan":           true,
		"router.home.arpa:8090": true,
		"evil.example.com:8090": false,
		"192.0.2.10.nip.io":     false,
		"":                      false,
	} {
		b := e.browser()
		b.host = host
		rec := b.do(http.MethodGet, "/api/firstrun/state", nil)
		if ok && rec.Code != http.StatusOK || !ok && (rec.Code != http.StatusMisdirectedRequest || !strings.Contains(rec.Body.String(), `"misdirected"`)) {
			t.Errorf("Host %q: %d %s", host, rec.Code, rec.Body)
		}
	}
}

func TestCSRF(t *testing.T) {
	e := newEnv(t)
	for name, tc := range map[string]struct {
		header map[string]string
		ok     bool
	}{
		"same origin":       {nil, true},
		"no Origin":         {map[string]string{"Origin": ""}, true},
		"no X-Homie":        {map[string]string{"X-Homie": ""}, false},
		"foreign Origin":    {map[string]string{"Origin": "http://evil.example.com"}, false},
		"other LAN port":    {map[string]string{"Origin": "http://192.0.2.10:8091"}, false},
		"cross-site fetch":  {map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		"same-origin fetch": {map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
	} {
		b := e.browser()
		for k, v := range tc.header {
			if v == "" {
				b.header[http.CanonicalHeaderKey(k)] = nil
				continue
			}
			b.header.Set(k, v)
		}
		rec := b.do(http.MethodPost, "/api/firstrun/pin", map[string]string{"pin": "x"})
		if tc.ok && rec.Code != http.StatusUnauthorized || !tc.ok && rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestRoutes(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	rec := b.do(http.MethodGet, "/", nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/firstrun" {
		t.Fatalf("/ → %d %v", rec.Code, rec.Header())
	}
	if rec = b.do(http.MethodGet, "/firstrun", nil); rec.Body.String() != "spa" || rec.Header().Get("X-Test-Path") != "/firstrun" {
		t.Fatalf("UI %q", rec.Body)
	}
	wantError(t, b.do(http.MethodGet, "/api/firstrun/nope", nil), http.StatusNotFound, "not_found")
	wantError(t, b.do(http.MethodGet, "/api/firstrun/pin", nil), http.StatusMethodNotAllowed, "method_not_allowed")
	rec = b.do(http.MethodGet, "/api/firstrun/state", nil)
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers %v", rec.Header())
	}
}

// The event stream sends the tunnel at once and again on every change, the connect as it goes, and ends with the
// cookie.
func TestEvents(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	b := e.browser()
	b.signIn()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/firstrun/events", http.NoBody)
	req.Host = lanHostPort
	req.Header.Set("Cookie", CookieName+"="+b.cookie)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := res.Body
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream %d %v", res.StatusCode, res.Header)
	}
	frames := make(chan string)
	go func() {
		defer close(frames)
		sc := bufio.NewScanner(body)
		var frame []string
		for sc.Scan() {
			if sc.Text() != "" {
				frame = append(frame, sc.Text())
				continue
			}
			if len(frame) > 0 && !strings.HasPrefix(frame[0], ":") {
				frames <- strings.Join(frame, "\n")
			}
			frame = nil
		}
	}()
	next := func() string {
		t.Helper()
		select {
		case f, ok := <-frames:
			if !ok {
				return ""
			}
			return f
		case <-ctx.Done():
			t.Fatal("no frame")
			return ""
		}
	}
	if f := next(); f != "retry: 3000" {
		t.Fatalf("first frame %q", f)
	}
	if f := next(); !strings.HasPrefix(f, "id: 1\nevent: tunnel\ndata: ") || !strings.Contains(f, `"mode":"off"`) {
		t.Fatalf("tunnel frame %q", f)
	}
	e.fake.Set("login-release")
	wantStatus(t, b.do(http.MethodPost, "/api/firstrun/tunnel", map[string]any{"mode": "named", "hostname": "home.example.com"}),
		http.StatusAccepted)
	sawConnect, sawUp := false, false
	for !sawConnect || !sawUp {
		f := next()
		switch {
		case f == "":
			t.Fatal("the stream ended early")
		case strings.Contains(f, "event: connect") && strings.Contains(f, `"state":"ok"`):
			sawConnect = true
		case strings.Contains(f, "event: tunnel") && strings.Contains(f, `"state":"connected"`):
			sawUp = true
		}
	}
	// The cookie expires: the stream ends.
	e.clock.Add(cookieTTL)
	for {
		if f := next(); f == "" {
			break
		}
	}
}

// Serve prints where to go and the PIN, serves, and stops with the context; served again on another listener (the
// page moves to loopback) it keeps the PIN and the cookies.
func TestServe(t *testing.T) {
	term := &terminal{}
	home := &fakeHome{}
	s, err := New(Options{Tunnel: stubTunnel{}, Home: home, Terminal: term})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	cookie := pinIn(t, ln.Addr().String(), s)
	pin, until := s.PIN()
	out := term.String()
	if !strings.Contains(out, "http://"+ln.Addr().String()+"/firstrun") || len(term.pins()) != 1 || term.pins()[0] != FormatPIN(pin) ||
		!strings.Contains(out, "closes at") {
		t.Fatalf("terminal %q", out)
	}
	if closes := s.ClosesAt(); closes.Sub(until) != OpenFor-PINEvery {
		t.Fatalf("closes %v, PIN until %v", closes, until)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s.Closed() {
		t.Fatal("stopping the listener closed the page")
	}
	// Served again: the same PIN, the same cookie.
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() { done <- s.Serve(ctx2, ln2) }()
	if again, _ := s.PIN(); again != pin {
		t.Fatal("the PIN changed on the move")
	}
	if st := getState(t, ln2.Addr().String(), cookie); st.Step != "tunnel" {
		t.Fatalf("state on the new listener %+v", st)
	}
	cancel2()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if urls := PageURLs(&net.TCPAddr{IP: net.IPv4zero, Port: 8090}); len(urls) == 0 || urls[len(urls)-1] != "http://localhost:8090/firstrun" {
		t.Fatalf("urls %v", urls)
	}
}

// stubTunnel is a tunnel that is off and stays off.
type stubTunnel struct{}

func (stubTunnel) Status() tunnel.Status {
	return tunnel.Status{Mode: tunnel.ModeOff, State: "stopped"}
}
func (stubTunnel) Job() (tunnel.ConnectJob, bool) { return tunnel.ConnectJob{}, false }
func (stubTunnel) Connect(string, bool) (tunnel.ConnectJob, error) {
	return tunnel.ConnectJob{}, core.ErrConflict
}
func (stubTunnel) Quick() error { return core.ErrConflict }
func pinIn(t *testing.T, addr string, s *Server) (cookie string) {
	t.Helper()
	pin, _ := s.PIN()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+addr+"/api/firstrun/pin",
		strings.NewReader(`{"pin":"`+pin+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Homie", "1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNoContent || len(res.Cookies()) != 1 {
		t.Fatalf("PIN: %d", res.StatusCode)
	}
	return res.Cookies()[0].Value
}

func getState(t *testing.T, addr, cookie string) state {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+"/api/firstrun/state", http.NoBody)
	req.Header.Set("Cookie", CookieName+"="+cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var st state
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// serveBriefly runs Serve with short timings and returns a function that waits for it to end.
func serveBriefly(t *testing.T, s *Server) (addr string, wait func() error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	t.Cleanup(cancel)
	return ln.Addr().String(), func() error {
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("Serve did not return")
			return nil
		}
	}
}

// The page closes by itself after its time; the PIN changes on a timer meanwhile.
func TestServeClosesAndRotates(t *testing.T) {
	term := &terminal{}
	logs := &terminal{}
	s, err := New(Options{Tunnel: stubTunnel{}, Home: &fakeHome{}, Terminal: term, OpenFor: 300 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	s.pinEvery = 40 * time.Millisecond
	addr, wait := serveBriefly(t, s)
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	if pins := term.pins(); len(pins) < 3 || pins[0] == pins[len(pins)-1] {
		t.Fatalf("PINs %v", pins)
	}
	if !strings.Contains(term.String(), "homed admin firstrun") || !strings.Contains(logs.String(), "first-run page closed") {
		t.Fatalf("terminal %q, log %q", term, logs)
	}
	if !s.Closed() {
		t.Fatal("a page whose time is over is not closed")
	}
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Fatal("the port is still open")
	}
}

// Too many wrong PINs close the listener too, not only the answers.
func TestServeClosesAfterTheCap(t *testing.T) {
	s, err := New(Options{Tunnel: stubTunnel{}, Home: &fakeHome{}})
	if err != nil {
		t.Fatal(err)
	}
	addr, wait := serveBriefly(t, s)
	s.mu.Lock()
	s.wrongTotal = maxWrongTotal - 1
	s.mu.Unlock()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+addr+"/api/firstrun/pin",
		strings.NewReader(`{"pin":"00000000"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Homie", "1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusGone {
		t.Fatalf("status %d", res.StatusCode)
	}
	if err := wait(); err != nil {
		t.Fatal(err)
	}
}

// Without a terminal (launchd, systemd) the PIN is never printed or logged; the log says how to get in.
func TestNoTerminalNoPIN(t *testing.T) {
	logs := &terminal{}
	s, err := New(Options{Tunnel: stubTunnel{}, Home: &fakeHome{}, OpenFor: 100 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	s.pinEvery = 20 * time.Millisecond
	_, wait := serveBriefly(t, s)
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	pin, _ := s.PIN()
	if strings.Contains(out, pin) || strings.Contains(out, FormatPIN(pin)) || !strings.Contains(out, "homed admin firstrun") ||
		!strings.Contains(out, "homed admin setup-link") {
		t.Fatalf("log %q", out)
	}
}

func TestDefaultListen(t *testing.T) {
	if DefaultListen(false) != "0.0.0.0:8090" || DefaultListen(true) != "127.0.0.1:8090" {
		t.Fatal(DefaultListen(false), DefaultListen(true))
	}
}

func TestNew(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("no tunnel accepted")
	}
	seen := map[string]bool{}
	for range 50 {
		p := newPIN()
		if len(p) != 8 || strings.Trim(p, "0123456789") != "" {
			t.Fatalf("PIN %q", p)
		}
		seen[p] = true
	}
	if len(seen) < 49 {
		t.Fatalf("PINs repeat: %d distinct of 50", len(seen))
	}
	if FormatPIN("12345678") != "1234 5678" {
		t.Fatal(FormatPIN("12345678"))
	}
}
