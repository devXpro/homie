package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devXpro/homie/internal/auth/wire"
	"github.com/devXpro/homie/internal/store"
)

func TestStepUp(t *testing.T) {
	e := newEnv(t)
	phone := newDevice()
	b := e.owner(phone)

	// Signing in counts: dangerous actions pass for 5 minutes.
	wantStatus(t, b.do(http.MethodPost, "/test/dangerous", map[string]any{}), http.StatusNoContent)
	e.clock.Add(StepUpWindow - time.Millisecond)
	wantStatus(t, b.do(http.MethodPost, "/test/dangerous", map[string]any{}), http.StatusNoContent)
	e.clock.Add(time.Millisecond)
	wantError(t, b.do(http.MethodPost, "/test/dangerous", map[string]any{}), http.StatusForbidden, "step_up_required")

	rec := b.stepUp(phone)
	wantStatus(t, rec, http.StatusOK)
	until := decodeAs[map[string]string](t, rec)["stepUpUntil"]
	if until != wire.Time(e.clock.Now().Add(StepUpWindow)) {
		t.Fatalf("stepUpUntil %q", until)
	}
	wantStatus(t, b.do(http.MethodPost, "/test/dangerous", map[string]any{}), http.StatusNoContent)
	if got := e.actions(); got[len(got)-1] != "auth.step_up" {
		t.Fatalf("audit %v", got)
	}

	// The step-up options allow only the person's own passkeys.
	raw, _ := b.begin("/api/admin/auth/stepup/begin", map[string]any{})
	if !slices.Contains(allowed(t, raw), passkeyID(phone.cred.ID)) || len(allowed(t, raw)) != 1 {
		t.Fatalf("allowCredentials %v", allowed(t, raw))
	}
}

