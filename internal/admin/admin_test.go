package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/relay"
	"github.com/devXpro/homie/internal/store"
)

const origin = "https://homie.example"

type env struct {
	t     *testing.T
	st    *memStore
	host  *fakeHost
	auth  *fakeAuth
	relay *fakeRelay
	inst  *fakeInstaller
	keys  *fakeKeys
	aw    *AuditWriter
	s     *Server
	web   *httptest.Server
	local *httptest.Server
}

func newEnv(t *testing.T, mods ...func(*Options)) *env {
	t.Helper()
	st := newMemStore()
	created := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	st.people["roman"] = store.Person{ID: "roman", Name: "Roman", Role: store.RoleOwner, Plugins: []string{"*"}, CreatedAt: created}
	st.people["tanya"] = store.Person{ID: "tanya", Name: "Tanya", Role: store.RoleMember, Plugins: []string{"tv"}, CreatedAt: created}
	st.passkeys["pk1"] = store.Passkey{ID: "pk1", PersonID: "roman", Name: "MacBook Pro", CreatedAt: created}
	st.passkeys["pk2"] = store.Passkey{ID: "pk2", PersonID: "roman", Name: "iPhone", CreatedAt: created}
	st.passkeys["pk3"] = store.Passkey{ID: "pk3", PersonID: "tanya", Name: "iPhone", CreatedAt: created}
	st.plugins["tv"] = store.Plugin{ID: "tv", Source: store.PluginGitHub, Repo: "devXpro/homie-tv", Version: "0.2.0", Enabled: true}
	auth := &fakeAuth{st: st, byTok: map[string]Principal{}}
	for _, s := range []struct{ tok, id, person, passkey string }{
		{"tok-roman", "s_r1", "roman", "pk1"},
		{"tok-roman2", "s_r2", "roman", "pk2"},
		{"tok-tanya", "s_t1", "tanya", "pk3"},
	} {
		sess := store.Session{ID: s.id, PersonID: s.person, PasskeyID: s.passkey, Device: "Safari · " + s.id, CreatedAt: created, LastSeenAt: created}
		st.sessions[s.id] = sess
		auth.byTok[s.tok] = Principal{
			Person: st.people[s.person], Session: sess,
			ExpiresAt: created.Add(7 * 24 * time.Hour), IdleExpiresAt: created.Add(12 * time.Hour),
		}
	}
	e := &env{
		t: t, st: st, host: newFakeHost(), auth: auth,
		relay: &fakeRelay{domain: "homie.example"},
		inst:  &fakeInstaller{release: make(chan struct{}), st: st},
		keys:  &fakeKeys{st: st},
	}
	e.aw = NewAuditWriter(st.Audit(), st.People(), nil)
	opts := Options{
		Store: st, Host: e.host, Auth: auth, Relay: e.relay, Installer: e.inst, Keys: e.keys, Audit: e.aw,
		WithProgress: testWithProgress, Origin: origin, InstanceID: "inst-1", Version: "0.2.0",
		StartedAt: created, LocalName: "devxpro",
	}
	for _, mod := range mods {
		mod(&opts)
	}
	s, err := newServer(opts, func(s *Server) {
		s.pollEvery = 20 * time.Millisecond
		s.pingEvery = 50 * time.Millisecond
		s.logsEvery = 20 * time.Millisecond
		s.progressGap = 10 * time.Millisecond
	})
	if err != nil {
		t.Fatal(err)
	}
	e.s = s
	e.web = httptest.NewServer(s.Handler())
	e.local = httptest.NewServer(s.LocalHandler())
	t.Cleanup(func() {
		close(e.inst.release)
		e.web.Close()
		e.local.Close()
		s.Close()
		e.aw.Close()
	})
	return e
}

func (e *env) stepUp(tok string) { e.auth.setStepUp(tok, time.Now().Add(5*time.Minute)) }

// call describes one request. Mutating requests get the CSRF header and the JSON content type unless told not to.
type call struct {
	method, path, tok, body string
	local                   bool
	noCSRF                  bool
	header                  map[string]string
}

type reply struct {
	status int
	header http.Header
	body   map[string]any
	raw    string
}

func (r reply) errCode() string {
	m, _ := r.body["error"].(map[string]any)
	s, _ := m["code"].(string)
	return s
}

