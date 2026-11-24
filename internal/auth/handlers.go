package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/devXpro/homie/internal/auth/wire"
	"github.com/devXpro/homie/internal/store"
)

// API errors of the ceremonies (ADMIN-API §1.2, §3.2).
var (
	errNoRelay       = wire.Errorf(http.StatusServiceUnavailable, "home_not_ready", "the home has no relay domain yet")
	errCeremony      = wire.Errorf(http.StatusBadRequest, "webauthn_failed", "the ceremony expired or was already used")
	errWebAuthn      = wire.Errorf(http.StatusBadRequest, "webauthn_failed", "the passkey did not verify")
	errLoginFailed   = wire.Errorf(http.StatusUnauthorized, "login_failed", "this passkey does not open this home")
	errTokenInvalid  = wire.Errorf(http.StatusNotFound, "token_invalid", "the link is not known")
	errTokenExpired  = wire.Errorf(http.StatusGone, "token_expired", "the link has expired")
	errTokenUsed     = wire.Errorf(http.StatusGone, "token_used", "the link was already used")
	errPasskeyExists = wire.Errorf(http.StatusConflict, "passkey_exists", "this passkey is already registered")
	errOwnerExists   = wire.Errorf(http.StatusConflict, "owner_exists", "the home already has an owner")
	errBusy          = wire.Errorf(http.StatusTooManyRequests, "rate_limited", "too many sign-ins in progress")
	errCSRF          = wire.Errorf(http.StatusForbidden, "csrf_failed", "cross-site request refused")
)

// Handler serves the passkey ceremonies, mounted with full paths (internal/admin's Options.AuthRoutes):
//
//	POST /api/admin/auth/token             public, rate limited per client address and in total
//	POST /api/admin/auth/register/begin    public, rate limited per client address and in total
//	POST /api/admin/auth/register/finish   public, rate limited per client address and in total
//	POST /api/admin/auth/login/begin       public, begins and failed finishes limited per client address
//	POST /api/admin/auth/login/finish      public, begins and failed finishes limited per client address
//	POST /api/admin/auth/stepup/begin      session, begins and failed finishes limited per session
//	POST /api/admin/auth/stepup/finish     session, begins and failed finishes limited per session
//	POST /api/admin/auth/logout            session
//	POST /api/admin/me/passkeys/begin      session + step-up, begins and failed finishes limited per session
//	POST /api/admin/me/passkeys/finish     session + step-up, begins and failed finishes limited per session
//
// It applies the CSRF rule of ADMIN-API §1.4 and its own session and step-up checks, so it is safe on its own; the
// admin's checks in front of it are harmless repeats. Other paths under /api/admin/auth/ answer 404.
func (a *Auth) Handler() http.Handler {
	mux := http.NewServeMux()
	const base = "/api/admin"
	for path, h := range map[string]http.Handler{
		"/auth/token":           a.route(rateToken, a.tokenInfo),
		"/auth/register/begin":  a.route(rateToken, a.registerBegin),
		"/auth/register/finish": a.route(rateToken, a.registerFinish),
		"/auth/login/begin":     a.route(rateLoginBegin, a.loginBegin),
		"/auth/login/finish":    a.route(rateLoginFinish, a.loginFinish),
		"/auth/stepup/begin":    a.route(rateStepUpBegin, a.stepUpBegin),
		"/auth/stepup/finish":   a.route(rateStepUpFinish, a.stepUpFinish),
		"/auth/logout":          a.route(rateNone, a.logout),
		"/me/passkeys/begin":    a.route(ratePasskeyBegin, a.passkeyBegin),
		"/me/passkeys/finish":   a.route(ratePasskeyFinish, a.passkeyFinish),
		"/auth/":                notFound(),
	} {
		mux.Handle(base+path, h)
	}
	return mux
}

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// rateClass names the buckets an endpoint uses (ratelimit.go, ADMIN-API §1.7).
type rateClass int

