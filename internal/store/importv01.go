package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"time"
)

// V01Suffix is added to the v0.1 database once ImportV01 copied it: homed.db becomes homed.db.v0.1, kept (0600) so
// the old tool history stays readable.
const V01Suffix = ".v0.1"

// importV01 names the v0.1 import in the imports table.
const importV01 = "v0.1"

// ImportResult says what ImportV01 did.
type ImportResult struct {
	// Imported is true when this call copied the data; false when it ran before or there was no old database.
	Imported bool
	// Plugins is how many plugins this call copied; a plugin whose id exists already is left alone.
	Plugins int
	// Language is true when the home's language was copied.
	Language bool
	// HomeName is true when the old home name was kept to pre-fill the first setup (only while no account exists).
	HomeName bool
	// Renamed is where this call moved the old database; "" when it moved nothing.
	Renamed string
}

// rename is os.Rename; tests replace it to stop the import between its steps, as a crash would.
var rename = os.Rename

// ImportV01 copies what can move from the v0.1 database at oldPath (<data dir>/homed.db) into s:
//   - the plugins, rows as they are: their commands point into the data dir (or at a local binary) and keep working,
//     and the plugins' own data (a TV's pairing) is outside the database anyway;
//   - the language;
//   - the home name, which pre-fills the first setup and then becomes the account's name; when the account exists
//     already, its name is the home's and the old one is dropped.
//
// People, passkeys, sessions, keys, connections and the old audit do not move. The mark of the import is written in
// the same transaction as the data, and the old file is renamed to oldPath+V01Suffix only after that transaction
// committed: a crash at any point leaves a state the next call finishes, and a call after the import is a no-op. homed
// calls it at every start.
func (s *Store) ImportV01(ctx context.Context, oldPath string) (ImportResult, error) {
	var res ImportResult
	if _, err := os.Lstat(oldPath); errors.Is(err, fs.ErrNotExist) {
		return res, nil
	} else if err != nil {
		return res, fmt.Errorf("store: the v0.1 database: %w", err)
	}
	done, err := s.imported(ctx, importV01)
	if err != nil {
		return res, err
	}
	if !done {
		old, err := readV01(ctx, oldPath)
		if err != nil {
			return res, fmt.Errorf("store: read the v0.1 database %s: %w", oldPath, err)
		}
		if res, err = s.applyV01(ctx, old); err != nil {
			return ImportResult{}, err
		}
	}
	dst, err := moveAside(oldPath, oldPath+V01Suffix, s.now())
	if err != nil {
		return res, fmt.Errorf("store: the v0.1 database was imported, but moving it aside failed (the next start "+
			"tries again): %w", err)
	}
	res.Renamed = dst
	return res, nil
}

// imported reports whether the import name ran.
func (s *Store) imported(ctx context.Context, name string) (bool, error) {
	var n int
	if err := s.Conn(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM imports WHERE name = ?`, name).Scan(&n); err != nil {
		return false, mapErr(err)
	}
	return n > 0, nil
}

// v01Data is what ImportV01 takes from a v0.1 database.
type v01Data struct {
	plugins     []v01Plugin
	hasSettings bool
	language    string
	homeName    string
}

// v01Plugin is a row of the v0.1 plugins table, its command still the stored JSON text.
type v01Plugin struct {
	id, source, repo, version, previous, command string
	enabled                                      int
	installed, updated                           int64
}

// readV01 reads the plugins and settings of a v0.1 database, and closes it (a clean close checkpoints its WAL, so the
// file can be renamed alone).
func readV01(ctx context.Context, path string) (v01Data, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String())
	if err != nil {
		return v01Data{}, err
	}
	defer func() { _ = db.Close() }()
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return v01Data{}, fmt.Errorf("not a homed v0.1 database: %w", err)
	}
	if version != 1 {
		return v01Data{}, fmt.Errorf("its schema version is %d; this homed imports version 1 (v0.1.x)", version)
	}
	var out v01Data
	rows, err := db.QueryContext(ctx, `SELECT id, source, repo, version, previous_version, command, enabled, installed_at,
		updated_at FROM plugins ORDER BY id`)
	if err != nil {
		return v01Data{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var p v01Plugin
		if err := rows.Scan(&p.id, &p.source, &p.repo, &p.version, &p.previous, &p.command, &p.enabled, &p.installed,
			&p.updated); err != nil {
			return v01Data{}, err
		}
		var cmd []string
		if err := json.Unmarshal([]byte(p.command), &cmd); err != nil {
			return v01Data{}, fmt.Errorf("plugin %s: bad command column: %w", p.id, err)
		}
		out.plugins = append(out.plugins, p)
	}
	if err := rows.Err(); err != nil {
		return v01Data{}, err
	}
	err = db.QueryRowContext(ctx, `SELECT home_name, language FROM settings WHERE id = 1`).Scan(&out.homeName, &out.language)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return v01Data{}, err
	default:
		out.hasSettings = true
	}
	return out, nil
}

// applyV01 writes old into s, with the mark, in one transaction.
func (s *Store) applyV01(ctx context.Context, old v01Data) (ImportResult, error) {
	var res ImportResult
	err := s.Tx(ctx, func(ctx context.Context) error {
		res = ImportResult{}
		done, err := s.imported(ctx, importV01)
		if err != nil || done {
			return err // another process imported meanwhile
		}
		q := s.Conn(ctx)
		for _, p := range old.plugins {
			r, err := q.ExecContext(ctx, `INSERT INTO plugins (`+pluginCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (id) DO NOTHING`,
				p.id, p.source, p.repo, p.version, p.previous, p.command, p.enabled, p.installed, p.updated)
			if err != nil {
				return mapErr(err)
			}
			if n, _ := r.RowsAffected(); n > 0 {
				res.Plugins++
			}
		}
		if old.hasSettings {
			accounts, err := s.Accounts().List(ctx)
			if err != nil {
				return err
			}
			err = s.Settings().Update(ctx, func(hs *HomeSettings) error {
				if old.language == "ru" || old.language == "en" {
					hs.Language, res.Language = old.language, true
				}
				if old.homeName != "" && len(accounts) == 0 {
					hs.HomeName, res.HomeName = old.homeName, true
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO imports (name, done_at) VALUES (?, ?)`, importV01, ms(s.now())); err != nil {
			return mapErr(err)
		}
		res.Imported = true
		return nil
	})
	if err != nil {
		return ImportResult{}, fmt.Errorf("store: import the v0.1 database: %w", err)
	}
	return res, nil
}

// moveAside renames the database at src (with its -wal and -shm, when they are there) to dst, or, when dst exists
// already, to dst plus the time: an earlier copy is never overwritten. The main file goes last, so while it is still
// at src the next call repeats the move. It returns the new path.
func moveAside(src, dst string, now time.Time) (string, error) {
	if _, err := os.Lstat(dst); err == nil {
		dst += "." + now.UTC().Format("20060102T150405Z")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := rename(src+suffix, dst+suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	if err := rename(src, dst); err != nil {
		return "", err
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return "", err
	}
	return dst, nil
}
