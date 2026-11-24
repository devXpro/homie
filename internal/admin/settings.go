package admin

import (
	"context"
	"net/http"
	"runtime"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/relay"
	"github.com/devXpro/homie/internal/store"
)

// Settings, relay, system and the home overview (ADMIN-API §3.4, §3.9).

const connectingGrace = 30_000 // ms: a disconnected relay counts as "connecting" this long after its last change

func (s *Server) getSettings(q *request) error {
	hs, err := s.st.Settings().Get(q.r.Context())
	if err != nil {
		return err
	}
	return s.writeJSON(q, http.StatusOK, toSettings(hs))
}

func (s *Server) patchSettings(q *request) error {
	var body struct {
		HomeName    *string `json:"homeName"`
		Language    *string `json:"language"`
		SiteSignOut *bool   `json:"siteSignOut"`
	}
	if err := decode(q, &body); err != nil {
		return err
	}
	ctx := q.r.Context()
	hs, err := s.st.Settings().Get(ctx)
	if err != nil {
		return err
	}
	var changed []string
	if body.HomeName != nil {
		name, err := checkName("/homeName", *body.HomeName)
		if err != nil {
			return err
		}
		if name != hs.HomeName {
			hs.HomeName = name
			changed = append(changed, "homeName")
		}
	}
	if body.Language != nil {
		if err := checkLanguage("/language", *body.Language); err != nil {
			return err
		}
		if *body.Language != hs.Language {
			hs.Language = *body.Language
			changed = append(changed, "language")
		}
	}
	if body.SiteSignOut != nil && *body.SiteSignOut != hs.SiteSignOut {
		hs.SiteSignOut = *body.SiteSignOut
		changed = append(changed, "siteSignOut")
	}
	if len(changed) > 0 {
		if err := s.st.Settings().Put(ctx, hs); err != nil {
			return err
		}
		s.recordAdmin(q, "settings.update", "", "", map[string]any{"changed": changed}, nil)
		s.publishSettings(hs)
		if s.opts.OnSettings != nil {
			s.opts.OnSettings(hs)
		}
	}
	return s.writeJSON(q, http.StatusOK, toSettings(hs))
}

func (s *Server) publishSettings(hs store.HomeSettings) {
	data, err := marshal(toSettings(hs))
	if err != nil {
		return
	}
	member, _ := marshal(map[string]string{"homeName": hs.HomeName})
	s.events.publishRaw("settings", data, member)
}

// relayStatus derives the RelayStatus object (ADMIN-API §2).
func (s *Server) relayStatus() relayJSON {
	r := s.opts.Relay
	if r == nil || r.Domain() == "" {
		return relayJSON{Configured: false, State: "not_configured"}
	}
	st := r.Status()
	out := relayJSON{
		Configured: true, Domain: r.Domain(), PublicURL: "https://" + r.Domain(),
		Since: optTS(st.Since), LastError: st.LastError,
	}
	switch {
	case st.Connected:
		out.State = "connected"
	case st.LastError == "" || !st.Since.IsZero() && s.now().Sub(st.Since).Milliseconds() < connectingGrace:
		out.State = "connecting"
	default:
		out.State = "offline"
	}
	if c := r.LastCheck(); !c.CheckedAt.IsZero() {
		out.SelfCheck = &selfCheckJSON{OK: c.OK, CheckedAt: ts(c.CheckedAt), Error: c.Error}
	}
	return out
}

func (s *Server) getRelay(q *request) error {
	if s.opts.Relay == nil {
		return s.writeJSON(q, http.StatusOK, relayJSON{State: "not_configured"})
	}
	return s.writeJSON(q, http.StatusOK, s.relayStatus())
}

func (s *Server) checkRelay(q *request) error {
	if err := decode(q, &struct{}{}); err != nil {
		return err
	}
	if s.opts.Relay == nil || s.opts.Relay.Domain() == "" {
		return s.writeJSON(q, http.StatusOK, relayJSON{State: "not_configured"})
	}
	s.opts.Relay.CheckNow(q.r.Context())
	out := s.relayStatus()
	s.events.publish("relay", out)
	return s.writeJSON(q, http.StatusOK, out)
}

