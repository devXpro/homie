package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devXpro/mcp-gatekeeper/core"

	"github.com/devXpro/homie/internal/admin"
	"github.com/devXpro/homie/internal/install"
	"github.com/devXpro/homie/internal/plugins"
	"github.com/devXpro/homie/internal/store"
)

// awaitStart bounds how long a new plugin version may take to come up (initialize and tools/list).
const awaitStart = 75 * time.Second

// restartDelay lets the hub.update job's last event reach the admin before homed goes down.
const restartDelay = time.Second

// installer adapts internal/install to the admin (admin.Installer): the job steps, the store row and the running
// plugin kept in step with the files, and installer errors mapped to the job error codes.
type installer struct {
	st      *store.Store
	runtime *pluginRuntime
	host    *plugins.Host
	inst    *install.Installer
	hub     *install.HubUpdater
	// restart asks homed to shut down gracefully and exit with install.ExitCodeRestart. Nil in the CLI's in-process
	// admin, where nothing is running to install into or to restart.
	restart func()
	locks   pluginLocks
}

// pluginLocks lets one job at a time change a plugin: an install, an update and a removal of the same id would
// otherwise race over its store row, its running process and its files. The admin refuses a second job for the same
// target at once; an install from a repo learns its id only here, so the lock is taken here for every kind.
type pluginLocks struct {
	mu   sync.Mutex
	busy map[string]bool
}

// lock takes id for the caller, or answers job_running while another job holds it. unlock releases it.
func (l *pluginLocks) lock(id string) (unlock func(), err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.busy[id] {
		return nil, &admin.JobError{Code: "job_running",
			Message: fmt.Sprintf("another install, update or removal of %s is running", id)}
	}
	if l.busy == nil {
		l.busy = map[string]bool{}
	}
	l.busy[id] = true
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.busy, id)
	}, nil
}

var _ admin.Installer = (*installer)(nil)

func newInstaller(dir string, st *store.Store, rt *pluginRuntime, host *plugins.Host, restart func()) *installer {
	cl := install.NewClient(install.TokenFromEnv())
	return &installer{
		st: st, runtime: rt, host: host, restart: restart,
		inst: &install.Installer{Client: cl, DataDir: dir, HubVersion: version},
		hub:  &install.HubUpdater{Client: cl, Version: version},
	}
}

var errNotServing = &admin.JobError{Code: "internal", Message: "homed is not running; start it with: homed run"}

// hooks turns the installer's step and download reports into job reports.
func hooks(report admin.Reporter) *install.Hooks {
	return &install.Hooks{
		Step:     func(step string) { report(admin.JobReport{Step: step}) },
		Progress: func(done, total int64) { report(admin.JobReport{Done: done, Total: total}) },
	}
}

// jobError gives an installer error its job error code (ADMIN-API §2, Job); the message never holds a token.
func jobError(err error) error {
	if err == nil {
		return nil
	}
	return &admin.JobError{Code: install.Code(err), Message: err.Error()}
}

// Preview implements admin.Installer.
func (i *installer) Preview(ctx context.Context, repo, version string) (admin.PluginPreview, error) {
	cand, err := i.inst.Resolve(ctx, repo, version)
	if errors.Is(err, install.ErrNotFound) {
		return admin.PluginPreview{}, admin.ErrNotFound
	}
	if err != nil {
		return admin.PluginPreview{}, jobError(err)
	}
	m, rel := cand.Manifest, cand.Release
	pv := admin.PluginPreview{
		Repo: rel.Repo, ID: m.ID, Title: m.Title, Description: m.Description, Version: rel.Version,
		Homepage: m.Homepage, MinHubVersion: m.MinHubVersion, Compatible: cand.Compatible, AssetFound: cand.AssetFound,
	}
	if !rel.PublishedAt.IsZero() {
		at := rel.PublishedAt
		pv.PublishedAt = &at
	}
	return pv, nil
}

// Install implements admin.Installer: download, verify and unpack the release, then store the plugin and start it.
// When the new plugin does not come up, its row goes again and the installer deletes its files.
func (i *installer) Install(ctx context.Context, repo, version, id string, report admin.Reporter) (string, error) {
	if i.restart == nil {
		return "", errNotServing
	}
	if id == "" {
		// Learn the id before anything is unpacked: a release of an installed plugin must not touch its files.
		cand, err := i.inst.Resolve(ctx, repo, version)
		if err != nil {
			return "", jobError(err)
		}
		id = cand.Manifest.ID
	}
	unlock, err := i.locks.lock(id)
	if err != nil {
		return "", err
	}
	defer unlock()
	if _, err := i.st.Plugins().Get(ctx, id); err == nil {
		return "", &admin.JobError{Code: "conflict", Message: fmt.Sprintf("a plugin with the id %q is installed", id)}
	} else if !errors.Is(err, core.ErrNotFound) {
		return "", err
	}
	h := hooks(report)
	h.Start = func(ctx context.Context, next *install.Installed) error {
		report(admin.JobReport{Message: next.Manifest.Title + " " + next.Version})
		row := store.Plugin{
			ID: next.ID, Source: store.PluginGitHub, Repo: next.Repo, Version: next.Version, Command: next.Command,
			Enabled: true,
		}
		if err := i.st.Plugins().Create(ctx, row); err != nil {
			return err
		}
		if err := i.startAndWait(ctx, next.ID); err != nil {
			bg := context.WithoutCancel(ctx)
			_ = i.st.Plugins().Delete(bg, next.ID)
			_ = i.runtime.Apply(bg)
			return err
		}
		return nil
	}
	next, err := i.inst.Install(ctx, repo, version, id, h)
	if err != nil {
		return "", jobError(err)
	}
	return next.ID, nil
}

