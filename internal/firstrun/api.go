package firstrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/devXpro/homie/internal/auth"
	"github.com/devXpro/homie/internal/auth/wire"
	"github.com/devXpro/homie/internal/relay"
	"github.com/devXpro/homie/internal/store"
)

// connectingGrace: a disconnected relay counts as connecting this long after its last change (ADMIN-API §2).
const connectingGrace = 30 * time.Second

// relayJSON is RelayStatus (ADMIN-API §2), built the way internal/admin builds it.
type relayJSON struct {
	Configured bool           `json:"configured"`
	Domain     string         `json:"domain,omitempty"`
	PublicURL  string         `json:"publicUrl,omitempty"`
	State      string         `json:"state"`
	Since      string         `json:"since,omitempty"`
	LastError  string         `json:"lastError,omitempty"`
	SelfCheck  *selfCheckJSON `json:"selfCheck,omitempty"`
}

type selfCheckJSON struct {
	OK        bool   `json:"ok"`
	CheckedAt string `json:"checkedAt"`
	Error     string `json:"error,omitempty"`
}

// relayStatus returns RelayStatus and whether the setup link can be made: connected, and the last self-check proved
// that https://<domain> reaches this hub.
func (s *Server) relayStatus() (out relayJSON, ready bool) {
	domain := s.relay.Domain()
	if domain == "" {
		return relayJSON{State: "not_configured"}, false
	}
	st := s.relay.Status()
	out = relayJSON{
		Configured: true, Domain: domain, PublicURL: "https://" + domain, Since: wire.OptTime(st.Since),
		LastError: st.LastError,
	}
	switch {
	case st.Connected:
		out.State = "connected"
	case st.LastError == "" || !st.Since.IsZero() && s.now().Sub(st.Since) < connectingGrace:
		out.State = "connecting"
	default:
		out.State = "offline"
	}
	checkedAt, failure := s.relay.SelfCheckResult()
	if !checkedAt.IsZero() {
		out.SelfCheck = &selfCheckJSON{OK: failure == "", CheckedAt: wire.Time(checkedAt), Error: failure}
	}
	return out, st.Connected && out.SelfCheck != nil && out.SelfCheck.OK
}

type stateJSON struct {
	Step     string     `json:"step"`
	HomeName string     `json:"homeName,omitempty"`
	Relay    *relayJSON `json:"relay,omitempty"`
}

// state is GET /api/firstrun/state: "pin" without the cookie, else relay → connecting → link.
func (s *Server) state(w http.ResponseWriter, r *http.Request) error {
	if !s.signedIn(r) {
		wire.WriteJSON(w, http.StatusOK, stateJSON{Step: "pin"})
		return nil
	}
	hs, err := s.st.Settings().Get(r.Context())
	if err != nil {
		return err
	}
	out := stateJSON{HomeName: hs.HomeName}
	rs, ready := s.relayStatus()
	switch {
	case !rs.Configured:
		out.Step = "relay"
	case ready:
		out.Step, out.Relay = "link", &rs
	default:
		out.Step, out.Relay = "connecting", &rs
	}
	wire.WriteJSON(w, http.StatusOK, out)
	return nil
}

var errRelayConfigured = wire.Errorf(http.StatusConflict, "conflict",
	"homed already has a relay; change it on the home box with `homed relay connect -`")

// connect is POST /api/firstrun/relay: the relay code of a home that has none yet. A configured relay is never
// replaced from this plain-http page (409 conflict); that is `homed relay connect -` on the home box, or a step-up in
// the admin. Any domain is fine for the first one: nobody has a passkey yet that a new relying party could lock out.
func (s *Server) connect(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Code string `json:"code"`
	}
	if err := wire.Decode(w, r, &body); err != nil {
		return err
	}
	code, err := relay.ParseCode(body.Code)
	if err != nil {
		return wire.Invalid("/code", "invalid", err.Error()) // ParseCode errors never contain the token
	}
	ctx := r.Context()
	client := clientOf(r)
	s.connectMu.Lock()
	defer s.connectMu.Unlock()
	if s.relay.Domain() != "" {
		s.log.Warn("firstrun: a relay code was refused, homed has a relay", "client", client)
		return errRelayConfigured
	}
	if err := s.relay.Connect(context.WithoutCancel(ctx), code); err != nil {
		return fmt.Errorf("firstrun: connect the relay: %w", err)
	}
	s.record(ctx, store.AuditRecord{At: s.now(), Via: "admin", Action: "relay.change", Target: code.Domain, OK: true,
		Args: clientDetails(client)})
	s.log.Info("firstrun: relay code applied", "domain", code.Domain, "client", client)
	rs, _ := s.relayStatus()
	wire.WriteJSON(w, http.StatusAccepted, rs)
	return nil
}

var errRelayNotReady = wire.Errorf(http.StatusConflict, "relay_not_ready",
	"the relay is not connected yet, or https://<domain> does not reach this hub")

// link is POST /api/firstrun/link: a new setup link for the relay domain, valid LinkTTL (3 minutes). Every call
// revokes the older links and changes the PIN.
func (s *Server) link(w http.ResponseWriter, r *http.Request) error {
	if err := wire.Decode(w, r, &struct{}{}); err != nil {
		return err
	}
	rs, ready := s.relayStatus()
	if !ready {
		return errRelayNotReady
	}
	client := clientOf(r)
	l, err := auth.NewSetupLink(r.Context(), s.st, s.audit,
		auth.LinkRequest{Origin: auth.ForDomain(rs.Domain).Origin, Via: "admin", Client: client, TTL: LinkTTL}, s.now())
	if err != nil {
		return err
	}
	s.log.Info("firstrun: setup link issued", "client", client, "expires", l.ExpiresAt)
	s.rotatePIN("a setup link was issued")
	wire.WriteJSON(w, http.StatusOK, map[string]string{"url": l.URL, "expiresAt": wire.Time(l.ExpiresAt)})
	return nil
}

// clientDetails names the client in an audit record.
func clientDetails(client string) []byte {
	b, _ := wire.Marshal(map[string]string{"client": client})
	return b
}

// events is GET /api/firstrun/events: server-sent "relay" events with the full RelayStatus whenever it changes. It
// ends when the client goes, the cookie expires or the home gets its owner.
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
		seq  int
		last []byte
	)
	send := func() error {
		rs, _ := s.relayStatus()
		b, err := wire.Marshal(rs)
		if err != nil || bytes.Equal(b, last) {
			return err
		}
		last, seq = b, seq+1
		if _, err := fmt.Fprintf(w, "id: %d\nevent: relay\ndata: %s\n\n", seq, b); err != nil {
			return err
		}
		flusher.Flush()
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
			if !s.signedIn(r) || send() != nil {
				return nil
			}
		case <-ping.C:
			if ready, err := auth.OwnerReady(ctx, s.st, s.rpID()); err != nil || ready || s.isClosed() {
				return nil
			}
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return nil //nolint:nilerr // the client is gone
			}
			flusher.Flush()
		}
	}
}

// record writes an admin audit record; it outlives the request.
func (s *Server) record(ctx context.Context, rec store.AuditRecord) {
	rec.Kind = store.AuditAdmin
	ctx = context.WithoutCancel(ctx)
	if s.audit != nil {
		s.audit.RecordAdmin(ctx, rec)
		return
	}
	if _, err := s.st.Audit().Append(ctx, []store.AuditRecord{rec}); err != nil {
		s.log.Error("firstrun: cannot store an audit record", "action", rec.Action, "err", err)
	}
}
