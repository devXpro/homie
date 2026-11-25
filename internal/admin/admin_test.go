package admin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devXpro/mcp-gatekeeper/adminsock"
	"github.com/devXpro/mcp-gatekeeper/core"
	"github.com/devXpro/mcp-gatekeeper/tunnel/cloudflare"

	"github.com/devXpro/homie/internal/audit"
	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/store"
	"github.com/devXpro/homie/internal/tunnel"
)

const account = core.AccountID("acc_home")

type env struct {
	t     *testing.T
	st    *store.Store
	host  *fakeHost
	inst  *fakeInstaller
	tun   *fakeTunnel
	sess  *fakeSessions
	act   *audit.Writer
	s     *Server
	web   *httptest.Server
	ready bool

	mu    sync.Mutex
	names []string // OnHomeName
}

// The people of the test home: Alice is the superadmin owner, Bella a co-owner, Carol a guest who may use the TV.
var (
	alice = core.Person{ID: "per_alice", AccountID: account, Name: "Alice", Role: core.RoleOwner, Superadmin: true}
	bella = core.Person{ID: "per_bella", AccountID: account, Name: "Bella", Role: core.RoleOwner}
	carol = core.Person{ID: "per_carol", AccountID: account, Name: "Carol", Role: core.RoleMember, Permissions: []string{"plugin:tv"}}
)

