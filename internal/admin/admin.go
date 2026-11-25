// Package admin serves the hub's own admin API under /api/home: plugins and their tools, admin tool calls with
// progress, jobs, the activity log, the settings, the tunnel, the system info and the event stream (server-sent
// events, with a long-poll fallback for a quick tunnel, which carries no SSE). People, invites, connections, sessions
// and passkeys are MCP Gatekeeper's (packages auth and people), and so is the audit of admin actions, which this
// package writes to (core.AuditEntry in the store's Audit).
//
// A Server lives as long as homed; Routes gives its routes behind the session layer of one gatekeeper stack (the
// public URL's), so a quick tunnel's new URL rebuilds only that layer. Commands are the admin socket's plugin, tool
// and update commands (`homed plugin …`, `homed tool …`, `homed update`).
//
// Who may call what: "owner" is a person of role owner (the superadmin and a co-owner alike); "session" is anyone
// signed in. A member sees their own activity and nothing of the devices here (GET /api/account lists the devices
// they may use).
package admin

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/devXpro/homie/internal/audit"
	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/plugins"
	"github.com/devXpro/homie/internal/store"
	"github.com/devXpro/homie/internal/tunnel"
)

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

// Tunnel is homed's tunnel as the admin sees it (internal/tunnel.Manager).
type Tunnel interface {
	// Status reports the tunnel.
	Status() tunnel.Status
	// Check runs the self-check through the tunnel now.
	Check(ctx context.Context) tunnel.Status
	// Restart starts cloudflared again; a quick tunnel gets a new URL.
	Restart() error
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

// JobError is an installer error with a stable code: checksum_mismatch, no_asset, incompatible, start_failed,
// rolled_back, downgrade, job_running, upstream, internal. Other errors end a job with code "internal".
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

// Installer is the plugin installer and self-updater as the admin sees it. Install, Update, Remove and UpdateHub run
// inside a job: they report steps through the Reporter and do every store and runtime change themselves (store row,
// atomic switch, start, rollback). Errors other than *JobError and ErrNotFound count as upstream failures where
// GitHub is involved. Update and UpdateHub refuse a release older than the installed one (job error "downgrade")
// unless allowDowngrade, which only the admin socket passes.
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
	Store *store.Store
	Host  PluginHost

	Runtime   PluginRuntime // optional
	Installer Installer     // optional: install, update and update checks answer 503 home_not_ready without it
	Tunnel    Tunnel        // optional: the tunnel endpoints answer 503 home_not_ready without it
	// Activity records the tool calls of the admin and the CLI, and its stored records become "activity" events.
	// Optional: nothing is recorded without it.
	Activity *audit.Writer

	// OwnerReady reports whether the superadmin has a passkey of the current public URL (bootstrap's ownerReady).
	// Nil answers false.
	OwnerReady func(ctx context.Context) (bool, error)
	// TunnelMode is the mode the bootstrap tells the SPA (quick: poll instead of SSE); nil reads Tunnel's status.
	TunnelMode func() string
	// OnHomeName is called after the home was renamed (the gateway's MCP title and instructions follow it).
	OnHomeName func(name string)

	// WithProgress returns a context that makes PluginHost.Call report the plugin's progress notifications to fn. Nil
	// means plugins.WithProgress, which is what plugins.Host understands; tests replace it.
	WithProgress func(ctx context.Context, fn func(plugins.Progress)) context.Context

	Version   string // hub version, e.g. "0.2.0"; empty means "dev"
	Commit    string
	StartedAt time.Time
	// LocalName names the admin socket's caller in the activity log (the OS user name).
	LocalName string

	Log *slog.Logger
}

// Server is the admin API. Create it with New, serve Routes, and Close it at shutdown.
type Server struct {
	opts   Options
	st     *store.Store
	host   PluginHost
	log    *slog.Logger
	now    func() time.Time
	events *broker
	limit  *limiter

	ctx    context.Context // lives until Close; detached calls and jobs run under it
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// timing; tests shorten them
	pollEvery    time.Duration // tunnel and plugin state polling for the events
	pingEvery    time.Duration // SSE keep-alive and session re-check
	logsEvery    time.Duration // plugin.log batching
	progressGap  time.Duration // at most one call event per gap per call
	inventoryTTL time.Duration
	longPoll     time.Duration // the most a poll of the events waits

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
	defaultLongPoll     = 20 * time.Second
)

// New returns the admin API. It starts the event watchers; Close stops them and cancels running calls and jobs.
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
		longPoll:     defaultLongPoll,
		calls:        map[string]*callEntry{},
		running:      map[string]string{},
		jobs:         map[string]*jobEntry{},
		inventory:    map[string]inventoryCache{},
		updates:      map[string]updateInfo{},
		schemas:      map[string]*schemaCache{},
	}
	if opts.Activity != nil {
		opts.Activity.OnStored(s.publishActivity)
	}
	if adjust != nil {
		adjust(s)
	}
	s.wg.Add(1)
	go s.watch()
	return s, nil
}

// Close stops the watchers, cancels running calls and jobs, and closes the event streams.
func (s *Server) Close() {
	s.cancel()
	s.events.close()
	s.wg.Wait()
}
