package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/devXpro/homie/internal/hub"
)

type bearerTransport struct {
	key  string
	base http.RoundTripper
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return b.base.RoundTrip(r)
}

func (e *env) connect(t *testing.T, key string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:             e.srv.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: bearerTransport{key: key, base: e.srv.Client().Transport}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}
	cs, err := client.Connect(t.Context(), transport, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func text(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestMCPAuth(t *testing.T) {
	e := newEnv(t, Options{})
	if _, err := e.connect(t, "ghk_wrong"); err == nil {
		t.Fatal("connected with a wrong key")
	}
	if _, err := e.connect(t, e.roman); err != nil {
		t.Fatalf("right key: %v", err)
	}
}

func TestMCPListAndCall(t *testing.T) {
	e := newEnv(t, Options{Instructions: func() string { return "TV ids come from tv_status." }})
	cs, err := e.connect(t, e.roman)
	if err != nil {
		t.Fatal(err)
	}
	init := cs.InitializeResult()
	if init.ServerInfo.Name != "homie" || init.ServerInfo.Title != "Мой шалаш" {
		t.Fatalf("serverInfo %+v", init.ServerInfo)
	}
	if !strings.HasPrefix(init.ServerInfo.Version, "0.1.0+") || len(init.ServerInfo.Version) != len("0.1.0+")+8 {
		t.Fatalf("version %q", init.ServerInfo.Version)
	}
	if !strings.Contains(init.Instructions, `"Мой шалаш"`) || !strings.Contains(init.Instructions, "TV ids come from tv_status.") {
		t.Fatalf("instructions %q", init.Instructions)
	}

	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 3 {
		t.Fatalf("tools %d", len(res.Tools))
	}
	on := res.Tools[slices.IndexFunc(res.Tools, func(t *mcp.Tool) bool { return t.Name == "tv_power_on" })]
	if on.Title != "Turn the TV on" || on.Annotations == nil || !on.Annotations.IdempotentHint || on.Annotations.ReadOnlyHint {
		t.Fatalf("tv_power_on %+v", on)
	}

	// A plain text result.
	call, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "tv_power_on", Arguments: map[string]any{"tv": "living", "pin": "1"}})
	if err != nil || call.IsError || text(call) != "Living room TV is on." {
		t.Fatalf("tv_power_on: %v %+v", err, call)
	}
	// A structured result.
	call, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "tv_status"})
	if err != nil || call.IsError {
		t.Fatalf("tv_status: %v %+v", err, call)
	}
	if b, _ := json.Marshal(call.StructuredContent); string(b) != `{"input":"HDMI 1","on":true}` && string(b) != `{"on":true,"input":"HDMI 1"}` {
		t.Fatalf("structured %s", b)
	}
	// Failures come back as isError text the model can relay.
	for mode, want := range map[string]string{
		"fail":    "Living room TV is offline.",
		"down":    "not running",
		"timeout": "did not answer in time",
		"gone":    "new chat",
		"boom":    "could not run",
	} {
		call, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "tv_power_on", Arguments: map[string]any{"mode": mode}})
		if err != nil || !call.IsError || !strings.Contains(text(call), want) {
			t.Errorf("mode %s: %v %+v", mode, err, call)
		}
	}

	calls := e.host.callLog()
	if calls[0].Caller != (hub.Caller{Person: "Roman", Via: "mcp"}) || calls[0].Args != `{"pin":"1","tv":"living"}` {
		t.Fatalf("host call %+v", calls[0])
	}
	entries := e.audit.all()
	if len(entries) != 7 {
		t.Fatalf("%d audit entries", len(entries))
	}
	if entries[0].Caller.Via != "mcp" || string(entries[0].Args) != `{"pin":"[redacted]","tv":"living"}` || !entries[0].OK {
		t.Fatalf("audit %+v (%s)", entries[0], entries[0].Args)
	}
	if entries[2].OK || entries[2].Error == "" {
		t.Fatalf("failed call audited as ok: %+v", entries[2])
	}
}

