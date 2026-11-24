package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/pluginkit"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultTimeout = 30 * time.Second
	maxTimeout     = 300 * time.Second
)

// listTools fetches every page of tools/list and converts it. Tools that break the protocol rules are skipped with a
// warning; bad _meta values fall back to safe defaults.
func (h *Host) listTools(ctx context.Context, p *plugin, cs *mcp.ClientSession) ([]hub.Tool, error) {
	ctx, cancel := context.WithTimeout(ctx, h.timing.rpcTimeout)
	defer cancel()
	var tools []hub.Tool
	inventory := ""
	for t, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		ht, warnings, err := convertTool(p.id, t)
		for _, w := range warnings {
			h.log.Warn("plugin tool _meta", "plugin", p.id, "tool", t.Name, "problem", w)
		}
		if err != nil {
			h.log.Warn("skipping plugin tool", "plugin", p.id, "tool", t.Name, "err", err)
			continue
		}
		tools = append(tools, ht)
	}
	slices.SortFunc(tools, func(a, b hub.Tool) int { return strings.Compare(a.Name, b.Name) })
	for i := range tools {
		if !tools[i].Inventory {
			continue
		}
		if inventory != "" {
			h.log.Warn("plugin has more than one inventory tool; keeping the first", "plugin", p.id, "kept", inventory, "ignored", tools[i].Name)
			tools[i].Inventory = false
			continue
		}
		inventory = tools[i].Name
	}
	return tools, nil
}

// convertTool turns an MCP tool into a hub tool. An error means the tool must be skipped; warnings report _meta
// values that were replaced by defaults.
func convertTool(pluginID string, t *mcp.Tool) (hub.Tool, []string, error) {
	if err := pluginkit.CheckToolName(pluginID, t.Name); err != nil {
		return hub.Tool{}, nil, err
	}
	if err := checkAddTool(t); err != nil {
		return hub.Tool{}, nil, err
	}
	out := hub.Tool{
		PluginID:    pluginID,
		Name:        t.Name,
		Title:       t.Title,
		Description: t.Description,
		Audience:    hub.AudienceAssistant,
		Timeout:     defaultTimeout,
	}
	if out.Title == "" && t.Annotations != nil {
		out.Title = t.Annotations.Title
	}
	var err error
	if t.InputSchema == nil {
		out.InputSchema = json.RawMessage(`{"type":"object"}`)
	} else if out.InputSchema, err = json.Marshal(t.InputSchema); err != nil {
		return hub.Tool{}, nil, fmt.Errorf("input schema: %w", err)
	}
	if t.OutputSchema != nil {
		if out.OutputSchema, err = json.Marshal(t.OutputSchema); err != nil {
			return hub.Tool{}, nil, fmt.Errorf("output schema: %w", err)
		}
	}
	if t.Annotations != nil {
		if out.Annotations, err = json.Marshal(t.Annotations); err != nil {
			return hub.Tool{}, nil, fmt.Errorf("annotations: %w", err)
		}
	}

	var warnings []string
	if v, ok := t.Meta[pluginkit.MetaAudience]; ok {
		switch v {
		case string(hub.AudienceAssistant):
		case string(hub.AudienceAdmin):
			out.Audience = hub.AudienceAdmin
		default:
			// Hiding a tool by mistake is safer than exposing an admin tool to assistants.
			out.Audience = hub.AudienceAdmin
			warnings = append(warnings, fmt.Sprintf("unknown audience %v, treated as admin", v))
		}
	}
	if v, ok := t.Meta[pluginkit.MetaTimeout]; ok {
		secs, isNum := v.(float64)
		switch {
		case !isNum || !(secs > 0): // also catches NaN
			warnings = append(warnings, fmt.Sprintf("bad timeout %v, using %v", v, defaultTimeout))
		case secs > maxTimeout.Seconds():
			out.Timeout = maxTimeout
			warnings = append(warnings, fmt.Sprintf("timeout %vs capped at %v", secs, maxTimeout))
		default:
			out.Timeout = time.Duration(secs * float64(time.Second))
		}
	}
	if v, ok := t.Meta[pluginkit.MetaRole]; ok {
		switch {
		case v != "inventory":
			warnings = append(warnings, fmt.Sprintf("unknown role %v ignored", v))
		case t.Annotations == nil || !t.Annotations.ReadOnlyHint:
			warnings = append(warnings, "inventory role ignored: the tool is not readOnlyHint")
		default:
			out.Inventory = true
		}
	}
	if v, ok := t.Meta[pluginkit.MetaI18n]; ok {
		var w []string
		out.I18n, w = parseI18n(v)
		warnings = append(warnings, w...)
	}
	return out, warnings, nil
}

// checkAddTool registers a copy of t on a throwaway MCP server and turns the go-sdk's AddTool panic into an error. The
// gateway serves every tool through such a server, so a schema the SDK refuses (no "object" input schema, an
// unmarshalable output schema, x-mcp-header on a non-primitive property) is caught here, at listing time.
func checkAddTool(t *mcp.Tool) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the MCP server refuses this tool: %v", r)
		}
	}()
	tt := *t
	if tt.InputSchema == nil {
		tt.InputSchema = json.RawMessage(`{"type":"object"}`) // what convertTool substitutes
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "homie-check", Version: "0"}, nil)
	s.AddTool(&tt, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, nil })
	return nil
}

var langRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// parseI18n reads _meta "homie/i18n": {"<lang>": {"title", "description", "properties": {"<prop>": "<label>"}}}.
// It is lenient: malformed languages and fields are skipped with a warning, the rest is kept. It returns nil when
// nothing usable is left.
func parseI18n(v any) (map[string]hub.ToolText, []string) {
	langs, ok := v.(map[string]any)
	if !ok {
		return nil, []string{fmt.Sprintf("i18n is not an object (%T), ignored", v)}
	}
	var out map[string]hub.ToolText
	var warnings []string
	for lang, raw := range langs {
		entry, ok := raw.(map[string]any)
		if !langRe.MatchString(lang) || !ok {
			warnings = append(warnings, fmt.Sprintf("i18n %q is not a language tag with an object, ignored", lang))
			continue
		}
		var text hub.ToolText
		for key, val := range entry {
			switch key {
			case "title", "description":
				s, ok := val.(string)
				if !ok {
					warnings = append(warnings, fmt.Sprintf("i18n %s.%s is not a string, ignored", lang, key))
					continue
				}
				if key == "title" {
					text.Title = s
				} else {
					text.Description = s
				}
			case "properties":
				props, ok := val.(map[string]any)
				if !ok {
					warnings = append(warnings, fmt.Sprintf("i18n %s.properties is not an object, ignored", lang))
					continue
				}
				for name, label := range props {
					s, ok := label.(string)
					if !ok {
						warnings = append(warnings, fmt.Sprintf("i18n %s.properties.%s is not a string, ignored", lang, name))
						continue
					}
					if text.Properties == nil {
						text.Properties = map[string]string{}
					}
					text.Properties[name] = s
				}
			default:
				warnings = append(warnings, fmt.Sprintf("i18n %s.%s is unknown, ignored", lang, key))
			}
		}
		if text.Title == "" && text.Description == "" && len(text.Properties) == 0 {
			continue
		}
		if out == nil {
			out = map[string]hub.ToolText{}
		}
		out[lang] = text
	}
	slices.Sort(warnings) // map order is random; keep the log stable
	return out, warnings
}
