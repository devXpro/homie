// Package firstrun serves the first-run page on the home network (docs/ADMIN-API.md §5, DESIGN.md §8.3): the owner
// enters a PIN homed shows on the home box, pastes the server's connect code if homed has no relay yet, waits for the
// relay, and gets the one-time setup link https://<domain>/admin/setup#t=<token> to open on the phone or the Mac.
//
// The listener (setup_listen: 0.0.0.0:8090 while homed has no relay, 127.0.0.1:8090 once it has one) speaks plain
// HTTP, so the page is built to be short-lived and hard to guess (docs/SECURITY.md):
//   - it closes 30 minutes after it opened, and for good after 20 wrong PINs; `homed admin firstrun` on the home box
//     (or a restart) opens it again with a new PIN, and `homed admin setup-link` never needs it;
//   - the PIN has 8 digits and changes every 10 minutes and after every issued link. It is printed only to a terminal
//     (Options.Terminal); under launchd or systemd the log says how to get it instead;
//   - one client gets 5 wrong PINs per 10 minutes and then waits, without locking out the other devices;
//   - the homie_firstrun cookie (HttpOnly, SameSite=Strict, 30 minutes) is random and kept only as a hash, in memory;
//   - links issued here live 3 minutes (the CLI's live 15); every link and relay change is logged and audited with the
//     client's address, and a relay that is already configured is never replaced from here;
//   - only Host names a LAN can have pass (IP literals, localhost, single-label and .local-style names), which stops
//     DNS rebinding; mutating requests need X-Homie: 1 and a same-origin Origin and Sec-Fetch-Site;
//   - once an owner has a passkey of the current relying party every path answers 410 setup_done.
//
// cmd/homed wires it: while Needed reports true, New(Options{…}) and Serve(ctx, ln); for every reopening a new Server.
// Relay is homed's relay client (the same adapter that serves internal/admin, plus SelfCheckResult); UI is the SPA
// bundle that has the /firstrun route (internal/web).
package firstrun

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devXpro/homie/internal/auth"
	"github.com/devXpro/homie/internal/auth/wire"
	"github.com/devXpro/homie/internal/relay"
	"github.com/devXpro/homie/internal/store"
)

// setup_listen defaults (DefaultListen).
const (
	// ListenLAN is the default while homed has no relay: the page takes the connect code, perhaps from another device
	// on the home network (a Raspberry Pi has no browser of its own).
	ListenLAN = "0.0.0.0:8090"
	// ListenLocal is the default once homed has a relay: the page only issues the link, which the home box itself
	// opens or shows as a QR code.
	ListenLocal = "127.0.0.1:8090"
)

// DefaultListen is setup_listen when neither the command line nor homed.yaml sets it.
func DefaultListen(hasRelay bool) string {
	if hasRelay {
		return ListenLocal
	}
	return ListenLAN
}

// CookieName is the first-run cookie. It is not __Host- and not Secure: the page is plain http on the LAN.
const CookieName = "homie_firstrun"

const (
	// OpenFor is how long the page stays open; then it closes until `homed admin firstrun` or a restart.
	OpenFor = 30 * time.Minute
	// LinkTTL is the life of a setup link issued here, shorter than the CLI's (auth.SetupLinkTTL): the page and the
	// link travel over plain http on the home network.
	LinkTTL = 3 * time.Minute
	// PINEvery is how often the PIN changes; it changes after every issued link too.
	PINEvery = 10 * time.Minute

	pinDigits      = 8
	cookieTTL      = 30 * time.Minute
	maxCookies     = 64
	maxWrongClient = 5                // wrong PINs one client (address) may enter per clientWindow
	clientWindow   = 10 * time.Minute // the window of maxWrongClient
	maxWrongTotal  = 20               // wrong PINs from everybody, then the page closes for good
)

