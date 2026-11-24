package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	basePath       = "/api/admin"
	maxBodyBytes   = 1 << 20
	maxArgsBytes   = 64 << 10
	csrfHeader     = "X-Homie"
	instanceHeader = "X-Homie-Instance"
	sessionCookie  = "__Host-homie_session"
)

// apiError is an error with its HTTP status and stable code (ADMIN-API §1.2).
type apiError struct {
	status  int
	code    string
	message string
	fields  []fieldError
	extra   map[string]string // e.g. callId, jobId
}

func (e *apiError) Error() string { return e.code + ": " + e.message }

type fieldError struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func errorf(status int, code, message string) *apiError {
	return &apiError{status: status, code: code, message: message}
}

func notFound(what string) *apiError {
	return errorf(http.StatusNotFound, "not_found", what+" not found")
}

func invalidField(path, code, message string) *apiError {
	return &apiError{
		status: http.StatusBadRequest, code: "invalid_args", message: strings.TrimPrefix(path, "/") + ": " + message,
		fields: []fieldError{{Path: path, Code: code, Message: message}},
	}
}

func notReady(what string) *apiError {
	return errorf(http.StatusServiceUnavailable, "home_not_ready", what+" is not available")
}

// level is who may call an endpoint (ADMIN-API §1.3).
type level int

const (
	levelPublic level = iota
	levelSession
	levelOwner
	levelOwnerStepUp
	levelSelfStepUp
)

// surface says where a route is served.
type surface int

const (
	both surface = iota
	webOnly
	localOnly
)

// request is one admin request with its principal.
type request struct {
	w     http.ResponseWriter
	r     *http.Request
	p     Principal
	local bool
	id    string
}

// via is the audit surface of the request.
func (q *request) via() string {
	if q.local {
		return "cli"
	}
	return "admin"
}

type handler func(q *request) error

type route struct {
	pattern string // "METHOD /path" relative to basePath
	level   level
	surface surface
	h       handler
}

// routes builds the web and the socket handlers from one table.
func (s *Server) routes() (web, local http.Handler) {
	webMux, localMux := http.NewServeMux(), http.NewServeMux()
	for _, rt := range s.table() {
		method, path, _ := strings.Cut(rt.pattern, " ")
		pattern := method + " " + basePath + path
		if rt.surface != localOnly {
			webMux.Handle(pattern, s.wrap(rt, false))
		}
		if rt.surface != webOnly {
			localMux.Handle(pattern, s.wrap(rt, true))
		}
	}
	// T2.2's ceremonies; the admin's rules apply first.
	if s.opts.AuthRoutes != nil {
		webMux.Handle(basePath+"/auth/", s.passthrough(levelPublic, s.opts.AuthRoutes))
		for _, p := range []string{"POST " + basePath + "/me/passkeys/begin", "POST " + basePath + "/me/passkeys/finish"} {
			webMux.Handle(p, s.passthrough(levelSelfStepUp, s.opts.AuthRoutes))
		}
	}
	if s.opts.LocalRoutes != nil {
		localMux.Handle(basePath+"/local/", s.localPassthrough(s.opts.LocalRoutes))
	}
	return s.frame(webMux, false), s.frame(localMux, true)
}

// frame sets the common headers and turns the mux's own 404 and 405 answers into the JSON error format.
func (s *Server) frame(mux *http.ServeMux, local bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := "r_" + randomID(6)
		h := w.Header()
		h.Set("X-Request-Id", id)
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		if s.opts.InstanceID != "" {
			h.Set(instanceHeader, s.opts.InstanceID)
		}
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		if _, pattern := mux.Handler(r); pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		// Unmatched: find out whether the mux would say 404 or 405 (it sets Allow for 405), then answer in JSON.
		probe := &statusProbe{header: http.Header{}}
		mux.ServeHTTP(probe, r)
		if allow := probe.header.Get("Allow"); allow != "" {
			h.Set("Allow", allow)
		}
		q := &request{w: w, r: r, local: local, id: id}
		if probe.status == http.StatusMethodNotAllowed {
			s.writeError(q, errorf(http.StatusMethodNotAllowed, "method_not_allowed", r.Method+" is not allowed here"))
			return
		}
		s.writeError(q, notFound("endpoint"))
	})
}

type requestIDKey struct{}

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

// wrap applies the request rules in order: CSRF, content type and size, authentication, level, rate limit.
func (s *Server) wrap(rt route, local bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := &request{w: w, r: r, local: local, id: requestID(r)}
		if err := s.admit(q, rt.level); err != nil {
			s.writeError(q, err)
			return
		}
		if err := rt.h(q); err != nil {
			s.writeError(q, err)
		}
	})
}

// passthrough hands a request to another handler after the admin's own checks.
func (s *Server) passthrough(lv level, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := &request{w: w, r: r, id: requestID(r)}
		if err := s.admit(q, lv); err != nil {
			s.writeError(q, err)
			return
		}
		next.ServeHTTP(w, q.r)
	})
}

func (s *Server) localPassthrough(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := &request{w: w, r: r, local: true, id: requestID(r)}
		if err := s.admit(q, levelOwner); err != nil {
			s.writeError(q, err)
			return
		}
		next.ServeHTTP(w, q.r)
	})
}

func requestID(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey{}).(string)
	return id
}

