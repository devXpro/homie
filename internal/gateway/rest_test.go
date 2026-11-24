package gateway

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devXpro/homie/internal/hub"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func TestHealthzAndInstanceHeader(t *testing.T) {
	e := newEnv(t, Options{})
	tests := []struct {
		method, path, key string
		status            int
	}{
		{"GET", "/healthz", "", 200},
		{"GET", "/api/v1/tools", "", 401},
		{"GET", "/api/v1/tools", e.roman, 200},
		{"POST", "/api/v1/tools/nope", e.roman, 404},
		{"GET", "/api/v2/whatever", "", 404},
		{"POST", "/mcp", "", 401},
	}
	for _, tt := range tests {
		status, h, body := e.do(t, tt.method, tt.path, tt.key, "")
		if status != tt.status {
			t.Errorf("%s %s: status %d, want %d (%s)", tt.method, tt.path, status, tt.status, body)
		}
		if h.Get(instanceHeader) != "inst-123" {
			t.Errorf("%s %s: instance header %q", tt.method, tt.path, h.Get(instanceHeader))
		}
		if !strings.HasPrefix(h.Get("Content-Type"), "application/json") {
			t.Errorf("%s %s: content type %q", tt.method, tt.path, h.Get("Content-Type"))
		}
	}
	if _, _, body := e.do(t, "GET", "/healthz", "", ""); body != `{"ok":true}` {
		t.Fatalf("healthz body %s", body)
	}
}

func TestAuth(t *testing.T) {
	e := newEnv(t, Options{})
	tests := []struct {
		name, header string
		status       int
	}{
		{"missing", "", 401},
		{"not bearer", "Basic " + e.roman, 401},
		{"bearer without key", "Bearer", 401},
		{"extra field", "Bearer " + e.roman + " x", 401},
		{"wrong key", "Bearer ghk_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", 401},
		{"right key", "Bearer " + e.roman, 200},
		{"scheme case-insensitive", "bearer " + e.roman, 200},
		{"second key of the same person", "Bearer " + e.romanB, 200},
	}
	for _, path := range []string{"/api/v1/tools", "/api/v1/tools/tv_power_on"} {
		for _, tt := range tests {
			t.Run(path+"/"+tt.name, func(t *testing.T) {
				method := http.MethodGet
				if path != "/api/v1/tools" {
					method = http.MethodPost
				}
				req, _ := http.NewRequestWithContext(t.Context(), method, e.srv.URL+path, strings.NewReader("{}"))
				if tt.header != "" {
					req.Header.Set("Authorization", tt.header)
				}
				resp, err := e.srv.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				var body bytes.Buffer
				_, _ = body.ReadFrom(resp.Body)
				if resp.StatusCode != tt.status {
					t.Fatalf("status %d, want %d", resp.StatusCode, tt.status)
				}
				if tt.status == 401 {
					if body.String() != `{"error":"unauthorized"}` {
						t.Fatalf("body %s", body.String())
					}
					if resp.Header.Get("WWW-Authenticate") != "Bearer" {
						t.Fatal("no WWW-Authenticate header")
					}
				}
			})
		}
	}
	for _, c := range e.host.callLog() {
		if c.Caller.Person != "Roman" {
			t.Fatalf("a call went through without the right key: %+v", c)
		}
	}
}

