package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devXpro/homie/internal/auth"
	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/keys"
	"github.com/devXpro/homie/internal/relay"
	"github.com/devXpro/homie/internal/store"
)

// stallingStore is a store whose Keys().List, once armed, reads the keys and then waits for release before it
// returns them: a rebuild (SIGHUP, a registration) that read the store just before a revoke and is slow to swap.
type stallingStore struct {
	store.Store
	mu      sync.Mutex
	armed   bool
	reading chan struct{} // closed when the armed List has read
	release chan struct{}
}

func (s *stallingStore) arm() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.armed = true
}

func (s *stallingStore) Keys() store.Keys { return stallingKeys{Keys: s.Store.Keys(), s: s} }

type stallingKeys struct {
	store.Keys
	s *stallingStore
}

func (k stallingKeys) List(ctx context.Context) ([]store.Key, error) {
	list, err := k.Keys.List(ctx)
	k.s.mu.Lock()
	armed := k.s.armed
	k.s.armed = false
	k.s.mu.Unlock()
	if armed {
		close(k.s.reading)
		<-k.s.release
	}
	return list, err
}

// A revoked key must stay revoked when another rebuild read the store before the revoke (review L1): every rebuild
// reads and swaps under one lock, so the revoke's own rebuild lands last.
func TestKeyRebuildCannotResurrectARevokedKey(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, shortTempDir(t))
	if err := db.People().Create(ctx, store.Person{ID: "roman", Name: "Roman", Role: store.RoleOwner, Plugins: []string{"*"},
		UserHandle: []byte("roman-handle")}); err != nil {
		t.Fatal(err)
	}
	secret, hash := keys.Generate()
	if err := db.Keys().Create(ctx, store.Key{ID: "k_1", PersonID: "roman", SHA256: hash, Label: "Site"}); err != nil {
		t.Fatal(err)
	}
	st := &stallingStore{Store: db, reading: make(chan struct{}), release: make(chan struct{})}
	kr, err := newKeyResolver(ctx, st, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	if p, err := kr.ResolveKey(ctx, secret); err != nil || p.ID != "roman" {
		t.Fatalf("the key before the revoke: %+v %v", p, err)
	}

	st.arm()
	stale := make(chan error, 1)
	go func() { stale <- kr.Rebuild(ctx) }() // SIGHUP: reads the key, then stalls
	<-st.reading
	if err := db.Keys().Delete(ctx, "k_1"); err != nil { // the owner revokes it…
		t.Fatal(err)
	}
	fresh := make(chan error, 1)
	go func() { fresh <- kr.Rebuild(ctx) }() // …and the admin rebuilds before answering
	select {
	case err := <-fresh:
		t.Fatalf("the revoke's rebuild finished while an older one still held its read of the store: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(st.release)
	if err := <-stale; err != nil {
		t.Fatal(err)
	}
	if err := <-fresh; err != nil {
		t.Fatal(err)
	}
	if _, err := kr.ResolveKey(ctx, secret); !errors.Is(err, hub.ErrUnknownKey) {
		t.Fatalf("the revoked key works again: %v", err)
	}
}

// The audit log keeps 180 days: older records go at start and then on every tick (ADMIN-API §3.6, review L3).
func TestPruneAudit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := openDB(t, shortTempDir(t))
	now := time.Now()
	if _, err := db.Audit().Append(ctx, []store.AuditRecord{
		{At: now.Add(-181 * 24 * time.Hour), Kind: store.AuditAdmin, Via: "cli", Action: "old", OK: true},
		{At: now.Add(-179 * 24 * time.Hour), Kind: store.AuditAdmin, Via: "cli", Action: "recent", OK: true},
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		pruneAudit(ctx, db, discardLog(), time.Hour)
	}()
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		recs, err := db.Audit().Query(ctx, store.AuditFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) == 1 {
			if recs[0].Action != "recent" {
				t.Fatalf("kept %+v", recs)
			}
			break
		}
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatalf("not pruned at start: %+v", recs)
		}
	}
	cancel()
	<-done
}