const (
	rateNone          rateClass = iota
	rateToken                   // every request: per client address and in total
	rateLoginBegin              // per client address: a begin, refused while the failures are spent
	rateLoginFinish             // per client address: refused while the failures are spent; a failure is counted
	rateStepUpBegin             // per session, as rateLoginBegin
	rateStepUpFinish            // per session, as rateLoginFinish
	ratePasskeyBegin            // per session, as rateLoginBegin
	ratePasskeyFinish           // per session, as rateLoginFinish
)

// route wraps an endpoint: the common headers, POST only, the CSRF rule and the endpoint's rate limit; a finish that
// failed to verify is counted against its client or session afterwards.
func (a *Auth) route(rc rateClass, h handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire.Headers(w)
		err := a.admit(w, r, rc)
		if err == nil {
			err = h(w, r)
			a.countFailure(r, rc, err)
		}
		if err != nil {
			a.writeError(w, r, err)
		}
	})
}

func (a *Auth) admit(w http.ResponseWriter, r *http.Request, rc rateClass) error {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		return wire.Errorf(http.StatusMethodNotAllowed, "method_not_allowed", r.Method+" is not allowed here")
	}
	if !a.sameOrigin(r) {
		return errCSRF
	}
	now, ok, wait := a.now(), true, time.Duration(0)
	switch rc {
	case rateNone:
	case rateToken:
		ok, wait = a.limits.token.take(ClientAddr(r), now)
	case rateLoginBegin:
		ok, wait = a.limits.login.begin(ClientAddr(r), now)
	case rateLoginFinish:
		ok, wait = a.limits.login.finish(ClientAddr(r), now)
	case rateStepUpBegin:
		ok, wait = a.limits.stepUp.begin(sessionKey(r), now)
	case rateStepUpFinish:
		ok, wait = a.limits.stepUp.finish(sessionKey(r), now)
	case ratePasskeyBegin:
		ok, wait = a.limits.passkey.begin(sessionKey(r), now)
	case ratePasskeyFinish:
		ok, wait = a.limits.passkey.finish(sessionKey(r), now)
	}
	if !ok {
		return &wire.Error{Status: http.StatusTooManyRequests, Code: "rate_limited",
			Message: "too many attempts; try again later", RetryAfter: wait}
	}
	return nil
}

// countFailure counts a finish whose passkey or ceremony did not verify against its client address or session.
// Successful finishes, and failures for other reasons (no session, step-up needed), cost nothing.
func (a *Auth) countFailure(r *http.Request, rc rateClass, err error) {
	we, ok := errors.AsType[*wire.Error](err)
	if !ok || we.Code != "webauthn_failed" && we.Code != "login_failed" {
		return
	}
	now := a.now()
	switch rc {
	case rateLoginFinish:
		a.limits.login.failed(ClientAddr(r), now)
	case rateStepUpFinish:
		a.limits.stepUp.failed(sessionKey(r), now)
	case ratePasskeyFinish:
		a.limits.passkey.failed(sessionKey(r), now)
	}
}

// sameOrigin is the CSRF rule of ADMIN-API §1.4 for mutating requests: the X-Homie header, an Origin (when sent)
// equal to the admin's origin, and a Sec-Fetch-Site (when sent) of same-origin.
func (a *Auth) sameOrigin(r *http.Request) bool {
	if r.Header.Get("X-Homie") != "1" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		rp, ok := a.rp()
		if !ok || o != rp.Origin {
			return false
		}
	}
	if sf := r.Header.Get("Sec-Fetch-Site"); sf != "" && sf != "same-origin" {
		return false
	}
	return true
}

func notFound() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		wire.Headers(w)
		wire.WriteError(w, wire.Errorf(http.StatusNotFound, "not_found", "endpoint not found"))
	})
}

// beginJSON answers every begin: the ceremony id and the WebAuthn options in their JSON form.
type beginJSON struct {
	CeremonyID string `json:"ceremonyId"`
	PublicKey  any    `json:"publicKey"`
}

// finishBody is the request of every finish.
type finishBody struct {
	CeremonyID  string          `json:"ceremonyId"`
	Credential  json.RawMessage `json:"credential"`
	PasskeyName string          `json:"passkeyName"` // register/finish
	Name        string          `json:"name"`        // me/passkeys/finish
}

