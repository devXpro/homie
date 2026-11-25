// Command homie-plug is a Homie plugin for smart plugs (Shelly Gen2+ over the local RPC API).
//
// It is the plugin template of docs/PLUGINS.md: one assistant tool (plug_switch), one inventory tool (plug_list) and
// one admin tool (plug_add, with progress). Copy the directory, rename it, and swap Shelly for your own device.
//
//	homie-plug [serve]   speak MCP over stdio to the hub (the default)
//	homie-plug version   print the version
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/devXpro/homie/pluginkit"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is set at release time: -ldflags "-X main.version={{.Version}}" (see .goreleaser.yaml).
var version = "0.0.0-dev"

// instructions reach the model through the hub, merged with the hub's own. Keep them short and in English.
const instructions = "Smart plugs switch lamps and other appliances. Switch a plug only when the user asks."

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "homie-plug:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "serve":
		dir, err := pluginkit.DataDir() // HOMIE_DATA_DIR under the hub, ./data in development
		if err != nil {
			return err
		}
		return pluginkit.Serve(ctx, newServer(NewStore(dir), NewShelly()))
	case "version":
		_, err := fmt.Fprintln(out, version)
		return err
	default:
		return fmt.Errorf("unknown command %q; usage: homie-plug [serve | version]", cmd)
	}
}

// newServer registers every tool. Tests call it with a fake device and a temp store.
func newServer(st *Store, dev Device) *mcp.Server {
	s := pluginkit.New(pluginkit.Plugin{
		ID: "plug", Name: "homie-plug", Title: "Smart plugs", Version: version, Instructions: instructions,
	})
	h := &handlers{store: st, dev: dev}

	// Inventory: the admin's device list, and a read-only tool the assistant may use.
	mcp.AddTool(s, pluginkit.I18n(pluginkit.Inventory(&mcp.Tool{
		Name:        "plug_list",
		Title:       "List plugs",
		Description: "Lists the smart plugs at home with their id, name and state (on, off or offline).",
		InputSchema: object(nil),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}), "ru", pluginkit.Text{Title: "Розетки", Description: "Все умные розетки дома и включены ли они."}), h.list)

	// Assistant tool: what ChatGPT calls when someone says "turn on the desk lamp".
	mcp.AddTool(s, pluginkit.I18n(&mcp.Tool{
		Name:        "plug_switch",
		Title:       "Switch a plug",
		Description: "Turns a smart plug on or off. Leave out plug when only one plug is set up.",
		InputSchema: object(map[string]any{
			"plug": pluginkit.InventoryRef(map[string]any{
				"type":        "string",
				"description": "The plug's id or name, as plug_list shows it.",
			}),
			"on": map[string]any{"type": "boolean", "description": "true to turn on, false to turn off."},
		}, "on"),
		Annotations: &mcp.ToolAnnotations{DestructiveHint: new(false), IdempotentHint: true},
	}, "ru", pluginkit.Text{
		Title:       "Включить или выключить",
		Description: "Включает или выключает розетку.",
		Properties:  map[string]string{"plug": "Розетка", "on": "Включить"},
	}), h.switchPlug)

	// Admin tool: hidden from assistants, shown in the admin as an action with a form and progress.
	mcp.AddTool(s, pluginkit.I18n(pluginkit.Admin(pluginkit.Timeout(&mcp.Tool{
		Name:        "plug_add",
		Title:       "Add a plug",
		Description: "Adds the Shelly Gen2+ plug at a LAN address and reads its model. The plug must be powered.",
		InputSchema: object(map[string]any{
			"host": map[string]any{"type": "string", "description": "The plug's IPv4 address, e.g. 192.168.1.30."},
			"name": map[string]any{"type": "string", "description": "What people call it, e.g. Desk lamp."},
		}, "host"),
		Annotations: &mcp.ToolAnnotations{DestructiveHint: new(false)},
	}, 30*time.Second)), "ru", pluginkit.Text{
		Title:       "Добавить розетку",
		Description: "Умная розетка Shelly по её IP-адресу.",
		Properties:  map[string]string{"host": "IP-адрес", "name": "Название"},
	}), h.add)
	return s
}

type handlers struct {
	store *Store
	dev   Device
}

type (
	switchArgs struct {
		Plug string `json:"plug,omitempty"`
		On   bool   `json:"on"`
	}
	addArgs struct {
		Host string `json:"host"`
		Name string `json:"name,omitempty"`
	}
)

// item is one inventory entry; state is one of on|off|offline|unknown|paired|unpaired.
type item struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Subtitle string `json:"subtitle,omitempty"`
	State    string `json:"state"`
}