func (e *env) do(c call) reply {
	e.t.Helper()
	base := e.web.URL
	if c.local {
		base = e.local.URL
	}
	var body io.Reader
	if c.body != "" {
		body = strings.NewReader(c.body)
	}
	req, err := http.NewRequestWithContext(context.Background(), c.method, base+"/api/admin"+c.path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if c.tok != "" {
		req.Header.Set("Cookie", sessionCookie+"="+c.tok)
	}
	if c.method != http.MethodGet && !c.noCSRF {
		req.Header.Set(csrfHeader, "1")
	}
	if c.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := reply{status: resp.StatusCode, header: resp.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func expect(t *testing.T, r reply, status int, code string) {
	t.Helper()
	if r.status != status || r.errCode() != code {
		t.Fatalf("got %d %q, want %d %q; body %s", r.status, r.errCode(), status, code, r.raw)
	}
}

func TestHeadersAndErrorShape(t *testing.T) {
	e := newEnv(t)
	r := e.do(call{method: "GET", path: "/bootstrap"})
	expect(t, r, 200, "")
	for _, h := range []string{"X-Request-Id", "Cache-Control", "X-Content-Type-Options", instanceHeader} {
		if r.header.Get(h) == "" {
			t.Errorf("header %s missing", h)
		}
	}
	if r.body["homeName"] != "Мой шалаш" || r.body["ownerReady"] != true {
		t.Errorf("bootstrap = %s", r.raw)
	}
	if strings.Contains(r.raw, "version") {
		t.Errorf("bootstrap reveals the version: %s", r.raw)
	}

	r = e.do(call{method: "GET", path: "/nope", tok: "tok-roman"})
	expect(t, r, 404, "not_found")
	if id := r.body["error"].(map[string]any)["requestId"]; id != r.header.Get("X-Request-Id") {
		t.Errorf("requestId %v differs from header %q", id, r.header.Get("X-Request-Id"))
	}
	r = e.do(call{method: "DELETE", path: "/overview", tok: "tok-roman"})
	expect(t, r, 405, "method_not_allowed")
	if r.header.Get("Allow") == "" {
		t.Error("405 without Allow")
	}
}

func TestAuthLevels(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name   string
		c      call
		status int
		code   string
	}{
		{"no cookie", call{method: "GET", path: "/plugins"}, 401, "unauthenticated"},
		{"unknown cookie", call{method: "GET", path: "/plugins", tok: "nope"}, 401, "unauthenticated"},
		{"member on owner endpoint", call{method: "GET", path: "/plugins", tok: "tok-tanya"}, 403, "forbidden"},
		{"member on session endpoint", call{method: "GET", path: "/session", tok: "tok-tanya"}, 200, ""},
		{"owner", call{method: "GET", path: "/plugins", tok: "tok-roman"}, 200, ""},
		{"socket has no session endpoint", call{method: "GET", path: "/session", local: true}, 404, "not_found"},
		{"socket is the owner", call{method: "GET", path: "/plugins", local: true}, 200, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expect(t, e.do(tt.c), tt.status, tt.code)
		})
	}
}

func TestCSRF(t *testing.T) {
	e := newEnv(t)
	patch := func(mod func(*call)) reply {
		c := call{method: "PATCH", path: "/settings", tok: "tok-roman", body: `{"language":"en"}`}
		mod(&c)
		return e.do(c)
	}
	expect(t, patch(func(c *call) { c.noCSRF = true }), 403, "csrf_failed")
	expect(t, patch(func(c *call) { c.header = map[string]string{"Origin": "https://evil.example"} }), 403, "csrf_failed")
	expect(t, patch(func(c *call) { c.header = map[string]string{"Sec-Fetch-Site": "cross-site"} }), 403, "csrf_failed")
	expect(t, patch(func(c *call) {
		c.header = map[string]string{"Origin": origin, "Sec-Fetch-Site": "same-origin"}
	}), 200, "")
	// The socket needs no header.
	expect(t, patch(func(c *call) { c.local, c.noCSRF, c.tok = true, true, "" }), 200, "")
	// CSRF comes before authentication, so a forged request learns nothing.
	expect(t, e.do(call{method: "POST", path: "/relay/check", noCSRF: true}), 403, "csrf_failed")
}

func TestBodyRules(t *testing.T) {
	e := newEnv(t)
	r := e.do(call{method: "PATCH", path: "/settings", tok: "tok-roman", body: `{"language":"en"}`,
		header: map[string]string{"Content-Type": "text/plain"}})
	expect(t, r, 415, "unsupported_media_type")
	expect(t, e.do(call{method: "PATCH", path: "/settings", tok: "tok-roman", body: `{"language":`}), 400, "invalid_json")
	expect(t, e.do(call{method: "PATCH", path: "/settings", tok: "tok-roman", body: `[1]`}), 400, "invalid_json")
	big := `{"homeName":"` + strings.Repeat("a", maxBodyBytes) + `"}`
	expect(t, e.do(call{method: "PATCH", path: "/settings", tok: "tok-roman", body: big}), 413, "too_large")
	r = e.do(call{method: "PATCH", path: "/settings", tok: "tok-roman", body: `{"language":"de"}`})
	expect(t, r, 400, "invalid_args")
	if f := r.body["error"].(map[string]any)["fields"].([]any)[0].(map[string]any); f["path"] != "/language" {
		t.Errorf("fields = %v", f)
	}
}

func TestStepUpAndKeys(t *testing.T) {
	e := newEnv(t)
	create := call{method: "POST", path: "/people/roman/keys", tok: "tok-roman", body: `{"label":"ChatGPT Site"}`}
	expect(t, e.do(create), 403, "step_up_required")
	if len(e.st.keys) != 0 {
		t.Fatal("a key was created without step-up")
	}
	e.stepUp("tok-roman")
	r := e.do(create)
	expect(t, r, 201, "")
	secret, _ := r.body["secret"].(string)
	key := r.body["key"].(map[string]any)
	if !strings.HasPrefix(secret, "ghk_") || key["hint"] != secret[:8] || key["personId"] != "roman" {
		t.Fatalf("create = %s", r.raw)
	}
	entries, updates := e.keys.snapshot()
	if updates != 1 || len(entries) != 1 || entries[0].Person.Name != "Roman" {
		t.Fatalf("resolver not updated before the answer: %d updates, %v", updates, entries)
	}
	if strings.Contains(e.do(call{method: "GET", path: "/keys", tok: "tok-roman"}).raw, secret) {
		t.Fatal("the list shows the secret")
	}
	expect(t, e.do(call{method: "POST", path: "/people/nobody/keys", tok: "tok-roman", body: `{"label":"x"}`}), 404, "not_found")
	expect(t, e.do(call{method: "POST", path: "/people/roman/keys", tok: "tok-roman", body: `{"label":" "}`}), 400, "invalid_args")

	// Another session of the same person is not stepped up.
	expect(t, e.do(call{method: "DELETE", path: "/keys/" + key["id"].(string), tok: "tok-roman2"}), 403, "step_up_required")
	expect(t, e.do(call{method: "DELETE", path: "/keys/" + key["id"].(string), tok: "tok-roman"}), 204, "")
	if entries, updates := e.keys.snapshot(); updates != 2 || len(entries) != 0 {
		t.Fatalf("after revoke: %d updates, %v", updates, entries)
	}
	// Expired step-up.
	e.auth.setStepUp("tok-roman", time.Now().Add(-time.Second))
	expect(t, e.do(create), 403, "step_up_required")
	// The socket needs no step-up.
	expect(t, e.do(call{method: "POST", path: "/people/roman/keys", local: true, body: `{"label":"CLI"}`}), 201, "")

	waitUntil(t, "admin audit records", func() bool {
		n := 0
		for _, rec := range e.st.auditRecords() {
			if rec.Kind == store.AuditAdmin && strings.HasPrefix(rec.Action, "key.") {
				n++
			}
		}
		return n == 3
	})
	for _, rec := range e.st.auditRecords() {
		if strings.Contains(string(rec.Args), secret) {
			t.Fatal("the secret reached the audit log")
		}
	}
}

func TestMePasskeysAndSessions(t *testing.T) {
	e := newEnv(t)
	r := e.do(call{method: "GET", path: "/me/passkeys", tok: "tok-roman"})
	expect(t, r, 200, "")
	items := r.body["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["current"] != true {
		t.Fatalf("passkeys = %s", r.raw)
	}
	expect(t, e.do(call{method: "PATCH", path: "/me/passkeys/pk3", tok: "tok-roman", body: `{"name":"x"}`}), 404, "not_found")
	r = e.do(call{method: "PATCH", path: "/me/passkeys/pk2", tok: "tok-roman", body: `{"name":"  Phone  "}`})
	if expect(t, r, 200, ""); r.body["name"] != "Phone" {
		t.Fatalf("rename = %s", r.raw)
	}
	expect(t, e.do(call{method: "DELETE", path: "/me/passkeys/pk3", tok: "tok-tanya"}), 403, "step_up_required")
	e.stepUp("tok-tanya")
	expect(t, e.do(call{method: "DELETE", path: "/me/passkeys/pk3", tok: "tok-tanya"}), 409, "last_passkey")

	// Removing pk2 ends the session signed in with it.
	e.stepUp("tok-roman")
	expect(t, e.do(call{method: "DELETE", path: "/me/passkeys/pk2", tok: "tok-roman"}), 204, "")
	expect(t, e.do(call{method: "GET", path: "/session", tok: "tok-roman2"}), 401, "unauthenticated")

	r = e.do(call{method: "GET", path: "/session", tok: "tok-roman"})
	expect(t, r, 200, "")
	if r.body["person"].(map[string]any)["passkeys"] != float64(1) || r.body["session"].(map[string]any)["stepUpUntil"] == nil {
		t.Fatalf("session = %s", r.raw)
	}

	r = e.do(call{method: "PATCH", path: "/me", tok: "tok-tanya", body: `{"name":"Таня","language":"en"}`})
	if expect(t, r, 200, ""); r.body["name"] != "Таня" || r.body["language"] != "en" {
		t.Fatalf("patch me = %s", r.raw)
	}

	expect(t, e.do(call{method: "DELETE", path: "/me/sessions/s_t1", tok: "tok-roman"}), 404, "not_found")
	r = e.do(call{method: "DELETE", path: "/me/sessions/s_t1", tok: "tok-tanya"})
	expect(t, r, 204, "")
	if !strings.Contains(r.header.Get("Set-Cookie"), sessionCookie+"=;") {
		t.Errorf("deleting the current session keeps the cookie: %q", r.header.Get("Set-Cookie"))
	}
}

func TestToolsAndPlugins(t *testing.T) {
	e := newEnv(t)
	r := e.do(call{method: "GET", path: "/tools?audience=assistant", tok: "tok-roman"})
	if expect(t, r, 200, ""); len(r.body["items"].([]any)) != 2 {
		t.Fatalf("assistant tools = %s", r.raw)
	}
	r = e.do(call{method: "GET", path: "/tools", tok: "tok-roman"})
	expect(t, r, 200, "")
	var pair map[string]any
	for _, it := range r.body["items"].([]any) {
		if m := it.(map[string]any); m["name"] == "tv_pair" {
			pair = m
		}
	}
	if pair["target"] != "tv" || pair["timeoutMs"] != float64(180000) || pair["i18n"] == nil {
		t.Fatalf("tv_pair = %v", pair)
	}

	r = e.do(call{method: "GET", path: "/plugins/tv", tok: "tok-roman"})
	expect(t, r, 200, "")
	if r.body["name"] != "homie-tv" || r.body["inventoryTool"] != "tv_list" || len(r.body["tools"].([]any)) != 3 ||
		r.body["command"] != nil {
		t.Fatalf("detail = %s", r.raw)
	}

	// Inventory: read, cached, counted.
	r = e.do(call{method: "GET", path: "/plugins/tv/inventory", tok: "tok-roman"})
	expect(t, r, 200, "")
	items := r.body["items"].([]any)
	if len(items) != 2 || items[1].(map[string]any)["state"] != "unknown" {
		t.Fatalf("inventory = %s", r.raw)
	}
	e.do(call{method: "GET", path: "/plugins/tv/inventory", tok: "tok-roman"})
	if n := e.host.callCount("tv_list"); n != 1 {
		t.Fatalf("inventory called %d times, want 1 (cache)", n)
	}
	r = e.do(call{method: "GET", path: "/plugins", tok: "tok-roman"})
	if p := r.body["items"].([]any)[0].(map[string]any); p["devices"] != float64(2) {
		t.Fatalf("summary = %v", p)
	}

	// Disable: state "disabled", audited.
	r = e.do(call{method: "PATCH", path: "/plugins/tv", tok: "tok-roman", body: `{"enabled":false}`})
	if expect(t, r, 200, ""); r.body["state"] != "disabled" {
		t.Fatalf("patch = %s", r.raw)
	}

	r = e.do(call{method: "GET", path: "/plugins/tv/logs?tail=1", tok: "tok-roman"})
	if expect(t, r, 200, ""); len(r.body["lines"].([]any)) != 1 {
		t.Fatalf("logs = %s", r.raw)
	}
	expect(t, e.do(call{method: "GET", path: "/plugins/tv/logs?tail=0", tok: "tok-roman"}), 400, "invalid_args")
	expect(t, e.do(call{method: "GET", path: "/plugins/nope", tok: "tok-roman"}), 404, "not_found")
}

func TestCallValidation(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name, args string
		path, code string
		message    string
	}{
		{"maximum", `{"tv":"tv","percent":150}`, "/percent", "maximum", "must be at most 100"},
		{"minimum", `{"tv":"tv","percent":-1}`, "/percent", "minimum", "must be at least 0"},
		{"type", `{"tv":5,"percent":1}`, "/tv", "type", "must be a string"},
		{"integer", `{"tv":"tv","percent":1.5}`, "/percent", "type", "must be an integer"},
		{"enum", `{"tv":"tv","percent":1,"mode":"x"}`, "/mode", "enum", `must be one of "quiet", "loud"`},
		{"required", `{"tv":"tv"}`, "/percent", "required", "is required"},
		{"additional", `{"tv":"tv","percent":1,"x":1}`, "/x", "additionalProperties", "is not expected"},
		{"not an object", `[1]`, "/args", "type", "must be a JSON object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := e.do(call{method: "POST", path: "/tools/tv_volume/calls", tok: "tok-roman", body: `{"args":` + tt.args + `}`})
			expect(t, r, 400, "invalid_args")
			f := r.body["error"].(map[string]any)["fields"].([]any)[0].(map[string]any)
			if f["path"] != tt.path || f["code"] != tt.code || f["message"] != tt.message {
				t.Fatalf("field = %v", f)
			}
		})
	}
	if n := e.host.callCount("tv_volume"); n != 0 {
		t.Fatalf("invalid calls reached the plugin %d times", n)
	}
	expect(t, e.do(call{method: "POST", path: "/tools/tv_nope/calls", tok: "tok-roman", body: `{}`}), 404, "unknown_tool")
	big := `{"args":{"tv":"` + strings.Repeat("a", maxArgsBytes) + `"}}`
	expect(t, e.do(call{method: "POST", path: "/tools/tv_pair/calls", tok: "tok-roman", body: big}), 413, "too_large")

	e.host.setState("tv", hub.PluginFailing)
	expect(t, e.do(call{method: "POST", path: "/tools/tv_pair/calls", tok: "tok-roman", body: `{"args":{"tv":"tv"}}`}),
		503, "plugin_unavailable")
}

