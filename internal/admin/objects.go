package admin

import (
	"encoding/json"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/store"
)

// The wire objects of ADMIN-API §2. Optional keys are omitted, never null.

const roleOwner = store.RoleOwner

type personJSON struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Role         string   `json:"role"`
	Plugins      []string `json:"plugins"`
	Language     string   `json:"language,omitempty"`
	CreatedAt    string   `json:"createdAt"`
	Passkeys     int      `json:"passkeys"`
	LastActiveAt *string  `json:"lastActiveAt,omitempty"`
}

type sessionJSON struct {
	ID         string `json:"id"`
	Device     string `json:"device"`
	CreatedAt  string `json:"createdAt"`
	LastSeenAt string `json:"lastSeenAt"`
	Current    bool   `json:"current"`
}

type passkeyJSON struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Provider   string  `json:"provider,omitempty"`
	CreatedAt  string  `json:"createdAt"`
	LastUsedAt *string `json:"lastUsedAt,omitempty"`
	Current    bool    `json:"current"`
}

type keyJSON struct {
	ID           string  `json:"id"`
	PersonID     string  `json:"personId"`
	Label        string  `json:"label"`
	Hint         string  `json:"hint,omitempty"`
	ConnectionID string  `json:"connectionId,omitempty"`
	CreatedAt    string  `json:"createdAt"`
	LastUsedAt   *string `json:"lastUsedAt,omitempty"`
}

type selfCheckJSON struct {
	OK        bool   `json:"ok"`
	CheckedAt string `json:"checkedAt"`
	Error     string `json:"error,omitempty"`
}

type relayJSON struct {
	Configured bool           `json:"configured"`
	Domain     string         `json:"domain,omitempty"`
	PublicURL  string         `json:"publicUrl,omitempty"`
	State      string         `json:"state"`
	Since      *string        `json:"since,omitempty"`
	LastError  string         `json:"lastError,omitempty"`
	SelfCheck  *selfCheckJSON `json:"selfCheck,omitempty"`
}

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

type auditPersonJSON struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

type auditItemJSON struct {
	ID         string           `json:"id"`
	At         string           `json:"at"`
	Kind       string           `json:"kind"`
	Person     *auditPersonJSON `json:"person,omitempty"`
	Via        string           `json:"via"`
	Tool       string           `json:"tool,omitempty"`
	ToolTitle  string           `json:"toolTitle,omitempty"`
	PluginID   string           `json:"pluginId,omitempty"`
	Action     string           `json:"action,omitempty"`
	Target     string           `json:"target,omitempty"`
	Args       json.RawMessage  `json:"args,omitempty"`
	OK         bool             `json:"ok"`
	Error      string           `json:"error,omitempty"`
	DurationMs int64            `json:"durationMs"`
}

type settingsJSON struct {
	HomeName       string `json:"homeName"`
	Language       string `json:"language"`
	ConnectionKind string `json:"connectionKind,omitempty"`
	SiteSignOut    *bool  `json:"siteSignOut,omitempty"`
}

func toPerson(p store.Person, passkeys int, lastActive time.Time) personJSON {
	plugins := p.Plugins
	if plugins == nil {
		plugins = []string{}
	}
	return personJSON{
		ID: p.ID, Name: p.Name, Role: string(p.Role), Plugins: plugins, Language: p.Language,
		CreatedAt: ts(p.CreatedAt), Passkeys: passkeys, LastActiveAt: optTS(lastActive),
	}
}

func toSession(s store.Session, currentID string) sessionJSON {
	return sessionJSON{
		ID: s.ID, Device: s.Device, CreatedAt: ts(s.CreatedAt), LastSeenAt: ts(s.LastSeenAt), Current: s.ID == currentID,
	}
}

func toPasskey(pk store.Passkey, currentID string) passkeyJSON {
	return passkeyJSON{
		ID: pk.ID, Name: pk.Name, Provider: pk.Provider, CreatedAt: ts(pk.CreatedAt),
		LastUsedAt: optTS(pk.LastUsedAt), Current: pk.ID == currentID,
	}
}

func toKey(k store.Key) keyJSON {
	return keyJSON{
		ID: k.ID, PersonID: k.PersonID, Label: k.Label, Hint: k.Hint, ConnectionID: k.ConnectionID,
		CreatedAt: ts(k.CreatedAt), LastUsedAt: optTS(k.LastUsedAt),
	}
}

func toSettings(hs store.HomeSettings) settingsJSON {
	out := settingsJSON{HomeName: hs.HomeName, Language: hs.Language, ConnectionKind: string(hs.ConnectionKind)}
	if hs.ConnectionKind != "" { // [P3] keys appear once T3.0 sets connectionKind
		v := hs.SiteSignOut
		out.SiteSignOut = &v
	}
	return out
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

func toAuditItem(rec store.AuditRecord, titles map[string]string) auditItemJSON {
	out := auditItemJSON{
		ID: strconv.FormatInt(rec.ID, 10), At: ts(rec.At), Kind: string(rec.Kind), Via: rec.Via,
		Tool: rec.Tool, PluginID: rec.PluginID, Action: rec.Action, Target: rec.Target,
		OK: rec.OK, Error: rec.Error, DurationMs: rec.Duration.Milliseconds(),
	}
	if rec.Person != "" || rec.PersonID != "" {
		out.Person = &auditPersonJSON{ID: rec.PersonID, Name: rec.Person}
	}
	if rec.Tool != "" {
		out.ToolTitle = titles[rec.Tool]
	}
	if len(rec.Args) > 0 && json.Valid(rec.Args) && string(rec.Args) != "null" {
		out.Args = rec.Args
	}
	return out
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
