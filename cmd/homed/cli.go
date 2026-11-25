package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/devXpro/mcp-gatekeeper/adminsock"

	"github.com/devXpro/homie/internal/config"
	"github.com/devXpro/homie/internal/tunnel"
)

// The homed CLI talks to the running homed over its admin socket (the gatekeeper's adminsock): the command names are
// the words the operator types (`homed plugin list` runs "plugin list"). Only `homed cloudflare connect` also works
// while homed is stopped, in this process.

// socketCLI is the admin socket of the data dir dir.
func socketCLI(dir string) adminsock.CLI {
	return adminsock.CLI{Socket: filepath.Join(dir, config.SocketFile), Prog: "homed", App: "homed",
		Hint: "Is homed running? Start it (the service, or: homed run), or pass the --data-dir it uses."}
}

// runCLI runs a command of the running homed.
func runCLI(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	dataDir, args, err := dataDirArg(args)
	if err != nil {
		fmt.Fprintln(stderr, "homed:", err)
		return exitUsage
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		fmt.Fprintln(stderr, "homed: no command")
		return exitUsage
	}
	dir, err := config.ResolveDataDir(dataDir)
	if err != nil {
		fmt.Fprintln(stderr, "homed:", err)
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cmd, sub := args[0], ""
	if len(args) > 1 {
		sub = args[1]
	}
	switch {
	case cmd == "admin" && (sub == "setup-link" || sub == "reset-access"):
		return adminLink(ctx, dir, sub, args[2:], stdout, stderr)
	case cmd == "admin" && sub == "firstrun":
		return socketCLI(dir).Run(ctx, args[1:], nil, stdout, stderr)
	case cmd == "cloudflare" && sub == "connect":
		return cloudflareConnect(ctx, dir, args[2:], stdout, stderr)
	case cmd == "plugin" && sub == "add":
		cmdArgs, err := pluginAddArgs(args[2:])
		if err != nil {
			fmt.Fprintln(stderr, "homed plugin add:", err)
			return exitUsage
		}
		return socketCLI(dir).Run(ctx, append([]string{"plugin", "add"}, cmdArgs...), nil, stdout, stderr)
	case cmd == "tool" && sub == "call":
		// The arguments come from stdin only with "-": a call typed at a terminal must not wait for its input.
		var in io.Reader
		if n := len(args); n > 3 && args[n-1] == "-" {
			in = stdin
		}
		return socketCLI(dir).Run(ctx, args, in, stdout, stderr)
	case cmd == "cloudflare" || cmd == "plugin" || cmd == "tool" || cmd == "update":
		return socketCLI(dir).Run(ctx, args, nil, stdout, stderr)
	}
	fmt.Fprint(stderr, usage)
	fmt.Fprintf(stderr, "homed: unknown command %q\n", strings.Join(args[:min(len(args), 2)], " "))
	return exitUsage
}

// dataDirArg takes --data-dir D (or --data-dir=D) out of args, before a "--".
func dataDirArg(args []string) (dir string, rest []string, err error) {
	wantDir := false
	for i, a := range args {
		switch {
		case wantDir:
			dir, wantDir = a, false
		case a == "--":
			return dir, append(rest, args[i:]...), nil
		case a == "--data-dir" || a == "-data-dir":
			wantDir = true
		case strings.HasPrefix(a, "--data-dir="):
			dir = strings.TrimPrefix(a, "--data-dir=")
		default:
			rest = append(rest, a)
		}
	}
	if wantDir {
		return "", nil, errors.New("--data-dir needs a directory")
	}
	return dir, rest, nil
}

// setupURLRe finds the link of setup-link and reset-access in their output.
var setupURLRe = regexp.MustCompile(`https?://\S+/setup#t=[A-Za-z0-9_-]+`)

// adminLink runs setup-link or reset-access and prints the link alone on stdout, so it can be piped (into open), and
// the rest of the output on stderr. The link is a secret until used: never paste it into a chat.
func adminLink(ctx context.Context, dir, name string, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "homed admin %s takes no arguments\n", name)
		return exitUsage
	}
	var out bytes.Buffer
	code := socketCLI(dir).Run(ctx, []string{name}, nil, &out, stderr)
	text := out.String()
	link := setupURLRe.FindString(text)
	if link == "" {
		_, _ = io.WriteString(stdout, text)
		return code
	}
	_, _ = io.WriteString(stderr, strings.Replace(text, "  "+link+"\n", "", 1))
	_, _ = fmt.Fprintln(stdout, link)
	return code
}

