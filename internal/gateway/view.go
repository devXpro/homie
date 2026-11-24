package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/devXpro/homie/internal/hub"
)

// snapshot is the host's assistant tool set at one moment, plus the per-scope views built from it on demand. It is
// replaced as soon as the host's Changed channel captured with it is closed.
type snapshot struct {
	changed      <-chan struct{}
	tools        []hub.Tool
	homeName     string // the MCP title; SetHomeName drops the snapshot
	instructions string

	mu    sync.Mutex
	views map[string]*view // keyed by scopeKey
}

// view is what one scope set (the plugins a person may use) sees: the tools, the REST manifest and the MCP server.
type view struct {
	byName   map[string]hub.Tool
	manifest []byte // the GET /api/v1/tools body
	version  string // "<hub version>+<fingerprint>"
	server   *mcp.Server
}

// manifestTool is one tool as the Site reads it (librus-gpt/site/core/mcp.mjs TOOL_KEYS). Optional keys are omitted.
type manifestTool struct {
	Name         string          `json:"name"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
}

type manifest struct {
	Instructions string         `json:"instructions"`
	Tools        []manifestTool `json:"tools"`
}

// current returns the snapshot, rebuilding it when the tool set has changed since it was taken.
func (g *gateway) current() *snapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.snap != nil && !closed(g.snap.changed) {
		return g.snap
	}
	changed := g.host.Changed() // before Tools, so a change in between is not missed
	var tools []hub.Tool
	for _, t := range g.host.Tools(hub.AudienceAssistant) {
		t, ok := usable(t)
		if !ok {
			continue
		}
		if err := checkAddTool(t); err != nil {
			// One bad schema must not take /mcp and the REST façade down for everyone who may use the plugin.
			g.opts.Log.Warn("skipping tool the MCP server refuses", "tool", t.Name, "plugin", t.PluginID, "err", err)
			continue
		}
		tools = append(tools, t)
	}
	g.snap = &snapshot{
		changed: changed, tools: tools, homeName: g.opts.HomeName, instructions: g.instructions(), views: map[string]*view{},
	}
	return g.snap
}

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// usable keeps assistant tools whose input schema is a JSON object of type "object", as MCP (and the go-sdk) require.
// A missing schema becomes {"type":"object"}. Null optional fields are dropped so they are omitted, never null.
func usable(t hub.Tool) (hub.Tool, bool) {
	if t.Audience != hub.AudienceAssistant || t.Name == "" {
		return t, false
	}
	t.InputSchema = present(t.InputSchema)
	if t.InputSchema == nil {
		t.InputSchema = json.RawMessage(`{"type":"object"}`)
	}
	var schema struct {
		Type any `json:"type"`
	}
	if json.Unmarshal(t.InputSchema, &schema) != nil || schema.Type != "object" {
		return t, false
	}
	t.OutputSchema = present(t.OutputSchema)
	t.Annotations = present(t.Annotations)
	if t.OutputSchema != nil && !json.Valid(t.OutputSchema) {
		t.OutputSchema = nil
	}
	if t.Annotations != nil && !json.Valid(t.Annotations) {
		t.Annotations = nil
	}
	return t, true
}

// checkAddTool registers t on a throwaway MCP server and turns the go-sdk's AddTool panic (a schema it refuses, e.g.
// x-mcp-header on a non-primitive property) into an error, so buildView never panics on the request path.
func checkAddTool(t hub.Tool) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	s := mcp.NewServer(&mcp.Implementation{Name: "homie-check", Version: "0"}, nil)
	s.AddTool(mcpTool(t), func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, nil })
	return nil
}

// present returns nil for an empty or JSON-null raw value, the value otherwise.
func present(raw json.RawMessage) json.RawMessage {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	return raw
}

// instructions is the hub preamble plus the plugins' own instructions (§5.4). g.mu is held. The home name is the
// owner's text: it goes in as a quoted string (strconv.Quote), so a quote or a line break in it can never end the
// name and read as an instruction of its own. The admin refuses such names anyway (wire.CheckName).
func (g *gateway) instructions() string {
	home := "the user's home"
	if g.opts.HomeName != "" {
		home = "the home " + strconv.Quote(g.opts.HomeName)
	}
	text := "These tools control devices in " + home + ". Act only when the user asks. After acting, confirm what " +
		"was done in one short sentence. If a device is offline or a tool fails, say so plainly and never claim it worked."
	if g.opts.Instructions != nil {
		if extra := strings.TrimSpace(g.opts.Instructions()); extra != "" {
			text += "\n\n" + extra
		}
	}
	return text
}

// scopeKey normalizes a person's plugin list: "*" if it allows everything, else the sorted unique ids.
func scopeKey(plugins []string) string {
	if slices.Contains(plugins, "*") {
		return "*"
	}
	ids := slices.Clone(plugins)
	slices.Sort(ids)
	return strings.Join(slices.Compact(ids), ",")
}

func allowed(scope, pluginID string) bool {
	return scope == "*" || slices.Contains(strings.Split(scope, ","), pluginID)
}

// view returns the view for a person's scopes, building it on first use.
func (s *snapshot) view(g *gateway, person hub.Person) *view {
	scope := scopeKey(person.Plugins)
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.views[scope]; ok {
		return v
	}
	v := g.buildView(s, scope)
	s.views[scope] = v
	return v
}

func (g *gateway) buildView(s *snapshot, scope string) *view {
	v := &view{byName: map[string]hub.Tool{}}
	m := manifest{Instructions: s.instructions, Tools: []manifestTool{}}
	var tools []hub.Tool
	for _, t := range s.tools {
		if !allowed(scope, t.PluginID) {
			continue
		}
		tools = append(tools, t)
		v.byName[t.Name] = t
		m.Tools = append(m.Tools, manifestTool{
			Name: t.Name, Title: t.Title, Description: t.Description,
			InputSchema: t.InputSchema, OutputSchema: t.OutputSchema, Annotations: t.Annotations,
		})
	}
	// Marshaling cannot fail: usable checked every raw field.
	toolsJSON, _ := marshal(m.Tools)
	v.version = g.opts.Version + "+" + fingerprint(string(toolsJSON)+s.instructions)
	v.manifest, _ = marshal(m)

	v.server = mcp.NewServer(
		&mcp.Implementation{Name: "homie", Title: s.homeName, Version: v.version},
		&mcp.ServerOptions{
			Instructions: s.instructions,
			Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}, // stateless: no list_changed
		},
	)
	for _, t := range tools {
		v.server.AddTool(mcpTool(t), g.mcpToolHandler(t))
	}
	return v
}

// fingerprint is FNV-1a 32 in hex, the same trick as the Librus Site: the version moves when what the model reads
// changes, so ChatGPT reloads the tools.
func fingerprint(text string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(text))
	return fmt.Sprintf("%08x", h.Sum32())
}

func mcpTool(t hub.Tool) *mcp.Tool {
	out := &mcp.Tool{Name: t.Name, Title: t.Title, Description: t.Description, InputSchema: t.InputSchema}
	if t.OutputSchema != nil {
		out.OutputSchema = t.OutputSchema
	}
	if t.Annotations != nil {
		var a mcp.ToolAnnotations
		if json.Unmarshal(t.Annotations, &a) == nil {
			out.Annotations = &a
		}
	}
	return out
}

// listTools serves GET /api/v1/tools.
func (g *gateway) listTools(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey{}).(principal)
	writeRaw(w, http.StatusOK, g.current().view(g, p.person).manifest)
}

// mcpServer picks the MCP server for the request's person (called by the go-sdk handler for every request).
func (g *gateway) mcpServer(r *http.Request) *mcp.Server {
	p, ok := r.Context().Value(principalKey{}).(principal)
	if !ok {
		return nil
	}
	return g.current().view(g, p.person).server
}
