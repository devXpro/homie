// Package gateway serves the hub's MCP endpoint, /mcp: the whole home as one MCP resource, "home", for ChatGPT and
// Claude. It is stateless and answers JSON (go-sdk Stateless + JSONResponse), so it needs no session state and no
// server-sent events.
//
// MCP Gatekeeper's access check (mcpguard) runs in front of it: Routes mounts exactly /mcp behind
// Guard.MiddlewareFor("home"), so only a live OAuth access token for the home, of a person who is active, gets in. The
// gateway then:
//   - takes a place among the person's calls in flight (Guard.AdmitAccess: 8 per person);
//   - shows each person the tools their permissions allow (core.Person.Permissions: core.PermissionAll, or
//     "plugin:<id>" per plugin; an owner has all; none means nothing), on an MCP server built per permission set, whose
//     version fingerprint and instructions follow the tools;
//   - limits tool calls per grant (120 a minute), caps a call at 90 s (Cloudflare cuts an answer after about 100 s),
//     tells the plugin who asked (hub.Caller: the person, Via "chatgpt" or "claude") and records every call in the
//     activity log with redacted arguments.
package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/devXpro/mcp-gatekeeper/core"
	"github.com/devXpro/mcp-gatekeeper/mcpguard"

	"github.com/devXpro/homie/internal/hub"
)

// The home as an MCP resource: one path, one resource id.
const (
	// ResourceID is the gatekeeper resource of the home.
	ResourceID core.ResourceID = "home"
	// Path is the MCP endpoint.
	Path = "/mcp"
)

// Paths maps the home to /mcp and back (core.SingleResource).
var Paths = core.SingleResource{At: Path, ID: ResourceID}

// PluginPermission is the permission id of a plugin: "plugin:<id>".
func PluginPermission(pluginID string) string { return pluginPrefix + pluginID }

const pluginPrefix = "plugin:"

// Options configures the gateway.
type Options struct {
	// HomeName is the home's name (the account's). It goes into the instructions and the MCP title.
	HomeName string
	// Instructions returns the enabled plugins' instructions, appended to the hub preamble. It is read again every
	// time the tool set changes. Nil means none.
	Instructions func() string
	// Version is the hub version, the first half of the MCP serverInfo.version. Empty means "dev".
	Version string
	// RateLimit is the number of tool calls per minute allowed per grant (token bucket). Zero or less means 120.
	RateLimit int
	// MaxToolTimeout caps how long an assistant's tool call may run; zero or less means 90 s, under Cloudflare's cut.
	MaxToolTimeout time.Duration
	// Log receives warnings about tools the gateway has to skip. Nil discards them.
	Log *slog.Logger
}

const (
	defaultRateLimit      = 120
	defaultMaxToolTimeout = 90 * time.Second
	principalExtra        = "homie/principal" // key of the principal in auth.TokenInfo.Extra
)

// Gateway is the MCP endpoint. Its home name may change while it serves.
type Gateway struct {
	host    hub.ToolHost
	audit   hub.Auditor // may be nil
	opts    Options
	limiter *limiter
	now     func() time.Time
	mcp     http.Handler

	mu   sync.Mutex
	snap *snapshot
}

// principal is the caller of one request: the person and the access their token stands for.
type principal struct {
	person core.Person
	access core.AccessInfo
}

type principalKey struct{}

// New returns the gateway. audit may be nil.
func New(host hub.ToolHost, audit hub.Auditor, opts Options) *Gateway {
	if opts.RateLimit <= 0 {
		opts.RateLimit = defaultRateLimit
	}
	if opts.MaxToolTimeout <= 0 {
		opts.MaxToolTimeout = defaultMaxToolTimeout
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	g := &Gateway{host: host, audit: audit, opts: opts, limiter: newLimiter(opts.RateLimit, time.Now), now: time.Now}
	handler := mcp.NewStreamableHTTPHandler(g.mcpServer, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// Requests arrive from cloudflared on 127.0.0.1 with the public Host header; the SDK's DNS-rebinding guard
		// would refuse every one of them. The access token is what protects this endpoint (and the hub's Host guard).
		DisableLocalhostProtection: true,
	})
	// The SDK hands auth.TokenInfo to tool handlers; this carries the principal the gatekeeper let through there.
	withTokenInfo := auth.RequireBearerToken(func(ctx context.Context, _ string, _ *http.Request) (*auth.TokenInfo, error) {
		p, ok := ctx.Value(principalKey{}).(principal)
		if !ok {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: string(p.person.ID), Expiration: p.access.ExpiresAt,
			Extra: map[string]any{principalExtra: p}}, nil
	}, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})
	g.mcp = withTokenInfo(handler)
	return g
}

