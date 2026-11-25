package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/devXpro/mcp-gatekeeper/core"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/store"
)

// Plugins and tools (ADMIN-API §3.5), without the calls (calls.go) and the jobs (jobs.go).

var (
	pluginIDRe = regexp.MustCompile(`^[a-z][a-z0-9]{1,15}$`)
	repoRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$`)
)

type inventoryCache struct {
	items     []inventoryItemJSON
	fetchedAt time.Time
}

// pluginView joins the stored plugin with the host's status. Plugins the host runs without a store row (phase 1
// config before the import) count as local and enabled.
type pluginView struct {
	row    store.Plugin
	status hub.PluginStatus
	stored bool
	known  bool // the host reports it
}

func (s *Server) pluginViews(ctx context.Context) ([]pluginView, error) {
	rows, err := s.st.Plugins().List(ctx)
	if err != nil {
		return nil, err
	}
	statuses := map[string]hub.PluginStatus{}
	for _, st := range s.host.Status() {
		statuses[st.ID] = st
	}
	var out []pluginView
	for _, row := range rows {
		st, known := statuses[row.ID]
		out = append(out, pluginView{row: row, status: st, stored: true, known: known})
		delete(statuses, row.ID)
	}
	for id, st := range statuses {
		out = append(out, pluginView{row: store.Plugin{ID: id, Source: store.PluginLocal, Enabled: true}, status: st, known: true})
	}
	slices.SortFunc(out, func(a, b pluginView) int { return strings.Compare(a.row.ID, b.row.ID) })
	return out, nil
}

func (s *Server) pluginView(ctx context.Context, id string) (pluginView, error) {
	views, err := s.pluginViews(ctx)
	if err != nil {
		return pluginView{}, err
	}
	for _, v := range views {
		if v.row.ID == id {
			return v, nil
		}
	}
	return pluginView{}, notFound("plugin")
}

func (s *Server) summary(v pluginView, tools []hub.Tool) pluginSummaryJSON {
	p := v.row
	out := pluginSummaryJSON{
		ID: p.ID, Name: pluginName(p), Title: v.status.Title, Version: v.status.Version,
		State: string(v.status.State), Since: optTS(v.status.Since), LastError: v.status.LastError, Enabled: p.Enabled,
		Source: sourceJSON{Kind: string(p.Source), Repo: p.Repo},
	}
	if out.Title == "" {
		out.Title = p.ID
	}
	if out.Version == "" {
		out.Version = p.Version
	}
	if out.State == "" {
		out.State = string(hub.PluginStopped)
	}
	if !p.Enabled {
		out.State = "disabled"
	}
	if out.Source.Kind == "" {
		out.Source.Kind = string(store.PluginLocal)
	}
	if p.Source == store.PluginGitHub && p.Repo != "" {
		out.Source.Homepage = "https://github.com/" + p.Repo
	}
	for _, t := range tools {
		if t.PluginID != p.ID {
			continue
		}
		if t.Audience == hub.AudienceAdmin {
			out.Tools.Admin++
		} else {
			out.Tools.Assistant++
		}
		if t.Inventory && out.InventoryTool == "" {
			out.InventoryTool = t.Name
		}
	}
	s.mu.Lock()
	if u, ok := s.updates[p.ID]; ok && u.latest != "" && versionLess(out.Version, u.latest) {
		out.Update = &updateJSON{Latest: u.latest, CheckedAt: ts(u.checkedAt)}
	}
	if inv, ok := s.inventory[p.ID]; ok {
		n := len(inv.items)
		out.Devices = &n
	}
	s.mu.Unlock()
	return out
}

func (s *Server) pluginSummaries(ctx context.Context) ([]pluginSummaryJSON, error) {
	views, err := s.pluginViews(ctx)
	if err != nil {
		return nil, err
	}
	tools := s.host.Tools(hub.AudienceAdmin)
	out := make([]pluginSummaryJSON, 0, len(views))
	for _, v := range views {
		out = append(out, s.summary(v, tools))
	}
	return out, nil
}

func (s *Server) listPlugins(q *request) error {
	items, err := s.pluginSummaries(q.ctx)
	if err != nil {
		return err
	}
	return s.writeJSON(q, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getPlugin(q *request) error {
	v, err := s.pluginView(q.ctx, q.r.PathValue("id"))
	if err != nil {
		return err
	}
	all := s.host.Tools(hub.AudienceAdmin)
	out := pluginDetailJSON{pluginSummaryJSON: s.summary(v, all), PreviousVersion: v.row.PreviousVersion, Tools: []toolJSON{}}
	if q.local {
		out.Command = v.row.Command // a local path; the web does not need it
	}
	if ih, ok := s.host.(interface{ PluginInstructions(id string) string }); ok {
		out.Instructions = ih.PluginInstructions(v.row.ID)
	}
	for _, t := range all {
		if t.PluginID == v.row.ID {
			out.Tools = append(out.Tools, toTool(t))
		}
	}
	return s.writeJSON(q, http.StatusOK, out)
}

func (s *Server) patchPlugin(q *request) error {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decode(q, &body); err != nil {
		return err
	}
	if body.Enabled == nil {
		return invalidField("/enabled", "required", "is required")
	}
	ctx := q.ctx
	id := q.r.PathValue("id")
	row, err := s.st.Plugins().Get(ctx, id)
	if errors.Is(err, core.ErrNotFound) {
		return notFound("plugin")
	}
	if err != nil {
		return err
	}
	if row.Enabled != *body.Enabled {
		row.Enabled = *body.Enabled
		if err := s.st.Plugins().Update(ctx, row); err != nil {
			return err
		}
		var applyErr error
		if s.opts.Runtime != nil {
			applyErr = s.opts.Runtime.Apply(ctx)
		}
		action := "plugin.disable"
		if row.Enabled {
			action = "plugin.enable"
		}
		s.recordAdmin(q, action, id, nil, applyErr)
		if applyErr != nil {
			return applyErr
		}
	}
	v, err := s.pluginView(ctx, id)
	if err != nil {
		return err
	}
	return s.writeJSON(q, http.StatusOK, s.summary(v, s.host.Tools(hub.AudienceAdmin)))
}

func (s *Server) restartPlugin(q *request) error {
	if err := decode(q, &struct{}{}); err != nil {
		return err
	}
	ctx := q.ctx
	id := q.r.PathValue("id")
	v, err := s.pluginView(ctx, id)
	if err != nil {
		return err
	}
	if s.opts.Runtime == nil {
		return notReady("plugin restart")
	}
	err = s.opts.Runtime.Restart(ctx, id)
	s.recordAdmin(q, "plugin.restart", id, nil, err)
	if err != nil {
		return err
	}
	out := s.summary(v, s.host.Tools(hub.AudienceAdmin))
	if v.row.Enabled {
		out.State = string(hub.PluginStarting)
	}
	return s.writeJSON(q, http.StatusAccepted, out)
}

func (s *Server) pluginLogs(q *request) error {
	if _, err := s.pluginView(q.ctx, q.r.PathValue("id")); err != nil {
		return err
	}
	tail := 200
	if raw := q.r.URL.Query().Get("tail"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1000 {
			return invalidField("/tail", "range", "must be 1 to 1000")
		}
		tail = n
	}
	lines := s.host.Logs(q.r.PathValue("id"))
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	if lines == nil {
		lines = []string{}
	}
	return s.writeJSON(q, http.StatusOK, map[string]any{"lines": lines})
}

func (s *Server) listTools(q *request) error {
	v := q.r.URL.Query()
	aud := hub.Audience(v.Get("audience"))
	switch aud {
	case "":
		aud = hub.AudienceAdmin
	case hub.AudienceAdmin, hub.AudienceAssistant:
	default:
		return invalidField("/audience", "enum", `must be "admin" or "assistant"`)
	}
	plugin := v.Get("plugin")
	items := []toolJSON{}
	for _, t := range s.host.Tools(aud) {
		if plugin == "" || t.PluginID == plugin {
			items = append(items, toTool(t))
		}
	}
	return s.writeJSON(q, http.StatusOK, map[string]any{"items": items})
}

// inventoryTool returns the plugin's inventory tool.
func (s *Server) inventoryTool(ctx context.Context, id string) (hub.Tool, error) {
	if _, err := s.pluginView(ctx, id); err != nil {
		return hub.Tool{}, err
	}
	for _, t := range s.host.Tools(hub.AudienceAdmin) {
		if t.PluginID == id && t.Inventory {
			return t, nil
		}
	}
	return hub.Tool{}, errorf(http.StatusNotFound, "no_inventory", "the plugin has no inventory tool")
}

var inventoryStates = []string{"on", "off", "offline", "unknown", "paired", "unpaired"}

func (s *Server) getInventory(q *request) error {
	ctx := q.ctx
	id := q.r.PathValue("id")
	tool, err := s.inventoryTool(ctx, id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	cached, ok := s.inventory[id]
	s.mu.Unlock()
	if ok && s.now().Sub(cached.fetchedAt) < s.inventoryTTL {
		return s.writeJSON(q, http.StatusOK, map[string]any{"items": cached.items, "fetchedAt": ts(cached.fetchedAt)})
	}
	res, err := s.invoke(ctx, s.caller(q), tool, json.RawMessage(`{}`))
	switch {
	case errors.Is(err, hub.ErrPluginDown):
		return errorf(http.StatusServiceUnavailable, "plugin_unavailable", "the plugin is not running")
	case errors.Is(err, context.DeadlineExceeded):
		return errorf(http.StatusGatewayTimeout, "timeout", "the plugin did not answer in time")
	case errors.Is(err, hub.ErrUnknownTool):
		return errorf(http.StatusNotFound, "no_inventory", "the plugin has no inventory tool")
	case err != nil:
		return err
	case res.IsError:
		return errorf(http.StatusUnprocessableEntity, "tool_failed", failureText(res))
	}
	items, err := parseInventory(res)
	if err != nil {
		return errorf(http.StatusUnprocessableEntity, "tool_failed", "the inventory tool answered in an unknown shape")
	}
	fetched := s.now()
	s.mu.Lock()
	s.inventory[id] = inventoryCache{items: items, fetchedAt: fetched}
	s.mu.Unlock()
	return s.writeJSON(q, http.StatusOK, map[string]any{"items": items, "fetchedAt": ts(fetched)})
}

// parseInventory reads {"items":[{"id","name","subtitle"?,"state"?}]} from the structured result, else the text.
func parseInventory(res hub.Result) ([]inventoryItemJSON, error) {
	raw := res.Structured
	if len(raw) == 0 {
		raw = json.RawMessage(res.Text)
	}
	var body struct {
		Items []inventoryItemJSON `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	items := make([]inventoryItemJSON, 0, len(body.Items))
	for _, it := range body.Items {
		if it.ID == "" {
			continue
		}
		if it.Name == "" {
			it.Name = it.ID
		}
		if it.State != "" && !slices.Contains(inventoryStates, it.State) {
			it.State = "unknown"
		}
		items = append(items, it)
	}
	return items, nil
}