func newEnv(t *testing.T, mods ...func(*Options)) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "homie.db")})
	if err != nil {
		t.Fatal(err)
	}
	acc := core.Account{ID: account, Name: "Дом", CreatedAt: time.Unix(1, 0)}
	if err := st.Accounts().Create(ctx, acc); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, host: newFakeHost(), sess: &fakeSessions{viewers: map[string]core.Viewer{}},
		tun: &fakeTunnel{st: connectedTunnel(), restarts: make(chan struct{}, 4)}, ready: true}
	for _, p := range []core.Person{alice, bella, carol} {
		p.CreatedAt = time.Unix(1, 0)
		if err := st.People().Create(ctx, p); err != nil {
			t.Fatal(err)
		}
		e.sess.add("tok-"+strings.ToLower(p.Name), core.Viewer{Person: p, Account: acc,
			Session: core.Session{IDHash: []byte("hash-" + p.Name), PersonID: p.ID}})
	}
	if err := st.Plugins().Create(ctx, store.Plugin{ID: "tv", Source: store.PluginGitHub, Repo: "devXpro/homie-tv",
		Version: "0.2.0", Command: []string{"/home/user/.local/share/homie/plugins/tv/bin/0.2.0/homie-tv", "serve"},
		Enabled: true}); err != nil {
		t.Fatal(err)
	}
	e.inst = &fakeInstaller{release: make(chan struct{}), st: st}
	e.act = audit.NewWriter(st.Activity(), nil)
	opts := Options{
		Store: st, Host: e.host, Installer: e.inst, Tunnel: e.tun, Activity: e.act,
		OwnerReady:   func(context.Context) (bool, error) { return e.ready, nil },
		OnHomeName:   func(n string) { e.mu.Lock(); e.names = append(e.names, n); e.mu.Unlock() },
		WithProgress: testWithProgress, Version: "0.2.0", LocalName: "operator",
	}
	for _, m := range mods {
		m(&opts)
	}
	e.s, err = newServer(opts, func(s *Server) {
		s.pollEvery, s.pingEvery, s.logsEvery, s.progressGap = 20*time.Millisecond, 50*time.Millisecond, 20*time.Millisecond, time.Millisecond
		s.longPoll = 2 * time.Second
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	e.s.Routes(e.sess, core.Site{PublicURL: "https://home.example.com", ClientIP: core.ClientIPCloudflare}).RegisterRoutes(mux)
	e.web = httptest.NewServer(mux)
	t.Cleanup(func() {
		e.web.Close()
		e.s.Close()
		e.act.Close()
		_ = st.Close()
	})
	return e
}

type call struct {
	method, path, tok, body string
	header                  map[string]string
}

type reply struct {
	status int
	header http.Header
	body   map[string]any
	raw    string
}

func (r reply) errCode() string {
	code, _ := r.body["error"].(string)
	return code
}

func (e *env) do(c call) reply {
	e.t.Helper()
	var rd io.Reader
	if c.body != "" {
		rd = strings.NewReader(c.body)
	}
	req, err := http.NewRequestWithContext(context.Background(), c.method, e.web.URL+basePath+c.path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if c.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.method != http.MethodGet {
		req.Header.Set("X-Homie", "1")
	}
	if c.tok != "" {
		req.Header.Set("Cookie", cookieName+"="+c.tok)
	}
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := reply{status: res.StatusCode, header: res.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

// expect checks the status and, for an error, the gatekeeper form {"error":"<code>","message":"…"}.
func expect(t *testing.T, r reply, status int, code string) {
	t.Helper()
	if r.status != status || r.errCode() != code || code != "" && r.body["message"] == "" {
		t.Fatalf("got %d %s, want %d %q", r.status, r.raw, status, code)
	}
}

// auditActions are the gatekeeper audit entries of the home, oldest first: "<action> <target> <via>".
func (e *env) auditActions() []string {
	entries, err := e.st.Audit().ListByAccount(context.Background(), account, "", 100)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, en := range slices.Backward(entries) {
		out = append(out, en.Action+" "+en.Target+" "+en.Detail["via"])
	}
	return out
}

func TestHeadersAndErrorShape(t *testing.T) {
	e := newEnv(t)
	r := e.do(call{method: "GET", path: "/nope", tok: "tok-alice"})
	expect(t, r, 404, "not_found")
	if r.header.Get("Cache-Control") != "no-store" || r.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers %v", r.header)
	}
	r = e.do(call{method: "PUT", path: "/plugins", tok: "tok-alice", body: `{}`})
	expect(t, r, 405, "method_not_allowed")
	if !strings.Contains(r.header.Get("Allow"), "GET") {
		t.Fatalf("Allow %q", r.header.Get("Allow"))
	}
	if r.raw != `{"error":"method_not_allowed","message":"PUT is not allowed here"}` {
		t.Fatalf("body %s", r.raw)
	}
}

// Every endpoint and every role: nobody gets 401, a member 403 on the owner's endpoints, the superadmin without a fresh
// passkey 403 step_up_required on the ✋ ones and with one passes the session layer, and a co-owner passes it on the
// owner's endpoints but gets 403 forbidden on the superadmin's (review R-3), even with a fresh passkey.
func TestRoles(t *testing.T) {
	e := newEnv(t)
	e.sess.stepUp("tok-bella")
	e.sess.add("tok-alice-up", e.sess.viewers["tok-alice"])
	e.sess.stepUp("tok-alice-up")
	path := func(p string) string {
		p = strings.ReplaceAll(p, "{id}", "tv")
		return strings.ReplaceAll(p, "{name}", "tv_list")
	}
	for _, rt := range e.s.table() {
		method, p, _ := strings.Cut(rt.pattern, " ")
		if p == "/events" {
			continue // a stream; TestEvents* cover it
		}
		body := ""
		if method != "GET" && method != "DELETE" {
			body = "{}"
		}
		c := call{method: method, path: path(p), body: body}
		t.Run(rt.pattern, func(t *testing.T) {
			anon := e.do(c)
			if rt.level == levelPublic {
				if anon.status != 200 {
					t.Fatalf("public: %d %s", anon.status, anon.raw)
				}
				return
			}
			expect(t, anon, 401, "no_session")
			c.tok = "tok-carol"
			member := e.do(c)
			if rt.level == levelSession {
				if member.status == 401 || member.status == 403 {
					t.Fatalf("member: %d %s", member.status, member.raw)
				}
			} else {
				expect(t, member, 403, "forbidden")
			}
			c.tok = "tok-alice"
			noStep := e.do(c)
			if rt.level == levelSuperadminStepUp {
				expect(t, noStep, 403, "step_up_required")
				c.tok = "tok-alice-up"
				if r := e.do(c); r.status == 401 || r.status == 403 {
					t.Fatalf("superadmin, stepped up: %d %s", r.status, r.raw)
				}
			} else if noStep.status == 401 || noStep.status == 403 {
				t.Fatalf("owner: %d %s", noStep.status, noStep.raw)
			}
			c.tok = "tok-bella" // the co-owner, stepped up
			coOwner := e.do(c)
			if rt.level == levelSuperadminStepUp {
				expect(t, coOwner, 403, "forbidden")
				if !strings.Contains(coOwner.body["message"].(string), "superadmin") {
					t.Fatalf("co-owner: %s", coOwner.raw)
				}
			} else if coOwner.status == 401 || coOwner.status == 403 {
				t.Fatalf("co-owner: %d %s", coOwner.status, coOwner.raw)
			}
		})
	}
}

func TestCSRFAndBodyRules(t *testing.T) {
	e := newEnv(t)
	expect(t, e.do(call{method: "PATCH", path: "/settings", tok: "tok-alice", body: `{}`, header: map[string]string{"X-Homie": "0"}}),
		403, "forbidden")
	expect(t, e.do(call{method: "PATCH", path: "/settings", tok: "tok-alice", body: `{}`,
		header: map[string]string{"Content-Type": "text/plain"}}), 415, "unsupported_media_type")
	expect(t, e.do(call{method: "PATCH", path: "/settings", tok: "tok-alice", body: `[1]`}), 400, "invalid_json")
	expect(t, e.do(call{method: "PATCH", path: "/settings", tok: "tok-alice", body: `{"homeName":5}`}), 400, "invalid_args")
	big := `{"homeName":"` + strings.Repeat("a", maxBodyBytes) + `"}`
	expect(t, e.do(call{method: "PATCH", path: "/settings", tok: "tok-alice", body: big}), 413, "too_large")
}

func TestBootstrap(t *testing.T) {
	e := newEnv(t)
	r := e.do(call{method: "GET", path: "/bootstrap"})
	expect(t, r, 200, "")
	if r.raw != `{"homeName":"Дом","language":"ru","ownerReady":true,"tunnel":{"mode":"named"}}` {
		t.Fatalf("bootstrap %s", r.raw)
	}
	e.ready = false
	e.tun.set(tunnel.Status{Mode: tunnel.ModeQuick, State: string(cloudflare.StateStarting)})
	if r := e.do(call{method: "GET", path: "/bootstrap"}); r.body["ownerReady"] != false ||
		r.body["tunnel"].(map[string]any)["mode"] != "quick" {
		t.Fatalf("bootstrap %s", r.raw)
	}
}

// Before the first setup there is no account: the name is the imported one that pre-fills the setup.
func TestHomeNameBeforeTheAccount(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.st.Accounts().Delete(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Settings().Put(ctx, store.HomeSettings{HomeName: "Дача", Language: "en"}); err != nil {
		t.Fatal(err)
	}
	if r := e.do(call{method: "GET", path: "/bootstrap"}); r.body["homeName"] != "Дача" || r.body["language"] != "en" {
		t.Fatalf("bootstrap %s", r.raw)
	}
}

func TestToolsAndPlugins(t *testing.T) {
	e := newEnv(t)
	r := e.do(call{method: "GET", path: "/tools?audience=assistant", tok: "tok-alice"})
	if expect(t, r, 200, ""); len(r.body["items"].([]any)) != 2 {
		t.Fatalf("assistant tools = %s", r.raw)
	}
	r = e.do(call{method: "GET", path: "/tools", tok: "tok-alice"})
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
	expect(t, e.do(call{method: "GET", path: "/tools?audience=x", tok: "tok-alice"}), 400, "invalid_args")

	r = e.do(call{method: "GET", path: "/plugins/tv", tok: "tok-alice"})
	expect(t, r, 200, "")
	if r.body["name"] != "homie-tv" || r.body["inventoryTool"] != "tv_list" || len(r.body["tools"].([]any)) != 3 ||
		r.body["command"] != nil {
		t.Fatalf("detail = %s", r.raw)
	}

	// Inventory: read, cached, counted.
	r = e.do(call{method: "GET", path: "/plugins/tv/inventory", tok: "tok-alice"})
	expect(t, r, 200, "")
	items := r.body["items"].([]any)
	if len(items) != 2 || items[1].(map[string]any)["state"] != "unknown" {
		t.Fatalf("inventory = %s", r.raw)
	}
	e.do(call{method: "GET", path: "/plugins/tv/inventory", tok: "tok-alice"})
	if n := e.host.callCount("tv_list"); n != 1 {
		t.Fatalf("inventory called %d times, want 1 (cache)", n)
	}
	if c := e.host.lastCall().Caller; c.Person != "Alice" || c.PersonID != "per_alice" || c.Via != "admin" {
		t.Fatalf("inventory caller = %+v", c)
	}
	r = e.do(call{method: "GET", path: "/plugins", tok: "tok-alice"})
	if p := r.body["items"].([]any)[0].(map[string]any); p["devices"] != float64(2) {
		t.Fatalf("summary = %v", p)
	}

	// Disable: state "disabled", audited in the gatekeeper's audit.
	r = e.do(call{method: "PATCH", path: "/plugins/tv", tok: "tok-alice", body: `{"enabled":false}`})
	if expect(t, r, 200, ""); r.body["state"] != "disabled" {
		t.Fatalf("patch = %s", r.raw)
	}
	expect(t, e.do(call{method: "PATCH", path: "/plugins/tv", tok: "tok-alice", body: `{}`}), 400, "invalid_args")
	if got := e.auditActions(); !slices.Contains(got, "plugin.disable tv admin") {
		t.Fatalf("audit %v", got)
	}

	r = e.do(call{method: "GET", path: "/plugins/tv/logs?tail=1", tok: "tok-alice"})
	if expect(t, r, 200, ""); len(r.body["lines"].([]any)) != 1 {
		t.Fatalf("logs = %s", r.raw)
	}
	expect(t, e.do(call{method: "GET", path: "/plugins/tv/logs?tail=0", tok: "tok-alice"}), 400, "invalid_args")
	expect(t, e.do(call{method: "GET", path: "/plugins/nope", tok: "tok-alice"}), 404, "not_found")
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
			r := e.do(call{method: "POST", path: "/tools/tv_volume/calls", tok: "tok-alice", body: `{"args":` + tt.args + `}`})
			expect(t, r, 400, "invalid_args")
			f := r.body["fields"].([]any)[0].(map[string]any)
			if f["path"] != tt.path || f["code"] != tt.code || f["message"] != tt.message {
				t.Fatalf("field = %v", f)
			}
		})
	}
	if n := e.host.callCount("tv_volume"); n != 0 {
		t.Fatalf("invalid calls reached the plugin %d times", n)
	}
	expect(t, e.do(call{method: "POST", path: "/tools/tv_nope/calls", tok: "tok-alice", body: `{}`}), 404, "unknown_tool")
	big := `{"args":{"tv":"` + strings.Repeat("a", maxArgsBytes) + `"}}`
	expect(t, e.do(call{method: "POST", path: "/tools/tv_pair/calls", tok: "tok-alice", body: big}), 413, "too_large")

	e.host.setState("tv", hub.PluginFailing)
	expect(t, e.do(call{method: "POST", path: "/tools/tv_pair/calls", tok: "tok-alice", body: `{"args":{"tv":"tv"}}`}),
		503, "plugin_unavailable")
}

func TestCallLifecycle(t *testing.T) {
	e := newEnv(t)
	gate := e.host.gate("tv_pair")
	r := e.do(call{method: "POST", path: "/tools/tv_pair/calls", tok: "tok-alice", body: `{"args":{"tv":"tv","pin":"1234"}}`})
	expect(t, r, 202, "")
	id := r.body["id"].(string)
	if r.body["state"] != "running" || r.body["args"].(map[string]any)["pin"] != "[redacted]" {
		t.Fatalf("call = %s", r.raw)
	}

	// One running call per tool.
	r = e.do(call{method: "POST", path: "/tools/tv_pair/calls", tok: "tok-alice", body: `{"args":{"tv":"tv"}}`})
	expect(t, r, 409, "call_running")
	if r.body["callId"] != id {
		t.Fatalf("409 body = %s", r.raw)
	}

	waitUntil(t, "progress", func() bool {
		r := e.do(call{method: "GET", path: "/calls/" + id, tok: "tok-alice"})
		p, _ := r.body["progress"].(map[string]any)
		return p != nil && p["message"] == "Press OK on the TV remote" && p["messageI18n"] != nil
	})
	r = e.do(call{method: "GET", path: "/calls?state=running", tok: "tok-alice"})
	if len(r.body["items"].([]any)) != 1 {
		t.Fatalf("running = %s", r.raw)
	}

	gate <- hub.Result{Text: "Living room TV is paired.", Structured: json.RawMessage(`{"paired":true}`)}
	waitUntil(t, "call end", func() bool {
		return e.do(call{method: "GET", path: "/calls/" + id, tok: "tok-alice"}).body["state"] == "ok"
	})
	r = e.do(call{method: "GET", path: "/calls/" + id, tok: "tok-alice"})
	res := r.body["result"].(map[string]any)
	if res["text"] != "Living room TV is paired." || res["structured"] == nil || r.body["durationMs"] == nil {
		t.Fatalf("finished = %s", r.raw)
	}

	// wait=1 answers the finished call; a failing tool ends "failed".
	r = e.do(call{method: "POST", path: "/tools/tv_volume/calls?wait=1", tok: "tok-bella", body: `{"args":{"tv":"tv","percent":5,"mode":"loud"}}`})
	expect(t, r, 200, "")
	if r.body["state"] != "failed" || r.body["result"].(map[string]any)["text"] != "Too loud for the neighbours." {
		t.Fatalf("wait = %s", r.raw)
	}

	// Cancel.
	e.host.gate("tv_pair")
	r = e.do(call{method: "POST", path: "/tools/tv_pair/calls", tok: "tok-alice", body: `{"args":{"tv":"tv"}}`})
	id2 := r.body["id"].(string)
	expect(t, e.do(call{method: "DELETE", path: "/calls/" + id2, tok: "tok-alice"}), 202, "")
	waitUntil(t, "cancelled", func() bool {
		return e.do(call{method: "GET", path: "/calls/" + id2, tok: "tok-alice"}).body["state"] == "cancelled"
	})

	// Every call is in the activity log with its person and surface; the PIN is not.
	waitUntil(t, "activity", func() bool {
		recs, _ := e.st.Activity().Query(context.Background(), store.ActivityFilter{})
		return len(recs) == 3
	})
	recs, _ := e.st.Activity().Query(context.Background(), store.ActivityFilter{})
	for _, rec := range recs {
		if strings.Contains(string(rec.Args), "1234") {
			t.Fatal("the pin reached the activity log")
		}
		if rec.Via != "admin" || rec.PersonID == "" {
			t.Fatalf("record %+v", rec)
		}
	}
	if recs[0].Error != "cancelled" || recs[1].PersonID != "per_bella" {
		t.Fatalf("records %+v", recs)
	}
}

// A schema failure keeps the plugin's text for the caller and a fixed text in the activity log (it may quote a
// secret).
func TestToolCallActivityText(t *testing.T) {
	e := newEnv(t)
	gate := e.host.gate("tv_volume")
	r := e.do(call{method: "POST", path: "/tools/tv_volume/calls", tok: "tok-alice", body: `{"args":{"tv":"tv","percent":5}}`})
	expect(t, r, 202, "")
	id := r.body["id"].(string)
	gate <- hub.Result{IsError: true, Text: `validating "arguments": pin: "4711" does not match`}
	waitUntil(t, "call end", func() bool {
		return e.do(call{method: "GET", path: "/calls/" + id, tok: "tok-alice"}).body["state"] == "failed"
	})
	if r := e.do(call{method: "GET", path: "/calls/" + id, tok: "tok-alice"}); !strings.Contains(r.raw, "4711") {
		t.Fatalf("the caller lost the plugin's text: %s", r.raw)
	}
	waitUntil(t, "activity", func() bool {
		recs, _ := e.st.Activity().Query(context.Background(), store.ActivityFilter{Tool: "tv_volume"})
		if len(recs) == 0 {
			return false
		}
		if strings.Contains(recs[0].Error, "4711") || !strings.HasPrefix(recs[0].Error, "invalid arguments") {
			t.Fatalf("activity error %q", recs[0].Error)
		}
		return true
	})
}

func TestJobs(t *testing.T) {
	e := newEnv(t)
	e.sess.stepUp("tok-alice")
	expect(t, e.do(call{method: "POST", path: "/plugins", tok: "tok-alice", body: `{"repo":"devXpro/homie-tv","id":"tv"}`}), 409, "conflict")
	expect(t, e.do(call{method: "POST", path: "/plugins", tok: "tok-alice", body: `{"repo":"nope"}`}), 400, "invalid_args")
	// A plugin from this machine is the home box's: homed plugin add.
	expect(t, e.do(call{method: "POST", path: "/plugins", tok: "tok-alice",
		body: `{"source":"local","id":"x1","command":["/bin/x"]}`}), 400, "invalid_args")

	r := e.do(call{method: "POST", path: "/plugins", tok: "tok-alice", body: `{"repo":"devXpro/homie-kettle","version":"0.1.0","id":"kettle"}`})
	expect(t, r, 202, "")
	id := r.body["id"].(string)
	if r.body["kind"] != "plugin.install" || r.body["step"] != "resolve" {
		t.Fatalf("job = %s", r.raw)
	}
	r = e.do(call{method: "POST", path: "/plugins", tok: "tok-alice", body: `{"repo":"devXpro/homie-kettle","id":"kettle"}`})
	expect(t, r, 409, "job_running")
	if r.body["jobId"] != id {
		t.Fatalf("409 = %s", r.raw)
	}
	waitUntil(t, "job report", func() bool {
		return e.do(call{method: "GET", path: "/jobs/" + id, tok: "tok-alice"}).body["step"] == "download"
	})
	e.inst.release <- struct{}{}
	jobDone(t, e, id)
	waitUntil(t, "install audit", func() bool { return slices.Contains(e.auditActions(), "plugin.install kettle admin") })

	r = e.do(call{method: "POST", path: "/plugins", tok: "tok-alice", body: `{"repo":"devXpro/broken"}`})
	bid := r.body["id"].(string)
	e.inst.release <- struct{}{}
	waitUntil(t, "failed job", func() bool {
		r := e.do(call{method: "GET", path: "/jobs/" + bid, tok: "tok-alice"})
		er, _ := r.body["error"].(map[string]any)
		return r.body["state"] == "failed" && er["code"] == "checksum_mismatch"
	})

	r = e.do(call{method: "GET", path: "/plugins/preview?repo=devXpro/homie-tv", tok: "tok-alice"})
	if expect(t, r, 200, ""); r.body["installed"].(map[string]any)["version"] != "0.2.0" {
		t.Fatalf("preview = %s", r.raw)
	}
	expect(t, e.do(call{method: "GET", path: "/plugins/preview?repo=a/b", tok: "tok-alice"}), 404, "not_found")
	r = e.do(call{method: "POST", path: "/plugins/check-updates", tok: "tok-alice", body: `{}`})
	if expect(t, r, 200, ""); !strings.Contains(r.raw, `"update":{"latest":"0.3.0"`) {
		t.Fatalf("check-updates = %s", r.raw)
	}
	r = e.do(call{method: "GET", path: "/jobs?state=ok", tok: "tok-alice"})
	if expect(t, r, 200, ""); len(r.body["items"].([]any)) != 1 {
		t.Fatalf("jobs = %s", r.raw)
	}
}

// jobDone waits until the job ends and checks that it succeeded.
func jobDone(t *testing.T, e *env, id string) {
	t.Helper()
	waitUntil(t, "job "+id, func() bool {
		return e.do(call{method: "GET", path: "/jobs/" + id, tok: "tok-alice"}).body["state"] != "running"
	})
	if r := e.do(call{method: "GET", path: "/jobs/" + id, tok: "tok-alice"}); r.body["state"] != "ok" {
		t.Fatalf("job %s: %s", id, r.raw)
	}
}

// The web never downgrades: an older version is refused at once, and so is the flag itself.
func TestNoDowngradeFromTheWeb(t *testing.T) {
	e := newEnv(t)
	e.sess.stepUp("tok-alice")
	for _, c := range []struct{ path, body, code string }{
		{"/plugins/tv/update", `{"version":"0.1.0"}`, "downgrade"},
		{"/plugins/tv/update", `{"version":"v0.1.9"}`, "downgrade"},
		{"/plugins/tv/update", `{"version":"0.1.0","allowDowngrade":true}`, "forbidden"},
		{"/plugins/tv/update", `{"allowDowngrade":true}`, "forbidden"},
		{"/system/update", `{"version":"0.1.0"}`, "downgrade"},
		{"/system/update", `{"allowDowngrade":true}`, "forbidden"},
	} {
		r := e.do(call{method: "POST", path: c.path, tok: "tok-alice", body: c.body})
		if r.errCode() != c.code {
			t.Errorf("%s %s: %d %s", c.path, c.body, r.status, r.raw)
		}
	}
	r := e.do(call{method: "POST", path: "/plugins/tv/update", tok: "tok-alice", body: `{}`})
	expect(t, r, 202, "")
	jobDone(t, e, r.body["id"].(string))
	if got := e.inst.allowed(); !slices.Equal(got, []bool{false}) {
		t.Fatalf("allowDowngrade passed to the installer: %v", got)
	}
}

// One job at a time per plugin, whatever its kind.
func TestOneJobPerPlugin(t *testing.T) {
	e := newEnv(t)
	e.sess.stepUp("tok-alice")
	e.inst.mu.Lock()
	e.inst.blockUpdates = true
	e.inst.mu.Unlock()
	r := e.do(call{method: "POST", path: "/plugins/tv/update", tok: "tok-alice", body: `{}`})
	expect(t, r, 202, "")
	id := r.body["id"].(string)
	r = e.do(call{method: "DELETE", path: "/plugins/tv", tok: "tok-alice"})
	expect(t, r, 409, "job_running")
	if r.body["jobId"] != id {
		t.Fatalf("409 = %s", r.raw)
	}
	e.inst.release <- struct{}{}
	jobDone(t, e, id)
	expect(t, e.do(call{method: "DELETE", path: "/plugins/tv?keepData=x", tok: "tok-alice"}), 400, "invalid_args")
	r = e.do(call{method: "DELETE", path: "/plugins/tv", tok: "tok-alice"})
	expect(t, r, 202, "")
	jobDone(t, e, r.body["id"].(string))
}

// The preview shows a manifest homepage only when it is a plain https link.
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
		r := e.do(call{method: "GET", path: "/plugins/preview?repo=devXpro/homie-tv", tok: "tok-alice"})
		got, _ := r.body["homepage"].(string)
		if expect(t, r, 200, ""); got != c.want {
			t.Errorf("homepage %q → %q, want %q", c.homepage, got, c.want)
		}
	}
}

