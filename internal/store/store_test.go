package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// clock is a settable time source for the store under test.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var epoch = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func newTestDB(t *testing.T) (*DB, *clock) {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "homed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c := &clock{t: epoch}
	db.now = c.now
	return db, c
}

func mustPerson(t *testing.T, db Store, id string, role Role) Person {
	t.Helper()
	p := Person{ID: id, Name: id, Role: role, Plugins: []string{"*"}, UserHandle: NewUserHandle()}
	if err := db.People().Create(context.Background(), p); err != nil {
		t.Fatalf("create person %s: %v", id, err)
	}
	return p
}

func mustPasskey(t *testing.T, db Store, id, person string) {
	t.Helper()
	pk := Passkey{ID: id, PersonID: person, Name: id, Credential: []byte("cred-" + id)}
	if err := db.Passkeys().Create(context.Background(), pk); err != nil {
		t.Fatalf("create passkey %s: %v", id, err)
	}
}

func TestOpenFilesAndPragmas(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sub", "homed.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// A write creates the -wal and -shm files.
	if err := db.Settings().Put(ctx, HomeSettings{HomeName: "x", Language: "en"}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		fi, err := os.Stat(path + suffix)
		if err != nil {
			t.Fatalf("stat %q: %v", suffix, err)
		}
		if mode := fi.Mode().Perm(); mode != 0o600 {
			t.Errorf("mode of %s%s = %o, want 600", filepath.Base(path), suffix, mode)
		}
	}
	var mode string
	if err := db.conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v; want wal", mode, err)
	}
	var fk, busy int
	// Pragmas are per connection; hold one connection so the same one answers both.
	c, err := db.conn.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := c.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys = %d, %v; want 1", fk, err)
	}
	if err := c.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busy); err != nil || busy < 1000 {
		t.Errorf("busy_timeout = %d, %v; want >= 1000", busy, err)
	}
}

func TestOpenTightensLooseMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homed.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil { //nolint:gosec // the point of the test
		t.Fatal(err)
	}
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestReopenKeepsDataAndMigrationsRunOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "homed.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	mustPerson(t, db, "roman", RoleOwner)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.People().Get(ctx, "roman"); err != nil {
		t.Errorf("person lost after reopen: %v", err)
	}
	var n int
	if err := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != 1 {
		t.Errorf("schema_migrations rows = %d, %v; want 1", n, err)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "homed.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(ctx, `INSERT INTO schema_migrations VALUES (999, 'future', 0)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(ctx, path); err == nil {
		t.Fatal("Open accepted a database from a newer homed")
	}
}

func TestPeople(t *testing.T) {
	ctx := context.Background()
	db, c := newTestDB(t)
	r := db.People()

	if has, err := r.HasOwner(ctx); err != nil || has {
		t.Fatalf("HasOwner on empty = %v, %v", has, err)
	}
	if _, err := r.Get(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing = %v, want ErrNotFound", err)
	}

	handle := NewUserHandle()
	roman := Person{ID: "roman", Name: "Roman", Role: RoleOwner, Plugins: []string{"*"}, Language: "ru", UserHandle: handle}
	if err := r.Create(ctx, roman); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, roman); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate id = %v, want ErrConflict", err)
	}
	dup := Person{ID: "other", Name: "Other", Role: RoleMember, UserHandle: handle}
	if err := r.Create(ctx, dup); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate user handle = %v, want ErrConflict", err)
	}
	if err := r.Create(ctx, Person{ID: "x", Role: "boss", UserHandle: handle}); err == nil {
		t.Error("bad role accepted")
	}
	if err := r.Create(ctx, Person{ID: "x", Role: RoleMember}); err == nil {
		t.Error("empty user handle accepted")
	}

	got, err := r.Get(ctx, "roman")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Roman" || got.Role != RoleOwner || got.Language != "ru" || string(got.UserHandle) != string(handle) ||
		len(got.Plugins) != 1 || got.Plugins[0] != "*" || !got.CreatedAt.Equal(epoch) || !got.UpdatedAt.Equal(epoch) {
		t.Errorf("Get = %+v", got)
	}
	if byHandle, err := r.GetByUserHandle(ctx, handle); err != nil || byHandle.ID != "roman" {
		t.Errorf("GetByUserHandle = %+v, %v", byHandle, err)
	}
	if _, err := r.GetByUserHandle(ctx, []byte("nope")); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByUserHandle missing = %v", err)
	}
	if has, err := r.HasOwner(ctx); err != nil || !has {
		t.Errorf("HasOwner = %v, %v", has, err)
	}

	// Ordering: owners first, then by name ignoring case.
	for _, p := range []Person{
		{ID: "zoya", Name: "zoya", Role: RoleMember},
		{ID: "anna", Name: "Anna", Role: RoleMember},
	} {
		p.UserHandle = NewUserHandle()
		if err := r.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	list, err := r.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range list {
		ids = append(ids, p.ID)
	}
	if fmt.Sprint(ids) != "[roman anna zoya]" {
		t.Errorf("List order = %v", ids)
	}

	// Update changes the editable fields and UpdatedAt, never the handle or CreatedAt.
	c.advance(time.Hour)
	got.Name, got.Plugins, got.Language, got.UserHandle = "Roma", []string{"tv", "kettle"}, "en", []byte("ignored")
	if err := r.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	upd, _ := r.Get(ctx, "roman")
	if upd.Name != "Roma" || upd.Language != "en" || fmt.Sprint(upd.Plugins) != "[tv kettle]" ||
		string(upd.UserHandle) != string(handle) || !upd.CreatedAt.Equal(epoch) || !upd.UpdatedAt.Equal(epoch.Add(time.Hour)) {
		t.Errorf("after Update = %+v", upd)
	}
	if err := r.Update(ctx, Person{ID: "ghost", Role: RoleMember}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update missing = %v", err)
	}
}

