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
	"syscall"
	"time"

	"github.com/mattn/go-isatty"

	"github.com/devXpro/homie/internal/admin"
	"github.com/devXpro/homie/internal/auth"
	"github.com/devXpro/homie/internal/auth/wire"
	"github.com/devXpro/homie/internal/config"
	"github.com/devXpro/homie/internal/firstrun"
	"github.com/devXpro/homie/internal/gateway"
	"github.com/devXpro/homie/internal/plugins"
	"github.com/devXpro/homie/internal/relay"
	"github.com/devXpro/homie/internal/store"
	"github.com/devXpro/homie/internal/web"
)

const shutdownTimeout = 5 * time.Second

// firstRunCloseDelay keeps the first-run page answering "the home is set up" for a while after the owner registered,
// so a page still open on the home network can say so; then its port closes.
const firstRunCloseDelay = 2 * time.Minute

// The audit log keeps 180 days (ADMIN-API §3.6); older records are pruned at start and then once a day.
const (
	auditKeep       = 180 * 24 * time.Hour
	auditPruneEvery = 24 * time.Hour
)

// errRestart ends serve after a hub update. main exits with install.ExitCodeRestart (75), and the service manager
// (launchd KeepAlive, systemd Restart=) starts the new binary.
var errRestart = errors.New("homed was updated; exiting so the service manager starts the new binary")

// runOptions are the `homed run` settings.
type runOptions struct {
	dataDir     string
	listen      string    // overrides homed.yaml listen
	setupListen string    // overrides homed.yaml setup_listen
	devOrigin   string    // development: the admin's origin on localhost; no relay
	caFile      string    // test harness only
	serverAddr  string    // test harness only
	terminal    io.Writer // where the first-run PIN is printed; nil when homed has no terminal (it is never printed)
	ready       func(*daemon)
}

// daemon is a running homed.
type daemon struct {
	dir         string
	log         *slog.Logger
	instanceID  string
	dev         *auth.RelyingParty // set by --dev-origin
	st          *store.DB
	host        *plugins.Host
	runtime     *pluginRuntime
	keys        *keyResolver
	gw          gateway.Handler
	relay       *relayManager
	auth        *auth.Auth
	audit       *admin.AuditWriter
	admin       *admin.Server
	httpAddr    net.Addr
	setupListen string    // setup_listen from the command line or homed.yaml; "" picks firstrun.DefaultListen
	terminal    io.Writer // the first-run PIN goes here; nil without a terminal

	restartOnce sync.Once
	restartCh   chan struct{}

	mu            sync.Mutex
	runCtx        context.Context // set once serving; the first-run page lives under it
	wg            *sync.WaitGroup // serve waits for the first-run page with the rest
	firstRun      *firstRunPage   // the open first-run page; nil when it is closed
	setupAddr     net.Addr        // where the first-run page listened last; nil when it never opened
	firstRunTimer *time.Timer
}

// firstRunPage is one opening of the first-run page.
type firstRunPage struct {
	srv  *firstrun.Server
	urls []string
	stop context.CancelFunc
}

