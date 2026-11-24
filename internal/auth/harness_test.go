package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"

	"github.com/devXpro/homie/internal/auth/authtest"
	"github.com/devXpro/homie/internal/store"
)

const (
	testDomain = "homie.example.com"
	testOrigin = "https://homie.example.com"
	safariMac  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Safari/605.1.15"
	safariIOS  = "Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Mobile/15E148 Safari/604.1"
)

// clock is the fake time every test runs on.
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

// env is one home: the fake store, the clock, Auth and a router with two probe endpoints behind the middlewares.
type env struct {
	t     *testing.T
	st    *authtest.Store
	clock *clock
	a     *Auth
	h     http.Handler

	mu     sync.Mutex
	ended  []string       // OnSessionEnd calls
	setups []store.Person // OnSetup calls
	noRP   bool
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, st: authtest.New(), clock: &clock{t: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)}}
	a, err := New(Options{
		Store: e.st,
		RelyingParty: func() (RelyingParty, bool) {
			e.mu.Lock()
			defer e.mu.Unlock()
			return ForDomain(testDomain), !e.noRP
		},
		OnSessionEnd: func(id string) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.ended = append(e.ended, id)
		},
		OnSetup: func(p store.Person, _ store.HomeSettings) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.setups = append(e.setups, p)
		},
		Version: "0.2.0",
		Now:     e.clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.a = a
	mux := http.NewServeMux()
	mux.Handle("/api/admin/", a.Handler())
	mux.Handle("/local/", http.StripPrefix("/local", a.LocalHandler()))
	mux.Handle("GET /test/session", a.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFrom(r.Context())
		_, _ = io.WriteString(w, p.Person.ID)
	})))
	mux.Handle("POST /test/dangerous", a.RequireStepUp(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	e.h = mux
	return e
}

func (e *env) endedSessions() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.ended...)
}

// link makes a setup link the way `homed admin setup-link` does and returns its token.
func (e *env) link() string {
	e.t.Helper()
	l, err := NewSetupLink(context.Background(), e.st, nil, LinkRequest{Origin: testOrigin, Via: "cli"}, e.clock.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	_, token, ok := strings.Cut(l.URL, "#t=")
	if !ok || !strings.HasPrefix(l.URL, testOrigin+"/admin/setup#t=") {
		e.t.Fatalf("setup link %q", l.URL)
	}
	return token
}

// addPerson stores a person directly, as the phase 1 import or T3.1 would.
func (e *env) addPerson(id, name string, role store.Role) store.Person {
	e.t.Helper()
	p := store.Person{ID: id, Name: name, Role: role, Plugins: []string{"*"}, UserHandle: randomBytes(32), CreatedAt: e.clock.Now()}
	if err := e.st.People().Create(context.Background(), p); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// audit returns the admin audit actions in the order they were written.
func (e *env) audit() []store.AuditRecord {
	e.t.Helper()
	recs, err := e.st.Audit().Query(context.Background(), store.AuditFilter{Limit: 200})
	if err != nil {
		e.t.Fatal(err)
	}
	for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}
	return recs
}

func (e *env) actions() []string {
	var out []string
	for _, r := range e.audit() {
		out = append(out, r.Action)
	}
	return out
}

// browser is one browser tab: it keeps the session cookie and sends the headers the SPA sends.
type browser struct {
	e      *env
	cookie string
	origin string // Origin header; "" sends none
	remote string
	xff    string
	ua     string
	header http.Header // extra headers
}

func (e *env) browser() *browser {
	return &browser{e: e, origin: testOrigin, remote: "127.0.0.1:50123", ua: safariMac, header: http.Header{}}
}

func (b *browser) do(method, path string, body any) *httptest.ResponseRecorder {
	b.e.t.Helper()
	var rd io.Reader
	switch v := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(v)
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			b.e.t.Fatal(err)
		}
		rd = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, "https://"+testDomain+path, rd)
	req.RemoteAddr = b.remote
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Homie", "1")
	if b.origin != "" {
		req.Header.Set("Origin", b.origin)
	}
	if b.xff != "" {
		req.Header.Set("X-Forwarded-For", b.xff)
	}
	req.Header.Set("User-Agent", b.ua)
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
			if c.MaxAge < 0 {
				b.cookie = ""
			} else {
				b.cookie = c.Value
			}
		}
	}
	return rec
}

// device is a phone or a Mac with one passkey: a virtual authenticator and its credential.
type device struct {
	rp   virtualwebauthn.RelyingParty
	auth virtualwebauthn.Authenticator
	cred virtualwebauthn.Credential
}

// newDevice is a synced platform passkey (backup eligible and backed up), like iCloud Keychain.
func newDevice() *device {
	return &device{
		rp: virtualwebauthn.RelyingParty{ID: testDomain, Name: rpDisplayName, Origin: testOrigin},
		auth: virtualwebauthn.NewAuthenticatorWithOptions(virtualwebauthn.AuthenticatorOptions{
			BackupEligible: true, BackupState: true,
		}),
		cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
	}
}

