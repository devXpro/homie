//go:build integration

package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The integration test runs the embedded client against the real frps v0.72.0 image, configured exactly as
// PLAN §5.5 (bindPort = vhostHTTPPort = 7000, token from a file, custom404Page), behind a TLS terminator in this
// process that plays Caddy's role (TLS for testDomain, then raw bytes to frps). It needs Docker.

const (
	frpsImage   = "ghcr.io/fatedier/frps:v0.72.0"
	offlineBody = "<html><body>Home is offline</body></html>"
	instanceID  = "it-instance"
)

func TestIntegrationRelay(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found")
	}
	pki := newTestPKI(t)
	token := randomHex(t)
	frpsAddr := startFrps(t, token)
	term := startTerminator(t, pki.Leaf, frpsAddr)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(InstanceHeader, instanceID)
		_, _ = io.WriteString(w, "hub saw "+r.Host+r.URL.Path) //nolint:gosec // plain-text test echo
	}))
	defer backend.Close()
	_, portStr, _ := net.SplitHostPort(backend.Listener.Addr().String())
	localPort, _ := strconv.Atoi(portStr)

	cfg := Config{Domain: testDomain, Token: token, LocalPort: localPort, CAFile: pki.CAFile, ServerAddr: term.addr()}
	public := publicClient(t, pki.CAFile, term.addr())

	t.Run("offline before connect", func(t *testing.T) {
		status, body, _ := get(t, public, "/anything")
		if status != http.StatusNotFound || !strings.Contains(body, "Home is offline") {
			t.Fatalf("got %d %q, want 404 offline page", status, body)
		}
		if err := cfg.SelfCheck(context.Background(), instanceID); err == nil || !strings.Contains(err.Error(), "offline") {
			t.Fatalf("SelfCheck while offline = %v", err)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	var c Client
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, cfg, nil) }()
	waitFor(t, 30*time.Second, "connected", func() bool { return c.Status().Connected })
	firstSince := c.Status().Since

	t.Run("requests reach the hub", func(t *testing.T) {
		status, body, hdr := get(t, public, "/api/v1/tools")
		if status != http.StatusOK || body != "hub saw "+testDomain+"/api/v1/tools" || hdr.Get(InstanceHeader) != instanceID {
			t.Fatalf("got %d %q %v", status, body, hdr)
		}
		if err := cfg.SelfCheck(context.Background(), instanceID); err != nil {
			t.Fatal(err)
		}
		if err := cfg.SelfCheck(context.Background(), "other"); err == nil {
			t.Fatal("SelfCheck accepted another instance id")
		}
	})

	t.Run("reconnects after the channel drops", func(t *testing.T) {
		term.dropAll()
		waitFor(t, 15*time.Second, "disconnected", func() bool { return !c.Status().Connected })
		waitFor(t, 30*time.Second, "reconnected", func() bool { return c.Status().Connected })
		if !c.Status().Since.After(firstSince) {
			t.Fatal("Since did not move on reconnect")
		}
		if err := cfg.SelfCheck(context.Background(), instanceID); err != nil {
			t.Fatal(err)
		}
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not stop")
	}
	if c.Status().Connected {
		t.Fatal("still connected after stop")
	}

	t.Run("offline after stop", func(t *testing.T) {
		waitFor(t, 15*time.Second, "offline page", func() bool {
			status, body, _ := get(t, public, "/healthz")
			return status == http.StatusNotFound && strings.Contains(body, "Home is offline")
		})
	})

	t.Run("refuses an untrusted certificate", func(t *testing.T) {
		other := newTestPKI(t)
		bad := cfg
		bad.CAFile = other.CAFile
		st := runUntilError(t, bad)
		if !strings.Contains(st.LastError, "certificate") {
			t.Fatalf("LastError = %q, want a certificate error", st.LastError)
		}
	})

	t.Run("reports a wrong token", func(t *testing.T) {
		bad := cfg
		bad.Token = randomHex(t)
		st := runUntilError(t, bad)
		if !strings.Contains(st.LastError, "token") || strings.Contains(st.LastError, bad.Token) {
			t.Fatalf("LastError = %q, want a token error without the token", st.LastError)
		}
	})
}

// runUntilError runs a client until it reports an error, checks it never connected, and stops it.
func runUntilError(t *testing.T, cfg Config) Status {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var c Client
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, cfg, nil) }()
	waitFor(t, 30*time.Second, "an error", func() bool { return c.Status().LastError != "" })
	st := c.Status()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if st.Connected {
		t.Fatal("connected despite the error")
	}
	t.Logf("LastError: %s", st.LastError)
	return st
}

