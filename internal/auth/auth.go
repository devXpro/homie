package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/devXpro/homie/internal/auth/wire"
	"github.com/devXpro/homie/internal/store"
)

// Lifetimes (PLAN §5.8, ADMIN-API §1.3, §1.5, §3.2).
const (
	// CookieName is the session cookie: __Host- binds it to the exact origin, path / and Secure.
	CookieName = "__Host-homie_session"
	// IdleTimeout ends a session that made no request for this long.
	IdleTimeout = 12 * time.Hour
	// AbsoluteTimeout ends every session this long after sign-in.
	AbsoluteTimeout = 7 * 24 * time.Hour
	// StepUpWindow is how long a passkey assertion (sign-in included) counts as fresh for dangerous actions.
	StepUpWindow = 5 * time.Minute
	// CeremonyTTL is how long a begun passkey ceremony can be finished.
	CeremonyTTL = 5 * time.Minute
	// SetupLinkTTL is the life of a setup link.
	SetupLinkTTL = 15 * time.Minute

	touchEvery = time.Minute // LastSeenAt is written at most this often per session
)

// ErrUnauthenticated means the request carries no valid session: no cookie, an unknown one, or an expired session.
var ErrUnauthenticated = errors.New("auth: no valid session")

// RelyingParty is the WebAuthn identity of the home. Passkeys are bound to ID; a new ID locks every passkey out.
type RelyingParty struct {
	ID     string // the relay domain, e.g. "homie.jobbyjobs.work"; "localhost" in development
	Origin string // the admin's origin, e.g. "https://homie.jobbyjobs.work"; "http://localhost:5173" in development
}

// ForDomain returns the relying party of a relay domain: the domain itself and https://<domain>.
func ForDomain(domain string) RelyingParty {
	return RelyingParty{ID: domain, Origin: "https://" + domain}
}

// AuditRecorder takes the admin audit records auth writes (ADMIN-API §7). *admin.AuditWriter implements it.
type AuditRecorder interface {
	RecordAdmin(ctx context.Context, rec store.AuditRecord)
}

// Features is Me.features (ADMIN-API §2).
type Features struct {
	People      bool `json:"people"`
	Connections bool `json:"connections"`
}

// Options configures Auth. Store and RelyingParty are required.
type Options struct {
	Store store.Store
	// RelyingParty returns the current relying party; ok is false while homed has neither a relay domain nor a
	// development origin, and the ceremonies then answer 503 home_not_ready. It is read on every request, so a relay
	// connected later (first run) takes effect at once.
	RelyingParty func() (rp RelyingParty, ok bool)
	// Audit receives the admin audit records. Nil appends them to Store.Audit directly (no SSE event then).
	Audit AuditRecorder
	// OnSessionEnd is called after auth deleted a session (logout, expiry, reset-access), so the admin's event streams
	// of that session close at once (admin.Server.SessionRevoked). Optional.
	OnSessionEnd func(sessionID string)
	// OnSetup is called after a registration with a setup or invite token stored its person and settings (a new or
	// renamed person, maybe a new home name), so the gateway's home name and the key resolver can follow. Optional.
	OnSetup func(person store.Person, settings store.HomeSettings)
	// Version is the hub version in Me; empty means "dev".
	Version string
	// Features is Me.features.
	Features Features
	// Log receives security events (failed ceremonies, clone warnings). Tokens and credentials never reach it.
	Log *slog.Logger
	// Now is the clock; nil means time.Now. Tests set it.
	Now func() time.Time
}

// Auth is the passkey and session service. It is safe for concurrent use.
type Auth struct {
	st         store.Store
	rp         func() (RelyingParty, bool)
	audit      AuditRecorder
	onEnd      func(string)
	onSetup    func(store.Person, store.HomeSettings)
	version    string
	features   Features
	log        *slog.Logger
	now        func() time.Time
	ceremonies *ceremonies
	limits     limits // the auth endpoints of ADMIN-API §1.7
}

