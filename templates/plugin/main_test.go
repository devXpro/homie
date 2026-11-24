package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/devXpro/homie/pluginkit"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeShelly is an httptest server that answers the three RPC methods like a Shelly Plus Plug S.
type fakeShelly struct {
	mu sync.Mutex
	on bool
}

func (f *fakeShelly) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/rpc/Shelly.GetDeviceInfo":
		_, _ = w.Write([]byte(`{"id":"shellyplusplugs-1","model":"SNPL-00112EU","gen":2}`))
	case "/rpc/Switch.GetStatus":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 0, "output": f.on})
	case "/rpc/Switch.Set":
		was := f.on
		f.on = r.URL.Query().Get("on") == "true"
		_ = json.NewEncoder(w).Encode(map[string]any{"was_on": was})
	default:
		http.NotFound(w, r)
	}
}

type fixture struct {
	shelly   *fakeShelly
	session  *mcp.ClientSession
	progress chan *mcp.ProgressNotificationParams
}

// newFixture connects an in-memory MCP client to the plugin, as the hub would over stdio. Every host name resolves to
// the fake plug, except "offline.invalid", which has nothing listening.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{shelly: &fakeShelly{}, progress: make(chan *mcp.ProgressNotificationParams, 10)}
	srv := httptest.NewServer(f.shelly)
	t.Cleanup(srv.Close)
	dev := NewShelly()
	dev.Base = func(host string) string {
		if host == "offline.invalid" {
			return "http://127.0.0.1:1"
		}
		return srv.URL
	}

	ctx := t.Context()
	srvT, cliT := mcp.NewInMemoryTransports()
	ss, err := newServer(NewStore(t.TempDir()), dev).Connect(ctx, srvT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			f.progress <- req.Params
		},
	}).Connect(ctx, cliT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	f.session = cs
	return f
}

type reply struct {
	isError bool
	text    string
	data    map[string]any
}

func (f *fixture) call(t *testing.T, name string, args map[string]any, progressToken any) reply {
	t.Helper()
	params := &mcp.CallToolParams{Name: name, Arguments: args}
	if progressToken != nil {
		params.SetProgressToken(progressToken)
	}
	res, err := f.session.CallTool(t.Context(), params)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	r := reply{isError: res.IsError}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			r.text += tc.Text
		}
	}
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, &r.data); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestToolsAndMeta(t *testing.T) {
	f := newFixture(t)
	res, err := f.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{ admin, inventory bool }{
		"plug_list":   {inventory: true},
		"plug_switch": {},
		"plug_add":    {admin: true},
	}
	if len(res.Tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(res.Tools), len(want))
	}
	for _, tool := range res.Tools {
		w, ok := want[tool.Name]
		if !ok {
			t.Errorf("unexpected tool %s", tool.Name)
			continue
		}
		if err := pluginkit.CheckToolName("plug", tool.Name); err != nil {
			t.Error(err)
		}
		if aud, _ := tool.Meta[pluginkit.MetaAudience].(string); (aud == "admin") != w.admin {
			t.Errorf("%s: audience %q", tool.Name, aud)
		}
		if role, _ := tool.Meta[pluginkit.MetaRole].(string); (role == "inventory") != w.inventory {
			t.Errorf("%s: role %q", tool.Name, role)
		}
		if i18n, _ := tool.Meta[pluginkit.MetaI18n].(map[string]any); i18n["ru"] == nil {
			t.Errorf("%s: no Russian texts", tool.Name)
		}
	}
}

func TestAddSwitchList(t *testing.T) {
	f := newFixture(t)

	if r := f.call(t, "plug_switch", map[string]any{"on": true}, nil); !r.isError || !strings.Contains(r.text, "No plugs") {
		t.Fatalf("switch before add: %+v", r)
	}

	r := f.call(t, "plug_add", map[string]any{"host": "192.168.1.30", "name": "Desk lamp"}, "t1")
	if r.isError || r.text != "Desk lamp is added." || r.data["message"] != r.text {
		t.Fatalf("add: %+v", r)
	}
	if p := <-f.progress; p.ProgressToken != "t1" || p.Message != "Checking the plug at 192.168.1.30" {
		t.Errorf("progress: %+v", p)
	}

	r = f.call(t, "plug_switch", map[string]any{"on": true}, nil) // the only plug: no name needed
	if r.isError || r.text != "Desk lamp is on." {
		t.Fatalf("switch: %+v", r)
	}

	r = f.call(t, "plug_list", map[string]any{}, nil)
	items, _ := r.data["items"].([]any)
	if r.isError || len(items) != 1 {
		t.Fatalf("list: %+v", r)
	}
	if it := items[0].(map[string]any); it["id"] != "plug" || it["state"] != "on" {
		t.Errorf("item: %v", it)
	}
}

func TestFailures(t *testing.T) {
	f := newFixture(t)
	if r := f.call(t, "plug_add", map[string]any{"host": "offline.invalid"}, nil); !r.isError || !strings.Contains(r.text, "No Shelly plug") {
		t.Errorf("add offline: %+v", r)
	}
	f.call(t, "plug_add", map[string]any{"host": "192.168.1.30", "name": "Desk lamp"}, nil)
	f.call(t, "plug_add", map[string]any{"host": "192.168.1.31", "name": "Kettle"}, nil)
	if r := f.call(t, "plug_switch", map[string]any{"on": false}, nil); !r.isError || !strings.Contains(r.text, "Kettle (plug-2)") {
		t.Errorf("ambiguous switch: %+v", r)
	}
	if r := f.call(t, "plug_switch", map[string]any{"plug": "kettle", "on": true}, nil); r.isError || r.text != "Kettle is on." {
		t.Errorf("switch by name: %+v", r)
	}
}