func TestStepUpRejected(t *testing.T) {
	e := newEnv(t)
	phone := newDevice()
	b := e.owner(phone)
	e.clock.Add(StepUpWindow)

	// Another person's passkey does not step up Roman's session.
	tanyaPhone := newDevice()
	tanya := e.addPerson("tanya", "Tanya", store.RoleMember)
	tanyaPhone.auth.Options.UserHandle = tanya.UserHandle
	token := newToken()
	ctx := context.Background()
	if err := e.st.Invites().Create(ctx, store.Invite{ID: "i_t", TokenHash: hashToken(token), Kind: store.InviteMember,
		PersonID: "tanya", CreatedAt: e.clock.Now(), ExpiresAt: e.clock.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, e.browser().register(tanyaPhone, token, nil), http.StatusCreated)
	wantError(t, b.stepUp(tanyaPhone), http.StatusUnauthorized, "login_failed")

	// A step-up ceremony begun in one session cannot be finished in another.
	other := e.browser()
	wantStatus(t, other.login(phone), http.StatusOK)
	raw, id := b.begin("/api/admin/auth/stepup/begin", map[string]any{})
	answer := phone.get(t, raw)
	wantError(t, other.do(http.MethodPost, "/api/admin/auth/stepup/finish",
		map[string]any{"ceremonyId": id, "credential": answer}), http.StatusBadRequest, "webauthn_failed")
	wantStatus(t, b.do(http.MethodPost, "/api/admin/auth/stepup/finish",
		map[string]any{"ceremonyId": id, "credential": answer}), http.StatusOK)

	// Without a session there is no step-up.
	wantError(t, e.browser().do(http.MethodPost, "/api/admin/auth/stepup/begin", map[string]any{}), http.StatusUnauthorized, "unauthenticated")
}

func TestSessionLifetime(t *testing.T) {
	ctx := context.Background()

	t.Run("idle", func(t *testing.T) {
		e := newEnv(t)
		b := e.owner(newDevice())
		sessions, _ := e.st.Sessions().ListByPerson(ctx, "roman")
		e.clock.Add(IdleTimeout - time.Millisecond)
		wantStatus(t, b.do(http.MethodGet, "/test/session", nil), http.StatusOK) // touches
		e.clock.Add(IdleTimeout - time.Millisecond)
		wantStatus(t, b.do(http.MethodGet, "/test/session", nil), http.StatusOK)
		e.clock.Add(IdleTimeout)
		wantError(t, b.do(http.MethodGet, "/test/session", nil), http.StatusUnauthorized, "unauthenticated")
		if left, _ := e.st.Sessions().ListByPerson(ctx, "roman"); len(left) != 0 {
			t.Fatalf("expired session kept: %+v", left)
		}
		if !slices.Equal(e.endedSessions(), []string{sessions[0].ID}) {
			t.Fatalf("OnSessionEnd %v", e.endedSessions())
		}
	})

	t.Run("absolute", func(t *testing.T) {
		e := newEnv(t)
		b := e.owner(newDevice())
		for elapsed := time.Duration(0); elapsed+11*time.Hour < AbsoluteTimeout; elapsed += 11 * time.Hour {
			e.clock.Add(11 * time.Hour)
			wantStatus(t, b.do(http.MethodGet, "/test/session", nil), http.StatusOK)
		}
		e.clock.Add(11 * time.Hour)
		wantError(t, b.do(http.MethodGet, "/test/session", nil), http.StatusUnauthorized, "unauthenticated")
	})

	t.Run("touch at most once a minute, never by an event stream", func(t *testing.T) {
		e := newEnv(t)
		b := e.owner(newDevice())
		start := e.clock.Now()
		lastSeen := func() time.Time {
			s, _ := e.st.Sessions().ListByPerson(ctx, "roman")
			return s[0].LastSeenAt
		}
		e.clock.Add(59 * time.Second)
		b.do(http.MethodGet, "/test/session", nil)
		if !lastSeen().Equal(start) {
			t.Fatalf("touched after 59 s: %v", lastSeen())
		}
		e.clock.Add(time.Minute)
		b.header.Set("Accept", "text/event-stream")
		b.do(http.MethodGet, "/test/session", nil)
		if !lastSeen().Equal(start) {
			t.Fatal("an event stream touched the session")
		}
		b.header.Del("Accept")
		b.do(http.MethodGet, "/test/session", nil)
		if !lastSeen().Equal(e.clock.Now()) {
			t.Fatalf("not touched after 2 minutes: %v", lastSeen())
		}
	})

	t.Run("bad cookies", func(t *testing.T) {
		e := newEnv(t)
		b := e.owner(newDevice())
		tampered := []byte(b.cookie)
		tampered[0] ^= 'A' ^ 'B' // flips between two base64url letters, so the cookie stays well formed
		if tampered[0] == b.cookie[0] || !wellFormedToken(string(tampered)) {
			tampered[0] = 'x'
		}
		for _, c := range []string{"", "nope", newToken(), string(tampered)} {
			x := e.browser()
			x.cookie = c
			wantError(t, x.do(http.MethodGet, "/test/session", nil), http.StatusUnauthorized, "unauthenticated")
		}
	})

	t.Run("person removed", func(t *testing.T) {
		e := newEnv(t)
		b := e.owner(newDevice())
		if err := e.st.People().Delete(ctx, "roman"); err != nil {
			t.Fatal(err)
		}
		wantError(t, b.do(http.MethodGet, "/test/session", nil), http.StatusUnauthorized, "unauthenticated")
	})

	t.Run("sweep", func(t *testing.T) {
		e := newEnv(t)
		phone := newDevice()
		e.owner(phone)
		e.clock.Add(IdleTimeout - time.Minute)
		fresh := e.browser()
		wantStatus(t, fresh.login(phone), http.StatusOK)
		e.clock.Add(time.Minute + time.Millisecond)
		e.a.sweep(ctx)
		left, _ := e.st.Sessions().ListByPerson(ctx, "roman")
		if len(left) != 1 || left[0].TokenHash != hashToken(fresh.cookie) {
			t.Fatalf("sessions left: %+v", left)
		}
	})
}

func TestNewPrincipal(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	p := newPrincipal(store.Person{ID: "roman"}, store.Session{CreatedAt: t0, LastSeenAt: t0.Add(AbsoluteTimeout - 2*time.Hour)})
	if !p.ExpiresAt.Equal(t0.Add(AbsoluteTimeout)) || !p.IdleExpiresAt.Equal(p.ExpiresAt) || !p.StepUpUntil.IsZero() ||
		p.SteppedUp(t0) {
		t.Fatalf("principal %+v", p)
	}
}

func TestAuthenticatePrincipal(t *testing.T) {
	e := newEnv(t)
	b := e.owner(newDevice())
	start := e.clock.Now()
	e.clock.Add(time.Hour)
	req, _ := http.NewRequest(http.MethodGet, "https://"+testDomain+"/api/admin/session", http.NoBody)
	req.Header.Set("Cookie", CookieName+"="+b.cookie)
	p, err := e.a.Authenticate(req)
	if err != nil {
		t.Fatal(err)
	}
	now := e.clock.Now()
	if p.Person.ID != "roman" || !p.ExpiresAt.Equal(start.Add(AbsoluteTimeout)) || !p.IdleExpiresAt.Equal(now.Add(IdleTimeout)) ||
		!p.StepUpUntil.Equal(start.Add(StepUpWindow)) || p.SteppedUp(now) || p.Session.PasskeyID == "" {
		t.Fatalf("principal %+v", p)
	}
}

func TestAddPasskey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	phone := newDevice()
	b := e.owner(phone)
	mac := newDevice()

	e.clock.Add(2 * time.Minute)
	raw, id := b.begin("/api/admin/me/passkeys/begin", map[string]any{})
	if ex := excluded(t, raw); !slices.Equal(ex, []string{passkeyID(phone.cred.ID)}) {
		t.Fatalf("excludeCredentials %v", ex)
	}
	answer := mac.create(t, raw)

	// Step-up expired between begin and finish: 403, and the ceremony survives for the retry after step-up.
	e.clock.Add(StepUpWindow - 2*time.Minute)
	finish := map[string]any{"ceremonyId": id, "credential": answer, "name": "Mac mini"}
	wantError(t, b.do(http.MethodPost, "/api/admin/me/passkeys/finish", finish), http.StatusForbidden, "step_up_required")
	wantError(t, b.do(http.MethodPost, "/api/admin/me/passkeys/begin", map[string]any{}), http.StatusForbidden, "step_up_required")
	wantStatus(t, b.stepUp(phone), http.StatusOK)
	rec := b.do(http.MethodPost, "/api/admin/me/passkeys/finish", finish)
	wantStatus(t, rec, http.StatusCreated)
	pk := decodeAs[map[string]any](t, rec)
	if pk["id"] != passkeyID(mac.cred.ID) || pk["name"] != "Mac mini" || pk["current"] != false || pk["lastUsedAt"] != nil {
		t.Fatalf("passkey %v", pk)
	}
	if pks, _ := e.st.Passkeys().ListByPerson(ctx, "roman"); len(pks) != 2 {
		t.Fatalf("passkeys %+v", pks)
	}
	if got := e.actions(); got[len(got)-1] != "passkey.add" || e.audit()[len(got)-1].Target != "Mac mini" {
		t.Fatalf("audit %v", got)
	}
	// The new passkey signs in.
	wantStatus(t, e.browser().login(mac), http.StatusOK)

	// The same authenticator again.
	raw, id = b.begin("/api/admin/me/passkeys/begin", map[string]any{})
	wantError(t, b.do(http.MethodPost, "/api/admin/me/passkeys/finish",
		map[string]any{"ceremonyId": id, "credential": mac.create(t, raw)}), http.StatusConflict, "passkey_exists")

	// Without a session.
	wantError(t, e.browser().do(http.MethodPost, "/api/admin/me/passkeys/begin", map[string]any{}), http.StatusUnauthorized, "unauthenticated")
}