func TestCallLifecycle(t *testing.T) {
	e := newEnv(t)
	gate := e.host.gate("tv_pair")
	r := e.do(call{method: "POST", path: "/tools/tv_pair/calls", tok: "tok-roman", body: `{"args":{"tv":"tv","pin":"1234"}}`})
	expect(t, r, 202, "")
	id := r.body["id"].(string)
	if r.body["state"] != "running" || r.body["args"].(map[string]any)["pin"] != "[redacted]" {
		t.Fatalf("call = %s", r.raw)
	}

	// One running call per tool.
	r = e.do(call{method: "POST", path: "/tools/tv_pair/calls", tok: "tok-roman", body: `{"args":{"tv":"tv"}}`})
	expect(t, r, 409, "call_running")
	if r.body["error"].(map[string]any)["callId"] != id {
		t.Fatalf("409 body = %s", r.raw)
	}

	// Progress arrives (the fake host reports once before blocking).
	waitUntil(t, "progress", func() bool {
		r := e.do(call{method: "GET", path: "/calls/" + id, tok: "tok-roman"})
		p, _ := r.body["progress"].(map[string]any)
		return p != nil && p["message"] == "Press OK on the TV remote" && p["messageI18n"] != nil
	})
	r = e.do(call{method: "GET", path: "/calls?state=running", tok: "tok-roman"})
	if len(r.body["items"].([]any)) != 1 {
		t.Fatalf("running = %s", r.raw)
	}

	gate <- hub.Result{Text: "Living room TV is paired.", Structured: json.RawMessage(`{"paired":true}`)}
	waitUntil(t, "call end", func() bool {
		return e.do(call{method: "GET", path: "/calls/" + id, tok: "tok-roman"}).body["state"] == "ok"
	})
	r = e.do(call{method: "GET", path: "/calls/" + id, tok: "tok-roman"})
	res := r.body["result"].(map[string]any)
	if res["text"] != "Living room TV is paired." || res["structured"] == nil || r.body["durationMs"] == nil {
		t.Fatalf("finished = %s", r.raw)
	}
	if c := e.host.calls[0].Caller; c.Person != "Roman" || c.Via != "admin" {
		t.Fatalf("caller = %+v", c)
	}

	// wait=1 answers the finished call; a failing tool ends "failed".
	r = e.do(call{method: "POST", path: "/tools/tv_volume/calls?wait=1", local: true, body: `{"args":{"tv":"tv","percent":5,"mode":"loud"}}`})
	expect(t, r, 200, "")
	if r.body["state"] != "failed" || r.body["result"].(map[string]any)["text"] != "Too loud for the neighbours." {
		t.Fatalf("wait = %s", r.raw)
	}

	// Cancel.
	e.host.gate("tv_pair")
	r = e.do(call{method: "POST", path: "/tools/tv_pair/calls", tok: "tok-roman", body: `{"args":{"tv":"tv"}}`})
	id2 := r.body["id"].(string)
	expect(t, e.do(call{method: "DELETE", path: "/calls/" + id2, tok: "tok-roman"}), 202, "")
	waitUntil(t, "cancelled", func() bool {
		return e.do(call{method: "GET", path: "/calls/" + id2, tok: "tok-roman"}).body["state"] == "cancelled"
	})

	// Every call is audited with its surface.
	waitUntil(t, "tool audit", func() bool {
		vias := map[string]int{}
		for _, rec := range e.st.auditRecords() {
			if rec.Kind == store.AuditTool {
				vias[rec.Via]++
			}
		}
		return vias["admin"] == 2 && vias["cli"] == 1
	})
	for _, rec := range e.st.auditRecords() {
		if strings.Contains(string(rec.Args), "1234") {
			t.Fatal("the pin reached the audit log")
		}
		if rec.Kind == store.AuditTool && rec.PersonID != "roman" {
			t.Fatalf("person id not filled: %+v", rec)
		}
	}
}

