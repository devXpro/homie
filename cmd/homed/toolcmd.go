package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/devXpro/homie/internal/config"
)

// toolJSON is Tool and callJSON is Call (ADMIN-API §2), as far as the CLI shows them.
type toolJSON struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Audience    string `json:"audience"`
}

type callJSON struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Progress *struct {
		Progress float64 `json:"progress"`
		Total    float64 `json:"total"`
		Message  string  `json:"message"`
	} `json:"progress"`
	Result *struct {
		Text       string          `json:"text"`
		Structured json.RawMessage `json:"structured"`
	} `json:"result"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// cmdToolList prints every tool of the running homed, admin tools included.
func cmdToolList(args []string, stdout, stderr io.Writer) error {
	fl, dataDir := newFlags("tool list", stderr)
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
		return err
	}
	defer c.Close()
	var tools struct {
		Items []toolJSON `json:"items"`
	}
	if err := c.do(ctx, "GET", "/tools?audience=admin", nil, &tools); err != nil {
		return err
	}
	if len(tools.Items) == 0 {
		fmt.Fprintln(stdout, "No tools (yet): see homed plugin list.")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tAUDIENCE\tDESCRIPTION")
	for _, t := range tools.Items {
		desc, _, _ := strings.Cut(t.Description, "\n")
		if r := []rune(desc); len(r) > 90 {
			desc = string(r[:89]) + "…"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", t.Name, t.Audience, desc)
	}
	return tw.Flush()
}

// maxToolArgs is the admin API's limit on tool arguments (ADMIN-API §1.1).
const maxToolArgs = 64 << 10

// cmdToolCall calls one tool through the running homed (the owner, via "cli"), prints its progress to stderr while it
// runs, then its text and its structured result as indented JSON. The arguments are a JSON object on the command
// line, or "-" to read them from stdin (for anything secret, like a pairing PIN, that must stay out of shell history
// and the process list). A tool that reports a failure makes homed exit with status 1.
func cmdToolCall(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fl, dataDir := newFlags("tool call", stderr)
	pos, err := parseFlags(fl, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return errors.New("usage: homed tool call <name> ['<json arguments>' | -]")
	}
	name, arguments := pos[0], json.RawMessage(`{}`)
	if len(pos) == 2 {
		raw := []byte(pos[1])
		if pos[1] == "-" {
			if raw, err = io.ReadAll(io.LimitReader(stdin, maxToolArgs+1)); err != nil {
				return fmt.Errorf("read the arguments from stdin: %w", err)
			}
			if len(raw) > maxToolArgs {
				return errors.New("the arguments on stdin are over 64 KiB")
			}
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
			return errors.New("the arguments must be a JSON object")
		}
		arguments = json.RawMessage(bytes.TrimSpace(raw))
	}
	dir, err := config.ResolveDataDir(*dataDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	c, err := connect(ctx, dir, false, stderr)
	if err != nil {
		return err
	}
	defer c.Close()
	var call callJSON
	err = c.do(ctx, "POST", "/tools/"+url.PathEscape(name)+"/calls", map[string]json.RawMessage{"args": arguments}, &call)
	switch {
	case isCode(err, "unknown_tool"):
		return fmt.Errorf("unknown tool %s (see homed tool list)", name)
	case err != nil:
		return err
	}
	if err := followCall(ctx, c, &call, stderr); err != nil {
		return err
	}
	switch call.State {
	case "ok", "failed":
	case "error":
		if call.Error != nil {
			return fmt.Errorf("%s (%s)", call.Error.Message, call.Error.Code)
		}
		return errors.New("the call could not run")
	default:
		return fmt.Errorf("the call ended %s", call.State)
	}
	out := stdout
	if call.State == "failed" {
		out = stderr
	}
	if call.Result != nil {
		if call.Result.Text != "" {
			fmt.Fprintln(out, call.Result.Text)
		}
		var buf bytes.Buffer
		if len(call.Result.Structured) > 0 && json.Indent(&buf, call.Result.Structured, "", "  ") == nil {
			fmt.Fprintln(stdout, buf.String())
		}
	}
	if call.State == "failed" {
		return errToolFailed
	}
	return nil
}

// followCall polls a running call until it ends and prints each new progress message to stderr.
func followCall(ctx context.Context, c *apiClient, call *callJSON, stderr io.Writer) error {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	shown := ""
	for {
		if p := call.Progress; p != nil && p.Message != "" && p.Message != shown {
			shown = p.Message
			if p.Total > 0 {
				fmt.Fprintf(stderr, "… %s (%g/%g)\n", p.Message, p.Progress, p.Total)
			} else {
				fmt.Fprintf(stderr, "… %s\n", p.Message)
			}
		}
		if call.State != "running" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		if err := c.do(ctx, "GET", "/calls/"+url.PathEscape(call.ID), nil, call); err != nil {
			return err
		}
	}
}