// An owner reads everyone's activity; a member only their own, whatever filter they send.
func TestActivity(t *testing.T) {
	e := newEnv(t)
	recs := []store.ActivityRecord{
		{At: time.Unix(10, 0), PersonID: "per_carol", Person: "Carol", Via: "chatgpt", Tool: "tv_list", PluginID: "tv", OK: true},
		{At: time.Unix(11, 0), PersonID: "per_alice", Person: "Alice", Via: "claude", Tool: "tv_volume", PluginID: "tv", OK: true},
		{At: time.Unix(12, 0), PersonID: "per_carol", Person: "Carol", Via: "chatgpt", Tool: "tv_volume", PluginID: "tv", OK: false, Error: "offline"},
		{At: time.Unix(13, 0), Person: "operator", Via: "cli", Tool: "tv_pair", PluginID: "tv", OK: true},
	}
	stored, err := e.st.Activity().Append(context.Background(), recs)
	if err != nil {
		t.Fatal(err)
	}
	r := e.do(call{method: "GET", path: "/activity", tok: "tok-alice"})
	if expect(t, r, 200, ""); len(r.body["items"].([]any)) != 4 {
		t.Fatalf("owner = %s", r.raw)
	}
	first := r.body["items"].([]any)[3].(map[string]any)
	if first["toolTitle"] != "List TVs" || first["person"].(map[string]any)["id"] != "per_carol" || first["via"] != "chatgpt" {
		t.Fatalf("item = %v", first)
	}
	for _, q := range []string{"", "?person=per_alice", "?via=claude"} {
		r := e.do(call{method: "GET", path: "/activity" + q, tok: "tok-carol"})
		for _, it := range r.body["items"].([]any) {
			if it.(map[string]any)["person"].(map[string]any)["id"] != "per_carol" {
				t.Fatalf("a member saw someone else's activity with %q: %s", q, r.raw)
			}
		}
	}
	expect(t, e.do(call{method: "GET", path: "/activity/" + itoa(stored[1].ID), tok: "tok-carol"}), 404, "not_found")
	expect(t, e.do(call{method: "GET", path: "/activity/" + itoa(stored[0].ID), tok: "tok-carol"}), 200, "")
	expect(t, e.do(call{method: "GET", path: "/activity/" + itoa(stored[1].ID), tok: "tok-alice"}), 200, "")
	expect(t, e.do(call{method: "GET", path: "/activity/99", tok: "tok-alice"}), 404, "not_found")

	// Paging: the cursor is bound to its filter.
	r = e.do(call{method: "GET", path: "/activity?limit=2", tok: "tok-alice"})
	cur := r.body["nextCursor"].(string)
	r = e.do(call{method: "GET", path: "/activity?limit=2&cursor=" + cur, tok: "tok-alice"})
	if items := r.body["items"].([]any); len(items) != 2 || items[0].(map[string]any)["id"] != itoa(stored[1].ID) {
		t.Fatalf("page 2 = %s", r.raw)
	}
	r = e.do(call{method: "GET", path: "/activity?status=failed&cursor=" + cur, tok: "tok-alice"})
	if expect(t, r, 400, "invalid_args"); r.body["fields"].([]any)[0].(map[string]any)["path"] != "/cursor" {
		t.Fatalf("cursor error = %s", r.raw)
	}
	expect(t, e.do(call{method: "GET", path: "/activity?status=x", tok: "tok-alice"}), 400, "invalid_args")
	expect(t, e.do(call{method: "GET", path: "/activity?since=yesterday", tok: "tok-alice"}), 400, "invalid_args")

	// The overview's recent list is the assistants' calls.
	r = e.do(call{method: "GET", path: "/overview", tok: "tok-alice"})
	if expect(t, r, 200, ""); len(r.body["recent"].([]any)) != 3 {
		t.Fatalf("overview = %s", r.raw)
	}
}