func randomHex(t *testing.T) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// startFrps runs the frps image with the §5.5 config and returns its host address.
func startFrps(t *testing.T, token string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"frps.toml": `bindPort = 7000
vhostHTTPPort = 7000
auth.method = "token"
auth.tokenSource.type = "file"
auth.tokenSource.file.path = "/etc/frp/token"
custom404Page = "/etc/frp/offline.html"
log.to = "console"
log.level = "info"
`,
		"token":        token + "\n",
		"offline.html": offlineBody,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil { //nolint:gosec // read by frps in the container
			t.Fatal(err)
		}
	}
	name := "homie-t03-frps-" + randomHex(t)[:8]
	docker(t, "run", "-d", "--name", name, "-p", "127.0.0.1::7000", "-v", dir+":/etc/frp:ro", frpsImage, "-c", "/etc/frp/frps.toml")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", name).CombinedOutput() //nolint:gosec // fixed command
			t.Logf("frps logs:\n%s", out)
		}
		_ = exec.Command("docker", "rm", "-f", name).Run() //nolint:gosec // fixed command
	})
	addr := strings.TrimSpace(strings.Split(docker(t, "port", name, "7000/tcp"), "\n")[0])
	// Docker's port forwarder accepts TCP before frps listens, so wait for frps' own offline answer.
	hc := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	waitFor(t, 30*time.Second, "frps to answer", func() bool {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+"/", http.NoBody)
		if err != nil {
			return false
		}
		req.Host = testDomain
		resp, err := hc.Do(req)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusNotFound
	})
	return addr
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput() //nolint:gosec // test-controlled arguments
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// terminator ends TLS for testDomain and pipes raw bytes to frps, like Caddy in front of frps.
type terminator struct {
	ln    net.Listener
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func startTerminator(t *testing.T, cert tls.Certificate, upstream string) *terminator {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	term := &terminator{ln: ln, conns: map[net.Conn]struct{}{}}
	t.Cleanup(func() { _ = ln.Close(); term.dropAll() })
	go func() {
		for {
			down, err := ln.Accept()
			if err != nil {
				return
			}
			go term.pipe(down, upstream)
		}
	}()
	return term
}

func (term *terminator) addr() string { return term.ln.Addr().String() }

func (term *terminator) track(cs ...net.Conn) {
	term.mu.Lock()
	defer term.mu.Unlock()
	for _, c := range cs {
		term.conns[c] = struct{}{}
	}
}

func (term *terminator) dropAll() {
	term.mu.Lock()
	defer term.mu.Unlock()
	for c := range term.conns {
		_ = c.Close()
		delete(term.conns, c)
	}
}

func (term *terminator) pipe(down net.Conn, upstream string) {
	up, err := net.Dial("tcp", upstream)
	if err != nil {
		_ = down.Close()
		return
	}
	term.track(down, up)
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = io.Copy(up, down); _ = up.Close() })
	wg.Go(func() { _, _ = io.Copy(down, up); _ = down.Close() })
	wg.Wait()
}

// publicClient talks to https://testDomain through the terminator, the way ChatGPT reaches the relay.
func publicClient(t *testing.T, caFile, addr string) *http.Client {
	t.Helper()
	pem, err := os.ReadFile(caFile) //nolint:gosec // test temp file
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: testDomain, MinVersion: tls.VersionTLS12},
			DialContext: func(ctx context.Context, n, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, n, addr)
			},
			DisableKeepAlives: true, // a fresh connection per request, so dropped channels leave no stale ones
		},
	}
}

func get(t *testing.T, c *http.Client, path string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+testDomain+path, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) {
			return 0, err.Error(), nil
		}
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, resp.Body)
	return resp.StatusCode, buf.String(), resp.Header
}
