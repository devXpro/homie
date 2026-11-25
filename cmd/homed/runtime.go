package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/devXpro/homie/internal/admin"
	"github.com/devXpro/homie/internal/plugins"
	"github.com/devXpro/homie/internal/store"
)

// pluginRuntime keeps the running plugins equal to store.Plugins (admin.PluginRuntime). Before the host has started
// (the CLI's in-process admin) Apply only updates the host's list.
type pluginRuntime struct {
	st   *store.Store
	host *plugins.Host
	mu   sync.Mutex // one reconciliation at a time, so an older read of the store never wins
}

var _ admin.PluginRuntime = (*pluginRuntime)(nil)

// enabledSpecs returns the enabled plugins of the store, as the host runs them.
func enabledSpecs(ctx context.Context, st *store.Store) ([]plugins.Spec, error) {
	rows, err := st.Plugins().List(ctx)
	if err != nil {
		return nil, err
	}
	var specs []plugins.Spec
	for _, p := range rows {
		if p.Enabled {
			specs = append(specs, plugins.Spec{ID: p.ID, Command: p.Command})
		}
	}
	return specs, nil
}

// Apply starts new and enabled plugins, stops disabled and removed ones, and restarts those whose command changed.
func (r *pluginRuntime) Apply(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	specs, err := enabledSpecs(ctx, r.st)
	if err != nil {
		return err
	}
	return r.host.Sync(specs)
}

// Restart stops and starts one plugin; a disabled plugin stays stopped.
func (r *pluginRuntime) Restart(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, err := r.st.Plugins().Get(ctx, id)
	if err != nil {
		return fmt.Errorf("plugin %s: %w", id, err)
	}
	if !row.Enabled {
		return nil
	}
	specs, err := enabledSpecs(ctx, r.st)
	if err != nil {
		return err
	}
	if err := r.host.Sync(specs); err != nil {
		return err
	}
	return r.host.Restart(id)
}
