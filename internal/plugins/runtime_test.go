package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devXpro/homie/internal/hub"
)

// started counts the starts of the fake plugin in its log.
func started(h *Host) int {
	return countPrefix(stripTimes(h.Logs("fake")), "homie: started pid")
}

// stripTimes drops the RFC 3339 time the host puts after "homie:" in its own log lines.
func stripTimes(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if rest, ok := strings.CutPrefix(l, "homie: "); ok {
			if _, after, found := strings.Cut(rest, " "); found {
				l = "homie: " + after
			}
		}
		out = append(out, l)
	}
	return out
}

func TestAddRestartRemove(t *testing.T) {
	h, dataDir := newHost(t, fastTiming)
	if err := h.Add(fake()); err != nil {
		t.Fatal(err)
	}
	waitUp(t, h, "fake")
	if got := h.PluginInstructions("fake"); got != "Fake instructions." {
		t.Errorf("instructions = %q", got)
	}
	if got := h.PluginInstructions("nope"); got != "" {
		t.Errorf("instructions of an unknown plugin = %q", got)
	}
	if err := h.Add(fake()); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("a second fake: %v", err)
	}
	for _, bad := range []Spec{{ID: "Bad", Command: []string{fakeBin}}, {ID: "kettle"}} {
		if err := h.Add(bad); err == nil {
			t.Errorf("Add(%+v) succeeded", bad)
		}
	}

	if err := h.Restart("fake"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, h, "the second start", func() bool { return started(h) == 2 && state(h, "fake").State == hub.PluginUp })
	if n := countPrefix(stripTimes(h.Logs("fake")), "homie: exited"); n != 1 {
		t.Fatalf("restart did not stop the first process (%d exits):\n%s", n, strings.Join(h.Logs("fake"), "\n"))
	}
	res, err := h.Call(t.Context(), alice, "fake_echo", json.RawMessage(`{"text":"again"}`))
	if err != nil || res.Text != "again" {
		t.Fatalf("call after restart = %+v, %v", res, err)
	}

	changed := h.Changed()
	if err := h.Remove("fake"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Error("removing a plugin with tools did not fire Changed")
	}
	if st, tools := h.Status(), h.Tools(hub.AudienceAdmin); len(st) != 0 || len(tools) != 0 {
		t.Fatalf("after remove: status %+v, tools %v", st, names(tools))
	}
	if _, err := h.Call(t.Context(), alice, "fake_echo", nil); !errors.Is(err, hub.ErrUnknownTool) {
		t.Fatalf("call after remove: %v", err)
	}
	logFile, err := os.ReadFile(filepath.Join(dataDir, "logs", "fake.log")) //nolint:gosec // test temp dir
	if err != nil || strings.Count(string(logFile), "homie: ") == 0 || !strings.Contains(string(logFile), "exited") {
		t.Fatalf("the removed plugin's process did not exit before Remove returned: %v\n%s", err, logFile)
	}
	for _, op := range []func(string) error{h.Remove, h.Restart} {
		if err := op("fake"); !errors.Is(err, ErrUnknownPlugin) {
			t.Errorf("on a removed plugin: %v", err)
		}
	}
}

func TestAddBeforeStartAndAfterClose(t *testing.T) {
	h, err := New(nil, t.TempDir(), slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	h.timing = fastTiming
	if err := h.Add(fake()); err != nil {
		t.Fatal(err)
	}
	if st := state(h, "fake"); st.State != hub.PluginStopped || started(h) != 0 {
		t.Fatalf("before Start: %+v", st)
	}
	h.Start(t.Context())
	waitUp(t, h, "fake")
	h.Close()
	if err := h.Add(Spec{ID: "echo", Command: []string{exampleBin}}); !errors.Is(err, errClosed) {
		t.Fatalf("Add after Close: %v", err)
	}
}

func TestSync(t *testing.T) {
	h, _ := newHost(t, fastTiming, fake())
	waitUp(t, h, "fake")

	// The same set changes nothing.
	if err := h.Sync([]Spec{fake()}); err != nil {
		t.Fatal(err)
	}
	if n := started(h); n != 1 || state(h, "fake").State != hub.PluginUp {
		t.Fatalf("an unchanged plugin was restarted (%d starts)", n)
	}

	// A new command restarts the plugin with it; a new id is added.
	echo := Spec{ID: "echo", Command: []string{exampleBin}}
	if err := h.Sync([]Spec{echo, fake("--legacy")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, h, "fake restarted and echo up", func() bool {
		return started(h) == 2 && state(h, "fake").State == hub.PluginUp && state(h, "echo").State == hub.PluginUp
	})
	logs := strings.Join(h.Logs("fake"), "\n")
	if !strings.Contains(logs, "--legacy") {
		t.Fatalf("the restart did not use the new command:\n%s", logs)
	}

	// Gone from the set: stopped and forgotten. Invalid sets change nothing.
	if err := h.Sync([]Spec{echo}); err != nil {
		t.Fatal(err)
	}
	if st := h.Status(); len(st) != 1 || st[0].ID != "echo" {
		t.Fatalf("status = %+v", st)
	}
	for _, bad := range [][]Spec{{{ID: "Bad", Command: []string{"x"}}}, {echo, echo}, {{ID: "kettle"}}} {
		if err := h.Sync(bad); err == nil {
			t.Errorf("Sync(%+v) succeeded", bad)
		}
	}
	if st := h.Status(); len(st) != 1 || st[0].ID != "echo" || st[0].State != hub.PluginUp {
		t.Fatalf("an invalid Sync changed the plugins: %+v", st)
	}
}

func TestAwait(t *testing.T) {
	h, _ := newHost(t, fastTiming)
	ctx := t.Context()
	if err := h.Await(ctx, "fake"); !errors.Is(err, ErrUnknownPlugin) {
		t.Fatalf("unknown plugin: %v", err)
	}
	if err := h.Add(fake("--crash-on-start")); err != nil {
		t.Fatal(err)
	}
	if err := h.Await(ctx, "fake"); err == nil || !strings.Contains(err.Error(), "did not start") {
		t.Fatalf("a plugin that crashes on start: %v", err)
	}
	if err := h.Sync([]Spec{fake()}); err != nil {
		t.Fatal(err)
	}
	if err := h.Await(ctx, "fake"); err != nil {
		t.Fatalf("after the fix: %v", err)
	}
	// A plugin that never answers initialize: Await ends with the context.
	if err := h.Add(Spec{ID: "mute", Command: []string{"/bin/sleep", "30"}}); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := h.Await(short, "mute"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a mute plugin: %v", err)
	}
}
