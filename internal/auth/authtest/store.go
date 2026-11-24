// Package authtest is an in-memory store.Store for the tests of internal/auth and internal/firstrun. It keeps the
// contract of internal/store/store.go (errors, fresh copies, millisecond times, InTx with rollback, nesting) for
// people, passkeys, sessions, invites, settings and audit; the person keys, connections and plugins repositories
// answer ErrNotFound or nothing.
//
// Every call is serialized with the transactions, so a rollback can never lose a concurrent write.
package authtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/devXpro/homie/internal/store"
)

// Store is the in-memory store. The zero value is not usable; call New.
type Store struct {
	mu     *sync.Mutex
	d      *data
	nested bool // the Store handed to an InTx function: the lock is already held
}

type data struct {
	seq      int64 // insertion order, the stand-in for SQLite's rowid
	people   map[string]row[store.Person]
	passkeys map[string]row[store.Passkey]
	sessions map[string]row[store.Session]
	invites  map[string]row[store.Invite]
	audit    []store.AuditRecord
	auditSeq int64
	settings *store.HomeSettings
}

type row[T any] struct {
	v   T
	seq int64
}

// New returns an empty store.
func New() *Store {
	return &Store{mu: &sync.Mutex{}, d: &data{
		people: map[string]row[store.Person]{}, passkeys: map[string]row[store.Passkey]{},
		sessions: map[string]row[store.Session]{}, invites: map[string]row[store.Invite]{},
	}}
}

var _ store.Store = (*Store)(nil)

