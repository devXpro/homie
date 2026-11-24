package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"syscall"
	"time"

	"github.com/devXpro/homie/internal/admin"
	"github.com/devXpro/homie/internal/auth"
	"github.com/devXpro/homie/internal/config"
	"github.com/devXpro/homie/internal/plugins"
	"github.com/devXpro/homie/internal/relay"
)

// The CLI speaks the admin API of ADMIN-API §6: to the running homed over its socket, or, for the commands that also
// work while homed is stopped (admin, relay connect, keys, plugins), to the same API served in-process over the data
// dir. Either way the paths, the bodies and the audit records are the same.

// errNotRunning means nothing answers on the socket.
var errNotRunning = errors.New("homed is not running")

// apiError is an error answer of the admin API (ADMIN-API §1.2).
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string { return e.Message }

// isCode reports whether err is an API error with the code.
func isCode(err error, code string) bool {
	ae, ok := errors.AsType[*apiError](err)
	return ok && ae.Code == code
}

// apiClient is one connection of the CLI to the admin API.
type apiClient struct {
	hc      *http.Client
	offline bool   // the in-process API: homed is not running
	close   func() // releases the in-process API
}

const apiBase = "http://homed/api/admin" // the host is a placeholder: the transport dials the socket

// connect returns a client for the running homed. When homed is not running it returns errNotRunning, or, with
// offlineOK, a client for the in-process API (Close releases it).
func connect(ctx context.Context, dir string, offlineOK bool, stderr io.Writer) (*apiClient, error) {
	sock := filepath.Join(dir, config.SocketFile)
	var dialer net.Dialer
	if c, err := dialer.DialContext(ctx, "unix", sock); err == nil {
		_ = c.Close()
		return &apiClient{hc: &http.Client{
			Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", sock)
			}},
			Timeout: time.Minute,
		}}, nil
	}
	if !offlineOK {
		return nil, fmt.Errorf("%w (no answer on %s); start it with: homed run", errNotRunning, sock)
	}
	h, closeFn, err := openOfflineAdmin(ctx, dir, stderr)
	if err != nil {
		return nil, err
	}
	return &apiClient{hc: &http.Client{Transport: handlerTransport{h}}, offline: true, close: closeFn}, nil
}

// Close releases the client.
func (c *apiClient) Close() {
	if c.close != nil {
		c.close()
	}
}

// do sends one request; body (when not nil) goes as JSON and out receives a 2xx answer. Other answers become
// *apiError.
func (c *apiClient) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, rd) //nolint:gosec // G704: a fixed placeholder host; the transport dials only the local socket
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req) //nolint:gosec // G704: dials only the local socket, or serves in-process
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return fmt.Errorf("%w (it stopped meanwhile)", errNotRunning)
		}
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Code != "" {
			return &apiError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
		}
		return fmt.Errorf("homed answered %s", resp.Status)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// openOfflineAdmin serves the admin API's socket routes in-process over the data dir: the database (importing a
// phase 1 homed.yaml first, as homed run would), homed.yaml and relay-token. It holds the instance lock until it is
// closed, so homed cannot start halfway through a command. Nothing runs: plugin changes apply at the next start.
func openOfflineAdmin(ctx context.Context, dir string, stderr io.Writer) (http.Handler, func(), error) {
	unlock, err := config.LockInstance(dir)
	if errors.Is(err, config.ErrAlreadyRunning) {
		return nil, nil, fmt.Errorf("homed is starting or stopping on %s; try again in a moment", dir)
	}
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (http.Handler, func(), error) {
		unlock()
		return nil, nil, err
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return fail(err)
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	st, err := openStore(ctx, dir, cfg, log)
	if err != nil {
		return fail(err)
	}
	host, err := plugins.New(nil, dir, log)
	if err != nil {
		_ = st.Close()
		return fail(err)
	}
	auditW := admin.NewAuditWriter(st.Audit(), st.People(), log)
	runtime := &pluginRuntime{st: st, host: host}
	port, _ := config.LoopbackPort(cfg.ListenAddr())
	rm := newRelayManager(dir, "", relay.Config{Domain: cfg.Relay.Domain, LocalPort: port}, false, log)
	a, err := auth.New(auth.Options{
		Store: st, Audit: auditW, Version: version, Log: log,
		RelyingParty: func() (auth.RelyingParty, bool) {
			if dom := rm.Domain(); dom != "" {
				return auth.ForDomain(dom), true
			}
			return auth.RelyingParty{}, false
		},
	})
	if err == nil {
		var srv *admin.Server
		srv, err = admin.New(admin.Options{
			Store: st, Host: host, Runtime: runtime, Relay: rm, Installer: newInstaller(dir, st, runtime, host, nil),
			Audit: auditW, Version: version, Commit: buildCommit(), LocalName: localUserName(),
			LocalRoutes: a.LocalHandler(), Log: log,
		})
		if err == nil {
			return srv.LocalHandler(), func() {
				srv.Close()
				auditW.Close()
				host.Close()
				_ = st.Close()
				unlock()
			}, nil
		}
	}
	auditW.Close()
	host.Close()
	_ = st.Close()
	return fail(err)
}

// handlerTransport answers requests with an in-process handler, as the socket would.
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if r.Body == nil {
		r.Body = http.NoBody
	}
	w := &memResponse{header: http.Header{}}
	t.h.ServeHTTP(w, r)
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return &http.Response{
		Status: fmt.Sprintf("%d %s", w.status, http.StatusText(w.status)), StatusCode: w.status,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: w.header,
		Body: io.NopCloser(&w.body), ContentLength: int64(w.body.Len()), Request: r,
	}, nil
}

// memResponse is an http.ResponseWriter that keeps the answer in memory.
type memResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (m *memResponse) Header() http.Header { return m.header }

func (m *memResponse) WriteHeader(code int) {
	if m.status == 0 {
		m.status = code
	}
}

func (m *memResponse) Write(b []byte) (int, error) {
	m.WriteHeader(http.StatusOK)
	return m.body.Write(b)
}