// startQuiet runs serve like startDaemon, with no terminal, and returns the daemon and its log.
func startQuiet(t *testing.T, opts runOptions) (*daemon, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan *daemon, 1)
	opts.ready = func(d *daemon) { ready <- d }
	done := make(chan error, 1)
	go func() { done <- serve(ctx, opts, slog.New(slog.NewTextHandler(logs, nil))) }()
	t.Cleanup(func() {
		cancel()
		<-done
		if t.Failed() {
			t.Logf("homed log:\n%s", logs.String())
		}
	})
	select {
	case d := <-ready:
		return d, logs
	case err := <-done:
		t.Fatalf("serve: %v\n%s", err, logs.String())
	case <-time.After(30 * time.Second):
		t.Fatal("homed did not start")
	}
	return nil, nil
}

// setupAddrNow reads where the first-run page listens, under the daemon's lock (a CLI call may have reopened it).
func setupAddrNow(d *daemon) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.setupAddr == nil {
		return ""
	}
	return d.setupAddr.String()
}

var cliPINRe = regexp.MustCompile(`PIN: (\d{4} \d{4})`)

// Under launchd homed has no terminal: the first-run PIN is never printed or logged. `homed admin firstrun` shows it,
// opens the page again once it closed, and refuses once the home has an owner (review H2).
func TestFirstRunWithoutATerminal(t *testing.T) {
	dir := shortTempDir(t)
	connectRelay(t, dir)
	port := freePort(t)
	d, logs := startQuiet(t, runOptions{
		dataDir: dir, listen: "127.0.0.1:" + strconv.Itoa(port), setupListen: "127.0.0.1:0",
		serverAddr: "127.0.0.1:" + strconv.Itoa(freePort(t)),
	})
	out := mustCLI(t, "admin", "firstrun", "--data-dir", dir)
	m := cliPINRe.FindStringSubmatch(out)
	first := setupAddrNow(d)
	if m == nil || !strings.Contains(out, "http://"+first+"/firstrun") || !strings.Contains(out, "open until") {
		t.Fatalf("admin firstrun:\n%s", out)
	}
	if log := logs.String(); strings.Contains(log, m[1]) || strings.Contains(log, strings.ReplaceAll(m[1], " ", "")) ||
		!strings.Contains(log, "homed admin firstrun") || !strings.Contains(log, "homed admin setup-link") {
		t.Fatalf("log:\n%s", log)
	}
	firstRunSignIn(t, "http://"+first, m[1])
	// While the page is open the command shows it as it is.
	if again := cliPINRe.FindStringSubmatch(mustCLI(t, "admin", "firstrun", "--data-dir", dir)); again == nil || again[1] != m[1] {
		t.Fatalf("a second call changed the open page: %v", again)
	}

	// The page closes (as after 30 minutes or 20 wrong PINs): the command opens it again, with a new PIN.
	d.mu.Lock()
	page := d.firstRun
	d.mu.Unlock()
	page.stop()
	deadline := time.Now().Add(10 * time.Second)
	for {
		d.mu.Lock()
		closed := d.firstRun == nil
		d.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the page did not close")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn, err := net.DialTimeout("tcp", first, time.Second); err == nil {
		_ = conn.Close()
		t.Fatal("the closed page still listens")
	}
	m2 := cliPINRe.FindStringSubmatch(mustCLI(t, "admin", "firstrun", "--data-dir", dir))
	if m2 == nil {
		t.Fatal("no PIN after reopening")
	}
	firstRunSignIn(t, "http://"+setupAddrNow(d), m2[1])

	// Once the owner has a passkey, there is nothing to open.
	b := &browser{t: t, base: "http://127.0.0.1:" + strconv.Itoa(port), host: "homie.test", origin: "https://homie.test"}
	if code, raw := b.register(newDevice("homie.test", "https://homie.test"), linkToken(t, dir, "https://homie.test"),
		map[string]string{"personName": "Roman"}); code != http.StatusCreated {
		t.Fatalf("register: %d %s", code, raw)
	}
	if _, _, err := cli(t, "", "admin", "firstrun", "--data-dir", dir); err == nil || !strings.Contains(err.Error(), "setup-link") {
		t.Fatalf("admin firstrun after the setup: %v", err)
	}
}