// admit checks everything that comes before the handler and fills q.p.
func (s *Server) admit(q *request, lv level) error {
	r := q.r
	mutating := r.Method != http.MethodGet && r.Method != http.MethodHead
	if mutating && !q.local {
		if err := s.checkCSRF(r); err != nil {
			return err
		}
	}
	if mutating {
		if err := checkBody(q); err != nil {
			return err
		}
	}
	if q.local {
		p, err := s.localPrincipal(r.Context())
		if err != nil {
			return err
		}
		q.p = p
		return nil
	}
	if lv == levelPublic {
		return nil
	}
	if s.opts.Auth == nil {
		return errorf(http.StatusUnauthorized, "unauthenticated", "sign-in is not available")
	}
	p, err := s.opts.Auth.Authenticate(r)
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return errorf(http.StatusUnauthorized, "unauthenticated", "sign in first")
	case err != nil:
		return err
	}
	q.p = p
	if !s.limit.allow(p.Session.ID, s.now()) {
		q.w.Header().Set("Retry-After", strconv.Itoa(int(s.limit.window/time.Second)))
		return errorf(http.StatusTooManyRequests, "rate_limited", "too many requests")
	}
	if (lv == levelOwner || lv == levelOwnerStepUp) && p.Person.Role != roleOwner {
		return errorf(http.StatusForbidden, "forbidden", "only the owner may do this")
	}
	if (lv == levelOwnerStepUp || lv == levelSelfStepUp) && !s.now().Before(p.StepUpUntil) {
		return errorf(http.StatusForbidden, "step_up_required", "confirm with your passkey first")
	}
	return nil
}

// checkCSRF applies ADMIN-API §1.4.
func (s *Server) checkCSRF(r *http.Request) error {
	failed := errorf(http.StatusForbidden, "csrf_failed", "cross-site request refused")
	if r.Header.Get(csrfHeader) != "1" {
		return failed
	}
	origin := s.opts.Origin
	if s.opts.OriginFunc != nil {
		origin = s.opts.OriginFunc()
	}
	if o := r.Header.Get("Origin"); o != "" && (origin == "" || o != origin) {
		return failed
	}
	if sf := r.Header.Get("Sec-Fetch-Site"); sf != "" && sf != "same-origin" {
		return failed
	}
	return nil
}

// checkBody enforces the JSON content type and the 1 MiB cap on requests that carry a body.
func checkBody(q *request) error {
	r := q.r
	if r.ContentLength == 0 || r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil || mt != "application/json" {
			return errorf(http.StatusUnsupportedMediaType, "unsupported_media_type", "send application/json")
		}
	} else {
		return errorf(http.StatusUnsupportedMediaType, "unsupported_media_type", "send application/json")
	}
	r.Body = http.MaxBytesReader(q.w, r.Body, maxBodyBytes)
	return nil
}

// decode reads the JSON object body into v. An empty body leaves v unchanged.
func decode(q *request, v any) error {
	body, err := io.ReadAll(q.r.Body)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return errorf(http.StatusRequestEntityTooLarge, "too_large", "the request body is too large")
		}
		return errorf(http.StatusBadRequest, "invalid_json", "the request body could not be read")
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil
	}
	if body[0] != '{' {
		return errorf(http.StatusBadRequest, "invalid_json", "the body must be a JSON object")
	}
	if err := json.Unmarshal(body, v); err != nil {
		if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok && te.Field != "" {
			return invalidField("/"+strings.ReplaceAll(te.Field, ".", "/"), "type", "has the wrong type")
		}
		return errorf(http.StatusBadRequest, "invalid_json", "the body is not valid JSON")
	}
	return nil
}

// writeJSON writes v without HTML escaping.
func (s *Server) writeJSON(q *request, status int, v any) error {
	b, err := marshal(v)
	if err != nil {
		return err
	}
	q.w.Header().Set("Content-Type", "application/json; charset=utf-8")
	q.w.WriteHeader(status)
	_, _ = q.w.Write(b)
	return nil
}

func noContent(q *request) error {
	q.w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) writeError(q *request, err error) {
	ae, ok := errors.AsType[*apiError](err)
	if !ok {
		s.log.Error("admin request failed", "method", q.r.Method, "path", q.r.URL.Path, "request", q.id, "err", err)
		ae = errorf(http.StatusInternalServerError, "internal", "internal error")
	}
	body := map[string]any{"code": ae.code, "message": ae.message, "requestId": q.id}
	if len(ae.fields) > 0 {
		body["fields"] = ae.fields
	}
	for k, v := range ae.extra {
		body[k] = v
	}
	b, mErr := marshal(map[string]any{"error": body})
	if mErr != nil {
		b = []byte(`{"error":{"code":"internal","message":"internal error"}}`)
	}
	q.w.Header().Set("Content-Type", "application/json; charset=utf-8")
	q.w.WriteHeader(ae.status)
	_, _ = q.w.Write(b)
}

// marshal is json.Marshal without HTML escaping, so "<" in plugin texts arrives unchanged.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// randomID returns n random bytes as base64url without padding.
func randomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // never fails since Go 1.24
	return base64.RawURLEncoding.EncodeToString(b)
}

// limiter is a per-session token bucket (ADMIN-API §1.7: 600 requests per minute).
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

// ts formats a time as RFC 3339 UTC with milliseconds (ADMIN-API §1.1).
func ts(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// optTS is ts for optional times: nil (omitted) when zero.
func optTS(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	v := ts(t)
	return &v
}