func TestCSRF(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		name   string
		mutate func(b *browser)
		want   int
	}{
		{"same origin", func(*browser) {}, http.StatusOK},
		{"no Origin header", func(b *browser) { b.origin = "" }, http.StatusOK},
		{"same-origin fetch", func(b *browser) { b.header.Set("Sec-Fetch-Site", "same-origin") }, http.StatusOK},
		{"no X-Homie", func(b *browser) { b.header.Set("X-Homie", "0") }, http.StatusForbidden},
		{"foreign Origin", func(b *browser) { b.origin = "https://evil.example.com" }, http.StatusForbidden},
		{"http Origin", func(b *browser) { b.origin = "http://" + testDomain }, http.StatusForbidden},
		{"cross-site fetch", func(b *browser) { b.header.Set("Sec-Fetch-Site", "cross-site") }, http.StatusForbidden},
		{"same-site fetch", func(b *browser) { b.header.Set("Sec-Fetch-Site", "same-site") }, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := e.browser()
			tc.mutate(b)
			rec := b.do(http.MethodPost, "/api/admin/auth/login/begin", map[string]any{})
			if tc.want == http.StatusForbidden {
				wantError(t, rec, tc.want, "csrf_failed")
				return
			}
			wantStatus(t, rec, tc.want)
		})
	}
}