func cmdRun(args []string, stderr io.Writer) error {
	fl, dataDir := newFlags("run", stderr)
	listen := fl.String("listen", "", "loopback address to serve on (default: homed.yaml listen, else "+config.DefaultListen+")")
	setupListen := fl.String("setup-listen", "", "first-run page address (default: homed.yaml setup_listen, else "+
		firstrun.ListenLAN+" while there is no relay and "+firstrun.ListenLocal+" with one)")
	devOrigin := fl.String("dev-origin", "",
		"development only: the admin's origin, e.g. http://localhost:5173; passkeys for localhost, no relay")
	caFile := fl.String("relay-ca-file", "", "test harness only: trust only this PEM CA for the relay")
	serverAddr := fl.String("relay-server-addr", "", "test harness only: dial this host:port instead of <domain>:443")
	if err := noArgs(fl, args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, runOptions{
		dataDir: *dataDir, listen: *listen, setupListen: *setupListen, devOrigin: *devOrigin,
		caFile: *caFile, serverAddr: *serverAddr, terminal: terminalOf(stderr),
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

// serve runs homed until ctx is done: the public listener (gateway, admin API and SPA) on the loopback address, the
// local socket, the plugins, the relay client with its self-check and, until the home has an owner with a passkey,
// the first-run page. SIGHUP re-reads keys, plugins and the home name from the database.
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
	cfg, err := config.Load(dir)
	if err != nil {
		return err
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
	d := &daemon{
		dir: dir, log: log, instanceID: id, restartCh: make(chan struct{}),
		setupListen: cmp.Or(opts.setupListen, cfg.SetupListen), terminal: opts.terminal,
	}
	if opts.devOrigin != "" {
		rp, err := parseDevOrigin(opts.devOrigin)
		if err != nil {
			return err
		}
		if cfg.Relay.Domain != "" {
			return fmt.Errorf("--dev-origin runs without a relay, but %s sets relay.domain %s; use another --data-dir "+
				"for development", config.FileName, cfg.Relay.Domain)
		}
		d.dev = &rp
	}
	relayCfg := relay.Config{Domain: cfg.Relay.Domain, LocalPort: port, CAFile: opts.caFile, ServerAddr: opts.serverAddr}
	if cfg.Relay.Domain != "" {
		if relayCfg.Token, err = config.ReadRelayToken(dir); err != nil {
			return fmt.Errorf("relay token: %w", err)
		}
	}

	if d.st, err = openStore(ctx, dir, cfg, log); err != nil {
		return err
	}
	defer func() {
		if err := d.st.Close(); err != nil {
			log.Error("close the database", "err", err)
		}
	}()

	sockLn, err := listenSocket(ctx, filepath.Join(dir, config.SocketFile), log)
	if err != nil {
		return err
	}
	defer sockLn.Close()
	httpLn, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer httpLn.Close()
	d.httpAddr = httpLn.Addr()

	auditW := admin.NewAuditWriter(d.st.Audit(), d.st.People(), log.With("component", "audit"))
	d.audit = auditW
	defer func() {
		auditW.Close()
		if n := auditW.Dropped(); n > 0 {
			log.Warn("audit records dropped", "count", n)
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

	if d.keys, err = newKeyResolver(ctx, d.st, log.With("component", "keys")); err != nil {
		return fmt.Errorf("person keys: %w", err)
	}
	hs, err := d.st.Settings().Get(ctx)
	if err != nil {
		return err
	}
	d.gw = gateway.New(d.host, d.keys, auditW, gateway.Options{
		HomeName: hs.HomeName, Instructions: d.host.Instructions, InstanceID: id, Version: version,
		Log: log.With("component", "gateway"),
	})
	d.relay = newRelayManager(dir, id, relayCfg, d.dev != nil, log.With("component", "relay"))
	if d.auth, err = auth.New(auth.Options{
		Store: d.st, RelyingParty: d.relyingParty, Audit: auditW, OnSessionEnd: d.sessionEnded, OnSetup: d.onSetup,
		Version: version, Log: log.With("component", "auth"),
	}); err != nil {
		return err
	}
	var adminRelay admin.Relay = d.relay
	if d.dev != nil {
		adminRelay = nil // the relay endpoints answer home_not_ready in development mode
	}
	if d.admin, err = admin.New(admin.Options{
		Store: d.st, Host: d.host, Auth: authAdapter{d.auth}, Runtime: d.runtime, Relay: adminRelay,
		Installer: newInstaller(dir, d.st, d.runtime, d.host, d.requestRestart), Keys: d.keys, Audit: auditW,
		OriginFunc: d.origin, InstanceID: id, Version: version, Commit: buildCommit(), StartedAt: time.Now(),
		LocalName: localUserName(), OnSettings: func(hs store.HomeSettings) { d.gw.SetHomeName(hs.HomeName) },
		UsablePasskey: d.auth.UsablePasskey, AuthRoutes: d.auth.Handler(), LocalRoutes: d.localRoutes(),
		Log: log.With("component", "admin"),
	}); err != nil {
		return err
	}
	closeAdmin := sync.OnceFunc(d.admin.Close)
	defer closeAdmin()

	httpSrv := publicServer(hostGuard(d.relay.Domain, port, d.publicHandler()))
	sockSrv := &http.Server{Handler: socketHandler(d.admin.LocalHandler()), ReadHeaderTimeout: 10 * time.Second}

	var wg sync.WaitGroup
	defer wg.Wait()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.mu.Lock()
	d.runCtx, d.wg = runCtx, &wg
	d.mu.Unlock()
	srvErr := make(chan error, 2)
	wg.Go(func() { srvErr <- httpSrv.Serve(httpLn) })
	wg.Go(func() { srvErr <- sockSrv.Serve(sockLn) })
	adminURL := "after the relay connects"
	if o := d.origin(); o != "" {
		adminURL = o + "/admin"
	}
	log.Info("homed listening", "addr", httpLn.Addr().String(), "data_dir", dir, "version", version,
		"plugins", len(specs), "admin", adminURL)
	wg.Go(func() { d.auth.Run(runCtx) })
	wg.Go(func() { d.relay.run(runCtx) })
	wg.Go(func() { pruneAudit(runCtx, d.st, log, auditPruneEvery) })
	ready, err := d.auth.OwnerReady(ctx)
	if err != nil {
		return err
	}
	if !ready {
		// Until the owner has a passkey: a busy port does not stop homed, the link also comes from the CLI.
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
	for _, srv := range []*http.Server{httpSrv, sockSrv} {
		if err := srv.Shutdown(shCtx); err != nil {
			_ = srv.Close() // a tool call still running (a pairing may take minutes) is cut
		}
	}
	return serveErr
}

// Timeouts of the public listener, the relay's traffic. ReadTimeout bounds a request with its body (1 MiB at most);
// net/http lifts the read deadline once the body is read (connReader.startBackgroundRead), so it never cuts a long
// answer. There is no WriteTimeout: the admin's event stream (SSE) and long tool calls (up to 300 s) stay open.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 2 * time.Minute
	idleTimeout       = 2 * time.Minute // an idle keep-alive connection closes
)

// publicServer is the http.Server of the public listener.
func publicServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout, IdleTimeout: idleTimeout}
}

// publicHandler is what the loopback listener serves, through the relay or locally: the admin API, the admin SPA,
// and the gateway (/healthz, the REST façade, /mcp). Every answer carries the instance id the relay self-check looks
// for.
func (d *daemon) publicHandler() http.Handler {
	ui := web.Handler()
	mux := http.NewServeMux()
	mux.Handle("/api/admin/", d.admin.Handler())
	mux.Handle("/admin", ui)
	mux.Handle("/admin/", ui)
	mux.Handle("/join", ui)
	mux.Handle("GET /{$}", http.RedirectHandler("/admin/", http.StatusFound))
	mux.Handle("/", d.gw)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(relay.InstanceHeader, d.instanceID)
		mux.ServeHTTP(w, r)
	})
}

// openFirstRun opens the first-run page unless it is open, and returns it: a new listener, a new PIN, 30 minutes and
// a fresh budget of wrong PINs. It listens on setup_listen, or by default on the home network only while homed has no
// relay (firstrun.DefaultListen). homed opens it at start while the owner has no passkey, and `homed admin firstrun`
// opens it again after it closed (30 minutes passed, too many wrong PINs).
func (d *daemon) openFirstRun() (*firstRunPage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.firstRun != nil {
		return d.firstRun, nil
	}
	if d.runCtx == nil || d.runCtx.Err() != nil {
		return nil, errors.New("homed is stopping")
	}
	addr := d.firstRunAddr()
	ln, err := (&net.ListenConfig{}).Listen(d.runCtx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("the first-run page cannot listen on %s: %w", addr, err)
	}
	srv, err := firstrun.New(firstrun.Options{
		Store: d.st, Relay: d.relay, Audit: d.audit, UI: web.Handler(), Terminal: d.terminal, RPID: d.rpID,
		Log: d.log.With("component", "firstrun"),
	})
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	ctx, stop := context.WithCancel(d.runCtx)
	p := &firstRunPage{srv: srv, urls: firstrun.PageURLs(ln.Addr()), stop: stop}
	d.firstRun, d.setupAddr = p, ln.Addr()
	d.wg.Go(func() {
		defer stop()
		if err := srv.Serve(ctx, ln); err != nil {
			d.log.Error("first-run page", "err", err)
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.firstRun == p {
			d.firstRun = nil
		}
	})
	if d.dev != nil && d.terminal != nil {
		_, _ = fmt.Fprintf(d.terminal, "development mode: get a setup link with: homed admin setup-link --data-dir %q\n\n", d.dir)
	}
	return p, nil
}

// firstRunAddr is where the first-run page listens: setup_listen when set, else the home network only while homed has
// no relay (the connect code may come from another device) and the home box itself once it has one (development mode
// counts as having one: it never takes a code).
func (d *daemon) firstRunAddr() string {
	if d.setupListen != "" {
		return d.setupListen
	}
	return firstrun.DefaultListen(d.relay.Domain() != "" || d.dev != nil)
}

// rpID is the passkeys' relying party ID now; "" while homed has none.
func (d *daemon) rpID() string {
	rp, _ := d.relyingParty()
	return rp.ID
}

// localRoutes serves the socket-only /api/admin/local/* endpoints: auth's setup-link and reset-access, and firstrun.
func (d *daemon) localRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/admin/local/firstrun", d.localFirstRun)
	mux.Handle("/api/admin/local/", d.auth.LocalHandler())
	return mux
}

// firstRunJSON is the answer of POST /api/admin/local/firstrun (ADMIN-API §6).
type firstRunJSON struct {
	URLs     []string `json:"urls"`
	PIN      string   `json:"pin"`
	PINUntil string   `json:"pinUntil"`
	ClosesAt string   `json:"closesAt"`
}

// localFirstRun is POST /api/admin/local/firstrun (`homed admin firstrun`): it opens the first-run page when it is
// closed and answers its addresses, the current PIN and when they end. The socket is the owner's (0600, own uid),
// so the PIN goes to the person at the home box's terminal and never to a log.
func (d *daemon) localFirstRun(w http.ResponseWriter, r *http.Request) {
	wire.Headers(w)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		wire.WriteError(w, wire.Errorf(http.StatusMethodNotAllowed, "method_not_allowed", r.Method+" is not allowed here"))
		return
	}
	ready, err := d.auth.OwnerReady(r.Context())
	if err != nil {
		d.log.Error("first-run page: cannot read the owner", "err", err)
		wire.WriteError(w, err)
		return
	}
	if ready {
		wire.WriteError(w, wire.Errorf(http.StatusConflict, "owner_exists",
			"the home is set up; add a passkey with a setup link: homed admin setup-link"))
		return
	}
	p, err := d.openFirstRun()
	if err != nil {
		wire.WriteError(w, wire.Errorf(http.StatusConflict, "conflict", err.Error()))
		return
	}
	pin, until := p.srv.PIN()
	wire.WriteJSON(w, http.StatusOK, firstRunJSON{
		URLs: p.urls, PIN: pin, PINUntil: wire.Time(until), ClosesAt: wire.Time(p.srv.ClosesAt()),
	})
}

