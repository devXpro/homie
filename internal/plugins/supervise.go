package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/devXpro/homie/internal/hub"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// supervise runs p until ctx is done, restarting it after every exit: with backoff 1, 2, 4… up to 60 s, and every
// 5 minutes once it crashed 5 times within 5 minutes (the failing state). A run that stays up for a minute resets both.
func (h *Host) supervise(ctx context.Context, p *plugin) {
	defer h.wg.Done()
	t := h.timing
	backoff := t.backoffMin
	var crashes []time.Time
	failing := false
	for {
		upFor, err := h.runOnce(ctx, p)
		if ctx.Err() != nil {
			h.setState(p, hub.PluginStopped, "")
			return
		}
		now := time.Now()
		if upFor >= t.stableAfter {
			backoff, failing, crashes = t.backoffMin, false, nil
		}
		recent := crashes[:0]
		for _, c := range crashes {
			if now.Sub(c) < t.crashWindow {
				recent = append(recent, c)
			}
		}
		recent = append(recent, now)
		crashes = recent
		if len(crashes) >= t.crashLimit {
			failing = true
		}

		delay, state := backoff, hub.PluginStarting
		if failing {
			delay, state = t.failingRetry, hub.PluginFailing
		} else {
			backoff = min(backoff*2, t.backoffMax)
		}
		h.setFailed(p, state, err.Error())
		h.log.Warn("plugin stopped unexpectedly", "plugin", p.id, "err", err, "restart_in", delay, "state", state)
		p.logs.event("stopped: %v; restart in %v", err, delay)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			h.setState(p, hub.PluginStopped, err.Error())
			return
		case <-timer.C:
		}
	}
}

// runOnce starts the plugin, connects to it and serves it until it exits or ctx is done. It returns how long the
// plugin was up and why the run ended.
func (h *Host) runOnce(ctx context.Context, p *plugin) (time.Duration, error) {
	h.setState(p, hub.PluginStarting, h.lastError(p))
	if err := mkdir0700(p.dir); err != nil {
		return 0, err
	}

	// Plain pipes rather than cmd.StdoutPipe: cmd.Wait must not close our ends while the MCP session still reads them.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		closeAll(stdinR, stdinW)
		return 0, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		closeAll(stdinR, stdinW, stdoutR, stdoutW)
		return 0, err
	}
	cmd := exec.Command(p.command[0], p.command[1:]...) //nolint:gosec // running the configured plugin is the point
	cmd.Dir = p.dir
	cmd.Env = h.pluginEnv(p)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinR, stdoutW, stderrW
	// Its own process group: a Ctrl-C in homed's terminal must not reach the plugins; the hub stops them in order.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = cmd.Start()
	closeAll(stdinR, stdoutW, stderrW) // the child holds its own copies
	if err != nil {
		closeAll(stdinW, stdoutR, stderrR)
		return 0, err
	}
	p.logs.event("started pid %d: %v", cmd.Process.Pid, p.command)

	logsDone := make(chan struct{})
	go func() {
		defer close(logsDone)
		p.logs.copy(stderrR)
		_ = stderrR.Close()
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	// finish stops the process if it is still running and waits for its stderr to drain.
	finish := func(cs *mcp.ClientSession, cause error) error {
		if cs != nil {
			_ = cs.Close() // closes the plugin's stdin and our end of its stdout
		} else {
			closeAll(stdinW, stdoutR)
		}
		waitErr := h.terminate(cmd, exited)
		<-logsDone
		p.logs.event("exited: %v", exitError(waitErr))
		if cause == nil {
			return exitError(waitErr)
		}
		return fmt.Errorf("%w (%w)", cause, exitError(waitErr))
	}

	refresh := make(chan struct{}, 1)
	client := mcp.NewClient(&mcp.Implementation{Name: "homie", Version: h.HubVersion}, &mcp.ClientOptions{
		Logger:       h.log.With("plugin", p.id),
		Capabilities: &mcp.ClientCapabilities{},
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case refresh <- struct{}{}:
			default:
			}
		},
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			h.deliverProgress(p.id, req.Params)
		},
	})
	connectCtx, cancel := context.WithTimeout(ctx, h.timing.rpcTimeout)
	cs, err := client.Connect(connectCtx, &mcp.IOTransport{Reader: stdoutR, Writer: stdinW}, nil)
	cancel()
	if err != nil {
		return 0, finish(nil, fmt.Errorf("initialize: %w", err))
	}
	tools, err := h.listTools(ctx, p, cs)
	if err != nil {
		return 0, finish(cs, fmt.Errorf("tools/list: %w", err))
	}
	h.setUp(p, cs, cs.InitializeResult(), tools)
	upAt := time.Now()
	h.log.Info("plugin up", "plugin", p.id, "tools", len(tools), "protocol", cs.InitializeResult().ProtocolVersion)

	sessionDone := make(chan struct{})
	go func() {
		_ = cs.Wait()
		close(sessionDone)
	}()
	for {
		select {
		case <-refresh:
			tools, err := h.listTools(ctx, p, cs)
			if err != nil {
				h.log.Warn("tools/list after list_changed failed", "plugin", p.id, "err", err)
				continue
			}
			h.setTools(p, tools)
		case waitErr := <-exited:
			exited <- waitErr // let finish see it too
			h.setState(p, hub.PluginStarting, "")
			return time.Since(upAt), finish(cs, nil)
		case <-sessionDone:
			h.setState(p, hub.PluginStarting, "")
			err := finish(cs, nil)
			return time.Since(upAt), fmt.Errorf("session closed (%w)", err)
		case <-ctx.Done():
			h.setState(p, hub.PluginStopped, "")
			_ = finish(cs, nil)
			return time.Since(upAt), ctx.Err()
		}
	}
}

// terminate waits for the process to exit: at once if it already has, otherwise after SIGTERM and, killGrace later,
// SIGKILL. It returns the error of cmd.Wait.
func (h *Host) terminate(cmd *exec.Cmd, exited chan error) error {
	select {
	case err := <-exited:
		return err
	default:
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(h.timing.killGrace)
	defer timer.Stop()
	select {
	case err := <-exited:
		return err
	case <-timer.C:
	}
	_ = cmd.Process.Kill()
	return <-exited
}

func exitError(err error) error {
	if err == nil {
		return errors.New("exited with status 0")
	}
	return fmt.Errorf("exited: %w", err)
}

func (h *Host) lastError(p *plugin) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return p.lastErr
}

func closeAll(files ...*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}