// putRelay applies a connect code. From the web only a code for the current domain is accepted (a token rotation):
// a new domain would change the WebAuthn RP ID and lock every passkey out. The socket accepts any domain.
func (s *Server) putRelay(q *request) error {
	var body struct {
		Code string `json:"code"`
	}
	if err := decode(q, &body); err != nil {
		return err
	}
	code, err := relay.ParseCode(body.Code)
	if err != nil {
		return invalidField("/code", "format", "is not a valid connect code")
	}
	if s.opts.Relay == nil {
		return notReady("the relay client")
	}
	if !q.local && code.Domain != s.opts.Relay.Domain() {
		return errorf(http.StatusConflict, "domain_change_local_only",
			"a new relay domain is set on the home box: homed relay connect, then homed admin reset-access")
	}
	err = s.opts.Relay.Connect(q.r.Context(), code)
	s.recordAdmin(q, "relay.change", code.Domain, "", nil, err)
	if err != nil {
		return err
	}
	out := s.relayStatus()
	if out.State != "connected" {
		out.State = "connecting"
	}
	return s.writeJSON(q, http.StatusAccepted, out)
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
	rel, err := s.opts.Installer.LatestHub(q.r.Context())
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
	if err := checkDowngrade(q, body.AllowDowngrade, body.Version, s.opts.Version); err != nil {
		return err
	}
	if s.opts.Installer == nil {
		return notReady("the updater")
	}
	inst := s.opts.Installer
	job, err := s.startJob(q, "hub.update", "", func(ctx context.Context, report Reporter) (string, error) {
		return "", inst.UpdateHub(ctx, body.Version, body.AllowDowngrade, report)
	})
	if err != nil {
		return err
	}
	return s.writeJSON(q, http.StatusAccepted, job)
}

type attentionJSON struct {
	Kind    string `json:"kind"`
	Target  string `json:"target,omitempty"`
	Version string `json:"version,omitempty"`
}

func (s *Server) getOverview(q *request) error {
	ctx := q.r.Context()
	hs, err := s.st.Settings().Get(ctx)
	if err != nil {
		return err
	}
	plugins, err := s.pluginSummaries(ctx)
	if err != nil {
		return err
	}
	rs := s.relayStatus()
	attention := []attentionJSON{}
	switch {
	case rs.State == "offline":
		attention = append(attention, attentionJSON{Kind: "relay_offline"})
	case rs.State == "connected" && rs.SelfCheck != nil && !rs.SelfCheck.OK:
		attention = append(attention, attentionJSON{Kind: "relay_unreachable"})
	}
	for _, p := range plugins {
		if p.State == string(hub.PluginFailing) {
			attention = append(attention, attentionJSON{Kind: "plugin_failing", Target: p.ID})
		}
	}
	for _, p := range plugins {
		if p.Update != nil {
			attention = append(attention, attentionJSON{Kind: "plugin_update", Target: p.ID, Version: p.Update.Latest})
		}
	}
	if sys := s.systemInfo(); sys.Update != nil {
		attention = append(attention, attentionJSON{Kind: "hub_update", Version: sys.Update.Latest})
	}
	recs, err := s.st.Audit().Query(ctx, store.AuditFilter{Kind: store.AuditTool, Via: []string{"site", "mcp"}, Limit: 5})
	if err != nil {
		return err
	}
	titles := s.toolTitles()
	recent := make([]auditItemJSON, 0, len(recs))
	for _, rec := range recs {
		recent = append(recent, toAuditItem(rec, titles))
	}
	return s.writeJSON(q, http.StatusOK, map[string]any{
		"home":      map[string]string{"name": hs.HomeName},
		"relay":     rs,
		"plugins":   plugins,
		"attention": attention,
		"recent":    recent,
	})
}
