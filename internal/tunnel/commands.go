package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/devXpro/mcp-gatekeeper/adminsock"
	"github.com/devXpro/mcp-gatekeeper/core"
	"github.com/devXpro/mcp-gatekeeper/tunnel/cloudflare"
)

// Auditor appends an entry to the admin audit; the commands audit what they change.
type Auditor func(ctx context.Context, e core.AuditEntry)

// upWait is how long a command waits for the tunnel to come up before it reports the state as it is.
var upWait = 45 * time.Second

// Commands are the admin socket's `cloudflare …` commands over m (`homed cloudflare …` while homed runs):
//
//	cloudflare connect <host> [--overwrite-dns]   connect the owner's domain; prints the Cloudflare login link, waits
//	cloudflare quick                              a quick tunnel, for trying (only while no tunnel is configured)
//	cloudflare status                             the mode, the state and the public and MCP URLs
//	cloudflare disconnect                         stop the tunnel; the hub is no longer reachable from the internet
func Commands(m *Manager, audit Auditor) []adminsock.Command {
	if audit == nil {
		audit = func(context.Context, core.AuditEntry) {}
	}
	return []adminsock.Command{
		{Name: "cloudflare connect", Usage: "<host> [--overwrite-dns]",
			Summary: "connect your domain through Cloudflare (prints the login link and waits)",
			Run:     func(ctx context.Context, inv adminsock.Invocation) error { return cmdConnect(ctx, m, audit, inv) }},
		{Name: "cloudflare quick", Summary: "start a quick tunnel: a temporary trycloudflare.com link, for trying",
			Run: func(ctx context.Context, inv adminsock.Invocation) error { return cmdQuick(ctx, m, audit, inv) }},
		{Name: "cloudflare status", Summary: "show the tunnel: mode, state, public and MCP addresses",
			Run: func(_ context.Context, inv adminsock.Invocation) error { return cmdStatus(m, inv) }},
		{Name: "cloudflare disconnect", Summary: "stop the tunnel; the hub is no longer reachable from the internet",
			Run: func(ctx context.Context, inv adminsock.Invocation) error { return cmdDisconnect(ctx, m, audit, inv) }},
	}
}

func socketDetail(p adminsock.Peer) map[string]string {
	d := map[string]string{"via": "cli"}
	if p.PID > 0 {
		d["peer_pid"], d["peer_exe"] = fmt.Sprint(p.PID), p.Exe
	}
	return d
}

// ParseConnectArgs reads `<host> [--overwrite-dns]` (the flag on either side).
func ParseConnectArgs(args []string) (host string, overwrite bool, err error) {
	for _, a := range args {
		switch {
		case a == "--overwrite-dns":
			overwrite = true
		case strings.HasPrefix(a, "-"):
			return "", false, fmt.Errorf("%w: unknown flag %s", adminsock.ErrUsage, a)
		case host != "":
			return "", false, fmt.Errorf("%w: one host, e.g. cloudflare connect home.example.com", adminsock.ErrUsage)
		default:
			host = strings.ToLower(strings.TrimSpace(a))
		}
	}
	if host == "" {
		return "", false, fmt.Errorf("%w: which domain? e.g. cloudflare connect home.example.com", adminsock.ErrUsage)
	}
	return host, overwrite, nil
}

var stepText = map[string]string{
	cloudflare.StepTunnel: "Step 2/4: the tunnel.",
	cloudflare.StepDNS:    "Step 3/4: the DNS record.",
	cloudflare.StepConfig: "Step 4/4: the tunnel's configuration.",
}