func (a *Auth) begin(w http.ResponseWriter, c *ceremony, options any) error {
	now := a.now()
	c.expires = now.Add(CeremonyTTL)
	id, ok := a.ceremonies.add(c, now)
	if !ok {
		return errBusy
	}
	wire.WriteJSON(w, http.StatusOK, beginJSON{CeremonyID: id, PublicKey: options})
	return nil
}

func isKind(k ceremonyKind) func(*ceremony) bool {
	return func(c *ceremony) bool { return c.kind == k }
}

// ofSession accepts a ceremony of kind k begun by the session id.
func ofSession(k ceremonyKind, id string) func(*ceremony) bool {
	return func(c *ceremony) bool { return c.kind == k && c.sessionID == id }
}

// rejected logs why a ceremony failed, without anything secret: go-webauthn's error text names the failed check;
// its debug info (expected and received values) is not logged.
func (a *Auth) rejected(what string, r *http.Request, err error) {
	reason := err.Error()
	if pe, ok := errors.AsType[*protocol.Error](err); ok {
		reason = pe.Type + ": " + pe.Details
	}
	a.log.Info("auth: "+what+" rejected", "reason", reason, "client", ClientAddr(r))
}

// --- setup and invite tokens ---

type tokenInfoJSON struct {
	Kind        string `json:"kind"`
	HomeName    string `json:"homeName"`
	PersonName  string `json:"personName,omitempty"`
	InviterName string `json:"inviterName,omitempty"`
	ExpiresAt   string `json:"expiresAt"`
}

func (a *Auth) tokenInfo(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Token string `json:"token"`
	}
	if err := wire.Decode(w, r, &body); err != nil {
		return err
	}
	ctx := r.Context()
	inv, err := a.checkToken(ctx, body.Token)
	if err != nil {
		return err
	}
	hs, err := a.st.Settings().Get(ctx)
	if err != nil {
		return err
	}
	out := tokenInfoJSON{Kind: string(inv.Kind), HomeName: hs.HomeName, ExpiresAt: wire.Time(inv.ExpiresAt)}
	if out.PersonName, err = a.personName(ctx, inv.PersonID); err != nil {
		return err
	}
	if out.InviterName, err = a.personName(ctx, inv.CreatedBy); err != nil {
		return err
	}
	wire.WriteJSON(w, http.StatusOK, out)
	return nil
}

// personName returns the name of a person, or "" for no id or a person that is gone.
func (a *Auth) personName(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	p, err := a.st.People().Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	return p.Name, err
}

// checkToken returns the usable setup link or invite behind a token: 404 token_invalid, 410 token_expired or
// token_used otherwise.
func (a *Auth) checkToken(ctx context.Context, token string) (store.Invite, error) {
	if !wellFormedToken(token) {
		return store.Invite{}, errTokenInvalid
	}
	inv, err := a.st.Invites().GetByTokenHash(ctx, hashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return store.Invite{}, errTokenInvalid
	}
	if err != nil {
		return store.Invite{}, err
	}
	switch {
	case inv.Kind != store.InviteSetup && inv.Kind != store.InviteMember:
		return store.Invite{}, errTokenInvalid
	case inv.Kind == store.InviteMember && inv.PersonID == "":
		return store.Invite{}, errTokenInvalid
	case !inv.UsedAt.IsZero():
		return store.Invite{}, errTokenUsed
	case !a.now().Before(inv.ExpiresAt):
		return store.Invite{}, errTokenExpired
	}
	return inv, nil
}

// tokenError maps the store's token errors of Invites.Consume.
func tokenError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return errTokenInvalid
	case errors.Is(err, store.ErrTokenExpired):
		return errTokenExpired
	case errors.Is(err, store.ErrTokenUsed):
		return errTokenUsed
	}
	return err
}

// --- registration with a token ---

