// Package store is the hub's database, <data dir>/homie.db: MCP Gatekeeper's tables (accounts, people, passkeys,
// sessions, invites, OAuth, the admin-action audit) through package sqlitestore, and the hub's own tables next to them
// in the same file:
//   - plugins: the installed plugins (what the plugin host runs, where each came from);
//   - settings: the home's language, the home name typed or imported before the account exists, and the last
//     access reset (an alert for the owner, review GK-5);
//   - activity: every tool call, from the assistants, the admin and the CLI, with redacted arguments.
//
// The home's name is the gatekeeper account's name once the account exists (Accounts().Rename); settings keep only the
// name that pre-fills the first setup.
//
// Store embeds *sqlitestore.Store, so it is the gatekeeper's store.Store as well: auth, oauth, people and mcpguard take
// it as it is. The hub's tables join the gatekeeper's transactions (Tx; a write outside a Tx opens one). Every write
// of the hub's repositories runs in a transaction, so it takes the store's in-process write lock.
//
// Conventions of the hub's repositories: times are INTEGER Unix milliseconds in UTC (0 means "never"); a get of
// something missing wraps core.ErrNotFound, a duplicate id wraps core.ErrConflict; returned slices are the caller's.
//
// ImportV01 copies what can move from a v0.1 homed.db (docs: the step-2 plan §3.3): the plugins, the language and the
// home name. Passkeys, sessions, keys and people cannot move (the passkeys' relying party changes with the tunnel).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/devXpro/mcp-gatekeeper/core"
	gkstore "github.com/devXpro/mcp-gatekeeper/store"
	"github.com/devXpro/mcp-gatekeeper/store/sqlitestore"
)

// Options configures Open.
type Options struct {
	// Path is the database file, <data dir>/homie.db. It is created 0600 in a 0700 directory when missing.
	Path string
	// Clock stamps the hub's rows, the audit ids and the migrations; nil is time.Now.
	Clock core.Clock
	// Logger receives the migration log and file-mode warnings; nil discards them.
	Logger *slog.Logger
}

// Store is homie.db: the gatekeeper's store plus the hub's repositories. It is safe for concurrent use.
type Store struct {
	*sqlitestore.Store
	now func() time.Time
}

var _ gkstore.Store = (*Store)(nil)

// Open opens (creating it when missing) the database, applies the gatekeeper's and the hub's migrations and returns
// the store. It refuses a database written by a newer homed.
func Open(ctx context.Context, o Options) (*Store, error) {
	clock := o.Clock
	if clock == nil {
		clock = time.Now
	}
	gk, err := sqlitestore.Open(ctx, sqlitestore.Options{
		Path: o.Path, Migrations: migrations, Clock: clock, Logger: o.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return &Store{Store: gk, now: func() time.Time { return clock().UTC() }}, nil
}

// Plugins returns the installed plugins.
func (s *Store) Plugins() Plugins { return plugins{s} }

// Settings returns the home settings.
func (s *Store) Settings() Settings { return settings{s} }

// Activity returns the tool-call log.
func (s *Store) Activity() Activity { return activity{s} }

// write runs fn in a write transaction: the one ctx carries, or a new one.
func (s *Store) write(ctx context.Context, fn func(ctx context.Context, q sqlitestore.Conn) error) error {
	return s.Tx(ctx, func(ctx context.Context) error { return fn(ctx, s.Conn(ctx)) })
}

// exec runs one write statement and returns how many rows it touched.
func (s *Store) exec(ctx context.Context, query string, args ...any) (int64, error) {
	var n int64
	err := s.write(ctx, func(ctx context.Context, q sqlitestore.Conn) error {
		res, err := q.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, mapErr(err)
}

// execOne runs one write statement that must touch a row; none wraps core.ErrNotFound.
func (s *Store) execOne(ctx context.Context, query string, args ...any) error {
	n, err := s.exec(ctx, query, args...)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("store: %w", core.ErrNotFound)
	}
	return nil
}

// scanner is *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

// collect runs a query and scans every row with scan.
func collect[T any](ctx context.Context, s *Store, scan func(scanner) (T, error), query string, args ...any) ([]T, error) {
	rows, err := s.Conn(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// one runs a query for a single row.
func one[T any](ctx context.Context, s *Store, scan func(scanner) (T, error), query string, args ...any) (T, error) {
	v, err := scan(s.Conn(ctx).QueryRowContext(ctx, query, args...))
	if err != nil {
		var zero T
		return zero, mapErr(err)
	}
	return v, nil
}

// mapErr turns driver errors into the gatekeeper's sentinels, keeping the original in the chain.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("store: %w", core.ErrNotFound)
	case errors.Is(err, core.ErrNotFound), errors.Is(err, core.ErrConflict):
		return err
	}
	if se, ok := errors.AsType[*sqlite.Error](err); ok {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return fmt.Errorf("store: %w (%w)", core.ErrConflict, err)
		}
	}
	return fmt.Errorf("store: %w", err)
}

// ms converts a time to stored Unix milliseconds; the zero time becomes 0.
func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// fromMS is the inverse of ms; 0 becomes the zero time.
func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

// stamp returns t, or now when t is zero.
func (s *Store) stamp(t time.Time) time.Time {
	if t.IsZero() {
		return s.now()
	}
	return t
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