// Update implements admin.Installer: the new version is unpacked next to the running one, the store row switched to
// it and the plugin restarted; when it does not come up, the old row and binary come back. An older release is
// refused unless allowDowngrade.
func (i *installer) Update(ctx context.Context, id, version string, allowDowngrade bool, report admin.Reporter) error {
	if i.restart == nil {
		return errNotServing
	}
	unlock, err := i.locks.lock(id)
	if err != nil {
		return err
	}
	defer unlock()
	row, err := i.st.Plugins().Get(ctx, id)
	if err != nil {
		return fmt.Errorf("plugin %s: %w", id, err)
	}
	if row.Source != store.PluginGitHub || row.Repo == "" {
		return &admin.JobError{Code: "internal", Message: "only plugins installed from GitHub can be updated here"}
	}
	h := hooks(report)
	h.Switch = func(ctx context.Context, next *install.Installed) error {
		report(admin.JobReport{Message: next.Manifest.Title + " " + next.Version})
		switched := row
		switched.Version, switched.PreviousVersion, switched.Command = next.Version, row.Version, next.Command
		return i.st.Plugins().Update(ctx, switched)
	}
	h.Start = func(ctx context.Context, _ *install.Installed) error { return i.startAndWait(ctx, id) }
	h.Rollback = func(ctx context.Context, _ *install.Installed) error {
		if err := i.st.Plugins().Update(ctx, row); err != nil {
			return err
		}
		return i.startAndWait(ctx, id)
	}
	_, err = i.inst.Update(ctx, id, row.Repo, row.Version, version, allowDowngrade, h)
	return jobError(err)
}

// startAndWait applies the store to the running plugins and waits until id answers initialize and tools/list.
func (i *installer) startAndWait(ctx context.Context, id string) error {
	if err := i.runtime.Apply(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, awaitStart)
	defer cancel()
	return i.host.Await(ctx, id)
}

// Remove implements admin.Installer: the plugin stops and leaves the store, then its files go (its data too unless
// keepData).
func (i *installer) Remove(ctx context.Context, id string, keepData bool, report admin.Reporter) error {
	unlock, err := i.locks.lock(id)
	if err != nil {
		return err
	}
	defer unlock()
	report(admin.JobReport{Step: "stop"})
	if err := i.st.Plugins().Delete(ctx, id); err != nil && !errors.Is(err, core.ErrNotFound) {
		return err
	}
	if err := i.runtime.Apply(ctx); err != nil {
		return err
	}
	report(admin.JobReport{Step: "remove"})
	if err := i.inst.Remove(id, keepData); err != nil {
		return fmt.Errorf("remove the files of %s: %w", id, err)
	}
	return nil
}

// LatestVersion implements admin.Installer.
func (i *installer) LatestVersion(ctx context.Context, repo string) (string, error) {
	rel, err := i.inst.Client.Latest(ctx, repo)
	if err != nil {
		return "", jobError(err)
	}
	return rel.Version, nil
}

// LatestHub implements admin.Installer.
func (i *installer) LatestHub(ctx context.Context) (admin.HubRelease, error) {
	rel, _, err := i.hub.Latest(ctx)
	if err != nil {
		return admin.HubRelease{}, jobError(err)
	}
	return admin.HubRelease{Version: rel.Version, NotesURL: rel.NotesURL}, nil
}

// UpdateHub implements admin.Installer: the new binary replaces the running one, the job ends, and a second later
// homed shuts down gracefully and exits with install.ExitCodeRestart (75) for the service manager to start it again
// (launchd KeepAlive, systemd Restart=; docs/DEV.md).
func (i *installer) UpdateHub(ctx context.Context, version string, allowDowngrade bool, report admin.Reporter) error {
	if i.restart == nil {
		return errNotServing
	}
	if version != "" {
		report(admin.JobReport{Message: version})
	}
	res, err := i.hub.Update(ctx, version, allowDowngrade, hooks(report))
	if err != nil {
		return jobError(err)
	}
	report(admin.JobReport{Step: install.StepRestart, Message: res.To})
	time.AfterFunc(restartDelay, i.restart)
	return nil
}
