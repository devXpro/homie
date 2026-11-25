package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/devXpro/mcp-gatekeeper/adminsock"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/wire"
)

// Commands are the admin socket's commands over the admin API's own functions (`homed <command>` while homed runs;
// the caller is the home box's operator, via "cli"):
//
//	plugin add --id <id> -- <absolute path> [args…]   add a plugin from this machine; homed starts it
//	plugin list                                       the plugins and their state
//	plugin remove [--delete-data] <id>                stop and remove a plugin (its data stays unless --delete-data)
//	tool list                                         every tool, admin ones included
//	tool call <name> [<json arguments> | -]           call a tool; "-" reads the arguments from the standard input
//	update [--version <v>] [--allow-downgrade]        replace homed with a release; homed restarts
func (s *Server) Commands(dataDir string) []adminsock.Command {
	return []adminsock.Command{
		{Name: "plugin add", Usage: "--id <id> -- <command> [args…]", Summary: "add a plugin from this machine",
			Run: s.cmdPluginAdd},
		{Name: "plugin list", Summary: "list the plugins and their state", Run: s.cmdPluginList},
		{Name: "plugin remove", Usage: "[--delete-data] <id>", Summary: "stop and remove a plugin",
			Run: func(ctx context.Context, inv adminsock.Invocation) error { return s.cmdPluginRemove(ctx, inv, dataDir) }},
		{Name: "tool list", Summary: "list every tool, admin ones included", Run: s.cmdToolList},
		{Name: "tool call", Usage: "<name> [<json arguments> | -]", Stdin: true,
			Summary: `call a tool; "-" reads the arguments from the standard input (keep a PIN out of history)`,
			Run:     s.cmdToolCall},
		{Name: "update", Usage: "[--version <v>] [--allow-downgrade]",
			Summary: "update homed to the latest release (or --version); homed restarts", Run: s.cmdUpdate},
	}
}

// local is the request of an admin socket command.
func (s *Server) local(ctx context.Context) *request { return &request{ctx: ctx, local: true} }

// usage marks a command error the operator caused.
func usage(format string, a ...any) error {
	return fmt.Errorf("%w: %s", adminsock.ErrUsage, fmt.Sprintf(format, a...))
}

// cliError words an API error for the terminal: its message, with its code.
func cliError(err error) error {
	if we, ok := errors.AsType[*wire.Error](err); ok {
		return fmt.Errorf("%s (%s)", we.Message, we.Code)
	}
	return err
}

func (s *Server) cmdPluginAdd(ctx context.Context, inv adminsock.Invocation) error {
	var id string
	args := inv.Args
	for len(args) > 0 && args[0] != "--" {
		switch {
		case args[0] == "--id" && len(args) > 1:
			id, args = args[1], args[2:]
		default:
			return usage("plugin add --id <id> -- <command> [args…]")
		}
	}
	if len(args) < 2 || id == "" {
		return usage("plugin add --id <id> -- <command> [args…]")
	}
	command := args[1:]
	if !filepath.IsAbs(command[0]) {
		return usage("the command must start with an absolute path")
	}
	p, err := s.addLocal(s.local(ctx), id, command)
	if err != nil {
		return cliError(err)
	}
	_, err = fmt.Fprintf(inv.Out, "Added plugin %s: %s\nhomed is starting it (see: homed plugin list).\n", p.ID,
		strings.Join(command, " "))
	return err
}

func (s *Server) cmdPluginList(ctx context.Context, inv adminsock.Invocation) error {
	if len(inv.Args) > 0 {
		return usage("plugin list takes no arguments")
	}
	items, err := s.pluginSummaries(ctx)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		_, err := fmt.Fprintln(inv.Out, "No plugins yet; add one in the admin (Devices) or with: homed plugin add --id ID -- <command> [args…]")
		return err
	}
	tw := tabwriter.NewWriter(inv.Out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tTITLE\tSTATE\tVERSION\tSOURCE")
	for _, p := range items {
		state := p.State
		if p.LastError != "" {
			state += " (" + p.LastError + ")"
		}
		source := p.Source.Kind
		if p.Source.Repo != "" {
			source += " " + p.Source.Repo
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.ID, p.Title, state, p.Version, source)
	}
	return tw.Flush()
}

