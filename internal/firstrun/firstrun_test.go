package firstrun

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"

	"github.com/devXpro/homie/internal/auth"
	"github.com/devXpro/homie/internal/auth/authtest"
	"github.com/devXpro/homie/internal/relay"
	"github.com/devXpro/homie/internal/store"
)

const (
	lanHostPort = "192.168.68.63:8090"
	testDomain  = "homie.example.com"
	testToken   = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

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

// fakeRelay is homed's relay client as the page sees it.
type fakeRelay struct {
	mu        sync.Mutex
	domain    string
	status    relay.Status
	checkedAt time.Time
	failure   string
	codes     []relay.Code
	err       error
}

func (f *fakeRelay) Domain() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.domain
}

func (f *fakeRelay) Status() relay.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeRelay) SelfCheckResult() (time.Time, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkedAt, f.failure
}

func (f *fakeRelay) Connect(_ context.Context, code relay.Code) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.codes = append(f.codes, code)
	f.domain = code.Domain
	f.status = relay.Status{}
	f.checkedAt, f.failure = time.Time{}, ""
	return nil
}

// set changes the relay state under the lock.
func (f *fakeRelay) set(fn func(f *fakeRelay)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
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

type env struct {
	t     *testing.T
	st    *authtest.Store
	clock *clock
	relay *fakeRelay
	term  *terminal
	s     *Server
	h     http.Handler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, st: authtest.New(), clock: &clock{t: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)},
		relay: &fakeRelay{}, term: &terminal{}}
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Path", r.URL.Path)
		_, _ = io.WriteString(w, "spa")
	})
	s, err := New(Options{Store: e.st, Relay: e.relay, UI: ui, Terminal: e.term, Now: e.clock.Now})
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

// connected makes the relay up and its self-check passing.
func (e *env) connected() {
	e.relay.set(func(f *fakeRelay) {
		f.domain = testDomain
		f.status = relay.Status{Connected: true, Since: e.clock.Now()}
		f.checkedAt, f.failure = e.clock.Now(), ""
	})
}

type browser struct {
	e      *env
	host   string
	remote string // the device's address on the LAN
	cookie string
	header http.Header
}