func TestRequestFormat(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	rec := b.do(http.MethodGet, "/api/admin/auth/login/begin", nil)
	wantError(t, rec, http.StatusMethodNotAllowed, "method_not_allowed")
	if rec.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("Allow %q", rec.Header().Get("Allow"))
	}
	wantError(t, b.do(http.MethodPost, "/api/admin/auth/nope", map[string]any{}), http.StatusNotFound, "not_found")
	wantError(t, b.do(http.MethodPost, "/api/admin/auth/token", "[1]"), http.StatusBadRequest, "invalid_json")
	wantError(t, b.do(http.MethodPost, "/api/admin/auth/token", "{"), http.StatusBadRequest, "invalid_json")
	wantError(t, b.do(http.MethodPost, "/api/admin/auth/token", `{"token":"a"} {}`), http.StatusBadRequest, "invalid_json")
	b.header.Set("Content-Type", "text/plain")
	wantError(t, b.do(http.MethodPost, "/api/admin/auth/token", `{"token":"a"}`), http.StatusUnsupportedMediaType, "unsupported_media_type")
	b.header.Del("Content-Type")
	big := `{"token":"` + string(make([]byte, wire.MaxBody)) + `"}`
	wantError(t, b.do(http.MethodPost, "/api/admin/auth/token", big), http.StatusRequestEntityTooLarge, "too_large")

	rec = b.do(http.MethodPost, "/api/admin/auth/login/begin", nil) // an empty body is {}
	wantStatus(t, rec, http.StatusOK)
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers %v", rec.Header())
	}
}

// auth/token and registration: every request counts, per client address and in total (ADMIN-API §1.7).
func TestRateLimitTokens(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	b.xff = "203.0.113.7, 172.18.0.2" // the client as Caddy appended it, then Caddy as frps saw it
	token := map[string]any{"token": "x"}
	for range tokenPerAddress {
		wantError(t, b.do(http.MethodPost, "/api/admin/auth/token", token), http.StatusNotFound, "token_invalid")
	}
	rec := b.do(http.MethodPost, "/api/admin/auth/token", token)
	wantError(t, rec, http.StatusTooManyRequests, "rate_limited")
	if after, _ := strconv.Atoi(rec.Header().Get("Retry-After")); after != 3 {
		t.Fatalf("Retry-After %q", rec.Header().Get("Retry-After"))
	}
	// A spoofed left-most value does not give the same client a new bucket; registration shares the class.
	b.xff = "198.51.100.1, 203.0.113.7, 172.18.0.2"
	wantError(t, b.do(http.MethodPost, "/api/admin/auth/register/begin", token), http.StatusTooManyRequests, "rate_limited")
	// The class is its own: the same client still signs in.
	wantStatus(t, b.do(http.MethodPost, "/api/admin/auth/login/begin", map[string]any{}), http.StatusOK)
	// Another client is not affected; a token comes back after 3 s.
	other := e.browser()
	other.xff = "203.0.113.8, 172.18.0.2"
	wantError(t, other.do(http.MethodPost, "/api/admin/auth/token", token), http.StatusNotFound, "token_invalid")
	e.clock.Add(3*time.Second + time.Millisecond)
	wantError(t, b.do(http.MethodPost, "/api/admin/auth/token", token), http.StatusNotFound, "token_invalid")

	// 60 a minute in total over the internet's addresses: 60 - 20 (b) - 1 (other) + 3 (refilled in 3 s) - 1 (b) = 41.
	for i := range 41 {
		c := e.browser()
		c.xff = "198.51.100." + strconv.Itoa(i) + ", 172.18.0.2"
		wantError(t, c.do(http.MethodPost, "/api/admin/auth/token", token), http.StatusNotFound, "token_invalid")
	}
	c := e.browser()
	c.xff = "192.0.2.1, 172.18.0.2"
	wantError(t, c.do(http.MethodPost, "/api/admin/auth/token", token), http.StatusTooManyRequests, "rate_limited")
	// A request without a client address (loopback, no X-Forwarded-For) has its own bucket, outside the total.
	local := e.browser()
	wantError(t, local.do(http.MethodPost, "/api/admin/auth/token", token), http.StatusNotFound, "token_invalid")
	// Sign-in is a class of its own and has no shared bucket: the spent total does not stop it.
	wantStatus(t, c.do(http.MethodPost, "/api/admin/auth/login/begin", map[string]any{}), http.StatusOK)
	// Logout is not an auth ceremony and is not limited.
	wantError(t, c.do(http.MethodPost, "/api/admin/auth/logout", map[string]any{}), http.StatusUnauthorized, "unauthenticated")
}

