package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/devXpro/mcp-gatekeeper/core"
)

// writeV01 makes a v0.1.1 homed.db at path: the schema of testdata/homed-v0.1.1.sql (internal/store's 0001_init.sql of
// v0.1.1) at schema version 1, with neutral data in every table the import reads or must leave alone.
func writeV01(t *testing.T, path string, withSettings bool) {
	t.Helper()
	schema, err := os.ReadFile(filepath.Join("testdata", "homed-v0.1.1.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ms := epoch.Add(-30 * 24 * time.Hour).UnixMilli()
	stmts := []string{
		string(schema),
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL) STRICT`,
		fmt.Sprintf(`INSERT INTO schema_migrations VALUES (1, '0001_init.sql', %d)`, ms),
		fmt.Sprintf(`INSERT INTO people VALUES ('alice', 'Alice', 'owner', '["*"]', 'en', X'0102', %d, %d)`, ms, ms),
		fmt.Sprintf(`INSERT INTO people VALUES ('bella', 'Bella', 'member', '["tv"]', '', X'0304', %d, %d)`, ms, ms),
		fmt.Sprintf(`INSERT INTO passkeys VALUES ('cred-a', 'alice', 'Laptop', '', X'00', %d, 0)`, ms),
		fmt.Sprintf(`INSERT INTO person_keys VALUES ('k_a', 'bella', '%064x', 'Site', 'ghk_AbCd', '', %d, 0)`, 1, ms),
		fmt.Sprintf(`INSERT INTO plugins VALUES ('tv', 'github', 'devXpro/homie-tv', '0.2.0', '0.1.0',
			'["/home/user/.local/share/homie/plugins/tv/bin/0.2.0/homie-tv","serve"]', 1, %d, %d)`, ms, ms+1000),
		fmt.Sprintf(`INSERT INTO plugins VALUES ('lamp', 'local', '', '', '', '["/home/user/bin/lamp"]', 0, %d, %d)`, ms, ms),
		fmt.Sprintf(`INSERT INTO audit (at, kind, person_id, person, via, tool, plugin_id, ok) VALUES
			(%d, 'tool', 'alice', 'Alice', 'mcp', 'tv_power_on', 'tv', 1)`, ms),
	}
	if withSettings {
		stmts = append(stmts, fmt.Sprintf(`INSERT INTO settings VALUES (1, 'Дом', 'en', 'site', 0, %d)`, ms))
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("v0.1 fixture: %v\n%s", err, s)
		}
	}
}

func newImportFixture(t *testing.T, withSettings bool) (st *Store, oldPath string) {
	t.Helper()
	st, _ = newTestStore(t)
	oldPath = filepath.Join(t.TempDir(), "homed.db")
	writeV01(t, oldPath, withSettings)
	return st, oldPath
}

func TestImportV01(t *testing.T) {
	ctx := context.Background()
	st, oldPath := newImportFixture(t, true)

	res, err := st.ImportV01(ctx, oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Imported || res.Plugins != 2 || !res.Language || !res.HomeName || res.Renamed != oldPath+V01Suffix {
		t.Errorf("result = %+v", res)
	}
	tv, err := st.Plugins().Get(ctx, "tv")
	if err != nil || tv.Source != PluginGitHub || tv.Repo != "devXpro/homie-tv" || tv.Version != "0.2.0" ||
		tv.PreviousVersion != "0.1.0" || !tv.Enabled || len(tv.Command) != 2 || tv.Command[1] != "serve" ||
		!tv.UpdatedAt.Equal(tv.InstalledAt.Add(time.Second)) {
		t.Errorf("tv = %+v, %v", tv, err)
	}
	if lamp, err := st.Plugins().Get(ctx, "lamp"); err != nil || lamp.Enabled || lamp.Source != PluginLocal {
		t.Errorf("lamp = %+v, %v", lamp, err)
	}
	if hs, _ := st.Settings().Get(ctx); hs.Language != "en" || hs.HomeName != "Дом" {
		t.Errorf("settings = %+v", hs)
	}
	// Nobody moves: people re-join by the setup link and invites.
	if accounts, _ := st.Accounts().List(ctx); len(accounts) != 0 {
		t.Errorf("accounts = %+v", accounts)
	}
	if n, _ := st.Activity().Query(ctx, ActivityFilter{}); len(n) != 0 {
		t.Errorf("the old tool audit was imported: %+v", n)
	}
	// The old file is aside, private, and still a readable database.
	if _, err := os.Stat(oldPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("homed.db is still there: %v", err)
	}
	fi, err := os.Stat(res.Renamed)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup: %v, %v", fi, err)
	}
	if old, err := readV01(ctx, res.Renamed); err != nil || len(old.plugins) != 2 {
		t.Errorf("the backup does not read: %+v, %v", old, err)
	}

	// Idempotent: the next start finds nothing to do, and keeps what the owner changed since.
	if err := st.Plugins().Delete(ctx, "lamp"); err != nil {
		t.Fatal(err)
	}
	again, err := st.ImportV01(ctx, oldPath)
	if err != nil || again != (ImportResult{}) {
		t.Errorf("second import = %+v, %v", again, err)
	}
	if _, err := st.Plugins().Get(ctx, "lamp"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("a second import brought lamp back: %v", err)
	}
}

func TestImportV01NoOldDatabase(t *testing.T) {
	st, _ := newTestStore(t)
	res, err := st.ImportV01(context.Background(), filepath.Join(t.TempDir(), "homed.db"))
	if err != nil || res != (ImportResult{}) {
		t.Errorf("= %+v, %v", res, err)
	}
}

func TestImportV01WithoutSettingsRow(t *testing.T) {
	ctx := context.Background()
	st, oldPath := newImportFixture(t, false)
	res, err := st.ImportV01(ctx, oldPath)
	if err != nil || !res.Imported || res.Language || res.HomeName || res.Plugins != 2 {
		t.Fatalf("= %+v, %v", res, err)
	}
	if hs, _ := st.Settings().Get(ctx); hs != (HomeSettings{Language: DefaultLanguage}) {
		t.Errorf("settings = %+v", hs)
	}
}

// An account that exists already names the home; the old name is not kept, and plugins with a taken id stay as they
// are.
func TestImportV01IntoExistingHome(t *testing.T) {
	ctx := context.Background()
	st, oldPath := newImportFixture(t, true)
	if err := st.Accounts().Create(ctx, core.Account{ID: "acc_a", Name: "Home", CreatedAt: epoch}); err != nil {
		t.Fatal(err)
	}
	if err := st.Plugins().Create(ctx, Plugin{ID: "tv", Source: PluginLocal, Command: []string{"/home/user/tv"}}); err != nil {
		t.Fatal(err)
	}
	res, err := st.ImportV01(ctx, oldPath)
	if err != nil || res.Plugins != 1 || res.HomeName || !res.Language {
		t.Fatalf("= %+v, %v", res, err)
	}
	if tv, _ := st.Plugins().Get(ctx, "tv"); tv.Source != PluginLocal {
		t.Errorf("tv was replaced: %+v", tv)
	}
	if hs, _ := st.Settings().Get(ctx); hs.HomeName != "" {
		t.Errorf("home name pre-fill = %q", hs.HomeName)
	}
	if acc, _ := st.Accounts().Get(ctx, "acc_a"); acc.Name != "Home" {
		t.Errorf("account renamed to %q", acc.Name)
	}
}

// A failure inside the import's transaction (here: at the very mark) leaves nothing behind and the old file where it
// was, so the next start imports everything.
func TestImportV01FailureInsideTheTransaction(t *testing.T) {
	ctx := context.Background()
	st, oldPath := newImportFixture(t, true)
	trigger := `CREATE TRIGGER crash BEFORE INSERT ON imports BEGIN SELECT RAISE(ABORT, 'crash'); END`
	if _, err := st.Conn(ctx).ExecContext(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ImportV01(ctx, oldPath); err == nil {
		t.Fatal("the import did not fail")
	}
	if list, _ := st.Plugins().List(ctx); len(list) != 0 {
		t.Errorf("plugins after the failed import: %+v", list)
	}
	if hs, _ := st.Settings().Get(ctx); hs.HomeName != "" {
		t.Errorf("settings after the failed import: %+v", hs)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("homed.db moved: %v", err)
	}
	if _, err := st.Conn(ctx).ExecContext(ctx, `DROP TRIGGER crash`); err != nil {
		t.Fatal(err)
	}
	res, err := st.ImportV01(ctx, oldPath)
	if err != nil || !res.Imported || res.Plugins != 2 || res.Renamed == "" {
		t.Errorf("restart = %+v, %v", res, err)
	}
}

// A crash after the commit and before the rename: the next start only moves the file.
func TestImportV01CrashBeforeTheRename(t *testing.T) {
	ctx := context.Background()
	st, oldPath := newImportFixture(t, true)
	crash := errors.New("crash")
	rename = func(string, string) error { return crash }
	res, err := st.ImportV01(ctx, oldPath)
	rename = os.Rename
	if !errors.Is(err, crash) || !res.Imported || res.Plugins != 2 {
		t.Fatalf("= %+v, %v", res, err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("homed.db moved: %v", err)
	}
	if err := st.Plugins().Delete(ctx, "tv"); err != nil { // the owner removed it meanwhile
		t.Fatal(err)
	}
	res, err = st.ImportV01(ctx, oldPath)
	if err != nil || res.Imported || res.Plugins != 0 || res.Renamed != oldPath+V01Suffix {
		t.Fatalf("restart = %+v, %v", res, err)
	}
	if _, err := st.Plugins().Get(ctx, "tv"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("the restart imported again: %v", err)
	}
}

// A backup that exists already is never overwritten, and the -wal and -shm files move with the database.
func TestImportV01KeepsAnEarlierBackup(t *testing.T) {
	ctx := context.Background()
	st, oldPath := newImportFixture(t, true)
	if err := os.WriteFile(oldPath+V01Suffix, []byte("earlier"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath+"-shm", []byte("shm"), 0o600); err != nil { // as a crashed v0.1 homed leaves it
		t.Fatal(err)
	}
	res, err := st.ImportV01(ctx, oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if res.Renamed == oldPath+V01Suffix || filepath.Dir(res.Renamed) != filepath.Dir(oldPath) {
		t.Errorf("renamed to %s", res.Renamed)
	}
	if b, _ := os.ReadFile(oldPath + V01Suffix); string(b) != "earlier" { //nolint:gosec // G304: the test's temp dir
		t.Error("the earlier backup was overwritten")
	}
	if _, err := os.Stat(oldPath + "-shm"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("-shm left behind: %v", err)
	}
}

func TestImportV01RefusesAnotherDatabase(t *testing.T) {
	ctx := context.Background()
	st, _ := newTestStore(t)
	oldPath := filepath.Join(t.TempDir(), "homed.db")
	if err := os.WriteFile(oldPath, []byte("not a database at all, just text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ImportV01(ctx, oldPath); err == nil {
		t.Fatal("a file that is no database was accepted")
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Errorf("the refused file moved: %v", err)
	}
}
