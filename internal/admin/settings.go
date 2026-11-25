package admin

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/devXpro/mcp-gatekeeper/auth"
	"github.com/devXpro/mcp-gatekeeper/core"
	"github.com/devXpro/mcp-gatekeeper/tunnel/cloudflare"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/store"
	"github.com/devXpro/homie/internal/tunnel"
	"github.com/devXpro/homie/internal/wire"
)

// The bootstrap, the settings, the tunnel, the system info and the home overview.

// accessResetShown is how long the overview shows an access reset (review GK-5).
const accessResetShown = 7 * 24 * time.Hour

// homeAccount returns the home's account (Homie has one); ok is false before the first setup made it.
func (s *Server) homeAccount(ctx context.Context) (core.Account, bool, error) {
	list, err := s.st.Accounts().List(ctx)
	if err != nil || len(list) == 0 {
		return core.Account{}, false, err
	}
	return list[0], true, nil
}

// homeName is the account's name, or before the first setup the name that pre-fills it.
func (s *Server) homeName(ctx context.Context, hs store.HomeSettings) (string, error) {
	acc, ok, err := s.homeAccount(ctx)
	switch {
	case err != nil:
		return "", err
	case ok:
		return acc.Name, nil
	}
	return hs.HomeName, nil
}

func (s *Server) tunnelMode() string {
	if s.opts.TunnelMode != nil {
		return s.opts.TunnelMode()
	}
	if s.opts.Tunnel != nil {
		return s.opts.Tunnel.Status().Mode
	}
	return tunnel.ModeOff
}

// getBootstrap is GET /bootstrap (public): the home's name and language, whether the owner can sign in at this URL,
// and the tunnel's mode (quick: the SPA polls the events). Nothing else: no version, no people.
func (s *Server) getBootstrap(q *request) error {
	ctx := q.ctx
	hs, err := s.st.Settings().Get(ctx)
	if err != nil {
		return err
	}
	name, err := s.homeName(ctx, hs)
	if err != nil {
		return err
	}
	ready := false
	if s.opts.OwnerReady != nil {
		if ready, err = s.opts.OwnerReady(ctx); err != nil {
			return err
		}
	}
	return s.writeJSON(q, http.StatusOK, map[string]any{
		"homeName": name, "language": hs.Language, "ownerReady": ready,
		"tunnel": map[string]string{"mode": s.tunnelMode()},
	})
}

type settingsJSON struct {
	HomeName string `json:"homeName"`
	Language string `json:"language"`
}

func (s *Server) settingsOf(ctx context.Context) (settingsJSON, error) {
	hs, err := s.st.Settings().Get(ctx)
	if err != nil {
		return settingsJSON{}, err
	}
	name, err := s.homeName(ctx, hs)
	if err != nil {
		return settingsJSON{}, err
	}
	return settingsJSON{HomeName: name, Language: hs.Language}, nil
}

func (s *Server) getSettings(q *request) error {
	out, err := s.settingsOf(q.ctx)
	if err != nil {
		return err
	}
	return s.writeJSON(q, http.StatusOK, out)
}

// patchSettings is PATCH /settings: the home's name renames the account (one source: the consent screen, the MCP
// title and the instructions follow it at once), the language is the home's default.
func (s *Server) patchSettings(q *request) error {
	var body struct {
		HomeName *string `json:"homeName"`
		Language *string `json:"language"`
	}
	if err := decode(q, &body); err != nil {
		return err
	}
	ctx := q.ctx
	cur, err := s.settingsOf(ctx)
	if err != nil {
		return err
	}
	var name string
	if body.HomeName != nil {
		if name, err = auth.NormalizeName(*body.HomeName); err != nil {
			return invalidField("/homeName", "pattern", err.Error())
		}
	}
	if body.Language != nil && *body.Language != "ru" && *body.Language != "en" {
		return invalidField("/language", "enum", `must be "ru" or "en"`)
	}
	var changed []string
	if body.Language != nil && *body.Language != cur.Language {
		err := s.st.Settings().Update(ctx, func(hs *store.HomeSettings) error {
			hs.Language = *body.Language
			return nil
		})
		if err != nil {
			return err
		}
		changed = append(changed, "language")
	}
	if body.HomeName != nil && name != cur.HomeName {
		if err := s.rename(ctx, name); err != nil {
			return err
		}
		changed = append(changed, "homeName")
	}
	out, err := s.settingsOf(ctx)
	if err != nil {
		return err
	}
	if len(changed) > 0 {
		s.recordAdmin(q, "settings.update", "", map[string]string{"keys": strings.Join(changed, ",")}, nil)
		s.publishSettings(out)
		if out.HomeName != cur.HomeName && s.opts.OnHomeName != nil {
			s.opts.OnHomeName(out.HomeName)
		}
	}
	return s.writeJSON(q, http.StatusOK, out)
}

