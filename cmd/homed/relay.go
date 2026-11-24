package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/devXpro/homie/internal/admin"
	"github.com/devXpro/homie/internal/auth/wire"
	"github.com/devXpro/homie/internal/config"
	"github.com/devXpro/homie/internal/relay"
)

// selfCheckInterval is how often a connected relay is checked end to end (PLAN §5.5).
const selfCheckInterval = 30 * time.Second

// errDevRelay is what Connect answers in development mode; it is a wire error, so the first-run page shows it as is.
var errDevRelay = wire.Errorf(http.StatusConflict, "relay_not_ready",
	"development mode (--dev-origin) runs without a relay; get a setup link with: homed admin setup-link")

// relayManager owns the embedded relay client: it connects the relay of homed.yaml, switches to another one when a
// connect code arrives (first-run page, admin, CLI), and runs the self-check every 30 s and at once after every
// connect. It is the admin's Relay (admin.Relay) and the first-run page's (firstrun.Relay).
//
// Without run (the CLI's in-process admin while homed is stopped) it only reads and stores the configuration.
type relayManager struct {
	dir        string
	instanceID string
	base       relay.Config // LocalPort and the test-harness settings; Domain and Token come per connection
	dev        bool         // --dev-origin: no relay at all
	log        *slog.Logger
	interval   time.Duration

	switchMu sync.Mutex // one switch of the client at a time (run's first connect, Connect)

	mu     sync.Mutex
	runCtx context.Context // set by run; nil while no client may run
	cfg    relay.Config    // the current relay; Domain is "" when none
	gen    uint64          // counts switches, so a late self-check result of an old relay is dropped
	client *relay.Client   // the current client; nil when none ran yet
	stop   context.CancelFunc
	done   chan struct{}
	check  admin.SelfCheck
	kick   chan struct{} // wakes the self-check loop after a switch
}

var _ admin.Relay = (*relayManager)(nil)

func newRelayManager(dir, instanceID string, cfg relay.Config, dev bool, log *slog.Logger) *relayManager {
	return &relayManager{
		dir: dir, instanceID: instanceID, base: cfg, cfg: cfg, dev: dev, log: log, interval: selfCheckInterval,
		kick: make(chan struct{}, 1),
	}
}

// run connects the configured relay and checks it until ctx ends, then stops the client and waits for it.
func (m *relayManager) run(ctx context.Context) {
	m.switchMu.Lock()
	m.mu.Lock()
	m.runCtx = ctx
	cfg := m.cfg
	m.mu.Unlock()
	switch {
	case m.dev:
		m.log.Info("development mode: no relay")
	case cfg.Domain == "":
		m.log.Warn("relay not configured; serving locally only (run: homed relay connect - and paste the code)")
	default:
		m.switchLocked(cfg)
	}
	m.switchMu.Unlock()
	m.checkLoop(ctx)
	m.halt()
}

// switchLocked stops the running client, waits until it has stopped (frp keeps process-wide state, so two must never
// run at once) and starts one for cfg, when homed is serving. m.switchMu is held.
func (m *relayManager) switchLocked(cfg relay.Config) {
	m.halt()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg, m.check, m.client = cfg, admin.SelfCheck{}, nil
	m.gen++
	if m.runCtx == nil || m.runCtx.Err() != nil || cfg.Domain == "" {
		return
	}
	ctx, stop := context.WithCancel(m.runCtx)
	c, done, log := &relay.Client{}, make(chan struct{}), m.log
	m.client, m.stop, m.done = c, stop, done
	go func() {
		defer close(done)
		if err := c.Run(ctx, cfg, log); err != nil {
			log.Error("relay stopped", "err", err)
		}
	}()
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// halt stops the running client, if any, and waits for it.
func (m *relayManager) halt() {
	m.mu.Lock()
	stop, done := m.stop, m.done
	m.stop, m.done = nil, nil
	m.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

// checkLoop runs the self-check as soon as the relay connects and then every interval while it stays connected.
func (m *relayManager) checkLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var (
		gen          uint64
		wasConnected bool
		last         time.Time
	)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.kick:
		}
		m.mu.Lock()
		cfg, client, g := m.cfg, m.client, m.gen
		m.mu.Unlock()
		if g != gen {
			gen, wasConnected, last = g, false, time.Time{}
		}
		if client == nil {
			continue
		}
		connected := client.Status().Connected
		due := connected && (!wasConnected || time.Since(last) >= m.interval)
		wasConnected = connected
		if due {
			m.selfCheck(ctx, cfg, g)
			last = time.Now()
		}
	}
}

// selfCheck runs one check of cfg and keeps its result while cfg is still the current relay. It logs changes only.
func (m *relayManager) selfCheck(ctx context.Context, cfg relay.Config, gen uint64) admin.SelfCheck {
	res := admin.SelfCheck{OK: true, CheckedAt: time.Now()}
	if err := cfg.SelfCheck(ctx, m.instanceID); err != nil {
		res.OK, res.Error = false, err.Error()
	}
	m.mu.Lock()
	current := m.gen == gen
	prev := m.check
	if current {
		m.check = res
	}
	m.mu.Unlock()
	if current && (prev.CheckedAt.IsZero() || prev.OK != res.OK || prev.Error != res.Error) {
		if res.OK {
			m.log.Info("relay self-check passed", "url", "https://"+cfg.Domain+"/healthz")
		} else {
			m.log.Warn("relay self-check failed", "err", res.Error)
		}
	}
	return res
}

// Domain implements admin.Relay and firstrun.Relay.
func (m *relayManager) Domain() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.Domain
}

// Status implements admin.Relay and firstrun.Relay.
func (m *relayManager) Status() relay.Status {
	m.mu.Lock()
	c := m.client
	m.mu.Unlock()
	if c == nil {
		return relay.Status{}
	}
	return c.Status()
}

// LastCheck implements admin.Relay.
func (m *relayManager) LastCheck() admin.SelfCheck {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.check
}

// SelfCheckResult implements firstrun.Relay.
func (m *relayManager) SelfCheckResult() (time.Time, string) {
	c := m.LastCheck()
	return c.CheckedAt, c.Error
}

// CheckNow implements admin.Relay: a self-check now, whatever the connection state.
func (m *relayManager) CheckNow(ctx context.Context) admin.SelfCheck {
	m.mu.Lock()
	cfg, gen := m.cfg, m.gen
	m.mu.Unlock()
	if cfg.Domain == "" {
		return admin.SelfCheck{}
	}
	return m.selfCheck(ctx, cfg, gen)
}

// Connect implements admin.Relay and firstrun.Relay: it stores the code the way `homed relay connect` always did
// (relay.domain in homed.yaml, the token in relay-token, 0600) and switches the running client to it. Whether a new
// domain is allowed is the caller's rule (the web accepts only the current one). Nothing here logs the token.
func (m *relayManager) Connect(_ context.Context, code relay.Code) error {
	if m.dev {
		return errDevRelay
	}
	if err := code.Validate(); err != nil {
		return err
	}
	m.switchMu.Lock()
	defer m.switchMu.Unlock()
	if err := config.WriteRelayToken(m.dir, code.Token); err != nil {
		return fmt.Errorf("save the relay token: %w", err)
	}
	if err := config.Update(m.dir, func(c *config.Config) error {
		c.Relay.Domain = code.Domain
		return nil
	}); err != nil {
		return fmt.Errorf("save the relay domain: %w", err)
	}
	cfg := m.base
	cfg.Domain, cfg.Token = code.Domain, code.Token
	m.switchLocked(cfg)
	m.log.Info("relay set", "domain", code.Domain)
	return nil
}