// relyingParty is the passkey identity of the home, read on every request: the development origin, else the relay
// domain; none while homed has neither.
func (d *daemon) relyingParty() (auth.RelyingParty, bool) {
	if d.dev != nil {
		return *d.dev, true
	}
	if dom := d.relay.Domain(); dom != "" {
		return auth.ForDomain(dom), true
	}
	return auth.RelyingParty{}, false
}

// origin is the admin's only origin (ADMIN-API §1.4); "" while there is none.
func (d *daemon) origin() string {
	rp, _ := d.relyingParty()
	return rp.Origin
}

// sessionEnded closes the event streams of a session auth deleted.
func (d *daemon) sessionEnded(id string) {
	if d.admin != nil {
		d.admin.SessionRevoked(id)
	}
}

// onSetup follows a registration with a setup or invite link: the home name may be new, the person new or renamed,
// and once the owner has a passkey the first-run page is no longer needed.
func (d *daemon) onSetup(_ store.Person, hs store.HomeSettings) {
	d.gw.SetHomeName(hs.HomeName)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.keys.Rebuild(ctx); err != nil {
		d.log.Error("reload the person keys", "err", err)
	}
	if ready, err := d.auth.OwnerReady(ctx); err == nil && ready {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.firstRun != nil && d.firstRunTimer == nil {
			d.firstRunTimer = time.AfterFunc(firstRunCloseDelay, d.firstRun.stop)
			d.log.Info("the home has an owner; the first-run page closes soon", "in", firstRunCloseDelay)
		}
	}
}

