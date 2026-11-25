package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// HomeSettings is the one row of home settings.
type HomeSettings struct {
	// Language is the home's default UI language, "ru" or "en".
	Language string
	// HomeName pre-fills the first setup: imported from v0.1 or kept from before the account existed. Once the account
	// exists, its name (core.Account.Name) is the home's name and this one is not read.
	HomeName string
	// AccessResetAt is when `homed admin reset-access` last ran (review GK-5: an alert the owner sees); zero: never.
	AccessResetAt time.Time
	// AccessResetBy names the program that asked on the admin socket, e.g. "/usr/bin/x (pid 4242)"; "" when unknown.
	AccessResetBy string
	UpdatedAt     time.Time
}

// DefaultLanguage is the language of a home that never chose one.
const DefaultLanguage = "ru"

// Settings stores the HomeSettings row.
type Settings interface {
	// Get returns the settings; before the first Put it returns HomeSettings{Language: DefaultLanguage}.
	Get(ctx context.Context) (HomeSettings, error)
	// Put replaces the settings and sets UpdatedAt.
	Put(ctx context.Context, hs HomeSettings) error
	// Update reads the settings, lets fn change them and stores them, in one transaction.
	Update(ctx context.Context, fn func(*HomeSettings) error) error
}

type settings struct{ s *Store }

func (r settings) Get(ctx context.Context) (HomeSettings, error) {
	var (
		hs               HomeSettings
		reset, updatedAt int64
	)
	err := r.s.Conn(ctx).QueryRowContext(ctx, `SELECT language, home_name, access_reset_at, access_reset_by, updated_at
		FROM settings WHERE id = 1`).Scan(&hs.Language, &hs.HomeName, &reset, &hs.AccessResetBy, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return HomeSettings{Language: DefaultLanguage}, nil
	}
	if err != nil {
		return HomeSettings{}, mapErr(err)
	}
	hs.AccessResetAt, hs.UpdatedAt = fromMS(reset), fromMS(updatedAt)
	return hs, nil
}

func (r settings) Put(ctx context.Context, hs HomeSettings) error {
	if hs.Language == "" {
		hs.Language = DefaultLanguage
	}
	_, err := r.s.exec(ctx, `INSERT INTO settings (id, language, home_name, access_reset_at, access_reset_by, updated_at)
		VALUES (1, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET language = excluded.language, home_name = excluded.home_name,
			access_reset_at = excluded.access_reset_at, access_reset_by = excluded.access_reset_by,
			updated_at = excluded.updated_at`,
		hs.Language, hs.HomeName, ms(hs.AccessResetAt), hs.AccessResetBy, ms(r.s.now()))
	return err
}

func (r settings) Update(ctx context.Context, fn func(*HomeSettings) error) error {
	return r.s.Tx(ctx, func(ctx context.Context) error {
		hs, err := r.Get(ctx)
		if err != nil {
			return err
		}
		if err := fn(&hs); err != nil {
			return err
		}
		return r.Put(ctx, hs)
	})
}