func TestAuditPaging(t *testing.T) {
	e := newEnv(t)
	var recs []store.AuditRecord
	for i := range 5 {
		recs = append(recs, store.AuditRecord{At: time.Unix(int64(i), 0), Kind: store.AuditTool, Via: "site", Tool: "tv_list", PluginID: "tv", OK: true})
	}
	recs = append(recs, store.AuditRecord{At: time.Unix(9, 0), Kind: store.AuditAdmin, Via: "admin", Action: "auth.login", OK: true})
	if _, err := e.st.Audit().Append(context.Background(), recs); err != nil {
		t.Fatal(err)
	}
	r := e.do(call{method: "GET", path: "/audit?kind=tool&limit=2", tok: "tok-roman"})
	expect(t, r, 200, "")
	items := r.body["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["id"] != "5" || items[0].(map[string]any)["toolTitle"] != "List TVs" {
		t.Fatalf("page 1 = %s", r.raw)
	}
	cur := r.body["nextCursor"].(string)
	r = e.do(call{method: "GET", path: "/audit?kind=tool&limit=2&cursor=" + cur, tok: "tok-roman"})
	if items := r.body["items"].([]any); items[0].(map[string]any)["id"] != "3" {
		t.Fatalf("page 2 = %s", r.raw)
	}
	r = e.do(call{method: "GET", path: "/audit?kind=admin&cursor=" + cur, tok: "tok-roman"})
	expect(t, r, 400, "invalid_args")
	if r.body["error"].(map[string]any)["fields"].([]any)[0].(map[string]any)["path"] != "/cursor" {
		t.Fatalf("cursor error = %s", r.raw)
	}
	r = e.do(call{method: "GET", path: "/audit/6", tok: "tok-roman"})
	if expect(t, r, 200, ""); r.body["action"] != "auth.login" {
		t.Fatalf("get = %s", r.raw)
	}
	expect(t, e.do(call{method: "GET", path: "/audit/99", tok: "tok-roman"}), 404, "not_found")
	r = e.do(call{method: "GET", path: "/overview", tok: "tok-roman"})
	if expect(t, r, 200, ""); len(r.body["recent"].([]any)) != 5 {
		t.Fatalf("overview = %s", r.raw)
	}
}