// Relay is homed's relay as the first-run page needs it. The adapter T2.6 gives internal/admin (admin.Relay) has
// Domain, Status and Connect already; SelfCheckResult is the one method more.
type Relay interface {
	// Domain is the configured relay domain; empty when homed has no relay yet.
	Domain() string
	// Status reports the relay client.
	Status() relay.Status
	// SelfCheckResult returns the latest self-check: when it ran (zero: never) and why it failed ("" if it passed).
	SelfCheckResult() (checkedAt time.Time, failure string)
	// Connect stores the code the way `homed relay connect` does and (re)connects. It should run a self-check as
	// soon as the relay connects, so the page does not wait for the 30-second check loop.
	Connect(ctx context.Context, code relay.Code) error
}

// Options configures the first-run server. Store and Relay are required.
type Options struct {
	Store store.Store
	Relay Relay
	// Audit receives the setup.link and relay.change records; nil appends them to Store.Audit directly.
	Audit auth.AuditRecorder
	// UI serves every path outside /api/: the SPA bundle with its /firstrun route and assets. Nil answers 404.
	UI http.Handler
	// Terminal is where the PIN is printed: the terminal homed runs in. Nil means homed has none (launchd, systemd,
	// a log file): the PIN is never printed, and the log says to use `homed admin firstrun` or `homed admin setup-link`.
	Terminal io.Writer
	// RPID returns the passkeys' relying party ID now: the relay domain, or "localhost" in development. The page closes
	// once an owner has a passkey of it. Nil means Relay.Domain().
	RPID func() string
	// OpenFor is how long Serve keeps the page open; 0 means OpenFor (30 minutes).
	OpenFor time.Duration
	// Log receives the page's events; the PIN and tokens never reach it.
	Log *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Server is one opening of the first-run page. Create it with New; a reopening is a new Server.
type Server struct {
	st      store.Store
	relay   Relay
	audit   auth.AuditRecorder
	ui      http.Handler
	term    io.Writer
	rpID    func() string
	openFor time.Duration
	log     *slog.Logger
	now     func() time.Time

	pollEvery, pingEvery, pinEvery time.Duration // the event stream and the PIN; tests shorten them

	closed    chan struct{} // closed when too many wrong PINs closed the page
	closeOnce sync.Once
	connectMu sync.Mutex // one relay code at a time, so "no relay yet" holds until the code is stored

	mu         sync.Mutex
	pin        string
	pinUntil   time.Time              // when the PIN changes next
	closesAt   time.Time              // when the page closes by itself
	wrong      map[string][]time.Time // client address → its wrong PINs inside clientWindow
	wrongTotal int
	cookies    map[string]time.Time // sha256 of the cookie → expiry
}

// New returns the server with a fresh PIN; Serve announces it.
func New(o Options) (*Server, error) {
	if o.Store == nil || o.Relay == nil {
		return nil, errors.New("firstrun: Store and Relay are required")
	}
	if o.RPID == nil {
		o.RPID = o.Relay.Domain
	}
	if o.OpenFor <= 0 {
		o.OpenFor = OpenFor
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	now := o.Now()
	return &Server{
		st: o.Store, relay: o.Relay, audit: o.Audit, ui: o.UI, term: o.Terminal, rpID: o.RPID, openFor: o.OpenFor,
		log: o.Log, now: o.Now,
		pollEvery: time.Second, pingEvery: 20 * time.Second, pinEvery: PINEvery,
		closed: make(chan struct{}), pin: newPIN(), pinUntil: now.Add(PINEvery), closesAt: now.Add(o.OpenFor),
		wrong: map[string][]time.Time{}, cookies: map[string]time.Time{},
	}, nil
}

// Needed reports whether the first-run page is still needed: no owner has a passkey of the relying party rpID (the
// relay domain, "localhost" in development; "" while there is none).
func Needed(ctx context.Context, st store.Store, rpID string) (bool, error) {
	ready, err := auth.OwnerReady(ctx, st, rpID)
	return !ready, err
}

// newPIN returns pinDigits random digits.
func newPIN() string {
	n, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
	if err != nil {
		panic(err) // crypto/rand does not fail since Go 1.24
	}
	return fmt.Sprintf("%08d", n.Int64())
}

// FormatPIN shows a PIN in two groups of four digits ("1234 5678"); the page ignores the space.
func FormatPIN(pin string) string {
	if len(pin) != pinDigits {
		return pin
	}
	return pin[:4] + " " + pin[4:]
}

// PIN returns the current PIN and when it changes next, for `homed admin firstrun` over the local socket.
func (s *Server) PIN() (pin string, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pin, s.pinUntil
}

// ClosesAt returns when the page closes by itself (New plus OpenFor; Serve starts right after New).
func (s *Server) ClosesAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closesAt
}

// Serve announces the page (the PIN on the terminal, or a hint in the log) and serves ln until ctx is done, OpenFor
// has passed or too many wrong PINs closed it. It changes the PIN every PINEvery. It closes ln.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.mu.Lock()
	pin, closesAt := s.pin, s.closesAt
	s.mu.Unlock()
	urls := PageURLs(ln.Addr())
	if s.term != nil {
		_, _ = fmt.Fprintf(s.term, "\nhomed first run: open %s and enter the PIN %s (the page closes at %s)\n\n",
			strings.Join(urls, " or "), FormatPIN(pin), closesAt.Local().Format("15:04"))
	} else {
		s.log.Info("the first-run page is open; there is no terminal to show its PIN: run `homed admin firstrun` in a "+
			"terminal on the home box for the PIN, or `homed admin setup-link` for a setup link", "urls", urls,
			"closes", closesAt)
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	rotate := time.NewTicker(s.pinEvery)
	defer rotate.Stop()
	closeAt := time.NewTimer(s.openFor)
	defer closeAt.Stop()
	why := "" // why the page closed by itself; "" when homed stops
serving:
	for {
		select {
		case err := <-errc:
			return fmt.Errorf("firstrun: %w", err)
		case <-ctx.Done():
			break serving
		case <-closeAt.C:
			why = fmt.Sprintf("it was open for %s", s.openFor)
			break serving
		case <-s.closed:
			why = fmt.Sprintf("%d wrong PINs", maxWrongTotal)
			break serving
		case <-rotate.C:
			s.rotatePIN("the PIN changes every " + s.pinEvery.String())
		}
	}
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		_ = srv.Close()
	}
	<-errc
	if why != "" {
		const again = "open it again with `homed admin firstrun`, or get a setup link with `homed admin setup-link`"
		s.log.Warn("the first-run page closed: "+why+"; "+again, "urls", urls)
		if s.term != nil {
			_, _ = fmt.Fprintf(s.term, "\nhomed first run: the page closed (%s); %s\n\n", why, again)
		}
	}
	return nil
}