// rename names the home: the account, or before the first setup the name that will pre-fill it.
func (s *Server) rename(ctx context.Context, name string) error {
	acc, ok, err := s.homeAccount(ctx)
	if err != nil {
		return err
	}
	if ok {
		return s.st.Accounts().Rename(ctx, acc.ID, name)
	}
	return s.st.Settings().Update(ctx, func(hs *store.HomeSettings) error {
		hs.HomeName = name
		return nil
	})
}

func (s *Server) publishSettings(out settingsJSON) {
	data, err := marshal(out)
	if err != nil {
		return
	}
	member, _ := marshal(map[string]string{"homeName": out.HomeName})
	s.events.publishRaw("settings", data, member, "")
}

func (s *Server) tunnelStatus() tunnel.StatusJSON {
	if s.opts.Tunnel == nil {
		return tunnel.Status{Mode: tunnel.ModeOff, State: string(cloudflare.StateStopped)}.JSON()
	}
	return s.opts.Tunnel.Status().JSON()
}

func (s *Server) getTunnel(q *request) error {
	if s.opts.Tunnel == nil {
		return notReady("the tunnel")
	}
	return s.writeJSON(q, http.StatusOK, s.tunnelStatus())
}

// checkTunnel is POST /tunnel/check: the self-check through the tunnel, now.
func (s *Server) checkTunnel(q *request) error {
	if err := decode(q, &struct{}{}); err != nil {
		return err
	}
	if s.opts.Tunnel == nil {
		return notReady("the tunnel")
	}
	out := s.opts.Tunnel.Check(q.ctx).JSON()
	s.events.publish("tunnel", out)
	return s.writeJSON(q, http.StatusOK, out)
}

// reconnectTunnel is POST /tunnel/reconnect (owner, step-up): cloudflared starts again once this answer is out (the
// answer may travel through the tunnel being restarted). A quick tunnel gets a new URL: the owner continues on the
// first-run page of the home box.
func (s *Server) reconnectTunnel(q *request) error {
	if err := decode(q, &struct{}{}); err != nil {
		return err
	}
	if s.opts.Tunnel == nil {
		return notReady("the tunnel")
	}
	out := s.opts.Tunnel.Status().JSON()
	if out.Mode == tunnel.ModeOff {
		return errorf(http.StatusConflict, "conflict", "no tunnel is configured")
	}
	s.recordAdmin(q, "tunnel.reconnect", out.Hostname, map[string]string{"mode": out.Mode}, nil)
	out.State = string(cloudflare.StateStarting)
	if err := s.writeJSON(q, http.StatusAccepted, out); err != nil {
		return err
	}
	_ = http.NewResponseController(q.w).Flush()
	tun, done := s.opts.Tunnel, q.ctx.Done()
	s.wg.Go(func() {
		select {
		case <-done: // the answer went out
		case <-s.ctx.Done():
			return
		}
		if err := tun.Restart(); err != nil {
			s.log.Error("the tunnel did not restart", "err", err)
		}
	})
	return nil
}

type updateInfo struct {
	latest    string
	notesURL  string
	checkedAt time.Time
}

type systemJSON struct {
	Version   string      `json:"version"`
	Commit    string      `json:"commit,omitempty"`
	OS        string      `json:"os"`
	Arch      string      `json:"arch"`
	StartedAt string      `json:"startedAt"`
	Update    *updateJSON `json:"update,omitempty"`
}

func (s *Server) systemInfo() systemJSON {
	out := systemJSON{
		Version: s.opts.Version, Commit: s.opts.Commit, OS: runtime.GOOS, Arch: runtime.GOARCH,
		StartedAt: ts(s.opts.StartedAt),
	}
	s.mu.Lock()
	if u := s.hubUpdate; u != nil && versionLess(s.opts.Version, u.latest) {
		out.Update = &updateJSON{Latest: u.latest, NotesURL: u.notesURL, CheckedAt: ts(u.checkedAt)}
	}
	s.mu.Unlock()
	return out
}

func (s *Server) getSystem(q *request) error {
	return s.writeJSON(q, http.StatusOK, s.systemInfo())
}

func (s *Server) checkHubUpdate(q *request) error {
	if err := decode(q, &struct{}{}); err != nil {
		return err
	}
	if s.opts.Installer == nil {
		return notReady("the updater")
	}
	rel, err := s.opts.Installer.LatestHub(q.ctx)
	if err != nil {
		return upstream(err)
	}
	s.mu.Lock()
	s.hubUpdate = &updateInfo{latest: rel.Version, notesURL: rel.NotesURL, checkedAt: s.now()}
	s.mu.Unlock()
	return s.writeJSON(q, http.StatusOK, s.systemInfo())
}