func failureText(res hub.Result) string {
	if res.Text != "" {
		return res.Text
	}
	return "The tool failed without saying why."
}

func (s *Server) previewPlugin(q *request) error {
	if s.opts.Installer == nil {
		return notReady("the plugin installer")
	}
	v := q.r.URL.Query()
	repo := v.Get("repo")
	if !repoRe.MatchString(repo) {
		return invalidField("/repo", "pattern", `must look like "owner/name"`)
	}
	version := v.Get("version")
	if version == "" {
		version = "latest"
	}
	ctx := q.ctx
	pv, err := s.opts.Installer.Preview(ctx, repo, version)
	if errors.Is(err, ErrNotFound) {
		return notFound("repository or release")
	}
	if err != nil {
		return upstream(err)
	}
	if row, err := s.st.Plugins().Get(ctx, pv.ID); err == nil {
		pv.Installed = &struct {
			Version string `json:"version"`
		}{Version: row.Version}
	}
	pv.Homepage = safeHomepage(pv.Homepage)
	return s.writeJSON(q, http.StatusOK, pv)
}

// safeHomepage keeps a release's homepage only when it is an absolute https URL with a host and no credentials. The
// manifest is the plugin author's text: the admin shows it as a link of its own, never as the repo's.
func safeHomepage(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return ""
	}
	return u.String()
}

