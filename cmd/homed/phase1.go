package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/devXpro/homie/internal/config"
	"github.com/devXpro/homie/internal/store"
)

// openStore opens <data_dir>/homed.db (0600). A phase 1 homed.yaml still holds people with their key hashes, plugins
// and the home name: they are imported once (the first person becomes the owner) and then moved out of homed.yaml
// into homed.yaml.phase1, so the database is the only place they live from now on.
func openStore(ctx context.Context, dir string, cfg *config.Config, log *slog.Logger) (*store.DB, error) {
	st, err := store.Open(ctx, filepath.Join(dir, config.DBFile))
	if err != nil {
		return nil, err
	}
	if !cfg.HasPhase1() {
		return st, nil
	}
	res, err := st.ImportPhase1(ctx, phase1Import(cfg))
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("import %s into %s: %w", config.FileName, config.DBFile, err)
	}
	backup, err := config.ArchivePhase1(dir)
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("move the imported sections out of %s: %w", config.FileName, err)
	}
	if res.Done {
		log.Info("imported people, keys, plugins and the home name from homed.yaml into homed.db",
			"people", res.People, "keys", res.Keys, "plugins", res.Plugins, "home_name", res.HomeName, "backup", backup)
	} else {
		// The import runs once per database; anything added to homed.yaml later is not imported.
		log.Warn("homed.yaml had people, plugins or home_name again; they were NOT imported (homed.db already has the "+
			"import) and moved to the backup. Make such changes in the admin or with the homed CLI.", "backup", backup)
	}
	return st, nil
}

// phase1Import turns the phase 1 sections of homed.yaml into the store's import.
func phase1Import(cfg *config.Config) store.Phase1Import {
	in := store.Phase1Import{HomeName: cfg.HomeName}
	for _, p := range cfg.People {
		person := store.Phase1Person{ID: p.ID, Name: p.Name, Plugins: p.Plugins}
		for _, k := range p.Keys {
			person.Keys = append(person.Keys, store.Phase1Key{SHA256: k.SHA256, Label: k.Label, Created: k.Created})
		}
		in.People = append(in.People, person)
	}
	for _, p := range cfg.Plugins {
		in.Plugins = append(in.Plugins, store.Phase1Plugin{ID: p.ID, Command: p.Command})
	}
	return in
}