func (a *Auth) registerBegin(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Token      string  `json:"token"`
		PersonName *string `json:"personName"`
		HomeName   *string `json:"homeName"`
	}
	if err := wire.Decode(w, r, &body); err != nil {
		return err
	}
	rp, wa, err := a.relyingParty()
	if err != nil {
		return err
	}
	ctx := r.Context()
	inv, err := a.checkToken(ctx, body.Token)
	if err != nil {
		return err
	}
	c := &ceremony{kind: kindRegister, rp: rp, tokenHash: inv.TokenHash, inviteKind: inv.Kind}
	if inv.Kind == store.InviteSetup { // a family member keeps the name the owner gave them
		if body.PersonName != nil {
			if c.personName, err = checkName("/personName", *body.PersonName); err != nil {
				return err
			}
		}
		if body.HomeName != nil {
			if c.homeName, err = checkName("/homeName", *body.HomeName); err != nil {
				return err
			}
		}
	}
	u := user{}
	if inv.PersonID != "" {
		u.person, err = a.st.People().Get(ctx, inv.PersonID)
		if errors.Is(err, store.ErrNotFound) {
			return errTokenInvalid
		}
		if err != nil {
			return err
		}
		if u.creds, err = a.credentials(ctx, u.person.ID, rp.ID); err != nil {
			return err
		}
	} else {
		// A setup link made before any owner existed: the owner is created when the passkey is stored.
		has, err := a.st.People().HasOwner(ctx)
		if err != nil {
			return err
		}
		if has {
			return errOwnerExists
		}
		if c.personName == "" {
			return wire.Invalid("/personName", "required", "is required to create the owner")
		}
		u.person = store.Person{Role: store.RoleOwner, Plugins: []string{"*"}, UserHandle: randomBytes(32)}
		c.newPerson = true
	}
	if c.personName != "" {
		u.person.Name = c.personName
	}
	c.person = u.person
	creation, data, err := wa.BeginRegistration(u, webauthn.WithExclusions(u.descriptors()))
	if err != nil {
		return fmt.Errorf("auth: begin registration: %w", err)
	}
	c.data = *data
	return a.begin(w, c, creation.Response)
}

func (a *Auth) registerFinish(w http.ResponseWriter, r *http.Request) error {
	var body finishBody
	if err := wire.Decode(w, r, &body); err != nil {
		return err
	}
	name, err := passkeyName("/passkeyName", body.PasskeyName, r.UserAgent())
	if err != nil {
		return err
	}
	now := a.now()
	c, ok := a.ceremonies.take(body.CeremonyID, now, isKind(kindRegister))
	if !ok {
		return errCeremony
	}
	ctx := r.Context()
	cred, err := a.verifyRegistration(r, c, body.Credential)
	if err != nil {
		a.registrationFailed(ctx, r, c, err)
		return err
	}
	blob, err := encodeCredential(c.rp.ID, cred)
	if err != nil {
		return err
	}
	token, sess := a.newSession("", passkeyID(cred.ID), r.UserAgent(), now)
	var (
		person store.Person
		hs     store.HomeSettings
		kind   store.InviteKind
	)
	err = a.st.InTx(ctx, func(tx store.Store) error {
		inv, err := tx.Invites().Consume(ctx, c.tokenHash, now)
		if err != nil {
			return tokenError(err)
		}
		kind = inv.Kind
		if person, err = registeringPerson(ctx, tx, c, inv, now); err != nil {
			return err
		}
		err = tx.Passkeys().Create(ctx, store.Passkey{
			ID: sess.PasskeyID, PersonID: person.ID, Name: name, Provider: providerName(cred.Authenticator.AAGUID),
			Credential: blob, CreatedAt: now, LastUsedAt: now,
		})
		if errors.Is(err, store.ErrConflict) {
			return errPasskeyExists
		}
		if err != nil {
			return err
		}
		if hs, err = tx.Settings().Get(ctx); err != nil {
			return err
		}
		if kind == store.InviteSetup && c.homeName != "" && c.homeName != hs.HomeName {
			hs.HomeName = c.homeName
			if err := tx.Settings().Put(ctx, hs); err != nil {
				return err
			}
		}
		sess.PersonID = person.ID
		return tx.Sessions().Create(ctx, sess)
	})
	if err != nil {
		a.registrationFailed(ctx, r, c, err)
		return err
	}
	action := registrationAction(kind)
	a.record(ctx, store.AuditRecord{
		At: now, PersonID: person.ID, Person: person.Name, Via: "admin", Action: action, Target: person.ID, OK: true,
		Args: clientDetails(r),
	})
	a.log.Info("auth: a passkey was registered with a one-time link", "action", action, "person", person.ID,
		"client", ClientAddr(r))
	if a.onSetup != nil {
		a.onSetup(person, hs)
	}
	return a.signIn(w, r, http.StatusCreated, token, person, sess, hs)
}