func TestRelayAndSystem(t *testing.T) {
	e := newEnv(t)
	e.relay.setStatus(relay.Status{LastError: "dial tcp: i/o timeout", Since: time.Now().Add(-time.Minute)})
	r := e.do(call{method: "GET", path: "/relay", tok: "tok-roman"})
	if expect(t, r, 200, ""); r.body["state"] != "offline" || r.body["publicUrl"] != "https://homie.example" {
		t.Fatalf("relay = %s", r.raw)
	}
	r = e.do(call{method: "GET", path: "/overview", tok: "tok-roman"})
	if a := r.body["attention"].([]any); len(a) == 0 || a[0].(map[string]any)["kind"] != "relay_offline" {
		t.Fatalf("attention = %s", r.raw)
	}

	same := encodeCode(t, "homie.example")
	other := encodeCode(t, "other.example")
	e.stepUp("tok-roman")
	expect(t, e.do(call{method: "PUT", path: "/relay", tok: "tok-roman", body: `{"code":"bad"}`}), 400, "invalid_args")
	expect(t, e.do(call{method: "PUT", path: "/relay", tok: "tok-roman", body: `{"code":"` + other + `"}`}), 409, "domain_change_local_only")
	r = e.do(call{method: "PUT", path: "/relay", tok: "tok-roman", body: `{"code":"` + same + `"}`})
	if expect(t, r, 202, ""); r.body["state"] != "connecting" {
		t.Fatalf("put = %s", r.raw)
	}
	expect(t, e.do(call{method: "PUT", path: "/relay", local: true, body: `{"code":"` + other + `"}`}), 202, "")
	if e.relay.Domain() != "other.example" {
		t.Fatal("the socket could not change the domain")
	}

	r = e.do(call{method: "POST", path: "/system/check-update", tok: "tok-roman", body: `{}`})
	if expect(t, r, 200, ""); r.body["update"].(map[string]any)["latest"] != "0.3.0" {
		t.Fatalf("system = %s", r.raw)
	}
}

