package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/devXpro/mcp-gatekeeper/adminsock"
	"github.com/devXpro/mcp-gatekeeper/auth"
	"github.com/devXpro/mcp-gatekeeper/core"
	"github.com/devXpro/mcp-gatekeeper/httpsec"
	"github.com/devXpro/mcp-gatekeeper/mcpguard"
	"github.com/devXpro/mcp-gatekeeper/oauth"
	gkpeople "github.com/devXpro/mcp-gatekeeper/people"
	gkstore "github.com/devXpro/mcp-gatekeeper/store"

	"github.com/devXpro/homie/internal/gateway"
	"github.com/devXpro/homie/internal/tunnel"
)

// brand is what Homie puts on the wire. A running hub never changes a name of it (core.Brand): the cookies would sign
// everybody out. AppNoun stays empty, so the gatekeeper's texts call homed "the hub".
var brand = core.Brand{
	Product:       "Homie",
	SessionCookie: "__Host-homie_session",
	TxnCookie:     "__Host-homie_txn",
	CSRFHeader:    "X-Homie",
	ResourceNoun:  "home",
}

// adminCLI is how the operator runs an admin command, as the gatekeeper's texts name it.
const adminCLI = "homed admin"

// MCP limits of the home (review GK-4): every person has their own 8 calls in flight.
const inFlightPerPerson = 8

// stack is the part of homed that belongs to one public URL (a core.Site): sign-in, OAuth, people, the MCP access
// check and the HTTP handler over them. A new URL (a quick tunnel's restart, a connect) builds a new stack and swaps
// it in at once; the old one's passkey ceremonies and consent transactions, which were bound to the old URL, are
// dropped with it.
type stack struct {
	site    core.Site
	auth    *auth.Auth
	oauth   *oauth.Server
	people  *gkpeople.Service
	guard   *mcpguard.Guard
	handler http.Handler
}

// newStack builds the gatekeeper over site.
func (d *daemon) newStack(site core.Site) (*stack, error) {
	a, err := auth.New(auth.Options{
		Site: site, Brand: brand, Store: d.st, Logger: d.log.With("component", "auth"), Clock: d.clock,
		AdminCLI: adminCLI, OnAccessReset: d.accessReset,
	})
	if err != nil {
		return nil, err
	}
	o, err := oauth.New(oauth.Options{
		Site: site, Brand: brand, Store: d.st, Sessions: a, Catalog: d.gw.Catalog(), Paths: gateway.Paths,
		Logger: d.log.With("component", "oauth"), Clock: d.clock,
	})
	if err != nil {
		return nil, err
	}
	p, err := gkpeople.New(gkpeople.Options{
		Site: site, Brand: brand, Sessions: a, Store: d.st, Inviter: inviter{a: a, d: d}, Revoker: o,
		NormalizeName: auth.NormalizeName, Eraser: keepHome{}, Fields: homeFields{d: d, site: site},
		Permissions: pluginCatalog{d}, CoOwners: true, SingleAccount: true,
		Log: d.log.With("component", "people"), Clock: d.clock,
	})
	if err != nil {
		return nil, err
	}
	g, err := mcpguard.New(mcpguard.Options{
		Site: site, Paths: gateway.Paths, Tokens: o, People: d.st.People(), Resources: d.gw.Catalog(), Brand: brand,
		InFlightPerResource: inFlightPerPerson, InFlightKey: mcpguard.PerPerson,
		Log: d.log.With("component", "mcpguard"), Clock: d.clock,
	})
	if err != nil {
		return nil, err
	}
	h, err := httpsec.New(httpsec.Options{
		Site:   site,
		Routes: []core.RouteRegistrar{a, o, p, d.admin.Routes(a, site), d.gw.Routes(g), d.ui},
		// cloudflared runs on this machine and the listener is loopback only: only it can say who the client is.
		Trusted: httpsec.LoopbackPeers(),
		Logger:  d.log.With("component", "http"),
	})
	if err != nil {
		return nil, err
	}
	return &stack{site: site, auth: a, oauth: o, people: p, guard: g, handler: h}, nil
}