// registrationAction is the audit action of a registration: auth.setup with a setup link, auth.join with an invite.
func registrationAction(kind store.InviteKind) string {
	if kind == store.InviteMember {
		return "auth.join"
	}
	return "auth.setup"
}

// registrationFailed audits a registration whose finish failed for the client's reasons (the passkey did not verify,
// the link was used or expired meanwhile, the passkey exists): its action with ok=false, the reason and the client's
// address. Store failures are logged as internal errors instead; they say nothing about the client.
func (a *Auth) registrationFailed(ctx context.Context, r *http.Request, c *ceremony, err error) {
	we, ok := errors.AsType[*wire.Error](err)
	if !ok {
		return
	}
	a.record(ctx, store.AuditRecord{
		PersonID: c.person.ID, Person: c.person.Name, Via: "admin", Action: registrationAction(c.inviteKind),
		Target: c.person.ID, Error: we.Message, Args: clientDetails(r),
	})
}

// registeringPerson returns the person a registration stores the passkey for, inside its transaction: the one the
// token names (renamed when asked), or a new owner.
func registeringPerson(ctx context.Context, tx store.Store, c *ceremony, inv store.Invite, now time.Time) (store.Person, error) {
	if !c.newPerson {
		if inv.PersonID != c.person.ID {
			return store.Person{}, errTokenInvalid
		}
		p, err := tx.People().Get(ctx, inv.PersonID)
		if errors.Is(err, store.ErrNotFound) {
			return store.Person{}, errTokenInvalid
		}
		if err != nil {
			return store.Person{}, err
		}
		if !bytes.Equal(p.UserHandle, c.person.UserHandle) { // the person was replaced while the ceremony ran
			return store.Person{}, errTokenInvalid
		}
		if c.personName != "" && c.personName != p.Name {
			p.Name, p.UpdatedAt = c.personName, now
			if err := tx.People().Update(ctx, p); err != nil {
				return store.Person{}, err
			}
		}
		return p, nil
	}
	if inv.PersonID != "" {
		return store.Person{}, errTokenInvalid
	}
	has, err := tx.People().HasOwner(ctx)
	if err != nil {
		return store.Person{}, err
	}
	if has {
		return store.Person{}, errOwnerExists
	}
	p := c.person
	if p.ID, err = freePersonID(ctx, tx, slug(p.Name)); err != nil {
		return store.Person{}, err
	}
	p.CreatedAt, p.UpdatedAt = now, now
	if err := tx.People().Create(ctx, p); err != nil {
		return store.Person{}, err
	}
	return p, nil
}

