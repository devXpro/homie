package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/keys"
)

// fakeHost is a hub.ToolHost with a fixed tool set that tests can swap. Calls are steered by the "mode" argument.
type fakeHost struct {
	mu      sync.Mutex
	tools   []hub.Tool
	changed chan struct{}
	calls   []fakeCall
}

type fakeCall struct {
	Caller hub.Caller
	Name   string
	Args   string
}

func newFakeHost(tools []hub.Tool) *fakeHost {
	return &fakeHost{tools: tools, changed: make(chan struct{})}
}

func (f *fakeHost) Tools(audience hub.Audience) []hub.Tool {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []hub.Tool
	for _, t := range f.tools {
		if audience == hub.AudienceAdmin || t.Audience == audience {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b hub.Tool) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (f *fakeHost) setTools(tools []hub.Tool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tools = tools
	close(f.changed)
	f.changed = make(chan struct{})
}

func (f *fakeHost) Changed() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.changed
}

func (f *fakeHost) Status() []hub.PluginStatus { return nil }

func (f *fakeHost) Call(ctx context.Context, caller hub.Caller, name string, args json.RawMessage) (hub.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{Caller: caller, Name: name, Args: string(args)})
	f.mu.Unlock()
	var in struct {
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal(args, &in)
	switch in.Mode {
	case "fail":
		return hub.Result{Text: "Living room TV is offline. Turn it on with the remote once.", IsError: true}, nil
	case "down":
		return hub.Result{}, hub.ErrPluginDown
	case "gone":
		return hub.Result{}, hub.ErrUnknownTool
	case "timeout":
		return hub.Result{}, context.DeadlineExceeded
	case "hang":
		<-ctx.Done()
		return hub.Result{}, ctx.Err()
	case "boom":
		return hub.Result{}, errors.New("broken pipe")
	case "invalid": // what a go-sdk typed handler returns for arguments its schema rejects
		return hub.Result{
			Text:    `validating "arguments": validating root: validating /properties/pin: pattern: "secret-4321" does not match`,
			IsError: true,
		}, nil
	case "rpcinvalid":
		return hub.Result{}, fmt.Errorf("%s: %w", name, &jsonrpc.Error{
			Code: jsonrpc.CodeInvalidParams, Message: `bad pin "secret-4321"`,
		})
	}
	if name == "tv_status" {
		return hub.Result{Text: "Living room TV is on.", Structured: json.RawMessage(`{"on":true,"input":"HDMI 1"}`)}, nil
	}
	return hub.Result{Text: "Living room TV is on."}, nil
}

func (f *fakeHost) callLog() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// recorder is a hub.Auditor that keeps entries in memory.
type recorder struct {
	mu      sync.Mutex
	entries []hub.AuditEntry
}

func (r *recorder) Record(_ context.Context, e hub.AuditEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
}

func (r *recorder) all() []hub.AuditEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.entries)
}

func baseTools() []hub.Tool {
	return []hub.Tool{
		{
			PluginID: "tv", Name: "tv_power_on", Title: "Turn the TV on",
			Description: "Turns a TV on (Wake-on-LAN, then waits up to 20 s for it to answer).",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"tv":{"type":"string","description":"TV id"}}}`),
			Annotations: json.RawMessage(`{"readOnlyHint":false,"destructiveHint":false,"idempotentHint":true}`),
			Audience:    hub.AudienceAssistant, Timeout: 30 * time.Second,
		},
		{
			PluginID: "tv", Name: "tv_status", Title: "TV status",
			Description:  "Says whether each TV is on and which input it shows.",
			InputSchema:  json.RawMessage(`{"type":"object"}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"on":{"type":"boolean"},"input":{"type":"string"}}}`),
			Annotations:  json.RawMessage(`{"readOnlyHint":true}`),
			Audience:     hub.AudienceAssistant, Inventory: true, Timeout: 30 * time.Second,
		},
		{
			PluginID: "tv", Name: "tv_pair", Title: "Pair a TV", Description: "Pairs a TV. Admin only.",
			InputSchema: json.RawMessage(`{"type":"object"}`), Audience: hub.AudienceAdmin, Timeout: 180 * time.Second,
		},
		{
			// Only the required keys, and explicit nulls that must not leak into the manifest.
			PluginID: "kettle", Name: "kettle_boil", Description: "Boils the kettle.",
			InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`null`),
			Audience: hub.AudienceAssistant,
		},
		{
			// Not a valid MCP input schema: skipped on every surface.
			PluginID: "tv", Name: "tv_broken", Description: "Bad schema.",
			InputSchema: json.RawMessage(`{"type":"string"}`), Audience: hub.AudienceAssistant,
		},
	}
}

type env struct {
	host   *fakeHost
	audit  *recorder
	gw     *gateway
	srv    *httptest.Server
	roman  string // plugins: *
	tanya  string // plugins: tv
	guest  string // plugins: none
	romanB string // Roman's second key
}

func newEnv(t *testing.T, opts Options) *env {
	t.Helper()
	e := &env{host: newFakeHost(baseTools()), audit: &recorder{}}
	var sums [4]string
	e.roman, sums[0] = keys.Generate()
	e.romanB, sums[1] = keys.Generate()
	e.tanya, sums[2] = keys.Generate()
	e.guest, sums[3] = keys.Generate()
	roman := hub.Person{ID: "roman", Name: "Roman", Plugins: []string{"*"}}
	resolver, err := keys.New([]keys.Entry{
		{Person: roman, SHA256: sums[0]},
		{Person: roman, SHA256: sums[1]},
		{Person: hub.Person{ID: "tanya", Name: "Tanya", Plugins: []string{"tv", "tv"}}, SHA256: sums[2]},
		{Person: hub.Person{ID: "guest", Name: "Guest"}, SHA256: sums[3]},
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.HomeName == "" {
		opts.HomeName = "Мой шалаш"
	}
	if opts.InstanceID == "" {
		opts.InstanceID = "inst-123"
	}
	if opts.Version == "" {
		opts.Version = "0.1.0"
	}
	e.gw = New(e.host, resolver, e.audit, opts).(*gateway)
	e.srv = httptest.NewServer(e.gw)
	t.Cleanup(e.srv.Close)
	return e
}

// do sends a request and returns the status, headers and body.
func (e *env) do(t *testing.T, method, path, key, body string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(b)
}

// clock is a settable time source that is safe to read from the server's goroutines.
type clock struct{ ns atomic.Int64 }

func newClock(at time.Time) *clock {
	c := &clock{}
	c.ns.Store(at.UnixNano())
	return c
}

func (c *clock) now() time.Time      { return time.Unix(0, c.ns.Load()) }
func (c *clock) add(d time.Duration) { c.ns.Add(int64(d)) }
