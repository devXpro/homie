package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"

	"github.com/devXpro/mcp-gatekeeper/adminsock"
	"github.com/devXpro/mcp-gatekeeper/core"

	"github.com/devXpro/homie/internal/admin"
	"github.com/devXpro/homie/internal/audit"
	"github.com/devXpro/homie/internal/config"
	"github.com/devXpro/homie/internal/firstrun"
	"github.com/devXpro/homie/internal/gateway"
	"github.com/devXpro/homie/internal/plugins"
	"github.com/devXpro/homie/internal/store"
	"github.com/devXpro/homie/internal/tunnel"
	"github.com/devXpro/homie/internal/web"
)

const shutdownTimeout = 5 * time.Second

// The activity log keeps 180 days; older records are pruned at start and then once a day.
const (
	activityKeep       = 180 * 24 * time.Hour
	activityPruneEvery = 24 * time.Hour
)

// ownerCheckEvery is how often homed looks whether the owner can sign in, to close the first-run page.
const ownerCheckEvery = 3 * time.Second

// errRestart ends serve after a hub update. main exits with install.ExitCodeRestart (75), and the service manager
// (launchd KeepAlive, systemd Restart=) starts the new binary.
var errRestart = errors.New("homed was updated; exiting so the service manager starts the new binary")

// runOptions are the `homed run` settings.
type runOptions struct {
	dataDir     string
	listen      string    // overrides homed.yaml listen
	setupListen string    // overrides homed.yaml setup_listen
	devOrigin   string    // development: the admin's origin on localhost; no tunnel
	terminal    io.Writer // where the first-run PIN is printed; nil when homed has no terminal (it is never printed)
	ready       func(*daemon)

	// Tests only.
	clock       core.Clock   // the gatekeeper's and the store's time
	probeClient *http.Client // the tunnel's self-check through "Cloudflare"
	listenLAN   string       // the first-run page's default addresses
	listenLocal string
}

// daemon is a running homed.
type daemon struct {
	dir         string
	log         *slog.Logger
	instanceID  string
	clock       core.Clock
	dev         *core.Site // --dev-origin
	st          *store.Store
	host        *plugins.Host
	runtime     *pluginRuntime
	activity    *audit.Writer
	gw          *gateway.Gateway
	admin       *admin.Server
	tun         *tunnel.Manager
	ui          core.RouteRegistrar
	firstRunUI  http.Handler
	bare        http.Handler
	httpAddr    net.Addr
	setupListen string    // setup_listen from the command line or homed.yaml; "" picks the defaults below
	listenLAN   string    // the first-run page's default without a tunnel (firstrun.ListenLAN; tests: a free port)
	listenLocal string    // its default with one (firstrun.ListenLocal)
	terminal    io.Writer // the first-run PIN goes here; nil without a terminal

	stack  atomic.Pointer[stack] // the gatekeeper of the current public URL; nil without one
	swapMu sync.Mutex            // one swap at a time

	restartOnce sync.Once
	restartCh   chan struct{}

	mu            sync.Mutex
	runCtx        context.Context // set once serving; the first-run page lives under it
	wg            *sync.WaitGroup // serve waits for the first-run page with the rest
	firstRun      *firstRunPage   // the open first-run page; nil when it is closed
	firstRunTimer *time.Timer
}

func cmdRun(args []string, stderr io.Writer) error {
	fl, dataDir := newFlags("run", stderr)
	listen := fl.String("listen", "", "loopback address to serve on (default: homed.yaml listen, else "+config.DefaultListen+")")
	setupListen := fl.String("setup-listen", "", "first-run page address (default: homed.yaml setup_listen, else "+
		firstrun.ListenLAN+" while no tunnel is configured and "+firstrun.ListenLocal+" with one)")
	devOrigin := fl.String("dev-origin", "",
		"development only: the admin's origin, e.g. http://localhost:5173; passkeys for localhost, no tunnel")
	if err := noArgs(fl, args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, runOptions{
		dataDir: *dataDir, listen: *listen, setupListen: *setupListen, devOrigin: *devOrigin, terminal: terminalOf(stderr),
	}, newLogger(stderr))
}

// terminalOf returns w when it is a terminal, else nil: under launchd or systemd stderr is a log file or a pipe,
// and the first-run PIN must never land in a log.
func terminalOf(w io.Writer) io.Writer {
	if f, ok := w.(*os.File); ok && (isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())) {
		return w
	}
	return nil
}

