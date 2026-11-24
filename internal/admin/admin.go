// Package admin serves the admin JSON API under /api/admin (docs/ADMIN-API.md): the session and "me" endpoints,
// plugins and their tools, admin tool calls with progress, jobs, the audit log, people and keys, settings, the relay,
// the system info, and the server-sent event stream.
//
// The package does not import internal/auth or internal/install, uses internal/auth/wire only for the shared name rule
// (wire.CheckName) and internal/plugins only for its progress context (plugins.WithProgress). It names the few methods it needs as interfaces below (Auth, PluginHost,
// PluginRuntime, Relay, Installer, KeyUpdater), and the integration (T2.6) adapts the real packages to them.
// Persistence goes through internal/store.
//
// Two handlers share one route table: Handler for the web (session cookie, CSRF header, step-up) and LocalHandler
// for the local unix socket (the owner, Via "cli", no cookie, no CSRF, no step-up; ADMIN-API §6).
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/plugins"
	"github.com/devXpro/homie/internal/relay"
	"github.com/devXpro/homie/internal/store"
)

// Principal is the person behind a request, with the times of their session.
type Principal struct {
	Person        store.Person
	Session       store.Session // zero on the local socket
	ExpiresAt     time.Time     // absolute end of the session (7 days after creation)
	IdleExpiresAt time.Time     // idle end of the session (12 hours after the last request)
	StepUpUntil   time.Time     // the session counts as stepped up before this time; zero when it never was
}

// ErrUnauthenticated is what Auth.Authenticate returns when the request has no valid session.
var ErrUnauthenticated = errors.New("admin: no valid session")

// Auth is what the admin API needs from internal/auth (T2.2). T2.6 adapts the auth package to it.
type Auth interface {
	// Authenticate returns the principal of the request's session cookie (__Host-homie_session), or ErrUnauthenticated
	// when there is none or it expired. It owns the expiry policy and may touch the session's LastSeenAt.
	Authenticate(r *http.Request) (Principal, error)
}

// PluginHost is the plugin host as the admin sees it: hub.ToolHost plus the plugin logs (plugins.Host has both).
// A host that also has PluginInstructions(id string) string fills PluginDetail.instructions.
type PluginHost interface {
	hub.ToolHost
	// Logs returns the last stderr lines of a plugin, oldest first; nil for an unknown id.
	Logs(id string) []string
}

// PluginRuntime starts and stops plugins after the admin changed store.Plugins. Nil means the store change takes
// effect at the next start of homed.
type PluginRuntime interface {
	// Apply makes the running plugins match store.Plugins: start new and enabled ones, stop disabled and removed ones.
	Apply(ctx context.Context) error
	// Restart stops and starts one plugin.
	Restart(ctx context.Context, id string) error
}

// SelfCheck is the outcome of the relay self-check (PLAN §5.5).
type SelfCheck struct {
	OK        bool
	CheckedAt time.Time // zero: never ran
	Error     string    // English, why it failed
}

// Relay is the relay client as the admin sees it.
type Relay interface {
	// Domain is the configured relay domain; empty when homed.yaml has no relay.
	Domain() string
	// Status reports the connection.
	Status() relay.Status
	// LastCheck returns the latest self-check result.
	LastCheck() SelfCheck
	// CheckNow runs the self-check now and returns its result.
	CheckNow(ctx context.Context) SelfCheck
	// Connect stores the code's domain and token and reconnects. The admin has already checked the domain rule.
	Connect(ctx context.Context, code relay.Code) error
}

// KeyUpdater rebuilds the gateway's key set from the store after every key or person change (cmd/homed's key
// resolver). Rebuild must read the store and swap the set under one lock that every other rebuild (SIGHUP, a
// registration) takes too: a rebuild that read the store before a revoke must never land after it and bring the
// revoked key back.
type KeyUpdater interface {
	Rebuild(ctx context.Context) error
}

// JobReport is one progress report of a running job.
type JobReport struct {
	Step    string // one of the job's steps; empty keeps the current one
	Done    int64  // bytes or items done; with Total, shown as progress
	Total   int64
	Message string // short English text, e.g. "homie-tv 0.2.0"
}

// Reporter receives a job's progress. It is safe to call from any goroutine.
type Reporter func(JobReport)