func TestSettingsRenameTheAccount(t *testing.T) {
	e := newEnv(t)
	r := e.do(call{method: "GET", path: "/settings", tok: "tok-alice"})
	if r.raw != `{"homeName":"Дом","language":"ru"}` {
		t.Fatalf("settings = %s", r.raw)
	}
	expect(t, e.do(call{method: "PATCH", path: "/settings", tok: "tok-alice", body: `{"homeName":"a\"b"}`}), 400, "invalid_args")
	expect(t, e.do(call{method: "PATCH", path: "/settings", tok: "tok-alice", body: `{"language":"de"}`}), 400, "invalid_args")
	r = e.do(call{method: "PATCH", path: "/settings", tok: "tok-bella", body: `{"homeName":" Дача ","language":"en"}`})
	if expect(t, r, 200, ""); r.raw != `{"homeName":"Дача","language":"en"}` {
		t.Fatalf("patch = %s", r.raw)
	}
	acc, _ := e.st.Accounts().Get(context.Background(), account)
	if acc.Name != "Дача" {
		t.Fatalf("account = %+v", acc)
	}
	e.mu.Lock()
	names := slices.Clone(e.names)
	e.mu.Unlock()
	if !slices.Equal(names, []string{"Дача"}) {
		t.Fatalf("OnHomeName = %v", names)
	}
	if got := e.auditActions(); !slices.Contains(got, "settings.update  admin") {
		t.Fatalf("audit = %v", got)
	}
	// Nothing changed: no audit, no event.
	before := len(e.auditActions())
	expect(t, e.do(call{method: "PATCH", path: "/settings", tok: "tok-alice", body: `{"homeName":"Дача"}`}), 200, "")
	if len(e.auditActions()) != before {
		t.Fatal("an unchanged name was audited")
	}
}

