package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/devXpro/homie/internal/keys"
	"github.com/devXpro/homie/internal/store"
)

// People and keys (ADMIN-API §3.7, phase 2 part).

func (s *Server) listPeople(q *request) error {
	ctx := q.r.Context()
	people, err := s.st.People().List(ctx)
	if err != nil {
		return err
	}
	items := make([]personJSON, 0, len(people))
	for _, p := range people {
		pj, err := s.personJSON(ctx, p)
		if err != nil {
			return err
		}
		items = append(items, pj)
	}
	return s.writeJSON(q, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getPerson(q *request) error {
	ctx := q.r.Context()
	p, err := s.st.People().Get(ctx, q.r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		return notFound("person")
	}
	if err != nil {
		return err
	}
	pj, err := s.personJSON(ctx, p)
	if err != nil {
		return err
	}
	return s.writeJSON(q, http.StatusOK, pj)
}

func (s *Server) listKeys(q *request) error {
	ctx := q.r.Context()
	var list []store.Key
	var err error
	if person := q.r.URL.Query().Get("person"); person != "" {
		list, err = s.st.Keys().ListByPerson(ctx, person)
	} else {
		list, err = s.st.Keys().List(ctx)
	}
	if err != nil {
		return err
	}
	items := make([]keyJSON, 0, len(list))
	for _, k := range list {
		items = append(items, toKey(k))
	}
	return s.writeJSON(q, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createKey(q *request) error {
	var body struct {
		Label string `json:"label"`
	}
	if err := decode(q, &body); err != nil {
		return err
	}
	label := strings.TrimSpace(body.Label)
	if n := utf8.RuneCountInString(label); n < 1 || n > 60 {
		return invalidField("/label", "length", "must be 1 to 60 characters")
	}
	ctx := q.r.Context()
	personID := q.r.PathValue("id")
	if _, err := s.st.People().Get(ctx, personID); errors.Is(err, store.ErrNotFound) {
		return notFound("person")
	} else if err != nil {
		return err
	}
	secret, hash := keys.Generate()
	k := store.Key{
		ID: "k_" + randomID(9), PersonID: personID, SHA256: hash, Label: label, Hint: secret[:8], CreatedAt: s.now(),
	}
	if err := s.st.Keys().Create(ctx, k); err != nil {
		return err
	}
	if err := s.rebuildKeys(ctx); err != nil {
		return err
	}
	s.recordAdmin(q, "key.create", k.ID, "", map[string]any{"person": personID, "label": label}, nil)
	return s.writeJSON(q, http.StatusCreated, map[string]any{"key": toKey(k), "secret": secret})
}

func (s *Server) deleteKey(q *request) error {
	ctx := q.r.Context()
	id := q.r.PathValue("id")
	k, err := s.st.Keys().Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return notFound("key")
	}
	if err != nil {
		return err
	}
	if err := s.st.Keys().Delete(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err := s.rebuildKeys(ctx); err != nil {
		return err
	}
	s.recordAdmin(q, "key.revoke", id, "", map[string]any{"person": k.PersonID, "label": k.Label}, nil)
	return noContent(q)
}

// rebuildKeys has the resolver re-read the full key set, with every person's current name and scope, before the
// answer goes out.
func (s *Server) rebuildKeys(ctx context.Context) error {
	if s.opts.Keys == nil {
		return nil
	}
	return s.opts.Keys.Rebuild(ctx)
}
