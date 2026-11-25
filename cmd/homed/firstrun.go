package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/devXpro/mcp-gatekeeper/adminsock"
	"github.com/devXpro/mcp-gatekeeper/core"

	"github.com/devXpro/homie/internal/firstrun"
	"github.com/devXpro/homie/internal/tunnel"
)

// firstRunCloseDelay keeps the first-run page answering "the home is set up" for a while after the owner registered,
// so a page still open can say so; then its port closes.
const firstRunCloseDelay = 2 * time.Minute

// firstRunPage is one opening of the first-run page.
type firstRunPage struct {
	srv  *firstrun.Server
	urls []string
	// moves is true when the page listens on the home network by default (no setup_listen, no tunnel yet): it moves to
	// loopback once a tunnel is configured.
	moves bool
	stop  context.CancelFunc
	done  chan struct{}
}

// openFirstRun opens the first-run page unless it is open, and returns it: a new listener, a new PIN, 30 minutes and
// a fresh budget of wrong PINs. homed opens it at start while the owner cannot sign in, when the public URL
// changes, and on `homed admin firstrun`; every caller has just seen that the owner cannot sign in, so a page that was
// about to close (the owner could sign in at the old quick URL) stays open.
func (d *daemon) openFirstRun() (*firstRunPage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p := d.firstRun; p != nil {
		if d.firstRunTimer == nil {
			return p, nil
		}
		if d.firstRunTimer.Stop() {
			d.firstRunTimer = nil
			d.log.Info("the owner cannot sign in at the public URL any more; the first-run page stays open")
			return p, nil
		}
		// The timer fired: that page is closing, a new one opens.
	}
	if d.runCtx == nil || d.runCtx.Err() != nil {
		return nil, errors.New("homed is stopping")
	}
	addr, moves := d.firstRunAddr()
	ln, err := (&net.ListenConfig{}).Listen(d.runCtx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("the first-run page cannot listen on %s: %w", addr, err)
	}
	srv, err := firstrun.New(firstrun.Options{
		Tunnel: d.tun, Home: firstRunHome{d}, UI: d.firstRunUI, Terminal: d.terminal,
		Log: d.log.With("component", "firstrun"),
	})
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	p := &firstRunPage{srv: srv, moves: moves}
	d.firstRun, d.firstRunTimer = p, nil
	d.servePageLocked(p, ln)
	return p, nil
}

// servePageLocked serves the page on ln until it closes or homed stops; d.mu is held.
func (d *daemon) servePageLocked(p *firstRunPage, ln net.Listener) {
	ctx, stop := context.WithCancel(d.runCtx)
	p.urls, p.stop, p.done = firstrun.PageURLs(ln.Addr()), stop, make(chan struct{})
	done := p.done
	d.wg.Go(func() {
		defer close(done)
		defer stop()
		if err := p.srv.Serve(ctx, ln); err != nil {
			d.log.Error("first-run page", "err", err)
		}
		if p.srv.Closed() {
			d.mu.Lock()
			if d.firstRun == p {
				d.firstRun = nil
			}
			d.mu.Unlock()
		}
	})
}

// firstRunAddr is where the first-run page listens: setup_listen when set, else the home network only while no tunnel
// is configured (and not in development mode); moves reports the default home-network address.
func (d *daemon) firstRunAddr() (addr string, moves bool) {
	if d.setupListen != "" {
		return d.setupListen, false
	}
	configured := d.dev != nil || d.tun.Status().Mode != tunnel.ModeOff
	if configured {
		return cmpOr(d.listenLocal, firstrun.ListenLocal), false
	}
	return cmpOr(d.listenLAN, firstrun.ListenLAN), true
}

