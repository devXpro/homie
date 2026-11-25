package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devXpro/homie/internal/store"
	"github.com/devXpro/homie/internal/tunnel"
)

// The activity log keeps 180 days: older records go at start, then daily.
func TestPruneActivity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "homie.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	now := time.Now()
	if _, err := st.Activity().Append(ctx, []store.ActivityRecord{
		{At: now.Add(-181 * 24 * time.Hour), Via: "cli", Tool: "old", OK: true},
		{At: now.Add(-179 * 24 * time.Hour), Via: "cli", Tool: "recent", OK: true},
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		pruneActivity(ctx, st, discardLog(), time.Hour)
	}()
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		recs, err := st.Activity().Query(ctx, store.ActivityFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) == 1 {
			if recs[0].Tool != "recent" {
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

// The first-run page listens on the home network only while no tunnel is configured, and never in development.
func TestFirstRunAddr(t *testing.T) {
	m, err := tunnel.New(tunnel.Options{Dir: t.TempDir(), Service: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	d := &daemon{tun: m}
	if addr, moves := d.firstRunAddr(); addr != "0.0.0.0:8090" || !moves {
		t.Fatalf("without a tunnel: %s %v", addr, moves)
	}
	// A named tunnel that cannot start (nothing connected in its directory) still counts as configured; it runs nothing.
	if err := m.Start(t.Context(), tunnel.ModeNamed, "home.example.com"); err == nil {
		t.Fatal("a named tunnel started without a connect")
	}
	if addr, moves := d.firstRunAddr(); addr != "127.0.0.1:8090" || moves {
		t.Fatalf("with a tunnel: %s %v", addr, moves)
	}
	d = &daemon{tun: m, dev: &devSite}
	if addr, _ := (&daemon{tun: mustTunnel(t), dev: &devSite}).firstRunAddr(); addr != "127.0.0.1:8090" {
		t.Fatalf("development: %s", addr)
	}
	d.setupListen = "192.0.2.10:9000"
	if addr, moves := d.firstRunAddr(); addr != "192.0.2.10:9000" || moves {
		t.Fatalf("setup_listen: %s %v", addr, moves)
	}
}

func mustTunnel(t *testing.T) *tunnel.Manager {
	t.Helper()
	m, err := tunnel.New(tunnel.Options{Dir: t.TempDir(), Service: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	return m
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
