package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

const sample = `# my home
home_name: "Мой шалаш"
listen: 127.0.0.1:8080 # loopback only
relay:
  domain: homie.test
plugins:
  # the TV
  - id: tv
    command: ["/bin/homie-tv", "serve"]
people:
  - id: roman
    name: Roman
    plugins: ["*"]
    keys:
      - sha256: "` + hashA + `"
        label: "ChatGPT Site"
        created: 2026-11-15T12:00:00Z
`

func writeSample(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // test temp dir
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestLoad(t *testing.T) {
	c, err := Load(writeSample(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	if c.HomeName != "Мой шалаш" || c.ListenAddr() != "127.0.0.1:8080" || c.Relay.Domain != "homie.test" {
		t.Fatalf("config = %+v", c)
	}
	if p := c.Plugin("tv"); p == nil || p.Command[0] != "/bin/homie-tv" {
		t.Fatalf("plugin tv = %+v", p)
	}
	r := c.Person("roman")
	if r == nil || len(r.Keys) != 1 || r.Keys[0].Label != "ChatGPT Site" || r.Keys[0].SHA256 != hashA ||
		!r.Keys[0].Created.Equal(time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)) || r.Plugins[0] != "*" {
		t.Fatalf("person = %+v", r)
	}
	if !c.HasPhase1() {
		t.Fatal("HasPhase1 = false for a phase 1 file")
	}
	if c, err := Load(writeSample(t, "listen: 127.0.0.1:8080\nsetup_listen: 192.168.68.63:8090\nrelay:\n  domain: homie.test\n")); err != nil ||
		c.HasPhase1() || c.SetupListen != "192.168.68.63:8090" {
		t.Fatalf("phase 2 file: %+v, %v", c, err)
	}
}

// After the import, the phase 1 sections leave homed.yaml for homed.yaml.phase1; listen, relay and comments stay,
// and a second archive never overwrites the first backup.
func TestArchivePhase1(t *testing.T) {
	dir := writeSample(t, sample)
	backup, err := ArchivePhase1(dir)
	if err != nil {
		t.Fatal(err)
	}
	if backup != filepath.Join(dir, Phase1Backup) {
		t.Fatalf("backup = %q", backup)
	}
	if got := readFile(t, backup); got != sample {
		t.Fatalf("backup content:\n%s", got)
	}
	if st, err := os.Stat(backup); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode: %v, %v", st, err)
	}
	out := readFile(t, filepath.Join(dir, FileName))
	for _, gone := range []string{"home_name", "plugins:", "people:", hashA, "homie-tv"} {
		if strings.Contains(out, gone) {
			t.Errorf("homed.yaml still has %q:\n%s", gone, out)
		}
	}
	// "# my home" sits right above home_name, so YAML gives it to that key and it leaves with it (the backup has it).
	for _, keep := range []string{"listen: 127.0.0.1:8080 # loopback only", "domain: homie.test"} {
		if !strings.Contains(out, keep) {
			t.Errorf("homed.yaml lost %q:\n%s", keep, out)
		}
	}
	c, err := Load(dir)
	if err != nil || c.HasPhase1() || c.Relay.Domain != "homie.test" {
		t.Fatalf("after archive: %+v, %v", c, err)
	}

	// Nothing left to move: no new backup.
	if again, err := ArchivePhase1(dir); err != nil || again != "" {
		t.Fatalf("second archive = %q, %v", again, err)
	}
	// A hand edit that brings a section back is archived next to the first backup.
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(out+"home_name: Дача\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := ArchivePhase1(dir)
	if err != nil || third == "" || third == backup || !strings.HasPrefix(third, backup+".") {
		t.Fatalf("archive after a hand edit = %q, %v", third, err)
	}
	if readFile(t, backup) != sample || !strings.Contains(readFile(t, third), "Дача") {
		t.Fatal("the first backup was overwritten")
	}
	if missing, err := ArchivePhase1(t.TempDir()); err != nil || missing != "" {
		t.Fatalf("no homed.yaml: %q, %v", missing, err)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr() != DefaultListen || c.Relay.Domain != "" || len(c.Plugins) != 0 {
		t.Fatalf("config = %+v", c)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name, yaml, want string
	}{
		{"unknown key", "home_nam: x\n", "field home_nam not found"},
		{"public listen", "listen: 0.0.0.0:8080\n", "loopback only"},
		{"setup_listen without port", "setup_listen: 0.0.0.0\n", "missing port"},
		{"setup_listen bad port", "setup_listen: 0.0.0.0:http\n", "bad port"},
		{"bad domain", "relay:\n  domain: https://x.test\n", "invalid relay domain"},
		{"bad plugin id", "plugins:\n  - id: TV\n    command: [x]\n", "must match"},
		{"duplicate plugin", "plugins:\n  - id: tv\n    command: [x]\n  - id: tv\n    command: [y]\n", "twice"},
		{"empty command", "plugins:\n  - id: tv\n    command: []\n", "command is empty"},
		{"bad person id", "people:\n  - id: Roman\n    name: R\n", "must match"},
		{"no name", "people:\n  - id: roman\n    name: \"\"\n", "name is empty"},
		{"bad scope", "people:\n  - id: roman\n    name: R\n    plugins: [\"TV!\"]\n", "neither"},
		{"bad hash", "people:\n  - id: roman\n    name: R\n    keys:\n      - sha256: abc\n", "64 lowercase hex"},
		{"duplicate hash", "people:\n  - id: a\n    name: A\n    keys:\n      - sha256: " + hashA +
			"\n  - id: b\n    name: B\n    keys:\n      - sha256: " + hashA + "\n", "twice"},
		{"not a mapping", "- a\n- b\n", "cannot unmarshal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeSample(t, tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestUpdateKeepsUntouchedSections(t *testing.T) {
	dir := writeSample(t, sample)
	err := Update(dir, func(c *Config) error {
		c.Person("roman").Keys = append(c.Person("roman").Keys, Key{SHA256: hashB, Label: "second"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out := readFile(t, filepath.Join(dir, FileName))
	for _, keep := range []string{"# my home", "# loopback only", "# the TV", `command: ["/bin/homie-tv", "serve"]`} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q:\n%s", keep, out)
		}
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(c.Person("roman").Keys); n != 2 {
		t.Fatalf("keys = %d:\n%s", n, out)
	}
	st, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", st.Mode(), err)
	}
}

func TestUpdateRemovesEmptySection(t *testing.T) {
	dir := writeSample(t, sample)
	if err := Update(dir, func(c *Config) error { c.Plugins = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, filepath.Join(dir, FileName))
	if strings.Contains(out, "plugins:\n  #") || strings.Contains(out, "homie-tv") {
		t.Fatalf("plugins still there:\n%s", out)
	}
	if !strings.Contains(out, "people:") {
		t.Fatalf("people lost:\n%s", out)
	}
}

func TestUpdateNewFileAndErrors(t *testing.T) {
	dir := t.TempDir()
	if err := Update(dir, func(c *Config) error { c.Relay.Domain = "homie.test"; return nil }); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil || c.Relay.Domain != "homie.test" {
		t.Fatalf("config = %+v, %v", c, err)
	}

	before := readFile(t, filepath.Join(dir, FileName))
	boom := errors.New("boom")
	if err := Update(dir, func(*Config) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if err := Update(dir, func(c *Config) error { c.Listen = "0.0.0.0:1"; return nil }); err == nil {
		t.Fatal("an invalid change was accepted")
	}
	if after := readFile(t, filepath.Join(dir, FileName)); after != before {
		t.Fatalf("a failed update wrote the file:\n%s", after)
	}
}

func TestRelayToken(t *testing.T) {
	dir := t.TempDir()
	if err := WriteRelayToken(dir, "tok"); err != nil {
		t.Fatal(err)
	}
	if tok, err := ReadRelayToken(dir); err != nil || tok != "tok" {
		t.Fatalf("token = %q, %v", tok, err)
	}
	if err := os.Chmod(filepath.Join(dir, RelayTokenFile), 0o644); err != nil { //nolint:gosec // deliberately loose
		t.Fatal(err)
	}
	if _, err := ReadRelayToken(dir); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstanceIDIsStable(t *testing.T) {
	dir := t.TempDir()
	a, err := InstanceID(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := InstanceID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a != b || len(a) != 32 {
		t.Fatalf("ids %q and %q", a, b)
	}
}

func TestResolveDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh")
	got, err := ResolveDataDir(dir)
	if err != nil || got != dir {
		t.Fatalf("dir = %q, %v", got, err)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, %v", st.Mode(), err)
	}
	env := filepath.Join(t.TempDir(), "env")
	t.Setenv("HOMED_DATA_DIR", env)
	if got, err := ResolveDataDir(""); err != nil || got != env {
		t.Fatalf("dir = %q, %v", got, err)
	}
}

// An existing dir is never chmodded (a mistaken --data-dir must not lock up a home dir); the platform default dir is
// homed's own and is tightened.
func TestResolveDataDirLeavesExistingModes(t *testing.T) {
	loose := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(loose, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o755); err != nil { //nolint:gosec // deliberately loose
		t.Fatal(err)
	}
	if _, err := ResolveDataDir(loose); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(loose); st.Mode().Perm() != 0o755 {
		t.Fatalf("an existing dir was changed to %v", st.Mode().Perm())
	}
	if w := DataDirWarning(loose); !strings.Contains(w, "chmod 700 "+loose) {
		t.Fatalf("warning = %q", w)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdgconfig"))
	def, err := DefaultDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(def, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(def, 0o755); err != nil { //nolint:gosec // deliberately loose
		t.Fatal(err)
	}
	if got, err := ResolveDataDir(""); err != nil || got != def {
		t.Fatalf("default dir = %q, %v", got, err)
	}
	if st, _ := os.Stat(def); st.Mode().Perm() != 0o700 {
		t.Fatalf("default dir mode %v, want 0700", st.Mode().Perm())
	}
	if w := DataDirWarning(def); w != "" {
		t.Fatalf("warning on a 0700 dir: %q", w)
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveDataDir(file); err == nil {
		t.Fatal("a regular file was accepted as the data dir")
	}
}

func TestLockInstance(t *testing.T) {
	dir := t.TempDir()
	unlock, err := LockInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	// flock locks belong to the open file, so a second open in the same process conflicts like another homed would.
	if _, err := LockInstance(dir); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second lock: %v", err)
	}
	unlock()
	again, err := LockInstance(dir)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again()
}

func TestLoopbackPort(t *testing.T) {
	tests := []struct {
		addr    string
		port    int
		wantErr bool
	}{
		{"127.0.0.1:8080", 8080, false},
		{"localhost:8081", 8081, false},
		// The relay forwards to 127.0.0.1 only, so other loopback addresses would be unreachable through it.
		{"[::1]:9000", 0, true},
		{"127.0.0.2:8080", 0, true},
		{"127.1:8080", 0, true},
		{"0.0.0.0:8080", 0, true},
		{"192.168.68.63:8080", 0, true},
		{"127.0.0.1", 0, true},
		{"127.0.0.1:0", 0, true},
		{"127.0.0.1:http", 0, true},
	}
	for _, tt := range tests {
		port, err := LoopbackPort(tt.addr)
		if (err != nil) != tt.wantErr || port != tt.port {
			t.Errorf("LoopbackPort(%q) = %d, %v", tt.addr, port, err)
		}
	}
}