// create answers a begin response like navigator.credentials.create and remembers the user handle.
func (d *device) create(t *testing.T, begin []byte) json.RawMessage {
	t.Helper()
	opts, err := virtualwebauthn.ParseAttestationOptions(string(begin))
	if err != nil {
		t.Fatalf("attestation options: %v\n%s", err, begin)
	}
	d.auth.Options.UserHandle = []byte(opts.UserID)
	return json.RawMessage(virtualwebauthn.CreateAttestationResponse(d.rp, d.auth, d.cred, *opts))
}

// get answers a begin response like navigator.credentials.get.
func (d *device) get(t *testing.T, begin []byte) json.RawMessage {
	t.Helper()
	opts, err := virtualwebauthn.ParseAssertionOptions(string(begin))
	if err != nil {
		t.Fatalf("assertion options: %v\n%s", err, begin)
	}
	return json.RawMessage(virtualwebauthn.CreateAssertionResponse(d.rp, d.auth, d.cred, *opts))
}

// beginBody is what every begin answers.
type beginBody struct {
	CeremonyID string          `json:"ceremonyId"`
	PublicKey  json.RawMessage `json:"publicKey"`
}

// begin posts a begin and returns its raw body and ceremony id.
func (b *browser) begin(path string, body any) ([]byte, string) {
	b.e.t.Helper()
	rec := b.do(http.MethodPost, path, body)
	wantStatus(b.e.t, rec, http.StatusOK)
	bb := decodeAs[beginBody](b.e.t, rec)
	if bb.CeremonyID == "" || len(bb.PublicKey) == 0 {
		b.e.t.Fatalf("begin %s: %s", path, rec.Body)
	}
	return rec.Body.Bytes(), bb.CeremonyID
}

// register runs register/begin and /finish with the token and the device.
func (b *browser) register(d *device, token string, names map[string]string) *httptest.ResponseRecorder {
	b.e.t.Helper()
	body := map[string]string{"token": token}
	for k, v := range names {
		body[k] = v
	}
	raw, id := b.begin("/api/admin/auth/register/begin", body)
	return b.do(http.MethodPost, "/api/admin/auth/register/finish",
		map[string]any{"ceremonyId": id, "credential": d.create(b.e.t, raw), "passkeyName": "MacBook Pro"})
}

// login runs a discoverable sign-in with the device.
func (b *browser) login(d *device) *httptest.ResponseRecorder {
	b.e.t.Helper()
	raw, id := b.begin("/api/admin/auth/login/begin", map[string]any{})
	return b.do(http.MethodPost, "/api/admin/auth/login/finish",
		map[string]any{"ceremonyId": id, "credential": d.get(b.e.t, raw)})
}

// stepUp runs a step-up with the device.
func (b *browser) stepUp(d *device) *httptest.ResponseRecorder {
	b.e.t.Helper()
	raw, id := b.begin("/api/admin/auth/stepup/begin", map[string]any{})
	return b.do(http.MethodPost, "/api/admin/auth/stepup/finish",
		map[string]any{"ceremonyId": id, "credential": d.get(b.e.t, raw)})
}

// owner sets up a fresh home: a setup link, then the registration of the device as owner "Roman".
func (e *env) owner(d *device) *browser {
	e.t.Helper()
	b := e.browser()
	rec := b.register(d, e.link(), map[string]string{"personName": "Roman", "homeName": "Мой шалаш"})
	wantStatus(e.t, rec, http.StatusCreated)
	return b
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
}

type errorBody struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"requestId"`
		Fields    []struct {
			Path string `json:"path"`
			Code string `json:"code"`
		} `json:"fields"`
	} `json:"error"`
}

func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) errorBody {
	t.Helper()
	wantStatus(t, rec, status)
	body := decodeAs[errorBody](t, rec)
	if body.Error.Code != code {
		t.Fatalf("error code %q, want %q: %s", body.Error.Code, code, rec.Body)
	}
	if body.Error.RequestID == "" || body.Error.RequestID != rec.Header().Get("X-Request-Id") {
		t.Fatalf("requestId %q, header %q", body.Error.RequestID, rec.Header().Get("X-Request-Id"))
	}
	return body
}

func decodeAs[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %T: %v: %s", v, err, rec.Body)
	}
	return v
}

// me is the Me object the sign-in and registration answer.
type me struct {
	Person struct {
		ID           string   `json:"id"`
		Name         string   `json:"name"`
		Role         string   `json:"role"`
		Plugins      []string `json:"plugins"`
		CreatedAt    string   `json:"createdAt"`
		Passkeys     int      `json:"passkeys"`
		LastActiveAt string   `json:"lastActiveAt"`
	} `json:"person"`
	Session struct {
		ID            string `json:"id"`
		CreatedAt     string `json:"createdAt"`
		ExpiresAt     string `json:"expiresAt"`
		IdleExpiresAt string `json:"idleExpiresAt"`
		StepUpUntil   string `json:"stepUpUntil"`
	} `json:"session"`
	Home struct {
		Name     string `json:"name"`
		Language string `json:"language"`
	} `json:"home"`
	Version  string          `json:"version"`
	Features map[string]bool `json:"features"`
}
