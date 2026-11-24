package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/devXpro/homie/internal/auth/wire"
	"github.com/devXpro/homie/internal/store"
)

// newSession makes a session for a fresh sign-in, which counts as step-up. The token is the cookie value; only its
// hash is stored.
func (a *Auth) newSession(personID, passkey, userAgent string, now time.Time) (token string, s store.Session) {
	token = newToken()
	return token, store.Session{
		ID: newID("s_"), TokenHash: hashToken(token), PersonID: personID, PasskeyID: passkey,
		Device: deviceLabel(userAgent), CreatedAt: now, LastSeenAt: now, StepUpAt: now,
	}
}

// setSessionCookie sends the session cookie; Max-Age is the absolute lifetime, as the session is brand new.
func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/", MaxAge: int(AbsoluteTimeout / time.Second),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

// clearSessionCookie tells the browser to drop the session cookie.
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

// deleteSession removes one session and tells OnSessionEnd.
func (a *Auth) deleteSession(ctx context.Context, id string) error {
	err := a.st.Sessions().Delete(context.WithoutCancel(ctx), id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if a.onEnd != nil {
		a.onEnd(id)
	}
	return nil
}

// Run deletes expired sessions now and then every hour, until ctx is done. Authenticate deletes the expired
// sessions it meets; this removes the abandoned ones.
func (a *Auth) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		a.sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *Auth) sweep(ctx context.Context) {
	now := a.now()
	if _, err := a.st.Sessions().DeleteExpired(ctx, now.Add(-IdleTimeout), now.Add(-AbsoluteTimeout)); err != nil && ctx.Err() == nil {
		a.log.Warn("auth: cannot delete expired sessions", "err", err)
	}
}

// The Me object of ADMIN-API §2, answered by the sign-in and registration ceremonies. internal/admin serves the same
// shape at GET /session.
type meJSON struct {
	Person   personJSON    `json:"person"`
	Session  meSessionJSON `json:"session"`
	Home     homeJSON      `json:"home"`
	Version  string        `json:"version"`
	Features Features      `json:"features"`
}

type personJSON struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Role         string   `json:"role"`
	Plugins      []string `json:"plugins"`
	Language     string   `json:"language,omitempty"`
	CreatedAt    string   `json:"createdAt"`
	Passkeys     int      `json:"passkeys"`
	LastActiveAt string   `json:"lastActiveAt,omitempty"`
}

type meSessionJSON struct {
	ID            string `json:"id"`
	CreatedAt     string `json:"createdAt"`
	ExpiresAt     string `json:"expiresAt"`
	IdleExpiresAt string `json:"idleExpiresAt"`
	StepUpUntil   string `json:"stepUpUntil,omitempty"`
}

type homeJSON struct {
	Name     string `json:"name"`
	Language string `json:"language"`
}

// me builds Me for a person and their new session.
func (a *Auth) me(ctx context.Context, person store.Person, s store.Session, hs store.HomeSettings) (meJSON, error) {
	pks, err := a.st.Passkeys().ListByPerson(ctx, person.ID)
	if err != nil {
		return meJSON{}, err
	}
	sessions, err := a.st.Sessions().ListByPerson(ctx, person.ID)
	if err != nil {
		return meJSON{}, err
	}
	var last time.Time
	for _, ss := range sessions {
		if ss.LastSeenAt.After(last) {
			last = ss.LastSeenAt
		}
	}
	plugins := person.Plugins
	if plugins == nil {
		plugins = []string{}
	}
	p := newPrincipal(person, s)
	return meJSON{
		Person: personJSON{
			ID: person.ID, Name: person.Name, Role: string(person.Role), Plugins: plugins, Language: person.Language,
			CreatedAt: wire.Time(person.CreatedAt), Passkeys: len(pks), LastActiveAt: wire.OptTime(last),
		},
		Session: meSessionJSON{
			ID: s.ID, CreatedAt: wire.Time(s.CreatedAt), ExpiresAt: wire.Time(p.ExpiresAt),
			IdleExpiresAt: wire.Time(p.IdleExpiresAt), StepUpUntil: wire.OptTime(p.StepUpUntil),
		},
		Home:     homeJSON{Name: hs.HomeName, Language: hs.Language},
		Version:  a.version,
		Features: a.features,
	}, nil
}

// signIn answers a successful sign-in or registration: Me, then the cookie. Me is built first, so a store failure
// does not leave the browser with a cookie and an error.
func (a *Auth) signIn(w http.ResponseWriter, r *http.Request, status int, token string, person store.Person,
	s store.Session, hs store.HomeSettings) error {
	body, err := a.me(r.Context(), person, s, hs)
	if err != nil {
		return err
	}
	setSessionCookie(w, token)
	wire.WriteJSON(w, status, body)
	return nil
}
