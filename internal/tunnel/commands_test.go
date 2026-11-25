package tunnel

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devXpro/mcp-gatekeeper/adminsock"
	"github.com/devXpro/mcp-gatekeeper/core"

	"github.com/devXpro/homie/internal/tunnel/tunneltest"
)

// syncBuffer is a bytes.Buffer safe for the writer and the test reading it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// socket serves the commands over a real admin socket and returns its client.
func socket(t *testing.T, cmds []adminsock.Command) *adminsock.Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "hc") // short: a socket path is at most 103 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	reg := adminsock.NewRegistry()
	if err := reg.Add(cmds...); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s")
	srv, err := adminsock.New(adminsock.Options{Path: path, Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })
	return adminsock.NewClient(path)
}

func TestCommands(t *testing.T) {
	upWait = 10 * time.Second
	r := newRig(t)
	var (
		mu      sync.Mutex
		audited []string
		ready   bool // whether the owner has a passkey of the current URL
	)
	c := socket(t, Commands(r.m, func(_ context.Context, e core.AuditEntry) {
		mu.Lock()
		defer mu.Unlock()
		audited = append(audited, e.Action+" "+e.Target+" "+e.Detail["via"])
	}, func(context.Context) (bool, error) { mu.Lock(); defer mu.Unlock(); return ready, nil }))
	ctx := context.Background()

	var out bytes.Buffer
	if err := c.Run(ctx, "cloudflare status", nil, nil, &out); err != nil || !strings.Contains(out.String(), "mode:  off") {
		t.Fatalf("status = %v\n%s", err, out.String())
	}

	// connect: the login link streams out before the operator authorizes, then the result.
	stream := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, "cloudflare connect", []string{"home.example.com"}, nil, stream) }()
	tunneltest.Eventually(t, "the login link in the output", func() bool {
		return strings.Contains(stream.String(), "https://dash.cloudflare.com/")
	})
	r.fake.Set("login-release")
	if err := <-done; err != nil {
		t.Fatalf("connect = %v\n%s", err, stream.String())
	}
	got := stream.String()
	for _, want := range []string{"Step 1/4", "Connected:", "The hub answers at https://home.example.com",
		"https://home.example.com/mcp", "The owner has no passkey for https://home.example.com yet", "homed admin setup-link"} {
		if !strings.Contains(got, want) {
			t.Errorf("connect output lacks %q:\n%s", want, got)
		}
	}

	// quick over a domain: refused, as conflict.
	err := c.Run(ctx, "cloudflare quick", nil, nil, &out)
	if re, ok := errors.AsType[*adminsock.RemoteError](err); !ok || re.Code != "conflict" {
		t.Errorf("quick over a domain = %v", err)
	}
	// Bad arguments are usage errors.
	for _, args := range [][]string{nil, {"a.example.com", "b.example.com"}, {"--force", "a.example.com"}, {"https://x"}} {
		err := c.Run(ctx, "cloudflare connect", args, nil, &out)
		if re, ok := errors.AsType[*adminsock.RemoteError](err); !ok || re.Code != "invalid" {
			t.Errorf("connect %v = %v", args, err)
		}
	}

	out.Reset()
	if err := c.Run(ctx, "cloudflare disconnect", nil, nil, &out); err != nil || !strings.Contains(out.String(), "stopped") {
		t.Fatalf("disconnect = %v\n%s", err, out.String())
	}
	out.Reset()
	mu.Lock()
	ready = true
	mu.Unlock()
	if err := c.Run(ctx, "cloudflare quick", nil, nil, &out); err != nil ||
		!strings.Contains(out.String(), "https://"+tunneltest.QuickHost(2)+"/mcp") { // run 1 was the named tunnel
		t.Fatalf("quick = %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "no passkey") {
		t.Errorf("the passkey hint while the owner can sign in:\n%s", out.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(audited, ",") != "tunnel.connect home.example.com cli,tunnel.disconnect  cli,tunnel.quick  cli" {
		t.Errorf("audit %q", audited)
	}
}

func TestParseConnectArgs(t *testing.T) {
	for _, tc := range []struct {
		args      []string
		host      string
		overwrite bool
	}{
		{[]string{"Home.Example.com"}, "home.example.com", false},
		{[]string{"--overwrite-dns", "home.example.com"}, "home.example.com", true},
		{[]string{"home.example.com", "--overwrite-dns"}, "home.example.com", true},
	} {
		host, ow, err := ParseConnectArgs(tc.args)
		if err != nil || host != tc.host || ow != tc.overwrite {
			t.Errorf("%v = %q %v %v", tc.args, host, ow, err)
		}
	}
	if _, _, err := ParseConnectArgs(nil); !errors.Is(err, adminsock.ErrUsage) {
		t.Errorf("no host = %v", err)
	}
}
