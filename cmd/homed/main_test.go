package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/devXpro/homie/internal/config"
	"github.com/devXpro/homie/internal/keys"
	"github.com/devXpro/homie/internal/relay"
	"github.com/devXpro/homie/internal/store"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testCode(t *testing.T, domain string) string {
	t.Helper()
	code, err := relay.FormatCode(relay.Code{Domain: domain, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// cli runs homed with args and returns stdout and stderr.
func cli(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = run(args, strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), err
}

// connectRelay runs `homed relay connect -` with a code for homie.test on stdin and returns its stdout.
func connectRelay(t *testing.T, dir string) string {
	t.Helper()
	out, errOut, err := cli(t, testCode(t, "homie.test")+"\n", "relay", "connect", "-", "--data-dir", dir)
	if err != nil {
		t.Fatalf("homed relay connect -: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	return out
}

func mustCLI(t *testing.T, args ...string) string {
	t.Helper()
	out, errOut, err := cli(t, "", args...)
	if err != nil {
		t.Fatalf("homed %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, out, errOut)
	}
	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // test temp dir
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// openDB opens a data dir's database the way homed does (with the import), for checks after a command.
func openDB(t *testing.T, dir string) *store.DB {
	t.Helper()
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := openStore(context.Background(), dir, cfg, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestVersion(t *testing.T) {
	if out := mustCLI(t, "version"); out != "homed dev\n" {
		t.Fatalf("version output = %q", out)
	}
}

func TestUnknownCommands(t *testing.T) {
	for _, args := range [][]string{{"bogus"}, {"relay", "bogus"}, {"key"}, {"tool", "x"}, {"admin", "x"}} {
		if _, _, err := cli(t, "", args...); err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("homed %v: err = %v", args, err)
		}
	}
	if _, _, err := cli(t, ""); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("no args: err = %v", err)
	}
}

func TestParseFlags(t *testing.T) {
	tests := []struct {
		args    []string
		dataDir string
		pos     []string
	}{
		{[]string{"a", "--data-dir", "D", "b"}, "D", []string{"a", "b"}},
		{[]string{"--data-dir", "D", "a"}, "D", []string{"a"}},
		{[]string{"-"}, "", []string{"-"}},
		{[]string{"--data-dir", "D", "--", "/bin/x", "--data-dir", "y"}, "D", []string{"/bin/x", "--data-dir", "y"}},
		{[]string{"tv_set", `{"percent":20}`}, "", []string{"tv_set", `{"percent":20}`}},
	}
	for _, tt := range tests {
		fl, dataDir := newFlags("t", &bytes.Buffer{})
		pos, err := parseFlags(fl, tt.args)
		if err != nil || *dataDir != tt.dataDir || strings.Join(pos, "|") != strings.Join(tt.pos, "|") {
			t.Errorf("parseFlags(%q) = %q, data-dir %q, %v", tt.args, pos, *dataDir, err)
		}
	}
}

// romanYAML is the shape of Roman's phase 1 homed.yaml: the relay, the TV plugin and his key.
func romanYAML(hash string) string {
	return `# my home
home_name: "Мой шалаш"
listen: 127.0.0.1:8080 # loopback only
relay:
  domain: homie.test
plugins:
  - id: tv
    command: ["/bin/sh", "serve"]
people:
  - id: roman
    name: Roman
    plugins: ["*"]
    keys:
      - sha256: "` + hash + `"
        label: "ChatGPT Site"
        created: 2026-11-15T12:00:00Z
`
}

// TestPhase1Import is the migration of an existing data dir: the people, the key, the plugin and the home name move
// into homed.db once, homed.yaml keeps listen and the relay, and the old file stays as homed.yaml.phase1.
func TestPhase1Import(t *testing.T) {
	dir := t.TempDir()
	_, hash := keys.Generate()
	writeFile(t, filepath.Join(dir, config.FileName), romanYAML(hash))
	ctx := context.Background()
	st := openDB(t, dir)

	roman, err := st.People().Get(ctx, "roman")
	if err != nil || roman.Name != "Roman" || roman.Role != store.RoleOwner || strings.Join(roman.Plugins, ",") != "*" {
		t.Fatalf("roman = %+v, %v", roman, err)
	}
	ks, err := st.Keys().List(ctx)
	if err != nil || len(ks) != 1 || ks[0].SHA256 != hash || ks[0].PersonID != "roman" || ks[0].Label != "ChatGPT Site" {
		t.Fatalf("keys = %+v, %v", ks, err)
	}
	tv, err := st.Plugins().Get(ctx, "tv")
	if err != nil || !tv.Enabled || tv.Source != store.PluginLocal || strings.Join(tv.Command, " ") != "/bin/sh serve" {
		t.Fatalf("tv = %+v, %v", tv, err)
	}
	if hs, err := st.Settings().Get(ctx); err != nil || hs.HomeName != "Мой шалаш" {
		t.Fatalf("settings = %+v, %v", hs, err)
	}
	cfg, err := config.Load(dir)
	if err != nil || cfg.HasPhase1() || cfg.Relay.Domain != "homie.test" || cfg.Listen != "127.0.0.1:8080" {
		t.Fatalf("homed.yaml after the import = %+v, %v", cfg, err)
	}
	if backup := readFile(t, filepath.Join(dir, config.Phase1Backup)); !strings.Contains(backup, hash) {
		t.Fatalf("backup:\n%s", backup)
	}
	// A second start finds nothing to import.
	_ = st.Close()
	st = openDB(t, dir)
	if people, err := st.People().List(ctx); err != nil || len(people) != 1 {
		t.Fatalf("people after a second open = %+v, %v", people, err)
	}
}

func TestRelayConnectOffline(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	writeFile(t, filepath.Join(dir, config.FileName), "# my relay\nlisten: 127.0.0.1:8080 # loopback\n")

	// "-" goes first and the flag after it, as people type it; the code comes on stdin.
	out, errOut, err := cli(t, testCode(t, "homie.test")+"\n", "relay", "connect", "-", "--data-dir", dir)
	if err != nil {
		t.Fatalf("%v; stderr %s", err, errOut)
	}
	if strings.Contains(out+errOut, testToken) || !strings.Contains(out, "homie.test") {
		t.Fatalf("output: %s %s", out, errOut)
	}
	st, err := os.Stat(filepath.Join(dir, config.RelayTokenFile))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("relay-token: %v, %v", st, err)
	}
	if tok, err := config.ReadRelayToken(dir); err != nil || tok != testToken {
		t.Fatalf("token = %q, %v", tok, err)
	}
	cfg, err := config.Load(dir)
	if err != nil || cfg.Relay.Domain != "homie.test" || cfg.Listen != "127.0.0.1:8080" {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
	if raw := readFile(t, filepath.Join(dir, config.FileName)); !strings.Contains(raw, "# loopback") {
		t.Errorf("homed.yaml lost its comment:\n%s", raw)
	}

	// A second connect, from stdin, replaces the domain in place and says what the passkeys need.
	_, errOut, err = cli(t, testCode(t, "other.test")+"\n", "relay", "connect", "--data-dir", dir, "-")
	if err != nil || !strings.Contains(errOut, "reset-access") {
		t.Fatalf("second connect: %v %s", err, errOut)
	}
	cfg, err = config.Load(dir)
	if err != nil || cfg.Relay.Domain != "other.test" {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
	if n := strings.Count(readFile(t, filepath.Join(dir, config.FileName)), "relay:"); n != 1 {
		t.Fatalf("homed.yaml has %d relay keys", n)
	}
	recs, err := openDB(t, dir).Audit().Query(context.Background(), store.AuditFilter{Kind: store.AuditAdmin})
	if err != nil || len(recs) != 2 || recs[0].Action != "relay.change" || recs[0].Target != "other.test" || recs[0].Via != "cli" {
		t.Fatalf("audit = %+v, %v", recs, err)
	}
}

func TestRelayConnectErrors(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		args []string
	}{
		{"no code", []string{"relay", "connect", "--data-dir", dir}},
		{"bad code", []string{"relay", "connect", "--data-dir", dir, "-"}},
		{"extra args", []string{"relay", "connect", "--data-dir", dir, "-", "extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := cli(t, "ghr1.zzz\n", tt.args...); err == nil {
				t.Fatal("want an error")
			}
		})
	}
	// A code on the command line is refused, a valid one too, and the error never repeats it (review L10).
	code := testCode(t, "a.test")
	out, errOut, err := cli(t, "", "relay", "connect", code, "--data-dir", dir)
	if !errors.Is(err, errCodeInArgv) || strings.Contains(err.Error()+out+errOut, code) || !strings.Contains(err.Error(), "relay connect -") {
		t.Fatalf("code in argv: %v %q %q", err, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, config.RelayTokenFile)); !os.IsNotExist(err) {
		t.Fatal("a failed connect wrote the token")
	}
}

func TestCommandsNeedRunningHomed(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"relay", "status"}, {"tool", "list"}, {"tool", "call", "x_y"}, {"admin", "firstrun"}} {
		_, _, err := cli(t, "", append(args, "--data-dir", dir)...)
		if !errors.Is(err, errNotRunning) {
			t.Errorf("homed %v: err = %v", args, err)
		}
	}
}

// TestOfflineCommands: without a running homed the key, plugin and admin commands work on the data dir (through
// the same admin API, in-process).
func TestOfflineCommands(t *testing.T) {
	dir := t.TempDir()
	known, hash := keys.Generate()
	writeFile(t, filepath.Join(dir, config.FileName), strings.Replace(romanYAML(hash), "relay:\n  domain: homie.test\n", "", 1))

	// key list runs the import first: Roman's phase 1 key is there.
	list := mustCLI(t, "key", "list", "--data-dir", dir)
	if !strings.Contains(list, "ChatGPT Site") || !strings.Contains(list, "Roman (roman)") || strings.Contains(list, known) {
		t.Fatalf("key list:\n%s", list)
	}
	out, errOut, err := cli(t, "", "key", "new", "--label", "Second", "--data-dir", dir)
	if err != nil {
		t.Fatal(err, errOut)
	}
	key := strings.TrimSpace(out)
	if !strings.HasPrefix(key, keys.Prefix) || strings.Count(out, "\n") != 1 || strings.Contains(errOut, key) ||
		!strings.Contains(errOut, "not running") {
		t.Fatalf("key new: stdout %q, stderr %q", out, errOut)
	}
	st := openDB(t, dir)
	ks, err := st.Keys().ListByPerson(context.Background(), "roman")
	if err != nil || len(ks) != 2 || ks[0].SHA256 != keys.Hash(key) || ks[0].Label != "Second" || ks[0].Hint != key[:8] {
		t.Fatalf("keys = %+v, %v", ks, err)
	}
	_ = st.Close()
	for _, bad := range [][]string{
		{"key", "new", "--person", "nobody"}, // unknown person
		{"key", "revoke", "k_"},              // too short
		{"key", "revoke", "k_nope-nope"},     // no match
	} {
		if _, _, err := cli(t, "", append(bad, "--data-dir", dir)...); err == nil {
			t.Errorf("homed %v: want an error", bad)
		}
	}
	mustCLI(t, "key", "revoke", ks[0].ID, "--data-dir", dir)
	if list := mustCLI(t, "key", "list", "--data-dir", dir); strings.Contains(list, "Second") || !strings.Contains(list, "ChatGPT Site") {
		t.Fatalf("key list after revoke:\n%s", list)
	}

	// Plugins: the imported one, a new one, a refused one, and a removal that keeps the data.
	out = mustCLI(t, "plugin", "add", "--id", "fake", "--data-dir", dir, "--", fakeBin, "serve")
	if !strings.Contains(out, "Added plugin fake") {
		t.Fatalf("plugin add: %s", out)
	}
	for _, bad := range [][]string{
		{"plugin", "add", "--data-dir", dir, "--id", "fake", "--", "sh"},                    // exists
		{"plugin", "add", "--data-dir", dir, "--id", "TV", "--", "sh"},                      // invalid id
		{"plugin", "add", "--data-dir", dir, "--id", "kettle"},                              // no command
		{"plugin", "add", "--data-dir", dir, "--id", "kettle", "--", "/no/such/homie-tool"}, // missing binary
		{"plugin", "remove", "--data-dir", dir, "kettle"},                                   // unknown
	} {
		if _, _, err := cli(t, "", bad...); err == nil {
			t.Errorf("homed %v: want an error", bad)
		}
	}
	if list := mustCLI(t, "plugin", "list", "--data-dir", dir); !strings.Contains(list, "fake") || !strings.Contains(list, "tv") {
		t.Fatalf("plugin list:\n%s", list)
	}
	writeFile(t, filepath.Join(dir, "plugins", "fake", "data", "state.json"), "{}")
	if out := mustCLI(t, "plugin", "remove", "fake", "--data-dir", dir); !strings.Contains(out, "data stays") {
		t.Fatalf("plugin remove: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins", "fake", "data", "state.json")); err != nil {
		t.Fatalf("the plugin data went: %v", err)
	}
	if list := mustCLI(t, "plugin", "list", "--data-dir", dir); strings.Contains(list, "fake") {
		t.Fatalf("plugin list after remove:\n%s", list)
	}

	// The setup link needs the relay domain (the passkeys belong to it).
	if _, _, err := cli(t, "", "admin", "setup-link", "--data-dir", dir); err == nil || !strings.Contains(err.Error(), "relay") {
		t.Fatalf("setup-link without a relay: %v", err)
	}
	connectRelay(t, dir)
	out, errOut, err = cli(t, "", "admin", "setup-link", "--data-dir", dir)
	if err != nil || !strings.HasPrefix(out, "https://homie.test/admin/setup#t=") || strings.Count(out, "\n") != 1 ||
		!strings.Contains(errOut, "works once") {
		t.Fatalf("setup-link: %v %q %q", err, out, errOut)
	}
	out, errOut, err = cli(t, "", "admin", "reset-access", "--data-dir", dir)
	if err != nil || !strings.HasPrefix(out, "https://homie.test/admin/setup#t=") || !strings.Contains(errOut, "Removed 0 passkeys and 0 sessions") {
		t.Fatalf("reset-access: %v %q %q", err, out, errOut)
	}
	recs, err := openDB(t, dir).Audit().Query(context.Background(), store.AuditFilter{Kind: store.AuditAdmin, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, r := range recs {
		actions = append(actions, r.Action)
	}
	for _, want := range []string{"key.create", "key.revoke", "plugin.install", "plugin.remove", "setup.link", "access.reset"} {
		if !strings.Contains(strings.Join(actions, " "), want) {
			t.Errorf("audit lacks %s: %v", want, actions)
		}
	}
}

func TestKeyNewToKeychain(t *testing.T) {
	dir := t.TempDir()
	_, hash := keys.Generate()
	writeFile(t, filepath.Join(dir, config.FileName), romanYAML(hash))
	// A fake `security` that records its arguments and its stdin.
	fake := filepath.Join(t.TempDir(), "security")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$0.args\"\ncat > \"$0.stdin\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil { //nolint:gosec // an executable test script
		t.Fatal(err)
	}
	old := securityCmd
	securityCmd = fake
	t.Cleanup(func() { securityCmd = old })

	out, errOut, err := cli(t, "", "key", "new", "--keychain", "homie-key", "--data-dir", dir)
	if err != nil {
		t.Fatalf("%v: %s", err, errOut)
	}
	args := readFile(t, fake+".args")
	line := strings.TrimSpace(readFile(t, fake+".stdin"))
	if strings.TrimSpace(args) != "-i" {
		t.Fatalf("security args = %q", args)
	}
	const prefix = `add-generic-password -U -s "homie-key" -a "roman" -w "`
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, `"`) {
		t.Fatalf("security stdin = %q", line)
	}
	key := strings.TrimSuffix(strings.TrimPrefix(line, prefix), `"`)
	if strings.Contains(out+errOut, key) {
		t.Fatal("the key was printed")
	}
	ks, err := openDB(t, dir).Keys().List(context.Background())
	if err != nil || len(ks) != 2 || (ks[0].SHA256 != keys.Hash(key) && ks[1].SHA256 != keys.Hash(key)) {
		t.Fatalf("keys = %+v, %v", ks, err)
	}

	// A Keychain failure takes the new key back.
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho boom\nexit 45\n"), 0o700); err != nil { //nolint:gosec // test script
		t.Fatal(err)
	}
	if _, _, err := cli(t, "", "key", "new", "--keychain", "homie-key", "--data-dir", dir); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if ks, err := openDB(t, dir).Keys().List(context.Background()); err != nil || len(ks) != 2 {
		t.Fatalf("keys after a failed Keychain write: %+v, %v", ks, err)
	}
}

func TestHostGuard(t *testing.T) {
	var mu sync.Mutex
	domain := "homie.test"
	current := func() string {
		mu.Lock()
		defer mu.Unlock()
		return domain
	}
	h := hostGuard(current, 8080, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	check := func(host string, want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Host %q: status %d, want %d", host, w.Code, want)
		}
	}
	for host, want := range map[string]int{
		"homie.test":           http.StatusTeapot,
		"HOMIE.test":           http.StatusTeapot,
		"homie.test:18443":     http.StatusTeapot,
		"homie.test.":          http.StatusTeapot,
		"127.0.0.1:8080":       http.StatusTeapot,
		"localhost:8080":       http.StatusTeapot,
		"[::1]:8080":           http.StatusTeapot,
		"127.0.0.1:9090":       http.StatusMisdirectedRequest,
		"127.0.0.1":            http.StatusMisdirectedRequest,
		"evil.test":            http.StatusMisdirectedRequest,
		"homie.test.evil.test": http.StatusMisdirectedRequest,
		"":                     http.StatusMisdirectedRequest,
	} {
		check(host, want)
	}
	// A relay connected at run time is accepted at once; without one only the loopback names pass.
	mu.Lock()
	domain = "other.test"
	mu.Unlock()
	check("other.test", http.StatusTeapot)
	check("homie.test", http.StatusMisdirectedRequest)
	mu.Lock()
	domain = ""
	mu.Unlock()
	check("other.test", http.StatusMisdirectedRequest)
	check("127.0.0.1:8080", http.StatusTeapot)
}

func TestParseDevOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:5173":   "http://localhost:5173",
		"http://localhost:5173/":  "http://localhost:5173",
		"https://localhost:8443":  "https://localhost:8443",
		"http://localhost":        "http://localhost",
		"http://127.0.0.1:5173":   "",
		"http://homie.test:5173":  "",
		"ftp://localhost:21":      "",
		"http://localhost:1/x":    "",
		"http://u@localhost:5173": "",
		"localhost:5173":          "",
	} {
		rp, err := parseDevOrigin(in)
		if want == "" {
			if err == nil {
				t.Errorf("parseDevOrigin(%q) accepted", in)
			}
			continue
		}
		if err != nil || rp.Origin != want || rp.ID != "localhost" {
			t.Errorf("parseDevOrigin(%q) = %+v, %v", in, rp, err)
		}
	}
}

func TestMatchKey(t *testing.T) {
	list := []keyJSON{{ID: "k_abcdef12"}, {ID: "k_abcdef34"}, {ID: "k_zzzzzz99"}}
	for ref, want := range map[string]string{
		"k_zzzzzz99": "k_zzzzzz99", // whole id
		"k_zzzz":     "k_zzzzzz99", // unique start
		"k_abcdef":   "",           // two match
		"k_ab":       "",           // too short
		"k_nope00":   "",           // none
	} {
		k, err := matchKey(list, ref)
		if (want == "") != (err != nil) || k.ID != want {
			t.Errorf("matchKey(%q) = %q, %v", ref, k.ID, err)
		}
	}
}