func TestPersonDeleteCascades(t *testing.T) {
	ctx := context.Background()
	db, c := newTestDB(t)
	mustPerson(t, db, "roman", RoleOwner)
	mustPerson(t, db, "tanya", RoleMember)
	mustPasskey(t, db, "pk-roman", "roman")
	mustPasskey(t, db, "pk-tanya", "tanya")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.Sessions().Create(ctx, Session{ID: "s_1", TokenHash: "h1", PersonID: "tanya"}))
	must(db.Keys().Create(ctx, Key{ID: "k_1", PersonID: "tanya", SHA256: "aa"}))
	must(db.Invites().Create(ctx, Invite{ID: "i_1", TokenHash: "t1", Kind: InviteMember, PersonID: "tanya", ExpiresAt: c.now().Add(time.Hour)}))
	must(db.Connections().Create(ctx, Connection{ID: "cn_1", PersonID: "tanya", Kind: ConnectionSite, State: ConnectionLive}))
	_, err := db.Audit().Append(ctx, []AuditRecord{{Kind: AuditAdmin, PersonID: "tanya", Person: "Tanya", Action: "auth.login", OK: true}})
	must(err)

	must(db.People().Delete(ctx, "tanya"))

	if _, err := db.Passkeys().Get(ctx, "pk-tanya"); !errors.Is(err, ErrNotFound) {
		t.Errorf("passkey survived: %v", err)
	}
	if _, err := db.Sessions().GetByTokenHash(ctx, "h1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("session survived: %v", err)
	}
	if _, err := db.Keys().Get(ctx, "k_1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("key survived: %v", err)
	}
	if _, err := db.Invites().GetByTokenHash(ctx, "t1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("invite survived: %v", err)
	}
	if _, err := db.Connections().Get(ctx, "cn_1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("connection survived: %v", err)
	}
	if _, err := db.Passkeys().Get(ctx, "pk-roman"); err != nil {
		t.Errorf("other person's passkey lost: %v", err)
	}
	recs, err := db.Audit().Query(ctx, AuditFilter{})
	if err != nil || len(recs) != 1 || recs[0].Person != "Tanya" {
		t.Errorf("audit after delete = %+v, %v", recs, err)
	}
	if err := db.People().Delete(ctx, "tanya"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v", err)
	}
}

func TestChildRowsNeedAPerson(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	err := db.Passkeys().Create(ctx, Passkey{ID: "pk", PersonID: "ghost", Credential: []byte("c")})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("passkey for missing person = %v, want ErrNotFound", err)
	}
}

func TestPasskeys(t *testing.T) {
	ctx := context.Background()
	db, c := newTestDB(t)
	r := db.Passkeys()
	mustPerson(t, db, "roman", RoleOwner)
	mustPerson(t, db, "tanya", RoleMember)

	if n, err := r.Count(ctx); err != nil || n != 0 {
		t.Fatalf("Count empty = %d, %v", n, err)
	}
	mustPasskey(t, db, "a", "roman")
	c.advance(time.Minute)
	mustPasskey(t, db, "b", "roman")
	mustPasskey(t, db, "t", "tanya")
	if err := r.Create(ctx, Passkey{ID: "a", PersonID: "roman", Credential: []byte("x")}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate credential id = %v", err)
	}
	if n, _ := r.Count(ctx); n != 3 {
		t.Errorf("Count = %d, want 3", n)
	}
	list, err := r.ListByPerson(ctx, "roman")
	if err != nil || len(list) != 2 || list[0].ID != "a" || list[1].ID != "b" {
		t.Errorf("ListByPerson = %+v, %v", list, err)
	}
	if empty, err := r.ListByPerson(ctx, "nobody"); err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("ListByPerson(nobody) = %#v, %v; want empty non-nil", empty, err)
	}

	if err := r.Rename(ctx, "a", "MacBook"); err != nil {
		t.Fatal(err)
	}
	c.advance(time.Hour)
	if err := r.RecordUse(ctx, "a", []byte("new-cred"), c.now()); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Get(ctx, "a")
	if got.Name != "MacBook" || string(got.Credential) != "new-cred" || !got.LastUsedAt.Equal(c.now()) {
		t.Errorf("Get = %+v", got)
	}
	if err := r.Rename(ctx, "zz", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Rename missing = %v", err)
	}

	// Last passkey: kept; the second-to-last may go.
	if err := r.Delete(ctx, "t"); !errors.Is(err, ErrLastPasskey) {
		t.Errorf("Delete last = %v, want ErrLastPasskey", err)
	}
	if _, err := r.Get(ctx, "t"); err != nil {
		t.Errorf("last passkey was deleted: %v", err)
	}
	if err := r.Delete(ctx, "a"); err != nil {
		t.Errorf("Delete with a sibling = %v", err)
	}
	if err := r.Delete(ctx, "b"); !errors.Is(err, ErrLastPasskey) {
		t.Errorf("Delete now-last = %v", err)
	}
	if err := r.Delete(ctx, "zz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete missing = %v", err)
	}

	if n, err := r.DeleteAll(ctx); err != nil || n != 2 {
		t.Errorf("DeleteAll = %d, %v; want 2", n, err)
	}
	if n, _ := r.Count(ctx); n != 0 {
		t.Errorf("Count after DeleteAll = %d", n)
	}
}

