package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/devXpro/homie/internal/config"
	"github.com/devXpro/homie/internal/relay"
)

// relayJSON is RelayStatus (ADMIN-API §2).
type relayJSON struct {
	Configured bool       `json:"configured"`
	Domain     string     `json:"domain"`
	State      string     `json:"state"`
	Since      *time.Time `json:"since"`
	LastError  string     `json:"lastError"`
	SelfCheck  *struct {
		OK        bool      `json:"ok"`
		CheckedAt time.Time `json:"checkedAt"`
		Error     string    `json:"error"`
	} `json:"selfCheck"`
}

// errCodeInArgv refuses a connect code on the command line: it holds the relay token, and argv lands in shell history
// and the process list. The message never repeats the argument.
var errCodeInArgv = errors.New("the connect code is a secret and is never taken from the command line (shell " +
	"history, the process list); give it on stdin: homed relay connect - (paste it and press Enter, or pipe it in, " +
	"e.g. pbpaste | homed relay connect -)")

// cmdRelayConnect stores a connect code read from stdin: relay.domain into homed.yaml and the token into relay-token
// (0600). A running homed switches to the relay at once. It never prints the code or the token.
func cmdRelayConnect(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fl, dataDir := newFlags("relay connect", stderr)
	pos, err := parseFlags(fl, args)
	if err != nil {
		return err
	}
	switch {
	case len(pos) == 0:
		return errors.New("missing \"-\"; usage: homed relay connect [--data-dir D] - (the code comes on stdin)")
	case len(pos) > 1:
		return errors.New("too many arguments; usage: homed relay connect [--data-dir D] -")
	case pos[0] != "-":
		return errCodeInArgv
	}
	line, err := bufio.NewReader(io.LimitReader(stdin, 4096)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read code from stdin: %w", err)
	}
	code, err := relay.ParseCode(line)
	if err != nil {
		return err
	}
	formatted, err := relay.FormatCode(code)
	if err != nil {
		return err
	}

	dir, err := config.ResolveDataDir(*dataDir)
	if err != nil {
		return err
	}
	if w := config.DataDirWarning(dir); w != "" {
		fmt.Fprintln(stderr, "warning:", w)
	}
	ctx := context.Background()
	c, err := connect(ctx, dir, true, stderr)
	if err != nil {
		return err
	}
	defer c.Close()
	var before, after relayJSON
	_ = c.do(ctx, "GET", "/relay", nil, &before)
	if err := c.do(ctx, "PUT", "/relay", map[string]string{"code": formatted}, &after); err != nil {
		return fmt.Errorf("relay connect: %w", err)
	}
	if c.offline {
		fmt.Fprintf(stdout, "Relay set to %s in %s; the token is in %s (0600). homed connects when it starts.\n",
			code.Domain, filepath.Join(dir, config.FileName), filepath.Join(dir, config.RelayTokenFile))
	} else {
		fmt.Fprintf(stdout, "Relay set to %s; homed is connecting (see: homed relay status).\n", code.Domain)
	}
	if before.Domain != "" && before.Domain != code.Domain {
		fmt.Fprintln(stderr, "The relay domain changed, and passkeys belong to their domain: run homed admin reset-access "+
			"and open the new link to sign in again.")
	}
	return nil
}

// cmdRelayStatus prints the relay state of the running homed.
func cmdRelayStatus(args []string, stdout, stderr io.Writer) error {
	fl, dataDir := newFlags("relay status", stderr)
	if err := noArgs(fl, args); err != nil {
		return err
	}
	dir, err := config.ResolveDataDir(*dataDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	c, err := connect(ctx, dir, false, stderr)
	if err != nil {
		if errors.Is(err, errNotRunning) {
			if cfg, cerr := config.Load(dir); cerr == nil && cfg.Relay.Domain != "" {
				fmt.Fprintf(stdout, "relay:      %s (configured)\n", cfg.Relay.Domain)
			}
		}
		return err
	}
	defer c.Close()
	var st relayJSON
	if err := c.do(ctx, "GET", "/relay", nil, &st); err != nil {
		return err
	}
	if !st.Configured {
		fmt.Fprintln(stdout, "relay:      not configured (run: homed relay connect - and paste the code)")
		return nil
	}
	state := st.State
	if st.Since != nil {
		state += " since " + st.Since.Local().Format(time.DateTime)
	}
	fmt.Fprintf(stdout, "relay:      %s\nstate:      %s\n", st.Domain, state)
	if st.LastError != "" {
		fmt.Fprintf(stdout, "last error: %s\n", st.LastError)
	}
	switch {
	case st.SelfCheck == nil:
		fmt.Fprintln(stdout, "self-check: not run yet (at connect, then every 30 s)")
	case st.SelfCheck.OK:
		fmt.Fprintf(stdout, "self-check: ok at %s\n", st.SelfCheck.CheckedAt.Local().Format(time.DateTime))
	default:
		fmt.Fprintf(stdout, "self-check: FAILED at %s: %s\n", st.SelfCheck.CheckedAt.Local().Format(time.DateTime), st.SelfCheck.Error)
	}
	return nil
}