// New returns an Auth.
func New(o Options) (*Auth, error) {
	if o.Store == nil || o.RelyingParty == nil {
		return nil, errors.New("auth: Store and RelyingParty are required")
	}
	if o.Version == "" {
		o.Version = "dev"
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Auth{
		st: o.Store, rp: o.RelyingParty, audit: o.Audit, onEnd: o.OnSessionEnd, onSetup: o.OnSetup,
		version: o.Version, features: o.Features, log: o.Log, now: o.Now,
		ceremonies: newCeremonies(),
		limits:     newLimits(),
	}, nil
}

// Principal is the signed-in person behind a request and the times of their session. Its fields match
// admin.Principal one for one, so T2.6 converts with admin.Principal(p).
type Principal struct {
	Person        store.Person
	Session       store.Session
	ExpiresAt     time.Time // absolute end of the session: 7 days after sign-in
	IdleExpiresAt time.Time // idle end of the session: 12 hours after the last request, never after ExpiresAt
	StepUpUntil   time.Time // the session counts as stepped up before this time; zero when it never was
}

// SteppedUp reports whether the session's last passkey assertion is fresh enough for a dangerous action at now.
func (p Principal) SteppedUp(now time.Time) bool { return now.Before(p.StepUpUntil) }

func newPrincipal(person store.Person, s store.Session) Principal {
	p := Principal{Person: person, Session: s, ExpiresAt: s.CreatedAt.Add(AbsoluteTimeout)}
	p.IdleExpiresAt = s.LastSeenAt.Add(IdleTimeout)
	if p.IdleExpiresAt.After(p.ExpiresAt) {
		p.IdleExpiresAt = p.ExpiresAt
	}
	if !s.StepUpAt.IsZero() {
		p.StepUpUntil = s.StepUpAt.Add(StepUpWindow)
	}
	return p
}

// expired reports whether a session is past its idle or absolute end at now.
func expired(s store.Session, now time.Time) bool {
	return !now.Before(s.LastSeenAt.Add(IdleTimeout)) || !now.Before(s.CreatedAt.Add(AbsoluteTimeout))
}

// Authenticate returns the principal of the request's session cookie, or ErrUnauthenticated. It owns the expiry
// policy: an expired session is deleted (and OnSessionEnd called). It touches LastSeenAt at most once a minute and
// never for an event stream (Accept: text/event-stream), so an open tab alone does not keep a session from idling
// out. Other errors are store failures.
func (a *Auth) Authenticate(r *http.Request) (Principal, error) {
	c, err := r.Cookie(CookieName)
	if err != nil || !wellFormedToken(c.Value) {
		return Principal{}, ErrUnauthenticated
	}
	ctx := r.Context()
	hash := hashToken(c.Value)
	s, err := a.st.Sessions().GetByTokenHash(ctx, hash)
	if errors.Is(err, store.ErrNotFound) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, fmt.Errorf("auth: load session: %w", err)
	}
	// The store looked the hash up; compare once more in constant time so a loose lookup can never let a session in.
	if subtle.ConstantTimeCompare([]byte(s.TokenHash), []byte(hash)) != 1 {
		return Principal{}, ErrUnauthenticated
	}
	now := a.now()
	if expired(s, now) {
		a.dropSession(ctx, s.ID)
		return Principal{}, ErrUnauthenticated
	}
	person, err := a.st.People().Get(ctx, s.PersonID)
	if errors.Is(err, store.ErrNotFound) {
		a.dropSession(ctx, s.ID)
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, fmt.Errorf("auth: load person: %w", err)
	}
	if now.Sub(s.LastSeenAt) >= touchEvery && !isEventStream(r) {
		if err := a.st.Sessions().Touch(ctx, s.ID, now); err != nil {
			a.log.Warn("auth: cannot touch session", "session", s.ID, "err", err)
		} else {
			s.LastSeenAt = now
		}
	}
	return newPrincipal(person, s), nil
}

func isEventStream(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/event-stream")
}

// dropSession deletes a session Authenticate found unusable; a failure only costs a row that Run removes later.
func (a *Auth) dropSession(ctx context.Context, id string) {
	if err := a.deleteSession(ctx, id); err != nil {
		a.log.Warn("auth: cannot delete session", "session", id, "err", err)
	}
}

type principalKey struct{}

// PrincipalFrom returns the principal RequireSession put into the request context.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// RequireSession lets a request through only with a valid session (401 unauthenticated otherwise) and puts its
// Principal into the request context (PrincipalFrom).
func (a *Auth) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := PrincipalFrom(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}
		p, err := a.Authenticate(r)
		if err != nil {
			wire.Headers(w)
			a.writeError(w, r, authError(err))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

// RequireStepUp is RequireSession plus step-up (ADMIN-API §1.5): the session's last passkey assertion must be less
// than 5 minutes old, else 403 step_up_required and nothing happens.
func (a *Auth) RequireStepUp(next http.Handler) http.Handler {
	return a.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFrom(r.Context())
		if !p.SteppedUp(a.now()) {
			wire.Headers(w)
			wire.WriteError(w, errStepUp)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

var errStepUp = wire.Errorf(http.StatusForbidden, "step_up_required", "confirm with your passkey first")

// authError maps an Authenticate error to the API error.
func authError(err error) error {
	if errors.Is(err, ErrUnauthenticated) {
		return wire.Errorf(http.StatusUnauthorized, "unauthenticated", "sign in first")
	}
	return err
}

// writeError logs errors that are not API errors (they become 500 internal) and writes the error body.
func (a *Auth) writeError(w http.ResponseWriter, r *http.Request, err error) {
	if _, ok := errors.AsType[*wire.Error](err); !ok {
		a.log.Error("auth: request failed", "method", r.Method, "path", r.URL.Path,
			"request", w.Header().Get(wire.RequestIDHeader), "err", err)
	}
	wire.WriteError(w, err)
}

// clientDetails is an audit record's details naming the client address a request came from (ClientAddr); nil when
// it is unknown.
func clientDetails(r *http.Request) json.RawMessage {
	addr := ClientAddr(r)
	if addr == "" {
		return nil
	}
	b, _ := wire.Marshal(map[string]string{"client": addr})
	return b
}

// record writes an admin audit record. Records outlive the request, so a client that disconnects cannot drop them.
func (a *Auth) record(ctx context.Context, rec store.AuditRecord) {
	if rec.At.IsZero() {
		rec.At = a.now()
	}
	recordAudit(context.WithoutCancel(ctx), a.st, a.audit, a.log, rec)
}

// recordAudit sends rec to the recorder, or appends it to the store when there is none. log may be nil.
func recordAudit(ctx context.Context, st store.Store, recorder AuditRecorder, log *slog.Logger, rec store.AuditRecord) {
	rec.Kind = store.AuditAdmin
	if recorder != nil {
		recorder.RecordAdmin(ctx, rec)
		return
	}
	if _, err := st.Audit().Append(ctx, []store.AuditRecord{rec}); err != nil && log != nil {
		log.Error("auth: cannot store an audit record", "action", rec.Action, "err", err)
	}
}