// siteCheck asserts the manifest is exactly what librus-gpt/site/core/mcp.mjs reads: an object with "instructions"
// (string) and "tools" (array); each tool only has keys from TOOL_KEYS, a non-empty name and an object inputSchema;
// no value anywhere is null.
func siteCheck(t *testing.T, body string) []string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("manifest is not a JSON object: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("manifest keys: %v", m)
	}
	if _, ok := m["instructions"].(string); !ok {
		t.Fatal("instructions is not a string")
	}
	tools, ok := m["tools"].([]any)
	if !ok {
		t.Fatalf("tools is not an array: %v", m["tools"])
	}
	toolKeys := []string{"name", "title", "description", "inputSchema", "outputSchema", "annotations"}
	var names []string
	for _, raw := range tools {
		tool := raw.(map[string]any)
		for k, v := range tool {
			if !slices.Contains(toolKeys, k) {
				t.Errorf("tool key %q is not one the Site reads", k)
			}
			if v == nil {
				t.Errorf("tool key %q is null", k)
			}
		}
		name, _ := tool["name"].(string)
		if name == "" {
			t.Errorf("tool without a name: %v", tool)
		}
		if schema, ok := tool["inputSchema"].(map[string]any); !ok || schema["type"] != "object" {
			t.Errorf("%s: inputSchema %v", name, tool["inputSchema"])
		}
		names = append(names, name)
	}
	if strings.Contains(body, "null") {
		t.Errorf("manifest contains null: %s", body)
	}
	return names
}