// Sign-in counts begins and failed finishes per client address, with no shared bucket: nobody can spend the owner's.
func TestRateLimitLogin(t *testing.T) {
	e := newEnv(t)
	d := newDevice()
	owner := e.owner(d)
	owner.xff = "198.51.100.20, 172.18.0.2"
	attacker := e.browser()
	attacker.xff = "203.0.113.7, 172.18.0.2"
	bogus := map[string]any{"ceremonyId": "nope", "credential": map[string]any{}}
	for range failuresPerKey {
		wantError(t, attacker.do(http.MethodPost, "/api/admin/auth/login/finish", bogus), http.StatusBadRequest, "webauthn_failed")
	}
	// The address's failures are spent: its finishes and begins wait.
	wantError(t, attacker.do(http.MethodPost, "/api/admin/auth/login/finish", bogus), http.StatusTooManyRequests, "rate_limited")
	wantError(t, attacker.do(http.MethodPost, "/api/admin/auth/login/begin", map[string]any{}), http.StatusTooManyRequests, "rate_limited")
	// The owner signs in from their own address, again and again: successes cost no failure.
	for range failuresPerKey + 2 {
		wantStatus(t, owner.login(d), http.StatusOK)
	}
	// Many addresses failing do not add up to anything shared.
	for i := range 50 {
		c := e.browser()
		c.xff = "192.0.2." + strconv.Itoa(i) + ", 172.18.0.2"
		wantError(t, c.do(http.MethodPost, "/api/admin/auth/login/finish", bogus), http.StatusBadRequest, "webauthn_failed")
	}
	wantStatus(t, owner.login(d), http.StatusOK)
	// Begins alone are limited per address too (they hold pending ceremonies).
	spammer := e.browser()
	spammer.xff = "203.0.113.9, 172.18.0.2"
	for range beginsPerKey {
		wantStatus(t, spammer.do(http.MethodPost, "/api/admin/auth/login/begin", map[string]any{}), http.StatusOK)
	}
	wantError(t, spammer.do(http.MethodPost, "/api/admin/auth/login/begin", map[string]any{}), http.StatusTooManyRequests, "rate_limited")
	wantStatus(t, owner.login(d), http.StatusOK)
	// Failures refill like everything else.
	e.clock.Add(time.Minute)
	wantError(t, attacker.do(http.MethodPost, "/api/admin/auth/login/finish", bogus), http.StatusBadRequest, "webauthn_failed")
}