// Two passkeys, deleted at the same moment: exactly one delete may win.
func TestPasskeyDeleteIsAtomic(t *testing.T) {
	ctx := context.Background()
	for round := 0; round < 20; round++ {
		db, _ := newTestDB(t)
		mustPerson(t, db, "roman", RoleOwner)
		mustPasskey(t, db, "a", "roman")
		mustPasskey(t, db, "b", "roman")
		var wg sync.WaitGroup
		var ok, last atomic.Int32
		for _, id := range []string{"a", "b"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				switch err := db.Passkeys().Delete(ctx, id); {
				case err == nil:
					ok.Add(1)
				case errors.Is(err, ErrLastPasskey):
					last.Add(1)
				default:
					t.Errorf("Delete %s: %v", id, err)
				}
			}()
		}
		wg.Wait()
		if ok.Load() != 1 || last.Load() != 1 {
			t.Fatalf("round %d: %d deleted, %d refused; want 1 and 1", round, ok.Load(), last.Load())
		}
		if n, _ := db.Passkeys().Count(ctx); n != 1 {
			t.Fatalf("round %d: %d passkeys left, want 1", round, n)
		}
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	db, c := newTestDB(t)
	r := db.Sessions()
	mustPerson(t, db, "roman", RoleOwner)
	mustPerson(t, db, "tanya", RoleMember)

	mk := func(id, hash, person string) {
		t.Helper()
		if err := r.Create(ctx, Session{ID: id, TokenHash: hash, PersonID: person, PasskeyID: "pk", Device: "Safari"}); err != nil {
			t.Fatal(err)
		}
	}
	mk("s_1", "h1", "roman")
	c.advance(time.Minute)
	mk("s_2", "h2", "roman")
	c.advance(time.Minute)
	mk("s_3", "h3", "roman")
	mk("s_4", "h4", "tanya")
	if err := r.Create(ctx, Session{ID: "s_5", TokenHash: "h1", PersonID: "roman"}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate token hash = %v", err)
	}

	got, err := r.GetByTokenHash(ctx, "h2")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "s_2" || got.Device != "Safari" || got.PasskeyID != "pk" ||
		!got.CreatedAt.Equal(epoch.Add(time.Minute)) || !got.LastSeenAt.Equal(got.CreatedAt) || !got.StepUpAt.IsZero() {
		t.Errorf("GetByTokenHash = %+v", got)
	}
	if _, err := r.GetByTokenHash(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing hash = %v", err)
	}

	c.advance(time.Hour)
	if err := r.Touch(ctx, "s_1", c.now()); err != nil {
		t.Fatal(err)
	}
	if err := r.SetStepUp(ctx, "s_2", c.now()); err != nil {
		t.Fatal(err)
	}
	list, _ := r.ListByPerson(ctx, "roman")
	if len(list) != 3 || list[0].ID != "s_1" {
		t.Errorf("ListByPerson order: first = %s of %d, want s_1 (touched last)", list[0].ID, len(list))
	}
	s2, _ := r.GetByTokenHash(ctx, "h2")
	if !s2.StepUpAt.Equal(c.now()) {
		t.Errorf("StepUpAt = %v", s2.StepUpAt)
	}
	if err := r.Touch(ctx, "nope", c.now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("Touch missing = %v", err)
	}

	// DeleteExpired: s_1 was touched an hour later, so the idle limit hits s_2, s_3 and tanya's s_4.
	n, err := r.DeleteExpired(ctx, epoch.Add(time.Hour), time.Time{})
	if err != nil || n != 3 {
		t.Errorf("DeleteExpired idle = %d, %v; want 3", n, err)
	}
	if n, err := r.DeleteExpired(ctx, time.Time{}, time.Time{}); err != nil || n != 0 {
		t.Errorf("DeleteExpired with zero bounds = %d, %v; want 0", n, err)
	}
	if n, err := r.DeleteExpired(ctx, time.Time{}, epoch.Add(time.Second)); err != nil || n != 1 {
		t.Errorf("DeleteExpired created = %d, %v; want 1 (s_1)", n, err)
	}
	mk("s_6", "h6", "roman")
	mk("s_7", "h7", "roman")
	if n, err := r.DeleteByPerson(ctx, "roman", "s_6"); err != nil || n != 1 {
		t.Errorf("DeleteByPerson keep = %d, %v; want 1", n, err)
	}
	if _, err := r.GetByTokenHash(ctx, "h6"); err != nil {
		t.Errorf("kept session gone: %v", err)
	}
	if err := r.Delete(ctx, "s_6"); err != nil {
		t.Error(err)
	}
	if err := r.Delete(ctx, "s_6"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete = %v", err)
	}
	mk("s_8", "h8", "tanya")
	if n, err := r.DeleteAll(ctx); err != nil || n != 1 {
		t.Errorf("DeleteAll = %d, %v; want 1", n, err)
	}
}