// JobError is an installer error with a stable code (ADMIN-API §2, Job): checksum_mismatch, no_asset, incompatible,
// start_failed, rolled_back, upstream, internal. Other errors end a job with code "internal".
type JobError struct {
	Code    string
	Message string
}

func (e *JobError) Error() string { return e.Code + ": " + e.Message }

// ErrNotFound is what an Installer returns from Preview when the repo or release does not exist.
var ErrNotFound = errors.New("admin: not found")

// PluginPreview is the GET /plugins/preview answer, built from a release's homie-plugin.json.
type PluginPreview struct {
	Repo          string     `json:"repo"`
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	Description   string     `json:"description,omitempty"`
	Version       string     `json:"version"`
	PublishedAt   *time.Time `json:"publishedAt,omitempty"`
	Homepage      string     `json:"homepage,omitempty"`
	MinHubVersion string     `json:"minHubVersion,omitempty"`
	Compatible    bool       `json:"compatible"`
	AssetFound    bool       `json:"assetFound"`
	Installed     *struct {
		Version string `json:"version"`
	} `json:"installed,omitempty"` // set by the admin from the store
}

// HubRelease is the latest hub release.
type HubRelease struct {
	Version  string
	NotesURL string
}

// Installer is the plugin installer and self-updater (T2.5) as the admin sees it. Install, Update, Remove and
// UpdateHub run inside a job: they report steps through the Reporter and do every store and runtime change
// themselves (store row, atomic switch, start, rollback). Errors other than *JobError and ErrNotFound count as
// upstream failures where GitHub is involved. Update and UpdateHub refuse a release older than the installed one
// (job error "downgrade") unless allowDowngrade, which the admin passes only for an explicit request on the socket.
type Installer interface {
	Preview(ctx context.Context, repo, version string) (PluginPreview, error)
	Install(ctx context.Context, repo, version, id string, report Reporter) (pluginID string, err error)
	Update(ctx context.Context, id, version string, allowDowngrade bool, report Reporter) error
	Remove(ctx context.Context, id string, keepData bool, report Reporter) error
	// LatestVersion returns the latest release version of a plugin repo.
	LatestVersion(ctx context.Context, repo string) (string, error)
	// LatestHub returns the latest hub release.
	LatestHub(ctx context.Context) (HubRelease, error)
	// UpdateHub replaces the homed binary; homed exits afterwards and the service manager restarts it.
	UpdateHub(ctx context.Context, version string, allowDowngrade bool, report Reporter) error
}

// Options configures the admin API. Store and Host are required.
type Options struct {
	Store store.Store
	Host  PluginHost
	Auth  Auth // required for Handler; LocalHandler works without it

	Runtime   PluginRuntime // optional
	Relay     Relay         // optional: relay endpoints answer 503 home_not_ready without it
	Installer Installer     // optional: install, update and update checks answer 503 home_not_ready without it
	Keys      KeyUpdater    // optional: rebuilt after every key change
	Audit     *AuditWriter  // optional: tool calls and admin actions are not recorded without it

	// WithProgress returns a context that makes PluginHost.Call report the plugin's progress notifications to fn
	// (ADMIN-API §8). Nil means plugins.WithProgress, which is what plugins.Host understands; tests replace it.
	WithProgress func(ctx context.Context, fn func(plugins.Progress)) context.Context
	// OnSettings is called after the settings changed (the gateway's home name and instructions follow it).
	OnSettings func(store.HomeSettings)
	// UsablePasskey reports whether a passkey can sign in now: it belongs to the current relying party (the relay
	// domain). Only those count for bootstrap's ownerReady and the last-passkey rule; passkeys of an old relay domain
	// open nothing. Nil counts every passkey. homed passes auth.Auth.UsablePasskey.
	UsablePasskey func(store.Passkey) bool

	// Origin is the only allowed Origin of a mutating web request, e.g. "https://homie.jobbyjobs.work".
	Origin string
	// OriginFunc, when set, replaces Origin and is read on every request, like the passkey relying party: a relay
	// connected while homed runs (the first-run page, `homed relay connect`) is the admin's origin at once.
	OriginFunc func() string
	// InstanceID goes into X-Homie-Instance on every response; empty sends nothing.
	InstanceID string
	Version    string // hub version, e.g. "0.2.0"; empty means "dev"
	Commit     string
	StartedAt  time.Time
	// LocalName is the socket principal's name before an owner exists (the OS user name).
	LocalName string

	// AuthRoutes serves the passkey ceremonies of T2.2: /api/admin/auth/* and POST /api/admin/me/passkeys/begin and
	// /finish. The admin applies its headers, the CSRF rule, and for the passkey ceremonies the session and
	// step-up checks, before handing the request over. Nil answers 404.
	AuthRoutes http.Handler
	// LocalRoutes serves the socket-only /api/admin/local/* endpoints of T2.2 (setup-link, reset-access). Nil
	// answers 404.
	LocalRoutes http.Handler

	Log *slog.Logger
}

