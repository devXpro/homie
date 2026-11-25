package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// PluginSource says where a plugin came from.
type PluginSource string

const (
	// PluginGitHub was installed from a GitHub release.
	PluginGitHub PluginSource = "github"
	// PluginLocal is a command on this machine, added with the CLI (development).
	PluginLocal PluginSource = "local"
)

// Plugin is one installed plugin: what the plugin host needs to run it (plugins.Spec) and where it came from.
type Plugin struct {
	ID              string // ^[a-z][a-z0-9]{1,15}$
	Source          PluginSource
	Repo            string   // "owner/name" for PluginGitHub, e.g. "devXpro/homie-tv"
	Version         string   // installed release, e.g. "0.2.0"; empty for PluginLocal
	PreviousVersion string   // the release kept for rollback; empty when none
	Command         []string // executable (absolute path) and arguments, e.g. [".../bin/0.2.0/homie-tv", "serve"]
	Enabled         bool     // disabled plugins are not started and their tools are not listed
	InstalledAt     time.Time
	UpdatedAt       time.Time
}

// Plugins stores the installed plugins.
type Plugins interface {
	// Get returns one plugin by id.
	Get(ctx context.Context, id string) (Plugin, error)
	// List returns every plugin, by id.
	List(ctx context.Context) ([]Plugin, error)
	// Create stores a newly installed plugin; a taken id wraps core.ErrConflict. Zero times are set to now.
	Create(ctx context.Context, p Plugin) error
	// Update replaces every field but ID and InstalledAt, and sets UpdatedAt.
	Update(ctx context.Context, p Plugin) error
	// Delete removes one plugin's row; its files are the installer's business.
	Delete(ctx context.Context, id string) error
}

type plugins struct{ s *Store }

const pluginCols = `id, source, repo, version, previous_version, command, enabled, installed_at, updated_at`

func scanPlugin(r scanner) (Plugin, error) {
	var (
		p                  Plugin
		source, command    string
		enabled            int
		installed, updated int64
	)
	if err := r.Scan(&p.ID, &source, &p.Repo, &p.Version, &p.PreviousVersion, &command, &enabled, &installed, &updated); err != nil {
		return Plugin{}, err
	}
	p.Source, p.Enabled = PluginSource(source), enabled != 0
	if err := json.Unmarshal([]byte(command), &p.Command); err != nil {
		return Plugin{}, fmt.Errorf("store: plugin %s: bad command column: %w", p.ID, err)
	}
	p.InstalledAt, p.UpdatedAt = fromMS(installed), fromMS(updated)
	return p, nil
}

func commandJSON(cmd []string) string {
	if cmd == nil {
		cmd = []string{}
	}
	b, _ := json.Marshal(cmd) //nolint:errchkjson // a []string always marshals
	return string(b)
}

func (r plugins) Get(ctx context.Context, id string) (Plugin, error) {
	return one(ctx, r.s, scanPlugin, `SELECT `+pluginCols+` FROM plugins WHERE id = ?`, id)
}

func (r plugins) List(ctx context.Context) ([]Plugin, error) {
	return collect(ctx, r.s, scanPlugin, `SELECT `+pluginCols+` FROM plugins ORDER BY id`)
}

func (r plugins) Create(ctx context.Context, p Plugin) error {
	if p.ID == "" {
		return errors.New("store: plugin id is empty")
	}
	_, err := r.s.exec(ctx, `INSERT INTO plugins (`+pluginCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, string(p.Source), p.Repo, p.Version, p.PreviousVersion, commandJSON(p.Command), boolInt(p.Enabled),
		ms(r.s.stamp(p.InstalledAt)), ms(r.s.stamp(p.UpdatedAt)))
	return err
}

func (r plugins) Update(ctx context.Context, p Plugin) error {
	return r.s.execOne(ctx, `UPDATE plugins SET source = ?, repo = ?, version = ?, previous_version = ?, command = ?,
		enabled = ?, updated_at = ? WHERE id = ?`,
		string(p.Source), p.Repo, p.Version, p.PreviousVersion, commandJSON(p.Command), boolInt(p.Enabled),
		ms(r.s.now()), p.ID)
}

func (r plugins) Delete(ctx context.Context, id string) error {
	return r.s.execOne(ctx, `DELETE FROM plugins WHERE id = ?`, id)
}