// SetHomeName changes the home name in the instructions and the MCP title; the next request sees it. homed calls it
// when the home is renamed.
func (g *Gateway) SetHomeName(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.opts.HomeName != name {
		g.opts.HomeName = name
		g.snap = nil
	}
}

// Routes mounts exactly /mcp behind guard's access check for the home.
func (g *Gateway) Routes(guard *mcpguard.Guard) core.RouteRegistrar {
	return routes{g: g, guard: guard}
}

type routes struct {
	g     *Gateway
	guard *mcpguard.Guard
}

// RegisterRoutes implements core.RouteRegistrar.
func (rt routes) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle(Path, rt.guard.MiddlewareFor(ResourceID)(rt.g.serve(rt.guard)))
}

// serve runs one MCP request of the caller the guard let through, inside the caller's places in flight.
func (g *Gateway) serve(guard *mcpguard.Guard) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info, ok := mcpguard.AccessFrom(r.Context())
		person, okp := mcpguard.PersonFrom(r.Context())
		if !ok || !okp {
			http.Error(w, "no caller", http.StatusInternalServerError) // a wiring mistake: the guard is in front
			return
		}
		release, ok := guard.AdmitAccess(w, info)
		if !ok {
			return
		}
		defer release()
		ctx := context.WithValue(r.Context(), principalKey{}, principal{person: person, access: info})
		g.mcp.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Resource is the home as the OAuth consent screen shows it: its name, and the assistants' tools.
func (g *Gateway) Resource() core.ResourceInfo { return g.resourceFor("*") }

// resourceFor is Resource with the tools of scope only (scopeOf: "*" for all).
func (g *Gateway) resourceFor(scope string) core.ResourceInfo {
	s := g.current()
	out := core.ResourceInfo{ID: ResourceID, Name: s.homeName, Description: "Your home's devices: what the assistant may do for you."}
	if out.Name == "" {
		out.Name = "Homie"
	}
	for _, t := range s.tools {
		if !allowed(scope, t.PluginID) {
			continue
		}
		access := "write"
		if readOnly(t) {
			access = "read"
		}
		title := t.Title
		if title == "" {
			title = t.Name
		}
		out.Tools = append(out.Tools, core.ToolInfo{Name: t.Name, Title: title, Access: access})
	}
	return out
}

// Catalog is the gatekeeper's resource catalog of the hub: one resource, the home. Any account of the hub may use it
// (a hub has one).
func (g *Gateway) Catalog() core.ResourceCatalog { return catalog{g} }

type catalog struct{ g *Gateway }

// Resource implements core.ResourceCatalog. With a viewer in ctx (the consent screen) it lists only the tools the
// viewer may use, so a guest never learns what else the home has; without one (the resource metadata) every tool.
func (c catalog) Resource(ctx context.Context, id core.ResourceID) (core.ResourceInfo, error) {
	if id != ResourceID {
		return core.ResourceInfo{}, core.ErrNotFound
	}
	if v, ok := core.ViewerFrom(ctx); ok {
		return c.g.resourceFor(scopeOf(v.Person)), nil
	}
	return c.g.Resource(), nil
}

// Available implements core.ResourceCatalog.
func (c catalog) Available(_ context.Context, a core.AccountID, id core.ResourceID) (bool, error) {
	return id == ResourceID && a != "", nil
}

// scopeOf normalizes a person's permissions: "*" for everything (an owner, or core.PermissionAll), else the sorted
// unique plugin ids joined by commas; "" sees nothing.
func scopeOf(p core.Person) string {
	if p.Role == core.RoleOwner || slices.Contains(p.Permissions, core.PermissionAll) {
		return "*"
	}
	var ids []string
	for _, perm := range p.Permissions {
		if id, ok := strings.CutPrefix(perm, pluginPrefix); ok && id != "" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return strings.Join(slices.Compact(ids), ",")
}

func allowed(scope, pluginID string) bool {
	return scope == "*" || scope != "" && slices.Contains(strings.Split(scope, ","), pluginID)
}
