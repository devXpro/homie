package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/devXpro/homie/internal/config"
)

// pluginJSON is PluginSummary (ADMIN-API §2), as far as the CLI shows it.
type pluginJSON struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Version   string `json:"version"`
	State     string `json:"state"`
	LastError string `json:"lastError"`
	Enabled   bool   `json:"enabled"`
	Source    struct {
		Kind string `json:"kind"`
		Repo string `json:"repo"`
	} `json:"source"`
}

// jobJSON is Job (ADMIN-API §2), as far as the CLI follows it.
type jobJSON struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Step  string `json:"step"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// cmdPluginAdd adds a plugin from this machine. The executable is resolved to an absolute path now, since homed may
// run from launchd with another working directory and a minimal PATH. A running homed starts it at once.
func cmdPluginAdd(args []string, stdout, stderr io.Writer) error {
	fl, dataDir := newFlags("plugin add", stderr)
	id := fl.String("id", "", "plugin id, e.g. tv")
	command, err := parseFlags(fl, args)
	if err != nil {
		return err
	}
	if *id == "" || len(command) == 0 {
		return errors.New("usage: homed plugin add --id ID -- <command> [args…]")
	}
	bin, err := exec.LookPath(command[0])
	if err != nil {
		return fmt.Errorf("plugin executable: %w", err)
	}
	if command[0], err = filepath.Abs(bin); err != nil {
		return err
	}
	dir, err := config.ResolveDataDir(*dataDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	c, err := connect(ctx, dir, true, stderr)
	if err != nil {
		return err
	}
	defer c.Close()
	err = c.do(ctx, "POST", "/plugins", map[string]any{"source": "local", "id": *id, "command": command}, nil)
	if isCode(err, "conflict") {
		return fmt.Errorf("plugin %q already exists; remove it first", *id)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Added plugin %s: %s\n", *id, strings.Join(command, " "))
	if c.offline {
		fmt.Fprintln(stderr, "homed is not running; the plugin starts with it.")
	} else {
		fmt.Fprintln(stderr, "The running homed is starting it (see: homed plugin list).")
	}
	return nil
}

// cmdPluginRemove stops a plugin and removes it. Its data dir stays unless --delete-data.
func cmdPluginRemove(args []string, stdout, stderr io.Writer) error {
	fl, dataDir := newFlags("plugin remove", stderr)
	deleteData := fl.Bool("delete-data", false, "also delete the plugin's data (e.g. paired TVs)")
	pos, err := parseFlags(fl, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: homed plugin remove [--delete-data] <id>")
	}
	id := pos[0]
	dir, err := config.ResolveDataDir(*dataDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	c, err := connect(ctx, dir, true, stderr)
	if err != nil {
		return err
	}
	defer c.Close()
	var job jobJSON
	path := "/plugins/" + url.PathEscape(id) + "?keepData=" + strconv.FormatBool(!*deleteData)
	err = c.do(ctx, "DELETE", path, nil, &job)
	if isCode(err, "not_found") {
		return fmt.Errorf("no plugin %q (see homed plugin list)", id)
	}
	if err != nil {
		return err
	}
	if err := waitJob(ctx, c, &job); err != nil {
		return err
	}
	if *deleteData {
		fmt.Fprintf(stdout, "Removed plugin %s and its data.\n", id)
	} else {
		fmt.Fprintf(stdout, "Removed plugin %s. Its data stays in %s.\n", id, filepath.Join(dir, "plugins", id, "data"))
	}
	return nil
}

// waitJob follows a job until it ends and returns its error.
func waitJob(ctx context.Context, c *apiClient, job *jobJSON) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for job.State == "running" {
		select {
		case <-ctx.Done():
			return fmt.Errorf("job %s did not finish: %w", job.ID, ctx.Err())
		case <-tick.C:
		}
		if err := c.do(ctx, "GET", "/jobs/"+url.PathEscape(job.ID), nil, job); err != nil {
			return err
		}
	}
	if job.State != "ok" {
		if job.Error != nil {
			return fmt.Errorf("%s (%s)", job.Error.Message, job.Error.Code)
		}
		return fmt.Errorf("job %s ended %s", job.ID, job.State)
	}
	return nil
}

// cmdPluginList prints the plugins with their state.
func cmdPluginList(args []string, stdout, stderr io.Writer) error {
	fl, dataDir := newFlags("plugin list", stderr)
	if err := noArgs(fl, args); err != nil {
		return err
	}
	dir, err := config.ResolveDataDir(*dataDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	c, err := connect(ctx, dir, true, stderr)
	if err != nil {
		return err
	}
	defer c.Close()
	var list struct {
		Items []pluginJSON `json:"items"`
	}
	if err := c.do(ctx, "GET", "/plugins", nil, &list); err != nil {
		return err
	}
	if len(list.Items) == 0 {
		fmt.Fprintln(stdout, "No plugins yet; add one with: homed plugin add --id ID -- <command> [args…]")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTITLE\tSTATE\tVERSION\tSOURCE")
	for _, p := range list.Items {
		state := p.State
		switch {
		case c.offline && p.Enabled:
			state = "-"
		case p.LastError != "":
			state += " (" + p.LastError + ")"
		}
		source := p.Source.Kind
		if p.Source.Repo != "" {
			source += " " + p.Source.Repo
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.ID, p.Title, state, p.Version, source)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if c.offline {
		fmt.Fprintln(stderr, "homed is not running: no live state.")
	}
	return nil
}
