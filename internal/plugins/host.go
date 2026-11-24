// Package plugins runs the hub's plugins and calls their tools. It implements hub.ToolHost.
//
// Every plugin is a separate executable speaking MCP over stdio (docs/PLUGINS.md). The host starts each one with a
// minimal environment and its own data directory, keeps its stderr as the plugin log, lists its tools, restarts it
// with backoff when it crashes, and stops it with SIGTERM, then SIGKILL.
//
// The tool snapshot keeps a plugin's last known tools while the plugin is restarting or failing, so a crash does not
// make tools vanish from assistants; calling them returns hub.ErrPluginDown until the plugin is back.
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/pluginkit"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Spec says how to run one plugin.
type Spec struct {
	ID      string   // plugin id, ^[a-z][a-z0-9]{1,15}$
	Command []string // executable and arguments, e.g. ["/path/homie-tv", "serve"]
}

// Host runs plugins and implements hub.ToolHost.
type Host struct {
	// HubVersion goes to plugins as HOMIE_HUB_VERSION and as the MCP clientInfo version. Set it before Start.
	HubVersion string

	log     *slog.Logger
	dataDir string
	timing  timing

	// lifecycle serializes Add, Remove, Restart and Sync, which stop a supervisor and wait for it outside mu.
	lifecycle sync.Mutex

	mu       sync.Mutex
	plugins  []*plugin     // sorted by id; changed by Add, Remove and Sync
	changed  chan struct{} // closed at the next change of the tool set
	statusCh chan struct{} // closed at the next status change (tests wait on it)
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	wg       sync.WaitGroup

	progressSeq uint64                   // last progress token number
	progress    map[string]*progressSink // by progress token, while the call runs
}

// timing holds the supervision delays; tests shorten them.
type timing struct {
	backoffMin   time.Duration // first restart delay
	backoffMax   time.Duration // restart delay cap
	crashWindow  time.Duration // crashes counted within this window...
	crashLimit   int           // ...make the plugin failing at this count
	failingRetry time.Duration // restart delay while failing
	stableAfter  time.Duration // a run this long resets backoff and the failing state
	killGrace    time.Duration // SIGTERM → SIGKILL
	rpcTimeout   time.Duration // initialize and tools/list
}

var defaultTiming = timing{
	backoffMin:   time.Second,
	backoffMax:   time.Minute,
	crashWindow:  5 * time.Minute,
	crashLimit:   5,
	failingRetry: 5 * time.Minute,
	stableAfter:  time.Minute,
	killGrace:    5 * time.Second,
	rpcTimeout:   30 * time.Second,
}

// plugin is one supervised plugin. Fields below logs are guarded by Host.mu; command changes only while no
// supervisor runs.
type plugin struct {
	id      string
	command []string
	dir     string // HOMIE_DATA_DIR and cwd
	logs    *logSink

	stop   context.CancelFunc // stops the current supervisor; nil while none runs
	done   chan struct{}      // closed when the current supervisor has returned
	failed bool               // the latest run failed and none has come up since the supervisor started

	state        hub.PluginState
	lastErr      string
	since        time.Time
	title        string
	version      string
	instructions string
	tools        []hub.Tool         // last known, sorted by name
	session      *mcp.ClientSession // non-nil while up
}

var pluginIDRe = regexp.MustCompile(`^[a-z][a-z0-9]{1,15}$`)

var _ hub.ToolHost = (*Host)(nil)

// New validates the specs and prepares the data directories. Plugins start with Start.
func New(specs []Spec, dataDir string, log *slog.Logger) (*Host, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	h := &Host{
		HubVersion: buildVersion(),
		log:        log,
		dataDir:    dataDir,
		timing:     defaultTiming,
		changed:    make(chan struct{}),
		statusCh:   make(chan struct{}),
	}
	if err := mkdir0700(h.logDir()); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, s := range specs {
		p, err := h.newPlugin(s)
		if err != nil {
			return nil, err
		}
		if seen[s.ID] {
			return nil, fmt.Errorf("plugin id %q is used twice", s.ID)
		}
		seen[s.ID] = true
		h.plugins = append(h.plugins, p)
	}
	sortPlugins(h.plugins)
	return h, nil
}

func (h *Host) logDir() string { return filepath.Join(h.dataDir, "logs") }

// newPlugin validates a spec and returns its stopped plugin.
func (h *Host) newPlugin(s Spec) (*plugin, error) {
	if !pluginIDRe.MatchString(s.ID) {
		return nil, fmt.Errorf("plugin id %q must match %s", s.ID, pluginIDRe)
	}
	command, err := absCommand(s)
	if err != nil {
		return nil, err
	}
	return &plugin{
		id:      s.ID,
		command: command,
		dir:     filepath.Join(h.dataDir, "plugins", s.ID, "data"),
		logs:    newLogSink(filepath.Join(h.logDir(), s.ID+".log"), h.log.With("plugin", s.ID)),
		state:   hub.PluginStopped,
		since:   time.Now(),
	}, nil
}