func (e *env) browser() *browser {
	return &browser{e: e, host: lanHostPort, remote: "192.168.68.20:50000", header: http.Header{}}
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

func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	wantStatus(t, rec, status)
	var body struct {
		Error struct {
			Code   string `json:"code"`
			Fields []struct {
				Path string `json:"path"`
			} `json:"fields"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != code {
		t.Fatalf("error %q, want %q: %s", body.Error.Code, code, rec.Body)
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
	Step     string `json:"step"`
	HomeName string `json:"homeName"`
	Relay    *struct {
		Configured bool   `json:"configured"`
		Domain     string `json:"domain"`
		PublicURL  string `json:"publicUrl"`
		State      string `json:"state"`
		LastError  string `json:"lastError"`
		SelfCheck  *struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"selfCheck"`
	} `json:"relay"`
}

func TestPIN(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	if st := decodeAs[state](t, b.do(http.MethodGet, "/api/firstrun/state", nil)); st.Step != "pin" || st.Relay != nil {
		t.Fatalf("state %+v", st)
	}
	// The cookie-level endpoints are closed before the PIN.
	wantError(t, b.do(http.MethodPost, "/api/firstrun/link", map[string]any{}), http.StatusUnauthorized, "unauthenticated")
	wantError(t, b.do(http.MethodGet, "/api/firstrun/events", nil), http.StatusUnauthorized, "unauthenticated")

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
	if st := decodeAs[state](t, b.do(http.MethodGet, "/api/firstrun/state", nil)); st.Step != "relay" {
		t.Fatalf("state %+v", st)
	}
	// The cookie lives 30 minutes; the server keeps only its hash.
	if _, ok := e.s.cookies[b.cookie]; ok {
		t.Fatal("the cookie is kept in clear")
	}
	e.clock.Add(cookieTTL)
	if st := decodeAs[state](t, b.do(http.MethodGet, "/api/firstrun/state", nil)); st.Step != "pin" {
		t.Fatalf("state after 30 minutes %+v", st)
	}
	// Nothing logged or printed carries the cookie.
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

// One device that keeps guessing waits; the other devices on the LAN do not (review H2).
func TestPINLockoutPerClient(t *testing.T) {
	e := newEnv(t)
	guesser := e.browser()
	guesser.remote = "192.168.68.99:40000"
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

// 20 wrong PINs from anywhere close the page for good; only the CLI path is left (review H2).
func TestPINFailureCapClosesThePage(t *testing.T) {
	e := newEnv(t)
	owner := e.browser()
	owner.signIn()
	wrong := map[string]string{"pin": e.wrongPIN()}
	for i := range maxWrongTotal - 1 {
		b := e.browser()
		b.remote = "192.168.68." + strconv.Itoa(100+i) + ":40000" // a new address for every guess
		wantError(t, b.do(http.MethodPost, "/api/firstrun/pin", wrong), http.StatusUnauthorized, "wrong_pin")
	}
	last := e.browser()
	last.remote = "192.168.68.200:40000"
	wantError(t, last.do(http.MethodPost, "/api/firstrun/pin", wrong), http.StatusGone, "setup_closed")
	if !e.s.isClosed() {
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

func TestRelayStep(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	b.signIn()

	for _, bad := range []string{"", "ghr1.!!!", "nonsense", "ghr1.eyJ2IjoxfQ"} {
		rec := b.do(http.MethodPost, "/api/firstrun/relay", map[string]string{"code": bad})
		wantError(t, rec, http.StatusBadRequest, "invalid_args")
		if strings.Contains(rec.Body.String(), testToken) {
			t.Fatal("the token is in the error")
		}
	}
	code, err := relay.FormatCode(relay.Code{Domain: testDomain, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	rec := b.do(http.MethodPost, "/api/firstrun/relay", map[string]string{"code": code})
	wantStatus(t, rec, http.StatusAccepted)
	rs := decodeAs[map[string]any](t, rec)
	if rs["domain"] != testDomain || rs["publicUrl"] != "https://"+testDomain || rs["state"] != "connecting" || rs["configured"] != true {
		t.Fatalf("relay status %v", rs)
	}
	if len(e.relay.codes) != 1 || e.relay.codes[0].Token != testToken {
		t.Fatalf("Connect got %+v", e.relay.codes)
	}
	st := decodeAs[state](t, b.do(http.MethodGet, "/api/firstrun/state", nil))
	if st.Step != "connecting" || st.Relay == nil || st.Relay.Domain != testDomain {
		t.Fatalf("state %+v", st)
	}
	// The relay keeps failing: after 30 s it is offline, with the error for "Подробнее".
	e.relay.set(func(f *fakeRelay) { f.status = relay.Status{LastError: "dial tcp: i/o timeout"} })
	if st := decodeAs[state](t, b.do(http.MethodGet, "/api/firstrun/state", nil)); st.Relay.State != "offline" || st.Relay.LastError == "" {
		t.Fatalf("state %+v", st.Relay)
	}
	// A configured relay is never replaced from this page, whatever the domain (review M3).
	other, _ := relay.FormatCode(relay.Code{Domain: "evil.example.com", Token: testToken})
	for _, c := range []string{code, other} {
		wantError(t, b.do(http.MethodPost, "/api/firstrun/relay", map[string]string{"code": c}), http.StatusConflict, "conflict")
	}
	if len(e.relay.codes) != 1 || e.relay.Domain() != testDomain {
		t.Fatalf("Connect got %+v", e.relay.codes)
	}

	recs, _ := e.st.Audit().Query(context.Background(), store.AuditFilter{})
	if len(recs) != 1 || recs[0].Action != "relay.change" || recs[0].Target != testDomain || strings.Contains(string(recs[0].Args), testToken) ||
		string(recs[0].Args) != `{"client":"192.168.68.20"}` {
		t.Fatalf("audit %+v", recs)
	}

	// A relay that fails to store its code answers 500 and changes nothing.
	e2 := newEnv(t)
	b2 := e2.browser()
	b2.signIn()
	e2.relay.set(func(f *fakeRelay) { f.err = errors.New("disk full") })
	wantError(t, b2.do(http.MethodPost, "/api/firstrun/relay", map[string]string{"code": code}), http.StatusInternalServerError, "internal")
}

func TestLinkNeedsTheRelay(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	b.signIn()
	wantError(t, b.do(http.MethodPost, "/api/firstrun/link", map[string]any{}), http.StatusConflict, "relay_not_ready")
	e.relay.set(func(f *fakeRelay) { f.domain = testDomain })
	wantError(t, b.do(http.MethodPost, "/api/firstrun/link", map[string]any{}), http.StatusConflict, "relay_not_ready")
	// Connected, but the outside check fails: the domain does not reach this hub.
	e.relay.set(func(f *fakeRelay) {
		f.status = relay.Status{Connected: true}
		f.checkedAt, f.failure = e.clock.Now(), "self-check: the domain reaches another Homie hub"
	})
	st := decodeAs[state](t, b.do(http.MethodGet, "/api/firstrun/state", nil))
	if st.Step != "connecting" || st.Relay.SelfCheck == nil || st.Relay.SelfCheck.OK {
		t.Fatalf("state %+v", st.Relay)
	}
	wantError(t, b.do(http.MethodPost, "/api/firstrun/link", map[string]any{}), http.StatusConflict, "relay_not_ready")
	e.connected()
	if st := decodeAs[state](t, b.do(http.MethodGet, "/api/firstrun/state", nil)); st.Step != "link" {
		t.Fatalf("state %+v", st)
	}
}

// The whole first run: PIN, relay, link, passkey on the phone; then the page closes.
func TestFirstRunEndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	b := e.browser()
	b.signIn()
	e.connected()

	pin := e.pin()
	rec := b.do(http.MethodPost, "/api/firstrun/link", map[string]any{})
	wantStatus(t, rec, http.StatusOK)
	first := decodeAs[map[string]string](t, rec)
	// Every issued link changes the PIN (review H2): a PIN seen on the LAN cannot fetch the next one.
	if pins := e.term.pins(); len(pins) != 2 || pins[1] == pin {
		t.Fatalf("PINs after a link %v", pins)
	}
	wantError(t, e.browser().do(http.MethodPost, "/api/firstrun/pin", map[string]string{"pin": pin}), http.StatusUnauthorized, "wrong_pin")
	rec = b.do(http.MethodPost, "/api/firstrun/link", map[string]any{}) // "Новая ссылка" revokes the first
	wantStatus(t, rec, http.StatusOK)
	link := decodeAs[map[string]string](t, rec)
	prefix := "https://" + testDomain + "/admin/setup#t="
	// A link from this page lives 3 minutes (review M2).
	if !strings.HasPrefix(link["url"], prefix) || link["url"] == first["url"] ||
		link["expiresAt"] != e.clock.Now().Add(LinkTTL).UTC().Format("2006-01-02T15:04:05.000Z") {
		t.Fatalf("link %v", link)
	}
	recs, _ := e.st.Audit().Query(ctx, store.AuditFilter{})
	if len(recs) != 2 || recs[0].Action != "setup.link" || recs[0].Via != "admin" || string(recs[0].Args) != `{"client":"192.168.68.20"}` {
		t.Fatalf("audit %+v", recs)
	}

	// The phone opens the link: the admin's registration with a passkey.
	a, err := auth.New(auth.Options{Store: e.st, Now: e.clock.Now,
		RelyingParty: func() (auth.RelyingParty, bool) { return auth.ForDomain(testDomain), true }})
	if err != nil {
		t.Fatal(err)
	}
	admin := a.Handler()
	post := func(path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "https://"+testDomain+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Homie", "1")
		rec := httptest.NewRecorder()
		admin.ServeHTTP(rec, req)
		return rec
	}
	oldToken := strings.TrimPrefix(first["url"], prefix)
	wantError(t, post("/api/admin/auth/token", map[string]string{"token": oldToken}), http.StatusGone, "token_used")
	token := strings.TrimPrefix(link["url"], prefix)
	rec = post("/api/admin/auth/register/begin", map[string]string{"token": token, "personName": "Roman", "homeName": "Мой шалаш"})
	wantStatus(t, rec, http.StatusOK)
	opts, err := virtualwebauthn.ParseAttestationOptions(rec.Body.String())
	if err != nil {
		t.Fatal(err)
	}
	var begin struct {
		CeremonyID string `json:"ceremonyId"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &begin)
	phone := virtualwebauthn.NewAuthenticator()
	cred := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
	answer := virtualwebauthn.CreateAttestationResponse(
		virtualwebauthn.RelyingParty{ID: testDomain, Name: "Homie", Origin: "https://" + testDomain}, phone, cred, *opts)
	wantStatus(t, post("/api/admin/auth/register/finish",
		map[string]any{"ceremonyId": begin.CeremonyID, "credential": json.RawMessage(answer)}), http.StatusCreated)

	// The page is closed now, for every path and every browser.
	for _, path := range []string{"/api/firstrun/state", "/api/firstrun/link", "/api/firstrun/pin"} {
		method := http.MethodPost
		if path == "/api/firstrun/state" {
			method = http.MethodGet
		}
		wantError(t, b.do(method, path, map[string]any{}), http.StatusGone, "setup_done")
	}
	rec = e.browser().do(http.MethodGet, "/firstrun", nil)
	wantStatus(t, rec, http.StatusGone)
	if !strings.Contains(rec.Body.String(), "Дом уже настроен") || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("done page %s", rec.Body)
	}
	// Only passkeys of the current relying party count (review L4): for another relay domain the page is needed again.
	if needed, err := Needed(ctx, e.st, "other.example.com"); err != nil || !needed {
		t.Fatalf("Needed for another domain %v %v", needed, err)
	}
	if needed, err := Needed(ctx, e.st, testDomain); err != nil || needed {
		t.Fatalf("Needed %v %v", needed, err)
	}
}

func TestHostGuard(t *testing.T) {
	e := newEnv(t)
	for host, ok := range map[string]bool{
		lanHostPort:                     true,
		"192.168.68.63":                 true,
		"[fe80::1]:8090":                true,
		"localhost:8090":                true,
		"romans-macbook-pro.local:8090": true,
		"homebox:8090":                  true,
		"homebox.lan":                   true,
		"router.home.arpa:8090":         true,
		"evil.example.com:8090":         false,
		"192.168.68.63.nip.io:8090":     false,
		"":                              false,
	} {
		b := e.browser()
		b.host = host
		rec := b.do(http.MethodGet, "/api/firstrun/state", nil)
		if ok && rec.Code != http.StatusOK || !ok && rec.Code != http.StatusMisdirectedRequest {
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
		"other LAN port":    {map[string]string{"Origin": "http://192.168.68.63:8091"}, false},
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
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-Id") == "" {
		t.Fatalf("headers %v", rec.Header())
	}
}

// The event stream sends the relay status at once and again on every change, and ends with the cookie.
func TestEvents(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	b := e.browser()
	b.signIn()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	if f := next(); !strings.HasPrefix(f, "id: 1\nevent: relay\ndata: ") || !strings.Contains(f, `"state":"not_configured"`) {
		t.Fatalf("relay frame %q", f)
	}
	e.connected()
	f := next()
	if !strings.HasPrefix(f, "id: 2\nevent: relay\n") || !strings.Contains(f, `"state":"connected"`) || !strings.Contains(f, `"selfCheck":{"ok":true`) {
		t.Fatalf("relay frame %q", f)
	}
	// The cookie expires: the stream ends.
	e.clock.Add(cookieTTL)
	if f := next(); f != "" {
		t.Fatalf("frame after the cookie expired: %q", f)
	}
}

// Serve prints where to go and the PIN, serves, and stops with the context.
func TestServe(t *testing.T) {
	st := authtest.New()
	term := &terminal{}
	s, err := New(Options{Store: st, Relay: &fakeRelay{}, Terminal: term})
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

	url := "http://" + ln.Addr().String() + "/api/firstrun/state"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
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
	if urls := PageURLs(&net.TCPAddr{IP: net.IPv4zero, Port: 8090}); len(urls) == 0 || urls[len(urls)-1] != "http://localhost:8090/firstrun" {
		t.Fatalf("urls %v", urls)
	}
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

// The page closes by itself after its time; the PIN changes on a timer meanwhile (review H2).
func TestServeClosesAndRotates(t *testing.T) {
	term := &terminal{}
	logs := &terminal{}
	s, err := New(Options{Store: authtest.New(), Relay: &fakeRelay{}, Terminal: term, OpenFor: 300 * time.Millisecond,
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
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Fatal("the port is still open")
	}
}

// Too many wrong PINs close the listener too, not only the answers.
func TestServeClosesAfterTheCap(t *testing.T) {
	s, err := New(Options{Store: authtest.New(), Relay: &fakeRelay{}})
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

// Without a terminal (launchd, systemd) the PIN is never printed or logged; the log says how to get in (review H2).
func TestNoTerminalNoPIN(t *testing.T) {
	logs := &terminal{}
	s, err := New(Options{Store: authtest.New(), Relay: &fakeRelay{}, OpenFor: 100 * time.Millisecond,
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
		t.Fatal("no store accepted")
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