func TestMCPScopes(t *testing.T) {
	e := newEnv(t, Options{})
	tanya, err := e.connect(t, e.tanya)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(t, tanya); !slices.Equal(got, []string{"tv_power_on", "tv_status"}) {
		t.Fatalf("tanya sees %v", got)
	}
	res, err := tanya.CallTool(t.Context(), &mcp.CallToolParams{Name: "kettle_boil"})
	if err == nil && !res.IsError {
		t.Fatal("tanya called kettle_boil")
	}
	guest, err := e.connect(t, e.guest)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(t, guest); len(got) != 0 {
		t.Fatalf("guest sees %v", got)
	}
	for _, c := range e.host.callLog() {
		if c.Name == "kettle_boil" {
			t.Fatal("the host got a call outside the person's scope")
		}
	}
	// People with the same scopes share one server; different scopes get their own.
	snap := e.gw.current()
	if len(snap.views) != 2 {
		t.Fatalf("%d views", len(snap.views))
	}
}

func TestMCPVersionFollowsTools(t *testing.T) {
	e := newEnv(t, Options{})
	version := func(key string) string {
		cs, err := e.connect(t, key)
		if err != nil {
			t.Fatal(err)
		}
		return cs.InitializeResult().ServerInfo.Version
	}
	v1 := version(e.roman)
	tanya1 := version(e.tanya)
	if v1 == tanya1 {
		t.Fatal("different tool sets share a fingerprint")
	}

	// A rebuild with the same tools keeps the fingerprint.
	e.host.setTools(baseTools())
	if v := version(e.roman); v != v1 {
		t.Fatalf("same tools, new version %s != %s", v, v1)
	}

	// A changed description moves it, and the new tool set is served.
	tools := baseTools()
	tools[0].Description = "Turns a TV on."
	tools = append(tools, hub.Tool{
		PluginID: "tv", Name: "tv_volume_set", Description: "Sets the volume, 0-100.",
		InputSchema: json.RawMessage(`{"type":"object"}`), Audience: hub.AudienceAssistant, Timeout: time.Second,
	})
	e.host.setTools(tools)
	v2 := version(e.roman)
	if v2 == v1 {
		t.Fatal("version did not change with the tools")
	}
	cs, err := e.connect(t, e.roman)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(t, cs); !slices.Contains(got, "tv_volume_set") {
		t.Fatalf("new tool not served: %v", got)
	}
	_, _, body := e.do(t, "GET", "/api/v1/tools", e.roman, "")
	if !strings.Contains(body, "tv_volume_set") {
		t.Fatal("REST manifest not rebuilt")
	}
}

func TestMCPRateLimit(t *testing.T) {
	e := newEnv(t, Options{RateLimit: 2})
	e.gw.limiter.now = func() time.Time { return time.Unix(0, 0) }
	cs, err := e.connect(t, e.roman)
	if err != nil {
		t.Fatal(err)
	}
	// The REST façade and /mcp share the key's bucket.
	if status, _, _ := e.do(t, "POST", "/api/v1/tools/tv_power_on", e.roman, "{}"); status != 200 {
		t.Fatalf("REST %d", status)
	}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "tv_power_on"})
	if err != nil || res.IsError {
		t.Fatalf("first MCP call: %v %+v", err, res)
	}
	res, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "tv_power_on"})
	if err != nil || !res.IsError || !strings.Contains(text(res), "Too many calls") {
		t.Fatalf("limited call: %v %+v", err, res)
	}
	if n := len(e.host.callLog()); n != 2 {
		t.Fatalf("host saw %d calls", n)
	}
}

// Through the relay, requests reach 127.0.0.1 with the public Host header; the SDK's DNS-rebinding guard must not
// turn them away.
func TestMCPAcceptsPublicHostOnLoopback(t *testing.T) {
	e := newEnv(t, Options{})
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},` +
		`"clientInfo":{"name":"t","version":"1"}}}`
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, e.srv.URL+"/mcp", strings.NewReader(body))
	req.Host = "homie.example.com"
	req.Header.Set("Authorization", "Bearer "+e.roman)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out struct {
		Result struct {
			ServerInfo mcp.Implementation `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Result.ServerInfo.Name != "homie" || resp.Header.Get(instanceHeader) != "inst-123" {
		t.Fatalf("reply %+v", out)
	}
}
