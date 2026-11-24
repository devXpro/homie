package install

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type recorder struct {
	steps     []string
	started   []string
	switched  []string
	rolled    []string
	startErr  error
	switchErr error
	rollErr   error
}

func (r *recorder) hooks() *Hooks {
	return &Hooks{
		Step: func(s string) { r.steps = append(r.steps, s) },
		Switch: func(_ context.Context, n *Installed) error {
			r.switched = append(r.switched, n.Version)
			return r.switchErr
		},
		Start: func(_ context.Context, n *Installed) error {
			r.started = append(r.started, n.Version)
			return r.startErr
		},
		Rollback: func(_ context.Context, p *Installed) error {
			r.rolled = append(r.rolled, p.Version)
			return r.rollErr
		},
	}
}

func versionsOnDisk(t *testing.T, inst *Installer) []string {
	t.Helper()
	entries, err := os.ReadDir(inst.binDir("tv"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

func TestInstallHappyPath(t *testing.T) {
	f := newFakeGitHub(t)
	f.add("devXpro/homie-tv", tvRelease(t, "0.2.0"))
	inst := newTestInstaller(t, f)
	rec := &recorder{}

	got, err := inst.Install(t.Context(), "devXpro/homie-tv", "", "tv", rec.hooks())
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(inst.DataDir, "plugins", "tv", "bin", "0.2.0")
	if got.Dir != wantDir || got.Binary != filepath.Join(wantDir, "homie-tv") || got.Version != "0.2.0" || got.ID != "tv" {
		t.Fatalf("unexpected Installed: %+v", got)
	}
	if want := []string{got.Binary, "serve"}; !slices.Equal(got.Command, want) {
		t.Fatalf("Command = %v, want %v", got.Command, want)
	}
	if want := []string{StepResolve, StepDownload, StepVerify, StepUnpack, StepStart}; !slices.Equal(rec.steps, want) {
		t.Fatalf("steps = %v, want %v", rec.steps, want)
	}
	if len(rec.switched) != 0 || !slices.Equal(rec.started, []string{"0.2.0"}) {
		t.Fatalf("hooks: switched=%v started=%v", rec.switched, rec.started)
	}
	fi, err := os.Stat(got.Binary)
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("binary: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(wantDir, ManifestAsset)); err != nil {
		t.Fatal("manifest not stored:", err)
	}
	// Nothing but the plugin dir remains: the staging directory is gone.
	entries, _ := os.ReadDir(filepath.Join(inst.DataDir, "plugins"))
	if len(entries) != 1 || entries[0].Name() != "tv" {
		t.Fatalf("plugins dir holds %v", entries)
	}
	// Locate reads it back offline.
	loc, err := inst.Locate("tv", "0.2.0")
	if err != nil || loc.Binary != got.Binary || loc.Manifest.Title != "TV" {
		t.Fatalf("Locate: %+v %v", loc, err)
	}
}

func TestInstallSpecificVersionAndTagFallback(t *testing.T) {
	f := newFakeGitHub(t)
	f.add("devXpro/homie-tv", tvRelease(t, "0.2.0"))
	old := tvRelease(t, "0.1.0")
	old.tag = "0.1.0" // a tag without the v
	f.add("devXpro/homie-tv", old)
	inst := newTestInstaller(t, f)

	got, err := inst.Install(t.Context(), "devXpro/homie-tv", "0.1.0", "", nil)
	if err != nil || got.Version != "0.1.0" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := inst.Install(t.Context(), "devXpro/homie-tv", "9.9.9", "", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing version: %v", err)
	}
}

func TestInstallRefusals(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, r *fakeRelease)
		want   error
		code   string
		wantID string
	}{
		{"checksum mismatch", func(_ *testing.T, r *fakeRelease) {
			r.sums = map[string]string{ArchiveName("homie-tv", "0.2.0", "linux", "arm64"): strings.Repeat("0", 64)}
		}, ErrChecksumMismatch, "checksum_mismatch", ""},
		{"no checksums.txt", func(_ *testing.T, r *fakeRelease) { r.noSums = true }, ErrNoChecksum, "checksum_mismatch", ""},
		{"archive not listed", func(_ *testing.T, r *fakeRelease) { r.omit = ArchiveName("homie-tv", "0.2.0", "linux", "arm64") }, ErrNoChecksum, "checksum_mismatch", ""},
		{"no asset for this cpu", func(_ *testing.T, r *fakeRelease) {
			r.files = map[string][]byte{ArchiveName("homie-tv", "0.2.0", "darwin", "amd64"): {1}}
		}, ErrNoAsset, "no_asset", ""},
		{"hub too old", func(_ *testing.T, r *fakeRelease) { r.manifest = strings.Replace(tvManifest, `"0.1.0"`, `"0.3.0"`, 1) }, ErrIncompatible, "incompatible", ""},
		{"no manifest", func(_ *testing.T, r *fakeRelease) { r.manifest = "" }, ErrBadManifest, "upstream", ""},
		{"bad manifest binary", func(_ *testing.T, r *fakeRelease) {
			r.manifest = strings.Replace(tvManifest, "homie-tv\",\"args", "../homie-tv\",\"args", 1)
		}, ErrBadManifest, "upstream", ""},
		{"id mismatch", func(_ *testing.T, _ *fakeRelease) {}, ErrIDMismatch, "internal", "kettle"},
		{"binary missing in archive", func(t *testing.T, r *fakeRelease) {
			b := makeTarGz(t, []tarEntry{{name: "README.md", body: "x"}})
			r.files = map[string][]byte{ArchiveName("homie-tv", "0.2.0", "linux", "arm64"): b}
		}, ErrBadManifest, "upstream", ""},
		// The review's chain: the stored manifest would have been written through the links, outside the plugin dir.
		{"manifest symlink through a chain", func(t *testing.T, r *fakeRelease) {
			b := makeTarGz(t, []tarEntry{
				{name: "homie-tv", body: script, mode: 0o755},
				{name: "a/b/c/", typ: tar.TypeDir},
				{name: "a/b/c/up", typ: tar.TypeSymlink, target: "../../.."},
				{name: ManifestAsset, typ: tar.TypeSymlink, target: "a/b/c/up/../escaped"},
			})
			r.files = map[string][]byte{ArchiveName("homie-tv", "0.2.0", "linux", "arm64"): b}
		}, ErrUnsafeArchive, "upstream", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGitHub(t)
			r := tvRelease(t, "0.2.0")
			tc.setup(t, r)
			f.add("devXpro/homie-tv", r)
			inst := newTestInstaller(t, f)
			rec := &recorder{}
			_, err := inst.Install(t.Context(), "devXpro/homie-tv", "", tc.wantID, rec.hooks())
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if Code(err) != tc.code {
				t.Fatalf("code = %q, want %q", Code(err), tc.code)
			}
			if len(rec.started) != 0 {
				t.Fatal("Start must not run")
			}
			if _, statErr := os.Stat(filepath.Join(inst.DataDir, "plugins", "tv")); statErr == nil {
				t.Fatal("a refused install left files behind")
			}
			entries, _ := os.ReadDir(filepath.Join(inst.DataDir, "plugins"))
			for _, e := range entries {
				t.Errorf("leftover in plugins dir: %s", e.Name())
			}
		})
	}
}