// pluginAddArgs resolves the plugin's executable to an absolute path now: homed may run from launchd with another
// working directory and a minimal PATH.
func pluginAddArgs(args []string) ([]string, error) {
	i := 0
	for i < len(args) && args[i] != "--" {
		i++
	}
	if i+1 >= len(args) {
		return nil, errors.New("usage: homed plugin add --id ID -- <command> [args…]")
	}
	bin, err := exec.LookPath(args[i+1])
	if err != nil {
		return nil, fmt.Errorf("plugin executable: %w", err)
	}
	abs, err := filepath.Abs(bin)
	if err != nil {
		return nil, err
	}
	out := append([]string(nil), args...)
	out[i+1] = abs
	return out, nil
}

// socketAnswers reports whether a homed answers on the admin socket of dir.
func socketAnswers(ctx context.Context, dir string) bool {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", filepath.Join(dir, config.SocketFile))
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// cloudflareConnect is `homed cloudflare connect <host> [--overwrite-dns]`: through the running homed, which then runs
// the tunnel at once, or, while homed is stopped, in this process (holding the instance lock, so homed cannot start
// halfway), writing the tunnel into the data dir and the mode into homed.yaml. Either way the Cloudflare login link is
// printed, and opened in the browser on a Mac; stdin is never read.
func cloudflareConnect(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) int {
	host, overwrite, err := tunnel.ParseConnectArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, "homed cloudflare connect:", strings.TrimPrefix(err.Error(), "usage: "))
		return exitUsage
	}
	out := &linkOpener{w: stdout}
	if socketAnswers(ctx, dir) {
		return socketCLI(dir).Run(ctx, append([]string{"cloudflare", "connect"}, args...), nil, out, stderr)
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "homed cloudflare connect:", err)
		return 1
	}
	unlock, err := config.LockInstance(dir)
	if errors.Is(err, config.ErrAlreadyRunning) {
		return fail(errors.New("homed is starting or stopping; try again in a moment"))
	}
	if err != nil {
		return fail(err)
	}
	defer unlock()
	cfg, err := config.Load(dir)
	if err != nil {
		return fail(err)
	}
	port, err := config.LoopbackPort(cfg.ListenAddr())
	if err != nil {
		return fail(err)
	}
	bin, err := tunnel.FindCloudflared(cfg.Tunnel.Cloudflared)
	if err != nil {
		return fail(err)
	}
	rec, err := tunnel.ConnectOffline(ctx, tunnel.Options{Dir: filepath.Join(dir, config.TunnelDir),
		Service: fmt.Sprintf("http://127.0.0.1:%d", port), Binary: bin}, host, overwrite, out)
	if err != nil {
		return fail(err)
	}
	err = config.Update(dir, func(c *config.Config) error {
		c.Tunnel.Mode, c.Tunnel.Hostname = config.TunnelNamed, rec.Hostname
		return nil
	})
	if err != nil {
		return fail(fmt.Errorf("the tunnel is made, but homed.yaml was not updated: %w", err))
	}
	fmt.Fprintf(stdout, "homed runs the tunnel when it starts. The MCP address for ChatGPT and Claude: https://%s%s\n",
		rec.Hostname, tunnel.MCPPath)
	return 0
}

// loginURLRe is Cloudflare's authorization link as cloudflared prints it.
var loginURLRe = regexp.MustCompile(`https://dash\.cloudflare\.com/\S+`)

// openURL opens a URL in the default browser (macOS's open); tests replace it.
var openURL = func(u string) error {
	return exec.Command("open", u).Start() //nolint:gosec // G204: a dash.cloudflare.com link matched by loginURLRe
}

// linkOpener copies the output and opens the first Cloudflare login link in it, on a Mac.
type linkOpener struct {
	w      io.Writer
	mu     sync.Mutex
	tail   string
	opened bool
}

func (o *linkOpener) Write(p []byte) (int, error) {
	o.mu.Lock()
	if !o.opened {
		o.tail += string(p)
		if m := loginURLRe.FindString(o.tail); m != "" && strings.ContainsAny(o.tail[strings.Index(o.tail, m)+len(m):], " \n") {
			o.opened = true
			if runtime.GOOS == "darwin" {
				if err := openURL(m); err != nil {
					_, _ = fmt.Fprintf(o.w, "(could not open the browser: %v; open the link yourself)\n", err)
				}
			}
		}
		if len(o.tail) > 4096 {
			o.tail = o.tail[len(o.tail)-1024:]
		}
	}
	o.mu.Unlock()
	return o.w.Write(p)
}
