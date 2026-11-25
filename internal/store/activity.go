package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// ActivityRecord is one tool call, as stored.
type ActivityRecord struct {
	ID       int64 // assigned by Append, strictly increasing; the page cursor
	At       time.Time
	PersonID string          // the gatekeeper person id; empty for the CLI
	Person   string          // the display name at the time; kept after the person is gone
	Via      string          // "chatgpt", "claude", "admin" or "cli"
	Tool     string          // e.g. "tv_power_on"
	PluginID string          // the tool's plugin
	Args     json.RawMessage // redacted arguments (audit.Redact); may be nil
	OK       bool
	Error    string // why it failed, human English; never secrets
	Duration time.Duration
}

// ActivityStatus filters by outcome.
type ActivityStatus string

const (
	// ActivityAny matches every record.
	ActivityAny ActivityStatus = ""
	// ActivityOK matches successes only.
	ActivityOK ActivityStatus = "ok"
	// ActivityFailed matches failures only.
	ActivityFailed ActivityStatus = "failed"
)

// ActivityFilter selects records. Zero fields match everything.
type ActivityFilter struct {
	PersonID string
	PluginID string
	Tool     string
	Via      []string // any of these; empty matches all
	Status   ActivityStatus
	Since    time.Time // At >= Since
	Until    time.Time // At < Until
	BeforeID int64     // ID < BeforeID: the cursor of the next page; 0 starts at the newest
	Limit    int       // page size; capped at 200, 50 when 0
}

// Activity stores the tool-call log. The hub's writer (internal/audit) batches into Append, so a call never waits
// for the disk.
type Activity interface {
	// Append stores records in order, in one transaction, and returns them with ID set; a zero At is now.
	Append(ctx context.Context, recs []ActivityRecord) ([]ActivityRecord, error)
	// Query returns matching records, newest (highest ID) first. When it returns Limit records, the next page is the
	// same filter with BeforeID set to the last record's ID.
	Query(ctx context.Context, f ActivityFilter) ([]ActivityRecord, error)
	// Get returns one record by id.
	Get(ctx context.Context, id int64) (ActivityRecord, error)
	// Prune deletes records older than before and returns how many it deleted.
	Prune(ctx context.Context, before time.Time) (int64, error)
}

type activity struct{ s *Store }

const (
	activityDefaultLimit = 50
	activityMaxLimit     = 200
	activityCols         = `id, at, person_id, person, via, tool, plugin_id, args, ok, error, duration_ns`
)

func scanActivity(r scanner) (ActivityRecord, error) {
	var (
		a       ActivityRecord
		args    sql.NullString
		at, dur int64
		okFlag  int
	)
	if err := r.Scan(&a.ID, &at, &a.PersonID, &a.Person, &a.Via, &a.Tool, &a.PluginID, &args, &okFlag, &a.Error,
		&dur); err != nil {
		return ActivityRecord{}, err
	}
	a.At, a.OK, a.Duration = fromMS(at), okFlag != 0, time.Duration(dur)
	if args.Valid {
		a.Args = json.RawMessage(args.String)
	}
	return a, nil
}

func (r activity) Append(ctx context.Context, recs []ActivityRecord) ([]ActivityRecord, error) {
	if len(recs) == 0 {
		return []ActivityRecord{}, nil
	}
	out := make([]ActivityRecord, len(recs))
	err := r.s.Tx(ctx, func(ctx context.Context) error {
		q := r.s.Conn(ctx)
		for i, rec := range recs {
			rec.At = r.s.stamp(rec.At).UTC().Truncate(time.Millisecond)
			var args any
			if rec.Args != nil {
				args = string(rec.Args)
			}
			res, err := q.ExecContext(ctx, `INSERT INTO activity (at, person_id, person, via, tool, plugin_id, args, ok,
				error, duration_ns) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				ms(rec.At), rec.PersonID, rec.Person, rec.Via, rec.Tool, rec.PluginID, args, boolInt(rec.OK), rec.Error,
				int64(rec.Duration))
			if err != nil {
				return mapErr(err)
			}
			if rec.ID, err = res.LastInsertId(); err != nil {
				return mapErr(err)
			}
			out[i] = rec
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r activity) Query(ctx context.Context, f ActivityFilter) ([]ActivityRecord, error) {
	var (
		where []string
		args  []any
	)
	add := func(cond string, a ...any) {
		where = append(where, cond)
		args = append(args, a...)
	}
	if f.PersonID != "" {
		add(`person_id = ?`, f.PersonID)
	}
	if f.PluginID != "" {
		add(`plugin_id = ?`, f.PluginID)
	}
	if f.Tool != "" {
		add(`tool = ?`, f.Tool)
	}
	if len(f.Via) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(f.Via)), ",")
		vias := make([]any, len(f.Via))
		for i, v := range f.Via {
			vias[i] = v
		}
		add(`via IN (`+marks+`)`, vias...)
	}
	switch f.Status {
	case ActivityOK:
		add(`ok = 1`)
	case ActivityFailed:
		add(`ok = 0`)
	}
	if !f.Since.IsZero() {
		add(`at >= ?`, ms(f.Since))
	}
	if !f.Until.IsZero() {
		add(`at < ?`, ms(f.Until))
	}
	if f.BeforeID > 0 {
		add(`id < ?`, f.BeforeID)
	}
	limit := f.Limit
	switch {
	case limit <= 0:
		limit = activityDefaultLimit
	case limit > activityMaxLimit:
		limit = activityMaxLimit
	}
	q := `SELECT ` + activityCols + ` FROM activity`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	return collect(ctx, r.s, scanActivity, q, args...)
}

func (r activity) Get(ctx context.Context, id int64) (ActivityRecord, error) {
	return one(ctx, r.s, scanActivity, `SELECT `+activityCols+` FROM activity WHERE id = ?`, id)
}

func (r activity) Prune(ctx context.Context, before time.Time) (int64, error) {
	if before.IsZero() {
		return 0, nil
	}
	return r.s.exec(ctx, `DELETE FROM activity WHERE at < ?`, ms(before))
}