// rotatePIN makes a new PIN and shows it on the terminal, if there is one.
func (s *Server) rotatePIN(why string) {
	s.mu.Lock()
	s.pin = newPIN()
	s.pinUntil = s.now().Add(s.pinEvery)
	pin := s.pin
	s.mu.Unlock()
	if s.term != nil {
		_, _ = fmt.Fprintf(s.term, "\nhomed first run: %s; the PIN is now %s\n\n", why, FormatPIN(pin))
	}
	s.log.Info("first-run PIN changed", "why", why)
}

// closeForGood closes the page after too many wrong PINs; Serve shuts the listener.
func (s *Server) closeForGood() { s.closeOnce.Do(func() { close(s.closed) }) }

func (s *Server) isClosed() bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

// PageURLs lists the page's addresses for the terminal and `homed admin firstrun`: every LAN IPv4 address and
// localhost when the listener is on all interfaces.
func PageURLs(addr net.Addr) []string {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return []string{"http://" + addr.String() + "/firstrun"}
	}
	var hosts []string
	if tcp.IP.IsUnspecified() {
		if addrs, err := net.InterfaceAddrs(); err == nil {
			for _, a := range addrs {
				if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
					hosts = append(hosts, n.IP.String())
				}
			}
		}
		hosts = append(hosts, "localhost")
	} else {
		hosts = append(hosts, tcp.IP.String())
	}
	urls := make([]string, 0, len(hosts))
	for _, h := range hosts {
		urls = append(urls, "http://"+net.JoinHostPort(h, strconv.Itoa(tcp.Port))+"/firstrun")
	}
	return urls
}