// serve runs homed until ctx is done: v0.1's data moved over (once), the database, the plugins, the gatekeeper of the
// public URL (sign-in, OAuth, people, the MCP access check) behind the loopback listener, the tunnel (cloudflared as a
// child), the admin socket and, while the owner cannot sign in, the first-run page. SIGHUP re-reads the plugins and the
// home name.
func serve(ctx context.Context, opts runOptions, log *slog.Logger) error {
	dir, err := config.ResolveDataDir(opts.dataDir)
	if err != nil {
		return err
	}
	// The instance lock comes first and is held until serve returns: one homed per data dir.
	unlock, err := config.LockInstance(dir)
	if err != nil {
		return err
	}
	defer unlock()
	if w := config.DataDirWarning(dir); w != "" {
		log.Warn(w)
	}
	if res, err := config.MigrateV01(dir); err != nil {
		return fmt.Errorf("move %s to v0.2: %w", config.FileName, err)
	} else if res != (config.MigrateResult{}) {
		log.Info("moved homed.yaml to v0.2: the relay is gone, the tunnel is off until you connect it",
			"backup", res.Backup, "relay_removed", res.RelayRemoved, "relay_token_deleted", res.TokenDeleted)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return err
	}
	if cfg.HasRelay() {
		log.Warn("homed.yaml still has v0.1's relay section; homed ignores it (the hub reaches the internet through " +
			"Cloudflare now: homed cloudflare connect <host>)")
	}
	id, err := config.InstanceID(dir)
	if err != nil {
		return fmt.Errorf("instance id: %w", err)
	}
	addr := cmp.Or(opts.listen, cfg.ListenAddr())
	port, err := config.LoopbackPort(addr)
	if err != nil {
		return err
	}
	clock := opts.clock
	if clock == nil {
		clock = time.Now
	}
	d := &daemon{
		dir: dir, log: log, instanceID: id, clock: clock, restartCh: make(chan struct{}),
		setupListen: cmp.Or(opts.setupListen, cfg.SetupListen), terminal: opts.terminal,
		listenLAN: cmp.Or(opts.listenLAN, firstrun.ListenLAN), listenLocal: cmp.Or(opts.listenLocal, firstrun.ListenLocal),
	}
	if opts.devOrigin != "" {
		site, err := parseDevOrigin(opts.devOrigin)
		if err != nil {
			return err
		}
		if cfg.Tunnel.Configured() {
			return fmt.Errorf("--dev-origin runs without a tunnel, but %s configures one (mode %s); use another "+
				"--data-dir for development", config.FileName, cfg.Tunnel.Mode)
		}
		d.dev = &site
	}

	if d.st, err = store.Open(ctx, store.Options{Path: filepath.Join(dir, config.DBFile), Clock: clock, Logger: log}); err != nil {
		return err
	}
	defer func() {
		if err := d.st.Close(); err != nil {
			log.Error("close the database", "err", err)
		}
	}()
	if res, err := d.st.ImportV01(ctx, filepath.Join(dir, config.OldDBFile)); err != nil {
		return err
	} else if res.Imported || res.Renamed != "" {
		log.Info("imported the v0.1 database: plugins, language and home name; people join again with a setup link "+
			"and invites", "plugins", res.Plugins, "home_name", res.HomeName, "kept_as", res.Renamed)
	}

	httpLn, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer httpLn.Close()
	d.httpAddr = httpLn.Addr()

	d.activity = audit.NewWriter(d.st.Activity(), log.With("component", "activity"))
	defer func() {
		d.activity.Close()
		if n := d.activity.Dropped(); n > 0 {
			log.Warn("activity records dropped", "count", n)
		}
	}()
	specs, err := enabledSpecs(ctx, d.st)
	if err != nil {
		return err
	}
	if d.host, err = plugins.New(specs, dir, log.With("component", "plugins")); err != nil {
		return err
	}
	d.host.HubVersion = version
	d.host.Start(ctx)
	defer d.host.Close()
	d.runtime = &pluginRuntime{st: d.st, host: d.host}
	d.gw = gateway.New(d.host, d.activity, gateway.Options{
		HomeName: d.homeName(ctx), Instructions: d.host.Instructions, Version: version, Log: log.With("component", "gateway"),
	})

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if d.tun, err = d.newTunnel(cfg, port, opts.probeClient); err != nil {
		return err
	}
	defer d.tun.Stop()

	if d.admin, err = admin.New(admin.Options{
		Store: d.st, Host: d.host, Runtime: d.runtime, Tunnel: d.tun, Activity: d.activity,
		Installer:  newInstaller(dir, d.st, d.runtime, d.host, d.requestRestart),
		OwnerReady: d.ownerReady, OnHomeName: d.gw.SetHomeName,
		Version: version, Commit: buildCommit(), StartedAt: time.Now(), LocalName: localUserName(),
		Log: log.With("component", "admin"),
	}); err != nil {
		return err
	}
	closeAdmin := sync.OnceFunc(d.admin.Close)
	defer closeAdmin()
	if d.ui, err = web.New(log.With("component", "web")); err != nil {
		return err
	}
	if d.firstRunUI, err = web.FirstRun(log.With("component", "web")); err != nil {
		return err
	}
	if d.bare, err = d.newBare(); err != nil {
		return err
	}

	sock, err := d.adminSocket()
	if err != nil {
		return err
	}
	if err := sock.Start(ctx); err != nil {
		return err
	}
	defer func() {
		sctx, scancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer scancel()
		_ = sock.Stop(sctx)
	}()

	var wg sync.WaitGroup
	defer wg.Wait()
	d.mu.Lock()
	d.runCtx, d.wg = runCtx, &wg
	d.mu.Unlock()
	defer func() {
		if s := d.stack.Load(); s != nil {
			sctx, scancel := context.WithTimeout(context.Background(), shutdownTimeout)
			_ = s.auth.Stop(sctx)
			scancel()
		}
	}()

	// The public URL: development's own; a named tunnel's at once (its host is known); a quick tunnel's when
	// cloudflared gives it (the tunnel's OnState).
	switch {
	case d.dev != nil:
		d.setSite(d.dev)
	case cfg.Tunnel.Mode == tunnel.ModeNamed:
		if site, err := siteOf("https://" + cfg.Tunnel.Hostname); err == nil {
			d.setSite(&site)
		}
	}
	if d.dev == nil {
		if err := d.tun.Start(runCtx, cfg.Tunnel.ModeOrOff(), cfg.Tunnel.Hostname); err != nil {
			log.Error("the tunnel did not start; the hub is not reachable from the internet until it does", "err", err)
		}
	}

	httpSrv := publicServer(instanceHeader(id, hostGuard(d.publicHost, port, d.handler())))
	srvErr := make(chan error, 1)
	wg.Go(func() { srvErr <- httpSrv.Serve(httpLn) })
	log.Info("homed listening", "addr", httpLn.Addr().String(), "data_dir", dir, "version", version,
		"plugins", len(specs), "tunnel", d.tun.Status().Mode)
	wg.Go(func() { pruneActivity(runCtx, d.st, log, activityPruneEvery) })
	wg.Go(func() { d.watchOwner(runCtx, ownerCheckEvery) })
	if ready, err := d.ownerReady(ctx); err != nil {
		return err
	} else if !ready {
		// Until the owner can sign in: a busy port does not stop homed, the link also comes from the CLI.
		if _, err := d.openFirstRun(); err != nil {
			log.Error("the first-run page did not open; get a setup link with: homed admin setup-link", "err", err)
		}
	}

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	wg.Go(func() {
		for {
			select {
			case <-runCtx.Done():
				return
			case <-hup:
				d.reload(runCtx)
			}
		}
	})

	if opts.ready != nil {
		opts.ready(d)
	}
	var serveErr error
	select {
	case serveErr = <-srvErr:
	case <-ctx.Done():
	case <-d.restartCh:
		serveErr = errRestart
	}
	log.Info("shutting down")
	cancel()
	closeAdmin() // ends the event streams and cancels running admin calls and jobs
	shCtx, shCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shCancel()
	if err := httpSrv.Shutdown(shCtx); err != nil {
		_ = httpSrv.Close() // a tool call still running (a pairing may take minutes) is cut
	}
	return serveErr
}

