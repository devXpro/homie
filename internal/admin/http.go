package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devXpro/mcp-gatekeeper/core"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/wire"
)

const (
	basePath     = "/api/home"
	maxBodyBytes = wire.MaxBody
	maxArgsBytes = 64 << 10
)

// errorf, notFound, invalidField and notReady build the API's errors (the gatekeeper's form, package wire).
func errorf(status int, code, message string) *wire.Error { return wire.Errorf(status, code, message) }

func notFound(what string) *wire.Error {
	return wire.Errorf(http.StatusNotFound, "not_found", what+" not found")
}

func invalidField(path, code, message string) *wire.Error { return wire.Invalid(path, code, message) }

func notReady(what string) *wire.Error {
	return wire.Errorf(http.StatusServiceUnavailable, "home_not_ready", what+" is not available")
}

// level is who may call an endpoint.
type level int

const (
	levelPublic           level = iota
	levelSession                // anyone signed in
	levelOwner                  // a person of role owner
	levelSuperadminStepUp       // the home's superadmin with a fresh passkey assertion: code that runs, the system
)

// request is one admin request, or one admin socket command (local, with neither w nor r).
type request struct {
	w     http.ResponseWriter
	r     *http.Request
	ctx   context.Context
	v     core.Viewer   // the signed-in person; zero on the admin socket
	local bool          // the admin socket (the homed CLI)
	ip    string        // the client address, for the audit
	sess  core.Sessions // the session layer the request came through; nil on the admin socket
}

// via is the activity's and the audit's surface of the request.
func (q *request) via() string {
	if q.local {
		return "cli"
	}
	return "admin"
}

// owner reports whether the request may act as an owner (the admin socket always may).
func (q *request) owner() bool { return q.local || q.v.Person.Role == core.RoleOwner }

// caller is the request as the plugin and the activity log see it.
func (s *Server) caller(q *request) hub.Caller {
	if q.local {
		return hub.Caller{Person: s.opts.LocalName, Via: q.via()}
	}
	return hub.Caller{Person: q.v.Person.Name, PersonID: string(q.v.Person.ID), Via: q.via()}
}

type handler func(q *request) error

type route struct {
	pattern string // "METHOD /path" relative to basePath
	level   level
	h       handler
}

// Routes returns the admin API's routes behind sessions, the session layer of one gatekeeper stack (its auth), whose
// Site gives the client address mode of the audit.
func (s *Server) Routes(sessions core.Sessions, site core.Site) core.RouteRegistrar {
	return routes{s: s, sess: sessions, ipMode: site.ClientIP}
}

type routes struct {
	s      *Server
	sess   core.Sessions
	ipMode core.ClientIPMode
}

// RegisterRoutes implements core.RouteRegistrar: everything under /api/home/.
func (rt routes) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle(basePath+"/", rt.s.handler(rt.sess, rt.ipMode))
}

// handler builds the routes of the table behind sess.
func (s *Server) handler(sess core.Sessions, ipMode core.ClientIPMode) http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.table() {
		method, path, _ := strings.Cut(rt.pattern, " ")
		mux.Handle(method+" "+basePath+path, s.wrap(rt, sess, ipMode))
	}
	return s.frame(mux)
}

// frame sets the common headers and turns the mux's own 404 and 405 answers into the JSON error form.
func (s *Server) frame(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire.Headers(w)
		if _, pattern := mux.Handler(r); pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		// Unmatched: find out whether the mux would say 404 or 405 (it sets Allow for 405), then answer in JSON.
		probe := &statusProbe{header: http.Header{}}
		mux.ServeHTTP(probe, r)
		if allow := probe.header.Get("Allow"); allow != "" {
			w.Header().Set("Allow", allow)
		}
		if probe.status == http.StatusMethodNotAllowed {
			wire.WriteError(w, errorf(http.StatusMethodNotAllowed, "method_not_allowed", r.Method+" is not allowed here"))
			return
		}
		wire.WriteError(w, notFound("endpoint"))
	})
}

// statusProbe records the status of a handler and discards its body.
type statusProbe struct {
	header http.Header
	status int
}

func (p *statusProbe) Header() http.Header { return p.header }
func (p *statusProbe) WriteHeader(code int) {
	if p.status == 0 {
		p.status = code
	}
}
func (p *statusProbe) Write(b []byte) (int, error) {
	p.WriteHeader(http.StatusOK)
	return len(b), nil
}