// Step-up and another passkey are limited per session, never by anonymous traffic.
func TestRateLimitStepUpPerSession(t *testing.T) {
	e := newEnv(t)
	d := newDevice()
	owner := e.owner(d)
	// The anonymous internet spends every public bucket it can, from the owner's own relay address too.
	for i := range 80 {
		c := e.browser()
		c.xff = "203.0.113." + strconv.Itoa(i) + ", 172.18.0.2"
		c.do(http.MethodPost, "/api/admin/auth/token", map[string]any{"token": "x"})
		c.do(http.MethodPost, "/api/admin/auth/login/finish", map[string]any{"ceremonyId": "nope", "credential": map[string]any{}})
	}
	for range 200 {
		e.browser().do(http.MethodPost, "/api/admin/auth/login/begin", map[string]any{}) // the owner's "local" address
	}
	wantStatus(t, owner.stepUp(d), http.StatusOK)

	// A second session (a thief with a stolen cookie but no passkey, say) spends only its own step-up.
	other := e.browser()
	other.xff = "198.51.100.30, 172.18.0.2"
	wantStatus(t, other.login(d), http.StatusOK)
	bogus := map[string]any{"ceremonyId": "nope", "credential": map[string]any{}}
	for range failuresPerKey {
		wantError(t, other.do(http.MethodPost, "/api/admin/auth/stepup/finish", bogus), http.StatusBadRequest, "webauthn_failed")
	}
	wantError(t, other.do(http.MethodPost, "/api/admin/auth/stepup/finish", bogus), http.StatusTooManyRequests, "rate_limited")
	wantError(t, other.do(http.MethodPost, "/api/admin/auth/stepup/begin", map[string]any{}), http.StatusTooManyRequests, "rate_limited")
	// The other session's spent step-up leaves the passkey ceremonies (their own class) and every other session alone.
	wantStatus(t, owner.stepUp(d), http.StatusOK)
	wantStatus(t, other.do(http.MethodPost, "/api/admin/me/passkeys/begin", map[string]any{}), http.StatusOK)
}

// Pending ceremonies: a public flood replaces the oldest public ones and never crowds out a session's step-up.
func TestCeremonyPools(t *testing.T) {
	cs := newCeremonies()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	first, ok := cs.add(&ceremony{kind: kindLogin, expires: now.Add(CeremonyTTL)}, now)
	if !ok {
		t.Fatal("refused")
	}
	for i := range maxCeremonies + 10 {
		if _, ok := cs.add(&ceremony{kind: kindLogin, expires: now.Add(CeremonyTTL + time.Duration(i+1)*time.Millisecond)}, now); !ok {
			t.Fatalf("public ceremony %d refused", i)
		}
	}
	if _, ok := cs.take(first, now, isKind(kindLogin)); ok {
		t.Fatal("the oldest public ceremony survived a full pool")
	}
	if len(cs.m) != maxCeremonies {
		t.Fatalf("%d pending", len(cs.m))
	}
	for i := range maxCeremonies {
		if _, ok := cs.add(&ceremony{kind: kindStepUp, sessionID: "s", expires: now.Add(CeremonyTTL)}, now); !ok {
			t.Fatalf("step-up %d refused next to a full public pool", i)
		}
	}
	if _, ok := cs.add(&ceremony{kind: kindAddPasskey, sessionID: "s", expires: now.Add(CeremonyTTL)}, now); ok {
		t.Fatal("a full session pool accepted more")
	}
	// Expired ones make room again.
	later := now.Add(CeremonyTTL + time.Hour)
	if _, ok := cs.add(&ceremony{kind: kindStepUp, sessionID: "s", expires: later.Add(CeremonyTTL)}, later); !ok || len(cs.m) != 1 {
		t.Fatalf("expired ceremonies kept: %d pending", len(cs.m))
	}
}