// absCommand checks the spec's command and resolves a relative path to the binary against our working directory, as
// the plugin runs in its data dir.
func absCommand(s Spec) ([]string, error) {
	if len(s.Command) == 0 || s.Command[0] == "" {
		return nil, fmt.Errorf("plugin %q: empty command", s.ID)
	}
	command := slices.Clone(s.Command)
	if strings.ContainsRune(command[0], filepath.Separator) && !filepath.IsAbs(command[0]) {
		abs, err := filepath.Abs(command[0])
		if err != nil {
			return nil, err
		}
		command[0] = abs
	}
	return command, nil
}

func sortPlugins(list []*plugin) {
	slices.SortFunc(list, func(a, b *plugin) int { return strings.Compare(a.id, b.id) })
}

func buildVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func mkdir0700(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700) //nolint:gosec // a directory needs the x bit; 0700 is owner-only
}

// Start launches every plugin. Plugins stop when ctx is done or Close is called. Start does nothing after the first
// call or after Close.
func (h *Host) Start(ctx context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancel != nil || h.closed {
		return
	}
	h.ctx, h.cancel = context.WithCancel(ctx)
	for _, p := range h.plugins {
		h.superviseLocked(p)
	}
}

// superviseLocked starts a supervisor for p. h.mu is held; the host has started and is not closed.
func (h *Host) superviseLocked(p *plugin) {
	ctx, stop := context.WithCancel(h.ctx)
	done := make(chan struct{})
	p.stop, p.done, p.failed = stop, done, false
	h.wg.Add(1)
	go func() {
		defer close(done)
		h.supervise(ctx, p)
	}()
}

// Close stops every plugin (closing the session, then SIGTERM, then SIGKILL after 5 s) and waits for them.
func (h *Host) Close() {
	h.mu.Lock()
	h.closed = true
	cancel := h.cancel
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	h.wg.Wait()
	h.mu.Lock()
	list := slices.Clone(h.plugins)
	h.mu.Unlock()
	for _, p := range list {
		p.logs.close()
	}
}

// Tools returns a snapshot of the tools visible to the audience, sorted by name.
func (h *Host) Tools(audience hub.Audience) []hub.Tool {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []hub.Tool
	for _, p := range h.plugins {
		for _, t := range p.tools {
			if audience == hub.AudienceAdmin || t.Audience == audience {
				out = append(out, t)
			}
		}
	}
	slices.SortFunc(out, func(a, b hub.Tool) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Changed returns a channel that is closed at the next change of the tool set or of the plugins' instructions.
func (h *Host) Changed() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.changed
}

// Instructions returns the instructions of every plugin that has any, in plugin id order, separated by blank lines.
// The gateway appends them to the hub preamble. A change fires Changed.
func (h *Host) Instructions() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var parts []string
	for _, p := range h.plugins {
		if s := strings.TrimSpace(p.instructions); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

// Status reports every plugin, sorted by id.
func (h *Host) Status() []hub.PluginStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]hub.PluginStatus, 0, len(h.plugins))
	for _, p := range h.plugins {
		title := p.title
		if title == "" {
			title = p.id
		}
		out = append(out, hub.PluginStatus{
			ID:        p.id,
			Title:     title,
			Version:   p.version,
			State:     p.state,
			LastError: p.lastErr,
			Since:     p.since,
		})
	}
	return out
}

// Logs returns the last lines (at most 1000) a plugin wrote to stderr, oldest first, with the host's own events
// prefixed by "homie:". It returns nil for an unknown id.
func (h *Host) Logs(id string) []string {
	h.mu.Lock()
	p := h.findLocked(id)
	h.mu.Unlock()
	if p == nil {
		return nil
	}
	return p.logs.lines()
}

// findLocked returns the plugin with the id, or nil. h.mu is held.
func (h *Host) findLocked(id string) *plugin {
	for _, p := range h.plugins {
		if p.id == id {
			return p
		}
	}
	return nil
}

// Call runs a tool. See hub.ToolHost. A context from WithProgress also receives the plugin's progress reports.
func (h *Host) Call(ctx context.Context, caller hub.Caller, name string, args json.RawMessage) (hub.Result, error) {
	tool, session, err := h.lookup(name)
	if err != nil {
		return hub.Result{}, err
	}
	arguments, err := objectArgs(args)
	if err != nil {
		return hub.Result{}, err
	}
	params := &mcp.CallToolParams{
		Meta:      mcp.Meta{pluginkit.MetaCaller: caller.Person, pluginkit.MetaVia: caller.Via},
		Name:      name,
		Arguments: arguments,
	}
	if fn := progressFunc(ctx); fn != nil {
		token, stop := h.watchProgress(tool.PluginID, fn)
		defer stop()
		params.SetProgressToken(token)
	}
	ctx, cancel := context.WithTimeout(ctx, tool.Timeout)
	defer cancel()
	res, err := session.CallTool(ctx, params)
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return hub.Result{}, fmt.Errorf("%s: %w", name, ctx.Err())
	case errors.Is(err, mcp.ErrConnectionClosed), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		// The plugin went away while the call was in flight.
		return hub.Result{}, fmt.Errorf("%s: %w", name, hub.ErrPluginDown)
	default:
		return hub.Result{}, fmt.Errorf("%s: %w", name, err)
	}
	return toResult(res)
}