// The first-run page listens on the home network only while there is no relay to configure (review H2).
func TestFirstRunAddr(t *testing.T) {
	d := &daemon{relay: newRelayManager(t.TempDir(), "", relay.Config{}, false, discardLog())}
	if got := d.firstRunAddr(); got != "0.0.0.0:8090" {
		t.Fatalf("without a relay: %s", got)
	}
	d.relay = newRelayManager(t.TempDir(), "", relay.Config{Domain: "homie.test"}, false, discardLog())
	if got := d.firstRunAddr(); got != "127.0.0.1:8090" {
		t.Fatalf("with a relay: %s", got)
	}
	d.relay = newRelayManager(t.TempDir(), "", relay.Config{}, true, discardLog())
	d.dev = &auth.RelyingParty{ID: "localhost", Origin: "http://localhost:5173"}
	if got := d.firstRunAddr(); got != "127.0.0.1:8090" {
		t.Fatalf("development: %s", got)
	}
	d.setupListen = "192.168.68.63:9000"
	if got := d.firstRunAddr(); got != "192.168.68.63:9000" {
		t.Fatalf("setup_listen: %s", got)
	}
}

// Only a terminal gets the PIN: a log file or a pipe (launchd, systemd) does not.
func TestTerminalOf(t *testing.T) {
	if terminalOf(&bytes.Buffer{}) != nil {
		t.Fatal("a buffer is a terminal")
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "homed.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if terminalOf(f) != nil {
		t.Fatal("a log file is a terminal")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if terminalOf(w) != nil {
		t.Fatal("a pipe is a terminal")
	}
}

// The public listener's ReadTimeout cuts a slow request but never a long answer, because net/http lifts the read
// deadline once the body is read: an event stream or a long tool call outlives it (review L8). This pins that behaviour.
func TestPublicServerTimeouts(t *testing.T) {
	if srv := publicServer(http.NotFoundHandler()); srv.ReadHeaderTimeout != readHeaderTimeout ||
		srv.ReadTimeout != readTimeout || srv.IdleTimeout != idleTimeout || srv.WriteTimeout != 0 {
		t.Fatalf("server timeouts: %+v", srv)
	}
	const short = 150 * time.Millisecond
	results := make(chan string, 4)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if _, err := io.ReadAll(r.Body); err != nil {
				results <- "body: " + err.Error()
				return
			}
		}
		select {
		case <-r.Context().Done():
			results <- "cancelled"
		case <-time.After(3 * short): // a long answer, past ReadTimeout
			results <- "answered"
			w.WriteHeader(http.StatusNoContent)
		}
	})
	srv := &http.Server{Handler: h, ReadHeaderTimeout: short, ReadTimeout: short} // publicServer, with short timeouts
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	base := "http://" + ln.Addr().String()

	for _, send := range []func() (*http.Response, error){
		func() (*http.Response, error) { return http.Get(base + "/events") }, //nolint:noctx // a test against a local server
		func() (*http.Response, error) {
			return http.Post(base+"/calls", "application/json", strings.NewReader(`{"args":{}}`)) //nolint:noctx // same
		},
	} {
		res, err := send()
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if got := <-results; got != "answered" || res.StatusCode != http.StatusNoContent {
			t.Fatalf("long answer: %s, %d", got, res.StatusCode)
		}
	}

	// A body that stalls past ReadTimeout is cut.
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "POST /calls HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 10\r\n\r\n{\"a"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-results:
		if !strings.HasPrefix(got, "body: ") || !strings.Contains(got, "timeout") {
			t.Fatalf("slow body: %s", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a slow body was never cut")
	}
}