func encodeCode(t *testing.T, domain string) string {
	t.Helper()
	code, err := relay.FormatCode(relay.Code{Domain: domain, Token: strings.Repeat("ab", 32)})
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestJobs(t *testing.T) {
	e := newEnv(t)
	e.stepUp("tok-roman")
	expect(t, e.do(call{method: "POST", path: "/plugins", tok: "tok-roman", body: `{"repo":"devXpro/homie-tv","id":"tv"}`}), 409, "conflict")
	expect(t, e.do(call{method: "POST", path: "/plugins", tok: "tok-roman", body: `{"repo":"nope"}`}), 400, "invalid_args")
	expect(t, e.do(call{method: "POST", path: "/plugins", tok: "tok-roman",
		body: `{"source":"local","id":"x1","command":["/bin/x"]}`}), 400, "invalid_args")

	r := e.do(call{method: "POST", path: "/plugins", tok: "tok-roman", body: `{"repo":"devXpro/homie-kettle","version":"0.1.0","id":"kettle"}`})
	expect(t, r, 202, "")
	id := r.body["id"].(string)
	if r.body["kind"] != "plugin.install" || r.body["step"] != "resolve" {
		t.Fatalf("job = %s", r.raw)
	}
	r = e.do(call{method: "POST", path: "/plugins", tok: "tok-roman", body: `{"repo":"devXpro/homie-kettle","id":"kettle"}`})
	expect(t, r, 409, "job_running")
	if r.body["error"].(map[string]any)["jobId"] != id {
		t.Fatalf("409 = %s", r.raw)
	}
	waitUntil(t, "job report", func() bool {
		return e.do(call{method: "GET", path: "/jobs/" + id, tok: "tok-roman"}).body["step"] == "download"
	})
	e.inst.release <- struct{}{}
	waitUntil(t, "job end", func() bool {
		return e.do(call{method: "GET", path: "/jobs/" + id, tok: "tok-roman"}).body["state"] == "ok"
	})
	waitUntil(t, "install audit", func() bool {
		for _, rec := range e.st.auditRecords() {
			if rec.Action == "plugin.install" && rec.Target == "kettle" && rec.OK {
				return true
			}
		}
		return false
	})

	r = e.do(call{method: "POST", path: "/plugins", tok: "tok-roman", body: `{"repo":"devXpro/broken"}`})
	bid := r.body["id"].(string)
	e.inst.release <- struct{}{}
	waitUntil(t, "failed job", func() bool {
		r := e.do(call{method: "GET", path: "/jobs/" + bid, tok: "tok-roman"})
		er, _ := r.body["error"].(map[string]any)
		return r.body["state"] == "failed" && er["code"] == "checksum_mismatch"
	})

	r = e.do(call{method: "GET", path: "/plugins/preview?repo=devXpro/homie-tv", tok: "tok-roman"})
	if expect(t, r, 200, ""); r.body["installed"].(map[string]any)["version"] != "0.2.0" {
		t.Fatalf("preview = %s", r.raw)
	}
	expect(t, e.do(call{method: "GET", path: "/plugins/preview?repo=a/b", tok: "tok-roman"}), 404, "not_found")
	r = e.do(call{method: "POST", path: "/plugins/check-updates", tok: "tok-roman", body: `{}`})
	if expect(t, r, 200, ""); !strings.Contains(r.raw, `"update":{"latest":"0.3.0"`) {
		t.Fatalf("check-updates = %s", r.raw)
	}

	// The socket adds local plugins.
	r = e.do(call{method: "POST", path: "/plugins", local: true, body: `{"source":"local","id":"lamp","command":["/opt/lamp","serve"]}`})
	if expect(t, r, 201, ""); r.body["source"].(map[string]any)["kind"] != "local" {
		t.Fatalf("local add = %s", r.raw)
	}
}

// Passkeys of an old relay domain open nothing: they do not count for ownerReady or as the last passkey (review L4).
func TestPasskeysOfAnotherDomain(t *testing.T) {
	var mu sync.Mutex
	stale := map[string]bool{"pk1": true}
	e := newEnv(t, func(o *Options) {
		o.UsablePasskey = func(pk store.Passkey) bool {
			mu.Lock()
			defer mu.Unlock()
			return !stale[pk.ID]
		}
	})
	e.stepUp("tok-roman2") // the session of pk2
	// pk2 is Roman's only passkey of the current domain: it stays, though the store holds two.
	expect(t, e.do(call{method: "DELETE", path: "/me/passkeys/pk2", tok: "tok-roman2"}), 409, "last_passkey")
	// The old domain's pk1 can go.
	expect(t, e.do(call{method: "DELETE", path: "/me/passkeys/pk1", tok: "tok-roman2"}), 204, "")
	if r := e.do(call{method: "GET", path: "/bootstrap"}); r.body["ownerReady"] != true {
		t.Fatalf("bootstrap = %s", r.raw)
	}
	// With every passkey of the owner from another domain, the home has no owner who can sign in.
	mu.Lock()
	stale["pk2"] = true
	mu.Unlock()
	if r := e.do(call{method: "GET", path: "/bootstrap"}); r.body["ownerReady"] != false {
		t.Fatalf("bootstrap with stale passkeys = %s", r.raw)
	}
}

// Names follow the registration's rule everywhere, and PATCH /me is audited (review L7, L3).
func TestNamesAndPatchMe(t *testing.T) {
	e := newEnv(t)
	for _, bad := range []string{`say "hi"`, "line\nbreak", `back\slash`, "tick`", "rtl\u202eover", "a\u2028b"} {
		name, _ := json.Marshal(bad)
		for _, c := range []call{
			{method: "PATCH", path: "/me", tok: "tok-roman", body: `{"name":` + string(name) + `}`},
			{method: "PATCH", path: "/settings", tok: "tok-roman", body: `{"homeName":` + string(name) + `}`},
			{method: "PATCH", path: "/me/passkeys/pk1", tok: "tok-roman", body: `{"name":` + string(name) + `}`},
		} {
			if r := e.do(c); r.status != 400 || r.errCode() != "invalid_args" {
				t.Errorf("%s %s %q: %d %s", c.method, c.path, bad, r.status, r.raw)
			}
		}
	}
	r := e.do(call{method: "PATCH", path: "/me", tok: "tok-roman", body: `{"name":"«Роман»","language":"en"}`})
	if expect(t, r, 200, ""); r.body["name"] != "«Роман»" {
		t.Fatalf("patch = %s", r.raw)
	}
	expect(t, e.do(call{method: "PATCH", path: "/me", tok: "tok-roman", body: `{"language":"en"}`}), 200, "") // no change
	waitUntil(t, "person.update audit", func() bool {
		var n int
		for _, rec := range e.st.auditRecords() {
			if rec.Action == "person.update" {
				n++
				if rec.Target != "roman" || rec.Via != "admin" || !strings.Contains(string(rec.Args), `"changed":["name","language"]`) {
					t.Fatalf("audit %+v %s", rec, rec.Args)
				}
			}
		}
		return n == 1
	})
	if _, rebuilds := e.keys.snapshot(); rebuilds != 1 {
		t.Fatalf("%d key rebuilds, want 1 (only the real change)", rebuilds)
	}
}

// The audit of an admin tool call keeps the gateway's text for a validation failure, which may quote the redacted
// arguments; the caller still sees the plugin's own words (review L3).
func TestToolCallAuditText(t *testing.T) {
	e := newEnv(t)
	gate := e.host.gate("tv_volume")
	r := e.do(call{method: "POST", path: "/tools/tv_volume/calls", tok: "tok-roman", body: `{"args":{"tv":"tv","percent":5}}`})
	expect(t, r, 202, "")
	id := r.body["id"].(string)
	gate <- hub.Result{IsError: true, Text: `validating "arguments": pin: "4711" does not match`}
	waitUntil(t, "call end", func() bool {
		return e.do(call{method: "GET", path: "/calls/" + id, tok: "tok-roman"}).body["state"] == "failed"
	})
	if r := e.do(call{method: "GET", path: "/calls/" + id, tok: "tok-roman"}); !strings.Contains(r.raw, "4711") {
		t.Fatalf("the caller lost the plugin's text: %s", r.raw)
	}
	waitUntil(t, "tool audit", func() bool {
		for _, rec := range e.st.auditRecords() {
			if rec.Kind == store.AuditTool && rec.Tool == "tv_volume" {
				if strings.Contains(rec.Error, "4711") || !strings.HasPrefix(rec.Error, "invalid arguments") {
					t.Fatalf("audit error %q", rec.Error)
				}
				return true
			}
		}
		return false
	})
}

// Downgrades of the hub and of plugins happen only on the socket, with the explicit flag (review L5).
func TestDowngradeOnlyOnTheSocket(t *testing.T) {
	e := newEnv(t)
	e.stepUp("tok-roman")
	// The web never downgrades: an older version is refused at once, and so is the flag itself.
	for _, c := range []struct{ path, body, code string }{
		{"/plugins/tv/update", `{"version":"0.1.0"}`, "downgrade"},
		{"/plugins/tv/update", `{"version":"v0.1.9"}`, "downgrade"},
		{"/plugins/tv/update", `{"version":"0.1.0","allowDowngrade":true}`, "forbidden"},
		{"/plugins/tv/update", `{"allowDowngrade":true}`, "forbidden"},
		{"/system/update", `{"version":"0.1.0"}`, "downgrade"},
		{"/system/update", `{"allowDowngrade":true}`, "forbidden"},
	} {
		r := e.do(call{method: "POST", path: c.path, tok: "tok-roman", body: c.body})
		if r.errCode() != c.code {
			t.Errorf("%s %s: %d %s", c.path, c.body, r.status, r.raw)
		}
	}
	// The socket needs the flag too.
	expect(t, e.do(call{method: "POST", path: "/plugins/tv/update", local: true, body: `{"version":"0.1.0"}`}), 409, "downgrade")
	r := e.do(call{method: "POST", path: "/plugins/tv/update", local: true, body: `{"version":"0.1.0","allowDowngrade":true}`})
	expect(t, r, 202, "")
	jobDone(t, e, r.body["id"].(string))
	r = e.do(call{method: "POST", path: "/system/update", local: true, body: `{"version":"0.1.0","allowDowngrade":true}`})
	expect(t, r, 202, "")
	jobDone(t, e, r.body["id"].(string))
	// A web update of the latest release reaches the installer with the downgrade refused (it checks "latest").
	r = e.do(call{method: "POST", path: "/plugins/tv/update", tok: "tok-roman", body: `{}`})
	expect(t, r, 202, "")
	jobDone(t, e, r.body["id"].(string))
	if got := e.inst.allowed(); !slices.Equal(got, []bool{true, true, false}) {
		t.Fatalf("allowDowngrade passed to the installer: %v", got)
	}
}

// One job at a time per plugin, whatever its kind (review L9).
func TestOneJobPerPlugin(t *testing.T) {
	e := newEnv(t)
	e.stepUp("tok-roman")
	e.inst.mu.Lock()
	e.inst.blockUpdates = true
	e.inst.mu.Unlock()
	r := e.do(call{method: "POST", path: "/plugins/tv/update", tok: "tok-roman", body: `{}`})
	expect(t, r, 202, "")
	id := r.body["id"].(string)
	r = e.do(call{method: "DELETE", path: "/plugins/tv", tok: "tok-roman"})
	expect(t, r, 409, "job_running")
	if r.body["error"].(map[string]any)["jobId"] != id {
		t.Fatalf("409 = %s", r.raw)
	}
	e.inst.release <- struct{}{}
	jobDone(t, e, id)
	expect(t, e.do(call{method: "DELETE", path: "/plugins/tv", tok: "tok-roman"}), 202, "")
}

// jobDone waits until the job ends and checks that it succeeded.
func jobDone(t *testing.T, e *env, id string) {
	t.Helper()
	waitUntil(t, "job "+id, func() bool {
		return e.do(call{method: "GET", path: "/jobs/" + id, tok: "tok-roman"}).body["state"] != "running"
	})
	if r := e.do(call{method: "GET", path: "/jobs/" + id, tok: "tok-roman"}); r.body["state"] != "ok" {
		t.Fatalf("job %s: %s", id, r.raw)
	}
}

// The preview shows a manifest homepage only when it is a plain https link (review L6).
func TestPreviewHomepage(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ homepage, want string }{
		{"https://example.com/homie-tv", "https://example.com/homie-tv"},
		{" https://example.com/x ", "https://example.com/x"},
		{"http://example.com", ""},
		{"javascript:alert(document.domain)", ""},
		{"https://user:pw@example.com", ""},
		{"//example.com", ""},
		{"https:example.com", ""},
		{"", ""},
	} {
		e.inst.mu.Lock()
		e.inst.homepage = c.homepage
		e.inst.mu.Unlock()
		r := e.do(call{method: "GET", path: "/plugins/preview?repo=devXpro/homie-tv", tok: "tok-roman"})
		got, _ := r.body["homepage"].(string)
		if expect(t, r, 200, ""); got != c.want {
			t.Errorf("homepage %q → %q, want %q", c.homepage, got, c.want)
		}
	}
}