func TestTunnel(t *testing.T) {
	e := newEnv(t)
	r := e.do(call{method: "GET", path: "/tunnel", tok: "tok-alice"})
	if r.raw != `{"mode":"named","state":"connected","url":"https://home.example.com","mcpUrl":"https://home.example.com/mcp",`+
		`"hostname":"home.example.com","since":"1970-01-01T00:00:01.000Z"}` {
		t.Fatalf("tunnel = %s", r.raw)
	}
	expect(t, e.do(call{method: "POST", path: "/tunnel/check", tok: "tok-alice", body: `{}`}), 200, "")
	if e.tun.checks != 1 {
		t.Fatal("check did not probe")
	}
	e.sess.stepUp("tok-alice")
	r = e.do(call{method: "POST", path: "/tunnel/reconnect", tok: "tok-alice", body: `{}`})
	if expect(t, r, 202, ""); r.body["state"] != "starting" {
		t.Fatalf("reconnect = %s", r.raw)
	}
	select {
	case <-e.tun.restarts:
	case <-time.After(5 * time.Second):
		t.Fatal("the tunnel did not restart after the answer")
	}
	waitUntil(t, "audit", func() bool { return slices.Contains(e.auditActions(), "tunnel.reconnect home.example.com admin") })
	e.tun.set(tunnel.Status{Mode: tunnel.ModeOff, State: string(cloudflare.StateStopped)})
	expect(t, e.do(call{method: "POST", path: "/tunnel/reconnect", tok: "tok-alice", body: `{}`}), 409, "conflict")

	noTunnel := newEnv(t, func(o *Options) { o.Tunnel = nil })
	expect(t, noTunnel.do(call{method: "GET", path: "/tunnel", tok: "tok-alice"}), 503, "home_not_ready")
}

