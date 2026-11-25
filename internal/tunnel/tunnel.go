// Package tunnel is how homed reaches the internet: a Cloudflare Tunnel run by cloudflared as homed's child process
// (MCP Gatekeeper's tunnel/cloudflare Supervisor), in one of two modes:
//   - named: the owner's own domain. Connect creates the tunnel with the cloudflared CLI (the login link, the tunnel,
//     the DNS record, config.yml in <data dir>/tunnel); the first-run page and `homed cloudflare connect <host>` run it;
//   - quick: a trycloudflare.com link, no account and no domain, "for trying": its host is random and changes at every
//     start, which kills every passkey and connector of the old host (the passkeys' relying party is the host).
//
// A Manager keeps the child up and says where the hub is (State): the public URL is the passkeys' relying party, the
// OAuth issuer and the MCP URL, so homed rebuilds its gatekeeper stack whenever it changes (Options.OnState). The
// supervisor's probe is the hub's self-check: GET https://<host>/healthz must come back with this hub's
// X-Homie-Instance, so a tunnel that reaches another hub, or none, never counts as connected.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/devXpro/mcp-gatekeeper/core"
	"github.com/devXpro/mcp-gatekeeper/tunnel/cloudflare"
)

// Modes, as homed.yaml's tunnel.mode says them.
const (
	ModeOff   = "off"
	ModeNamed = "named"
	ModeQuick = "quick"
)

// NamePrefix starts the names of the tunnels Connect creates ("homie-home-example-com"). A hub keeps it: it is the name
// of tunnels already created.
const NamePrefix = "homie-"

// InstanceHeader carries the hub's instance id on every answer of the public listener; the probe looks for it.
const InstanceHeader = "X-Homie-Instance"

// probePath is what the probe fetches through the tunnel.
const probePath = "/healthz"

// State is where the hub is reachable: the mode and the public URL (https://<host>, the passkeys' relying party and
// the OAuth issuer). URL and Host are empty while there is none: mode off, or a quick tunnel whose host is not known
// yet.
type State struct {
	Mode string
	URL  string
	Host string
}

// Status is the tunnel as the admin and the first-run page show it.
type Status struct {
	Mode string
	// State is the supervisor's: starting, probing, connected, failing or stopped (mode off).
	State     string
	URL       string // https://<host>; "" while unknown
	Hostname  string // the named tunnel's host
	Since     time.Time
	LastError string
}

// Saver stores the tunnel's mode and host in homed.yaml (config.Update); the Manager calls it when a connect, a
// switch to quick or a disconnect changes them, before the new tunnel starts.
type Saver func(mode, hostname string) error

// Options configures a Manager.
type Options struct {
	// Dir is the tunnel directory, <data dir>/tunnel (0700): a named tunnel's config.yml, credentials.json and
	// tunnel.json, and the child's private HOME.
	Dir string
	// Service is where cloudflared forwards, the hub's loopback listener, e.g. http://127.0.0.1:8080.
	Service string
	// Binary is cloudflared (FindCloudflared).
	Binary string
	// InstanceID is what the probe expects in X-Homie-Instance.
	InstanceID string
	// Save stores the mode and host in homed.yaml. Required for Connect, Quick and Disconnect.
	Save Saver
	// OnState is called when the mode or the public URL changes (a quick tunnel's new host, a connect, a disconnect),
	// on the goroutine that changed it; it should return soon.
	OnState func(State)
	// Runner runs the cloudflared CLI for Connect; nil is cloudflare.ExecRunner{Binary}.
	Runner cloudflare.Runner
	// ProbeClient fetches https://<host>/healthz; nil is a client with a 10 s timeout. Tests route it to the listener.
	ProbeClient *http.Client
	// ChildLog receives cloudflared's output (homed passes <data dir>/logs/cloudflared.log); nil discards it.
	ChildLog io.Writer
	// Logger gets the manager's own events; nil discards them.
	Logger *slog.Logger
	// Clock stamps the statuses and the connect jobs; nil is time.Now.
	Clock core.Clock
}

// supervisor is what the Manager needs of cloudflare.Supervisor; tests use the real one with a fake cloudflared.
type supervisor interface {
	Start(ctx context.Context) error
	Stop()
	Status() cloudflare.Status
}

// Manager runs the tunnel. It is safe for concurrent use.
type Manager struct {
	o      Options
	log    *slog.Logger
	now    core.Clock
	probe  *http.Client
	conn   *cloudflare.Connector
	runCtx context.Context // the context of Start; the children live under it

	mu        sync.Mutex
	mode      string
	hostname  string     // named
	sup       supervisor // nil while mode is off
	startErr  string     // why the child could not start (no cloudflared, no tunnel connected)
	url       string     // the last public URL announced through OnState
	job       *ConnectJob
	jobCancel context.CancelFunc
}