func TestManifestGolden(t *testing.T) {
	e := newEnv(t, Options{Instructions: func() string { return "TV ids come from tv_status." }})
	status, _, body := e.do(t, "GET", "/api/v1/tools", e.roman, "")
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	siteCheck(t, body)

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(body), "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	const golden = "testdata/tools.golden.json"
	if *update {
		if err := os.WriteFile(golden, pretty.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if pretty.String() != string(want) {
		t.Fatalf("manifest differs from %s (run with -update after checking):\n%s", golden, pretty.String())
	}
}

func TestScopes(t *testing.T) {
	e := newEnv(t, Options{})
	lists := []struct {
		name string
		key  string
		want []string
	}{
		{"all plugins", e.roman, []string{"kettle_boil", "tv_power_on", "tv_status"}},
		{"tv only", e.tanya, []string{"tv_power_on", "tv_status"}},
		{"no plugins", e.guest, nil},
	}
	for _, tt := range lists {
		t.Run("list/"+tt.name, func(t *testing.T) {
			_, _, body := e.do(t, "GET", "/api/v1/tools", tt.key, "")
			if got := siteCheck(t, body); !slices.Equal(got, tt.want) {
				t.Fatalf("tools %v, want %v", got, tt.want)
			}
		})
	}

	calls := []struct {
		name, key, tool string
		status          int
	}{
		{"allowed", e.tanya, "tv_power_on", 200},
		{"other plugin", e.tanya, "kettle_boil", 404},
		{"admin tool", e.roman, "tv_pair", 404},
		{"skipped tool", e.roman, "tv_broken", 404},
		{"no plugins", e.guest, "tv_power_on", 404},
		{"all plugins", e.roman, "kettle_boil", 200},
	}
	for _, tt := range calls {
		t.Run("call/"+tt.name, func(t *testing.T) {
			before := len(e.host.callLog())
			status, _, body := e.do(t, "POST", "/api/v1/tools/"+tt.tool, tt.key, "{}")
			if status != tt.status {
				t.Fatalf("status %d, want %d (%s)", status, tt.status, body)
			}
			reached := len(e.host.callLog()) > before
			if reached != (tt.status == 200) {
				t.Fatalf("host reached = %v", reached)
			}
			if status == 404 && body != `{"error":"unknown tool"}` {
				t.Fatalf("404 body %s", body)
			}
		})
	}
}

func TestCallStatusTable(t *testing.T) {
	e := newEnv(t, Options{})
	tests := []struct {
		name, tool, body string
		status           int
		want             string
	}{
		{"text result", "tv_power_on", `{"tv":"living"}`, 200, `{"text":"Living room TV is on."}`},
		{"empty body is no arguments", "tv_power_on", ``, 200, `{"text":"Living room TV is on."}`},
		{"structured result", "tv_status", `{}`, 200, `{"on":true,"input":"HDMI 1"}`},
		{"tool failed", "tv_power_on", `{"mode":"fail"}`, 422, `{"error":"Living room TV is offline. Turn it on with the remote once."}`},
		{"invalid JSON", "tv_power_on", `{"tv":`, 400, `{"error":"invalid JSON"}`},
		{"array body", "tv_power_on", `["tv"]`, 400, `{"error":"the body must be a JSON object of arguments"}`},
		{"null body", "tv_power_on", `null`, 400, `{"error":"the body must be a JSON object of arguments"}`},
		{"unknown tool", "tv_nope", `{}`, 404, `{"error":"unknown tool"}`},
		{"tool vanished in the host", "tv_power_on", `{"mode":"gone"}`, 404, `{"error":"unknown tool"}`},
		{"plugin down", "tv_power_on", `{"mode":"down"}`, 503, `{"error":"plugin unavailable"}`},
		{"host timeout", "tv_power_on", `{"mode":"timeout"}`, 504, `{"error":"timeout"}`},
		{"other error", "tv_power_on", `{"mode":"boom"}`, 500, `{"error":"internal error"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, h, body := e.do(t, "POST", "/api/v1/tools/"+tt.tool, e.roman, tt.body)
			if status != tt.status || body != tt.want {
				t.Fatalf("got %d %s, want %d %s", status, body, tt.status, tt.want)
			}
			if !strings.HasPrefix(h.Get("Content-Type"), "application/json") {
				t.Fatalf("content type %q", h.Get("Content-Type"))
			}
		})
	}

	t.Run("401", func(t *testing.T) {
		if status, _, body := e.do(t, "POST", "/api/v1/tools/tv_power_on", "", "{}"); status != 401 || body != `{"error":"unauthorized"}` {
			t.Fatalf("got %d %s", status, body)
		}
	})

	t.Run("429", func(t *testing.T) {
		e := newEnv(t, Options{RateLimit: 1})
		e.gw.limiter.now = func() time.Time { return time.Unix(0, 0) }
		e.do(t, "POST", "/api/v1/tools/tv_power_on", e.roman, "{}")
		if status, _, body := e.do(t, "POST", "/api/v1/tools/tv_power_on", e.roman, "{}"); status != 429 || body != `{"error":"rate limited"}` {
			t.Fatalf("got %d %s", status, body)
		}
	})

	t.Run("504 from the tool's own timeout", func(t *testing.T) {
		tools := baseTools()
		tools[0].Timeout = 20 * time.Millisecond
		e.host.setTools(tools)
		if status, _, body := e.do(t, "POST", "/api/v1/tools/tv_power_on", e.roman, `{"mode":"hang"}`); status != 504 || body != `{"error":"timeout"}` {
			t.Fatalf("got %d %s", status, body)
		}
	})

	t.Run("body too large", func(t *testing.T) {
		big := `{"x":"` + strings.Repeat("a", maxBodyBytes) + `"}`
		if status, _, _ := e.do(t, "POST", "/api/v1/tools/tv_power_on", e.roman, big); status != 400 {
			t.Fatalf("status %d", status)
		}
	})

	t.Run("wrong method is a JSON 404, never the relay's HTML one", func(t *testing.T) {
		if status, _, body := e.do(t, "GET", "/api/v1/tools/tv_power_on", e.roman, ""); status != 404 || body != `{"error":"not found"}` {
			t.Fatalf("got %d %s", status, body)
		}
	})
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t, Options{RateLimit: 3})
	clk := newClock(time.Unix(1_000_000, 0))
	e.gw.limiter.now = clk.now
	call := func(key string) int {
		status, _, _ := e.do(t, "POST", "/api/v1/tools/tv_power_on", key, "{}")
		return status
	}
	for i := range 3 {
		if s := call(e.roman); s != 200 {
			t.Fatalf("call %d: %d", i, s)
		}
	}
	if s := call(e.roman); s != 429 {
		t.Fatalf("4th call: %d", s)
	}
	if s := call(e.romanB); s != 200 {
		t.Fatalf("another key of the same person shares the bucket: %d", s)
	}
	if s := call(e.tanya); s != 200 {
		t.Fatalf("another person is limited: %d", s)
	}
	if status, _, _ := e.do(t, "GET", "/api/v1/tools", e.roman, ""); status != 200 {
		t.Fatalf("listing is limited: %d", status)
	}
	clk.add(20 * time.Second) // 3 per minute: one token back
	if s := call(e.roman); s != 200 {
		t.Fatalf("after refill: %d", s)
	}
	if s := call(e.roman); s != 429 {
		t.Fatalf("second call after one refill: %d", s)
	}
	clk.add(time.Hour) // the bucket caps at the burst
	for i := range 3 {
		if s := call(e.roman); s != 200 {
			t.Fatalf("after an hour, call %d: %d", i, s)
		}
	}
	if s := call(e.roman); s != 429 {
		t.Fatalf("burst exceeded the limit: %d", s)
	}
	if n := len(e.host.callLog()); n != 9 {
		t.Fatalf("host saw %d calls, want 9", n)
	}
}

func TestAudit(t *testing.T) {
	e := newEnv(t, Options{RateLimit: 3})
	e.gw.limiter.now = func() time.Time { return time.Unix(0, 0) }
	e.do(t, "POST", "/api/v1/tools/tv_power_on", e.roman, `{"tv":"living","pin":"123456","auth":{"clientKey":"abc"}}`)
	e.do(t, "POST", "/api/v1/tools/tv_power_on", e.tanya, `{"mode":"fail"}`)
	e.do(t, "POST", "/api/v1/tools/kettle_boil", e.tanya, `{}`)
	e.do(t, "POST", "/api/v1/tools/tv_power_on", e.roman, `{"mode":"down"}`)
	e.do(t, "POST", "/api/v1/tools/tv_power_on", e.roman, `{}`)
	e.do(t, "POST", "/api/v1/tools/tv_power_on", e.roman, `{}`) // rate limited
	e.do(t, "POST", "/api/v1/tools/tv_power_on", "", `{}`)      // unauthenticated: not audited

	got := e.audit.all()
	want := []struct {
		person, tool, args, err string
		ok                      bool
	}{
		{"Roman", "tv_power_on", `{"auth":{"clientKey":"[redacted]"},"pin":"[redacted]","tv":"living"}`, "", true},
		{"Tanya", "tv_power_on", `{"mode":"fail"}`, "Living room TV is offline. Turn it on with the remote once.", false},
		{"Tanya", "kettle_boil", ``, "unknown tool", false},
		{"Roman", "tv_power_on", `{"mode":"down"}`, "plugin is not running", false},
		{"Roman", "tv_power_on", `{}`, "", true},
		{"Roman", "tv_power_on", ``, "rate limited", false},
	}
	if len(got) != len(want) {
		t.Fatalf("%d audit entries, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Caller.Person != w.person || g.Caller.Via != "site" || g.Tool != w.tool || string(g.Args) != w.args ||
			g.Error != w.err || g.OK != w.ok || g.At.IsZero() || g.Duration < 0 {
			t.Errorf("entry %d = %+v (args %s), want %+v", i, g, g.Args, w)
		}
	}
	// The plugin still gets the real arguments; only the audit copy is redacted.
	if c := e.host.callLog()[0]; !strings.Contains(c.Args, "123456") || c.Caller != (hub.Caller{Person: "Roman", Via: "site"}) {
		t.Fatalf("host call %+v", c)
	}
}

func TestInstructions(t *testing.T) {
	var plugin atomic.Pointer[string]
	first := "TV ids come from tv_status."
	plugin.Store(&first)
	e := newEnv(t, Options{Instructions: func() string { return *plugin.Load() }})
	_, _, body := e.do(t, "GET", "/api/v1/tools", e.roman, "")
	var m struct{ Instructions string }
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`"Мой шалаш"`, "only when the user asks", "one short sentence", "offline", first} {
		if !strings.Contains(m.Instructions, part) {
			t.Errorf("instructions lack %q: %s", part, m.Instructions)
		}
	}
	// Plugin instructions are read again when the tool set changes.
	second := "Kettle instructions."
	plugin.Store(&second)
	e.host.setTools(baseTools())
	_, _, body = e.do(t, "GET", "/api/v1/tools", e.roman, "")
	if !strings.Contains(body, second) {
		t.Fatalf("instructions not refreshed: %s", body)
	}
}
