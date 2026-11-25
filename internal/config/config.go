// Package config reads and writes the hub's data dir: homed.yaml, the instance id, the instance lock and the paths of
// the other files homed keeps there.
//
// homed.yaml holds only what homed needs before its database opens: listen, setup_listen and the tunnel (how homed
// reaches the internet through Cloudflare). Plugins, people, the home name and the language live in homie.db.
//
//	listen: 127.0.0.1:8080        # loopback only; cloudflared, run by homed, forwards to it
//	setup_listen: 0.0.0.0:8090    # the first-run page; default: the home network while no tunnel is configured
//	tunnel:
//	  mode: named                 # named (your domain), quick (a trycloudflare.com link, for trying) or off
//	  hostname: home.example.com  # named only; written by `homed cloudflare connect`
//	  cloudflared: /usr/local/bin/cloudflared   # optional; default: PATH, then next to homed
//
// homed.yaml is edited in place: Update rewrites only the top-level sections that changed, so comments and the order
// of untouched sections survive a `homed cloudflare connect`.
//
// MigrateV01 turns a v0.1 data dir into this one: v0.1's relay section goes, and so do the phase-1 sections of a v0.1
// that never ran (its homed.yaml is kept as homed.yaml.v0.1), the relay token file is deleted and a tunnel section is
// added.
package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"go.yaml.in/yaml/v3"
)

// Files in the data dir.
const (
	FileName       = "homed.yaml"
	InstanceIDFile = "instance-id"
	// SocketFile is the admin socket (the gatekeeper's adminsock): the homed CLI's way into the running hub.
	SocketFile = "homed.sock"
	// DBFile is the database: the gatekeeper's tables and the hub's (plugins, settings, activity).
	DBFile = "homie.db"
	// OldDBFile is v0.1's database; homed imports it once and renames it to OldDBFile + ".v0.1".
	OldDBFile = "homed.db"
	// TunnelDir holds the Cloudflare tunnel: config.yml, credentials.json and tunnel.json of a named tunnel, and the
	// private HOME of the cloudflared child.
	TunnelDir = "tunnel"
	// LogsDir holds the plugins' logs and cloudflared.log.
	LogsDir = "logs"
	// InstanceLockFile is flocked by the running homed for its whole life (LockInstance).
	InstanceLockFile = ".homed.lock"
	// V01Backup receives v0.1's homed.yaml as it was before MigrateV01 rewrote it.
	V01Backup = FileName + ".v0.1"
	// relayTokenFile was v0.1's relay secret; MigrateV01 deletes it.
	relayTokenFile = "relay-token"
	lockFile       = ".homed.yaml.lock"
)

// DefaultListen is the hub's address when homed.yaml sets none.
const DefaultListen = "127.0.0.1:8080"

// Tunnel modes.
const (
	// TunnelOff: no tunnel; the hub is not reachable from the internet (the first-run page connects one).
	TunnelOff = "off"
	// TunnelNamed: the owner's own domain through a named Cloudflare Tunnel (`homed cloudflare connect <host>`).
	TunnelNamed = "named"
	// TunnelQuick: a Cloudflare quick tunnel, a random trycloudflare.com link that changes at every restart; for trying.
	TunnelQuick = "quick"
)

// Config is homed.yaml.
type Config struct {
	Listen string `yaml:"listen,omitempty"`
	// SetupListen is the first-run page's address; empty means firstrun.DefaultListen (0.0.0.0:8090 while no tunnel
	// is configured, 127.0.0.1:8090 with one).
	SetupListen string `yaml:"setup_listen,omitempty"`
	Tunnel      Tunnel `yaml:"tunnel,omitempty"`
	// Relay is v0.1's relay section, read only so that an old file still loads: homed ignores it (with a warning,
	// HasRelay) and MigrateV01 removes it.
	Relay any `yaml:"relay,omitempty"`
	// HomeName, Plugins and People are v0.1's phase-1 sections. v0.1 moved them into its database at its first start,
	// so only a v0.1 data dir that never ran still has them. They are read only so that such a file still loads: homed
	// ignores them (HasPhase1) and MigrateV01 removes them, keeping them in homed.yaml.v0.1.
	HomeName any `yaml:"home_name,omitempty"`
	Plugins  any `yaml:"plugins,omitempty"`
	People   any `yaml:"people,omitempty"`
}