// freePersonID returns base, or base with a "-2", "-3"… suffix, whichever no person has yet.
func freePersonID(ctx context.Context, tx store.Store, base string) (string, error) {
	for i := 1; i <= 100; i++ {
		id := base
		if i > 1 {
			suffix := "-" + strconv.Itoa(i)
			id = strings.TrimRight(base[:min(len(base), 32-len(suffix))], "-") + suffix
		}
		_, err := tx.People().Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return id, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("auth: no free person id")
}

// verifyRegistration checks an attestation against its ceremony: challenge, origin, RP ID, user presence and
// verification, algorithm and attestation signature.
func (a *Auth) verifyRegistration(r *http.Request, c *ceremony, raw json.RawMessage) (*webauthn.Credential, error) {
	wa, err := newWebAuthn(c.rp)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(raw)
	if err != nil {
		a.rejected("passkey registration", r, err)
		return nil, errWebAuthn
	}
	cred, err := wa.CreateCredential(user{person: c.person}, c.data, parsed)
	if err != nil {
		a.rejected("passkey registration", r, err)
		return nil, errWebAuthn
	}
	return cred, nil
}

// passkeyName checks a passkey label, or names the passkey after the platform when the client sent none.
func passkeyName(path, v, userAgent string) (string, error) {
	if strings.TrimSpace(v) == "" {
		return defaultPasskeyName(userAgent), nil
	}
	return checkName(path, v)
}

// --- sign-in ---

func (a *Auth) loginBegin(w http.ResponseWriter, r *http.Request) error {
	if err := wire.Decode(w, r, &struct{}{}); err != nil {
		return err
	}
	rp, wa, err := a.relyingParty()
	if err != nil {
		return err
	}
	assertion, data, err := wa.BeginDiscoverableLogin()
	if err != nil {
		return fmt.Errorf("auth: begin login: %w", err)
	}
	return a.begin(w, &ceremony{kind: kindLogin, rp: rp, data: *data}, assertion.Response)
}

func (a *Auth) loginFinish(w http.ResponseWriter, r *http.Request) error {
	var body finishBody
	if err := wire.Decode(w, r, &body); err != nil {
		return err
	}
	now := a.now()
	c, ok := a.ceremonies.take(body.CeremonyID, now, isKind(kindLogin))
	if !ok {
		return errCeremony
	}
	ctx := r.Context()
	person, cred, err := a.verifyLogin(r, c, body.Credential)
	if errors.Is(err, errLoginFailed) {
		a.record(ctx, store.AuditRecord{At: now, Via: "admin", Action: "auth.login_failed", Error: "the passkey did not verify",
			Args: clientDetails(r)})
	}
	if err != nil {
		return err
	}
	if err := a.recordUse(ctx, c.rp.ID, cred); err != nil {
		return err
	}
	hs, err := a.st.Settings().Get(ctx)
	if err != nil {
		return err
	}
	token, sess := a.newSession(person.ID, passkeyID(cred.ID), r.UserAgent(), now)
	if err := a.st.Sessions().Create(ctx, sess); err != nil {
		return err
	}
	a.record(ctx, store.AuditRecord{At: now, PersonID: person.ID, Person: person.Name, Via: "admin", Action: "auth.login", OK: true})
	return a.signIn(w, r, http.StatusOK, token, person, sess, hs)
}

// verifyLogin checks a discoverable assertion: the user handle names a person, the credential is one of theirs, and
// challenge, origin, RP ID, user verification and signature hold. Every failure is errLoginFailed.
func (a *Auth) verifyLogin(r *http.Request, c *ceremony, raw json.RawMessage) (store.Person, *webauthn.Credential, error) {
	wa, err := newWebAuthn(c.rp)
	if err != nil {
		return store.Person{}, nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(raw)
	if err != nil {
		a.rejected("sign-in", r, err)
		return store.Person{}, nil, errLoginFailed
	}
	ctx := r.Context()
	var (
		person    store.Person
		lookupErr error
	)
	lookup := func(_, userHandle []byte) (webauthn.User, error) {
		p, err := a.st.People().GetByUserHandle(ctx, userHandle)
		if err == nil {
			var creds []webauthn.Credential
			if creds, err = a.credentials(ctx, p.ID, c.rp.ID); err == nil {
				person = p
				return user{person: p, creds: creds}, nil
			}
		}
		lookupErr = err
		return nil, err
	}
	_, cred, err := wa.ValidatePasskeyLogin(lookup, c.data, parsed)
	if lookupErr != nil && !errors.Is(lookupErr, store.ErrNotFound) {
		return store.Person{}, nil, lookupErr
	}
	if err != nil {
		a.rejected("sign-in", r, err)
		return store.Person{}, nil, errLoginFailed
	}
	if err := a.checkClone(r, cred); err != nil {
		return store.Person{}, nil, err
	}
	return person, cred, nil
}

// checkClone refuses an assertion whose signature counter did not move forward: two copies of the private key may
// exist. Synced passkeys always report 0 and never trip it.
func (a *Auth) checkClone(r *http.Request, cred *webauthn.Credential) error {
	if cred.Authenticator.CloneWarning {
		a.log.Warn("auth: passkey signature counter went backwards; possible cloned authenticator",
			"passkey", passkeyID(cred.ID), "client", ClientAddr(r))
		return errLoginFailed
	}
	return nil
}

// --- step-up ---

func (a *Auth) stepUpBegin(w http.ResponseWriter, r *http.Request) error {
	p, err := a.Authenticate(r)
	if err != nil {
		return authError(err)
	}
	if err := wire.Decode(w, r, &struct{}{}); err != nil {
		return err
	}
	rp, wa, err := a.relyingParty()
	if err != nil {
		return err
	}
	creds, err := a.credentials(r.Context(), p.Person.ID, rp.ID)
	if err != nil {
		return err
	}
	if len(creds) == 0 {
		return wire.Errorf(http.StatusConflict, "conflict", "you have no passkey for this home to confirm with")
	}
	assertion, data, err := wa.BeginLogin(user{person: p.Person, creds: creds})
	if err != nil {
		return fmt.Errorf("auth: begin step-up: %w", err)
	}
	return a.begin(w, &ceremony{kind: kindStepUp, rp: rp, data: *data, person: p.Person, sessionID: p.Session.ID},
		assertion.Response)
}

func (a *Auth) stepUpFinish(w http.ResponseWriter, r *http.Request) error {
	p, err := a.Authenticate(r)
	if err != nil {
		return authError(err)
	}
	var body finishBody
	if err := wire.Decode(w, r, &body); err != nil {
		return err
	}
	now := a.now()
	c, ok := a.ceremonies.take(body.CeremonyID, now, ofSession(kindStepUp, p.Session.ID))
	if !ok {
		return errCeremony
	}
	ctx := r.Context()
	cred, err := a.verifyAssertion(r, c, p.Person, body.Credential)
	if errors.Is(err, errLoginFailed) {
		a.record(ctx, store.AuditRecord{At: now, PersonID: p.Person.ID, Person: p.Person.Name, Via: "admin",
			Action: "auth.step_up", Error: "the passkey did not verify", Args: clientDetails(r)})
	}
	if err != nil {
		return err
	}
	if err := a.recordUse(ctx, c.rp.ID, cred); err != nil {
		return err
	}
	if err := a.st.Sessions().SetStepUp(ctx, p.Session.ID, now); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return authError(ErrUnauthenticated)
		}
		return err
	}
	a.record(ctx, store.AuditRecord{At: now, PersonID: p.Person.ID, Person: p.Person.Name, Via: "admin", Action: "auth.step_up", OK: true})
	wire.WriteJSON(w, http.StatusOK, map[string]string{"stepUpUntil": wire.Time(now.Add(StepUpWindow))})
	return nil
}

