// Package hub holds the types the hub's packages share, so they can be built in parallel.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Audience says who may see a tool.
type Audience string

const (
	AudienceAssistant Audience = "assistant" // ChatGPT and other assistants
	AudienceAdmin     Audience = "admin"     // the admin UI and the homed CLI only
)

// Tool is one plugin tool as the hub exposes it.
type Tool struct {
	PluginID     string
	Name         string // "<plugin id>_<verb>", unique across the hub
	Title        string
	Description  string
	InputSchema  json.RawMessage // JSON Schema object
	OutputSchema json.RawMessage // optional
	Annotations  json.RawMessage // MCP tool annotations, passed through unchanged; optional
	Audience     Audience
	Inventory    bool          // _meta "homie/role": "inventory"
	Timeout      time.Duration // from _meta "homie/timeout", default 30s, max 300s
	// I18n holds translated texts from _meta "homie/i18n", keyed by language ("ru"). Only the admin UI uses it;
	// assistants always get the English Title and Description. Optional.
	I18n map[string]ToolText
}

// ToolText is a tool's title, description and input labels in one language.
type ToolText struct {
	Title       string
	Description string
	Properties  map[string]string // input property name → label
}

// Result is the outcome of a tool that ran.
type Result struct {
	Text       string          // the plugin's text content, joined with newlines
	Structured json.RawMessage // structuredContent, if any
	IsError    bool            // the tool ran and reported a failure
}

// Caller says who asked; it goes to the activity log and to the plugin as request _meta (the name and Via only).
type Caller struct {
	Person   string // display name, e.g. "Alice"
	PersonID string // the gatekeeper person id; empty for the CLI
	Via      string // "chatgpt", "claude" (the MCP endpoint, by the assistant), "admin" or "cli"
}

// PluginState is the life state of one plugin process.
type PluginState string

const (
	PluginStarting PluginState = "starting"
	PluginUp       PluginState = "up"
	PluginFailing  PluginState = "failing"
	PluginStopped  PluginState = "stopped"
)

// PluginStatus reports one plugin for health checks and the admin.
type PluginStatus struct {
	ID        string
	Title     string
	Version   string
	State     PluginState
	LastError string
	Since     time.Time
}

// ToolHost runs the plugins and calls their tools.
type ToolHost interface {
	// Tools returns a snapshot of the tools visible to the audience, sorted by name.
	// AudienceAdmin sees every tool; AudienceAssistant sees only assistant tools.
	Tools(audience Audience) []Tool
	// Call runs a tool. err is non-nil when the call could not be made (ErrUnknownTool, ErrPluginDown,
	// context deadline). A tool that ran and failed returns Result.IsError with a nil err.
	Call(ctx context.Context, caller Caller, name string, args json.RawMessage) (Result, error)
	// Changed returns a channel that is closed at the next change of the tool set.
	Changed() <-chan struct{}
	// Status reports every plugin.
	Status() []PluginStatus
}

// AuditEntry is one tool call, as stored.
type AuditEntry struct {
	At       time.Time
	Caller   Caller
	Tool     string
	Args     json.RawMessage // already redacted
	OK       bool            // false when err != nil or Result.IsError
	Error    string
	Duration time.Duration
}

// Auditor records tool calls; it must not block the call path for long.
type Auditor interface {
	Record(ctx context.Context, e AuditEntry)
}

var (
	ErrUnknownTool = errors.New("unknown tool")
	ErrPluginDown  = errors.New("plugin is not running")
)
