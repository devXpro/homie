package admin

import (
	"encoding/json"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/store"
)

// The wire objects of the plugin and tool endpoints. Optional keys are omitted, never null.

type sourceJSON struct {
	Kind     string `json:"kind"`
	Repo     string `json:"repo,omitempty"`
	Homepage string `json:"homepage,omitempty"`
}

type updateJSON struct {
	Latest    string `json:"latest"`
	NotesURL  string `json:"notesUrl,omitempty"`
	CheckedAt string `json:"checkedAt"`
}

type toolCounts struct {
	Assistant int `json:"assistant"`
	Admin     int `json:"admin"`
}

type pluginSummaryJSON struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Title         string      `json:"title"`
	Description   string      `json:"description,omitempty"`
	Version       string      `json:"version"`
	State         string      `json:"state"`
	Since         *string     `json:"since,omitempty"`
	LastError     string      `json:"lastError,omitempty"`
	Enabled       bool        `json:"enabled"`
	Source        sourceJSON  `json:"source"`
	Update        *updateJSON `json:"update,omitempty"`
	Tools         toolCounts  `json:"tools"`
	InventoryTool string      `json:"inventoryTool,omitempty"`
	Devices       *int        `json:"devices,omitempty"`
}

type pluginDetailJSON struct {
	pluginSummaryJSON
	Instructions    string     `json:"instructions,omitempty"`
	PreviousVersion string     `json:"previousVersion,omitempty"`
	Command         []string   `json:"command,omitempty"`
	Tools           []toolJSON `json:"tools"`
}

type toolTextJSON struct {
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description,omitempty"`
	Properties  map[string]string `json:"properties,omitempty"`
}

type toolJSON struct {
	Name         string                  `json:"name"`
	PluginID     string                  `json:"pluginId"`
	Title        string                  `json:"title"`
	Description  string                  `json:"description"`
	Audience     string                  `json:"audience"`
	InputSchema  json.RawMessage         `json:"inputSchema"`
	OutputSchema json.RawMessage         `json:"outputSchema,omitempty"`
	Annotations  json.RawMessage         `json:"annotations,omitempty"`
	TimeoutMs    int64                   `json:"timeoutMs"`
	Inventory    bool                    `json:"inventory"`
	Target       string                  `json:"target,omitempty"`
	I18n         map[string]toolTextJSON `json:"i18n,omitempty"`
}

type inventoryItemJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Subtitle string `json:"subtitle,omitempty"`
	State    string `json:"state,omitempty"`
}

func toTool(t hub.Tool) toolJSON {
	out := toolJSON{
		Name: t.Name, PluginID: t.PluginID, Title: t.Title, Description: t.Description,
		Audience: string(t.Audience), InputSchema: t.InputSchema, TimeoutMs: t.Timeout.Milliseconds(),
		Inventory: t.Inventory, Target: toolTarget(t),
	}
	if out.Title == "" {
		out.Title = t.Name
	}
	if out.Audience == "" {
		out.Audience = string(hub.AudienceAssistant)
	}
	if len(out.InputSchema) == 0 {
		out.InputSchema = json.RawMessage(`{"type":"object"}`)
	}
	if len(t.OutputSchema) > 0 {
		out.OutputSchema = t.OutputSchema
	}
	if len(t.Annotations) > 0 {
		out.Annotations = t.Annotations
	}
	if len(t.I18n) > 0 {
		out.I18n = map[string]toolTextJSON{}
		for lang, tt := range t.I18n {
			out.I18n[lang] = toolTextJSON{Title: tt.Title, Description: tt.Description, Properties: tt.Properties}
		}
	}
	return out
}

// toolTarget names the input property that holds an inventory item id (ADMIN-API §2, Tool): the property marked
// "x-homie-ref": "inventory", else a required string property named like the plugin id, "device" or "item".
func toolTarget(t hub.Tool) string {
	var schema struct {
		Properties map[string]struct {
			Type any    `json:"type"`
			Ref  string `json:"x-homie-ref"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if json.Unmarshal(t.InputSchema, &schema) != nil {
		return ""
	}
	var marked []string
	for name, p := range schema.Properties {
		if p.Ref == "inventory" {
			marked = append(marked, name)
		}
	}
	if len(marked) > 0 {
		return slices.Min(marked)
	}
	for _, name := range []string{t.PluginID, "device", "item"} {
		p, ok := schema.Properties[name]
		if ok && p.Type == "string" && slices.Contains(schema.Required, name) {
			return name
		}
	}
	return ""
}

// pluginName is the plugin's binary or repository name, e.g. "homie-tv".
func pluginName(p store.Plugin) string {
	if p.Repo != "" {
		return path.Base(p.Repo)
	}
	if len(p.Command) > 0 {
		return path.Base(strings.ReplaceAll(p.Command[0], "\\", "/"))
	}
	return p.ID
}

// versionLess compares dotted numeric versions ("0.2.10" < "0.3.0"), ignoring a leading "v" and any pre-release
// suffix; non-numeric parts compare as strings.
func versionLess(a, b string) bool {
	pa := strings.Split(strings.TrimPrefix(cutSuffix(a), "v"), ".")
	pb := strings.Split(strings.TrimPrefix(cutSuffix(b), "v"), ".")
	for i := range max(len(pa), len(pb)) {
		var x, y string
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x == y {
			continue
		}
		nx, ex := strconv.Atoi(x)
		ny, ey := strconv.Atoi(y)
		if ex == nil && ey == nil {
			return nx < ny
		}
		return x < y
	}
	return false
}

func cutSuffix(v string) string {
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		return v[:i]
	}
	return v
}
