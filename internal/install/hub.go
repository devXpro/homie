package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// ExitCodeRestart is the exit status homed should use after a successful HubUpdater.Update, once it has shut down
// gracefully. It is non-zero (EX_TEMPFAIL) so that both systemd "Restart=on-failure" and "Restart=always", and
// launchd "KeepAlive" in either form, bring the new binary up. See the package doc for the unit settings.
const ExitCodeRestart = 75

// DefaultHubRepo is where homed releases come from.
const DefaultHubRepo = "devXpro/homie"

// HubUpdater replaces the running homed binary with a newer release.
type HubUpdater struct {
	Client  *Client
	Repo    string // default DefaultHubRepo
	Binary  string // archive and file name, default "homed"
	Version string // the running version; "" or a dev build never counts as up to date
	ExePath string // the binary to replace; default os.Executable() with symlinks resolved
	GOOS    string // default runtime.GOOS
	GOARCH  string // default runtime.GOARCH
	Limits  Limits
	// SkipSmokeTest disables running "<new binary> version" before the swap. By default the new binary must
	// exit 0 within 15 s, which catches a wrong-platform or corrupt build while the old one is still in place.
	SkipSmokeTest bool
}

// HubUpdate describes a finished replacement.
type HubUpdate struct {
	From       string // running version before the update
	To         string // the installed release version
	ExePath    string // the replaced binary
	BackupPath string // the previous binary (ExePath + ".prev"), "" when it could not be kept
}

func (u *HubUpdater) repo() string {
	if u.Repo == "" {
		return DefaultHubRepo
	}
	return u.Repo
}

func (u *HubUpdater) binary() string {
	if u.Binary == "" {
		return "homed"
	}
	return u.Binary
}

func (u *HubUpdater) exe() (string, error) {
	p := u.ExePath
	if p == "" {
		var err error
		if p, err = os.Executable(); err != nil {
			return "", err
		}
	}
	return filepath.EvalSymlinks(p)
}

// Latest returns the latest release and whether it is newer than the running version. A dev build is always
// updatable.
func (u *HubUpdater) Latest(ctx context.Context) (*Release, bool, error) {
	rel, err := u.Client.Latest(ctx, u.repo())
	if err != nil {
		return nil, false, err
	}
	return rel, u.newer(rel.Version), nil
}

func (u *HubUpdater) newer(v string) bool {
	if isDevVersion(u.Version) {
		return true
	}
	c, err := CompareVersions(v, u.Version)
	return err == nil && c > 0
}

// Update downloads version ("" or "latest" is the latest release), verifies it against checksums.txt, unpacks it
// to a temporary directory, smoke-tests it and atomically swaps it over the running binary (a same-directory
// rename; the old binary stays at ExePath+".prev"). It does not exit: the caller finishes its job, shuts homed
// down gracefully and calls os.Exit(ExitCodeRestart) so the service manager starts the new binary. A package-
// manager-owned or read-only location gives ErrNotWritable; then the update has to go through that manager. A
// release older than the running version is ErrDowngrade unless allowDowngrade.
func (u *HubUpdater) Update(ctx context.Context, version string, allowDowngrade bool, hooks *Hooks) (*HubUpdate, error) {
	hooks.step(StepResolve)
	exe, err := u.exe()
	if err != nil {
		return nil, err
	}
	var rel *Release
	if version == "" || version == "latest" {
		rel, err = u.Client.Latest(ctx, u.repo())
	} else {
		rel, err = u.Client.ReleaseByVersion(ctx, u.repo(), version)
	}
	if err != nil {
		return nil, err
	}
	if rel.Version == u.Version {
		return nil, fmt.Errorf("%w: homed %s", ErrUpToDate, u.Version)
	}
	if !allowDowngrade && older(rel.Version, u.Version) {
		return nil, fmt.Errorf("%w: homed %s runs, the release is %s", ErrDowngrade, u.Version, rel.Version)
	}
	goos, goarch := u.GOOS, u.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	asset, err := FindAsset(rel, u.binary(), goos, goarch)
	if err != nil {
		return nil, err
	}
	if err := checkDirWritable(filepath.Dir(exe)); err != nil {
		return nil, err
	}

	lim := u.Limits.withDefaults()
	stage, err := os.MkdirTemp("", "homed-update-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)

	hooks.step(StepDownload)
	var pr Progress
	if hooks != nil {
		pr = hooks.Progress
	}
	archive, err := u.Client.Download(ctx, asset, stage, lim.MaxArchiveBytes, pr)
	if err != nil {
		return nil, err
	}
	hooks.step(StepVerify)
	if err := u.Client.Verify(ctx, rel, asset, archive); err != nil {
		return nil, err
	}
	unpacked := filepath.Join(stage, "unpacked")
	if err := os.Mkdir(unpacked, 0o700); err != nil {
		return nil, err
	}
	if err := Unpack(archive, unpacked, lim); err != nil {
		return nil, err
	}
	if err := flattenSingleDir(unpacked, u.binary()); err != nil {
		return nil, err
	}
	newBin := filepath.Join(unpacked, u.binary())
	if err := checkExecutable(newBin); err != nil {
		return nil, err
	}
	if !u.SkipSmokeTest {
		if err := smokeTest(ctx, newBin); err != nil {
			return nil, err
		}
	}

	hooks.step(StepReplace)
	backup, err := replaceExecutable(newBin, exe)
	if err != nil {
		return nil, err
	}
	return &HubUpdate{From: u.Version, To: rel.Version, ExePath: exe, BackupPath: backup}, nil
}

// Rollback puts the binary kept by Update back (ExePath+".prev" renamed over ExePath). It is for an operator or a
// failed restart; a running homed does not call it on its own.
func (u *HubUpdater) Rollback() error {
	exe, err := u.exe()
	if err != nil {
		return err
	}
	return os.Rename(exe+".prev", exe)
}

func smokeTest(ctx context.Context, bin string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "version")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: the new binary failed its self-test: %w", ErrStartFailed, err)
	}
	return nil
}

func checkDirWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".homed-write-test-*")
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrNotWritable, dir, err)
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// replaceExecutable copies src next to exe, keeps the old file as exe+".prev" (a hard link) and renames the copy
// over exe. The rename is atomic, so a crash leaves either the old or the new binary, never half of one.
func replaceExecutable(src, exe string) (string, error) {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".homed-new-*")
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrNotWritable, dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func(err error) (string, error) {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", err
	}
	in, err := os.Open(src) //nolint:gosec // the file Update just unpacked
	if err != nil {
		return cleanup(err)
	}
	_, err = io.Copy(tmp, in)
	_ = in.Close()
	if err != nil {
		return cleanup(err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		return cleanup(err)
	}
	backup := exe + ".prev"
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return cleanup(err)
	}
	if err := os.Link(exe, backup); err != nil {
		backup = "" // keeping a backup is best effort (some filesystems have no hard links)
	}
	if err := os.Rename(tmpName, exe); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	return backup, nil
}