func TestClientAddr(t *testing.T) {
	for _, tc := range []struct {
		remote string
		xff    []string
		want   string
	}{
		{"127.0.0.1:5000", []string{"203.0.113.7, 172.18.0.2"}, "203.0.113.7"},
		{"127.0.0.1:5000", []string{"1.1.1.1, 203.0.113.7, 172.18.0.2"}, "203.0.113.7"},
		{"127.0.0.1:5000", []string{"203.0.113.7", "172.18.0.2"}, "203.0.113.7"}, // two header lines
		{"127.0.0.1:5000", []string{"2001:db8:1:2:3:4:5:6, 172.18.0.2"}, "2001:db8:1:2::/64"},
		{"127.0.0.1:5000", []string{"::ffff:203.0.113.7, 172.18.0.2"}, "203.0.113.7"},
		{"127.0.0.1:5000", []string{"172.18.0.2"}, ""}, // cannot tell
		{"127.0.0.1:5000", nil, ""},
		{"127.0.0.1:5000", []string{"junk, 172.18.0.2"}, ""},
		{"[::1]:5000", []string{"203.0.113.7, 172.18.0.2"}, "203.0.113.7"},
		{"192.168.68.20:5000", []string{"1.2.3.4, 5.6.7.8"}, "192.168.68.20"}, // not via the relay: the peer
		{"garbage", nil, ""},
	} {
		r, _ := http.NewRequest(http.MethodGet, "http://x/", http.NoBody)
		r.RemoteAddr = tc.remote
		for _, v := range tc.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		if got := ClientAddr(r); got != tc.want {
			t.Errorf("%s %v: %q, want %q", tc.remote, tc.xff, got, tc.want)
		}
	}
}

func TestLocalEndpoints(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	phone := newDevice()
	b := e.owner(phone)
	other := e.browser()
	wantStatus(t, other.login(phone), http.StatusOK)
	sessions, _ := e.st.Sessions().ListByPerson(ctx, "roman")

	local := e.browser()
	local.origin = ""
	rec := local.do(http.MethodPost, "/local/api/admin/local/setup-link", map[string]any{})
	wantStatus(t, rec, http.StatusOK)
	link := decodeAs[map[string]any](t, rec)
	if url, _ := link["url"].(string); len(url) != len(testOrigin+"/admin/setup#t=")+43 || link["expiresAt"] != wire.Time(e.clock.Now().Add(SetupLinkTTL)) {
		t.Fatalf("link %v", link)
	}

	rec = local.do(http.MethodPost, "/local/api/admin/local/reset-access", nil)
	wantStatus(t, rec, http.StatusOK)
	res := decodeAs[map[string]any](t, rec)
	if res["revokedPasskeys"] != float64(1) || res["revokedSessions"] != float64(2) || res["url"] == link["url"] {
		t.Fatalf("reset %v", res)
	}
	if ready, _ := OwnerReady(ctx, e.st, testDomain); ready {
		t.Fatal("owner still ready after reset")
	}
	wantError(t, b.do(http.MethodGet, "/test/session", nil), http.StatusUnauthorized, "unauthenticated")
	wantError(t, e.browser().login(phone), http.StatusUnauthorized, "login_failed")
	ended := e.endedSessions()
	if len(ended) != 2 || !slices.Contains(ended, sessions[0].ID) || !slices.Contains(ended, sessions[1].ID) {
		t.Fatalf("OnSessionEnd %v, sessions %+v", ended, sessions)
	}
	// The earlier link is revoked; the new one registers a passkey for the same owner.
	_, oldToken, _ := strings.Cut(link["url"].(string), "#t=")
	wantError(t, e.browser().do(http.MethodPost, "/api/admin/auth/token", map[string]string{"token": oldToken}), http.StatusGone, "token_used")
	_, newTokenValue, _ := strings.Cut(res["url"].(string), "#t=")
	rec = e.browser().register(newDevice(), newTokenValue, nil)
	wantStatus(t, rec, http.StatusCreated)
	if m := decodeAs[me](t, rec); m.Person.ID != "roman" {
		t.Fatalf("Me %+v", m)
	}
	recs := e.audit()
	var reset store.AuditRecord
	for _, r := range recs {
		if r.Action == "access.reset" {
			reset = r
		}
	}
	if reset.Via != "cli" || string(reset.Args) != `{"credentials":1,"sessions":2}` {
		t.Fatalf("access.reset record %+v %s", reset, reset.Args)
	}

	wantError(t, local.do(http.MethodGet, "/local/api/admin/local/setup-link", nil), http.StatusMethodNotAllowed, "method_not_allowed")
	wantError(t, local.do(http.MethodPost, "/local/api/admin/local/other", nil), http.StatusNotFound, "not_found")
}

