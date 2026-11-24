package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devXpro/homie/internal/auth/wire"
	"github.com/devXpro/homie/internal/store"
)

func TestRegistrationAndLogin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	phone := newDevice()
	token := e.link()

	b := e.browser()
	rec := b.do(http.MethodPost, "/api/admin/auth/token", map[string]string{"token": token})
	wantStatus(t, rec, http.StatusOK)
	info := decodeAs[map[string]string](t, rec)
	if info["kind"] != "setup" || info["personName"] != "" || info["expiresAt"] != wire.Time(e.clock.Now().Add(SetupLinkTTL)) {
		t.Fatalf("token info %v", info)
	}

	// The begin options ask for a discoverable, user-verified passkey of this domain, without attestation.
	raw, id := b.begin("/api/admin/auth/register/begin",
		map[string]string{"token": token, "personName": " Роман ", "homeName": "Мой шалаш"})
	for _, want := range []string{`"rp":{"name":"Homie","id":"homie.example.com"}`, `"residentKey":"required"`,
		`"userVerification":"required"`, `"attestation":"none"`, `"name":"Роман"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("creation options lack %s: %s", want, raw)
		}
	}
	rec = b.do(http.MethodPost, "/api/admin/auth/register/finish",
		map[string]any{"ceremonyId": id, "credential": phone.create(t, raw), "passkeyName": "MacBook Pro"})
	wantStatus(t, rec, http.StatusCreated)
	m := decodeAs[me](t, rec)
	now := e.clock.Now()
	if m.Person.ID != "roman" || m.Person.Name != "Роман" || m.Person.Role != "owner" || m.Person.Passkeys != 1 ||
		!slices.Equal(m.Person.Plugins, []string{"*"}) || m.Home.Name != "Мой шалаш" || m.Home.Language != "ru" ||
		m.Version != "0.2.0" || m.Features["people"] || m.Session.StepUpUntil != wire.Time(now.Add(StepUpWindow)) ||
		m.Session.ExpiresAt != wire.Time(now.Add(AbsoluteTimeout)) ||
		m.Session.IdleExpiresAt != wire.Time(now.Add(IdleTimeout)) || m.Person.LastActiveAt != wire.Time(now) {
		t.Fatalf("Me %+v", m)
	}
	cookie := rec.Result().Cookies()[0]
	if cookie.Name != CookieName || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode ||
		cookie.Path != "/" || cookie.MaxAge != int(AbsoluteTimeout/time.Second) || !wellFormedToken(cookie.Value) {
		t.Fatalf("cookie %+v", cookie)
	}

	// Stored: the owner, the passkey (credential readable, of this RP), the consumed link, the home name, a session
	// keeping only the cookie's hash.
	pks, _ := e.st.Passkeys().ListByPerson(ctx, "roman")
	if len(pks) != 1 || pks[0].Name != "MacBook Pro" || pks[0].ID != passkeyID(phone.cred.ID) {
		t.Fatalf("passkeys %+v", pks)
	}
	rpID, cred, err := decodeCredential(pks[0].Credential)
	if err != nil || rpID != testDomain || !cred.Flags.BackupEligible || !cred.Flags.UserVerified {
		t.Fatalf("stored credential %q %+v %v", rpID, cred.Flags, err)
	}
	if hs, _ := e.st.Settings().Get(ctx); hs.HomeName != "Мой шалаш" {
		t.Fatalf("home name %q", hs.HomeName)
	}
	sessions, _ := e.st.Sessions().ListByPerson(ctx, "roman")
	if len(sessions) != 1 || sessions[0].TokenHash != hashToken(b.cookie) || sessions[0].Device != "Safari · Mac" ||
		sessions[0].ID != m.Session.ID || !strings.HasPrefix(sessions[0].ID, "s_") {
		t.Fatalf("sessions %+v", sessions)
	}
	if inv, _ := e.st.Invites().GetByTokenHash(ctx, hashToken(token)); inv.UsedAt.IsZero() {
		t.Fatal("the setup link was not consumed")
	}
	if len(e.setups) != 1 || e.setups[0].ID != "roman" {
		t.Fatalf("OnSetup %+v", e.setups)
	}
	if ready, _ := OwnerReady(ctx, e.st, testDomain); !ready {
		t.Fatal("owner not ready after setup")
	}

	rec = b.do(http.MethodGet, "/test/session", nil)
	wantStatus(t, rec, http.StatusOK)
	if rec.Body.String() != "roman" {
		t.Fatalf("principal %q", rec.Body)
	}

	// Logout ends the session and clears the cookie.
	rec = b.do(http.MethodPost, "/api/admin/auth/logout", map[string]any{})
	wantStatus(t, rec, http.StatusNoContent)
	if b.cookie != "" || !slices.Equal(e.endedSessions(), []string{m.Session.ID}) {
		t.Fatalf("after logout: cookie %q, ended %v", b.cookie, e.endedSessions())
	}
	wantError(t, b.do(http.MethodGet, "/test/session", nil), http.StatusUnauthorized, "unauthenticated")

	// A later sign-in on another browser with the same passkey.
	e.clock.Add(time.Hour)
	b2 := e.browser()
	b2.ua = safariIOS
	rec = b2.login(phone)
	wantStatus(t, rec, http.StatusOK)
	m = decodeAs[me](t, rec)
	if m.Person.ID != "roman" || m.Session.StepUpUntil != wire.Time(e.clock.Now().Add(StepUpWindow)) || b2.cookie == "" {
		t.Fatalf("login Me %+v", m)
	}
	pk, _ := e.st.Passkeys().Get(ctx, passkeyID(phone.cred.ID))
	if !pk.LastUsedAt.Equal(e.clock.Now()) {
		t.Fatalf("passkey last used %v", pk.LastUsedAt)
	}
	sessions, _ = e.st.Sessions().ListByPerson(ctx, "roman")
	if len(sessions) != 1 || sessions[0].Device != "Safari · iPhone" || sessions[0].PasskeyID != pk.ID {
		t.Fatalf("sessions %+v", sessions)
	}
	if got := e.actions(); !slices.Equal(got, []string{"setup.link", "auth.setup", "auth.logout", "auth.login"}) {
		t.Fatalf("audit %v", got)
	}
	for _, r := range e.audit()[1:] {
		if r.Kind != store.AuditAdmin || r.Via != "admin" || r.PersonID != "roman" || !r.OK {
			t.Fatalf("audit record %+v", r)
		}
	}
}

// A home imported from phase 1 already has its owner; the setup link adds the passkey to them.
func TestRegisterExistingOwner(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addPerson("roman", "Roman", store.RoleOwner)
	e.addPerson("tanya", "Tanya", store.RoleMember)

	token := e.link()
	b := e.browser()
	info := decodeAs[map[string]string](t, b.do(http.MethodPost, "/api/admin/auth/token", map[string]string{"token": token}))
	if info["personName"] != "Roman" {
		t.Fatalf("token info %v", info)
	}
	rec := b.register(newDevice(), token, nil) // no names: both stay
	wantStatus(t, rec, http.StatusCreated)
	if m := decodeAs[me](t, rec); m.Person.ID != "roman" || m.Person.Name != "Roman" || m.Home.Name != "" {
		t.Fatalf("Me %+v", m)
	}

	// Another link (homed admin setup-link on a new device) adds a second passkey and renames.
	rec = e.browser().register(newDevice(), e.link(), map[string]string{"personName": "Роман"})
	wantStatus(t, rec, http.StatusCreated)
	if m := decodeAs[me](t, rec); m.Person.ID != "roman" || m.Person.Name != "Роман" || m.Person.Passkeys != 2 {
		t.Fatalf("Me %+v", m)
	}
	if p, _ := e.st.People().Get(ctx, "roman"); p.Name != "Роман" {
		t.Fatalf("person %+v", p)
	}
}

// A family invite (phase 3) registers the invited person; the names in the request are ignored.
func TestRegisterWithInvite(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.owner(newDevice())
	tanya := e.addPerson("tanya", "Tanya", store.RoleMember)
	token := newToken()
	if err := e.st.Invites().Create(ctx, store.Invite{ID: "i_1", TokenHash: hashToken(token), Kind: store.InviteMember,
		PersonID: tanya.ID, CreatedBy: "roman", CreatedAt: e.clock.Now(), ExpiresAt: e.clock.Now().Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	b := e.browser()
	info := decodeAs[map[string]string](t, b.do(http.MethodPost, "/api/admin/auth/token", map[string]string{"token": token}))
	if info["kind"] != "member" || info["personName"] != "Tanya" || info["inviterName"] != "Roman" {
		t.Fatalf("token info %v", info)
	}
	rec := b.register(newDevice(), token, map[string]string{"personName": "Hacker", "homeName": "Not yours"})
	wantStatus(t, rec, http.StatusCreated)
	if m := decodeAs[me](t, rec); m.Person.ID != "tanya" || m.Person.Name != "Tanya" || m.Home.Name != "Мой шалаш" {
		t.Fatalf("Me %+v", m)
	}
	if got := e.actions(); got[len(got)-1] != "auth.join" {
		t.Fatalf("audit %v", got)
	}
}

func TestRegistrationRejected(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(d *device)
	}{
		{"wrong origin", func(d *device) { d.rp.Origin = "https://evil.example.com" }},
		{"origin over http", func(d *device) { d.rp.Origin = "http://" + testDomain }},
		{"wrong RP ID", func(d *device) { d.rp.ID = "evil.example.com" }},
		{"parent domain as RP ID", func(d *device) { d.rp.ID = "example.com" }},
		{"user not verified", func(d *device) { d.auth.Options.UserNotVerified = true }},
		{"user not present", func(d *device) { d.auth.Options.UserNotPresent = true }},
		{"backup state without eligibility", func(d *device) { d.auth.Options.BackupEligible = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			token := e.link()
			bad := newDevice()
			tc.spoil(bad)
			b := e.browser()
			wantError(t, b.register(bad, token, map[string]string{"personName": "Roman"}), http.StatusBadRequest, "webauthn_failed")
			if b.cookie != "" {
				t.Fatal("a cookie after a failed registration")
			}
			// Nothing was stored and the link still works.
			if n, _ := e.st.Passkeys().Count(context.Background()); n != 0 {
				t.Fatalf("%d passkeys stored", n)
			}
			wantStatus(t, e.browser().register(newDevice(), token, map[string]string{"personName": "Roman"}), http.StatusCreated)
		})
	}
}

func TestLoginRejected(t *testing.T) {
	stranger := func(_ *device) *device { return newDevice() } // a passkey this home never saw
	for _, tc := range []struct {
		name  string
		spoil func(d *device) *device
	}{
		{"wrong origin", func(d *device) *device { d.rp.Origin = "https://evil.example.com"; return d }},
		{"wrong RP ID", func(d *device) *device { d.rp.ID = "evil.example.com"; return d }},
		{"user not verified", func(d *device) *device { d.auth.Options.UserNotVerified = true; return d }},
		{"unknown passkey, unknown user", func(d *device) *device {
			s := stranger(d)
			s.auth.Options.UserHandle = []byte("nobody")
			return s
		}},
		{"unknown passkey of a known user", func(d *device) *device {
			s := stranger(d)
			s.auth.Options.UserHandle = d.auth.Options.UserHandle
			return s
		}},
		{"no user handle", func(d *device) *device { d.auth.Options.UserHandle = nil; return d }},
		{"backup eligibility changed", func(d *device) *device {
			d.auth.Options.BackupEligible, d.auth.Options.BackupState = false, false
			return d
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			phone := newDevice()
			e.owner(phone)
			bad := *phone
			b := e.browser()
			wantError(t, b.login(tc.spoil(&bad)), http.StatusUnauthorized, "login_failed")
			if b.cookie != "" {
				t.Fatal("a cookie after a failed sign-in")
			}
			recs := e.audit()
			if last := recs[len(recs)-1]; last.Action != "auth.login_failed" || last.OK || last.PersonID != "" {
				t.Fatalf("audit %+v", last)
			}
			wantStatus(t, e.browser().login(phone), http.StatusOK)
		})
	}
}

// A ceremony is single use and lives 5 minutes; an answer to one challenge does not finish another.
func TestCeremonyReplay(t *testing.T) {
	e := newEnv(t)
	phone := newDevice()
	e.owner(phone)
	b := e.browser()

	raw, id := b.begin("/api/admin/auth/login/begin", map[string]any{})
	finish := map[string]any{"ceremonyId": id, "credential": phone.get(t, raw)}
	wantStatus(t, b.do(http.MethodPost, "/api/admin/auth/login/finish", finish), http.StatusOK)
	wantError(t, b.do(http.MethodPost, "/api/admin/auth/login/finish", finish), http.StatusBadRequest, "webauthn_failed")

	// The answer to ceremony A sent for ceremony B: the challenge does not match.
	rawA, _ := b.begin("/api/admin/auth/login/begin", map[string]any{})
	_, idB := b.begin("/api/admin/auth/login/begin", map[string]any{})
	wantError(t, b.do(http.MethodPost, "/api/admin/auth/login/finish",
		map[string]any{"ceremonyId": idB, "credential": phone.get(t, rawA)}), http.StatusUnauthorized, "login_failed")

	// A sign-in ceremony cannot finish a registration, and the wrong endpoint does not burn it.
	raw, id = b.begin("/api/admin/auth/login/begin", map[string]any{})
	answer := phone.get(t, raw)
	wantError(t, b.do(http.MethodPost, "/api/admin/auth/register/finish",
		map[string]any{"ceremonyId": id, "credential": answer}), http.StatusBadRequest, "webauthn_failed")
	wantStatus(t, b.do(http.MethodPost, "/api/admin/auth/login/finish",
		map[string]any{"ceremonyId": id, "credential": answer}), http.StatusOK)

	// Expired after 5 minutes.
	raw, id = b.begin("/api/admin/auth/login/begin", map[string]any{})
	answer = phone.get(t, raw)
	e.clock.Add(CeremonyTTL)
	wantError(t, b.do(http.MethodPost, "/api/admin/auth/login/finish",
		map[string]any{"ceremonyId": id, "credential": answer}), http.StatusBadRequest, "webauthn_failed")

	// A registration finish replayed after it succeeded.
	token := e.link()
	b2 := e.browser()
	raw, id = b2.begin("/api/admin/auth/register/begin", map[string]string{"token": token})
	d := newDevice()
	finish = map[string]any{"ceremonyId": id, "credential": d.create(t, raw)}
	wantStatus(t, b2.do(http.MethodPost, "/api/admin/auth/register/finish", finish), http.StatusCreated)
	wantError(t, b2.do(http.MethodPost, "/api/admin/auth/register/finish", finish), http.StatusBadRequest, "webauthn_failed")
}

func TestSetupTokenSingleUseAndExpiry(t *testing.T) {
	ctx := context.Background()
	names := map[string]string{"personName": "Roman"}

	t.Run("used", func(t *testing.T) {
		e := newEnv(t)
		token := e.link()
		wantStatus(t, e.browser().register(newDevice(), token, names), http.StatusCreated)
		b := e.browser()
		wantError(t, b.do(http.MethodPost, "/api/admin/auth/token", map[string]string{"token": token}), http.StatusGone, "token_used")
		wantError(t, b.do(http.MethodPost, "/api/admin/auth/register/begin", map[string]string{"token": token}), http.StatusGone, "token_used")
	})

	t.Run("two registrations race for one link", func(t *testing.T) {
		e := newEnv(t)
		token := e.link()
		b1, b2 := e.browser(), e.browser()
		d1, d2 := newDevice(), newDevice()
		raw1, id1 := b1.begin("/api/admin/auth/register/begin", map[string]string{"token": token, "personName": "Roman"})
		raw2, id2 := b2.begin("/api/admin/auth/register/begin", map[string]string{"token": token, "personName": "Mallory"})
		wantStatus(t, b1.do(http.MethodPost, "/api/admin/auth/register/finish",
			map[string]any{"ceremonyId": id1, "credential": d1.create(t, raw1)}), http.StatusCreated)
		wantError(t, b2.do(http.MethodPost, "/api/admin/auth/register/finish",
			map[string]any{"ceremonyId": id2, "credential": d2.create(t, raw2)}), http.StatusGone, "token_used")
		if people, _ := e.st.People().List(ctx); len(people) != 1 || people[0].Name != "Roman" {
			t.Fatalf("people %+v", people)
		}
	})

	t.Run("expired", func(t *testing.T) {
		e := newEnv(t)
		token := e.link()
		e.clock.Add(SetupLinkTTL)
		b := e.browser()
		wantError(t, b.do(http.MethodPost, "/api/admin/auth/token", map[string]string{"token": token}), http.StatusGone, "token_expired")
		wantError(t, b.do(http.MethodPost, "/api/admin/auth/register/begin", map[string]string{"token": token}), http.StatusGone, "token_expired")
	})

	t.Run("expires between begin and finish", func(t *testing.T) {
		e := newEnv(t)
		token := e.link()
		e.clock.Add(SetupLinkTTL - time.Minute)
		b := e.browser()
		d := newDevice()
		raw, id := b.begin("/api/admin/auth/register/begin", map[string]string{"token": token, "personName": "Roman"})
		e.clock.Add(2 * time.Minute)
		wantError(t, b.do(http.MethodPost, "/api/admin/auth/register/finish",
			map[string]any{"ceremonyId": id, "credential": d.create(t, raw)}), http.StatusGone, "token_expired")
		// The transaction rolled back: no owner, no passkey, no session.
		if people, _ := e.st.People().List(ctx); len(people) != 0 {
			t.Fatalf("people %+v", people)
		}
		if n, _ := e.st.Passkeys().Count(ctx); n != 0 || b.cookie != "" {
			t.Fatalf("passkeys %d, cookie %q", n, b.cookie)
		}
	})

	t.Run("a newer link revokes the older one", func(t *testing.T) {
		e := newEnv(t)
		older := e.link()
		newer := e.link()
		b := e.browser()
		wantError(t, b.do(http.MethodPost, "/api/admin/auth/token", map[string]string{"token": older}), http.StatusGone, "token_used")
		wantStatus(t, b.do(http.MethodPost, "/api/admin/auth/token", map[string]string{"token": newer}), http.StatusOK)
	})

	t.Run("unknown and malformed", func(t *testing.T) {
		e := newEnv(t)
		b := e.browser()
		for _, token := range []string{newToken(), "short", "", strings.Repeat("!", 43)} {
			wantError(t, b.do(http.MethodPost, "/api/admin/auth/token", map[string]string{"token": token}), http.StatusNotFound, "token_invalid")
		}
	})
}

func TestRegisterBeginChecks(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	token := e.link()
	for _, tc := range []struct {
		body       map[string]any
		path, code string
	}{
		{map[string]any{"token": token}, "/personName", "required"},
		{map[string]any{"token": token, "personName": "   "}, "/personName", "length"},
		{map[string]any{"token": token, "personName": strings.Repeat("я", 41)}, "/personName", "length"},
		{map[string]any{"token": token, "personName": "Roman\nadmin"}, "/personName", "pattern"},
		{map[string]any{"token": token, "personName": "Roman", "homeName": ""}, "/homeName", "length"},
		{map[string]any{"token": token, "personName": 5}, "/personName", "type"},
	} {
		body := wantError(t, b.do(http.MethodPost, "/api/admin/auth/register/begin", tc.body), http.StatusBadRequest, "invalid_args")
		if len(body.Error.Fields) != 1 || body.Error.Fields[0].Path != tc.path || body.Error.Fields[0].Code != tc.code {
			t.Errorf("%v: fields %+v", tc.body, body.Error.Fields)
		}
	}
	// 40 characters are fine, counted as characters, not bytes.
	wantStatus(t, b.do(http.MethodPost, "/api/admin/auth/register/begin",
		map[string]any{"token": token, "personName": strings.Repeat("я", 40)}), http.StatusOK)
}

// The same authenticator cannot be registered twice.
func TestPasskeyExists(t *testing.T) {
	e := newEnv(t)
	phone := newDevice()
	e.owner(phone)
	copyOfPhone := *phone
	wantError(t, e.browser().register(&copyOfPhone, e.link(), nil), http.StatusConflict, "passkey_exists")
}

// The sign counter of a hardware key must move forward; a repeat means a cloned key.
func TestCloneWarning(t *testing.T) {
	e := newEnv(t)
	key := newDevice()
	key.auth.Options.BackupEligible, key.auth.Options.BackupState = false, false
	key.cred.Counter = 5
	e.owner(key)
	wantError(t, e.browser().login(key), http.StatusUnauthorized, "login_failed")
	key.cred.Counter = 6
	wantStatus(t, e.browser().login(key), http.StatusOK)
	key.cred.Counter = 6
	wantError(t, e.browser().login(key), http.StatusUnauthorized, "login_failed")
}

func TestNoRelayDomain(t *testing.T) {
	e := newEnv(t)
	token := e.link()
	e.noRP = true
	b := e.browser()
	b.origin = ""
	wantError(t, b.do(http.MethodPost, "/api/admin/auth/login/begin", map[string]any{}), http.StatusServiceUnavailable, "home_not_ready")
	wantError(t, b.do(http.MethodPost, "/api/admin/auth/register/begin",
		map[string]any{"token": token, "personName": "Roman"}), http.StatusServiceUnavailable, "home_not_ready")
	wantError(t, b.do(http.MethodPost, "/local/api/admin/local/setup-link", map[string]any{}), http.StatusConflict, "relay_not_ready")
}

// Failed registrations, sign-ins, step-ups and passkey additions all reach the audit, with the client's address
// (review L3); a successful registration names it too.
func TestFailuresAreAudited(t *testing.T) {
	e := newEnv(t)
	phone := newDevice()
	token := e.link()
	attacker := e.browser()
	attacker.xff = "203.0.113.7, 172.18.0.2"
	bad := *phone
	bad.rp.Origin = "https://evil.example.com"
	wantError(t, attacker.register(&bad, token, map[string]string{"personName": "Mallory"}), http.StatusBadRequest, "webauthn_failed")
	owner := e.browser()
	owner.xff = "198.51.100.20, 172.18.0.2"
	wantStatus(t, owner.register(phone, token, map[string]string{"personName": "Roman"}), http.StatusCreated)
	// The link is used now: a second finish with a fresh ceremony fails at the token.
	wantError(t, attacker.do(http.MethodPost, "/api/admin/auth/register/begin", map[string]any{"token": token}), http.StatusGone, "token_used")

	wantError(t, attacker.login(newDevice()), http.StatusUnauthorized, "login_failed")
	wantError(t, owner.stepUp(newDevice()), http.StatusUnauthorized, "login_failed")
	e.clock.Add(time.Second) // still stepped up from the registration
	raw, id := owner.begin("/api/admin/me/passkeys/begin", map[string]any{})
	other := newDevice()
	other.rp.ID = "evil.example.com"
	wantError(t, owner.do(http.MethodPost, "/api/admin/me/passkeys/finish",
		map[string]any{"ceremonyId": id, "credential": other.create(t, raw), "name": "Evil"}), http.StatusBadRequest, "webauthn_failed")

	want := []struct {
		action, client string
		ok             bool
	}{
		{"setup.link", "", true},
		{"auth.setup", "203.0.113.7", false},
		{"auth.setup", "198.51.100.20", true},
		{"auth.login_failed", "203.0.113.7", false},
		{"auth.step_up", "198.51.100.20", false},
		{"passkey.add", "198.51.100.20", false},
	}
	recs := e.audit()
	if len(recs) != len(want) {
		t.Fatalf("audit %+v", recs)
	}
	for i, w := range want {
		r := recs[i]
		var details struct {
			Client string `json:"client"`
		}
		if len(r.Args) > 0 {
			_ = json.Unmarshal(r.Args, &details)
		}
		if r.Action != w.action || r.OK != w.ok || details.Client != w.client || !w.ok && r.Error == "" {
			t.Errorf("record %d = %+v (%s), want %+v", i, r, r.Args, w)
		}
	}
	if recs[1].Person != "Mallory" || recs[4].PersonID == "" || recs[5].Target != "Evil" {
		t.Errorf("who: %+v", recs)
	}
}