// verifyAssertion checks an assertion of a known person (step-up): only their own passkeys count, so an assertion of
// somebody else fails like any other.
func (a *Auth) verifyAssertion(r *http.Request, c *ceremony, person store.Person, raw json.RawMessage) (*webauthn.Credential, error) {
	wa, err := newWebAuthn(c.rp)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(raw)
	if err != nil {
		a.rejected("step-up", r, err)
		return nil, errLoginFailed
	}
	creds, err := a.credentials(r.Context(), person.ID, c.rp.ID)
	if err != nil {
		return nil, err
	}
	cred, err := wa.ValidateLogin(user{person: person, creds: creds}, c.data, parsed)
	if err != nil {
		a.rejected("step-up", r, err)
		return nil, errLoginFailed
	}
	if err := a.checkClone(r, cred); err != nil {
		return nil, err
	}
	return cred, nil
}

// --- logout ---

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) error {
	p, err := a.Authenticate(r)
	if err != nil {
		if errors.Is(err, ErrUnauthenticated) {
			clearSessionCookie(w)
		}
		return authError(err)
	}
	if err := wire.Decode(w, r, &struct{}{}); err != nil {
		return err
	}
	ctx := r.Context()
	if err := a.deleteSession(ctx, p.Session.ID); err != nil {
		return err
	}
	clearSessionCookie(w)
	a.record(ctx, store.AuditRecord{PersonID: p.Person.ID, Person: p.Person.Name, Via: "admin", Action: "auth.logout", OK: true})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- another passkey for the signed-in person ---

// steppedUp authenticates the request and requires step-up.
func (a *Auth) steppedUp(r *http.Request) (Principal, error) {
	p, err := a.Authenticate(r)
	if err != nil {
		return Principal{}, authError(err)
	}
	if !p.SteppedUp(a.now()) {
		return Principal{}, errStepUp
	}
	return p, nil
}

func (a *Auth) passkeyBegin(w http.ResponseWriter, r *http.Request) error {
	p, err := a.steppedUp(r)
	if err != nil {
		return err
	}
	if err := wire.Decode(w, r, &struct{}{}); err != nil {
		return err
	}
	rp, wa, err := a.relyingParty()
	if err != nil {
		return err
	}
	u := user{person: p.Person}
	if u.creds, err = a.credentials(r.Context(), p.Person.ID, rp.ID); err != nil {
		return err
	}
	creation, data, err := wa.BeginRegistration(u, webauthn.WithExclusions(u.descriptors()))
	if err != nil {
		return fmt.Errorf("auth: begin registration: %w", err)
	}
	return a.begin(w, &ceremony{kind: kindAddPasskey, rp: rp, data: *data, person: p.Person, sessionID: p.Session.ID},
		creation.Response)
}

// passkeyJSON is the Passkey object of ADMIN-API §2.
type passkeyJSON struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Provider   string `json:"provider,omitempty"`
	CreatedAt  string `json:"createdAt"`
	LastUsedAt string `json:"lastUsedAt,omitempty"`
	Current    bool   `json:"current"`
}