func (h *handlers) list(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	plugs, err := h.store.List()
	if err != nil {
		return nil, nil, err // the SDK turns a plain error into an isError result
	}
	items := make([]item, len(plugs))
	words := make([]string, len(plugs))
	for i, p := range plugs {
		state := "offline"
		probe, cancel := context.WithTimeout(ctx, 2*time.Second) // keep the list fast when a plug is unplugged
		if on, err := h.dev.State(probe, p.Host); err == nil {
			state = map[bool]string{true: "on", false: "off"}[on]
		}
		cancel()
		items[i] = item{ID: p.ID, Name: p.Name, Subtitle: p.Model + " · " + p.Host, State: state}
		words[i] = p.Name + " is " + state
	}
	text := "No plugs are set up yet."
	if len(plugs) > 0 {
		text = strings.Join(words, "; ") + "."
	}
	return result(text, map[string]any{"items": items}), nil, nil
}

func (h *handlers) switchPlug(ctx context.Context, req *mcp.CallToolRequest, in switchArgs) (*mcp.CallToolResult, any, error) {
	p, problem, err := h.find(in.Plug)
	if err != nil {
		return nil, nil, err
	}
	if problem != "" {
		return failure(problem), nil, nil
	}
	person, via := pluginkit.Caller(req) // for the log only: never a security decision
	slog.Info("switch", "plug", p.ID, "on", in.On, "caller", person, "via", via)
	if err := h.dev.Set(ctx, p.Host, in.On); err != nil {
		slog.Warn("switch failed", "plug", p.ID, "err", err) // stderr is the plugin's log
		return failure(p.Name + " did not answer. Check that it is plugged in and on the home Wi-Fi."), nil, nil
	}
	word := map[bool]string{true: "on", false: "off"}[in.On]
	return result(p.Name+" is "+word+".", map[string]any{"plug": p.ID, "on": in.On}), nil, nil
}

func (h *handlers) add(ctx context.Context, req *mcp.CallToolRequest, in addArgs) (*mcp.CallToolResult, any, error) {
	host := strings.TrimSpace(in.Host)
	if host == "" || strings.ContainsAny(host, "/?#@ ") {
		return failure("Give the plug's address on the home network, e.g. 192.168.1.30."), nil, nil
	}
	// Tell the admin what is happening; a no-op when an assistant calls (no progress token).
	_ = pluginkit.Progress(ctx, req, 1, 2, "Checking the plug at "+host,
		map[string]string{"ru": "Проверяем розетку " + host})
	model, err := h.dev.Info(ctx, host)
	if err != nil {
		return failure("No Shelly plug answered at " + host + ". Check the address and that the plug is powered."), nil, nil
	}
	_ = pluginkit.Progress(ctx, req, 2, 2, "Saving", map[string]string{"ru": "Сохраняем"})

	plugs, err := h.store.List()
	if err != nil {
		return nil, nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "Plug " + model
	}
	p := Plug{ID: newID(plugs, host), Name: name, Host: host, Model: model}
	if err := h.store.Add(p); err != nil {
		return nil, nil, err
	}
	return result(p.Name+" is added.", map[string]any{"plug": p}), nil, nil
}

// find resolves the plug argument: an id or a name, or nothing when exactly one plug exists. problem is a text for
// the user when it cannot be resolved.
func (h *handlers) find(arg string) (p Plug, problem string, err error) {
	plugs, err := h.store.List()
	if err != nil {
		return Plug{}, "", err
	}
	if len(plugs) == 0 {
		return Plug{}, "No plugs are set up yet. Add one in the Homie admin.", nil
	}
	arg = strings.TrimSpace(arg)
	if arg == "" && len(plugs) == 1 {
		return plugs[0], "", nil
	}
	names := make([]string, len(plugs))
	for i, q := range plugs {
		if arg != "" && (q.ID == arg || strings.EqualFold(q.Name, arg)) {
			return q, "", nil
		}
		names[i] = q.Name + " (" + q.ID + ")"
	}
	return Plug{}, "Say which plug: " + strings.Join(names, ", ") + ".", nil
}

// newID keeps the id of a plug already stored at host; otherwise it is "plug", "plug-2", ...
func newID(plugs []Plug, host string) string {
	taken := map[string]bool{}
	for _, p := range plugs {
		if p.Host == host {
			return p.ID
		}
		taken[p.ID] = true
	}
	id := "plug"
	for n := 2; taken[id]; n++ {
		id = fmt.Sprintf("plug-%d", n)
	}
	return id
}

// result is a successful answer. A client may show only structuredContent when there is one, so the human text goes
// into it as "message" too.
func result(text string, structured map[string]any) *mcp.CallToolResult {
	structured["message"] = text
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, StructuredContent: structured}
}

// failure is a tool that ran and failed: isError with a text that says what to do.
func failure(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func object(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}