// sse reads one event stream.
type sse struct {
	resp *http.Response
	sc   *bufio.Scanner
}

type frame struct{ id, event, data string }

func (e *env) openSSE(tok, lastID, query string) *sse {
	e.t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, e.web.URL+"/api/admin/events"+query, nil)
	req.Header.Set("Cookie", sessionCookie+"="+tok)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("events: %d", resp.StatusCode)
	}
	s := &sse{resp: resp, sc: bufio.NewScanner(resp.Body)}
	e.t.Cleanup(func() { _ = resp.Body.Close() })
	return s
}

// next returns the next event frame; comments and retry lines are skipped. ok is false at the end of the stream.
func (s *sse) next() (frame, bool) {
	var f frame
	for s.sc.Scan() {
		line := s.sc.Text()
		switch {
		case line == "":
			if f.event != "" {
				return f, true
			}
		case strings.HasPrefix(line, "id: "):
			f.id = line[4:]
		case strings.HasPrefix(line, "event: "):
			f.event = line[7:]
		case strings.HasPrefix(line, "data: "):
			f.data = line[6:]
		}
	}
	return f, false
}

// until returns the first frame of the given event type.
func (s *sse) until(t *testing.T, event string) frame {
	t.Helper()
	timer := time.AfterFunc(5*time.Second, func() { _ = s.resp.Body.Close() }) // a missing event fails, not hangs
	defer timer.Stop()
	for {
		f, ok := s.next()
		if !ok {
			t.Fatalf("stream ended before %q", event)
		}
		if f.event == event {
			return f
		}
	}
}

