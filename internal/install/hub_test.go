package install

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	oldHub = "#!/bin/sh\n# old homed\nexit 0\n"
)

func homedRelease(t *testing.T, body string) *fakeRelease {
	const version = "0.3.0"
	t.Helper()
	arch := makeTarGz(t, []tarEntry{
		{name: "homed", body: body, mode: 0o755},
		{name: "LICENSE", body: "mit"},
	})
	return &fakeRelease{tag: "v" + version, files: map[string][]byte{ArchiveName("homed", version, "linux", "arm64"): arch}}
}

func newHubUpdater(t *testing.T, f *fakeGitHub, running string) (*HubUpdater, string) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "homed")
	if err := os.WriteFile(exe, []byte(oldHub), 0o755); err != nil { //nolint:gosec // a test executable
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil { // macOS temp dirs live behind a symlink
		exe = resolved
	}
	return &HubUpdater{Client: f.client(""), Repo: "devXpro/homie", Version: running, ExePath: exe, GOOS: "linux", GOARCH: "arm64"}, exe
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p) //nolint:gosec // test helper
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestHubUpdateReplacesBinaryAndKeepsBackup(t *testing.T) {
	f := newFakeGitHub(t)
	newBody := "#!/bin/sh\n# new homed 0.3.0\nexit 0\n"
	f.add("devXpro/homie", homedRelease(t, newBody))
	u, exe := newHubUpdater(t, f, "0.2.0")
	var steps []string

	rel, newer, err := u.Latest(t.Context())
	if err != nil || !newer || rel.Version != "0.3.0" {
		t.Fatalf("Latest: %+v %v %v", rel, newer, err)
	}
	res, err := u.Update(t.Context(), "", false, &Hooks{Step: func(s string) { steps = append(steps, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if res.From != "0.2.0" || res.To != "0.3.0" || res.BackupPath != exe+".prev" {
		t.Fatalf("result: %+v", res)
	}
	if got := readFile(t, exe); got != newBody {
		t.Fatalf("exe = %q", got)
	}
	if got := readFile(t, exe+".prev"); got != oldHub {
		t.Fatalf("backup = %q", got)
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v", fi.Mode())
	}
	if strings.Join(steps, ",") != "resolve,download,verify,replace" {
		t.Fatalf("steps = %v", steps)
	}
	// No temp files left next to the binary.
	entries, _ := os.ReadDir(filepath.Dir(exe))
	if len(entries) != 2 {
		t.Fatalf("directory holds %d entries: %v", len(entries), entries)
	}
	// The operator's rollback.
	if err := u.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, exe); got != oldHub {
		t.Fatalf("after rollback exe = %q", got)
	}
}

func TestHubUpdateRefusals(t *testing.T) {
	t.Run("up to date", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.add("devXpro/homie", homedRelease(t, oldHub))
		u, exe := newHubUpdater(t, f, "0.3.0")
		_, newer, err := u.Latest(t.Context())
		if err != nil || newer {
			t.Fatalf("newer=%v err=%v", newer, err)
		}
		if _, err := u.Update(t.Context(), "", false, nil); !errors.Is(err, ErrUpToDate) {
			t.Fatalf("err = %v", err)
		}
		if readFile(t, exe) != oldHub {
			t.Fatal("binary touched")
		}
	})
	t.Run("an older release is a downgrade", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.add("devXpro/homie", homedRelease(t, oldHub+"# 0.3.0\n"))
		u, exe := newHubUpdater(t, f, "0.4.1")
		for _, version := range []string{"", "latest", "0.3.0"} {
			if _, err := u.Update(t.Context(), version, false, nil); !errors.Is(err, ErrDowngrade) || Code(err) != "downgrade" {
				t.Fatalf("%q: err = %v", version, err)
			}
		}
		if readFile(t, exe) != oldHub {
			t.Fatal("binary touched")
		}
		if _, err := u.Update(t.Context(), "0.3.0", true, nil); err != nil {
			t.Fatalf("allowed downgrade: %v", err)
		}
		if readFile(t, exe) != oldHub+"# 0.3.0\n" {
			t.Fatal("the allowed downgrade did not replace the binary")
		}
	})
	t.Run("checksum mismatch leaves the binary alone", func(t *testing.T) {
		f := newFakeGitHub(t)
		r := homedRelease(t, "evil")
		r.sums = map[string]string{ArchiveName("homed", "0.3.0", "linux", "arm64"): strings.Repeat("a", 64)}
		f.add("devXpro/homie", r)
		u, exe := newHubUpdater(t, f, "0.2.0")
		if _, err := u.Update(t.Context(), "", false, nil); !errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("err = %v", err)
		}
		if readFile(t, exe) != oldHub {
			t.Fatal("binary touched")
		}
	})
	t.Run("no asset", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.add("devXpro/homie", homedRelease(t, oldHub))
		u, _ := newHubUpdater(t, f, "0.2.0")
		u.GOOS = "plan9"
		if _, err := u.Update(t.Context(), "", false, nil); !errors.Is(err, ErrNoAsset) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("failing self-test", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.add("devXpro/homie", homedRelease(t, "#!/bin/sh\nexit 3\n"))
		u, exe := newHubUpdater(t, f, "0.2.0")
		if _, err := u.Update(t.Context(), "", false, nil); !errors.Is(err, ErrStartFailed) {
			t.Fatalf("err = %v", err)
		}
		if readFile(t, exe) != oldHub {
			t.Fatal("binary touched")
		}
		u.SkipSmokeTest = true
		if _, err := u.Update(t.Context(), "", false, nil); err != nil {
			t.Fatalf("with the self-test skipped: %v", err)
		}
	})
	t.Run("read-only directory", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.add("devXpro/homie", homedRelease(t, oldHub))
		u, exe := newHubUpdater(t, f, "0.2.0")
		dir := filepath.Dir(exe)
		if err := os.Chmod(dir, 0o555); err != nil { //nolint:gosec // the point of the test
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) //nolint:gosec // let TempDir clean up
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		if _, err := u.Update(t.Context(), "", false, nil); !errors.Is(err, ErrNotWritable) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestHubUpdateFollowsSymlinkedExecutable(t *testing.T) {
	f := newFakeGitHub(t)
	f.add("devXpro/homie", homedRelease(t, oldHub+"# new\n"))
	u, target := newHubUpdater(t, f, "0.2.0")
	link := filepath.Join(t.TempDir(), "homed-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	u.ExePath = link
	if _, err := u.Update(t.Context(), "0.3.0", false, nil); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink must stay a symlink: %v %v", fi, err)
	}
	if !strings.HasSuffix(readFile(t, target), "# new\n") {
		t.Fatal("the real binary was not replaced")
	}
}

func TestHubDevBuildIsAlwaysUpdatable(t *testing.T) {
	f := newFakeGitHub(t)
	f.add("devXpro/homie", homedRelease(t, oldHub))
	u, _ := newHubUpdater(t, f, "dev")
	if _, newer, err := u.Latest(t.Context()); err != nil || !newer {
		t.Fatalf("newer=%v err=%v", newer, err)
	}
}
