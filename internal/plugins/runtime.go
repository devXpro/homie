package plugins

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/devXpro/homie/internal/hub"
)

// Run-time changes of the plugin set: the admin installs, removes, disables and restarts plugins while homed runs.
// Every change waits until the old process has stopped, so one plugin id never has two processes.

// ErrUnknownPlugin means no plugin with that id is in the host.
var ErrUnknownPlugin = errors.New("unknown plugin")

var errClosed = errors.New("the plugin host is closed")

// Add adds a plugin and, when the host has started, starts it. It fails for an invalid spec or an id in use.
func (h *Host) Add(s Spec) error {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	p, err := h.newPlugin(s)
	if err != nil {
		return err
	}
	return h.add(p)
}

func (h *Host) add(p *plugin) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errClosed
	}
	if h.findLocked(p.id) != nil {
		return fmt.Errorf("plugin id %q is used twice", p.id)
	}
	h.plugins = append(h.plugins, p)
	sortPlugins(h.plugins)
	h.fireStatusLocked()
	if h.cancel != nil {
		h.superviseLocked(p)
	}
	return nil
}

// Remove stops a plugin, waits until its process has exited and forgets it with its tools. Its data dir and log file
// stay. ErrUnknownPlugin when no such plugin is in the host.
func (h *Host) Remove(id string) error {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	h.mu.Lock()
	p := h.findLocked(id)
	h.mu.Unlock()
	if p == nil {
		return fmt.Errorf("%w: %s", ErrUnknownPlugin, id)
	}
	h.remove(p)
	return nil
}

func (h *Host) remove(p *plugin) {
	h.mu.Lock()
	h.plugins = slices.DeleteFunc(h.plugins, func(q *plugin) bool { return q == p })
	if len(p.tools) > 0 || p.instructions != "" {
		h.fireChangedLocked()
	}
	h.fireStatusLocked()
	h.mu.Unlock()
	h.halt(p)
	p.logs.close()
}

// Restart stops a plugin and starts it again. Its last known tools stay listed meanwhile, as after a crash.
// ErrUnknownPlugin when no such plugin is in the host.
func (h *Host) Restart(id string) error {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	h.mu.Lock()
	p := h.findLocked(id)
	h.mu.Unlock()
	if p == nil {
		return fmt.Errorf("%w: %s", ErrUnknownPlugin, id)
	}
	h.restart(p, nil)
	return nil
}

// restart stops p's supervisor and starts a new one, with command when it is not nil.
func (h *Host) restart(p *plugin, command []string) {
	h.halt(p)
	h.mu.Lock()
	defer h.mu.Unlock()
	if command != nil {
		p.command = command // no supervisor runs now, so nothing reads it
	}
	if h.cancel != nil && !h.closed && slices.Contains(h.plugins, p) {
		h.superviseLocked(p)
	}
}

// halt stops p's supervisor, if one runs, and waits until it has returned (the process has exited).
func (h *Host) halt(p *plugin) {
	h.mu.Lock()
	stop, done := p.stop, p.done
	p.stop, p.done = nil, nil
	h.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

// Sync makes the host's plugins match specs: plugins missing from specs are stopped and forgotten, new ones are added
// (and started once the host has), and those whose command changed are restarted with the new command. Plugins that
// did not change keep running. Invalid specs fail the whole call before anything changes.
func (h *Host) Sync(specs []Spec) error {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	want := make(map[string]*plugin, len(specs))
	for _, s := range specs {
		p, err := h.newPlugin(s)
		if err != nil {
			return err
		}
		if want[s.ID] != nil {
			return fmt.Errorf("plugin id %q is used twice", s.ID)
		}
		want[s.ID] = p
	}
	h.mu.Lock()
	current := slices.Clone(h.plugins)
	h.mu.Unlock()
	for _, p := range current {
		next, ok := want[p.id]
		switch {
		case !ok:
			h.remove(p)
		case !slices.Equal(next.command, p.command):
			h.restart(p, next.command)
		}
		delete(want, p.id)
	}
	added := make([]*plugin, 0, len(want))
	for _, p := range want {
		added = append(added, p)
	}
	sortPlugins(added)
	var errs []error
	for _, p := range added {
		errs = append(errs, h.add(p))
	}
	return errors.Join(errs...)
}

// Await waits until the plugin is up and returns nil. It returns an error as soon as a run since the plugin's last
// (re)start failed, when the plugin is not in the host (ErrUnknownPlugin), or when ctx ends. The installer uses it to
// tell whether a new version came up.
func (h *Host) Await(ctx context.Context, id string) error {
	for {
		h.mu.Lock()
		p := h.findLocked(id)
		var up, failed bool
		var lastErr string
		if p != nil {
			up, failed, lastErr = p.state == hub.PluginUp, p.failed, p.lastErr
		}
		next := h.statusCh
		h.mu.Unlock()
		switch {
		case p == nil:
			return fmt.Errorf("%w: %s", ErrUnknownPlugin, id)
		case up:
			return nil
		case failed:
			return fmt.Errorf("plugin %s did not start: %s", id, lastErr)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("plugin %s did not start: %w", id, ctx.Err())
		case <-next:
		}
	}
}

// PluginInstructions returns the instructions the plugin sent at initialize; "" for an unknown id. The admin shows
// them on the plugin's page.
func (h *Host) PluginInstructions(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p := h.findLocked(id); p != nil {
		return p.instructions
	}
	return ""
}