// moveFirstRun moves a page that listens on the home network by default to loopback once a tunnel is configured: the
// page is never on the home network with a tunnel (the PIN, the cookies and the time stay; a browser on localhost
// keeps working).
func (d *daemon) moveFirstRun() {
	if d.tun.Status().Mode == tunnel.ModeOff {
		return
	}
	d.mu.Lock()
	p := d.firstRun
	if p == nil || !p.moves {
		d.mu.Unlock()
		return
	}
	p.moves = false
	stop, done := p.stop, p.done
	d.mu.Unlock()
	stop()
	<-done
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.firstRun != p || d.runCtx.Err() != nil {
		return
	}
	ln, err := (&net.ListenConfig{}).Listen(d.runCtx, "tcp", cmpOr(d.listenLocal, firstrun.ListenLocal))
	if err != nil {
		d.log.Error("the first-run page could not move to loopback; open it again with: homed admin firstrun", "err", err)
		d.firstRun = nil
		return
	}
	d.log.Info("a tunnel is configured: the first-run page moved off the home network", "urls", firstrun.PageURLs(ln.Addr()))
	d.servePageLocked(p, ln)
}

// closeFirstRunSoon closes the first-run page a while after the owner could sign in, so a page still open can say so.
func (d *daemon) closeFirstRunSoon() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p := d.firstRun; p != nil && d.firstRunTimer == nil {
		d.firstRunTimer = time.AfterFunc(firstRunCloseDelay, func() {
			p.stop()
			d.mu.Lock()
			if d.firstRun == p {
				d.firstRun, d.firstRunTimer = nil, nil
			}
			d.mu.Unlock()
		})
		d.log.Info("the home has an owner; the first-run page closes soon", "in", firstRunCloseDelay)
	}
}

// watchOwner closes the first-run page once the owner can sign in at the public URL.
func (d *daemon) watchOwner(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		d.mu.Lock()
		open := d.firstRun != nil && d.firstRunTimer == nil
		d.mu.Unlock()
		if !open {
			continue
		}
		if ready, err := d.ownerReady(ctx); err == nil && ready {
			d.closeFirstRunSoon()
		}
	}
}

// firstRunHome is homed as the first-run page needs it.
type firstRunHome struct{ d *daemon }

func (h firstRunHome) OwnerReady(ctx context.Context) (bool, error) { return h.d.ownerReady(ctx) }

func (h firstRunHome) OwnerExists(ctx context.Context) (bool, error) {
	sas, err := superadmins(ctx, h.d.st)
	return len(sas) > 0, err
}

// SetupLink is auth's in-process setup link of the current public URL, living at most ttl.
func (h firstRunHome) SetupLink(ctx context.Context, ttl time.Duration) (string, error) {
	s := h.d.stack.Load()
	if s == nil {
		return "", errors.New("the home has no public URL yet")
	}
	url, _, err := s.auth.IssueSetupLinkTTL(ctx, ttl)
	return url, err
}

func (h firstRunHome) HomeName(ctx context.Context) string { return h.d.homeName(ctx) }

func (h firstRunHome) Audit(ctx context.Context, e core.AuditEntry) { h.d.auditCLI(ctx, e) }

// cmdFirstRun is the admin socket's `firstrun` (`homed admin firstrun`): it opens the first-run page when it is
// closed and prints its addresses and its PIN. This is how to get the PIN when homed runs under launchd or systemd,
// where it never prints it; the PIN goes to that terminal only.
func (d *daemon) cmdFirstRun(ctx context.Context, inv adminsock.Invocation) error {
	if len(inv.Args) > 0 {
		return fmt.Errorf("%w: firstrun takes no arguments", adminsock.ErrUsage)
	}
	if ready, err := d.ownerReady(ctx); err != nil {
		return err
	} else if ready {
		return fmt.Errorf("the home is set up; add a passkey with a setup link: homed admin setup-link: %w", core.ErrConflict)
	}
	p, err := d.openFirstRun()
	if err != nil {
		return fmt.Errorf("%w: %w", err, core.ErrConflict)
	}
	pin, until := p.srv.PIN()
	d.mu.Lock()
	urls := p.urls
	d.mu.Unlock()
	_, _ = fmt.Fprintf(inv.Out, "The first-run page is open until %s on:\n", p.srv.ClosesAt().Local().Format("15:04"))
	for _, u := range urls {
		_, _ = fmt.Fprintf(inv.Out, "  %s\n", u)
	}
	_, err = fmt.Fprintf(inv.Out, "PIN: %s (it changes at %s and after every setup link; run this again for the current one)\n",
		firstrun.FormatPIN(pin), until.Local().Format("15:04"))
	return err
}
