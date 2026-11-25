package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `# my home
listen: 127.0.0.1:8080 # loopback only
setup_listen: 0.0.0.0:8090
tunnel:
  # my domain
  mode: named
  hostname: home.example.com
`

// v01 is a v0.1 homed.yaml: a relay section and nothing else.
const v01 = `# my home
listen: 127.0.0.1:8080 # loopback only
relay:
  domain: relay.example.com
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
	if c.ListenAddr() != "127.0.0.1:8080" || c.SetupListen != "0.0.0.0:8090" || c.Tunnel.Mode != TunnelNamed ||
		c.Tunnel.Hostname != "home.example.com" || !c.Tunnel.Configured() || c.HasRelay() {
		t.Fatalf("config = %+v", c)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr() != DefaultListen || c.Tunnel.ModeOrOff() != TunnelOff || c.Tunnel.Configured() {
		t.Fatalf("config = %+v", c)
	}
}

// v0.1's relay section still loads (homed ignores it, with a warning, until MigrateV01 removes it).
func TestLoadIgnoresTheOldRelay(t *testing.T) {
	c, err := Load(writeSample(t, v01))
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasRelay() || c.Tunnel.Configured() {
		t.Fatalf("config = %+v", c)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name, yaml, want string
	}{
		{"unknown key", "listn: x\n", "field listn not found"},
		{"phase 1 section", "people: []\n", "field people not found"},
		{"public listen", "listen: 0.0.0.0:8080\n", "loopback only"},
		{"setup_listen without port", "setup_listen: 0.0.0.0\n", "missing port"},
		{"setup_listen bad port", "setup_listen: 0.0.0.0:http\n", "bad port"},
		{"unknown tunnel mode", "tunnel:\n  mode: relay\n", `mode "relay" is not named, quick or off`},
		{"named without host", "tunnel:\n  mode: named\n", "needs a hostname"},
		{"bad hostname", "tunnel:\n  mode: named\n  hostname: https://home.example.com\n", "not a full host name"},
		{"upper-case hostname", "tunnel:\n  mode: named\n  hostname: Home.Example.com\n", "not a full host name"},
		{"single label", "tunnel:\n  mode: named\n  hostname: home\n", "not a full host name"},
		{"relative cloudflared", "tunnel:\n  cloudflared: bin/cloudflared\n", "absolute path or a name"},
		{"unknown tunnel key", "tunnel:\n  token: x\n", "field token not found"},
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
	for _, ok := range []string{
		"tunnel:\n  mode: quick\n",
		"tunnel:\n  mode: off\n  hostname: home.example.com\n",
		"tunnel:\n  cloudflared: cloudflared\n",
		"tunnel:\n  cloudflared: /usr/local/bin/cloudflared\n",
	} {
		if _, err := Load(writeSample(t, ok)); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

func TestUpdateKeepsUntouchedSections(t *testing.T) {
	dir := writeSample(t, sample)
	err := Update(dir, func(c *Config) error {
		c.Tunnel = Tunnel{Mode: TunnelQuick}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out := readFile(t, filepath.Join(dir, FileName))
	for _, keep := range []string{"# my home", "# loopback only", "setup_listen: 0.0.0.0:8090"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q:\n%s", keep, out)
		}
	}
	c, err := Load(dir)
	if err != nil || c.Tunnel.Mode != TunnelQuick || c.Tunnel.Hostname != "" {
		t.Fatalf("config = %+v, %v\n%s", c, err, out)
	}
	st, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", st.Mode(), err)
	}
}

func TestUpdateRemovesEmptySection(t *testing.T) {
	dir := writeSample(t, sample)
	if err := Update(dir, func(c *Config) error { c.Tunnel = Tunnel{}; return nil }); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, filepath.Join(dir, FileName))
	if strings.Contains(out, "tunnel") || strings.Contains(out, "home.example.com") {
		t.Fatalf("tunnel still there:\n%s", out)
	}
	if !strings.Contains(out, "listen: 127.0.0.1:8080") {
		t.Fatalf("listen lost:\n%s", out)
	}
}

func TestUpdateNewFileAndErrors(t *testing.T) {
	dir := t.TempDir()
	err := Update(dir, func(c *Config) error {
		c.Tunnel = Tunnel{Mode: TunnelNamed, Hostname: "home.example.com"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil || c.Tunnel.Hostname != "home.example.com" {
		t.Fatalf("config = %+v, %v", c, err)
	}

	before := readFile(t, filepath.Join(dir, FileName))
	boom := errors.New("boom")
	if err := Update(dir, func(*Config) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if err := Update(dir, func(c *Config) error { c.Tunnel.Mode = "relay"; return nil }); err == nil {
		t.Fatal("an invalid change was accepted")
	}
	if after := readFile(t, filepath.Join(dir, FileName)); after != before {
		t.Fatalf("a failed update wrote the file:\n%s", after)
	}
}

func TestMigrateV01(t *testing.T) {
	dir := writeSample(t, v01)
	token := filepath.Join(dir, relayTokenFile)
	if err := os.WriteFile(token, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := MigrateV01(dir)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, V01Backup)
	if res != (MigrateResult{Backup: backup, RelayRemoved: true, TokenDeleted: true}) {
		t.Errorf("result = %+v", res)
	}
	if got := readFile(t, backup); got != v01 {
		t.Errorf("backup = %q", got)
	}
	if fi, err := os.Stat(backup); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("backup mode = %v, %v", fi, err)
	}
	if _, err := os.Stat(token); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("relay-token still there: %v", err)
	}
	out := readFile(t, filepath.Join(dir, FileName))
	if strings.Contains(out, "relay") || !strings.Contains(out, "# loopback only") {
		t.Errorf("homed.yaml =\n%s", out)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The home had a relay, so its first-run page does not move onto the home network.
	if c.HasRelay() || c.Tunnel.ModeOrOff() != TunnelOff || c.Tunnel.Mode != TunnelOff || c.SetupListen != "127.0.0.1:8090" {
		t.Errorf("config = %+v", c)
	}
	// Idempotent.
	before := readFile(t, filepath.Join(dir, FileName))
	if res, err := MigrateV01(dir); err != nil || res != (MigrateResult{}) {
		t.Errorf("second run = %+v, %v", res, err)
	}
	if after := readFile(t, filepath.Join(dir, FileName)); after != before {
		t.Errorf("a second run changed homed.yaml:\n%s", after)
	}
}

// A setup_listen of the owner's own stays as it was.
func TestMigrateV01KeepsSetupListen(t *testing.T) {
	dir := writeSample(t, "setup_listen: 0.0.0.0:9090\nrelay:\n  domain: relay.example.com\n")
	if _, err := MigrateV01(dir); err != nil {
		t.Fatal(err)
	}
	if c, err := Load(dir); err != nil || c.SetupListen != "0.0.0.0:9090" {
		t.Errorf("config = %+v, %v", c, err)
	}
}

// A crash after the backup and before the rewrite: the next run rewrites and keeps the first backup.
func TestMigrateV01AfterACrash(t *testing.T) {
	dir := writeSample(t, v01)
	backup := filepath.Join(dir, V01Backup)
	if err := os.WriteFile(backup, []byte(v01), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := MigrateV01(dir)
	if err != nil || res.Backup != "" || !res.RelayRemoved {
		t.Fatalf("= %+v, %v", res, err)
	}
	if c, err := Load(dir); err != nil || c.HasRelay() {
		t.Errorf("config = %+v, %v", c, err)
	}
	// A crash after the rewrite and before the token was deleted: only the token goes.
	if err := os.WriteFile(filepath.Join(dir, relayTokenFile), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, err := MigrateV01(dir); err != nil || res != (MigrateResult{TokenDeleted: true}) {
		t.Errorf("= %+v, %v", res, err)
	}
}

func TestMigrateV01FreshDataDir(t *testing.T) {
	dir := t.TempDir()
	if res, err := MigrateV01(dir); err != nil || res != (MigrateResult{}) {
		t.Errorf("= %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a fresh dir got a homed.yaml: %v", err)
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
	if fi, err := os.Stat(filepath.Join(dir, InstanceIDFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", fi, err)
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
		// cloudflared forwards to 127.0.0.1 only, so other loopback addresses would be unreachable through it.
		{"[::1]:9000", 0, true},
		{"127.0.0.2:8080", 0, true},
		{"127.1:8080", 0, true},
		{"0.0.0.0:8080", 0, true},
		{"192.0.2.10:8080", 0, true},
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

func TestCheckHostname(t *testing.T) {
	for _, ok := range []string{"home.example.com", "a.b", "x-1.example.com"} {
		if err := CheckHostname(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "home", "Home.example.com", "-a.example.com", "a..example.com", "a.example.com.",
		"https://a.example.com", "a.example.com/x", "a_b.example.com"} {
		if err := CheckHostname(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