// Tunnel is how homed reaches the internet: cloudflared, run by homed as a child process.
type Tunnel struct {
	// Mode is TunnelNamed, TunnelQuick or TunnelOff; empty is off.
	Mode string `yaml:"mode,omitempty"`
	// Hostname is the named tunnel's public host, e.g. home.example.com: the passkeys' relying party and the OAuth
	// issuer. Required in named mode.
	Hostname string `yaml:"hostname,omitempty"`
	// Cloudflared is the cloudflared binary: an absolute path or a name looked up in PATH. Empty: "cloudflared" in
	// PATH, then next to the homed binary.
	Cloudflared string `yaml:"cloudflared,omitempty"`
}

// ModeOrOff is Mode, with empty read as TunnelOff.
func (t Tunnel) ModeOrOff() string {
	if t.Mode == "" {
		return TunnelOff
	}
	return t.Mode
}

// Configured reports whether a tunnel is set up (named or quick). While it is not, the first-run page may listen on
// the home network.
func (t Tunnel) Configured() bool { return t.ModeOrOff() != TunnelOff }

var hostnameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// CheckHostname reports whether host is a full lower-case host name, e.g. home.example.com.
func CheckHostname(host string) error {
	if len(host) > 253 || !hostnameRe.MatchString(host) {
		return fmt.Errorf("%q is not a full host name in lower case, e.g. home.example.com", host)
	}
	return nil
}

// ListenAddr returns Listen or DefaultListen.
func (c *Config) ListenAddr() string {
	if c.Listen != "" {
		return c.Listen
	}
	return DefaultListen
}

// HasRelay reports whether homed.yaml still has v0.1's relay section, which homed ignores.
func (c *Config) HasRelay() bool { return c.Relay != nil }

// HasPhase1 reports whether homed.yaml still has v0.1's phase-1 sections (home_name, plugins, people), which homed
// ignores.
func (c *Config) HasPhase1() bool { return c.HomeName != nil || c.Plugins != nil || c.People != nil }

// Validate checks the whole config.
func (c *Config) Validate() error {
	if c.Listen != "" {
		if _, err := LoopbackPort(c.Listen); err != nil {
			return err
		}
	}
	if c.SetupListen != "" {
		if err := checkSetupListen(c.SetupListen); err != nil {
			return err
		}
	}
	t := c.Tunnel
	switch t.Mode {
	case "", TunnelOff, TunnelQuick:
	case TunnelNamed:
		if t.Hostname == "" {
			return errors.New("tunnel: mode named needs a hostname; connect one with: homed cloudflare connect <host>")
		}
	default:
		return fmt.Errorf("tunnel: mode %q is not %s, %s or %s", t.Mode, TunnelNamed, TunnelQuick, TunnelOff)
	}
	if t.Hostname != "" {
		if err := CheckHostname(t.Hostname); err != nil {
			return fmt.Errorf("tunnel: hostname: %w", err)
		}
	}
	if b := t.Cloudflared; b != "" && !filepath.IsAbs(b) && strings.ContainsRune(b, filepath.Separator) {
		return fmt.Errorf("tunnel: cloudflared %q must be an absolute path or a name looked up in PATH", b)
	}
	return nil
}

// LoopbackPort checks that addr is 127.0.0.1:<port> or localhost:<port> and returns the port. cloudflared forwards to
// 127.0.0.1:<port> and the Host guard knows only that address, so other loopback addresses (127.0.0.2, [::1]) would
// leave the hub unreachable through the tunnel; nothing else should reach the hub directly.
func LoopbackPort(addr string) (int, error) {
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("listen %q: %w", addr, err)
	}
	if host != "127.0.0.1" && host != "localhost" {
		return 0, fmt.Errorf("listen %q: the hub serves on loopback only: 127.0.0.1 or localhost (e.g. %s)", addr, DefaultListen)
	}
	port, err := strconv.Atoi(p)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("listen %q: bad port", addr)
	}
	return port, nil
}

// checkSetupListen accepts "host:port" with any host (the first-run page is meant for the home network while no tunnel
// is configured, so 0.0.0.0 is the default then) and a real port.
func checkSetupListen(addr string) error {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("setup_listen %q: %w", addr, err)
	}
	if port, err := strconv.Atoi(p); err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf("setup_listen %q: bad port", addr)
	}
	return nil
}