func TestInstallStartFailureCleansUp(t *testing.T) {
	f := newFakeGitHub(t)
	f.add("devXpro/homie-tv", tvRelease(t, "0.2.0"))
	inst := newTestInstaller(t, f)
	rec := &recorder{startErr: errors.New("initialize timed out")}

	_, err := inst.Install(t.Context(), "devXpro/homie-tv", "", "tv", rec.hooks())
	if !errors.Is(err, ErrStartFailed) || Code(err) != "start_failed" {
		t.Fatalf("err = %v (%s)", err, Code(err))
	}
	if _, statErr := os.Stat(filepath.Join(inst.DataDir, "plugins", "tv")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("fresh plugin directory must be removed after a failed start")
	}
}

func TestInstallReplacesSameVersionDirectory(t *testing.T) {
	f := newFakeGitHub(t)
	f.add("devXpro/homie-tv", tvRelease(t, "0.2.0"))
	inst := newTestInstaller(t, f)
	dir := inst.VersionDir("tv", "0.2.0")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stale"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Install(t.Context(), "devXpro/homie-tv", "", "tv", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stale")); err == nil {
		t.Fatal("stale file survived")
	}
}

func TestInstallWrappedArchive(t *testing.T) {
	f := newFakeGitHub(t)
	r := tvRelease(t, "0.2.0")
	r.files = map[string][]byte{ArchiveName("homie-tv", "0.2.0", "linux", "arm64"): makeTarGz(t, []tarEntry{
		{name: "homie-tv_0.2.0_linux_arm64", typ: '5', mode: 0o755},
		{name: "homie-tv_0.2.0_linux_arm64/homie-tv", body: script, mode: 0o755},
		{name: "homie-tv_0.2.0_linux_arm64/LICENSE", body: "mit"},
	})}
	f.add("devXpro/homie-tv", r)
	inst := newTestInstaller(t, f)
	got, err := inst.Install(t.Context(), "devXpro/homie-tv", "", "tv", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(got.Dir, "LICENSE")); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateSuccessPrunesOldVersions(t *testing.T) {
	f := newFakeGitHub(t)
	for _, v := range []string{"0.1.0", "0.2.0", "0.3.0"} {
		f.add("devXpro/homie-tv", tvRelease(t, v))
	}
	inst := newTestInstaller(t, f)
	ctx := t.Context()
	// Install 0.1.0 and 0.2.0 the long way, leaving both on disk.
	if _, err := inst.Install(ctx, "devXpro/homie-tv", "0.1.0", "tv", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Update(ctx, "tv", "devXpro/homie-tv", "0.1.0", "0.2.0", false, nil); err != nil {
		t.Fatal(err)
	}
	if got := versionsOnDisk(t, inst); !slices.Equal(got, []string{"0.1.0", "0.2.0"}) {
		t.Fatalf("after first update: %v", got)
	}

	rec := &recorder{}
	next, err := inst.Update(ctx, "tv", "devXpro/homie-tv", "0.2.0", "latest", false, rec.hooks())
	if err != nil || next.Version != "0.3.0" {
		t.Fatalf("update: %+v %v", next, err)
	}
	if want := []string{StepResolve, StepDownload, StepVerify, StepUnpack, StepSwitch, StepStart}; !slices.Equal(rec.steps, want) {
		t.Fatalf("steps = %v", rec.steps)
	}
	if !slices.Equal(rec.switched, []string{"0.3.0"}) || !slices.Equal(rec.started, []string{"0.3.0"}) || len(rec.rolled) != 0 {
		t.Fatalf("hooks: %+v", rec)
	}
	// Only the new and the previous (rollback) versions stay.
	if got := versionsOnDisk(t, inst); !slices.Equal(got, []string{"0.2.0", "0.3.0"}) {
		t.Fatalf("after second update: %v", got)
	}
}

func TestUpdateRollsBackWhenStartFails(t *testing.T) {
	f := newFakeGitHub(t)
	f.add("devXpro/homie-tv", tvRelease(t, "0.1.0"))
	f.add("devXpro/homie-tv", tvRelease(t, "0.2.0"))
	inst := newTestInstaller(t, f)
	ctx := t.Context()
	if _, err := inst.Install(ctx, "devXpro/homie-tv", "0.1.0", "tv", nil); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{startErr: errors.New("plugin exited with status 2")}
	_, err := inst.Update(ctx, "tv", "devXpro/homie-tv", "0.1.0", "", false, rec.hooks())
	if !errors.Is(err, ErrStartFailed) || !errors.Is(err, ErrRolledBack) || Code(err) != "rolled_back" {
		t.Fatalf("err = %v (%s)", err, Code(err))
	}
	if !slices.Equal(rec.rolled, []string{"0.1.0"}) {
		t.Fatalf("rollback hook got %v", rec.rolled)
	}
	if want := []string{StepResolve, StepDownload, StepVerify, StepUnpack, StepSwitch, StepStart, StepRollback}; !slices.Equal(rec.steps, want) {
		t.Fatalf("steps = %v", rec.steps)
	}
	if got := versionsOnDisk(t, inst); !slices.Equal(got, []string{"0.1.0"}) {
		t.Fatalf("disk after rollback: %v", got)
	}
}

func TestUpdateRollbackFailureKeepsFiles(t *testing.T) {
	f := newFakeGitHub(t)
	f.add("devXpro/homie-tv", tvRelease(t, "0.1.0"))
	f.add("devXpro/homie-tv", tvRelease(t, "0.2.0"))
	inst := newTestInstaller(t, f)
	if _, err := inst.Install(t.Context(), "devXpro/homie-tv", "0.1.0", "tv", nil); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{startErr: errors.New("boom"), rollErr: errors.New("store down")}
	_, err := inst.Update(t.Context(), "tv", "devXpro/homie-tv", "0.1.0", "", false, rec.hooks())
	if !errors.Is(err, ErrStartFailed) || errors.Is(err, ErrRolledBack) || Code(err) != "start_failed" {
		t.Fatalf("err = %v (%s)", err, Code(err))
	}
	if got := versionsOnDisk(t, inst); !slices.Equal(got, []string{"0.1.0", "0.2.0"}) {
		t.Fatalf("both versions must stay for the operator: %v", got)
	}
}

func TestUpdateSwitchFailureAborts(t *testing.T) {
	f := newFakeGitHub(t)
	f.add("devXpro/homie-tv", tvRelease(t, "0.1.0"))
	f.add("devXpro/homie-tv", tvRelease(t, "0.2.0"))
	inst := newTestInstaller(t, f)
	if _, err := inst.Install(t.Context(), "devXpro/homie-tv", "0.1.0", "tv", nil); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{switchErr: errors.New("db locked")}
	if _, err := inst.Update(t.Context(), "tv", "devXpro/homie-tv", "0.1.0", "", false, rec.hooks()); err == nil {
		t.Fatal("expected error")
	}
	if len(rec.started) != 0 || len(rec.rolled) != 0 {
		t.Fatalf("Start/Rollback must not run: %+v", rec)
	}
	if got := versionsOnDisk(t, inst); !slices.Equal(got, []string{"0.1.0"}) {
		t.Fatalf("disk: %v", got)
	}
}

func TestUpdateRefusals(t *testing.T) {
	f := newFakeGitHub(t)
	f.add("devXpro/homie-tv", tvRelease(t, "0.1.0"))
	inst := newTestInstaller(t, f)
	if _, err := inst.Update(t.Context(), "tv", "devXpro/homie-tv", "0.1.0", "", false, nil); !errors.Is(err, ErrUpToDate) || Code(err) != "up_to_date" {
		t.Fatalf("same version: %v", err)
	}
	if _, err := inst.Update(t.Context(), "kettle", "devXpro/homie-tv", "0.0.1", "", false, nil); !errors.Is(err, ErrIDMismatch) {
		t.Fatalf("id mismatch: %v", err)
	}
	if _, err := inst.Update(t.Context(), "../x", "devXpro/homie-tv", "0.0.1", "", false, nil); err == nil {
		t.Fatal("bad id accepted")
	}
}

// An older release is a downgrade, named or as "latest" (a newer release was pulled), and needs explicit permission.
func TestUpdateRefusesDowngrade(t *testing.T) {
	f := newFakeGitHub(t)
	f.add("devXpro/homie-tv", tvRelease(t, "0.1.0"))
	f.add("devXpro/homie-tv", tvRelease(t, "0.2.0"))
	inst := newTestInstaller(t, f)
	ctx := t.Context()
	if _, err := inst.Install(ctx, "devXpro/homie-tv", "0.2.0", "tv", nil); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"0.1.0", "v0.1.0"} {
		rec := &recorder{}
		_, err := inst.Update(ctx, "tv", "devXpro/homie-tv", "0.2.0", version, false, rec.hooks())
		if !errors.Is(err, ErrDowngrade) || Code(err) != "downgrade" {
			t.Fatalf("%s: err = %v (%s)", version, err, Code(err))
		}
		if len(rec.switched) != 0 || len(rec.started) != 0 {
			t.Fatalf("%s: hooks ran: %+v", version, rec)
		}
	}
	// "latest" resolving to an older release (installed 0.3.0 was pulled from GitHub) is a downgrade too.
	if _, err := inst.Update(ctx, "tv", "devXpro/homie-tv", "0.3.0", "latest", false, nil); !errors.Is(err, ErrDowngrade) {
		t.Fatalf("latest older than installed: %v", err)
	}
	if got := versionsOnDisk(t, inst); !slices.Equal(got, []string{"0.2.0"}) {
		t.Fatalf("disk after refused downgrades: %v", got)
	}
	// Allowed explicitly, it goes through.
	next, err := inst.Update(ctx, "tv", "devXpro/homie-tv", "0.2.0", "0.1.0", true, nil)
	if err != nil || next.Version != "0.1.0" {
		t.Fatalf("allowed downgrade: %+v %v", next, err)
	}
	// A development build or an unknown version has nothing to go back from.
	if older("0.1.0", "") || older("0.1.0", "dev") || !older("0.1.0", "0.2.0") || older("0.2.0", "0.1.0") {
		t.Fatal("older() is wrong")
	}
}

func TestResolvePreview(t *testing.T) {
	f := newFakeGitHub(t)
	r := tvRelease(t, "0.2.0")
	r.files = map[string][]byte{ArchiveName("homie-tv", "0.2.0", "windows", "amd64"): {1}}
	r.manifest = strings.Replace(tvManifest, `"0.1.0"`, `"0.9.0"`, 1)
	f.add("devXpro/homie-tv", r)
	inst := newTestInstaller(t, f)
	c, err := inst.Resolve(t.Context(), "devXpro/homie-tv", "latest")
	if err != nil {
		t.Fatal(err)
	}
	if c.AssetFound || c.Compatible || c.Manifest.ID != "tv" || c.Release.Version != "0.2.0" || len(c.RawManifest) == 0 {
		t.Fatalf("candidate: %+v", c)
	}
	// A development hub is compatible with any minHubVersion.
	inst.HubVersion = "dev"
	if c, _ = inst.Resolve(t.Context(), "devXpro/homie-tv", ""); !c.Compatible {
		t.Fatal("dev hub must be compatible")
	}
}

func TestRemoveAndPrune(t *testing.T) {
	f := newFakeGitHub(t)
	f.add("devXpro/homie-tv", tvRelease(t, "0.1.0"))
	inst := newTestInstaller(t, f)
	if _, err := inst.Install(t.Context(), "devXpro/homie-tv", "", "tv", nil); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(inst.DataDir, "plugins", "tv", "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := inst.Remove("tv", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(data); err != nil {
		t.Fatal("keepData must keep the data dir")
	}
	if _, err := os.Stat(inst.binDir("tv")); err == nil {
		t.Fatal("bin must go")
	}
	if err := inst.Remove("tv", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(inst.DataDir, "plugins", "tv")); err == nil {
		t.Fatal("plugin dir must go")
	}
	if err := inst.Remove("../etc", false); err == nil {
		t.Fatal("bad id accepted")
	}
}