// Server is the admin API. Create it with New, serve Handler and LocalHandler, and Close it at shutdown.
type Server struct {
	opts   Options
	st     store.Store
	host   PluginHost
	log    *slog.Logger
	now    func() time.Time
	events *broker
	limit  *limiter

	web   http.Handler
	local http.Handler

	ctx    context.Context // lives until Close; detached calls and jobs run under it
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// timing; tests shorten them
	pollEvery    time.Duration // relay and plugin state polling for SSE
	pingEvery    time.Duration // SSE keep-alive and session re-check
	logsEvery    time.Duration // plugin.log batching
	progressGap  time.Duration // at most one call event per gap per call
	inventoryTTL time.Duration

	mu        sync.Mutex
	calls     map[string]*callEntry
	running   map[string]string // tool name → running call id
	jobs      map[string]*jobEntry
	inventory map[string]inventoryCache
	updates   map[string]updateInfo // plugin id → latest release
	hubUpdate *updateInfo
	schemas   map[string]*schemaCache
}

const (
	defaultPollEvery    = time.Second
	defaultPingEvery    = 20 * time.Second
	defaultLogsEvery    = 250 * time.Millisecond
	defaultProgressGap  = 250 * time.Millisecond
	defaultInventoryTTL = 10 * time.Second
)

// New returns the admin API. It starts the SSE watchers; Close stops them and cancels running calls and jobs.
func New(opts Options) (*Server, error) { return newServer(opts, nil) }

// newServer is New with a hook that adjusts the server (tests: timings, clock) before the watchers start.
func newServer(opts Options, adjust func(*Server)) (*Server, error) {
	if opts.Store == nil || opts.Host == nil {
		return nil, errors.New("admin: Store and Host are required")
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.StartedAt.IsZero() {
		opts.StartedAt = time.Now()
	}
	if opts.LocalName == "" {
		opts.LocalName = "local"
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.WithProgress == nil {
		opts.WithProgress = plugins.WithProgress
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		opts:         opts,
		st:           opts.Store,
		host:         opts.Host,
		log:          opts.Log,
		now:          time.Now,
		events:       newBroker(500),
		limit:        newLimiter(600, time.Minute),
		ctx:          ctx,
		cancel:       cancel,
		pollEvery:    defaultPollEvery,
		pingEvery:    defaultPingEvery,
		logsEvery:    defaultLogsEvery,
		progressGap:  defaultProgressGap,
		inventoryTTL: defaultInventoryTTL,
		calls:        map[string]*callEntry{},
		running:      map[string]string{},
		jobs:         map[string]*jobEntry{},
		inventory:    map[string]inventoryCache{},
		updates:      map[string]updateInfo{},
		schemas:      map[string]*schemaCache{},
	}
	if opts.Audit != nil {
		opts.Audit.setOnStored(s.publishAudit)
	}
	if adjust != nil {
		adjust(s)
	}
	s.web, s.local = s.routes()
	s.wg.Add(1)
	go s.watch()
	return s, nil
}

// Handler serves /api/admin/* for the web: session cookie, CSRF header and step-up rules apply.
func (s *Server) Handler() http.Handler { return s.web }

// LocalHandler serves /api/admin/* on the local unix socket (ADMIN-API §6).
func (s *Server) LocalHandler() http.Handler { return s.local }

// SessionRevoked tells the event streams of a session that it ended (logout, revoke, expiry): they send
// session.revoked and close. internal/auth calls it through T2.6 after deleting sessions itself.
func (s *Server) SessionRevoked(sessionID string) {
	s.events.revokeSession(sessionID)
}

// Close stops the watchers, cancels running calls and jobs, and closes the event streams.
func (s *Server) Close() {
	s.cancel()
	s.events.close()
	s.wg.Wait()
}
