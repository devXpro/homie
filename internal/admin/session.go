package admin

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/devXpro/homie/internal/auth/wire"
	"github.com/devXpro/homie/internal/store"
)

// The bootstrap, session and "me" endpoints (ADMIN-API §3.1, §3.3). The passkey ceremonies are T2.2's (AuthRoutes).

var features = map[string]bool{"people": false, "connections": false}

func (s *Server) getBootstrap(q *request) error {
	ctx := q.r.Context()
	hs, err := s.st.Settings().Get(ctx)
	if err != nil {
		return err
	}
	ready, err := s.ownerReady(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{"homeName": hs.HomeName, "language": hs.Language, "ownerReady": ready, "features": features}
	if hs.ConnectionKind != "" {
		body["connectionKind"] = string(hs.ConnectionKind)
	}
	return s.writeJSON(q, http.StatusOK, body)
}

// ownerReady reports whether an owner with at least one usable passkey exists.
func (s *Server) ownerReady(ctx context.Context) (bool, error) {
	people, err := s.st.People().List(ctx)
	if err != nil {
		return false, err
	}
	for _, p := range people {
		if p.Role != roleOwner {
			continue
		}
		pks, err := s.st.Passkeys().ListByPerson(ctx, p.ID)
		if err != nil {
			return false, err
		}
		if slices.ContainsFunc(pks, s.usable) {
			return true, nil
		}
	}
	return false, nil
}

// usable reports whether a passkey can sign in now (Options.UsablePasskey: it belongs to the current relying party).
func (s *Server) usable(pk store.Passkey) bool {
	return s.opts.UsablePasskey == nil || s.opts.UsablePasskey(pk)
}

func (s *Server) getSession(q *request) error {
	ctx := q.r.Context()
	person, err := s.personJSON(ctx, q.p.Person)
	if err != nil {
		return err
	}
	hs, err := s.st.Settings().Get(ctx)
	if err != nil {
		return err
	}
	sess := map[string]any{
		"id": q.p.Session.ID, "createdAt": ts(q.p.Session.CreatedAt),
		"expiresAt": ts(q.p.ExpiresAt), "idleExpiresAt": ts(q.p.IdleExpiresAt),
	}
	if s.now().Before(q.p.StepUpUntil) {
		sess["stepUpUntil"] = ts(q.p.StepUpUntil)
	}
	return s.writeJSON(q, http.StatusOK, map[string]any{
		"person":   person,
		"session":  sess,
		"home":     map[string]string{"name": hs.HomeName, "language": hs.Language},
		"version":  s.opts.Version,
		"features": features,
	})
}

// personJSON fills the computed keys of a person: the passkey count and the last activity.
func (s *Server) personJSON(ctx context.Context, p store.Person) (personJSON, error) {
	pks, err := s.st.Passkeys().ListByPerson(ctx, p.ID)
	if err != nil {
		return personJSON{}, err
	}
	sessions, err := s.st.Sessions().ListByPerson(ctx, p.ID)
	if err != nil {
		return personJSON{}, err
	}
	var last time.Time
	for _, ss := range sessions {
		if ss.LastSeenAt.After(last) {
			last = ss.LastSeenAt
		}
	}
	return toPerson(p, len(pks), last), nil
}

// localPrincipal is the socket's principal: the owner, or before one exists a local owner named after the OS user.
func (s *Server) localPrincipal(ctx context.Context) (Principal, error) {
	people, err := s.st.People().List(ctx)
	if err != nil {
		return Principal{}, err
	}
	for _, p := range people {
		if p.Role == roleOwner {
			return Principal{Person: p}, nil
		}
	}
	return Principal{Person: store.Person{ID: "local", Name: s.opts.LocalName, Role: roleOwner, Plugins: []string{"*"}}}, nil
}

// checkName trims a display name and checks it with the rule internal/auth applies at registration (wire.CheckName:
// 1–40 characters, no control characters, line breaks, quotes, backslashes or backticks), answering in the admin's
// error format.
func checkName(path, v string) (string, error) {
	name, err := wire.CheckName(path, v)
	if we, ok := errors.AsType[*wire.Error](err); ok && len(we.Fields) > 0 {
		return "", invalidField(we.Fields[0].Path, we.Fields[0].Code, we.Fields[0].Message)
	}
	return name, err
}

func checkLanguage(path, v string) error {
	if v != "ru" && v != "en" {
		return invalidField(path, "enum", `must be "ru" or "en"`)
	}
	return nil
}

func (s *Server) patchMe(q *request) error {
	var body struct {
		Name     *string `json:"name"`
		Language *string `json:"language"`
	}
	if err := decode(q, &body); err != nil {
		return err
	}
	ctx := q.r.Context()
	p, err := s.st.People().Get(ctx, q.p.Person.ID)
	if err != nil {
		return err
	}
	var changed []string
	if body.Name != nil {
		name, err := checkName("/name", *body.Name)
		if err != nil {
			return err
		}
		if name != p.Name {
			p.Name = name
			changed = append(changed, "name")
		}
	}
	if body.Language != nil {
		if err := checkLanguage("/language", *body.Language); err != nil {
			return err
		}
		if *body.Language != p.Language {
			p.Language = *body.Language
			changed = append(changed, "language")
		}
	}
	if len(changed) > 0 {
		if err := s.st.People().Update(ctx, p); err != nil {
			return err
		}
		if err := s.rebuildKeys(ctx); err != nil { // a new name reaches the audit names of key callers
			return err
		}
		s.recordAdmin(q, "person.update", p.ID, "", map[string]any{"changed": changed}, nil)
	}
	out, err := s.personJSON(ctx, p)
	if err != nil {
		return err
	}
	return s.writeJSON(q, http.StatusOK, out)
}

func (s *Server) listMyPasskeys(q *request) error {
	pks, err := s.st.Passkeys().ListByPerson(q.r.Context(), q.p.Person.ID)
	if err != nil {
		return err
	}
	items := make([]passkeyJSON, 0, len(pks))
	for _, pk := range pks {
		items = append(items, toPasskey(pk, q.p.Session.PasskeyID))
	}
	return s.writeJSON(q, http.StatusOK, map[string]any{"items": items})
}

// ownPasskey returns one of the principal's passkeys; someone else's is not_found.
func (s *Server) ownPasskey(q *request) (store.Passkey, error) {
	pk, err := s.st.Passkeys().Get(q.r.Context(), q.r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) || err == nil && pk.PersonID != q.p.Person.ID {
		return store.Passkey{}, notFound("passkey")
	}
	return pk, err
}

func (s *Server) renameMyPasskey(q *request) error {
	var body struct {
		Name string `json:"name"`
	}
	if err := decode(q, &body); err != nil {
		return err
	}
	name, err := checkName("/name", body.Name)
	if err != nil {
		return err
	}
	pk, err := s.ownPasskey(q)
	if err != nil {
		return err
	}
	if err := s.st.Passkeys().Rename(q.r.Context(), pk.ID, name); err != nil {
		return err
	}
	pk.Name = name
	s.recordAdmin(q, "passkey.rename", name, "", nil, nil)
	return s.writeJSON(q, http.StatusOK, toPasskey(pk, q.p.Session.PasskeyID))
}

func (s *Server) deleteMyPasskey(q *request) error {
	pk, err := s.ownPasskey(q)
	if err != nil {
		return err
	}
	ctx := q.r.Context()
	// The last passkey that can still sign in stays, even next to passkeys of an old relay domain (which the store's
	// own count would accept); the check and the delete share one transaction.
	err = s.st.InTx(ctx, func(tx store.Store) error {
		if s.usable(pk) {
			pks, err := tx.Passkeys().ListByPerson(ctx, pk.PersonID)
			if err != nil {
				return err
			}
			if !slices.ContainsFunc(pks, func(o store.Passkey) bool { return o.ID != pk.ID && s.usable(o) }) {
				return store.ErrLastPasskey
			}
		}
		return tx.Passkeys().Delete(ctx, pk.ID)
	})
	if errors.Is(err, store.ErrLastPasskey) {
		return errorf(http.StatusConflict, "last_passkey", "the last passkey cannot be removed")
	}
	if err != nil {
		return err
	}
	// Sessions signed in with it end too.
	sessions, err := s.st.Sessions().ListByPerson(ctx, pk.PersonID)
	if err != nil {
		return err
	}
	for _, ss := range sessions {
		if ss.PasskeyID != pk.ID {
			continue
		}
		if err := s.st.Sessions().Delete(ctx, ss.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		s.events.revokeSession(ss.ID)
		if ss.ID == q.p.Session.ID {
			clearCookie(q.w)
		}
	}
	s.recordAdmin(q, "passkey.remove", pk.Name, "", nil, nil)
	return noContent(q)
}

func (s *Server) listMySessions(q *request) error {
	sessions, err := s.st.Sessions().ListByPerson(q.r.Context(), q.p.Person.ID)
	if err != nil {
		return err
	}
	items := make([]sessionJSON, 0, len(sessions))
	for _, ss := range sessions {
		items = append(items, toSession(ss, q.p.Session.ID))
	}
	return s.writeJSON(q, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) deleteMySession(q *request) error {
	ctx := q.r.Context()
	id := q.r.PathValue("id")
	sessions, err := s.st.Sessions().ListByPerson(ctx, q.p.Person.ID)
	if err != nil {
		return err
	}
	for _, ss := range sessions {
		if ss.ID != id {
			continue
		}
		if err := s.st.Sessions().Delete(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		s.events.revokeSession(id)
		if id == q.p.Session.ID {
			clearCookie(q.w)
		}
		s.recordAdmin(q, "session.revoke", ss.Device, "", nil, nil)
		return noContent(q)
	}
	return notFound("session")
}

func (s *Server) revokeOtherSessions(q *request) error {
	if err := decode(q, &struct{}{}); err != nil {
		return err
	}
	ctx := q.r.Context()
	sessions, err := s.st.Sessions().ListByPerson(ctx, q.p.Person.ID)
	if err != nil {
		return err
	}
	n, err := s.st.Sessions().DeleteByPerson(ctx, q.p.Person.ID, q.p.Session.ID)
	if err != nil {
		return err
	}
	for _, ss := range sessions {
		if ss.ID != q.p.Session.ID {
			s.events.revokeSession(ss.ID)
		}
	}
	if n > 0 {
		s.recordAdmin(q, "session.revoke", "others", "", map[string]any{"count": n}, nil)
	}
	return s.writeJSON(q, http.StatusOK, map[string]int{"revoked": n})
}

// clearCookie ends the browser's session cookie.
func clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}