// siteOf is the Site of a public URL https://<host> behind cloudflared: the host is the passkeys' relying party.
func siteOf(publicURL string) (core.Site, error) {
	u, err := url.Parse(publicURL)
	if err != nil {
		return core.Site{}, err
	}
	s := core.Site{PublicURL: publicURL, RPID: u.Hostname(), ClientIP: core.ClientIPCloudflare}
	return s, s.Validate()
}

// setSite builds and swaps in the stack of site, or, with nil, drops the stack: the hub has no public URL (no tunnel,
// or a quick tunnel without a host yet). The same site again changes nothing.
func (d *daemon) setSite(site *core.Site) {
	d.swapMu.Lock()
	defer d.swapMu.Unlock()
	old := d.stack.Load()
	if site != nil && old != nil && old.site == *site || site == nil && old == nil {
		return
	}
	var next *stack
	if site != nil {
		var err error
		if next, err = d.newStack(*site); err != nil {
			d.log.Error("the public URL cannot be served", "url", site.PublicURL, "err", err)
			next = nil
		}
	}
	if next != nil {
		_ = next.auth.Start(context.Background())
	}
	d.stack.Store(next)
	if old != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = old.auth.Stop(ctx)
		cancel()
	}
	if next != nil {
		d.log.Info("serving the public URL", "url", next.site.PublicURL)
	} else {
		d.log.Info("the hub has no public URL")
	}
}

// publicHost is the host of the current public URL (the host guard lets it through); "" without one.
func (d *daemon) publicHost() string {
	if s := d.stack.Load(); s != nil && s.site.ClientIP == core.ClientIPCloudflare {
		return s.site.RPID
	}
	return ""
}

// handler serves the current stack, or, while the hub has no public URL, the bare handler: /healthz, the bootstrap
// and the SPA, and 503 home_not_ready for everything that needs the gatekeeper.
func (d *daemon) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s := d.stack.Load(); s != nil {
			s.handler.ServeHTTP(w, r)
			return
		}
		d.bare.ServeHTTP(w, r)
	})
}

// newBare is the handler of a hub without a public URL.
func (d *daemon) newBare() (http.Handler, error) {
	site := core.Site{ClientIP: core.ClientIPCloudflare}
	return httpsec.New(httpsec.Options{
		Site:    site,
		Routes:  []core.RouteRegistrar{d.admin.Routes(noSessions{}, site), d.ui, notConnected{}},
		Trusted: httpsec.LoopbackPeers(),
		Logger:  d.log.With("component", "http"),
	})
}

// noSessions is core.Sessions with nobody signed in: the hub has no public URL, so nobody can be.
type noSessions struct{}

func (noSessions) Viewer(*http.Request) (core.Viewer, error) { return core.Viewer{}, core.ErrNoSession }

func (n noSessions) RequireViewer(http.Handler) http.Handler {
	return n.SameOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpsec.WriteError(w, http.StatusUnauthorized, httpsec.CodeNoSession, "sign in first")
	}))
}

func (noSessions) RequireStepUp(next http.Handler) http.Handler { return next }

func (noSessions) SameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get(brand.CSRFHeader) != "1" {
			httpsec.WriteError(w, http.StatusForbidden, httpsec.CodeForbidden, "cross-site request refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// notConnected answers what needs the gatekeeper while the hub has no public URL.
type notConnected struct{}

func (notConnected) RegisterRoutes(mux *http.ServeMux) {
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpsec.WriteError(w, http.StatusServiceUnavailable, "home_not_ready",
			"the home is not connected to the internet yet: connect it on the first-run page of the home box, or with "+
				"homed cloudflare connect <host>")
	})
	for _, p := range []string{"/api/auth/", "/api/me", "/api/me/", "/api/account", "/api/account/", "/api/oauth/",
		"/api/admin/", "/oauth/", "/.well-known/", gateway.Path} {
		mux.Handle(p, h)
	}
}