func TestOverviewAttention(t *testing.T) {
	e := newEnv(t)
	kinds := func() []string {
		r := e.do(call{method: "GET", path: "/overview", tok: "tok-alice"})
		expect(t, r, 200, "")
		var out []string
		for _, a := range r.body["attention"].([]any) {
			out = append(out, a.(map[string]any)["kind"].(string))
		}
		return out
	}
	if got := kinds(); len(got) != 0 {
		t.Fatalf("attention = %v", got)
	}
	e.s.AccessReset(context.Background(), time.Now(), "/usr/bin/x (pid 7)")
	e.tun.set(tunnel.Status{Mode: tunnel.ModeQuick, State: string(cloudflare.StateFailing), LastError: "exit status 1"})
	e.host.setState("tv", hub.PluginFailing)
	if got := kinds(); !slices.Equal(got, []string{"access_reset", "tunnel_offline", "plugin_failing", "tunnel_quick"}) {
		t.Fatalf("attention = %v", got)
	}
	e.tun.set(tunnel.Status{Mode: tunnel.ModeNamed, State: string(cloudflare.StateProbing), LastError: "timeout"})
	if got := kinds(); !slices.Contains(got, "tunnel_unreachable") {
		t.Fatalf("attention = %v", got)
	}
	r := e.do(call{method: "GET", path: "/overview", tok: "tok-alice"})
	if a := r.body["attention"].([]any)[0].(map[string]any); a["peer"] != "/usr/bin/x (pid 7)" || a["at"] == nil {
		t.Fatalf("access reset = %v", a)
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
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, e.web.URL+basePath+"/events"+query, nil)
	req.Header.Set("Cookie", cookieName+"="+tok)
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
	st := e.openSSE("tok-alice", "", "")
	if f, _ := st.next(); f.event != "hello" || !strings.Contains(f.data, `"version":"0.2.0"`) {
		t.Fatalf("first frame = %+v", f)
	}
	e.s.events.publish("job", map[string]string{"n": "1"})
	e.s.events.publish("job", map[string]string{"n": "2"})
	e.s.events.publish("job", map[string]string{"n": "3"})
	first := st.until(t, "job")
	st.until(t, "job")
	st.until(t, "job")

	r := e.openSSE("tok-alice", first.id, "")
	if f, _ := r.next(); f.event != "hello" {
		t.Fatalf("resume first = %+v", f)
	}
	if f, _ := r.next(); f.event != "job" || f.data != `{"n":"2"}` {
		t.Fatalf("replay = %+v", f)
	}
	for _, last := range []string{"zzzzzz-1", "garbage"} {
		r := e.openSSE("tok-alice", last, "")
		r.next()
		if f, _ := r.next(); f.event != "resync" {
			t.Fatalf("Last-Event-ID %q: %+v", last, f)
		}
	}
	for range 600 {
		e.s.events.publish("job", map[string]string{})
	}
	r = e.openSSE("tok-alice", first.id, "")
	r.next()
	if f, _ := r.next(); f.event != "resync" {
		t.Fatalf("fallen out of the ring: %+v", f)
	}
}

