package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/devXpro/mcp-gatekeeper/core"
	"github.com/devXpro/mcp-gatekeeper/tunnel/cloudflare"
)

// The states of a ConnectJob.
const (
	JobRunning = "running"
	JobOK      = "ok"
	JobFailed  = "failed"
)

// CodeDNSExists is ConnectJob.Code when the hostname already has a DNS record: connecting again with overwriteDNS
// replaces it, so the page asks the owner first.
const CodeDNSExists = "dns_exists"

// ConnectJob is a connect of the owner's domain, as the first-run page and `homed cloudflare connect` follow it.
type ConnectJob struct {
	Hostname string
	State    string // JobRunning, JobOK or JobFailed
	// Step is the step it is at, or failed in: login, tunnel, dns or config (cloudflare.Step*).
	Step string
	// LoginURL is Cloudflare's authorization link, once cloudflared printed it. It is for the operator only: whoever
	// opens it first picks the Cloudflare account; it is never logged.
	LoginURL  string
	Error     string // why it failed, English
	Code      string // a failure a client may act on: CodeDNSExists; empty otherwise
	StartedAt time.Time
	EndedAt   time.Time // zero while running

	from string // the mode when the connect began: a connect that finds another one at its end does not switch
}

// errConnectCancelled is a connect that a disconnect, or homed stopping, cut short: it stores nothing.
var errConnectCancelled = errors.New("the connect was cancelled: the tunnel was disconnected, or homed stopped")

// Connect starts connecting the owner's domain hostname in the background: the Cloudflare login (Job().LoginURL as
// soon as cloudflared prints it), the tunnel, the DNS record (replacing a record only with overwriteDNS) and
// config.yml in the tunnel directory. When it succeeds, the mode named and the host are stored in homed.yaml and the
// named tunnel replaces the running one, a quick tunnel included. One connect runs at a time (core.ErrConflict); a bad
// host name wraps cloudflare.ErrHostname. Whether a domain that is connected may be replaced is the caller's rule.
func (m *Manager) Connect(hostname string, overwriteDNS bool) (ConnectJob, error) {
	m.mu.Lock()
	if m.job != nil && m.job.State == JobRunning {
		m.mu.Unlock()
		return ConnectJob{}, fmt.Errorf("tunnel: a connect is running already: %w", core.ErrConflict)
	}
	if m.o.Save == nil {
		m.mu.Unlock()
		return ConnectJob{}, errors.New("tunnel: this manager cannot store homed.yaml")
	}
	ctx, cancel := context.WithCancel(m.runCtx)
	job := &ConnectJob{Hostname: hostname, State: JobRunning, Step: cloudflare.StepLogin, StartedAt: m.now(), from: m.mode}
	m.job, m.jobCancel = job, cancel
	m.mu.Unlock()

	flow, err := m.conn.Start(ctx, cloudflare.ConnectOptions{Hostname: hostname, OverwriteDNS: overwriteDNS}, m.progress(job))
	if err != nil {
		cancel()
		m.mu.Lock()
		if m.job == job {
			m.job, m.jobCancel = nil, nil
		}
		m.mu.Unlock()
		if errors.Is(err, cloudflare.ErrHostname) {
			return ConnectJob{}, fmt.Errorf("tunnel: %w", err)
		}
		return ConnectJob{}, fmt.Errorf("tunnel: %w: %w", err, core.ErrConflict)
	}
	m.log.Info("connecting a domain through Cloudflare", "hostname", hostname)
	go m.finish(ctx, job, flow, cancel)
	m.mu.Lock()
	defer m.mu.Unlock()
	return *job, nil
}

// progress turns the connector's steps into the job.
func (m *Manager) progress(job *ConnectJob) func(cloudflare.Step) {
	return func(s cloudflare.Step) {
		m.mu.Lock()
		defer m.mu.Unlock()
		job.Step = s.Name
		if s.LoginURL != "" {
			job.LoginURL = s.LoginURL
		}
	}
}

// finish waits for the flow, then stores the mode and starts the named tunnel.
func (m *Manager) finish(ctx context.Context, job *ConnectJob, flow *cloudflare.Flow, cancel context.CancelFunc) {
	defer cancel()
	rec, err := flow.Wait()
	switch {
	case err != nil && ctx.Err() != nil:
		err = errConnectCancelled
	case err == nil:
		err = m.switchToNamed(ctx, job, rec.Hostname)
	}
	m.mu.Lock()
	job.EndedAt = m.now()
	if err != nil {
		job.State, job.Error = JobFailed, err.Error()
		if errors.Is(err, cloudflare.ErrDNSRecordExists) {
			job.Code = CodeDNSExists
		}
	} else {
		job.State = JobOK
	}
	m.mu.Unlock()
	if err != nil {
		m.log.Warn("the domain was not connected", "hostname", job.Hostname, "step", job.Step, "err", err)
		return
	}
	m.log.Info("the domain is connected", "hostname", rec.Hostname, "tunnel", rec.ID)
}

// switchToNamed stores the named mode of a finished connect and starts its tunnel, unless the connect was cancelled
// (a disconnect) or the mode changed since it began: both are checked under switchMu, so a disconnect that raced the
// end of the flow still wins.
func (m *Manager) switchToNamed(ctx context.Context, job *ConnectJob, hostname string) error {
	m.switchMu.Lock()
	defer m.switchMu.Unlock()
	if ctx.Err() != nil {
		return errConnectCancelled
	}
	m.mu.Lock()
	mode := m.mode
	m.mu.Unlock()
	if mode != job.from {
		return fmt.Errorf("the tunnel changed while connecting (mode %s, was %s); nothing was switched: connect again",
			mode, job.from)
	}
	if err := m.save(ModeNamed, hostname); err != nil {
		return err
	}
	return m.run(ModeNamed, hostname)
}

// Job returns the running or the last connect job; ok is false when there was none since homed started.
func (m *Manager) Job() (ConnectJob, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return ConnectJob{}, false
	}
	return *m.job, true
}

// ConnectOffline is `homed cloudflare connect` while homed is not running: the same flow in this process, writing to
// out (the login link included) and into dir. The caller stores the mode in homed.yaml.
func ConnectOffline(ctx context.Context, o Options, hostname string, overwriteDNS bool, out io.Writer) (cloudflare.Record, error) {
	m, err := New(o)
	if err != nil {
		return cloudflare.Record{}, err
	}
	return m.conn.Connect(ctx, cloudflare.ConnectOptions{Hostname: hostname, OverwriteDNS: overwriteDNS}, out)
}