func (s *Store) lock() func() {
	if s.nested {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func (d *data) next() int64 {
	d.seq++
	return d.seq
}

// clone deep-copies the data for a rollback.
func (d *data) clone() *data {
	c := &data{
		seq: d.seq, people: maps.Clone(d.people), passkeys: maps.Clone(d.passkeys), sessions: maps.Clone(d.sessions),
		invites: maps.Clone(d.invites), audit: slices.Clone(d.audit), auditSeq: d.auditSeq,
	}
	if d.settings != nil {
		hs := *d.settings
		c.settings = &hs
	}
	return c
}

// stamp mimics the SQLite store: UTC, millisecond precision.
func stamp(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return time.UnixMilli(t.UnixMilli()).UTC()
}

// InTx runs fn with all calls under one lock and restores the data when fn fails.
func (s *Store) InTx(_ context.Context, fn func(tx store.Store) error) error {
	if s.nested {
		return fn(s)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := s.d.clone()
	if err := fn(&Store{mu: s.mu, d: s.d, nested: true}); err != nil {
		*s.d = *snapshot
		return err
	}
	return nil
}

// Close does nothing.
func (s *Store) Close() error { return nil }

// People returns the people repository.
func (s *Store) People() store.People { return people{s} }

// Passkeys returns the passkeys repository.
func (s *Store) Passkeys() store.Passkeys { return passkeys{s} }

// Sessions returns the sessions repository.
func (s *Store) Sessions() store.Sessions { return sessions{s} }

// Invites returns the invites repository.
func (s *Store) Invites() store.Invites { return invites{s} }

// Settings returns the settings repository.
func (s *Store) Settings() store.Settings { return settings{s} }

// Audit returns the audit repository.
func (s *Store) Audit() store.Audit { return audit{s} }

// Keys answers ErrNotFound or nothing.
func (s *Store) Keys() store.Keys { return keys{} }

// Connections answers ErrNotFound or nothing.
func (s *Store) Connections() store.Connections { return connections{} }

// Plugins answers ErrNotFound or nothing.
func (s *Store) Plugins() store.Plugins { return plugins{} }

// --- people ---

type people struct{ s *Store }

func copyPerson(p store.Person) store.Person {
	p.Plugins = slices.Clone(p.Plugins)
	p.UserHandle = bytes.Clone(p.UserHandle)
	return p
}

func (r people) Get(_ context.Context, id string) (store.Person, error) {
	defer r.s.lock()()
	p, ok := r.s.d.people[id]
	if !ok {
		return store.Person{}, store.ErrNotFound
	}
	return copyPerson(p.v), nil
}

func (r people) GetByUserHandle(_ context.Context, handle []byte) (store.Person, error) {
	defer r.s.lock()()
	for _, p := range r.s.d.people {
		if len(handle) > 0 && bytes.Equal(p.v.UserHandle, handle) {
			return copyPerson(p.v), nil
		}
	}
	return store.Person{}, store.ErrNotFound
}

func (r people) List(_ context.Context) ([]store.Person, error) {
	defer r.s.lock()()
	out := make([]store.Person, 0, len(r.s.d.people))
	for _, p := range r.s.d.people {
		out = append(out, copyPerson(p.v))
	}
	slices.SortFunc(out, func(a, b store.Person) int {
		if (a.Role == store.RoleOwner) != (b.Role == store.RoleOwner) {
			if a.Role == store.RoleOwner {
				return -1
			}
			return 1
		}
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

func (r people) HasOwner(_ context.Context) (bool, error) {
	defer r.s.lock()()
	for _, p := range r.s.d.people {
		if p.v.Role == store.RoleOwner {
			return true, nil
		}
	}
	return false, nil
}

func (r people) Create(_ context.Context, p store.Person) error {
	defer r.s.lock()()
	if p.ID == "" || (p.Role != store.RoleOwner && p.Role != store.RoleMember) || len(p.UserHandle) == 0 {
		return fmt.Errorf("authtest: person %q is incomplete", p.ID)
	}
	if _, ok := r.s.d.people[p.ID]; ok {
		return store.ErrConflict
	}
	for _, other := range r.s.d.people {
		if bytes.Equal(other.v.UserHandle, p.UserHandle) {
			return store.ErrConflict
		}
	}
	p = copyPerson(p)
	p.CreatedAt, p.UpdatedAt = stamp(p.CreatedAt), stamp(p.UpdatedAt)
	r.s.d.people[p.ID] = row[store.Person]{p, r.s.d.next()}
	return nil
}

func (r people) Update(_ context.Context, p store.Person) error {
	defer r.s.lock()()
	cur, ok := r.s.d.people[p.ID]
	if !ok {
		return store.ErrNotFound
	}
	cur.v.Name, cur.v.Role, cur.v.Plugins, cur.v.Language = p.Name, p.Role, slices.Clone(p.Plugins), p.Language
	cur.v.UpdatedAt = stamp(time.Now())
	r.s.d.people[p.ID] = cur
	return nil
}

func (r people) Delete(_ context.Context, id string) error {
	defer r.s.lock()()
	if _, ok := r.s.d.people[id]; !ok {
		return store.ErrNotFound
	}
	delete(r.s.d.people, id)
	maps.DeleteFunc(r.s.d.passkeys, func(_ string, pk row[store.Passkey]) bool { return pk.v.PersonID == id })
	maps.DeleteFunc(r.s.d.sessions, func(_ string, s row[store.Session]) bool { return s.v.PersonID == id })
	maps.DeleteFunc(r.s.d.invites, func(_ string, i row[store.Invite]) bool { return i.v.PersonID == id })
	return nil
}

// --- passkeys ---

type passkeys struct{ s *Store }

func copyPasskey(pk store.Passkey) store.Passkey {
	pk.Credential = bytes.Clone(pk.Credential)
	return pk
}

func (r passkeys) Get(_ context.Context, id string) (store.Passkey, error) {
	defer r.s.lock()()
	pk, ok := r.s.d.passkeys[id]
	if !ok {
		return store.Passkey{}, store.ErrNotFound
	}
	return copyPasskey(pk.v), nil
}

func (r passkeys) ListByPerson(_ context.Context, personID string) ([]store.Passkey, error) {
	defer r.s.lock()()
	var rows []row[store.Passkey]
	for _, pk := range r.s.d.passkeys {
		if pk.v.PersonID == personID {
			rows = append(rows, pk)
		}
	}
	slices.SortFunc(rows, func(a, b row[store.Passkey]) int {
		if c := a.v.CreatedAt.Compare(b.v.CreatedAt); c != 0 {
			return c
		}
		return int(a.seq - b.seq)
	})
	out := make([]store.Passkey, 0, len(rows))
	for _, pk := range rows {
		out = append(out, copyPasskey(pk.v))
	}
	return out, nil
}

func (r passkeys) Count(_ context.Context) (int, error) {
	defer r.s.lock()()
	return len(r.s.d.passkeys), nil
}

func (r passkeys) Create(_ context.Context, pk store.Passkey) error {
	defer r.s.lock()()
	if pk.ID == "" {
		return errors.New("authtest: passkey id is empty")
	}
	if _, ok := r.s.d.people[pk.PersonID]; !ok {
		return fmt.Errorf("authtest: passkey of unknown person %q", pk.PersonID)
	}
	if _, ok := r.s.d.passkeys[pk.ID]; ok {
		return store.ErrConflict
	}
	pk = copyPasskey(pk)
	pk.CreatedAt, pk.LastUsedAt = stamp(pk.CreatedAt), stamp(pk.LastUsedAt)
	r.s.d.passkeys[pk.ID] = row[store.Passkey]{pk, r.s.d.next()}
	return nil
}

func (r passkeys) Rename(_ context.Context, id, name string) error {
	defer r.s.lock()()
	pk, ok := r.s.d.passkeys[id]
	if !ok {
		return store.ErrNotFound
	}
	pk.v.Name = name
	r.s.d.passkeys[id] = pk
	return nil
}

func (r passkeys) RecordUse(_ context.Context, id string, credential []byte, at time.Time) error {
	defer r.s.lock()()
	pk, ok := r.s.d.passkeys[id]
	if !ok {
		return store.ErrNotFound
	}
	pk.v.Credential, pk.v.LastUsedAt = bytes.Clone(credential), stamp(at)
	r.s.d.passkeys[id] = pk
	return nil
}

func (r passkeys) Delete(_ context.Context, id string) error {
	defer r.s.lock()()
	pk, ok := r.s.d.passkeys[id]
	if !ok {
		return store.ErrNotFound
	}
	n := 0
	for _, other := range r.s.d.passkeys {
		if other.v.PersonID == pk.v.PersonID {
			n++
		}
	}
	if n < 2 {
		return store.ErrLastPasskey
	}
	delete(r.s.d.passkeys, id)
	return nil
}

func (r passkeys) DeleteAll(_ context.Context) (int, error) {
	defer r.s.lock()()
	n := len(r.s.d.passkeys)
	clear(r.s.d.passkeys)
	return n, nil
}

// --- sessions ---

type sessions struct{ s *Store }

func (r sessions) GetByTokenHash(_ context.Context, tokenHash string) (store.Session, error) {
	defer r.s.lock()()
	for _, s := range r.s.d.sessions {
		if s.v.TokenHash == tokenHash {
			return s.v, nil
		}
	}
	return store.Session{}, store.ErrNotFound
}

func (r sessions) ListByPerson(_ context.Context, personID string) ([]store.Session, error) {
	defer r.s.lock()()
	var rows []row[store.Session]
	for _, s := range r.s.d.sessions {
		if s.v.PersonID == personID {
			rows = append(rows, s)
		}
	}
	slices.SortFunc(rows, func(a, b row[store.Session]) int {
		if c := b.v.LastSeenAt.Compare(a.v.LastSeenAt); c != 0 {
			return c
		}
		return int(b.seq - a.seq)
	})
	out := make([]store.Session, 0, len(rows))
	for _, s := range rows {
		out = append(out, s.v)
	}
	return out, nil
}

func (r sessions) Create(_ context.Context, s store.Session) error {
	defer r.s.lock()()
	if s.ID == "" || s.TokenHash == "" {
		return errors.New("authtest: session id and token hash are required")
	}
	if _, ok := r.s.d.people[s.PersonID]; !ok {
		return fmt.Errorf("authtest: session of unknown person %q", s.PersonID)
	}
	for _, other := range r.s.d.sessions {
		if other.v.ID == s.ID || other.v.TokenHash == s.TokenHash {
			return store.ErrConflict
		}
	}
	s.CreatedAt, s.LastSeenAt, s.StepUpAt = stamp(s.CreatedAt), stamp(s.LastSeenAt), stamp(s.StepUpAt)
	if s.LastSeenAt.IsZero() {
		s.LastSeenAt = s.CreatedAt
	}
	r.s.d.sessions[s.ID] = row[store.Session]{s, r.s.d.next()}
	return nil
}

func (r sessions) update(id string, fn func(*store.Session)) error {
	defer r.s.lock()()
	s, ok := r.s.d.sessions[id]
	if !ok {
		return store.ErrNotFound
	}
	fn(&s.v)
	r.s.d.sessions[id] = s
	return nil
}

func (r sessions) Touch(_ context.Context, id string, at time.Time) error {
	return r.update(id, func(s *store.Session) { s.LastSeenAt = stamp(at) })
}

func (r sessions) SetStepUp(_ context.Context, id string, at time.Time) error {
	return r.update(id, func(s *store.Session) { s.StepUpAt = stamp(at) })
}

func (r sessions) Delete(_ context.Context, id string) error {
	defer r.s.lock()()
	if _, ok := r.s.d.sessions[id]; !ok {
		return store.ErrNotFound
	}
	delete(r.s.d.sessions, id)
	return nil
}

func (r sessions) deleteWhere(keep func(store.Session) bool) int {
	n := len(r.s.d.sessions)
	maps.DeleteFunc(r.s.d.sessions, func(_ string, s row[store.Session]) bool { return !keep(s.v) })
	return n - len(r.s.d.sessions)
}

func (r sessions) DeleteByPerson(_ context.Context, personID, keep string) (int, error) {
	defer r.s.lock()()
	return r.deleteWhere(func(s store.Session) bool { return s.PersonID != personID || s.ID == keep }), nil
}

func (r sessions) DeleteAll(_ context.Context) (int, error) {
	defer r.s.lock()()
	return r.deleteWhere(func(store.Session) bool { return false }), nil
}

func (r sessions) DeleteExpired(_ context.Context, idleBefore, createdBefore time.Time) (int, error) {
	defer r.s.lock()()
	return r.deleteWhere(func(s store.Session) bool {
		return (idleBefore.IsZero() || !s.LastSeenAt.Before(idleBefore)) &&
			(createdBefore.IsZero() || !s.CreatedAt.Before(createdBefore))
	}), nil
}

// --- invites ---

type invites struct{ s *Store }

func (r invites) GetByTokenHash(_ context.Context, tokenHash string) (store.Invite, error) {
	defer r.s.lock()()
	return r.byHash(tokenHash)
}

func (r invites) byHash(tokenHash string) (store.Invite, error) {
	for _, inv := range r.s.d.invites {
		if inv.v.TokenHash == tokenHash {
			return inv.v, nil
		}
	}
	return store.Invite{}, store.ErrNotFound
}

func (r invites) ListActive(_ context.Context, kind store.InviteKind, now time.Time) ([]store.Invite, error) {
	defer r.s.lock()()
	var rows []row[store.Invite]
	for _, inv := range r.s.d.invites {
		if inv.v.Kind == kind && inv.v.UsedAt.IsZero() && inv.v.ExpiresAt.After(now) {
			rows = append(rows, inv)
		}
	}
	slices.SortFunc(rows, func(a, b row[store.Invite]) int { return int(b.seq - a.seq) })
	out := make([]store.Invite, 0, len(rows))
	for _, inv := range rows {
		out = append(out, inv.v)
	}
	return out, nil
}

func (r invites) Create(_ context.Context, inv store.Invite) error {
	defer r.s.lock()()
	if inv.ID == "" || inv.TokenHash == "" || inv.ExpiresAt.IsZero() {
		return errors.New("authtest: invite id, token hash and expiry are required")
	}
	if inv.Kind != store.InviteSetup && inv.Kind != store.InviteMember {
		return fmt.Errorf("authtest: bad invite kind %q", inv.Kind)
	}
	if inv.PersonID != "" {
		if _, ok := r.s.d.people[inv.PersonID]; !ok {
			return fmt.Errorf("authtest: invite for unknown person %q", inv.PersonID)
		}
	}
	if _, err := r.byHash(inv.TokenHash); err == nil {
		return store.ErrConflict
	}
	if _, ok := r.s.d.invites[inv.ID]; ok {
		return store.ErrConflict
	}
	inv.CreatedAt, inv.ExpiresAt, inv.UsedAt = stamp(inv.CreatedAt), stamp(inv.ExpiresAt), stamp(inv.UsedAt)
	r.s.d.invites[inv.ID] = row[store.Invite]{inv, r.s.d.next()}
	return nil
}

func (r invites) Consume(_ context.Context, tokenHash string, now time.Time) (store.Invite, error) {
	defer r.s.lock()()
	inv, err := r.byHash(tokenHash)
	switch {
	case err != nil:
		return store.Invite{}, err
	case !inv.UsedAt.IsZero():
		return store.Invite{}, store.ErrTokenUsed
	case !inv.ExpiresAt.After(stamp(now)):
		return store.Invite{}, store.ErrTokenExpired
	}
	inv.UsedAt = stamp(now)
	cur := r.s.d.invites[inv.ID]
	cur.v = inv
	r.s.d.invites[inv.ID] = cur
	return inv, nil
}

func (r invites) Revoke(_ context.Context, id string, at time.Time) error {
	defer r.s.lock()()
	inv, ok := r.s.d.invites[id]
	if !ok {
		return store.ErrNotFound
	}
	if inv.v.UsedAt.IsZero() {
		inv.v.UsedAt = stamp(at)
		r.s.d.invites[id] = inv
	}
	return nil
}

func (r invites) RevokeAll(_ context.Context, kind store.InviteKind, personID string, at time.Time) (int, error) {
	defer r.s.lock()()
	n := 0
	for id, inv := range r.s.d.invites {
		if inv.v.Kind == kind && inv.v.UsedAt.IsZero() && (personID == "" || inv.v.PersonID == personID) {
			inv.v.UsedAt = stamp(at)
			r.s.d.invites[id] = inv
			n++
		}
	}
	return n, nil
}

// --- settings ---

type settings struct{ s *Store }

func (r settings) Get(_ context.Context) (store.HomeSettings, error) {
	defer r.s.lock()()
	if r.s.d.settings == nil {
		return store.HomeSettings{Language: "ru"}, nil
	}
	return *r.s.d.settings, nil
}

func (r settings) Put(_ context.Context, hs store.HomeSettings) error {
	defer r.s.lock()()
	hs.UpdatedAt = stamp(time.Now())
	r.s.d.settings = &hs
	return nil
}

// --- audit ---

type audit struct{ s *Store }

func copyRecord(rec store.AuditRecord) store.AuditRecord {
	rec.Args = bytes.Clone(rec.Args)
	return rec
}

func (r audit) Append(_ context.Context, recs []store.AuditRecord) ([]store.AuditRecord, error) {
	defer r.s.lock()()
	out := make([]store.AuditRecord, 0, len(recs))
	for _, rec := range recs {
		if len(rec.Args) > 0 && !json.Valid(rec.Args) {
			return nil, errors.New("authtest: audit args are not JSON")
		}
		rec = copyRecord(rec)
		r.s.d.auditSeq++
		rec.ID = r.s.d.auditSeq
		rec.At = stamp(rec.At)
		r.s.d.audit = append(r.s.d.audit, rec)
		out = append(out, copyRecord(rec))
	}
	return out, nil
}

func (r audit) Query(_ context.Context, f store.AuditFilter) ([]store.AuditRecord, error) {
	defer r.s.lock()()
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	var out []store.AuditRecord
	for i := len(r.s.d.audit) - 1; i >= 0 && len(out) < limit; i-- {
		rec := r.s.d.audit[i]
		if f.BeforeID != 0 && rec.ID >= f.BeforeID || f.Kind != "" && rec.Kind != f.Kind ||
			f.PersonID != "" && rec.PersonID != f.PersonID || f.PluginID != "" && rec.PluginID != f.PluginID ||
			f.Tool != "" && rec.Tool != f.Tool || len(f.Via) > 0 && !slices.Contains(f.Via, rec.Via) ||
			f.Status == store.AuditOK && !rec.OK || f.Status == store.AuditFailed && rec.OK ||
			!f.Since.IsZero() && rec.At.Before(f.Since) || !f.Until.IsZero() && !rec.At.Before(f.Until) {
			continue
		}
		out = append(out, copyRecord(rec))
	}
	return out, nil
}

func (r audit) Get(_ context.Context, id int64) (store.AuditRecord, error) {
	defer r.s.lock()()
	for _, rec := range r.s.d.audit {
		if rec.ID == id {
			return copyRecord(rec), nil
		}
	}
	return store.AuditRecord{}, store.ErrNotFound
}

func (r audit) Prune(_ context.Context, before time.Time) (int64, error) {
	defer r.s.lock()()
	n := len(r.s.d.audit)
	r.s.d.audit = slices.DeleteFunc(r.s.d.audit, func(rec store.AuditRecord) bool { return rec.At.Before(before) })
	return int64(n - len(r.s.d.audit)), nil
}

// --- repositories auth does not use ---

var errNotKept = errors.New("authtest: not kept by this fake")

type keys struct{}

func (keys) Get(context.Context, string) (store.Key, error)            { return store.Key{}, store.ErrNotFound }
func (keys) List(context.Context) ([]store.Key, error)                 { return nil, nil }
func (keys) ListByPerson(context.Context, string) ([]store.Key, error) { return nil, nil }
func (keys) Create(context.Context, store.Key) error                   { return errNotKept }
func (keys) Touch(context.Context, string, time.Time) error            { return store.ErrNotFound }
func (keys) Delete(context.Context, string) error                      { return store.ErrNotFound }

type connections struct{}

func (connections) Get(context.Context, string) (store.Connection, error) {
	return store.Connection{}, store.ErrNotFound
}
func (connections) List(context.Context) ([]store.Connection, error) { return nil, nil }
func (connections) ListByPerson(context.Context, string) ([]store.Connection, error) {
	return nil, nil
}
func (connections) Create(context.Context, store.Connection) error { return errNotKept }
func (connections) Update(context.Context, store.Connection) error { return store.ErrNotFound }
func (connections) Delete(context.Context, string) error           { return store.ErrNotFound }

type plugins struct{}

func (plugins) Get(context.Context, string) (store.Plugin, error) {
	return store.Plugin{}, store.ErrNotFound
}
func (plugins) List(context.Context) ([]store.Plugin, error) { return nil, nil }
func (plugins) Create(context.Context, store.Plugin) error   { return errNotKept }
func (plugins) Update(context.Context, store.Plugin) error   { return store.ErrNotFound }
func (plugins) Delete(context.Context, string) error         { return store.ErrNotFound }