func TestKeys(t *testing.T) {
	ctx := context.Background()
	db, c := newTestDB(t)
	r := db.Keys()
	mustPerson(t, db, "roman", RoleOwner)
	mustPerson(t, db, "tanya", RoleMember)

	k1 := Key{ID: "k_1", PersonID: "roman", SHA256: "h1", Label: "ChatGPT Site", Hint: "ghk_AbCd", ConnectionID: "cn_1"}
	if err := r.Create(ctx, k1); err != nil {
		t.Fatal(err)
	}
	c.advance(time.Minute)
	if err := r.Create(ctx, Key{ID: "k_2", PersonID: "roman", SHA256: "h2"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, Key{ID: "k_3", PersonID: "tanya", SHA256: "h3"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, Key{ID: "k_9", PersonID: "roman", SHA256: "h1"}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate hash = %v", err)
	}
	if err := r.Create(ctx, Key{ID: "k_1", PersonID: "roman", SHA256: "h9"}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate id = %v", err)
	}
	got, err := r.Get(ctx, "k_1")
	if err != nil || got.Label != "ChatGPT Site" || got.Hint != "ghk_AbCd" || got.ConnectionID != "cn_1" ||
		!got.CreatedAt.Equal(epoch) || !got.LastUsedAt.IsZero() {
		t.Errorf("Get = %+v, %v", got, err)
	}
	all, _ := r.List(ctx)
	if len(all) != 3 {
		t.Errorf("List = %d keys", len(all))
	}
	mine, _ := r.ListByPerson(ctx, "roman")
	if len(mine) != 2 || mine[0].ID != "k_2" {
		t.Errorf("ListByPerson newest first: %+v", mine)
	}
	if err := r.Touch(ctx, "k_1", epoch.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Get(ctx, "k_1"); !got.LastUsedAt.Equal(epoch.Add(time.Hour)) {
		t.Errorf("LastUsedAt = %v", got.LastUsedAt)
	}
	if err := r.Delete(ctx, "k_1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, "k_1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete = %v", err)
	}
}

func TestInvites(t *testing.T) {
	ctx := context.Background()
	db, c := newTestDB(t)
	r := db.Invites()
	mustPerson(t, db, "tanya", RoleMember)

	setup := Invite{ID: "i_1", TokenHash: "setup", Kind: InviteSetup, ExpiresAt: epoch.Add(15 * time.Minute)}
	member := Invite{ID: "i_2", TokenHash: "member", Kind: InviteMember, PersonID: "tanya", CreatedBy: "roman", ExpiresAt: epoch.Add(24 * time.Hour)}
	for _, inv := range []Invite{setup, member} {
		if err := r.Create(ctx, inv); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Create(ctx, Invite{ID: "i_3", TokenHash: "x", Kind: "weird", ExpiresAt: epoch}); err == nil {
		t.Error("bad kind accepted")
	}
	if err := r.Create(ctx, Invite{ID: "i_4", TokenHash: "setup", Kind: InviteSetup, ExpiresAt: epoch}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate hash = %v", err)
	}
	// A setup link with no person row yet round-trips with an empty PersonID.
	got, err := r.GetByTokenHash(ctx, "setup")
	if err != nil || got.PersonID != "" || got.Kind != InviteSetup || !got.CreatedAt.Equal(epoch) {
		t.Errorf("setup invite = %+v, %v", got, err)
	}
	if got, _ := r.GetByTokenHash(ctx, "member"); got.PersonID != "tanya" || got.CreatedBy != "roman" {
		t.Errorf("member invite = %+v", got)
	}

	if list, _ := r.ListActive(ctx, InviteSetup, c.now()); len(list) != 1 || list[0].ID != "i_1" {
		t.Errorf("ListActive setup = %+v", list)
	}
	if list, _ := r.ListActive(ctx, InviteSetup, epoch.Add(15*time.Minute)); len(list) != 0 {
		t.Errorf("ListActive at the expiry instant = %+v, want none", list)
	}

	// Consume: success, then used, expired and unknown.
	inv, err := r.Consume(ctx, "setup", c.now())
	if err != nil || inv.ID != "i_1" || !inv.UsedAt.Equal(c.now()) {
		t.Errorf("Consume = %+v, %v", inv, err)
	}
	if _, err := r.Consume(ctx, "setup", c.now()); !errors.Is(err, ErrTokenUsed) {
		t.Errorf("second Consume = %v, want ErrTokenUsed", err)
	}
	if _, err := r.Consume(ctx, "member", epoch.Add(25*time.Hour)); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("Consume expired = %v, want ErrTokenExpired", err)
	}
	if cur, _ := r.GetByTokenHash(ctx, "member"); !cur.UsedAt.IsZero() {
		t.Error("failed Consume changed the invite")
	}
	if _, err := r.Consume(ctx, "nope", c.now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("Consume unknown = %v", err)
	}

	// Revoke: used invites report ErrTokenUsed on consume; revoking twice keeps the first time.
	if err := r.Revoke(ctx, "i_2", epoch.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := r.Revoke(ctx, "i_2", epoch.Add(time.Hour)); err != nil {
		t.Errorf("second Revoke = %v", err)
	}
	if cur, _ := r.GetByTokenHash(ctx, "member"); !cur.UsedAt.Equal(epoch.Add(time.Minute)) {
		t.Errorf("UsedAt = %v, want the first revoke time", cur.UsedAt)
	}
	if _, err := r.Consume(ctx, "member", c.now()); !errors.Is(err, ErrTokenUsed) {
		t.Errorf("Consume revoked = %v", err)
	}
	if err := r.Revoke(ctx, "nope", c.now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("Revoke missing = %v", err)
	}
}

func TestInviteRevokeAll(t *testing.T) {
	ctx := context.Background()
	db, c := newTestDB(t)
	r := db.Invites()
	mustPerson(t, db, "tanya", RoleMember)
	mustPerson(t, db, "kid", RoleMember)
	exp := c.now().Add(time.Hour)
	for _, inv := range []Invite{
		{ID: "i_1", TokenHash: "1", Kind: InviteSetup, ExpiresAt: exp},
		{ID: "i_2", TokenHash: "2", Kind: InviteSetup, ExpiresAt: exp},
		{ID: "i_3", TokenHash: "3", Kind: InviteMember, PersonID: "tanya", ExpiresAt: exp},
		{ID: "i_4", TokenHash: "4", Kind: InviteMember, PersonID: "kid", ExpiresAt: exp},
	} {
		if err := r.Create(ctx, inv); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := r.RevokeAll(ctx, InviteMember, "tanya", c.now()); err != nil || n != 1 {
		t.Errorf("RevokeAll person = %d, %v; want 1", n, err)
	}
	if n, err := r.RevokeAll(ctx, InviteSetup, "", c.now()); err != nil || n != 2 {
		t.Errorf("RevokeAll kind = %d, %v; want 2", n, err)
	}
	if n, _ := r.RevokeAll(ctx, InviteSetup, "", c.now()); n != 0 {
		t.Errorf("RevokeAll again = %d, want 0", n)
	}
	if list, _ := r.ListActive(ctx, InviteMember, c.now()); len(list) != 1 || list[0].ID != "i_4" {
		t.Errorf("ListActive = %+v", list)
	}
}

// Many goroutines race to consume one token: exactly one may win.
func TestInviteConsumeSingleWinner(t *testing.T) {
	ctx := context.Background()
	db, c := newTestDB(t)
	if err := db.Invites().Create(ctx, Invite{ID: "i_1", TokenHash: "tok", Kind: InviteSetup, ExpiresAt: c.now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var won, used atomic.Int32
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch _, err := db.Invites().Consume(ctx, "tok", c.now()); {
			case err == nil:
				won.Add(1)
			case errors.Is(err, ErrTokenUsed):
				used.Add(1)
			default:
				t.Errorf("Consume: %v", err)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 || used.Load() != 11 {
		t.Errorf("won=%d used=%d, want 1 and 11", won.Load(), used.Load())
	}
}

func TestConnections(t *testing.T) {
	ctx := context.Background()
	db, c := newTestDB(t)
	r := db.Connections()
	mustPerson(t, db, "roman", RoleOwner)
	mustPerson(t, db, "tanya", RoleMember)

	cn := Connection{
		ID: "cn_1", PersonID: "roman", Kind: ConnectionSite, State: ConnectionPending, Title: "Мой шалаш",
		Detail: json.RawMessage(`{"slug":"moy-shalash"}`),
	}
	if err := r.Create(ctx, cn); err != nil {
		t.Fatal(err)
	}
	c.advance(time.Minute)
	if err := r.Create(ctx, Connection{ID: "cn_2", PersonID: "roman", Kind: ConnectionPlugin, State: ConnectionPending}); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, Connection{ID: "cn_3", PersonID: "tanya", Kind: ConnectionSite, State: ConnectionPending}); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, Connection{ID: "cn_4", PersonID: "tanya", Kind: ConnectionSite, State: ConnectionPending, Detail: json.RawMessage(`{bad`)}); err == nil {
		t.Error("invalid detail JSON accepted")
	}
	got, err := r.Get(ctx, "cn_1")
	if err != nil || string(got.Detail) != `{"slug":"moy-shalash"}` || got.Title != "Мой шалаш" || !got.PublishedAt.IsZero() {
		t.Errorf("Get = %+v, %v", got, err)
	}
	if got, _ := r.Get(ctx, "cn_2"); got.Detail != nil {
		t.Errorf("empty detail = %q, want nil", got.Detail)
	}

	c.advance(time.Hour)
	got.State, got.KeyID, got.Fingerprint, got.PublishedAt, got.LastError = ConnectionLive, "k_1", "fp1", c.now(), ""
	got.Kind, got.PersonID = ConnectionPlugin, "tanya" // immutable: must be ignored
	if err := r.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	upd, _ := r.Get(ctx, "cn_1")
	if upd.State != ConnectionLive || upd.KeyID != "k_1" || upd.Fingerprint != "fp1" || !upd.PublishedAt.Equal(c.now()) ||
		!upd.UpdatedAt.Equal(c.now()) || !upd.CreatedAt.Equal(epoch) || upd.Kind != ConnectionSite || upd.PersonID != "roman" {
		t.Errorf("after Update = %+v", upd)
	}
	if err := r.Update(ctx, Connection{ID: "ghost"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update missing = %v", err)
	}

	all, _ := r.List(ctx)
	var ids []string
	for _, x := range all {
		ids = append(ids, x.ID)
	}
	if fmt.Sprint(ids) != "[cn_2 cn_1 cn_3]" {
		t.Errorf("List order = %v, want by person then newest first", ids)
	}
	if mine, _ := r.ListByPerson(ctx, "tanya"); len(mine) != 1 {
		t.Errorf("ListByPerson = %+v", mine)
	}
	if err := r.Delete(ctx, "cn_1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, "cn_1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete = %v", err)
	}
}

func TestPlugins(t *testing.T) {
	ctx := context.Background()
	db, c := newTestDB(t)
	r := db.Plugins()

	tv := Plugin{ID: "tv", Source: PluginGitHub, Repo: "devXpro/homie-tv", Version: "0.1.0",
		Command: []string{"/bin/0.1.0/homie-tv", "serve"}, Enabled: true}
	if err := r.Create(ctx, tv); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, tv); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate = %v", err)
	}
	if err := r.Create(ctx, Plugin{ID: "kettle", Source: PluginLocal, Command: []string{"/x/kettle"}}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, "tv")
	if err != nil || got.Repo != "devXpro/homie-tv" || !got.Enabled || fmt.Sprint(got.Command) != "[/bin/0.1.0/homie-tv serve]" ||
		!got.InstalledAt.Equal(epoch) || !got.UpdatedAt.Equal(epoch) {
		t.Errorf("Get = %+v, %v", got, err)
	}
	if k, _ := r.Get(ctx, "kettle"); k.Enabled {
		t.Error("Enabled false was stored as true")
	}
	list, _ := r.List(ctx)
	if len(list) != 2 || list[0].ID != "kettle" || list[1].ID != "tv" {
		t.Errorf("List = %+v", list)
	}

	c.advance(time.Hour)
	got.Version, got.PreviousVersion, got.Enabled, got.Command = "0.2.0", "0.1.0", false, []string{"/bin/0.2.0/homie-tv", "serve"}
	got.InstalledAt = epoch.Add(99 * time.Hour) // must be ignored
	if err := r.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	upd, _ := r.Get(ctx, "tv")
	if upd.Version != "0.2.0" || upd.PreviousVersion != "0.1.0" || upd.Enabled || fmt.Sprint(upd.Command) != "[/bin/0.2.0/homie-tv serve]" ||
		!upd.InstalledAt.Equal(epoch) || !upd.UpdatedAt.Equal(c.now()) {
		t.Errorf("after Update = %+v", upd)
	}
	if err := r.Update(ctx, Plugin{ID: "ghost"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update missing = %v", err)
	}
	if err := r.Delete(ctx, "tv"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, "tv"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete = %v", err)
	}
}

func TestAuditAppendAndGet(t *testing.T) {
	ctx := context.Background()
	db, c := newTestDB(t)
	r := db.Audit()

	if out, err := r.Append(ctx, nil); err != nil || len(out) != 0 {
		t.Fatalf("Append(nil) = %v, %v", out, err)
	}
	in := []AuditRecord{
		{Kind: AuditTool, PersonID: "roman", Person: "Roman", Via: "site", Tool: "tv_power_on", PluginID: "tv",
			Args: json.RawMessage(`{"tv":"tv","pin":"[redacted]"}`), OK: true, Duration: 1500 * time.Microsecond},
		{Kind: AuditAdmin, Person: "Roman", Via: "admin", Action: "plugin.install", Target: "tv", PluginID: "tv",
			OK: false, Error: "checksum mismatch", At: epoch.Add(-time.Hour)},
	}
	out, err := r.Append(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].ID == 0 || out[1].ID != out[0].ID+1 {
		t.Fatalf("ids = %+v", out)
	}
	if !out[0].At.Equal(c.now()) {
		t.Errorf("zero At was not defaulted: %v", out[0].At)
	}
	if in[0].ID != 0 {
		t.Error("Append changed the caller's slice")
	}
	got, err := r.Get(ctx, out[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Args) != `{"tv":"tv","pin":"[redacted]"}` || !got.OK || got.Duration != 1500*time.Microsecond ||
		got.Tool != "tv_power_on" || got.Kind != AuditTool || got.PersonID != "roman" {
		t.Errorf("Get = %+v", got)
	}
	adm, _ := r.Get(ctx, out[1].ID)
	if adm.Args != nil || adm.OK || adm.Error != "checksum mismatch" || adm.Action != "plugin.install" || !adm.At.Equal(epoch.Add(-time.Hour)) {
		t.Errorf("admin record = %+v", adm)
	}
	if _, err := r.Get(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing = %v", err)
	}
	// A bad batch stores nothing.
	if _, err := r.Append(ctx, []AuditRecord{{Kind: AuditTool}, {Kind: "weird"}}); err == nil {
		t.Error("bad kind accepted")
	}
	if all, _ := r.Query(ctx, AuditFilter{}); len(all) != 2 {
		t.Errorf("records after a refused batch = %d, want 2", len(all))
	}
}

func TestAuditQueryFilters(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	r := db.Audit()
	at := func(m int) time.Time { return epoch.Add(time.Duration(m) * time.Minute) }
	_, err := r.Append(ctx, []AuditRecord{
		{At: at(0), Kind: AuditTool, PersonID: "roman", Via: "site", Tool: "tv_power_on", PluginID: "tv", OK: true},
		{At: at(1), Kind: AuditTool, PersonID: "tanya", Via: "mcp", Tool: "tv_set_volume", PluginID: "tv", OK: false, Error: "offline"},
		{At: at(2), Kind: AuditTool, PersonID: "roman", Via: "cli", Tool: "kettle_boil", PluginID: "kettle", OK: true},
		{At: at(3), Kind: AuditAdmin, PersonID: "roman", Via: "admin", Action: "auth.login", OK: true},
		{At: at(4), Kind: AuditAdmin, Via: "admin", Action: "auth.login", OK: false, Error: "unknown passkey"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		f    AuditFilter
		want []string // actions or tools, newest first
	}{
		{"all", AuditFilter{}, []string{"auth.login", "auth.login", "kettle_boil", "tv_set_volume", "tv_power_on"}},
		{"tool kind", AuditFilter{Kind: AuditTool}, []string{"kettle_boil", "tv_set_volume", "tv_power_on"}},
		{"admin kind", AuditFilter{Kind: AuditAdmin}, []string{"auth.login", "auth.login"}},
		{"person", AuditFilter{PersonID: "roman"}, []string{"auth.login", "kettle_boil", "tv_power_on"}},
		{"plugin", AuditFilter{PluginID: "tv"}, []string{"tv_set_volume", "tv_power_on"}},
		{"tool", AuditFilter{Tool: "kettle_boil"}, []string{"kettle_boil"}},
		{"via any of", AuditFilter{Via: []string{"site", "mcp"}}, []string{"tv_set_volume", "tv_power_on"}},
		{"failed", AuditFilter{Status: AuditFailed}, []string{"auth.login", "tv_set_volume"}},
		{"ok", AuditFilter{Status: AuditOK, Kind: AuditTool}, []string{"kettle_boil", "tv_power_on"}},
		{"since and until", AuditFilter{Since: at(1), Until: at(3)}, []string{"kettle_boil", "tv_set_volume"}},
		{"combined", AuditFilter{Kind: AuditTool, PersonID: "roman", Status: AuditOK, Via: []string{"cli"}}, []string{"kettle_boil"}},
		{"nothing", AuditFilter{Tool: "nope"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recs, err := r.Query(ctx, tc.f)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, rec := range recs {
				if rec.Tool != "" {
					got = append(got, rec.Tool)
				} else {
					got = append(got, rec.Action)
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAuditPaginationLimitsAndPrune(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	r := db.Audit()
	var batch []AuditRecord
	for i := range 130 {
		batch = append(batch, AuditRecord{At: epoch.Add(time.Duration(i) * time.Second), Kind: AuditTool, Tool: fmt.Sprintf("t_%03d", i), OK: true})
	}
	if _, err := r.Append(ctx, batch); err != nil {
		t.Fatal(err)
	}
	// Default page is 50; walking pages by BeforeID visits everything exactly once, newest first.
	var seen []int64
	f := AuditFilter{}
	for pages := 0; ; pages++ {
		page, err := r.Query(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		if pages == 0 && len(page) != 50 {
			t.Fatalf("default page = %d, want 50", len(page))
		}
		for _, rec := range page {
			seen = append(seen, rec.ID)
		}
		if len(page) < 50 {
			break
		}
		f.BeforeID = page[len(page)-1].ID
	}
	if len(seen) != 130 {
		t.Fatalf("walked %d records, want 130", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] >= seen[i-1] {
			t.Fatalf("ids not strictly decreasing at %d: %d then %d", i, seen[i-1], seen[i])
		}
	}
	if page, _ := r.Query(ctx, AuditFilter{Limit: 1000}); len(page) != 130 {
		t.Errorf("Limit 1000 returned %d, want 130 (cap 200)", len(page))
	}
	if page, _ := r.Query(ctx, AuditFilter{Limit: 7}); len(page) != 7 {
		t.Errorf("Limit 7 returned %d", len(page))
	}

	n, err := r.Prune(ctx, epoch.Add(100*time.Second))
	if err != nil || n != 100 {
		t.Errorf("Prune = %d, %v; want 100", n, err)
	}
	if n, _ := r.Prune(ctx, time.Time{}); n != 0 {
		t.Errorf("Prune(zero) = %d, want 0", n)
	}
	// Ids are not reused after a prune.
	out, err := r.Append(ctx, []AuditRecord{{Kind: AuditTool, Tool: "after"}})
	if err != nil || out[0].ID <= seen[0] {
		t.Errorf("id after prune = %v, %v; want > %d", out, err, seen[0])
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	db, c := newTestDB(t)
	r := db.Settings()
	got, err := r.Get(ctx)
	if err != nil || got != (HomeSettings{Language: "ru"}) {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	c.advance(time.Hour)
	want := HomeSettings{HomeName: "Мой шалаш", Language: "en", ConnectionKind: ConnectionSite, SiteSignOut: true}
	if err := r.Put(ctx, want); err != nil {
		t.Fatal(err)
	}
	want.UpdatedAt = c.now()
	if got, _ := r.Get(ctx); got != want {
		t.Errorf("Get = %+v, want %+v", got, want)
	}
	c.advance(time.Hour)
	if err := r.Put(ctx, HomeSettings{HomeName: "Home", Language: "ru"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Get(ctx); got.HomeName != "Home" || got.SiteSignOut || got.ConnectionKind != "" || !got.UpdatedAt.Equal(c.now()) {
		t.Errorf("after replace = %+v", got)
	}
}

func TestInTx(t *testing.T) {
	ctx := context.Background()
	db, c := newTestDB(t)

	t.Run("commit", func(t *testing.T) {
		err := db.InTx(ctx, func(tx Store) error {
			mustPerson(t, tx, "roman", RoleOwner)
			mustPasskey(t, tx, "pk", "roman")
			return tx.Settings().Put(ctx, HomeSettings{HomeName: "A", Language: "ru"})
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Passkeys().Get(ctx, "pk"); err != nil {
			t.Errorf("committed passkey missing: %v", err)
		}
	})

	t.Run("rollback", func(t *testing.T) {
		boom := errors.New("boom")
		err := db.InTx(ctx, func(tx Store) error {
			mustPerson(t, tx, "tanya", RoleMember)
			if err := tx.Settings().Put(ctx, HomeSettings{HomeName: "B", Language: "en"}); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("InTx = %v, want boom", err)
		}
		if _, err := db.People().Get(ctx, "tanya"); !errors.Is(err, ErrNotFound) {
			t.Errorf("rolled back person visible: %v", err)
		}
		if s, _ := db.Settings().Get(ctx); s.HomeName != "A" {
			t.Errorf("rolled back settings visible: %+v", s)
		}
	})

	t.Run("nested joins the outer transaction", func(t *testing.T) {
		boom := errors.New("boom")
		err := db.InTx(ctx, func(tx Store) error {
			if err := tx.InTx(ctx, func(inner Store) error {
				mustPerson(t, inner, "kid", RoleMember)
				return nil
			}); err != nil {
				return err
			}
			if _, err := tx.People().Get(ctx, "kid"); err != nil {
				t.Errorf("inner write not visible to the outer: %v", err)
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatal(err)
		}
		if _, err := db.People().Get(ctx, "kid"); !errors.Is(err, ErrNotFound) {
			t.Errorf("inner write survived the outer rollback: %v", err)
		}
	})

	t.Run("Close inside a transaction is refused", func(t *testing.T) {
		_ = db.InTx(ctx, func(tx Store) error {
			if err := tx.Close(); err == nil {
				t.Error("Close succeeded inside InTx")
			}
			return nil
		})
	})

	// The first-run flow of PLAN §5.8: consume the setup token, create the owner, store the first passkey, together.
	t.Run("setup flow", func(t *testing.T) {
		db2, c2 := newTestDB(t)
		if err := db2.Invites().Create(ctx, Invite{ID: "i_1", TokenHash: "tok", Kind: InviteSetup, ExpiresAt: c2.now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		run := func(passkeyID string) error {
			return db2.InTx(ctx, func(tx Store) error {
				if _, err := tx.Invites().Consume(ctx, "tok", c2.now()); err != nil {
					return err
				}
				if err := tx.People().Create(ctx, Person{ID: "roman", Name: "Roman", Role: RoleOwner, UserHandle: NewUserHandle()}); err != nil {
					return err
				}
				return tx.Passkeys().Create(ctx, Passkey{ID: passkeyID, PersonID: "roman", Credential: []byte("c")})
			})
		}
		// First attempt fails at the passkey (empty id): the token must stay usable and no owner may exist.
		if err := run(""); err == nil {
			t.Fatal("expected failure")
		}
		if has, _ := db2.People().HasOwner(ctx); has {
			t.Error("owner created by a failed registration")
		}
		if err := run("pk"); err != nil {
			t.Fatalf("retry: %v", err)
		}
		if err := run("pk2"); !errors.Is(err, ErrTokenUsed) {
			t.Errorf("replay = %v, want ErrTokenUsed", err)
		}
	})
	_ = c
}

// Writers and readers on separate pool connections must not fail with SQLITE_BUSY.
func TestConcurrentUse(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	mustPerson(t, db, "roman", RoleOwner)
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 25 {
				if _, err := db.Audit().Append(ctx, []AuditRecord{{Kind: AuditTool, Tool: fmt.Sprintf("t_%d_%d", w, i), OK: true}}); err != nil {
					t.Errorf("append: %v", err)
					return
				}
				if err := db.InTx(ctx, func(tx Store) error {
					if _, err := tx.People().Get(ctx, "roman"); err != nil {
						return err
					}
					return tx.Keys().Create(ctx, Key{ID: NewID("k"), PersonID: "roman", SHA256: NewID("h")})
				}); err != nil {
					t.Errorf("tx: %v", err)
					return
				}
				if _, err := db.Audit().Query(ctx, AuditFilter{Limit: 10}); err != nil {
					t.Errorf("query: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if recs, _ := db.Audit().Query(ctx, AuditFilter{Limit: 200}); len(recs) != 200 {
		t.Errorf("records = %d, want 200", len(recs))
	}
	if keys, _ := db.Keys().List(ctx); len(keys) != 200 {
		t.Errorf("keys = %d, want 200", len(keys))
	}
}

func TestCancelledContext(t *testing.T) {
	db, _ := newTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.People().List(ctx); err == nil {
		t.Error("List ran with a cancelled context")
	}
}

func TestNewID(t *testing.T) {
	a, b := NewID("k"), NewID("k")
	if a == b || len(a) != len("k_")+16 || a[:2] != "k_" {
		t.Errorf("NewID = %q, %q", a, b)
	}
	if h := NewUserHandle(); len(h) != 32 {
		t.Errorf("user handle is %d bytes", len(h))
	}
}