// ResolveDataDir applies the flag value, then HOMED_DATA_DIR, then the platform default. A dir it has to create is
// made 0700. An existing dir keeps its mode, so a mistaken --data-dir (a home dir, /var/lib) is never chmodded; the
// one exception is the platform default dir, which is homed's own and is tightened back to 0700. DataDirWarning
// reports an existing dir that is open to others.
func ResolveDataDir(flagValue string) (string, error) {
	dir := flagValue
	if dir == "" {
		dir = os.Getenv("HOMED_DATA_DIR")
	}
	if dir == "" {
		var err error
		if dir, err = DefaultDataDir(); err != nil {
			return "", err
		}
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("create data dir: %w", err)
		}
		return dir, secureDir(dir) // a umask can only narrow 0700, but say it once
	case err != nil:
		return "", fmt.Errorf("data dir: %w", err)
	case !st.IsDir():
		return "", fmt.Errorf("data dir %s is not a directory", dir)
	}
	if def, err := DefaultDataDir(); err == nil && def == dir && st.Mode().Perm()&0o077 != 0 {
		return dir, secureDir(dir)
	}
	return dir, nil
}

func secureDir(dir string) error {
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // a directory needs the x bit; 0700 is owner-only
		return fmt.Errorf("secure data dir: %w", err)
	}
	return nil
}

// DataDirWarning returns a warning when the data dir is readable or writable by group or others, "" otherwise. Its
// secret files are 0600 either way, but others could list them or, with write access, swap them.
func DataDirWarning(dir string) string {
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm()&0o077 == 0 {
		return ""
	}
	return fmt.Sprintf("data dir %s is open to other users (mode %o); homed does not change the mode of a dir it did "+
		"not create. Run: chmod 700 %s", dir, st.Mode().Perm(), dir)
}

// DefaultDataDir is ~/Library/Application Support/homie on macOS and $XDG_DATA_HOME/homie (or
// ~/.local/share/homie) elsewhere.
func DefaultDataDir() (string, error) {
	if runtime.GOOS == "darwin" {
		base, err := os.UserConfigDir() // ~/Library/Application Support
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "homie"), nil
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "homie"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "homie"), nil
}

// Load reads and validates homed.yaml in dir. A missing file is an empty config. Unknown keys are errors, so a typo
// does not silently switch something off; v0.1's relay and phase-1 sections are the exceptions (HasRelay, HasPhase1).
func Load(dir string) (*Config, error) {
	raw, err := readConfig(dir)
	if err != nil {
		return nil, err
	}
	return parse(raw)
}

func readConfig(dir string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(dir, FileName)) //nolint:gosec // the hub's own data dir
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return raw, err
}

func parse(raw []byte) (*Config, error) {
	c := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	return c, nil
}

// Update loads homed.yaml under an exclusive lock, lets fn change it, validates the result and writes it back
// atomically (0600). Only the top-level sections fn changed are rewritten; the rest of the file, comments included,
// stays as it was. When fn returns an error nothing is written.
func Update(dir string, fn func(*Config) error) error {
	unlock, err := lock(dir)
	if err != nil {
		return err
	}
	defer unlock()
	return updateLocked(dir, fn)
}

// MigrateResult says what MigrateV01 did.
type MigrateResult struct {
	// Backup is where v0.1's homed.yaml was copied; "" when this call made no copy.
	Backup string
	// RelayRemoved is true when the relay section was removed from homed.yaml.
	RelayRemoved bool
	// Phase1Removed is true when v0.1's phase-1 sections (home_name, plugins, people) were removed from homed.yaml;
	// they are in the backup.
	Phase1Removed bool
	// TokenDeleted is true when the relay token file was deleted.
	TokenDeleted bool
}