func cmdConnect(ctx context.Context, m *Manager, audit Auditor, inv adminsock.Invocation) error {
	host, overwrite, err := ParseConnectArgs(inv.Args)
	if err != nil {
		return err
	}
	job, err := m.Connect(host, overwrite)
	if errors.Is(err, cloudflare.ErrHostname) {
		return fmt.Errorf("%w: %w", adminsock.ErrUsage, err)
	}
	if err != nil {
		return err
	}
	audit(ctx, core.AuditEntry{Action: "tunnel.connect", Target: host, Detail: socketDetail(inv.Peer)})
	out := inv.Out
	_, _ = fmt.Fprintf(out, "Connecting %s through Cloudflare.\nStep 1/4: authorize Cloudflare: open the link below, "+
		"sign in and pick the zone of %s.\n", job.Hostname, job.Hostname)
	shownLink, step := false, cloudflare.StepLogin
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		j, _ := m.Job()
		if !shownLink && j.LoginURL != "" {
			shownLink = true
			_, _ = fmt.Fprintf(out, "\n  %s\n\nWaiting for the authorization (the link lives a few minutes).\n", j.LoginURL)
		}
		if j.Step != step && j.State == JobRunning {
			step = j.Step
			if t := stepText[step]; t != "" {
				_, _ = fmt.Fprintln(out, t)
			}
		}
		switch j.State {
		case JobFailed:
			return fmt.Errorf("the domain was not connected (step %s): %s", j.Step, j.Error)
		case JobOK:
			_, _ = fmt.Fprintf(out, "Connected: the tunnel of %s is set up.\n", j.Hostname)
			return reportUp(ctx, m, out)
		}
		select {
		case <-ctx.Done():
			return errors.New("the command was stopped; the connect goes on in homed: see homed cloudflare status")
		case <-tick.C:
		}
	}
}

// reportUp waits for the tunnel to come up (or upWait) and prints where the hub is.
func reportUp(ctx context.Context, m *Manager, out io.Writer) error {
	deadline := time.NewTimer(upWait)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		st := m.Status()
		if st.State == string(cloudflare.StateConnected) && st.URL != "" {
			_, _ = fmt.Fprintf(out, "The hub answers at %s.\nThe MCP address for ChatGPT and Claude: %s%s\n", st.URL, st.URL, MCPPath)
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			printStatus(out, st)
			_, _ = fmt.Fprintln(out, "The tunnel is not up yet; follow it with: homed cloudflare status")
			return nil
		case <-tick.C:
		}
	}
}

func cmdQuick(ctx context.Context, m *Manager, audit Auditor, inv adminsock.Invocation) error {
	if len(inv.Args) > 0 {
		return fmt.Errorf("%w: cloudflare quick takes no arguments", adminsock.ErrUsage)
	}
	if err := m.Quick(); err != nil {
		return err
	}
	audit(ctx, core.AuditEntry{Action: "tunnel.quick", Detail: socketDetail(inv.Peer)})
	_, _ = fmt.Fprintln(inv.Out, "A quick tunnel is starting. It is for trying: its link changes at every restart of "+
		"homed, and with it every passkey and connector.")
	return reportUp(ctx, m, inv.Out)
}

func cmdStatus(m *Manager, inv adminsock.Invocation) error {
	if len(inv.Args) > 0 {
		return fmt.Errorf("%w: cloudflare status takes no arguments", adminsock.ErrUsage)
	}
	printStatus(inv.Out, m.Status())
	return nil
}

func printStatus(out io.Writer, st Status) {
	_, _ = fmt.Fprintf(out, "mode:  %s\nstate: %s\n", st.Mode, st.State)
	if st.URL != "" {
		_, _ = fmt.Fprintf(out, "url:   %s\nmcp:   %s%s\n", st.URL, st.URL, MCPPath)
	}
	if !st.Since.IsZero() {
		_, _ = fmt.Fprintf(out, "since: %s\n", st.Since.Local().Format(time.DateTime))
	}
	if st.LastError != "" {
		_, _ = fmt.Fprintf(out, "error: %s\n", st.LastError)
	}
}

func cmdDisconnect(ctx context.Context, m *Manager, audit Auditor, inv adminsock.Invocation) error {
	if len(inv.Args) > 0 {
		return fmt.Errorf("%w: cloudflare disconnect takes no arguments", adminsock.ErrUsage)
	}
	if err := m.Disconnect(); err != nil {
		return err
	}
	audit(ctx, core.AuditEntry{Action: "tunnel.disconnect", Detail: socketDetail(inv.Peer)})
	_, _ = fmt.Fprintln(inv.Out, "The tunnel is stopped: the hub is no longer reachable from the internet. A named "+
		"tunnel and its DNS record stay in your Cloudflare account; homed cloudflare connect <host> reuses it.")
	return nil
}