// newTunnel makes the tunnel manager: cloudflared found by homed.yaml, PATH or next to homed; its output in
// logs/cloudflared.log; homed.yaml written on a connect, a switch to quick or a disconnect.
func (d *daemon) newTunnel(cfg *config.Config, port int, probe *http.Client) (*tunnel.Manager, error) {
	bin, err := tunnel.Cloudflared(cfg.Tunnel.Cloudflared)
	if err != nil {
		d.log.Warn("cloudflared was not found; the tunnel cannot start", "err", err)
	}
	childLog := tunnel.NewChildLog(filepath.Join(d.dir, config.LogsDir, "cloudflared.log"), func(err error) {
		d.log.Warn("cannot write cloudflared.log", "err", err)
	})
	return tunnel.New(tunnel.Options{
		Dir: filepath.Join(d.dir, config.TunnelDir), Service: fmt.Sprintf("http://127.0.0.1:%d", port), Binary: bin,
		InstanceID: d.instanceID, ProbeClient: probe, ChildLog: childLog, Logger: d.log.With("component", "tunnel"),
		Clock: d.clock, OnState: d.onTunnelState,
		Save: func(mode, hostname string) error {
			return config.Update(d.dir, func(c *config.Config) error {
				c.Tunnel.Mode, c.Tunnel.Hostname = mode, hostname
				return nil
			})
		},
	})
}