// reload re-reads what homed caches from the database: the person keys, the plugins and the home name. SIGHUP.
func (d *daemon) reload(ctx context.Context) {
	if err := d.keys.Rebuild(ctx); err != nil {
		d.log.Error("reload the person keys", "err", err)
	}
	if err := d.runtime.Apply(ctx); err != nil {
		d.log.Error("apply the plugins", "err", err)
	}
	if hs, err := d.st.Settings().Get(ctx); err == nil {
		d.gw.SetHomeName(hs.HomeName)
	}
	d.log.Info("reloaded the person keys, the plugins and the home name")
}

// pruneAudit deletes the audit records older than auditKeep now and then every interval, until ctx is done.
func pruneAudit(ctx context.Context, st store.Store, log *slog.Logger, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		n, err := st.Audit().Prune(ctx, time.Now().Add(-auditKeep))
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("cannot prune the audit log", "err", err)
		case n > 0:
			log.Info("pruned old audit records", "count", n, "kept_days", int(auditKeep/(24*time.Hour)))
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

// authAdapter is internal/auth as the admin sees it (admin.Auth).
type authAdapter struct{ a *auth.Auth }

func (x authAdapter) Authenticate(r *http.Request) (admin.Principal, error) {
	p, err := x.a.Authenticate(r)
	switch {
	case errors.Is(err, auth.ErrUnauthenticated):
		return admin.Principal{}, admin.ErrUnauthenticated
	case err != nil:
		return admin.Principal{}, err
	}
	return admin.Principal(p), nil
}

// parseDevOrigin accepts http(s)://localhost[:port]: browsers treat localhost as a secure context, so passkeys work
// there over plain http with the relying party ID "localhost".
func parseDevOrigin(s string) (auth.RelyingParty, error) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() != "localhost" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return auth.RelyingParty{}, fmt.Errorf("--dev-origin %q: want http://localhost:<port>", s)
	}
	return auth.RelyingParty{ID: "localhost", Origin: u.Scheme + "://" + u.Host}, nil
}

// localUserName names the socket's principal before the home has an owner.
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