func (s *Server) cmdPluginRemove(ctx context.Context, inv adminsock.Invocation, dataDir string) error {
	deleteData := false
	var pos []string
	for _, a := range inv.Args {
		switch {
		case a == "--delete-data":
			deleteData = true
		case strings.HasPrefix(a, "-"):
			return usage("unknown flag %s", a)
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 1 {
		return usage("plugin remove [--delete-data] <id>")
	}
	id := pos[0]
	q := s.local(ctx)
	if _, err := s.pluginView(ctx, id); err != nil {
		return fmt.Errorf("no plugin %q (see homed plugin list)", id)
	}
	if s.opts.Installer == nil {
		return cliError(notReady("the plugin installer"))
	}
	inst := s.opts.Installer
	job, err := s.startJob(q, "plugin.remove", id, func(ctx context.Context, report Reporter) (string, error) {
		return id, inst.Remove(ctx, id, !deleteData, report)
	})
	if err != nil {
		return cliError(err)
	}
	if err := s.waitJob(ctx, job, inv.Out); err != nil {
		return err
	}
	if deleteData {
		_, err = fmt.Fprintf(inv.Out, "Removed plugin %s and its data.\n", id)
	} else {
		_, err = fmt.Fprintf(inv.Out, "Removed plugin %s. Its data stays in %s.\n", id,
			filepath.Join(dataDir, "plugins", id, "data"))
	}
	return err
}

// waitJob follows a job until it ends, printing its steps, and returns its error.
func (s *Server) waitJob(ctx context.Context, job *jobEntry, out io.Writer) error {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	shown := ""
	for {
		j := s.jobSnapshot(job)
		if j.Step != "" && j.Step != shown && j.State == "running" {
			shown = j.Step
			_, _ = fmt.Fprintf(out, "… %s\n", j.Step)
		}
		if j.State != "running" {
			if j.State == "ok" {
				return nil
			}
			if j.Error != nil {
				return fmt.Errorf("%s (%s)", j.Error.Message, j.Error.Code)
			}
			return fmt.Errorf("the job ended %s", j.State)
		}
		select {
		case <-ctx.Done():
			return errors.New("stopped following the job; it goes on in homed")
		case <-job.done:
		case <-tick.C:
		}
	}
}

func (s *Server) cmdToolList(_ context.Context, inv adminsock.Invocation) error {
	if len(inv.Args) > 0 {
		return usage("tool list takes no arguments")
	}
	tools := s.host.Tools(hub.AudienceAdmin)
	if len(tools) == 0 {
		_, err := fmt.Fprintln(inv.Out, "No tools (yet): see homed plugin list.")
		return err
	}
	tw := tabwriter.NewWriter(inv.Out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tAUDIENCE\tDESCRIPTION")
	for _, t := range tools {
		desc, _, _ := strings.Cut(t.Description, "\n")
		if r := []rune(desc); len(r) > 90 {
			desc = string(r[:89]) + "…"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", t.Name, t.Audience, desc)
	}
	return tw.Flush()
}

// errToolFailed is a tool that ran and reported a failure: the CLI exits with 1.
var errToolFailed = errors.New("the tool reported a failure")

// cmdToolCall calls one tool as the operator (via "cli"), prints its progress while it runs, then its text and its
// structured result as indented JSON. The arguments are a JSON object, or "-": the standard input (for anything
// secret, like a pairing PIN, which must stay out of shell history and the process list).
func (s *Server) cmdToolCall(ctx context.Context, inv adminsock.Invocation) error {
	if len(inv.Args) < 1 || len(inv.Args) > 2 {
		return usage("tool call <name> [<json arguments> | -]")
	}
	name, raw := inv.Args[0], []byte(`{}`)
	if len(inv.Args) == 2 {
		raw = []byte(inv.Args[1])
		if inv.Args[1] == "-" {
			raw = inv.Stdin
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
			return usage("the arguments must be a JSON object")
		}
	}
	e, err := s.beginCall(s.local(ctx), name, json.RawMessage(bytes.TrimSpace(raw)))
	if we, ok := errors.AsType[*wire.Error](err); ok && we.Code == "unknown_tool" {
		return fmt.Errorf("unknown tool %s (see homed tool list)", name)
	}
	if err != nil {
		return cliError(err)
	}
	shown := ""
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		c := s.callSnapshot(e)
		if p := c.Progress; p != nil && p.Message != "" && p.Message != shown {
			shown = p.Message
			if p.Total > 0 {
				_, _ = fmt.Fprintf(inv.Out, "… %s (%g/%g)\n", p.Message, p.Progress, p.Total)
			} else {
				_, _ = fmt.Fprintf(inv.Out, "… %s\n", p.Message)
			}
		}
		if c.State != "running" {
			return printCall(inv.Out, c)
		}
		select {
		case <-ctx.Done():
			return errors.New("stopped following the call; it goes on in homed")
		case <-e.done:
		case <-tick.C:
		}
	}
}

func printCall(out io.Writer, c callJSON) error {
	switch c.State {
	case "ok", "failed":
	case "error":
		if c.Error != nil {
			return fmt.Errorf("%s (%s)", c.Error.Message, c.Error.Code)
		}
		return errors.New("the call could not run")
	default:
		return fmt.Errorf("the call ended %s", c.State)
	}
	if c.Result != nil {
		if c.Result.Text != "" {
			_, _ = fmt.Fprintln(out, c.Result.Text)
		}
		var buf bytes.Buffer
		if len(c.Result.Structured) > 0 && json.Indent(&buf, c.Result.Structured, "", "  ") == nil {
			_, _ = fmt.Fprintln(out, buf.String())
		}
	}
	if c.State == "failed" {
		return errToolFailed
	}
	return nil
}

func (s *Server) cmdUpdate(ctx context.Context, inv adminsock.Invocation) error {
	var version string
	allow := false
	for args := inv.Args; len(args) > 0; {
		switch {
		case args[0] == "--allow-downgrade":
			allow, args = true, args[1:]
		case args[0] == "--version" && len(args) > 1:
			version, args = args[1], args[2:]
		default:
			return usage("update [--version <v>] [--allow-downgrade]")
		}
	}
	job, err := s.startHubUpdate(s.local(ctx), version, allow)
	if err != nil {
		return cliError(err)
	}
	if err := s.waitJob(ctx, job, inv.Out); err != nil {
		return err
	}
	_, err = fmt.Fprintf(inv.Out, "Updated to %s; homed restarts now.\n", s.jobSnapshot(job).Message)
	return err
}