func (a *Auth) passkeyFinish(w http.ResponseWriter, r *http.Request) error {
	// The step-up check comes before the ceremony is taken, so the SPA can step up and retry the same finish.
	p, err := a.steppedUp(r)
	if err != nil {
		return err
	}
	var body finishBody
	if err := wire.Decode(w, r, &body); err != nil {
		return err
	}
	name, err := passkeyName("/name", body.Name, r.UserAgent())
	if err != nil {
		return err
	}
	now := a.now()
	c, ok := a.ceremonies.take(body.CeremonyID, now, ofSession(kindAddPasskey, p.Session.ID))
	if !ok {
		return errCeremony
	}
	ctx := r.Context()
	failed := func(we *wire.Error) error {
		a.record(ctx, store.AuditRecord{At: now, PersonID: p.Person.ID, Person: p.Person.Name, Via: "admin",
			Action: "passkey.add", Target: name, Error: we.Message, Args: clientDetails(r)})
		return we
	}
	cred, err := a.verifyRegistration(r, c, body.Credential)
	if err != nil {
		if errors.Is(err, errWebAuthn) {
			return failed(errWebAuthn)
		}
		return err
	}
	blob, err := encodeCredential(c.rp.ID, cred)
	if err != nil {
		return err
	}
	pk := store.Passkey{
		ID: passkeyID(cred.ID), PersonID: p.Person.ID, Name: name, Provider: providerName(cred.Authenticator.AAGUID),
		Credential: blob, CreatedAt: now,
	}
	err = a.st.Passkeys().Create(ctx, pk)
	if errors.Is(err, store.ErrConflict) {
		return failed(errPasskeyExists)
	}
	if err != nil {
		return err
	}
	a.record(ctx, store.AuditRecord{At: now, PersonID: p.Person.ID, Person: p.Person.Name, Via: "admin", Action: "passkey.add", Target: name, OK: true})
	wire.WriteJSON(w, http.StatusCreated, passkeyJSON{ID: pk.ID, Name: pk.Name, Provider: pk.Provider, CreatedAt: wire.Time(now)})
	return nil
}