// onTunnelState follows the public URL: a new stack for it (none without one); the first-run page moves off the home
// network once a tunnel is configured, and opens again when a quick tunnel's new URL left the owner without a
// passkey of it (a reconnect).
func (d *daemon) onTunnelState(st tunnel.State) {
	if d.dev != nil {
		return
	}
	if st.URL == "" {
		d.setSite(nil)
	} else if site, err := siteOf(st.URL); err != nil {
		d.log.Error("the tunnel's URL cannot be served", "url", st.URL, "err", err)
	} else {
		d.setSite(&site)
	}
	go func() { // never on the supervisor's goroutine
		d.moveFirstRun()
		if st.Mode != tunnel.ModeQuick || st.URL == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if ready, err := d.ownerReady(ctx); err == nil && !ready {
			if _, err := d.openFirstRun(); err != nil {
				d.log.Error("the first-run page did not open for the new quick URL; get a setup link with: homed admin "+
					"setup-link", "err", err)
			}
		}
	}()
}

// adminSocket is the gatekeeper's admin socket with every command of homed.
func (d *daemon) adminSocket() (*adminsock.Server, error) {
	reg := adminsock.NewRegistry()
	cmds := []adminsock.Command{
		d.authCommand("setup-link", "print a one-time link that registers the owner's passkey"),
		d.authCommand("reset-access", "wipe the owner's passkeys, sessions, connectors and invites and print a new setup link"),
		{Name: "firstrun", Summary: "open the first-run page and print its address and PIN", Run: d.cmdFirstRun},
	}
	cmds = append(cmds, tunnel.Commands(d.tun, d.auditCLI)...)
	cmds = append(cmds, d.admin.Commands(d.dir)...)
	if err := reg.Add(cmds...); err != nil {
		return nil, err
	}
	return adminsock.New(adminsock.Options{Path: filepath.Join(d.dir, config.SocketFile),
		Logger: d.log.With("component", "adminsock"), Registry: reg})
}

// Timeouts of the public listener, cloudflared's traffic. ReadTimeout bounds a request with its body; net/http lifts
// the read deadline once the body is read, so it never cuts a long answer. There is no WriteTimeout: the admin's
// event stream and tool calls (up to 90 s for an assistant) stay open.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 2 * time.Minute
	idleTimeout       = 2 * time.Minute // an idle keep-alive connection closes
)

// publicServer is the http.Server of the public listener.
func publicServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout, IdleTimeout: idleTimeout}
}

// instanceHeader puts the hub's instance id on every answer: the tunnel's self-check looks for it.
func instanceHeader(id string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(tunnel.InstanceHeader, id)
		next.ServeHTTP(w, r)
	})
}

// reload re-reads what homed caches from the database: the plugins and the home name. SIGHUP.
func (d *daemon) reload(ctx context.Context) {
	if err := d.runtime.Apply(ctx); err != nil {
		d.log.Error("apply the plugins", "err", err)
	}
	d.gw.SetHomeName(d.homeName(ctx))
	d.log.Info("reloaded the plugins and the home name")
}

// pruneActivity deletes the activity older than activityKeep now and then every interval, until ctx is done.
func pruneActivity(ctx context.Context, st *store.Store, log *slog.Logger, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		n, err := st.Activity().Prune(ctx, time.Now().Add(-activityKeep))
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("cannot prune the activity log", "err", err)
		case n > 0:
			log.Info("pruned old activity records", "count", n, "kept_days", int(activityKeep/(24*time.Hour)))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// requestRestart makes serve shut down gracefully and return errRestart (the hub update's last step).
func (d *daemon) requestRestart() {
	d.restartOnce.Do(func() { close(d.restartCh) })
}

// parseDevOrigin accepts http(s)://localhost[:port]: browsers treat localhost as a secure context, so passkeys work
// there over plain http with the relying party ID "localhost".
func parseDevOrigin(s string) (core.Site, error) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() != "localhost" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return core.Site{}, fmt.Errorf("--dev-origin %q: want http://localhost:<port>", s)
	}
	site := core.Site{PublicURL: u.Scheme + "://" + u.Host, RPID: "localhost", ClientIP: core.ClientIPRemote}
	return site, site.Validate()
}

// localUserName names the admin socket's caller in the activity log.
func localUserName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "local"
}

func newLogger(w io.Writer) *slog.Logger {
	level := slog.LevelInfo
	if os.Getenv("HOMED_LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}