func TestEventsContent(t *testing.T) {
	e := newEnv(t)
	expect(t, e.do(call{method: "GET", path: "/events?logs=tv", tok: "tok-carol"}), 403, "forbidden")
	owner := e.openSSE("tok-alice", "", "?logs=tv")
	member := e.openSSE("tok-carol", "", "")
	owner.next()
	member.next()

	// Settings: owners get everything, members only the home name.
	expect(t, e.do(call{method: "PATCH", path: "/settings", tok: "tok-alice", body: `{"homeName":"Дача"}`}), 200, "")
	if f := member.until(t, "settings"); f.data != `{"homeName":"Дача"}` {
		t.Fatalf("member settings = %+v", f)
	}
	if f := owner.until(t, "settings"); f.data != `{"homeName":"Дача","language":"ru"}` {
		t.Fatalf("owner settings = %+v", f)
	}

	// Activity: owners get every call, a member their own only.
	e.act.Record(context.Background(), hub.AuditEntry{Caller: hub.Caller{Person: "Alice", PersonID: "per_alice", Via: "claude"}, Tool: "tv_list", OK: true})
	e.act.Record(context.Background(), hub.AuditEntry{Caller: hub.Caller{Person: "Carol", PersonID: "per_carol", Via: "chatgpt"}, Tool: "tv_volume", OK: true})
	if f := member.until(t, "activity"); !strings.Contains(f.data, `"per_carol"`) {
		t.Fatalf("member activity = %+v", f)
	}
	if f := owner.until(t, "activity"); !strings.Contains(f.data, `"per_alice"`) {
		t.Fatalf("owner activity = %+v", f)
	}

	// Logs, plugin state, tools and the tunnel reach the owner.
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
	e.tun.set(tunnel.Status{Mode: tunnel.ModeNamed, State: string(cloudflare.StateFailing), URL: "https://home.example.com"})
	if f := owner.until(t, "tunnel"); !strings.Contains(f.data, `"state":"failing"`) {
		t.Fatalf("tunnel = %+v", f)
	}

	// The member's session ends (signed out elsewhere, removed, term over): the next ping closes the stream.
	e.sess.drop("tok-carol")
	member.until(t, "session.revoked")
	if _, ok := member.next(); ok {
		t.Fatal("the member stream stays open")
	}
}

func TestPollEvents(t *testing.T) {
	e := newEnv(t)
	poll := func(tok, after string) (events []map[string]any, cursor string) {
		t.Helper()
		r := e.do(call{method: "GET", path: "/events/poll?after=" + after, tok: tok})
		expect(t, r, 200, "")
		for _, ev := range r.body["events"].([]any) {
			events = append(events, ev.(map[string]any))
		}
		return events, r.body["cursor"].(string)
	}
	evs, cur := poll("tok-alice", "")
	if len(evs) != 1 || evs[0]["event"] != "hello" || cur == "" {
		t.Fatalf("first poll = %v %q", evs, cur)
	}
	_, mcur := poll("tok-carol", "")

	// An event published while the poll waits comes back at once.
	go func() {
		time.Sleep(50 * time.Millisecond) // let the poll start waiting
		e.s.events.publish("job", map[string]string{"n": "1"})
	}()
	evs, cur2 := poll("tok-alice", cur)
	if len(evs) != 1 || evs[0]["event"] != "job" || evs[0]["id"] != cur2 || cur2 == cur {
		t.Fatalf("waited poll = %v %q", evs, cur2)
	}
	// A member does not get owner events: its poll times out empty with the same cursor.
	evs, same := poll("tok-carol", mcur)
	if len(evs) != 0 || same != mcur {
		t.Fatalf("member poll = %v %q", evs, same)
	}
	// Events already in the ring are answered at once, oldest first.
	e.s.events.publish("job", map[string]string{"n": "2"})
	e.s.events.publish("job", map[string]string{"n": "3"})
	evs, _ = poll("tok-alice", cur2)
	if len(evs) != 2 || evs[1]["data"].(map[string]any)["n"] != "3" {
		t.Fatalf("replay = %v", evs)
	}
	if evs, _ := poll("tok-alice", "zzzz-1"); len(evs) != 1 || evs[0]["event"] != "resync" {
		t.Fatalf("resync = %v", evs)
	}
	expect(t, e.do(call{method: "GET", path: "/events/poll"}), 401, "no_session")
}

func TestAccessReset(t *testing.T) {
	e := newEnv(t)
	owner := e.openSSE("tok-alice", "", "")
	owner.next()
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	e.s.AccessReset(context.Background(), at, "/usr/bin/x (pid 7)")
	if f := owner.until(t, "attention"); f.data != `{"kind":"access_reset","at":"2026-11-15T12:00:00.000Z","peer":"/usr/bin/x (pid 7)"}` {
		t.Fatalf("attention = %+v", f)
	}
	hs, _ := e.st.Settings().Get(context.Background())
	if !hs.AccessResetAt.Equal(at) || hs.AccessResetBy != "/usr/bin/x (pid 7)" {
		t.Fatalf("settings = %+v", hs)
	}
}

// commands runs an admin socket command in-process.
func (e *env) command(name string, args []string, stdin string) (string, error) {
	e.t.Helper()
	for _, c := range e.s.Commands("/home/user/.local/share/homie") {
		if c.Name == name {
			var out bytes.Buffer
			inv := adminsock.Invocation{Args: args, Out: &out}
			if c.Stdin {
				inv.Stdin = []byte(stdin)
			}
			err := c.Run(context.Background(), inv)
			return out.String(), err
		}
	}
	e.t.Fatalf("no command %q", name)
	return "", nil
}