// MigrateV01 turns a v0.1 data dir into a v0.2 one; homed calls it at every start, before Load, and it is a no-op once
// done. When homed.yaml has v0.1's relay section, or its phase-1 sections (a v0.1 data dir that never ran):
//  1. homed.yaml as it is goes to homed.yaml.v0.1 (0600; an existing copy is kept, never overwritten);
//  2. homed.yaml is rewritten without them and with a tunnel section (mode off), and, when it had a relay and set no
//     setup_listen, with setup_listen 127.0.0.1:8090: the home had a relay, so its first-run page stays off the home
//     network;
//  3. the relay token file is deleted (also when homed.yaml had no relay left).
//
// A crash between the steps leaves a state the next call finishes.
func MigrateV01(dir string) (MigrateResult, error) {
	var res MigrateResult
	unlock, err := lock(dir)
	if err != nil {
		return res, err
	}
	defer unlock()
	raw, err := readConfig(dir)
	if err != nil {
		return res, err
	}
	c, err := parse(raw)
	if err != nil {
		return res, err
	}
	if relay, phase1 := c.HasRelay(), c.HasPhase1(); relay || phase1 {
		backup := filepath.Join(dir, V01Backup)
		switch _, err := os.Lstat(backup); {
		case errors.Is(err, fs.ErrNotExist):
			if err := WriteFileAtomic(backup, raw, 0o600); err != nil {
				return res, fmt.Errorf("back up %s: %w", FileName, err)
			}
			res.Backup = backup
		case err != nil:
			return res, err
		}
		err = updateLocked(dir, func(c *Config) error {
			c.Relay, c.HomeName, c.Plugins, c.People = nil, nil, nil, nil
			if c.Tunnel.Mode == "" {
				c.Tunnel.Mode = TunnelOff
			}
			if relay && c.SetupListen == "" {
				c.SetupListen = "127.0.0.1:8090"
			}
			return nil
		})
		if err != nil {
			return res, err
		}
		res.RelayRemoved, res.Phase1Removed = relay, phase1
	}
	switch err := os.Remove(filepath.Join(dir, relayTokenFile)); {
	case err == nil:
		res.TokenDeleted = true
	case !errors.Is(err, fs.ErrNotExist):
		return res, fmt.Errorf("delete the relay token: %w", err)
	}
	return res, nil
}

// updateLocked is Update without taking the lock.
func updateLocked(dir string, fn func(*Config) error) error {
	raw, err := readConfig(dir)
	if err != nil {
		return err
	}
	before, err := parse(raw)
	if err != nil {
		return err
	}
	after, err := parse(raw) // a deep copy for fn to change
	if err != nil {
		return err
	}
	if err := fn(after); err != nil {
		return err
	}
	if err := after.Validate(); err != nil {
		return err
	}

	var doc yaml.Node
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("%s: %w", FileName, err)
		}
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s: the top level must be a mapping", FileName)
	}
	if err := mergeSections(doc.Content[0], before, after); err != nil {
		return err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return WriteFileAtomic(filepath.Join(dir, FileName), buf.Bytes(), 0o600)
}

// mergeSections rewrites, in the mapping root, every top-level section that differs between before and after.
func mergeSections(root *yaml.Node, before, after *Config) error {
	bv, av := reflect.ValueOf(*before), reflect.ValueOf(*after)
	t := bv.Type()
	for i := range t.NumField() {
		if reflect.DeepEqual(bv.Field(i).Interface(), av.Field(i).Interface()) {
			continue
		}
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if av.Field(i).IsZero() {
			removeKey(root, name)
			continue
		}
		var n yaml.Node
		if err := n.Encode(av.Field(i).Interface()); err != nil {
			return err
		}
		setKey(root, name, &n)
	}
	return nil
}

func setKey(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

func removeKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// lock takes an exclusive advisory lock that serializes writers of homed.yaml (two CLI commands at once).
func lock(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // the hub's own data dir
	if err != nil {
		return nil, err
	}
	fd := int(f.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", FileName, err)
	}
	return func() {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// ErrAlreadyRunning means another homed holds the instance lock of the data dir.
var ErrAlreadyRunning = errors.New("homed is already running on this data dir")

// LockInstance takes the data dir's instance lock (<dir>/.homed.lock, flock, non-blocking) and returns its release
// func. The daemon holds it for its whole life: the kernel drops it when the process ends, crash included, so there
// is no stale-lock cleanup and no window in which two daemons both see "nobody runs here".
func LockInstance(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, InstanceLockFile), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // the hub's own data dir
	if err != nil {
		return nil, err
	}
	fd := int(f.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (%s)", ErrAlreadyRunning, dir)
		}
		return nil, fmt.Errorf("lock %s: %w", InstanceLockFile, err)
	}
	return func() {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// InstanceID returns the hub's persistent random id, creating it on first use. Every answer of the public listener
// carries it (X-Homie-Instance), so the tunnel's self-check can tell that https://<host> reaches this hub.
func InstanceID(dir string) (string, error) {
	path := filepath.Join(dir, InstanceIDFile)
	raw, err := os.ReadFile(path) //nolint:gosec // the hub's own data dir
	if err == nil {
		if id := strings.TrimSpace(string(raw)); id != "" {
			return id, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error since Go 1.24
	id := hex.EncodeToString(b)
	if err := WriteFileAtomic(path, []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

// WriteFileAtomic writes through a temp file in the same dir and renames it over path.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op after the rename
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
