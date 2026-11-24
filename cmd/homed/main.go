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
                                               serve the hub, the admin (/admin), the plugins, the relay and the
                                               local socket; until the home has an owner, also the first-run page
                                               (30 min; on 0.0.0.0:8090 without a relay, 127.0.0.1:8090 with one)

First run and access (the home box only):
  homed admin firstrun                         open the first-run page again and print its address and PIN
  homed admin setup-link                       print a one-time link that adds a passkey for the owner (15 min)
  homed admin reset-access                     delete every passkey and session, print a new setup link

Relay:
  homed relay connect -                        store a connect code read from stdin (never from the command
                                               line: it would show in shell history and the process list)
  homed relay status                           connection and self-check of the running homed

Person keys (for ChatGPT Sites; the key itself is shown once):
  homed key new [--person ID] [--label TEXT] [--keychain SERVICE]
                                               print a new key, or save it to the macOS Keychain
  homed key list
  homed key revoke <key id>

Plugins:
  homed plugin add --id ID -- <command> [args…] add a plugin from this machine (a running homed starts it)
  homed plugin list
  homed plugin remove [--delete-data] <id>

Tools (through the running homed; every tool, admin ones included):
  homed tool list
  homed tool call <name> ['<json arguments>' | -]
                                               "-" reads the arguments from stdin (keep a PIN out of history)

  homed version

The CLI talks to the running homed over <data dir>/homed.sock. Without a running homed, the admin, relay connect,
key and plugin commands work on the data dir directly. Every command takes --data-dir D. The data dir defaults to
~/Library/Application Support/homie (macOS) or $XDG_DATA_HOME/homie (Linux); HOMED_DATA_DIR overrides it.
`

// errToolFailed is returned when a tool ran and reported a failure; homed exits with status 1 for it.
var errToolFailed = errors.New("the tool reported a failure")

func main() {
	err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	switch {
	case err == nil:
	case errors.Is(err, errRestart):
		// After a hub update: the service manager (launchd KeepAlive, systemd Restart=) starts the new binary.
		os.Exit(install.ExitCodeRestart)
	case errors.Is(err, errToolFailed):
		os.Exit(1)
	case errors.Is(err, flag.ErrHelp):
		os.Exit(2)
	default:
		fmt.Fprintln(os.Stderr, "homed:", err)
		os.Exit(2)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return flag.ErrHelp
	}
	cmd, rest := args[0], args[1:]
	sub := ""
	if len(rest) > 0 {
		sub = rest[0]
	}
	switch {
	case cmd == "run":
		return cmdRun(rest, stderr)
	case cmd == "admin" && sub == "firstrun":
		return cmdAdminFirstRun(rest[1:], stdout, stderr)
	case cmd == "admin" && sub == "setup-link":
		return cmdAdminSetupLink(rest[1:], stdout, stderr)
	case cmd == "admin" && sub == "reset-access":
		return cmdAdminResetAccess(rest[1:], stdout, stderr)
	case cmd == "relay" && sub == "connect":
		return cmdRelayConnect(rest[1:], stdin, stdout, stderr)
	case cmd == "relay" && sub == "status":
		return cmdRelayStatus(rest[1:], stdout, stderr)
	case cmd == "key" && sub == "new":
		return cmdKeyNew(rest[1:], stdout, stderr)
	case cmd == "key" && sub == "list":
		return cmdKeyList(rest[1:], stdout, stderr)
	case cmd == "key" && sub == "revoke":
		return cmdKeyRevoke(rest[1:], stdout, stderr)
	case cmd == "plugin" && sub == "add":
		return cmdPluginAdd(rest[1:], stdout, stderr)
	case cmd == "plugin" && sub == "list":
		return cmdPluginList(rest[1:], stdout, stderr)
	case cmd == "plugin" && sub == "remove":
		return cmdPluginRemove(rest[1:], stdout, stderr)
	case cmd == "tool" && sub == "list":
		return cmdToolList(rest[1:], stdout, stderr)
	case cmd == "tool" && sub == "call":
		return cmdToolCall(rest[1:], stdin, stdout, stderr)
	case cmd == "version" || cmd == "--version" || cmd == "-v":
		fmt.Fprintln(stdout, "homed", version)
		return nil
	case cmd == "help" || cmd == "-h" || cmd == "--help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", joinCmd(cmd, sub))
	}
}

func joinCmd(cmd, sub string) string {
	switch cmd {
	case "admin", "relay", "key", "plugin", "tool":
		return cmd + " " + sub
	}
	return cmd
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

// parseFlags parses fl over args and allows flags after positional arguments, as people type them
// (homed relay connect - --data-dir D). Everything after "--" is positional. It returns the positionals.
func parseFlags(fl *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fl.Parse(args); err != nil {
			return nil, err
		}
		rest := fl.Args()
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			return append(pos, rest...), nil
		}
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// noArgs parses flags for a command that takes no positional arguments.
func noArgs(fl *flag.FlagSet, args []string) error {
	pos, err := parseFlags(fl, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return fmt.Errorf("unexpected arguments: %v", pos)
	}
	return nil
}