// New checks the options and returns a stopped Manager.
func New(o Options) (*Manager, error) {
	switch {
	case o.Dir == "":
		return nil, errors.New("tunnel: Dir is empty")
	case o.Service == "":
		return nil, errors.New("tunnel: Service is empty")
	}
	if o.Binary == "" {
		o.Binary = "cloudflared"
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.ChildLog == nil {
		o.ChildLog = io.Discard
	}
	runner := o.Runner
	if runner == nil {
		runner = cloudflare.ExecRunner{Binary: o.Binary}
	}
	conn, err := cloudflare.New(cloudflare.Options{Runner: runner, Dir: o.Dir, Service: o.Service, NamePrefix: NamePrefix})
	if err != nil {
		return nil, fmt.Errorf("tunnel: %w", err)
	}
	probe := o.ProbeClient
	if probe == nil {
		probe = &http.Client{Timeout: 10 * time.Second}
	}
	return &Manager{o: o, log: o.Logger, now: o.Clock, probe: probe, conn: conn, mode: ModeOff,
		runCtx: context.Background()}, nil
}

// Start runs the tunnel homed.yaml asks for: mode off runs nothing; named needs the tunnel Connect made for hostname
// (tunnel.json must name it); quick starts a quick tunnel. A child that cannot start (cloudflared missing, no tunnel
// connected) is an error, and the status says why; homed then runs without the tunnel. The children run until ctx
// ends or Stop.
func (m *Manager) Start(ctx context.Context, mode, hostname string) error {
	m.mu.Lock()
	m.runCtx = ctx
	m.mu.Unlock()
	switch mode {
	case "", ModeOff:
		m.mu.Lock()
		m.mode, m.hostname = ModeOff, ""
		m.mu.Unlock()
		return nil
	case ModeNamed:
		rec, err := cloudflare.ReadRecord(m.o.Dir)
		if err != nil {
			return m.failStart(ModeNamed, hostname, err)
		}
		if rec.Hostname != hostname {
			return m.failStart(ModeNamed, hostname, fmt.Errorf("the tunnel directory %s serves %q, homed.yaml says %q: "+
				"connect it again with: homed cloudflare connect %s", m.o.Dir, rec.Hostname, hostname, hostname))
		}
		return m.run(ModeNamed, hostname)
	case ModeQuick:
		return m.run(ModeQuick, "")
	default:
		return fmt.Errorf("tunnel: unknown mode %q", mode)
	}
}

// failStart records why the child of mode could not start.
func (m *Manager) failStart(mode, hostname string, err error) error {
	m.mu.Lock()
	m.mode, m.hostname, m.sup, m.startErr = mode, hostname, nil, err.Error()
	m.mu.Unlock()
	m.log.Error("the tunnel did not start", "mode", mode, "err", err)
	return fmt.Errorf("tunnel: %w", err)
}

// run starts a supervisor of mode, replacing the running one. Named mode announces its URL at once; quick mode when
// cloudflared gives it the host.
func (m *Manager) run(mode, hostname string) error {
	m.mu.Lock()
	old := m.sup
	m.sup = nil
	ctx := m.runCtx
	m.mu.Unlock()
	if old != nil {
		old.Stop()
	}
	sup, err := cloudflare.NewSupervisor(cloudflare.SupervisorOptions{
		Mode: cloudflare.Mode(mode), Binary: m.o.Binary, Dir: m.o.Dir, Service: m.o.Service, Probe: m.Probe,
		OnURL: func(url string) { m.announce(url) }, Log: m.o.ChildLog, Logger: m.log, Clock: m.o.Clock,
	})
	if err == nil {
		err = sup.Start(ctx)
	}
	if err != nil {
		return m.failStart(mode, hostname, err)
	}
	m.mu.Lock()
	m.mode, m.hostname, m.sup, m.startErr = mode, hostname, sup, ""
	m.mu.Unlock()
	m.log.Info("the tunnel started", "mode", mode, "hostname", hostname)
	if mode == ModeNamed {
		m.announce("https://" + hostname)
	}
	return nil
}

// announce tells OnState about a new public URL ("" for none), once per change.
func (m *Manager) announce(url string) {
	m.mu.Lock()
	changed := url != m.url
	m.url = url
	st := m.stateLocked()
	m.mu.Unlock()
	if changed {
		m.log.Info("the hub's public URL changed", "mode", st.Mode, "url", url)
		if m.o.OnState != nil {
			m.o.OnState(st)
		}
	}
}

func (m *Manager) stateLocked() State {
	st := State{Mode: m.mode, URL: m.url}
	st.Host = strings.TrimPrefix(m.url, "https://")
	return st
}

// State returns where the hub is reachable now.
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stateLocked()
}

