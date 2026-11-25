package firstrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/devXpro/mcp-gatekeeper/core"
	"github.com/devXpro/mcp-gatekeeper/tunnel/cloudflare"

	"github.com/devXpro/homie/internal/config"
	"github.com/devXpro/homie/internal/tunnel"
	"github.com/devXpro/homie/internal/wire"
)

// The steps of the page (GET /api/firstrun/state).
const (
	stepPIN        = "pin"        // no cookie yet
	stepTunnel     = "tunnel"     // nothing connects the home to the internet: pick "my domain" or "try without one"
	stepConnecting = "connecting" // a connect runs, or the tunnel is starting or its self-check does not pass yet
	stepLink       = "link"       // the tunnel is up: issue the setup link
)

type stateJSON struct {
	Step        string              `json:"step"`
	HomeName    string              `json:"homeName,omitempty"`
	OwnerExists *bool               `json:"ownerExists,omitempty"`
	Tunnel      *tunnel.StatusJSON  `json:"tunnel,omitempty"`
	Connect     *tunnel.ConnectJSON `json:"connect,omitempty"`
}

// tunnelReady reports whether the setup link can be made: a tunnel is up and its self-check passed.
func tunnelReady(st tunnel.Status) bool {
	return st.Mode != tunnel.ModeOff && st.URL != "" && st.State == string(cloudflare.StateConnected)
}

// step is where the page stands, from the tunnel and the last connect.
func step(st tunnel.Status, job *tunnel.ConnectJob) string {
	switch {
	case job != nil && job.State == tunnel.JobRunning:
		return stepConnecting
	case tunnelReady(st):
		return stepLink
	case st.Mode == tunnel.ModeOff:
		return stepTunnel
	}
	return stepConnecting
}

// snapshot is the tunnel and the last connect as the page shows them.
func (s *Server) snapshot() (tunnel.Status, *tunnel.ConnectJob) {
	st := s.tun.Status()
	if job, ok := s.tun.Job(); ok {
		return st, &job
	}
	return st, nil
}

// state is GET /api/firstrun/state: "pin" without the cookie, else tunnel → connecting → link.
func (s *Server) state(w http.ResponseWriter, r *http.Request) error {
	if !s.signedIn(r) {
		wire.WriteJSON(w, http.StatusOK, stateJSON{Step: stepPIN})
		return nil
	}
	ctx := r.Context()
	exists, err := s.home.OwnerExists(ctx)
	if err != nil {
		return err
	}
	st, job := s.snapshot()
	ts := st.JSON()
	out := stateJSON{Step: step(st, job), HomeName: s.home.HomeName(ctx), OwnerExists: &exists, Tunnel: &ts}
	if job != nil {
		cj := job.JSON()
		out.Connect = &cj
	}
	wire.WriteJSON(w, http.StatusOK, out)
	return nil
}

type tunnelAnswer struct {
	Tunnel  tunnel.StatusJSON   `json:"tunnel"`
	Connect *tunnel.ConnectJSON `json:"connect,omitempty"`
}

// connect is POST /api/firstrun/tunnel: {"mode":"named","hostname":…,"overwriteDns"?} connects the owner's domain on a
// hub with no tunnel or a quick one; {"mode":"quick"} starts a quick tunnel on a hub with none. A connected domain is
// never replaced from this plain-http page (409 conflict): that is `homed cloudflare connect` on the home box.
func (s *Server) connect(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Mode         string `json:"mode"`
		Hostname     string `json:"hostname"`
		OverwriteDNS bool   `json:"overwriteDns"`
	}
	if err := wire.Decode(w, r, &body); err != nil {
		return err
	}
	ctx := r.Context()
	client := clientOf(r)
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	st := s.tun.Status()
	var answer tunnelAnswer
	switch body.Mode {
	case tunnel.ModeNamed:
		host := strings.ToLower(strings.TrimSpace(body.Hostname))
		if err := config.CheckHostname(host); err != nil {
			return wire.Invalid("/hostname", "format", err.Error())
		}
		if st.Mode == tunnel.ModeNamed {
			s.log.Warn("firstrun: a domain change was refused, a domain is connected", "client", client)
			return errDomainConnected
		}
		job, err := s.tun.Connect(host, body.OverwriteDNS)
		switch {
		case errors.Is(err, cloudflare.ErrHostname):
			return wire.Invalid("/hostname", "format", err.Error())
		case errors.Is(err, core.ErrConflict):
			return wire.Errorf(http.StatusConflict, "conflict", "a connect is running already")
		case err != nil:
			return err
		}
		s.home.Audit(ctx, core.AuditEntry{At: s.now(), Action: "tunnel.connect", Target: host,
			Detail: map[string]string{"via": "firstrun", "client": client}, IP: client})
		s.log.Info("firstrun: connecting a domain", "hostname", host, "client", client)
		cj := job.JSON()
		answer.Connect = &cj
	case tunnel.ModeQuick:
		if st.Mode != tunnel.ModeOff {
			return wire.Errorf(http.StatusConflict, "conflict", "the home has a tunnel already")
		}
		if err := s.tun.Quick(); err != nil {
			if errors.Is(err, core.ErrConflict) {
				return wire.Errorf(http.StatusConflict, "conflict", err.Error())
			}
			return err
		}
		s.home.Audit(ctx, core.AuditEntry{At: s.now(), Action: "tunnel.quick",
			Detail: map[string]string{"via": "firstrun", "client": client}, IP: client})
		s.log.Info("firstrun: quick tunnel started", "client", client)
	default:
		return wire.Invalid("/mode", "enum", `must be "named" or "quick"`)
	}
	answer.Tunnel = s.tun.Status().JSON()
	wire.WriteJSON(w, http.StatusAccepted, answer)
	return nil
}

