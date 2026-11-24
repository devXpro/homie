// Package gateway serves the hub's public HTTP surface (PLAN.md §5.4): /healthz, the REST façade that ChatGPT Sites
// call (GET /api/v1/tools, POST /api/v1/tools/{name}) and the stateless MCP endpoint /mcp. Every surface uses the same
// bearer person keys, the same per-person plugin scopes, the same per-key rate limit and the same audit.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/keys"
)

// Options configures the gateway.
type Options struct {
	// HomeName is the name Roman gave the home, e.g. «Мой шалаш». It goes into the instructions and the MCP title.
	HomeName string
	// Instructions returns the enabled plugins' instructions, appended to the hub preamble. It is read again every
	// time the tool set changes, so it may follow plugins coming and going. Nil means none.
	Instructions func() string
	// InstanceID is sent as X-Homie-Instance on every response; empty sends nothing.
	InstanceID string
	// Version is the hub version, the first half of the MCP serverInfo.version. Empty means "dev".
	Version string
	// RateLimit is the number of tool calls per minute allowed per key (token bucket). Zero or less means 120.
	RateLimit int
	// Log receives warnings about tools the gateway has to skip. Nil discards them.
	Log *slog.Logger
}

const (
	instanceHeader   = "X-Homie-Instance"
	defaultRateLimit = 120
	maxBodyBytes     = 1 << 20
	principalExtra   = "homie/principal" // key of the principal in auth.TokenInfo.Extra
)

// gateway is the http.Handler New returns.
type gateway struct {
	host    hub.ToolHost
	keys    hub.KeyResolver
	audit   hub.Auditor // may be nil
	opts    Options
	limiter *limiter
	mux     *http.ServeMux
	now     func() time.Time

	mu   sync.Mutex
	snap *snapshot
}

// principal is an authenticated caller.
type principal struct {
	person hub.Person
	bucket string // sha256 of the bearer key: the rate-limit bucket
}

type principalKey struct{}

// Handler is the gateway: the http.Handler of §5.4, whose home name may change while it serves.
type Handler interface {
	http.Handler
	// SetHomeName changes the home name in the instructions and the MCP title; the next request sees it. homed calls
	// it when the owner names or renames the home in the admin.
	SetHomeName(name string)
}

// New returns the handler for §5.4. audit may be nil.
func New(host hub.ToolHost, keyResolver hub.KeyResolver, audit hub.Auditor, opts Options) Handler {
	if opts.RateLimit <= 0 {
		opts.RateLimit = defaultRateLimit
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	g := &gateway{
		host:    host,
		keys:    keyResolver,
		audit:   audit,
		opts:    opts,
		limiter: newLimiter(opts.RateLimit, time.Now),
		mux:     http.NewServeMux(),
		now:     time.Now,
	}

	mcpHandler := mcp.NewStreamableHTTPHandler(g.mcpServer, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// Requests arrive from the relay client on 127.0.0.1 with the public Host header; the SDK's DNS-rebinding
		// guard would refuse every one of them. Bearer keys are what protects this endpoint.
		DisableLocalhostProtection: true,
	})
	// The SDK hands auth.TokenInfo to tool handlers; this carries the principal resolved by g.authenticate there.
	withTokenInfo := auth.RequireBearerToken(func(ctx context.Context, _ string, _ *http.Request) (*auth.TokenInfo, error) {
		p, ok := ctx.Value(principalKey{}).(principal)
		if !ok {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: p.person.ID, Extra: map[string]any{principalExtra: p}}, nil
	}, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})

	g.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	g.mux.Handle("GET /api/v1/tools", g.authenticate(http.HandlerFunc(g.listTools)))
	g.mux.Handle("POST /api/v1/tools/{name}", g.authenticate(http.HandlerFunc(g.callTool)))
	g.mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		// JSON, so a Site never mistakes it for the relay's HTML "home is offline" 404.
		writeError(w, http.StatusNotFound, "not found")
	})
	g.mux.Handle("/mcp", g.authenticate(withTokenInfo(mcpHandler)))
	return g
}

// SetHomeName implements Handler. The tool snapshot is rebuilt on the next request, so the version fingerprint moves
// too and ChatGPT reloads the instructions.
func (g *gateway) SetHomeName(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.opts.HomeName != name {
		g.opts.HomeName = name
		g.snap = nil
	}
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g.opts.InstanceID != "" {
		w.Header().Set(instanceHeader, g.opts.InstanceID)
	}
	g.mux.ServeHTTP(w, r)
}

// authenticate resolves the bearer key to a person and puts the principal in the request context.
func (g *gateway) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := bearer(r)
		if !ok {
			unauthorized(w)
			return
		}
		person, err := g.keys.ResolveKey(r.Context(), key)
		if errors.Is(err, hub.ErrUnknownKey) {
			unauthorized(w)
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		p := principal{person: person, bucket: keys.Hash(key)}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

// bearer extracts the key the same way the go-sdk auth middleware does, so both agree on what a bearer header is.
func bearer(r *http.Request) (string, bool) {
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bearer") {
		return "", false
	}
	return fields[1], true
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, "unauthorized")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := marshal(v)
	if err != nil {
		status, b = http.StatusInternalServerError, []byte(`{"error":"internal error"}`)
	}
	writeRaw(w, status, b)
}

func writeRaw(w http.ResponseWriter, status int, b []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// marshal is json.Marshal without HTML escaping, so text like "<" reaches the model unchanged.
func marshal(v any) ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(b.String(), "\n")), nil
}