// wrap applies the rules in order: the session (with the CSRF checks of a write) and the role, step-up, then the
// content type, the size and the rate limit, then the handler.
func (s *Server) wrap(rt route, sess core.Sessions, ipMode core.ClientIPMode) http.Handler {
	var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := &request{w: w, r: r, ctx: r.Context(), ip: core.ClientIP(r, ipMode), sess: sess}
		q.v, _ = core.ViewerFrom(r.Context())
		if err := s.admit(q, rt.level); err != nil {
			s.writeError(q, err)
			return
		}
		if err := rt.h(q); err != nil {
			s.writeError(q, err)
		}
	})
	switch rt.level {
	case levelPublic:
		return sess.SameOrigin(h)
	case levelSession:
		return sess.RequireViewer(h)
	case levelOwner:
		return sess.RequireViewer(ownerOnly(h))
	default:
		return sess.RequireViewer(superadminOnly(sess.RequireStepUp(h)))
	}
}

// superadminOnly answers 403 forbidden to anyone signed in who is not the home's superadmin, co-owners included, before
// any step-up is asked for: installing, updating or removing a plugin, updating the hub and reconnecting the tunnel
// run code on the home box or change the system (docs/SECURITY.md).
func superadminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v, ok := core.ViewerFrom(r.Context()); !ok || v.Person.Role != core.RoleOwner || !v.Person.Superadmin {
			wire.WriteError(w, errorf(http.StatusForbidden, "forbidden", "only the home's superadmin may do this: "+
				"installing, updating and removing plugins, updating the hub and reconnecting the tunnel run code on the "+
				"home box or change it; co-owners manage people, settings and the devices' tools"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ownerOnly answers 403 forbidden to anyone signed in who is not an owner, before any step-up is asked for.
func ownerOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v, ok := core.ViewerFrom(r.Context()); !ok || v.Person.Role != core.RoleOwner {
			wire.WriteError(w, errorf(http.StatusForbidden, "forbidden", "only an owner of the home may do this"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// admit checks the body and the rate limit of a request that passed the session layer.
func (s *Server) admit(q *request, lv level) error {
	r := q.r
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if err := checkBody(q); err != nil {
			return err
		}
	}
	if lv == levelPublic {
		return nil
	}
	key := hex.EncodeToString(q.v.Session.IDHash)
	if !s.limit.allow(key, s.now()) {
		return &wire.Error{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "too many requests",
			RetryAfter: s.limit.window}
	}
	return nil
}

// checkBody enforces the JSON content type and the 1 MiB cap on requests that carry a body.
func checkBody(q *request) error {
	r := q.r
	if r.ContentLength == 0 || r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return errorf(http.StatusUnsupportedMediaType, "unsupported_media_type", "send application/json")
	}
	r.Body = http.MaxBytesReader(q.w, r.Body, maxBodyBytes)
	return nil
}

// decode reads the JSON object body into v. An empty body leaves v unchanged.
func decode(q *request, v any) error {
	if q.r == nil {
		return nil
	}
	return wire.Decode(q.w, q.r, v)
}

// writeJSON writes v without HTML escaping.
func (s *Server) writeJSON(q *request, status int, v any) error {
	wire.WriteJSON(q.w, status, v)
	return nil
}

func (s *Server) writeError(q *request, err error) {
	if _, ok := errors.AsType[*wire.Error](err); !ok {
		s.log.Error("admin request failed", "method", q.r.Method, "path", q.r.URL.Path, "err", err)
	}
	wire.WriteError(q.w, err)
}

// marshal is json.Marshal without HTML escaping, so "<" in plugin texts arrives unchanged.
func marshal(v any) ([]byte, error) { return wire.Marshal(v) }

// randomID returns n random bytes as base64url without padding.
func randomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // never fails since Go 1.24
	return base64.RawURLEncoding.EncodeToString(b)
}

// limiter is a per-session token bucket: 600 requests a minute.
type limiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per window
	window  time.Duration
	buckets map[string]*bucket
	swept   time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(rate int, window time.Duration) *limiter {
	return &limiter{rate: float64(rate), window: window, buckets: map[string]*bucket{}}
}

func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.swept) > l.window {
		for k, b := range l.buckets {
			if now.Sub(b.last) > l.window {
				delete(l.buckets, k)
			}
		}
		l.swept = now
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.rate, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.rate, b.tokens+now.Sub(b.last).Seconds()*l.rate/l.window.Seconds())
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ts formats a time as RFC 3339 UTC with milliseconds.
func ts(t time.Time) string { return wire.Time(t) }

// optTS is ts for optional times: nil (omitted) when zero.
func optTS(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	v := ts(t)
	return &v
}

// itoa is strconv.FormatInt for the ids on the wire.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }
