// Package config reads and writes the hub's data dir (PLAN.md §5.6): homed.yaml, the relay token, the instance id
// and the paths of the other files homed keeps there.
//
// From phase 2 on homed.yaml holds only what homed needs before its database opens: listen, setup_listen and the relay
// domain. People, their key hashes, plugins and the home name live in homed.db. A phase 1 homed.yaml still has those
// sections; homed imports them into the database once (store.ImportPhase1) and then ArchivePhase1 moves them out of
// homed.yaml into homed.yaml.phase1, so an edit there can never look like it took effect.
//
// homed.yaml is edited in place: Update rewrites only the top-level sections that changed, so comments and the order
// of untouched sections survive a `homed relay connect`.
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
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/devXpro/homie/internal/relay"
)

// Files in the data dir.
const (
	FileName       = "homed.yaml"
	RelayTokenFile = "relay-token"
	InstanceIDFile = "instance-id"
	SocketFile     = "homed.sock"
	// DBFile is the SQLite database (phase 2): people, passkeys, sessions, keys, plugins, audit and settings.
	DBFile = "homed.db"
	// Phase1Backup receives the phase 1 sections of homed.yaml once they are imported (ArchivePhase1).
	Phase1Backup = FileName + ".phase1"
	// AuditDir held the phase 1 audit log (JSON lines); phase 2 writes the audit to DBFile.
	AuditDir = "audit"
	// InstanceLockFile is flocked by the running homed for its whole life (LockInstance).
	InstanceLockFile = ".homed.lock"
	lockFile         = ".homed.yaml.lock"
)

// DefaultListen is the hub's address when homed.yaml sets none.
const DefaultListen = "127.0.0.1:8080"

// Config is homed.yaml.
type Config struct {
	Listen string `yaml:"listen,omitempty"`
	// SetupListen is the first-run page's address; empty means firstrun.DefaultListen (0.0.0.0:8090 while there is
	// no relay, 127.0.0.1:8090 with one). The page runs only until the home has an owner with a passkey.
	SetupListen string `yaml:"setup_listen,omitempty"`
	Relay       Relay  `yaml:"relay,omitempty"`

	// The phase 1 sections: read once by the import into homed.db, then moved out by ArchivePhase1.
	HomeName string   `yaml:"home_name,omitempty"`
	Plugins  []Plugin `yaml:"plugins,omitempty"`
	People   []Person `yaml:"people,omitempty"`
}

// Relay is the relay the hub connects to; the token lives in the relay-token file.
type Relay struct {
	Domain string `yaml:"domain,omitempty"`
}

// Plugin is one plugin executable.
type Plugin struct {
	ID      string   `yaml:"id"`
	Command []string `yaml:"command,flow"`
}

// Person may call assistant tools with their keys.
type Person struct {
	ID      string   `yaml:"id"`
	Name    string   `yaml:"name"`
	Plugins []string `yaml:"plugins,flow"` // plugin ids, or ["*"] for all
	Keys    []Key    `yaml:"keys,omitempty"`
}

// Key is one person key, stored as its hash.
type Key struct {
	SHA256  string    `yaml:"sha256"`
	Label   string    `yaml:"label,omitempty"`
	Created time.Time `yaml:"created,omitempty"`
}

var (
	pluginIDRe = regexp.MustCompile(`^[a-z][a-z0-9]{1,15}$`)
	personIDRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	sha256Re   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ListenAddr returns Listen or DefaultListen.
func (c *Config) ListenAddr() string {
	if c.Listen != "" {
		return c.Listen
	}
	return DefaultListen
}

// Person returns the person with the id, or nil.
func (c *Config) Person(id string) *Person {
	for i := range c.People {
		if c.People[i].ID == id {
			return &c.People[i]
		}
	}
	return nil
}

// Plugin returns the plugin with the id, or nil.
func (c *Config) Plugin(id string) *Plugin {
	for i := range c.Plugins {
		if c.Plugins[i].ID == id {
			return &c.Plugins[i]
		}
	}
	return nil
}

// HasPhase1 reports whether homed.yaml still has a phase 1 section: home_name, plugins or people.
func (c *Config) HasPhase1() bool {
	return c.HomeName != "" || len(c.Plugins) > 0 || len(c.People) > 0
}

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
	if c.Relay.Domain != "" {
		if err := relay.ValidateDomain(c.Relay.Domain); err != nil {
			return fmt.Errorf("relay: %w", err)
		}
	}
	seen := map[string]bool{}
	for i, p := range c.Plugins {
		if !pluginIDRe.MatchString(p.ID) {
			return fmt.Errorf("plugins[%d]: id %q must match %s", i, p.ID, pluginIDRe)
		}
		if seen[p.ID] {
			return fmt.Errorf("plugins: id %q is listed twice", p.ID)
		}
		seen[p.ID] = true
		if len(p.Command) == 0 || p.Command[0] == "" {
			return fmt.Errorf("plugin %q: command is empty", p.ID)
		}
	}
	people := map[string]bool{}
	hashes := map[string]bool{}
	for i, p := range c.People {
		if !personIDRe.MatchString(p.ID) {
			return fmt.Errorf("people[%d]: id %q must match %s", i, p.ID, personIDRe)
		}
		if people[p.ID] {
			return fmt.Errorf("people: id %q is listed twice", p.ID)
		}
		people[p.ID] = true
		if strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("person %q: name is empty", p.ID)
		}
		for _, pl := range p.Plugins {
			if pl != "*" && !pluginIDRe.MatchString(pl) {
				return fmt.Errorf("person %q: plugin %q is neither \"*\" nor a plugin id", p.ID, pl)
			}
		}
		for j, k := range p.Keys {
			if !sha256Re.MatchString(k.SHA256) {
				return fmt.Errorf("person %q: keys[%d].sha256 must be 64 lowercase hex characters", p.ID, j)
			}
			if hashes[k.SHA256] {
				return fmt.Errorf("person %q: keys[%d] is listed twice", p.ID, j)
			}
			hashes[k.SHA256] = true
		}
	}
	return nil
}

// LoopbackPort checks that addr is 127.0.0.1:<port> or localhost:<port> and returns the port. The relay forwards to
// 127.0.0.1:<port> and the Host guard knows only that address, so other loopback addresses (127.0.0.2, [::1]) would
// leave the hub unreachable through the relay; nothing else should reach the hub directly.
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

// checkSetupListen accepts "host:port" with any host (the first-run page is meant for the home network, so
// 0.0.0.0 is the default) and a real port.
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
// does not silently switch something off.
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

// ArchivePhase1 moves the phase 1 sections (home_name, plugins, people) out of homed.yaml once the import has copied
// them into homed.db. The file as it was goes to homed.yaml.phase1 (0600; when that exists, a name with the time
// appended, so no earlier backup is overwritten), then homed.yaml is rewritten without those sections; the rest and
// its comments stay. It returns the backup's path, or "" when there was nothing to move.
func ArchivePhase1(dir string) (string, error) {
	unlock, err := lock(dir)
	if err != nil {
		return "", err
	}
	defer unlock()
	raw, err := readConfig(dir)
	if err != nil {
		return "", err
	}
	c, err := parse(raw)
	if err != nil {
		return "", err
	}
	if !c.HasPhase1() {
		return "", nil
	}
	backup := filepath.Join(dir, Phase1Backup)
	switch _, err := os.Lstat(backup); {
	case err == nil:
		backup += "." + time.Now().UTC().Format("20060102T150405.000Z")
	case !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	if err := WriteFileAtomic(backup, raw, 0o600); err != nil {
		return "", fmt.Errorf("back up %s: %w", FileName, err)
	}
	err = updateLocked(dir, func(c *Config) error {
		c.HomeName, c.Plugins, c.People = "", nil, nil
		return nil
	})
	if err != nil {
		return "", err
	}
	return backup, nil
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

// ReadRelayToken reads the relay-token file and refuses one readable by group or others.
func ReadRelayToken(dir string) (string, error) {
	return readSecretFile(filepath.Join(dir, RelayTokenFile))
}

// WriteRelayToken stores the relay token (0600).
func WriteRelayToken(dir, token string) error {
	return WriteFileAtomic(filepath.Join(dir, RelayTokenFile), []byte(token+"\n"), 0o600)
}

func readSecretFile(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is readable by others (mode %o); run: chmod 600 %s", path, st.Mode().Perm(), path)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the hub's own data dir
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// InstanceID returns the hub's persistent random id, creating it on first use.
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