func TestEventsResumeAndResync(t *testing.T) {
	e := newEnv(t)
	st := e.openSSE("tok-roman", "", "")
	if f, _ := st.next(); f.event != "hello" || !strings.Contains(f.data, `"version":"0.2.0"`) {
		t.Fatalf("first frame = %+v", f)
	}
	e.s.events.publish("job", map[string]string{"n": "1"})
	e.s.events.publish("job", map[string]string{"n": "2"})
	e.s.events.publish("job", map[string]string{"n": "3"})
	first := st.until(t, "job")
	st.until(t, "job")
	st.until(t, "job")

	// Resume after the first event: the two later ones are replayed, no resync.
	r := e.openSSE("tok-roman", first.id, "")
	if f, _ := r.next(); f.event != "hello" {
		t.Fatalf("resume first = %+v", f)
	}
	if f, _ := r.next(); f.event != "job" || f.data != `{"n":"2"}` {
		t.Fatalf("replay = %+v", f)
	}

	// Another hub run, or an id older than the ring: resync.
	for _, last := range []string{"zzzzzz-1", "garbage"} {
		r := e.openSSE("tok-roman", last, "")
		r.next()
		if f, _ := r.next(); f.event != "resync" {
			t.Fatalf("Last-Event-ID %q: %+v", last, f)
		}
	}
	for range 600 {
		e.s.events.publish("job", map[string]string{})
	}
	r = e.openSSE("tok-roman", first.id, "")
	r.next()
	if f, _ := r.next(); f.event != "resync" {
		t.Fatalf("fallen out of the ring: %+v", f)
	}
}

func TestEventsContent(t *testing.T) {
	e := newEnv(t)
	expect(t, e.do(call{method: "GET", path: "/events?logs=tv", tok: "tok-tanya"}), 403, "forbidden")
	owner := e.openSSE("tok-roman", "", "?logs=tv")
	member := e.openSSE("tok-tanya", "", "")
	owner.next()
	member.next()

	// Settings: owners get everything, members only the home name.
	expect(t, e.do(call{method: "PATCH", path: "/settings", tok: "tok-roman", body: `{"homeName":"Дача"}`}), 200, "")
	if f := member.until(t, "settings"); f.data != `{"homeName":"Дача"}` {
		t.Fatalf("member settings = %+v", f)
	}
	if f := owner.until(t, "settings"); !strings.Contains(f.data, `"language":"ru"`) {
		t.Fatalf("owner settings = %+v", f)
	}
	if f := owner.until(t, "audit"); !strings.Contains(f.data, `"action":"settings.update"`) {
		t.Fatalf("audit = %+v", f)
	}

	// Logs, plugin state and tools reach the owner.
	e.host.addLog("tv", "three")
	if f := owner.until(t, "plugin.log"); f.data != `{"pluginId":"tv","lines":["three"]}` {
		t.Fatalf("plugin.log = %+v", f)
	}
	e.host.setState("tv", hub.PluginFailing)
	if f := owner.until(t, "plugin"); !strings.Contains(f.data, `"state":"failing"`) {
		t.Fatalf("plugin = %+v", f)
	}
	e.host.setTools(testTools()[:2])
	if f := owner.until(t, "tools"); f.data != `{"pluginId":"tv"}` {
		t.Fatalf("tools = %+v", f)
	}

	// Revoking a session closes its stream with session.revoked.
	e.stepUp("tok-tanya")
	expect(t, e.do(call{method: "DELETE", path: "/me/sessions/s_t1", tok: "tok-tanya"}), 204, "")
	member.until(t, "session.revoked")
	if _, ok := member.next(); ok {
		t.Fatal("the member stream stays open after revocation")
	}

}

func TestEventsSessionExpiry(t *testing.T) {
	e := newEnv(t)
	st := e.openSSE("tok-roman2", "", "")
	st.next()
	// The session vanishes behind the admin's back (expiry, logout in auth): the next ping notices.
	e.st.mu.Lock()
	delete(e.st.sessions, "s_r2")
	e.st.mu.Unlock()
	st.until(t, "session.revoked")
}

func TestLogTail(t *testing.T) {
	var lt logTail
	if got := lt.next([]string{"a", "b"}); len(got) != 2 {
		t.Fatalf("first = %v", got)
	}
	if got := lt.next([]string{"a", "b", "c"}); len(got) != 1 || got[0] != "c" {
		t.Fatalf("append = %v", got)
	}
	if got := lt.next([]string{"b", "c", "d", "e"}); len(got) != 2 || got[0] != "d" {
		t.Fatalf("rotated = %v", got)
	}
	if got := lt.next([]string{"b", "c", "d", "e"}); len(got) != 0 {
		t.Fatalf("unchanged = %v", got)
	}
}

func TestVersionLess(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{{"0.2.0", "0.3.0", true}, {"0.2.10", "0.2.9", false}, {"v1.0.0", "1.0.1", true}, {"1.0.0", "1.0.0", false}, {"", "0.1.0", true}} {
		if got := versionLess(tt.a, tt.b); got != tt.want {
			t.Errorf("versionLess(%q, %q) = %v", tt.a, tt.b, got)
		}
	}
}