func (s *Server) updateHub(q *request) error {
	var body struct {
		Version        string `json:"version"`
		AllowDowngrade bool   `json:"allowDowngrade"`
	}
	if err := decode(q, &body); err != nil {
		return err
	}
	job, err := s.startHubUpdate(q, body.Version, body.AllowDowngrade)
	if err != nil {
		return err
	}
	return s.writeJSON(q, http.StatusAccepted, s.jobSnapshot(job))
}

// startHubUpdate starts the hub.update job (the web and the admin socket's `update`).
func (s *Server) startHubUpdate(q *request, version string, allowDowngrade bool) (*jobEntry, error) {
	if err := checkDowngrade(q, allowDowngrade, version, s.opts.Version); err != nil {
		return nil, err
	}
	if s.opts.Installer == nil {
		return nil, notReady("the updater")
	}
	inst := s.opts.Installer
	return s.startJob(q, "hub.update", "", func(ctx context.Context, report Reporter) (string, error) {
		return "", inst.UpdateHub(ctx, version, allowDowngrade, report)
	})
}

type attentionJSON struct {
	Kind    string `json:"kind"`
	Target  string `json:"target,omitempty"`
	Version string `json:"version,omitempty"`
	At      string `json:"at,omitempty"`
	Peer    string `json:"peer,omitempty"`
}

// AccessReset records that reset-access ran on the admin socket (review GK-5: on a desktop any program of the user
// reaches the socket, so the owner is told): the overview shows it for seven days, and an "attention" event says it
// now. by names the program that asked, when the socket knew it.
func (s *Server) AccessReset(ctx context.Context, at time.Time, by string) {
	err := s.st.Settings().Update(ctx, func(hs *store.HomeSettings) error {
		hs.AccessResetAt, hs.AccessResetBy = at, by
		return nil
	})
	if err != nil {
		s.log.Error("cannot store the access reset", "err", err)
	}
	s.events.publish("attention", attentionJSON{Kind: "access_reset", At: ts(at), Peer: by})
}

func (s *Server) getOverview(q *request) error {
	ctx := q.ctx
	hs, err := s.st.Settings().Get(ctx)
	if err != nil {
		return err
	}
	name, err := s.homeName(ctx, hs)
	if err != nil {
		return err
	}
	plugins, err := s.pluginSummaries(ctx)
	if err != nil {
		return err
	}
	tun := s.tunnelStatus()
	attention := []attentionJSON{}
	if !hs.AccessResetAt.IsZero() && s.now().Sub(hs.AccessResetAt) < accessResetShown {
		attention = append(attention, attentionJSON{Kind: "access_reset", At: ts(hs.AccessResetAt), Peer: hs.AccessResetBy})
	}
	switch {
	case tun.State == string(cloudflare.StateFailing):
		attention = append(attention, attentionJSON{Kind: "tunnel_offline"})
	case tun.State == string(cloudflare.StateProbing) && tun.LastError != "":
		attention = append(attention, attentionJSON{Kind: "tunnel_unreachable"})
	}
	for _, p := range plugins {
		if p.State == string(hub.PluginFailing) {
			attention = append(attention, attentionJSON{Kind: "plugin_failing", Target: p.ID})
		}
	}
	if sys := s.systemInfo(); sys.Update != nil {
		attention = append(attention, attentionJSON{Kind: "hub_update", Version: sys.Update.Latest})
	}
	for _, p := range plugins {
		if p.Update != nil {
			attention = append(attention, attentionJSON{Kind: "plugin_update", Target: p.ID, Version: p.Update.Latest})
		}
	}
	if tun.Mode == tunnel.ModeQuick {
		attention = append(attention, attentionJSON{Kind: "tunnel_quick"})
	}
	recs, err := s.st.Activity().Query(ctx, store.ActivityFilter{Via: []string{"chatgpt", "claude"}, Limit: 5})
	if err != nil {
		return err
	}
	titles := s.toolTitles()
	recent := make([]activityItemJSON, 0, len(recs))
	for _, rec := range recs {
		recent = append(recent, toActivityItem(rec, titles))
	}
	return s.writeJSON(q, http.StatusOK, map[string]any{
		"home":      map[string]string{"name": name},
		"tunnel":    tun,
		"plugins":   plugins,
		"attention": attention,
		"recent":    recent,
	})
}

// upstream turns an installer failure into 502 upstream (a job error with its own code stays as it is).
func upstream(err error) error {
	if je, ok := errors.AsType[*JobError](err); ok && je.Code != "upstream" {
		return err
	}
	return wire.Errorf(http.StatusBadGateway, "upstream", "GitHub did not answer as expected: "+err.Error())
}
