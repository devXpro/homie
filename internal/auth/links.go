package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/devXpro/homie/internal/auth/wire"
	"github.com/devXpro/homie/internal/store"
)

// SetupLink is a one-time link that lets the owner register a passkey (PLAN §5.8).
type SetupLink struct {
	// URL is <origin>/admin/setup#t=<token>. The token rides in the fragment, so neither Caddy's nor frps's logs nor
	// a Referer ever contain it.
	URL       string
	ExpiresAt time.Time
}

// LinkRequest says who asks for a setup link and how long it lives.
type LinkRequest struct {
	Origin string        // the admin's origin, "https://<domain>"
	Via    string        // the audit surface: "cli" (socket, CLI) or "admin" (the LAN first-run page)
	Client string        // the address that asked, for the audit record; "" on the socket
	TTL    time.Duration // the link's life; 0 means SetupLinkTTL (the LAN page asks for less)
}

// NewSetupLink makes a setup link, valid once and for req.TTL (15 minutes by default), and revokes every older one.
// The link names the owner when one exists, so the passkey is added to them; otherwise the registration creates the
// owner. rec may be nil: the "setup.link" record (with the client's address, when known) then goes straight to st.
//
// It works on any store.Store, so homed's socket handler and a CLI that opened the database itself can both use it.
// Showing the link is the caller's business; it is a secret until used or expired.
func NewSetupLink(ctx context.Context, st store.Store, rec AuditRecorder, req LinkRequest, now time.Time) (SetupLink, error) {
	if err := checkOrigin(req.Origin); err != nil {
		return SetupLink{}, err
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = SetupLinkTTL
	}
	token := newToken()
	if err := st.InTx(ctx, func(tx store.Store) error { return createSetupInvite(ctx, tx, token, now, ttl) }); err != nil {
		return SetupLink{}, fmt.Errorf("auth: setup link: %w", err)
	}
	var details json.RawMessage
	if req.Client != "" {
		details, _ = wire.Marshal(map[string]string{"client": req.Client})
	}
	recordAudit(context.WithoutCancel(ctx), st, rec, nil,
		store.AuditRecord{At: now, Via: req.Via, Action: "setup.link", Args: details, OK: true})
	return SetupLink{URL: setupURL(req.Origin, token), ExpiresAt: now.Add(ttl)}, nil
}

// createSetupInvite revokes the unused setup links and stores a new one for token, valid for ttl.
func createSetupInvite(ctx context.Context, tx store.Store, token string, now time.Time, ttl time.Duration) error {
	owner, _, err := findOwner(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.Invites().RevokeAll(ctx, store.InviteSetup, "", now); err != nil {
		return err
	}
	return tx.Invites().Create(ctx, store.Invite{
		ID: newID("i_"), TokenHash: hashToken(token), Kind: store.InviteSetup, PersonID: owner.ID,
		CreatedAt: now, ExpiresAt: now.Add(ttl),
	})
}

// Reset is what ResetAccess did.
type Reset struct {
	SetupLink
	RevokedPasskeys int
	RevokedSessions int
	SessionIDs      []string // the deleted sessions, so their event streams can be closed
}

// ResetAccess is `homed admin reset-access`, for a lost or compromised passkey: in one transaction it deletes every
// session and every passkey of every person, revokes every unused setup link and invite, and makes a new setup link
// for the owner. Person keys (ChatGPT access) stay. It records "access.reset". Arguments as for NewSetupLink.
func ResetAccess(ctx context.Context, st store.Store, rec AuditRecorder, origin string, now time.Time) (Reset, error) {
	if err := checkOrigin(origin); err != nil {
		return Reset{}, err
	}
	token := newToken()
	var out Reset
	err := st.InTx(ctx, func(tx store.Store) error {
		people, err := tx.People().List(ctx)
		if err != nil {
			return err
		}
		out.SessionIDs = nil
		for _, p := range people {
			sessions, err := tx.Sessions().ListByPerson(ctx, p.ID)
			if err != nil {
				return err
			}
			for _, s := range sessions {
				out.SessionIDs = append(out.SessionIDs, s.ID)
			}
		}
		if out.RevokedSessions, err = tx.Sessions().DeleteAll(ctx); err != nil {
			return err
		}
		if out.RevokedPasskeys, err = tx.Passkeys().DeleteAll(ctx); err != nil {
			return err
		}
		if _, err := tx.Invites().RevokeAll(ctx, store.InviteMember, "", now); err != nil {
			return err
		}
		return createSetupInvite(ctx, tx, token, now, SetupLinkTTL)
	})
	if err != nil {
		return Reset{}, fmt.Errorf("auth: reset access: %w", err)
	}
	// The detail keys avoid the audit redaction pattern ("key", "token"…), which would hide the counts.
	details, _ := wire.Marshal(map[string]int{"credentials": out.RevokedPasskeys, "sessions": out.RevokedSessions})
	recordAudit(context.WithoutCancel(ctx), st, rec, nil, store.AuditRecord{At: now, Via: "cli", Action: "access.reset", Args: details, OK: true})
	out.SetupLink = SetupLink{URL: setupURL(origin, token), ExpiresAt: now.Add(SetupLinkTTL)}
	return out, nil
}

// ResetAccess runs the package's ResetAccess on this Auth's store and recorder, drops the pending ceremonies and
// tells OnSessionEnd about every deleted session.
func (a *Auth) ResetAccess(ctx context.Context, origin string) (Reset, error) {
	res, err := ResetAccess(ctx, a.st, a.audit, origin, a.now())
	if err != nil {
		return Reset{}, err
	}
	a.ceremonies.clear()
	if a.onEnd != nil {
		for _, id := range res.SessionIDs {
			a.onEnd(id)
		}
	}
	return res, nil
}

// OwnerReady reports whether an owner has at least one passkey of the relying party rpID (the relay domain, or
// localhost in development): the home then has a way in, the login screen works and the first-run page is closed.
// Passkeys of another relying party (registered before a relay change) can no longer sign in and do not count; with
// no relying party yet (rpID "") nothing does.
func OwnerReady(ctx context.Context, st store.Store, rpID string) (bool, error) {
	if rpID == "" {
		return false, nil
	}
	people, err := st.People().List(ctx)
	if err != nil {
		return false, err
	}
	for _, p := range people {
		if p.Role != store.RoleOwner {
			continue
		}
		pks, err := st.Passkeys().ListByPerson(ctx, p.ID)
		if err != nil {
			return false, err
		}
		for _, pk := range pks {
			if PasskeyRPID(pk) == rpID {
				return true, nil
			}
		}
	}
	return false, nil
}

// OwnerReady is the package's OwnerReady for the current relying party; false while there is none.
func (a *Auth) OwnerReady(ctx context.Context) (bool, error) {
	rp, ok := a.rp()
	if !ok {
		return false, nil
	}
	return OwnerReady(ctx, a.st, rp.ID)
}

// PasskeyRPID returns the relying party ID a stored passkey belongs to; "" when its credential cannot be read.
func PasskeyRPID(pk store.Passkey) string {
	id, _, err := decodeCredential(pk.Credential)
	if err != nil {
		return ""
	}
	return id
}

// UsablePasskey reports whether a passkey can sign in to the home now: it belongs to the current relying party.
// internal/admin counts only these for ownerReady and the last-passkey rule.
func (a *Auth) UsablePasskey(pk store.Passkey) bool {
	rp, ok := a.rp()
	return ok && PasskeyRPID(pk) == rp.ID
}

// findOwner returns the first owner; found is false when the home has none yet.
func findOwner(ctx context.Context, st store.Store) (owner store.Person, found bool, err error) {
	people, err := st.People().List(ctx)
	if err != nil {
		return store.Person{}, false, err
	}
	for _, p := range people {
		if p.Role == store.RoleOwner {
			return p, true, nil
		}
	}
	return store.Person{}, false, nil
}

func setupURL(origin, token string) string { return origin + "/admin/setup#t=" + token }

// checkOrigin accepts "http(s)://host[:port]" and nothing more.
func checkOrigin(origin string) error {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("auth: %q is not an origin", origin)
	}
	return nil
}