func TestCommands(t *testing.T) {
	e := newEnv(t)
	out, err := e.command("plugin add", []string{"--id", "lamp", "--", "/home/user/bin/lamp", "serve"}, "")
	if err != nil || !strings.Contains(out, "Added plugin lamp") {
		t.Fatalf("plugin add = %v\n%s", err, out)
	}
	if _, err := e.command("plugin add", []string{"--id", "lamp", "--", "lamp"}, ""); err == nil {
		t.Fatal("a relative command was accepted")
	}
	if _, err := e.command("plugin add", []string{"--id", "lamp", "--", "/x"}, ""); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("duplicate = %v", err)
	}
	out, err = e.command("plugin list", nil, "")
	if err != nil || !strings.Contains(out, "lamp") || !strings.Contains(out, "github devXpro/homie-tv") {
		t.Fatalf("plugin list = %v\n%s", err, out)
	}
	out, err = e.command("tool list", nil, "")
	if err != nil || !strings.Contains(out, "tv_pair") || !strings.Contains(out, "admin") {
		t.Fatalf("tool list = %v\n%s", err, out)
	}
	out, err = e.command("tool call", []string{"tv_volume", "-"}, `{"tv":"tv","percent":5}`)
	if err != nil || !strings.Contains(out, "… Press OK on the TV remote (1/2)") || !strings.Contains(out, "Done.") {
		t.Fatalf("tool call = %v\n%s", err, out)
	}
	if c := e.host.lastCall().Caller; c.Person != "operator" || c.Via != "cli" || c.PersonID != "" {
		t.Fatalf("caller = %+v", c)
	}
	if _, err := e.command("tool call", []string{"tv_volume", `{"tv":"tv","percent":5,"mode":"loud"}`}, ""); !errors.Is(err, errToolFailed) {
		t.Fatalf("a failing tool = %v", err)
	}
	if _, err := e.command("tool call", []string{"tv_nope"}, ""); err == nil || !strings.Contains(err.Error(), "homed tool list") {
		t.Fatalf("unknown tool = %v", err)
	}
	if _, err := e.command("tool call", []string{"tv_volume", "[1]"}, ""); err == nil {
		t.Fatal("non-object arguments accepted")
	}
	out, err = e.command("plugin remove", []string{"--delete-data", "lamp"}, "")
	if err != nil || !strings.Contains(out, "Removed plugin lamp and its data.") {
		t.Fatalf("plugin remove = %v\n%s", err, out)
	}
	// The socket may downgrade, with the flag only.
	if _, err := e.command("update", []string{"--version", "0.1.0"}, ""); err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("update without the flag = %v", err)
	}
	out, err = e.command("update", []string{"--version", "0.1.0", "--allow-downgrade"}, "")
	if err != nil || !strings.Contains(out, "homed restarts") {
		t.Fatalf("update = %v\n%s", err, out)
	}
	if got := e.inst.allowed(); !slices.Equal(got, []bool{true}) {
		t.Fatalf("allowDowngrade = %v", got)
	}
	waitUntil(t, "audit", func() bool {
		got := e.auditActions()
		return slices.Contains(got, "plugin.install lamp cli") && slices.Contains(got, "plugin.remove lamp cli") &&
			slices.Contains(got, "hub.update 0.3.0 cli")
	})
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

// Removing a plugin takes its permission from every person and open invite (review R-12): a person keeps their other
// permissions, an open invite that gives it is revoked, a used or another plugin's invite stays; all audited.
func TestRemovingAPluginDropsItsPermission(t *testing.T) {
	e := newEnv(t)
	e.sess.stepUp("tok-alice")
	ctx := context.Background()
	now := time.Now()
	dan := core.Person{ID: "per_dan", AccountID: account, Name: "Dan", Role: core.RoleMember, CreatedAt: now,
		Permissions: []string{"plugin:kettle", "plugin:tv"}}
	if err := e.st.People().Create(ctx, dan); err != nil {
		t.Fatal(err)
	}
	used := now.Add(-time.Minute)
	for _, inv := range []core.Invite{
		{ID: "inv_tv", Permissions: []string{"plugin:tv"}},
		{ID: "inv_kettle", Permissions: []string{"plugin:kettle"}},
		{ID: "inv_used", Permissions: []string{"plugin:tv"}, UsedAt: &used},
	} {
		inv.TokenHash, inv.Kind, inv.AccountID, inv.Name = []byte("hash-"+inv.ID), core.InviteMember, account, inv.ID
		inv.CreatedAt, inv.ExpiresAt, inv.CreatedBy = now, now.Add(24*time.Hour), alice.ID
		if err := e.st.Invites().Create(ctx, inv); err != nil {
			t.Fatal(err)
		}
	}
	r := e.do(call{method: "DELETE", path: "/plugins/tv", tok: "tok-alice"})
	expect(t, r, 202, "")
	jobDone(t, e, r.body["id"].(string))
	waitUntil(t, "the invite revoked", func() bool {
		return slices.Contains(e.auditActions(), "account.invite.revoke inv_tv admin")
	})
	for id, want := range map[core.PersonID][]string{carol.ID: {}, dan.ID: {"plugin:kettle"}} {
		p, err := e.st.People().Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Permissions) != len(want) || len(want) > 0 && !slices.Equal(p.Permissions, want) {
			t.Errorf("%s keeps %v, want %v", id, p.Permissions, want)
		}
	}
	// The store lists the open invites: the kettle's stays, the TV's is revoked (the used one was never open).
	invites, err := e.st.Invites().ListByAccount(ctx, account)
	if err != nil || len(invites) != 1 || invites[0].ID != "inv_kettle" {
		t.Errorf("open invites = %+v, %v", invites, err)
	}
	got := e.auditActions()
	if slices.Contains(got, "account.invite.revoke inv_used admin") {
		t.Errorf("a used invite was revoked: %v", got)
	}
	for _, want := range []string{"account.member.update per_carol admin", "account.member.update per_dan admin"} {
		if !slices.Contains(got, want) {
			t.Errorf("audit %v lacks %q", got, want)
		}
	}
	entries, _ := e.st.Audit().ListByAccount(ctx, account, "", 100)
	for _, en := range entries {
		if en.Target == string(dan.ID) && (en.Detail["permissions"] != "plugin:kettle" || en.Detail["reason"] != "plugin.remove" ||
			en.Actor != alice.ID) {
			t.Errorf("dan's audit = %+v", en)
		}
	}
}
