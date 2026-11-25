// Command homed is the Homie hub daemon and its CLI. See `homed help` and docs/DEV.md.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/devXpro/homie/internal/install"
)

// version and commit are set at build time with -ldflags "-X main.version=… -X main.commit=…".
var (
	version = "dev"
	commit  = ""
)

const usage = `homed — the Homie hub

Daemon:
  homed run [--listen 127.0.0.1:8080] [--setup-listen ADDR] [--dev-origin http://localhost:5173]
        serve the hub: the admin, the MCP endpoint /mcp for ChatGPT and Claude, the plugins, the Cloudflare
        tunnel (cloudflared as a child) and the admin socket; until the owner can sign in, also the first-run
        page (30 min; on 0.0.0.0:8090 while no tunnel is configured, 127.0.0.1:8090 with one)

The internet, through Cloudflare:
  homed cloudflare connect <host> [--overwrite-dns]
        connect your domain (its zone on your Cloudflare account): prints the Cloudflare login link, opens
        it on a Mac, and waits. Works while homed is stopped too.
  homed cloudflare quick         a temporary trycloudflare.com link, for trying (it changes at every restart)
  homed cloudflare status        the tunnel: mode, state, public and MCP addresses
  homed cloudflare disconnect    stop the tunnel

First run and access (the home box only):
  homed admin firstrun           open the first-run page and print its address and PIN
  homed admin setup-link         print a one-time link that registers the owner's passkey (15 min)
  homed admin reset-access       wipe the owner's passkeys, sessions and connectors, print a new setup link

Plugins and tools:
  homed plugin add --id ID -- <command> [args…]    add a plugin from this machine
  homed plugin list
  homed plugin remove [--delete-data] <id>
  homed tool list                                  every tool, admin ones included
  homed tool call <name> ['<json arguments>' | -]  "-" reads the arguments from stdin (keep a PIN out of
                                                   history)

  homed update [--version V] [--allow-downgrade]   update homed to a release; it restarts
  homed version

Every command but run, version and cloudflare connect talks to the running homed over <data dir>/homed.sock.
Every command takes --data-dir D. The data dir defaults to ~/Library/Application Support/homie (macOS) or
$XDG_DATA_HOME/homie (Linux); HOMED_DATA_DIR overrides it.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// Exit codes besides adminsock's 0, 1 and 2.
const exitUsage = 2

// run runs one command and returns the exit code: 0, 1 (a failure, a tool that failed), 2 (wrong usage or homed could
// not start), or install.ExitCodeRestart after a hub update.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "run":
		err := cmdRun(args[1:], stderr)
		switch {
		case err == nil:
			return 0
		case errors.Is(err, errRestart):
			// After a hub update: the service manager (launchd KeepAlive, systemd Restart=) starts the new binary.
			return install.ExitCodeRestart
		case errors.Is(err, flag.ErrHelp):
			return exitUsage
		default:
			fmt.Fprintln(stderr, "homed:", err)
			return exitUsage
		}
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "homed", version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	return runCLI(args, stdin, stdout, stderr)
}

// buildCommit is the -ldflags commit, else the VCS revision Go stamped into the binary (short), else "".
func buildCommit() string {
	if commit != "" {
		return commit
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				return s.Value[:7]
			}
		}
	}
	return ""
}

// newFlags returns a flag set with the --data-dir flag every command takes.
func newFlags(name string, stderr io.Writer) (*flag.FlagSet, *string) {
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(stderr)
	dataDir := fl.String("data-dir", "", "data directory (default: the platform data dir, or HOMED_DATA_DIR)")
	return fl, dataDir
}

// noArgs parses flags for a command that takes no positional arguments.
func noArgs(fl *flag.FlagSet, args []string) error {
	if err := fl.Parse(args); err != nil {
		return err
	}
	if fl.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", fl.Args())
	}
	return nil
}