// LocalHandler serves the socket-only endpoints of ADMIN-API §6 (internal/admin's Options.LocalRoutes):
//
//	POST /api/admin/local/setup-link     → {"url","expiresAt"}                                  (homed admin setup-link)
//	POST /api/admin/local/reset-access   → {"url","expiresAt","revokedPasskeys","revokedSessions"} (homed admin reset-access)
//
// Mount it on the unix socket only: whoever reaches it gets a link that signs them in as the owner.
func (a *Auth) LocalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/admin/local/setup-link", a.local(a.localSetupLink))
	mux.Handle("/api/admin/local/reset-access", a.local(a.localResetAccess))
	mux.Handle("/api/admin/local/", notFound())
	return mux
}

// local wraps a socket endpoint: the common headers and POST only. The socket needs no CSRF rule (ADMIN-API §6).
func (a *Auth) local(h handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire.Headers(w)
		var err error
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			err = wire.Errorf(http.StatusMethodNotAllowed, "method_not_allowed", r.Method+" is not allowed here")
		} else if err = wire.Decode(w, r, &struct{}{}); err == nil {
			err = h(w, r)
		}
		if err != nil {
			a.writeError(w, r, err)
		}
	})
}

var errNoOrigin = wire.Errorf(http.StatusConflict, "relay_not_ready", "no relay is configured; run `homed relay connect` first")

type linkJSON struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
}

func (a *Auth) localSetupLink(w http.ResponseWriter, r *http.Request) error {
	rp, ok := a.rp()
	if !ok {
		return errNoOrigin
	}
	link, err := NewSetupLink(r.Context(), a.st, a.audit, LinkRequest{Origin: rp.Origin, Via: "cli"}, a.now())
	if err != nil {
		return err
	}
	wire.WriteJSON(w, http.StatusOK, linkJSON{URL: link.URL, ExpiresAt: wire.Time(link.ExpiresAt)})
	return nil
}

func (a *Auth) localResetAccess(w http.ResponseWriter, r *http.Request) error {
	rp, ok := a.rp()
	if !ok {
		return errNoOrigin
	}
	res, err := a.ResetAccess(r.Context(), rp.Origin)
	if err != nil {
		return err
	}
	wire.WriteJSON(w, http.StatusOK, struct {
		linkJSON
		RevokedPasskeys int `json:"revokedPasskeys"`
		RevokedSessions int `json:"revokedSessions"`
	}{linkJSON{URL: res.URL, ExpiresAt: wire.Time(res.ExpiresAt)}, res.RevokedPasskeys, res.RevokedSessions})
	return nil
}