func (h *Host) lookup(name string) (hub.Tool, *mcp.ClientSession, error) {
	id, _, _ := strings.Cut(name, "_")
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.plugins {
		if p.id != id {
			continue
		}
		i, found := slices.BinarySearchFunc(p.tools, name, func(t hub.Tool, n string) int { return strings.Compare(t.Name, n) })
		if !found {
			break
		}
		if p.state != hub.PluginUp || p.session == nil {
			return hub.Tool{}, nil, fmt.Errorf("%s: %w", name, hub.ErrPluginDown)
		}
		return p.tools[i], p.session, nil
	}
	return hub.Tool{}, nil, fmt.Errorf("%s: %w", name, hub.ErrUnknownTool)
}

// objectArgs checks that args is a JSON object; empty args mean {}.
func objectArgs(args json.RawMessage) (json.RawMessage, error) {
	if len(args) == 0 {
		return json.RawMessage("{}"), nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(args, &m); err != nil || m == nil {
		return nil, errors.New("tool arguments must be a JSON object")
	}
	return args, nil
}

func toResult(res *mcp.CallToolResult) (hub.Result, error) {
	var texts []string
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			texts = append(texts, t.Text)
		}
	}
	out := hub.Result{Text: strings.Join(texts, "\n"), IsError: res.IsError}
	if res.StructuredContent != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			return hub.Result{}, fmt.Errorf("structured content: %w", err)
		}
		out.Structured = b
	}
	return out, nil
}

// setState records a life state change. A plugin that leaves "up" loses its session but keeps its last known tools.
func (h *Host) setState(p *plugin, state hub.PluginState, lastErr string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.setStateLocked(p, state, lastErr)
}

// setFailed records a run that ended in failure: the state it waits in for the restart, and Await's verdict.
func (h *Host) setFailed(p *plugin, state hub.PluginState, lastErr string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p.failed = true
	h.setStateLocked(p, state, lastErr)
}

func (h *Host) setStateLocked(p *plugin, state hub.PluginState, lastErr string) {
	if state != hub.PluginUp {
		p.session = nil
	}
	if p.state != state {
		p.since = time.Now()
	}
	p.state = state
	p.lastErr = lastErr
	h.fireStatusLocked()
}

// setUp marks a plugin up with its identity and tools.
func (h *Host) setUp(p *plugin, cs *mcp.ClientSession, info *mcp.InitializeResult, tools []hub.Tool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p.session = cs
	p.state = hub.PluginUp
	p.lastErr = ""
	p.failed = false
	p.since = time.Now()
	p.title, p.version = "", ""
	if si := info.ServerInfo; si != nil {
		p.title = si.Title
		if p.title == "" {
			p.title = si.Name
		}
		p.version = si.Version
	}
	if p.instructions != info.Instructions {
		p.instructions = info.Instructions
		h.fireChangedLocked()
	}
	h.setToolsLocked(p, tools)
	h.fireStatusLocked()
}

func (h *Host) setTools(p *plugin, tools []hub.Tool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.setToolsLocked(p, tools)
}

func (h *Host) setToolsLocked(p *plugin, tools []hub.Tool) {
	if reflect.DeepEqual(p.tools, tools) {
		return
	}
	p.tools = tools
	h.fireChangedLocked()
}

func (h *Host) fireChangedLocked() {
	close(h.changed)
	h.changed = make(chan struct{})
}

func (h *Host) fireStatusLocked() {
	close(h.statusCh)
	h.statusCh = make(chan struct{})
}

// statusChanged returns a channel closed at the next status change.
func (h *Host) statusChanged() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.statusCh
}

// pluginEnv is the minimal environment of §5.1.
func (h *Host) pluginEnv(p *plugin) []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "LANG", "TZ"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env,
		pluginkit.EnvPluginID+"="+p.id,
		pluginkit.EnvDataDir+"="+p.dir,
		pluginkit.EnvLogLevel+"="+logLevel(h.log),
		pluginkit.EnvHubVersion+"="+h.HubVersion,
	)
}

func logLevel(l *slog.Logger) string {
	ctx := context.Background()
	switch {
	case l.Enabled(ctx, slog.LevelDebug):
		return "debug"
	case l.Enabled(ctx, slog.LevelInfo):
		return "info"
	case l.Enabled(ctx, slog.LevelWarn):
		return "warn"
	default:
		return "error"
	}
}