func (s *Server) checkUpdates(q *request) error {
	if err := decode(q, &struct{}{}); err != nil {
		return err
	}
	if s.opts.Installer == nil {
		return notReady("the plugin installer")
	}
	ctx := q.ctx
	rows, err := s.st.Plugins().List(ctx)
	if err != nil {
		return err
	}
	var failed error
	checked := 0
	for _, row := range rows {
		if row.Source != store.PluginGitHub || row.Repo == "" {
			continue
		}
		latest, err := s.opts.Installer.LatestVersion(ctx, row.Repo)
		if err != nil {
			failed = err
			continue
		}
		checked++
		s.mu.Lock()
		s.updates[row.ID] = updateInfo{latest: latest, checkedAt: s.now()}
		s.mu.Unlock()
	}
	if failed != nil && checked == 0 {
		return upstream(failed)
	}
	items, err := s.pluginSummaries(ctx)
	if err != nil {
		return err
	}
	for _, it := range items {
		data, _ := marshal(it)
		s.events.publishRaw("plugin", data, nil, "")
	}
	return s.writeJSON(q, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) installPlugin(q *request) error {
	var body struct {
		Source  string `json:"source"`
		Repo    string `json:"repo"`
		Version string `json:"version"`
		ID      string `json:"id"`
	}
	if err := decode(q, &body); err != nil {
		return err
	}
	if body.Source != "" && body.Source != "github" {
		// A plugin from this machine is added on the home box: homed plugin add.
		return invalidField("/source", "enum", `must be "github"`)
	}
	if !repoRe.MatchString(body.Repo) {
		return invalidField("/repo", "pattern", `must look like "owner/name"`)
	}
	if body.ID != "" && !pluginIDRe.MatchString(body.ID) {
		return invalidField("/id", "pattern", "must be 2 to 16 lowercase letters and digits, starting with a letter")
	}
	if s.opts.Installer == nil {
		return notReady("the plugin installer")
	}
	ctx := q.ctx
	if body.ID != "" {
		if _, err := s.st.Plugins().Get(ctx, body.ID); err == nil {
			return errorf(http.StatusConflict, "conflict", "a plugin with this id is installed")
		} else if !errors.Is(err, core.ErrNotFound) {
			return err
		}
	}
	target := body.ID
	if target == "" {
		target = body.Repo
	}
	inst := s.opts.Installer
	job, err := s.startJob(q, "plugin.install", target, func(ctx context.Context, report Reporter) (string, error) {
		id, err := inst.Install(ctx, body.Repo, body.Version, body.ID, report)
		return id, err
	})
	if err != nil {
		return err
	}
	return s.writeJSON(q, http.StatusAccepted, s.jobSnapshot(job))
}

// addLocal adds a plugin from this machine (the admin socket's `plugin add`): command is its executable, an absolute
// path, and its arguments. A running homed starts it at once.
func (s *Server) addLocal(q *request, id string, command []string) (pluginSummaryJSON, error) {
	if !pluginIDRe.MatchString(id) {
		return pluginSummaryJSON{}, invalidField("/id", "pattern", "must be 2 to 16 lowercase letters and digits, starting with a letter")
	}
	if len(command) == 0 || !strings.HasPrefix(command[0], "/") {
		return pluginSummaryJSON{}, invalidField("/command", "format", "must start with an absolute path")
	}
	ctx := q.ctx
	row := store.Plugin{ID: id, Source: store.PluginLocal, Command: command, Enabled: true, InstalledAt: s.now()}
	err := s.st.Plugins().Create(ctx, row)
	if errors.Is(err, core.ErrConflict) {
		return pluginSummaryJSON{}, errorf(http.StatusConflict, "conflict", "a plugin with this id is installed")
	}
	if err != nil {
		return pluginSummaryJSON{}, err
	}
	var applyErr error
	if s.opts.Runtime != nil {
		applyErr = s.opts.Runtime.Apply(ctx)
	}
	s.recordAdmin(q, "plugin.install", id, map[string]string{"source": "local"}, applyErr)
	if applyErr != nil {
		return pluginSummaryJSON{}, applyErr
	}
	v, err := s.pluginView(ctx, id)
	if err != nil {
		return pluginSummaryJSON{}, err
	}
	return s.summary(v, s.host.Tools(hub.AudienceAdmin)), nil
}

// releaseRe matches a release version (as opposed to a development build such as "dev").
var releaseRe = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+([-+].*)?$`)

// checkDowngrade applies the downgrade rule to an update request: allowDowngrade is honoured only on the socket, and
// without it an explicit version older than the installed one is refused at once (the installer refuses an older
// "latest" in the job too).
func checkDowngrade(q *request, allow bool, version, installed string) error {
	if allow && !q.local {
		return errorf(http.StatusForbidden, "forbidden", "a downgrade is allowed only on the home box (the local socket)")
	}
	if !allow && version != "" && version != "latest" && releaseRe.MatchString(installed) && versionLess(version, installed) {
		return errorf(http.StatusConflict, "downgrade",
			"the version is older than the installed one; downgrades are done on the home box")
	}
	return nil
}

func (s *Server) updatePlugin(q *request) error {
	var body struct {
		Version        string `json:"version"`
		AllowDowngrade bool   `json:"allowDowngrade"`
	}
	if err := decode(q, &body); err != nil {
		return err
	}
	ctx := q.ctx
	id := q.r.PathValue("id")
	row, err := s.st.Plugins().Get(ctx, id)
	if errors.Is(err, core.ErrNotFound) {
		return notFound("plugin")
	}
	if err != nil {
		return err
	}
	if row.Source != store.PluginGitHub {
		return errorf(http.StatusConflict, "conflict", "a local plugin is updated on the home box")
	}
	if err := checkDowngrade(q, body.AllowDowngrade, body.Version, row.Version); err != nil {
		return err
	}
	if s.opts.Installer == nil {
		return notReady("the plugin installer")
	}
	inst := s.opts.Installer
	job, err := s.startJob(q, "plugin.update", id, func(ctx context.Context, report Reporter) (string, error) {
		return id, inst.Update(ctx, id, body.Version, body.AllowDowngrade, report)
	})
	if err != nil {
		return err
	}
	return s.writeJSON(q, http.StatusAccepted, s.jobSnapshot(job))
}

func (s *Server) removePlugin(q *request) error {
	ctx := q.ctx
	id := q.r.PathValue("id")
	if _, err := s.pluginView(ctx, id); err != nil {
		return err
	}
	keepData := false
	if raw := q.r.URL.Query().Get("keepData"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return invalidField("/keepData", "type", "must be true or false")
		}
		keepData = v
	}
	if s.opts.Installer == nil {
		return notReady("the plugin installer")
	}
	inst := s.opts.Installer
	job, err := s.startJob(q, "plugin.remove", id, func(ctx context.Context, report Reporter) (string, error) {
		return id, inst.Remove(ctx, id, keepData, report)
	})
	if err != nil {
		return err
	}
	return s.writeJSON(q, http.StatusAccepted, s.jobSnapshot(job))
}