// Handler serves the page: /api/firstrun/* and, for every other path, the UI.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/firstrun/state", s.endpoint(http.MethodGet, false, s.state))
	mux.Handle("/api/firstrun/pin", s.endpoint(http.MethodPost, false, s.enterPIN))
	mux.Handle("/api/firstrun/relay", s.endpoint(http.MethodPost, true, s.connect))
	mux.Handle("/api/firstrun/events", s.endpoint(http.MethodGet, true, s.events))
	mux.Handle("/api/firstrun/link", s.endpoint(http.MethodPost, true, s.link))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		wire.Headers(w)
		wire.WriteError(w, wire.Errorf(http.StatusNotFound, "not_found", "endpoint not found"))
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/firstrun", http.StatusFound)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if s.ui == nil {
			http.NotFound(w, r)
			return
		}
		s.ui.ServeHTTP(w, r)
	})
	return s.guard(mux)
}

// errClosed answers every request once too many wrong PINs closed the page.
var errClosed = wire.Errorf(http.StatusGone, "setup_closed",
	"the setup page is closed; open it again with `homed admin firstrun` on the home box, or get a setup link with "+
		"`homed admin setup-link`")

// guard refuses foreign Host names and closes everything once the page closed or the home has an owner with a
// passkey.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !lanHost(r.Host) {
			wire.Headers(w)
			wire.WriteError(w, wire.Errorf(http.StatusMisdirectedRequest, "forbidden",
				"open the setup page by the address homed printed"))
			return
		}
		if s.isClosed() {
			gone(w, r, errClosed, `<p>Страница настройки закрыта. Откройте её снова командой <code>homed admin firstrun</code> на домашнем компьютере.</p>`+
				`<p lang="en">The setup page is closed. Open it again with <code>homed admin firstrun</code> on the home box.</p>`)
			return
		}
		ready, err := auth.OwnerReady(r.Context(), s.st, s.rpID())
		if err != nil {
			s.log.Error("firstrun: cannot read the owner", "err", err)
			wire.Headers(w)
			wire.WriteError(w, err)
			return
		}
		if ready {
			gone(w, r, wire.Errorf(http.StatusGone, "setup_done", "the home is set up; this page is closed"),
				`<p>Дом уже настроен. Эта страница больше не нужна.</p>`+
					`<p lang="en">Your home is set up. You don't need this page anymore.</p>`)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// gone answers 410: the JSON error for the API, a short bilingual page for a browser.
func gone(w http.ResponseWriter, r *http.Request, apiErr *wire.Error, page string) {
	wire.Headers(w)
	if strings.HasPrefix(r.URL.Path, "/api/") {
		wire.WriteError(w, apiErr)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.WriteHeader(http.StatusGone)
	_, _ = io.WriteString(w, `<!doctype html><html lang="ru"><meta charset="utf-8"><title>Homie</title>`+page+`</html>`)
}

// lanHost accepts the Host names a browser on the home network uses: IP literals, localhost, single-label names
// and the usual local suffixes. A public name means DNS rebinding, so it is refused.
func lanHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	switch {
	case host == "":
		return false
	case net.ParseIP(host) != nil, host == "localhost", !strings.Contains(host, "."):
		return true
	}
	for _, suffix := range []string{".local", ".lan", ".home", ".home.arpa", ".internal"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// endpoint wraps an API endpoint: the common headers, the method, the CSRF rule for POST and, when signedIn is
// set, the first-run cookie.
func (s *Server) endpoint(method string, cookie bool, h handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire.Headers(w)
		var err error
		switch {
		case r.Method != method:
			w.Header().Set("Allow", method)
			err = wire.Errorf(http.StatusMethodNotAllowed, "method_not_allowed", r.Method+" is not allowed here")
		case method == http.MethodPost && !sameOrigin(r):
			err = wire.Errorf(http.StatusForbidden, "csrf_failed", "cross-site request refused")
		case cookie && !s.signedIn(r):
			err = wire.Errorf(http.StatusUnauthorized, "unauthenticated", "enter the PIN first")
		default:
			err = h(w, r)
		}
		if err != nil {
			if _, ok := errors.AsType[*wire.Error](err); !ok {
				s.log.Error("firstrun: request failed", "path", r.URL.Path, "err", err)
			}
			wire.WriteError(w, err)
		}
	})
}

// sameOrigin is ADMIN-API §1.4 for a page whose origin is whatever LAN address the browser used.
func sameOrigin(r *http.Request) bool {
	if r.Header.Get("X-Homie") != "1" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	if sf := r.Header.Get("Sec-Fetch-Site"); sf != "" && sf != "same-origin" {
		return false
	}
	return true
}

// clientOf is the address a request came from: the page is reached directly on the LAN, with no proxy in between,
// so the peer is the client (one address, not a /64: a home network is a single /64).
func clientOf(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func hashCookie(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// signedIn reports whether the request carries a live first-run cookie.
func (s *Server) signedIn(r *http.Request) bool {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return false
	}
	key := hashCookie(c.Value)
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.cookies[key]
	if ok && !s.now().Before(exp) {
		delete(s.cookies, key)
		return false
	}
	return ok
}

// enterPIN checks the PIN. A client gets maxWrongClient wrong PINs per clientWindow; maxWrongTotal wrong PINs from
// everybody close the page.
func (s *Server) enterPIN(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		PIN string `json:"pin"`
	}
	if err := wire.Decode(w, r, &body); err != nil {
		return err
	}
	pin := strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(body.PIN))
	client := clientOf(r)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isClosed() {
		return errClosed
	}
	tries := slices.DeleteFunc(s.wrong[client], func(t time.Time) bool { return !now.Before(t.Add(clientWindow)) })
	s.wrong[client] = tries
	if len(tries) >= maxWrongClient {
		return locked(tries, now)
	}
	if subtle.ConstantTimeCompare([]byte(pin), []byte(s.pin)) != 1 {
		tries = append(tries, now)
		s.wrong[client] = tries
		s.wrongTotal++
		s.log.Warn("firstrun: wrong PIN", "client", client, "tries", len(tries), "total", s.wrongTotal)
		switch {
		case s.wrongTotal >= maxWrongTotal:
			s.log.Warn("firstrun: too many wrong PINs; the page closes")
			s.closeForGood()
			return errClosed
		case len(tries) >= maxWrongClient:
			return locked(tries, now)
		}
		return wire.Errorf(http.StatusUnauthorized, "wrong_pin", "wrong PIN")
	}
	for k, exp := range s.cookies {
		if !now.Before(exp) {
			delete(s.cookies, k)
		}
	}
	if len(s.cookies) >= maxCookies {
		return wire.Errorf(http.StatusTooManyRequests, "rate_limited", "too many open setup pages")
	}
	token := wire.Random(32)
	s.cookies[hashCookie(token)] = now.Add(cookieTTL)
	s.log.Info("firstrun: PIN accepted", "client", client)
	//nolint:gosec // G124: plain http on the LAN by design (ADMIN-API §5); a Secure cookie would never come back.
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/", MaxAge: int(cookieTTL / time.Second),
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// locked answers a client whose wrong PINs fill its window; it ends when the oldest one leaves the window.
func locked(tries []time.Time, now time.Time) error {
	return &wire.Error{Status: http.StatusTooManyRequests, Code: "rate_limited",
		Message: "too many wrong PINs from this device; wait, then try again", RetryAfter: tries[0].Add(clientWindow).Sub(now)}
}
