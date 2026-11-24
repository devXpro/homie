package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/devXpro/homie/internal/hub"
)

// syncBuffer is a goroutine-safe log sink: the gateway logs from the server's goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
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

// A tool whose schema makes the go-sdk's AddTool panic is skipped with a warning; the other tools of the same plugin
// stay usable on every surface.
func TestToolRefusedByMCPServerIsSkipped(t *testing.T) {
	var logs syncBuffer
	e := newEnv(t, Options{Log: slog.New(slog.NewTextHandler(&logs, nil))})
	bad := []hub.Tool{
		{
			PluginID: "tv", Name: "tv_header_object", Description: "x-mcp-header on an object property.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"opts":{"type":"object","x-mcp-header":"X-Opts"}}}`),
			Audience:    hub.AudienceAssistant,
		},
		{
			PluginID: "tv", Name: "tv_header_bad_name", Description: "x-mcp-header that is not a header name.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"tv":{"type":"string","x-mcp-header":"bad name"}}}`),
			Audience:    hub.AudienceAssistant,
		},
	}
	e.host.setTools(append(baseTools(), bad...))

	status, _, body := e.do(t, "GET", "/api/v1/tools", e.tanya, "")
	if status != 200 {
		t.Fatalf("GET /api/v1/tools = %d %s", status, body)
	}
	var m manifest
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range m.Tools {
		names = append(names, tool.Name)
	}
	if !slices.Equal(names, []string{"tv_power_on", "tv_status"}) {
		t.Fatalf("manifest tools = %v", names)
	}
	if status, _, body := e.do(t, "POST", "/api/v1/tools/tv_power_on", e.tanya, `{}`); status != 200 {
		t.Fatalf("POST tv_power_on = %d %s", status, body)
	}
	if status, _, _ := e.do(t, "POST", "/api/v1/tools/tv_header_object", e.tanya, `{}`); status != 404 {
		t.Fatalf("POST of the skipped tool = %d, want 404", status)
	}
	cs, err := e.connect(t, e.tanya)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(t, cs); !slices.Equal(got, []string{"tv_power_on", "tv_status"}) {
		t.Fatalf("/mcp tools = %v", got)
	}
	for _, name := range []string{"tv_header_object", "tv_header_bad_name"} {
		if !strings.Contains(logs.String(), name) {
			t.Errorf("no warning names %s: %s", name, logs.String())
		}
	}
}

func TestCheckAddTool(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		output string
		ok     bool
	}{
		{"plain", `{"type":"object","properties":{"tv":{"type":"string"}}}`, "", true},
		{"header on a string", `{"type":"object","properties":{"tv":{"type":"string","x-mcp-header":"X-TV"}}}`, "", true},
		{"header on an object", `{"type":"object","properties":{"o":{"type":"object","x-mcp-header":"X-O"}}}`, "", false},
		{"header twice", `{"type":"object","properties":{"a":{"type":"string","x-mcp-header":"X-A"},` +
			`"b":{"type":"string","x-mcp-header":"x-a"}}}`, "", false},
		{"header not a string", `{"type":"object","properties":{"a":{"type":"string","x-mcp-header":7}}}`, "", false},
		{"output schema", `{"type":"object"}`, `{"type":"object"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := hub.Tool{Name: "tv_x", InputSchema: json.RawMessage(tt.schema), Audience: hub.AudienceAssistant}
			if tt.output != "" {
				tool.OutputSchema = json.RawMessage(tt.output)
			}
			if err := checkAddTool(tool); (err == nil) != tt.ok {
				t.Fatalf("checkAddTool = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

// Schema-validation failures quote the rejected values; the audit log must not keep them.
func TestAuditDropsValidationDetails(t *testing.T) {
	e := newEnv(t, Options{})
	status, _, body := e.do(t, "POST", "/api/v1/tools/tv_power_on", e.roman, `{"mode":"invalid","pin":"secret-4321"}`)
	if status != 422 || !strings.Contains(body, "validating") {
		t.Fatalf("invalid arguments = %d %s", status, body) // the caller still learns what was wrong
	}
	e.do(t, "POST", "/api/v1/tools/tv_power_on", e.roman, `{"mode":"rpcinvalid","pin":"secret-4321"}`)
	e.do(t, "POST", "/api/v1/tools/tv_power_on", e.roman, `{"mode":"fail"}`)
	got := e.audit.all()
	if len(got) != 3 {
		t.Fatalf("%d audit entries: %+v", len(got), got)
	}
	for i, want := range []string{errInvalidArgs, errInvalidArgs, "Living room TV is offline. Turn it on with the remote once."} {
		if got[i].Error != want || got[i].OK {
			t.Errorf("entry %d error = %q, want %q", i, got[i].Error, want)
		}
		if strings.Contains(got[i].Error+string(got[i].Args), "secret-4321") {
			t.Errorf("entry %d leaks the value: %+v", i, got[i])
		}
	}
}

// AuditError is also what the homed CLI socket writes to the audit log.
func TestAuditError(t *testing.T) {
	rpc := &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: `pin "1234" is too short`}
	tests := []struct {
		name string
		res  hub.Result
		err  error
		want string
	}{
		{"ok", hub.Result{Text: "done"}, nil, ""},
		{"tool failure keeps its text", hub.Result{Text: "TV is off.", IsError: true}, nil, "TV is off."},
		{"typed handler validation", hub.Result{Text: `validating "arguments": "1234" too short`, IsError: true}, nil, errInvalidArgs},
		{"JSON-RPC invalid params", hub.Result{}, fmt.Errorf("tv_pair: %w", rpc), errInvalidArgs},
		{"other JSON-RPC error", hub.Result{}, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "boom"}, "boom"},
		{"plugin down", hub.Result{}, hub.ErrPluginDown, "plugin is not running"},
	}
	for _, tt := range tests {
		if got := AuditError(tt.res, tt.err); got != tt.want {
			t.Errorf("%s: AuditError = %q, want %q", tt.name, got, tt.want)
		}
	}
}