var errDomainConnected = wire.Errorf(http.StatusConflict, "conflict",
	"the home's domain is connected; change it on the home box with `homed cloudflare connect <host>`")

var errTunnelNotReady = wire.Errorf(http.StatusConflict, "tunnel_not_ready",
	"the tunnel is not up yet, or https://<host> does not reach this hub")

// link is POST /api/firstrun/link: a new setup link for the current public URL, valid LinkTTL (3 minutes). Every call
// revokes the older links and changes the PIN. After a quick tunnel's URL changed, it is the link that reconnects the
// owner, whose passkeys of the old URL no longer count.
func (s *Server) link(w http.ResponseWriter, r *http.Request) error {
	if err := wire.Decode(w, r, &struct{}{}); err != nil {
		return err
	}
	if !tunnelReady(s.tun.Status()) {
		return errTunnelNotReady
	}
	ctx := r.Context()
	client := clientOf(r)
	url, err := s.home.SetupLink(ctx, LinkTTL)
	if errors.Is(err, core.ErrConflict) {
		return wire.Errorf(http.StatusGone, "setup_done", "the owner can sign in already; this page is closed")
	}
	if err != nil {
		return err
	}
	expires := s.now().Add(LinkTTL)
	s.home.Audit(ctx, core.AuditEntry{At: s.now(), Action: "firstrun.link",
		Detail: map[string]string{"client": client, "expires_at": wire.Time(expires)}, IP: client})
	s.log.Info("firstrun: setup link issued", "client", client, "expires", expires)
	s.rotatePIN("a setup link was issued")
	wire.WriteJSON(w, http.StatusOK, map[string]string{"url": url, "expiresAt": wire.Time(expires)})
	return nil
}

// events is GET /api/firstrun/events: server-sent "tunnel" (TunnelStatus) and "connect" (ConnectJob) events whenever
// one changes, each once at the start. It ends when the client goes, the cookie expires, the page closes or the owner
// can sign in.
func (s *Server) events(w http.ResponseWriter, r *http.Request) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("firstrun: the connection cannot stream")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, "retry: 3000\n\n"); err != nil {
		return nil //nolint:nilerr // the client is gone; nothing to answer
	}
	var (
		seq                 int
		lastTun, lastConn   []byte
		sendTunnel, sendJob = true, true
	)
	frame := func(typ string, b []byte) error {
		seq++
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", seq, typ, b); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	send := func() error {
		st, job := s.snapshot()
		if b, err := wire.Marshal(st.JSON()); err == nil && (sendTunnel || !bytes.Equal(b, lastTun)) {
			lastTun, sendTunnel = b, false
			if err := frame("tunnel", b); err != nil {
				return err
			}
		}
		if job == nil {
			return nil
		}
		if b, err := wire.Marshal(job.JSON()); err == nil && (sendJob || !bytes.Equal(b, lastConn)) {
			lastConn, sendJob = b, false
			return frame("connect", b)
		}
		return nil
	}
	if send() != nil {
		return nil
	}
	poll, ping := time.NewTicker(s.pollEvery), time.NewTicker(s.pingEvery)
	defer poll.Stop()
	defer ping.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
			if !s.signedIn(r) || s.isClosed() || send() != nil {
				return nil
			}
		case <-ping.C:
			if done(ctx, s.home) {
				return nil
			}
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return nil //nolint:nilerr // the client is gone
			}
			flusher.Flush()
		}
	}
}

// done reports whether the owner can sign in now (or it cannot be told): the stream has nothing left to say.
func done(ctx context.Context, home Home) bool {
	ready, err := home.OwnerReady(ctx)
	return err != nil || ready
}