func TestOwnerReady(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	check := func(want bool) {
		t.Helper()
		if got, err := OwnerReady(ctx, e.st, testDomain); err != nil || got != want {
			t.Fatalf("OwnerReady %v %v, want %v", got, err, want)
		}
		if got, err := e.a.OwnerReady(ctx); err != nil || got != want {
			t.Fatalf("Auth.OwnerReady %v %v, want %v", got, err, want)
		}
	}
	check(false)
	e.addPerson("roman", "Roman", store.RoleOwner) // imported, no passkey yet
	check(false)
	wantStatus(t, e.browser().register(newDevice(), e.link(), nil), http.StatusCreated)
	check(true)
	// Only passkeys of the current relying party count (review L4): after a relay change the old ones open nothing.
	if got, _ := OwnerReady(ctx, e.st, "other.example.com"); got {
		t.Fatal("a passkey of another domain counts")
	}
	if got, _ := OwnerReady(ctx, e.st, ""); got {
		t.Fatal("ready without a relying party")
	}
	pks, _ := e.st.Passkeys().ListByPerson(ctx, "roman")
	if len(pks) != 1 || PasskeyRPID(pks[0]) != testDomain || !e.a.UsablePasskey(pks[0]) {
		t.Fatalf("passkeys %+v", pks)
	}
	e.mu.Lock()
	e.noRP = true
	e.mu.Unlock()
	if e.a.UsablePasskey(pks[0]) {
		t.Fatal("usable without a relying party")
	}
	if got, _ := e.a.OwnerReady(ctx); got {
		t.Fatal("Auth.OwnerReady without a relying party")
	}
	if PasskeyRPID(store.Passkey{Credential: []byte("junk")}) != "" {
		t.Fatal("an unreadable passkey has a relying party")
	}
}

func TestNewSetupLinkChecksOrigin(t *testing.T) {
	st := newEnv(t).st
	for _, origin := range []string{"", "homie.example.com", "https://homie.example.com/", "https://h/x", "ftp://h", "https://u@h"} {
		if _, err := NewSetupLink(context.Background(), st, nil, LinkRequest{Origin: origin, Via: "cli"}, time.Now()); err == nil {
			t.Errorf("origin %q accepted", origin)
		}
	}
}

// allowed returns the allowCredentials ids of request options.
func allowed(t *testing.T, raw []byte) []string {
	t.Helper()
	var v struct {
		PublicKey struct {
			AllowCredentials []struct {
				ID string `json:"id"`
			} `json:"allowCredentials"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range v.PublicKey.AllowCredentials {
		ids = append(ids, c.ID)
	}
	return ids
}

// excluded returns the excludeCredentials ids of creation options.
func excluded(t *testing.T, raw []byte) []string {
	t.Helper()
	var v struct {
		PublicKey struct {
			ExcludeCredentials []struct {
				ID string `json:"id"`
			} `json:"excludeCredentials"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range v.PublicKey.ExcludeCredentials {
		ids = append(ids, c.ID)
	}
	return ids
}

// A link from the LAN page lives 3 minutes and its audit record names the client (review M2, L3).
func TestSetupLinkTTLAndClient(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := e.clock.Now()
	l, err := NewSetupLink(ctx, e.st, nil, LinkRequest{Origin: testOrigin, Via: "admin", Client: "192.168.68.20", TTL: 3 * time.Minute}, now)
	if err != nil || !l.ExpiresAt.Equal(now.Add(3*time.Minute)) {
		t.Fatalf("link %+v %v", l, err)
	}
	_, token, _ := strings.Cut(l.URL, "#t=")
	if inv, err := e.st.Invites().GetByTokenHash(ctx, hashToken(token)); err != nil || !inv.ExpiresAt.Equal(now.Add(3*time.Minute)) {
		t.Fatalf("invite %+v %v", inv, err)
	}
	e.clock.Add(3 * time.Minute)
	wantError(t, e.browser().do(http.MethodPost, "/api/admin/auth/token", map[string]string{"token": token}), http.StatusGone, "token_expired")
	recs := e.audit()
	if last := recs[len(recs)-1]; last.Action != "setup.link" || last.Via != "admin" || string(last.Args) != `{"client":"192.168.68.20"}` {
		t.Fatalf("audit %+v %s", last, last.Args)
	}
}