// Status returns the tunnel's status now.
func (m *Manager) Status() Status {
	m.mu.Lock()
	mode, hostname, sup, startErr := m.mode, m.hostname, m.sup, m.startErr
	m.mu.Unlock()
	st := Status{Mode: mode, Hostname: hostname, State: string(cloudflare.StateStopped)}
	switch {
	case sup != nil:
		s := sup.Status()
		st.State, st.URL, st.Since, st.LastError = string(s.State), s.URL, s.Since, s.LastError
	case startErr != "":
		st.State, st.LastError = string(cloudflare.StateFailing), startErr
		if hostname != "" {
			st.URL = "https://" + hostname
		}
	}
	return st
}

// Probe is the self-check: GET publicURL/healthz must answer 200 with this hub's instance id.
func (m *Manager) Probe(ctx context.Context, publicURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, publicURL+probePath, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := m.probe.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	switch got := resp.Header.Get(InstanceHeader); {
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%s answered %s", publicURL, resp.Status)
	case got == "":
		return fmt.Errorf("%s answered without %s: something else serves that address", publicURL, InstanceHeader)
	case got != m.o.InstanceID:
		return fmt.Errorf("%s reaches another Homie hub (instance %s)", publicURL, got)
	}
	return nil
}

// Check runs the probe now against the current URL and returns the status with its result: the supervisor's own
// probe stops once it passed, so this is how the admin re-checks a tunnel that is up.
func (m *Manager) Check(ctx context.Context) Status {
	st := m.Status()
	if st.URL == "" {
		return st
	}
	if err := m.Probe(ctx, st.URL); err != nil {
		st.LastError = "the public URL does not reach this hub: " + err.Error()
		if st.State == string(cloudflare.StateConnected) {
			st.State = string(cloudflare.StateProbing)
		}
	}
	return st
}

// Restart starts the child again (the admin's reconnect). A quick tunnel gets a new host: every passkey and connector
// of the old one stops working.
func (m *Manager) Restart() error {
	m.mu.Lock()
	mode, hostname := m.mode, m.hostname
	m.mu.Unlock()
	switch mode {
	case ModeNamed, ModeQuick:
		if mode == ModeQuick {
			m.announce("") // the old host is gone with the old child
		}
		return m.run(mode, hostname)
	}
	return fmt.Errorf("tunnel: no tunnel is configured: %w", core.ErrConflict)
}

// Quick switches a hub without a tunnel to a quick tunnel and stores the mode. A hub whose domain is connected keeps
// it (core.ErrConflict): disconnect it first.
func (m *Manager) Quick() error {
	m.mu.Lock()
	mode, busy := m.mode, m.job != nil && m.job.State == JobRunning
	m.mu.Unlock()
	switch {
	case mode == ModeQuick:
		return nil
	case mode == ModeNamed:
		return fmt.Errorf("tunnel: a domain is connected; disconnect it first (homed cloudflare disconnect): %w", core.ErrConflict)
	case busy:
		return fmt.Errorf("tunnel: a domain is being connected: %w", core.ErrConflict)
	}
	if err := m.save(ModeQuick, ""); err != nil {
		return err
	}
	return m.run(ModeQuick, "")
}

// Disconnect stops the tunnel and stores mode off. The tunnel directory stays, so a later connect of the same domain
// reuses the tunnel.
func (m *Manager) Disconnect() error {
	m.mu.Lock()
	sup := m.sup
	m.sup, m.startErr = nil, ""
	m.mu.Unlock()
	if sup != nil {
		sup.Stop()
	}
	if err := m.save(ModeOff, ""); err != nil {
		return err
	}
	m.mu.Lock()
	m.mode, m.hostname = ModeOff, ""
	m.mu.Unlock()
	m.announce("")
	return nil
}

func (m *Manager) save(mode, hostname string) error {
	if m.o.Save == nil {
		return errors.New("tunnel: this manager cannot store homed.yaml")
	}
	if err := m.o.Save(mode, hostname); err != nil {
		return fmt.Errorf("tunnel: store the mode in homed.yaml: %w", err)
	}
	return nil
}

// Stop ends the child; the mode stays.
func (m *Manager) Stop() {
	m.mu.Lock()
	sup, cancel := m.sup, m.jobCancel
	m.sup = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if sup != nil {
		sup.Stop()
	}
}

// FindCloudflared returns the cloudflared to run: configured (homed.yaml tunnel.cloudflared: a path, or a name looked
// up in PATH), else "cloudflared" in PATH, else the one next to the homed binary (where the Linux installer puts it).
func FindCloudflared(configured string) (string, error) {
	if configured != "" {
		p, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("tunnel.cloudflared %q: %w", configured, err)
		}
		return p, nil
	}
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p, nil
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "cloudflared")
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p, nil
		}
	}
	return "", errors.New("cloudflared is not installed: install it (the installer does: install/home.sh), or set " +
		"tunnel.cloudflared in homed.yaml")
}
