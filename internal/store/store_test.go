package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/devXpro/mcp-gatekeeper/core"
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

func newTestStore(t *testing.T) (*Store, *clock) {
	t.Helper()
	c := &clock{t: epoch}
	st, err := Open(context.Background(), Options{Path: filepath.Join(t.TempDir(), "homie.db"), Clock: c.now})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, c
}

func TestOpenFileModes(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "homie.db")
	st, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Settings().Put(ctx, HomeSettings{Language: "en"}); err != nil { // a write makes -wal and -shm
		t.Fatal(err)
	}
	for _, f := range []struct {
		path string
		mode os.FileMode
	}{{dir, 0o700}, {path, 0o600}, {path + "-wal", 0o600}, {path + "-shm", 0o600}} {
		fi, err := os.Stat(f.path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != f.mode {
			t.Errorf("%s: mode %o, want %o", filepath.Base(f.path), fi.Mode().Perm(), f.mode)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopening keeps the data and applies nothing twice.
	st, err = Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if hs, err := st.Settings().Get(ctx); err != nil || hs.Language != "en" {
		t.Errorf("after reopen = %+v, %v", hs, err)
	}
}

func TestPlugins(t *testing.T) {
	ctx := context.Background()
	st, c := newTestStore(t)
	r := st.Plugins()

	tv := Plugin{ID: "tv", Source: PluginGitHub, Repo: "devXpro/homie-tv", Version: "0.1.0",
		Command: []string{"/bin/0.1.0/homie-tv", "serve"}, Enabled: true}
	if err := r.Create(ctx, tv); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, tv); !errors.Is(err, core.ErrConflict) {
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
	if err := r.Update(ctx, Plugin{ID: "ghost"}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Update missing = %v", err)
	}
	if err := r.Delete(ctx, "tv"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, "tv"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get after Delete = %v", err)
	}
	if err := r.Delete(ctx, "tv"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Delete missing = %v", err)
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	st, c := newTestStore(t)
	r := st.Settings()
	got, err := r.Get(ctx)
	if err != nil || got != (HomeSettings{Language: "ru"}) {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	c.advance(time.Hour)
	want := HomeSettings{Language: "en", HomeName: "Дом", AccessResetAt: epoch.Add(time.Minute), AccessResetBy: "/usr/bin/x (pid 7)"}
	if err := r.Put(ctx, want); err != nil {
		t.Fatal(err)
	}
	want.UpdatedAt = c.now()
	if got, _ := r.Get(ctx); got != want {
		t.Errorf("Get = %+v, want %+v", got, want)
	}
	c.advance(time.Hour)
	err = r.Update(ctx, func(hs *HomeSettings) error {
		hs.HomeName = ""
		return nil
	})
	if got, _ := r.Get(ctx); err != nil || got.HomeName != "" || got.Language != "en" || !got.UpdatedAt.Equal(c.now()) {
		t.Errorf("after Update = %+v, %v", got, err)
	}
	boom := errors.New("boom")
	if err := r.Update(ctx, func(hs *HomeSettings) error { hs.Language = "ru"; return boom }); !errors.Is(err, boom) {
		t.Errorf("Update error = %v", err)
	}
	if got, _ := r.Get(ctx); got.Language != "en" {
		t.Errorf("a failed Update stored %+v", got)
	}
}

func TestActivityAppendAndGet(t *testing.T) {
	ctx := context.Background()
	st, c := newTestStore(t)
	r := st.Activity()

	if out, err := r.Append(ctx, nil); err != nil || len(out) != 0 {
		t.Fatalf("Append(nil) = %v, %v", out, err)
	}
	in := []ActivityRecord{
		{PersonID: "per_alice", Person: "Alice", Via: "chatgpt", Tool: "tv_power_on", PluginID: "tv",
			Args: json.RawMessage(`{"tv":"tv","pin":"[redacted]"}`), OK: true, Duration: 1500 * time.Microsecond},
		{Person: "local", Via: "cli", Tool: "tv_pair", PluginID: "tv", OK: false, Error: "the TV did not answer",
			At: epoch.Add(-time.Hour)},
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
		got.Tool != "tv_power_on" || got.PersonID != "per_alice" || got.Via != "chatgpt" {
		t.Errorf("Get = %+v", got)
	}
	second, _ := r.Get(ctx, out[1].ID)
	if second.Args != nil || second.OK || second.Error != "the TV did not answer" || !second.At.Equal(epoch.Add(-time.Hour)) {
		t.Errorf("second record = %+v", second)
	}
	if _, err := r.Get(ctx, 9999); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get missing = %v", err)
	}
}

func TestActivityQueryFilters(t *testing.T) {
	ctx := context.Background()
	st, _ := newTestStore(t)
	r := st.Activity()
	at := func(m int) time.Time { return epoch.Add(time.Duration(m) * time.Minute) }
	_, err := r.Append(ctx, []ActivityRecord{
		{At: at(0), PersonID: "per_alice", Via: "chatgpt", Tool: "tv_power_on", PluginID: "tv", OK: true},
		{At: at(1), PersonID: "per_bella", Via: "claude", Tool: "tv_set_volume", PluginID: "tv", OK: false, Error: "offline"},
		{At: at(2), PersonID: "per_alice", Via: "cli", Tool: "kettle_boil", PluginID: "kettle", OK: true},
		{At: at(3), PersonID: "per_alice", Via: "admin", Tool: "tv_list", PluginID: "tv", OK: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		f    ActivityFilter
		want []string // tools, newest first
	}{
		{"all", ActivityFilter{}, []string{"tv_list", "kettle_boil", "tv_set_volume", "tv_power_on"}},
		{"person", ActivityFilter{PersonID: "per_alice"}, []string{"tv_list", "kettle_boil", "tv_power_on"}},
		{"plugin", ActivityFilter{PluginID: "tv"}, []string{"tv_list", "tv_set_volume", "tv_power_on"}},
		{"tool", ActivityFilter{Tool: "kettle_boil"}, []string{"kettle_boil"}},
		{"via any of", ActivityFilter{Via: []string{"chatgpt", "claude"}}, []string{"tv_set_volume", "tv_power_on"}},
		{"failed", ActivityFilter{Status: ActivityFailed}, []string{"tv_set_volume"}},
		{"ok", ActivityFilter{Status: ActivityOK}, []string{"tv_list", "kettle_boil", "tv_power_on"}},
		{"since and until", ActivityFilter{Since: at(1), Until: at(3)}, []string{"kettle_boil", "tv_set_volume"}},
		{"combined", ActivityFilter{PersonID: "per_alice", Status: ActivityOK, Via: []string{"cli"}}, []string{"kettle_boil"}},
		{"nothing", ActivityFilter{Tool: "nope"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recs, err := r.Query(ctx, tc.f)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, rec := range recs {
				got = append(got, rec.Tool)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestActivityPaginationLimitsAndPrune(t *testing.T) {
	ctx := context.Background()
	st, _ := newTestStore(t)
	r := st.Activity()
	var batch []ActivityRecord
	for i := range 130 {
		batch = append(batch, ActivityRecord{At: epoch.Add(time.Duration(i) * time.Second), Tool: fmt.Sprintf("t_%03d", i), OK: true})
	}
	if _, err := r.Append(ctx, batch); err != nil {
		t.Fatal(err)
	}
	var seen []int64
	f := ActivityFilter{}
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
	if page, _ := r.Query(ctx, ActivityFilter{Limit: 1000}); len(page) != 130 {
		t.Errorf("Limit 1000 returned %d, want 130 (cap 200)", len(page))
	}
	if page, _ := r.Query(ctx, ActivityFilter{Limit: 7}); len(page) != 7 {
		t.Errorf("Limit 7 returned %d", len(page))
	}
	n, err := r.Prune(ctx, epoch.Add(100*time.Second))
	if err != nil || n != 100 {
		t.Errorf("Prune = %d, %v; want 100", n, err)
	}
	if n, _ := r.Prune(ctx, time.Time{}); n != 0 {
		t.Errorf("Prune(zero) = %d, want 0", n)
	}
	out, err := r.Append(ctx, []ActivityRecord{{Tool: "after"}})
	if err != nil || out[0].ID <= seen[0] {
		t.Errorf("id after prune = %v, %v; want > %d", out, err, seen[0])
	}
}

// The hub's tables join the gatekeeper's transactions: one rollback drops both.
func TestTransactionsSpanBothSchemas(t *testing.T) {
	ctx := context.Background()
	st, _ := newTestStore(t)
	boom := errors.New("boom")
	err := st.Tx(ctx, func(ctx context.Context) error {
		if err := st.Accounts().Create(ctx, core.Account{ID: "acc_a", Name: "Дом", CreatedAt: epoch}); err != nil {
			return err
		}
		if err := st.Plugins().Create(ctx, Plugin{ID: "tv", Source: PluginLocal, Command: []string{"/x"}}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Tx = %v", err)
	}
	if _, err := st.Accounts().Get(ctx, "acc_a"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("account after rollback: %v", err)
	}
	if _, err := st.Plugins().Get(ctx, "tv"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("plugin after rollback: %v", err)
	}
}

func TestLinkTTL(t *testing.T) {
	ctx := context.Background()
	st, _ := newTestStore(t)
	if err := st.Accounts().Create(ctx, core.Account{ID: "acc_a", Name: "Дом", CreatedAt: epoch}); err != nil {
		t.Fatal(err)
	}
	invite := func(ctx context.Context, kind core.InviteKind, hash string) core.Invite {
		t.Helper()
		inv := core.Invite{ID: "inv_" + hash, TokenHash: []byte(hash), Kind: kind, CreatedAt: epoch, ExpiresAt: epoch.Add(15 * time.Minute)}
		if kind == core.InviteMember {
			inv.AccountID, inv.CreatedBy = "acc_a", "per_alice"
		}
		if err := st.Invites().Create(ctx, inv); err != nil {
			t.Fatal(err)
		}
		got, err := st.Invites().Peek(ctx, []byte(hash), epoch)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	short := WithLinkTTL(ctx, 3*time.Minute)
	if got := invite(short, core.InviteSetup, "a"); !got.ExpiresAt.Equal(epoch.Add(3 * time.Minute)) {
		t.Errorf("setup link under WithLinkTTL ends %v", got.ExpiresAt)
	}
	if got := invite(ctx, core.InviteSetup, "b"); !got.ExpiresAt.Equal(epoch.Add(15 * time.Minute)) {
		t.Errorf("setup link without it ends %v", got.ExpiresAt)
	}
	if got := invite(short, core.InviteMember, "c"); !got.ExpiresAt.Equal(epoch.Add(15 * time.Minute)) {
		t.Errorf("a member invite was clipped: %v", got.ExpiresAt)
	}
	if got := invite(WithLinkTTL(ctx, time.Hour), core.InviteSetup, "d"); !got.ExpiresAt.Equal(epoch.Add(15 * time.Minute)) {
		t.Errorf("a longer TTL extended the link: %v", got.ExpiresAt)
	}
}

// Every audit entry with an actor carries the actor's name, whichever package writes it.
func TestAuditNamesTheActor(t *testing.T) {
	ctx := context.Background()
	st, _ := newTestStore(t)
	if err := st.Accounts().Create(ctx, core.Account{ID: "acc_a", Name: "Дом", CreatedAt: epoch}); err != nil {
		t.Fatal(err)
	}
	if err := st.People().Create(ctx, core.Person{ID: "per_alice", AccountID: "acc_a", Name: "Alice", Role: core.RoleOwner,
		CreatedAt: epoch}); err != nil {
		t.Fatal(err)
	}
	if err := st.Audit().Append(ctx, core.AuditEntry{At: epoch, AccountID: "acc_a", Actor: "per_alice",
		Action: "plugin.install", Target: "tv", Detail: map[string]string{"via": "admin"}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Audit().ListByAccount(ctx, "acc_a", "", 10)
	if err != nil || len(got) != 1 || got[0].Detail["actor_name"] != "Alice" || got[0].Detail["via"] != "admin" {
		t.Fatalf("entries = %+v, %v", got, err)
	}
}