// ownerReady reports whether the home's superadmin has a usable passkey of the current public URL: then nothing needs
// the first-run page. False while there is no public URL. The current stack's auth answers for its own relying party,
// so a passkey of an old URL does not count, which is how a quick tunnel's new URL reopens the first-run page.
func (d *daemon) ownerReady(ctx context.Context) (bool, error) {
	s := d.stack.Load()
	if s == nil {
		return false, nil
	}
	return s.auth.SuperadminCanSignIn(ctx)
}

// superadmins returns the superadmins of every account.
func superadmins(ctx context.Context, st gkstore.Store) ([]core.Person, error) {
	accounts, err := st.Accounts().List(ctx)
	if err != nil {
		return nil, err
	}
	var out []core.Person
	for _, a := range accounts {
		people, err := st.People().ListByAccount(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		for _, p := range people {
			if p.Superadmin {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

// codeQuickMode refuses what a quick tunnel cannot keep: its URL changes on every restart, and every passkey with it.
const codeQuickMode = "quick_mode"

// inviter is *auth.Auth as the people service's AccessInviter. In quick mode it refuses every invite: a quick tunnel is
// the owner's alone, its address changes on every restart, and every passkey with it.
type inviter struct {
	a *auth.Auth
	d *daemon
}

var _ gkpeople.AccessInviter = inviter{}

// quick refuses an invite while the home is on a quick tunnel.
func (i inviter) quick() error {
	if i.d == nil || i.d.tun == nil || i.d.tun.Status().Mode != tunnel.ModeQuick {
		return nil
	}
	return gkpeople.NewError(http.StatusConflict, codeQuickMode,
		"invites need the home's own domain: a quick tunnel is for trying, and its address changes on every restart")
}

func (i inviter) IssueInvite(ctx context.Context, kind core.InviteKind, account core.AccountID, name string,
	by core.PersonID) (gkpeople.IssuedInvite, error) {
	if err := i.quick(); err != nil {
		return gkpeople.IssuedInvite{}, err
	}
	inv, err := i.a.IssueInvite(ctx, kind, account, name, by)
	return gkpeople.IssuedInvite(inv), err
}

func (i inviter) IssueInviteIn(ctx context.Context, st gkstore.Store, kind core.InviteKind, account core.AccountID,
	name string, by core.PersonID) (gkpeople.IssuedInvite, error) {
	if err := i.quick(); err != nil {
		return gkpeople.IssuedInvite{}, err
	}
	inv, err := i.a.IssueInviteIn(ctx, st, kind, account, name, by)
	return gkpeople.IssuedInvite(inv), err
}

func (i inviter) IssueInviteWithAccess(ctx context.Context, kind core.InviteKind, account core.AccountID, name string,
	by core.PersonID, until *time.Time, perms []string) (gkpeople.IssuedInvite, error) {
	if err := i.quick(); err != nil {
		return gkpeople.IssuedInvite{}, err
	}
	inv, err := i.a.IssueInviteWithAccess(ctx, kind, account, name, by, until, perms)
	return gkpeople.IssuedInvite(inv), err
}

// revokeInvitesForQuick revokes every open member and owner invite before the home switches to a quick tunnel (review
// R-11): quick mode is the owner's alone, and an invite made on the domain carries no host, so it could otherwise be
// redeemed at the quick URL. Setup links stay: they are how the owner gets a passkey of the quick URL. Each revoke is
// audited as account.invite.revoke with reason tunnel.quick.
func (d *daemon) revokeInvitesForQuick(ctx context.Context) error {
	accounts, err := d.st.Accounts().List(ctx)
	if err != nil {
		return err
	}
	now := d.clock()
	revoked := 0
	err = d.st.Tx(ctx, func(ctx context.Context) error {
		for _, acc := range accounts {
			invites, err := d.st.Invites().ListByAccount(ctx, acc.ID)
			if err != nil {
				return err
			}
			for _, inv := range invites {
				if inv.Kind == core.InviteSetup || inv.UsedAt != nil || inv.RevokedAt != nil || !now.Before(inv.ExpiresAt) {
					continue
				}
				if err := d.st.Invites().Revoke(ctx, inv.ID, now); err != nil {
					return err
				}
				if err := d.st.Audit().Append(ctx, core.AuditEntry{At: now.UTC(), AccountID: acc.ID,
					Action: gkpeople.ActionInviteRevoke, Target: inv.ID,
					Detail: map[string]string{"via": "tunnel", "reason": "tunnel.quick", "kind": string(inv.Kind)}}); err != nil {
					return err
				}
				revoked++
			}
		}
		return nil
	})
	if err == nil && revoked > 0 {
		d.log.Info("revoked the open invites: a quick tunnel is the owner's alone", "invites", revoked)
	}
	return err
}

// keepHome refuses "delete everything" (DELETE /api/account): the home is its one account, and it goes with homed's
// uninstall on the home box, not from a browser.
type keepHome struct{}

func (keepHome) EraseAccount(context.Context, core.AccountID, core.PersonID) (map[string]string, error) {
	return nil, gkpeople.NewError(http.StatusForbidden, httpsec.CodeForbidden,
		"the home is not deleted from the admin; uninstall homed on the home box to delete its data")
}

// pluginCatalog is the permission catalog of the home (Q1: a device is a plugin): every installed plugin, disabled
// ones included, as "plugin:<id>", titled by the plugin.
type pluginCatalog struct{ d *daemon }

func (c pluginCatalog) Permissions(ctx context.Context, _ core.AccountID) ([]core.PermissionInfo, error) {
	rows, err := c.d.st.Plugins().List(ctx)
	if err != nil {
		return nil, err
	}
	titles := c.d.pluginTitles()
	out := make([]core.PermissionInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, core.PermissionInfo{ID: gateway.PluginPermission(row.ID), Title: cmpOr(titles[row.ID], row.ID)})
	}
	return out, nil
}

// pluginTitles are the plugins' own titles (their serverInfo), by id.
func (d *daemon) pluginTitles() map[string]string {
	out := map[string]string{}
	for _, st := range d.host.Status() {
		if st.Title != "" {
			out[st.ID] = st.Title
		}
	}
	return out
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// homeFields are Homie's fields of GET /api/account (docs/ADMIN-API.md §5): the home as the connect page's resource,
// the devices the viewer may use, and the tunnel's mode.
type homeFields struct {
	d    *daemon
	site core.Site
}

type resourceJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MCPURL      string `json:"mcp_url"`
	Enabled     bool   `json:"enabled"`
}

type deviceJSON struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Permission string `json:"permission"`
}

func (f homeFields) AccountFields(ctx context.Context, v core.Viewer) (map[string]any, error) {
	rows, err := f.d.st.Plugins().List(ctx)
	if err != nil {
		return nil, err
	}
	titles := f.d.pluginTitles()
	devices := []deviceJSON{}
	for _, row := range rows {
		perm := gateway.PluginPermission(row.ID)
		if v.Person.Role == core.RoleOwner || slices.Contains(v.Person.Permissions, core.PermissionAll) ||
			slices.Contains(v.Person.Permissions, perm) {
			devices = append(devices, deviceJSON{ID: row.ID, Title: cmpOr(titles[row.ID], row.ID), Permission: perm})
		}
	}
	return map[string]any{
		"resources": []resourceJSON{{ID: string(gateway.ResourceID), Name: v.Account.Name,
			Description: "The devices of the home", MCPURL: f.site.PublicURL + gateway.Path, Enabled: true}},
		"devices": devices,
		"tunnel":  map[string]string{"mode": f.d.tun.Status().Mode},
	}, nil
}

// accessReset hears of every reset-access on the admin socket (review GK-5): an alert in the admin, a warning in the
// log, with the program that asked and, when the socket knew them, the programs that ran it (review R-5: the peer is
// the homed CLI itself, so the chain above it is what says who asked).
func (d *daemon) accessReset(ctx context.Context, r auth.AccessReset) {
	by := ""
	if r.Peer.PID > 0 {
		by = fmt.Sprintf("%s (pid %d)", cmpOr(r.Peer.Exe, "unknown program"), r.Peer.PID)
		if chain := d.resetPeerChain(ctx, r.At); chain != "" {
			by += ", run from " + chain
		}
	}
	d.log.Warn("ACCESS WAS RESET on the admin socket: every passkey and session of the owner is gone; if you did not "+
		"do this, a program of this user did", "by", by, "people", len(r.People))
	d.admin.AccessReset(ctx, r.At, by)
}

// peerChainDetail is the audit detail of admin.reset-access (and admin.setup-link) that names the socket peer's
// ancestors, nearest first: "<exe> (pid N) < <exe> (pid N) < …".
const peerChainDetail = "peer_chain"

// resetPeerChain is the peer_chain of the admin.reset-access entry the gatekeeper wrote at at (it writes the entry
// before it calls OnAccessReset); "" when there is none, or the gatekeeper does not record it.
func (d *daemon) resetPeerChain(ctx context.Context, at time.Time) string {
	entries, err := d.st.Audit().ListSystem(ctx, "", 20)
	if err != nil {
		d.log.Warn("cannot read the audit for the access reset's peer", "err", err)
		return ""
	}
	for _, e := range entries {
		if e.Action == "admin.reset-access" && (e.At.Sub(at)).Abs() < time.Millisecond {
			return e.Detail[peerChainDetail]
		}
	}
	return ""
}

// authCommand is one of auth's admin socket commands (setup-link, reset-access) run by the current stack's auth: a
// stack's commands belong to its public URL, and the registry outlives the stacks.
func (d *daemon) authCommand(name, summary string) adminsock.Command {
	return adminsock.Command{Name: name, Summary: summary, Run: func(ctx context.Context, inv adminsock.Invocation) error {
		s := d.stack.Load()
		if s == nil {
			return errors.New("the home has no public address yet: connect it first (homed cloudflare connect <host>, " +
				"homed cloudflare quick, or the first-run page: homed admin firstrun)")
		}
		for _, c := range s.auth.AdminCommands() {
			if c.Name == name {
				return c.Run(ctx, inv)
			}
		}
		return fmt.Errorf("no command %s", name)
	}}
}

// auditCLI appends an admin socket action to the home's audit (in its account when it has one).
func (d *daemon) auditCLI(ctx context.Context, e core.AuditEntry) {
	if e.At.IsZero() {
		e.At = d.clock().UTC()
	}
	if e.AccountID == "" {
		if accs, err := d.st.Accounts().List(ctx); err == nil && len(accs) > 0 {
			e.AccountID = accs[0].ID
		}
	}
	if err := d.st.Audit().Append(context.WithoutCancel(ctx), e); err != nil {
		d.log.Error("cannot write an audit entry", "action", e.Action, "err", err)
	}
}

// homeName is the account's name, or before the first setup the name that pre-fills it.
func (d *daemon) homeName(ctx context.Context) string {
	if accs, err := d.st.Accounts().List(ctx); err == nil && len(accs) > 0 {
		return accs[0].Name
	}
	if hs, err := d.st.Settings().Get(ctx); err == nil {
		return hs.HomeName
	}
	return ""
}

// The compile-time checks of the adapters.
var (
	_ core.PermissionCatalog = pluginCatalog{}
	_ gkpeople.AccountFields = homeFields{}
	_ gkpeople.Eraser        = keepHome{}
	_ core.Sessions          = noSessions{}
)
