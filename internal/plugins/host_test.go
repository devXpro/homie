package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devXpro/homie/internal/hub"
)

// Binaries built once by TestMain.
var fakeBin, exampleBin string

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "plugins-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	fakeBin = filepath.Join(dir, "fakeplugin")
	exampleBin = filepath.Join(dir, "homie-echo")
	for bin, pkg := range map[string]string{fakeBin: "./testdata/fakeplugin", exampleBin: "../../pluginkit/example"} {
		out, err := exec.Command("go", "build", "-o", bin, pkg).CombinedOutput() //nolint:gosec // fixed test packages
		if err != nil {
			fmt.Fprintf(os.Stderr, "building %s: %v\n%s", pkg, err, out)
			return 1
		}
	}
	return m.Run()
}

// fastTiming keeps tests quick; individual tests override what they exercise.
var fastTiming = timing{
	backoffMin:   10 * time.Millisecond,
	backoffMax:   50 * time.Millisecond,
	crashWindow:  time.Minute,
	crashLimit:   5,
	failingRetry: time.Hour,
	stableAfter:  time.Hour,
	killGrace:    time.Second,
	rpcTimeout:   10 * time.Second,
}

func newHost(t *testing.T, tm timing, specs ...Spec) (*Host, string) {
	t.Helper()
	dataDir := t.TempDir()
	h, err := New(specs, dataDir, slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	h.timing = tm
	h.HubVersion = "test-version"
	h.Start(t.Context())
	t.Cleanup(h.Close)
	return h, dataDir
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func fake(args ...string) Spec {
	return Spec{ID: "fake", Command: append([]string{fakeBin, "serve"}, args...)}
}

// waitFor waits until cond holds, re-checking at every status, tool-set or log change of the host.
func waitFor(t *testing.T, h *Host, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		status, tools := h.statusChanged(), h.Changed()
		var logs []<-chan struct{}
		for _, p := range h.plugins {
			logs = append(logs, p.logs.changed())
		}
		if cond() {
			return
		}
		logChanged := make(chan struct{})
		stop := make(chan struct{})
		for _, l := range logs {
			go func() {
				select {
				case <-l:
					select {
					case logChanged <- struct{}{}:
					case <-stop:
					}
				case <-stop:
				}
			}()
		}
		select {
		case <-status:
		case <-tools:
		case <-logChanged:
		case <-deadline:
			close(stop)
			t.Fatalf("timed out waiting for %s; status %+v", what, h.Status())
		}
		close(stop)
	}
}

func state(h *Host, id string) hub.PluginStatus {
	for _, s := range h.Status() {
		if s.ID == id {
			return s
		}
	}
	return hub.PluginStatus{}
}

func waitUp(t *testing.T, h *Host, id string) {
	t.Helper()
	waitFor(t, h, id+" up", func() bool { return state(h, id).State == hub.PluginUp })
}

func names(tools []hub.Tool) []string {
	var out []string
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

func toolByName(tools []hub.Tool, name string) hub.Tool {
	for _, t := range tools {
		if t.Name == name {
			return t
		}
	}
	return hub.Tool{}
}

var roman = hub.Caller{Person: "Roman", Via: "cli"}

func TestListingAndAudiences(t *testing.T) {
	h, _ := newHost(t, fastTiming, fake())
	waitUp(t, h, "fake")

	admin := h.Tools(hub.AudienceAdmin)
	wantAdmin := []string{"fake_add", "fake_crash", "fake_echo", "fake_env", "fake_fail", "fake_list", "fake_log",
		"fake_progress", "fake_release", "fake_sleep", "fake_weird", "fake_writer"}
	if got := names(admin); !slices.Equal(got, wantAdmin) {
		t.Fatalf("admin tools = %v, want %v", got, wantAdmin)
	}
	wantAssistant := slices.DeleteFunc(slices.Clone(wantAdmin), func(n string) bool { return n == "fake_add" || n == "fake_weird" })
	if got := names(h.Tools(hub.AudienceAssistant)); !slices.Equal(got, wantAssistant) {
		t.Fatalf("assistant tools = %v, want %v", got, wantAssistant)
	}

	tests := []struct {
		name      string
		audience  hub.Audience
		timeout   time.Duration
		inventory bool
	}{
		{"fake_echo", hub.AudienceAssistant, 30 * time.Second, false},
		{"fake_sleep", hub.AudienceAssistant, time.Second, false},
		{"fake_add", hub.AudienceAdmin, 30 * time.Second, false},
		{"fake_list", hub.AudienceAssistant, 30 * time.Second, true},
		{"fake_weird", hub.AudienceAdmin, 300 * time.Second, false}, // second inventory ignored, timeout capped
		{"fake_writer", hub.AudienceAssistant, 30 * time.Second, false},
	}
	for _, tt := range tests {
		got := toolByName(admin, tt.name)
		if got.PluginID != "fake" || got.Audience != tt.audience || got.Timeout != tt.timeout || got.Inventory != tt.inventory {
			t.Errorf("%s = {plugin %q, audience %q, timeout %v, inventory %v}, want {fake, %q, %v, %v}", tt.name,
				got.PluginID, got.Audience, got.Timeout, got.Inventory, tt.audience, tt.timeout, tt.inventory)
		}
	}
	echo := toolByName(admin, "fake_echo")
	var schema map[string]any
	if err := json.Unmarshal(echo.InputSchema, &schema); err != nil || schema["type"] != "object" {
		t.Errorf("fake_echo input schema = %s (%v)", echo.InputSchema, err)
	}
	if echo.Description != "Echoes text." {
		t.Errorf("fake_echo description = %q", echo.Description)
	}
	var ann map[string]any
	if err := json.Unmarshal(toolByName(admin, "fake_list").Annotations, &ann); err != nil || ann["readOnlyHint"] != true {
		t.Errorf("fake_list annotations = %s (%v)", toolByName(admin, "fake_list").Annotations, err)
	}

	st := state(h, "fake")
	if st.Title != "Fake" || st.Version != "1.0.0" || st.LastError != "" {
		t.Errorf("status = %+v", st)
	}
	if got := h.Instructions(); got != "Fake instructions." {
		t.Errorf("instructions = %q", got)
	}
}

func TestInvalidNamesSkipped(t *testing.T) {
	h, _ := newHost(t, fastTiming, fake())
	waitUp(t, h, "fake")
	for _, n := range names(h.Tools(hub.AudienceAdmin)) {
		if n == "other_tool" || n == "fake-dash" || n == "fake_Upper" {
			t.Errorf("invalid tool %q was not skipped", n)
		}
	}
	if _, err := h.Call(t.Context(), roman, "other_tool", nil); !errors.Is(err, hub.ErrUnknownTool) {
		t.Errorf("calling a skipped tool: err = %v, want ErrUnknownTool", err)
	}
}

func TestCall(t *testing.T) {
	h, _ := newHost(t, fastTiming, fake())
	waitUp(t, h, "fake")
	ctx := t.Context()

	res, err := h.Call(ctx, roman, "fake_echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	var structured map[string]string
	if err := json.Unmarshal(res.Structured, &structured); err != nil {
		t.Fatalf("structured %s: %v", res.Structured, err)
	}
	if res.Text != "hi" || res.IsError || structured["caller"] != "Roman" || structured["via"] != "cli" {
		t.Errorf("fake_echo = %+v, structured %v", res, structured)
	}

	res, err = h.Call(ctx, roman, "fake_fail", nil)
	if err != nil || !res.IsError || res.Text != "The fake device is unreachable." {
		t.Errorf("fake_fail = %+v, %v; want IsError with text and nil err", res, err)
	}

	if res, err := h.Call(ctx, roman, "fake_list", nil); err != nil || res.Text != "one device" || res.Structured != nil {
		t.Errorf("fake_list = %+v, %v", res, err)
	}

	for _, name := range []string{"fake_nope", "nope_x", "", "fake"} {
		if _, err := h.Call(ctx, roman, name, nil); !errors.Is(err, hub.ErrUnknownTool) {
			t.Errorf("Call(%q) err = %v, want ErrUnknownTool", name, err)
		}
	}
	for _, args := range []string{`[1]`, `null`, `"x"`, `{`} {
		if _, err := h.Call(ctx, roman, "fake_echo", json.RawMessage(args)); err == nil {
			t.Errorf("Call with args %s succeeded, want an error", args)
		}
	}
}

func TestCallTimeout(t *testing.T) {
	h, _ := newHost(t, fastTiming, fake())
	waitUp(t, h, "fake")

	start := time.Now()
	_, err := h.Call(t.Context(), roman, "fake_sleep", json.RawMessage(`{"ms":20000}`)) // tool timeout is 1 s
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if d := time.Since(start); d < time.Second || d > 10*time.Second {
		t.Errorf("timed out after %v, want about 1s", d)
	}

	// The caller's own deadline wins when it is shorter.
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := h.Call(ctx, roman, "fake_sleep", json.RawMessage(`{"ms":20000}`)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}

	// The plugin is still fine.
	if res, err := h.Call(t.Context(), roman, "fake_sleep", json.RawMessage(`{"ms":1}`)); err != nil || res.Text != "slept" {
		t.Fatalf("after timeouts: %+v, %v", res, err)
	}
}

func TestCrashKeepsToolsAndReportsDown(t *testing.T) {
	tm := fastTiming
	tm.backoffMin, tm.backoffMax = time.Hour, time.Hour // stay down after the crash
	h, _ := newHost(t, tm, fake())
	waitUp(t, h, "fake")
	before := names(h.Tools(hub.AudienceAdmin))
	changed := h.Changed()

	if _, err := h.Call(t.Context(), roman, "fake_crash", nil); !errors.Is(err, hub.ErrPluginDown) {
		t.Fatalf("fake_crash err = %v, want ErrPluginDown", err)
	}
	waitFor(t, h, "the crash to be recorded", func() bool { return state(h, "fake").LastError != "" })
	st := state(h, "fake")
	if st.State != hub.PluginStarting || !strings.Contains(st.LastError, "exit status 3") {
		t.Errorf("status after crash = %+v", st)
	}
	if got := names(h.Tools(hub.AudienceAdmin)); !slices.Equal(got, before) {
		t.Errorf("tools after crash = %v, want the last known %v", got, before)
	}
	select {
	case <-changed:
		t.Error("Changed fired although the tool set did not change")
	default:
	}
	if _, err := h.Call(t.Context(), roman, "fake_echo", json.RawMessage(`{"text":"x"}`)); !errors.Is(err, hub.ErrPluginDown) {
		t.Errorf("call while down: err = %v, want ErrPluginDown", err)
	}
}

func TestCrashAndRestart(t *testing.T) {
	h, _ := newHost(t, fastTiming, fake())
	waitUp(t, h, "fake")

	if _, err := h.Call(t.Context(), roman, "fake_crash", nil); !errors.Is(err, hub.ErrPluginDown) {
		t.Fatalf("fake_crash err = %v, want ErrPluginDown", err)
	}
	waitFor(t, h, "a second start", func() bool {
		return countPrefix(h.Logs("fake"), "fake plugin starting") >= 2 && state(h, "fake").State == hub.PluginUp
	})
	res, err := h.Call(t.Context(), roman, "fake_echo", json.RawMessage(`{"text":"back"}`))
	if err != nil || res.Text != "back" {
		t.Fatalf("after restart: %+v, %v", res, err)
	}
	if !slices.ContainsFunc(h.Logs("fake"), func(l string) bool { return strings.Contains(l, "exit status 3") }) {
		t.Errorf("logs do not record the crash: %q", h.Logs("fake"))
	}
}

func TestFailingState(t *testing.T) {
	tm := fastTiming
	tm.crashLimit = 3
	h, _ := newHost(t, tm, fake("--crash-on-start"))
	waitFor(t, h, "failing", func() bool { return state(h, "fake").State == hub.PluginFailing })
	st := state(h, "fake")
	if !strings.Contains(st.LastError, "exit status 3") {
		t.Errorf("LastError = %q", st.LastError)
	}
	if n := countPrefix(h.Logs("fake"), "fake plugin crashing on start"); n != 3 {
		t.Errorf("started %d times before failing, want 3", n)
	}
	if tools := h.Tools(hub.AudienceAdmin); len(tools) != 0 {
		t.Errorf("a plugin that never came up has tools: %v", names(tools))
	}
}

func TestMissingBinary(t *testing.T) {
	tm := fastTiming
	tm.crashLimit = 2
	h, _ := newHost(t, tm, Spec{ID: "ghost", Command: []string{filepath.Join(t.TempDir(), "nope"), "serve"}})
	waitFor(t, h, "failing", func() bool { return state(h, "ghost").State == hub.PluginFailing })
	if st := state(h, "ghost"); !strings.Contains(st.LastError, "no such file") {
		t.Errorf("LastError = %q", st.LastError)
	}
}

func TestChangedFires(t *testing.T) {
	h, _ := newHost(t, fastTiming, fake())
	waitUp(t, h, "fake")
	changed := h.Changed()

	if _, err := h.Call(t.Context(), roman, "fake_add", json.RawMessage(`{"text":"extra"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(10 * time.Second):
		t.Fatal("Changed did not fire after the plugin added a tool")
	}
	if !slices.Contains(names(h.Tools(hub.AudienceAssistant)), "fake_extra") {
		t.Fatalf("fake_extra missing: %v", names(h.Tools(hub.AudienceAssistant)))
	}
	res, err := h.Call(t.Context(), roman, "fake_extra", nil)
	if err != nil || res.Text != "added tool ran" {
		t.Fatalf("fake_extra = %+v, %v", res, err)
	}
}

// TestLegacyProtocolPlugin covers plugins on SDKs that only speak the initialize handshake (2025-11-25), where
// list_changed arrives as a plain notification instead of through subscriptions/listen.
func TestLegacyProtocolPlugin(t *testing.T) {
	h, _ := newHost(t, fastTiming, fake("--legacy"))
	waitUp(t, h, "fake")
	h.mu.Lock()
	protocol := h.plugins[0].session.InitializeResult().ProtocolVersion
	h.mu.Unlock()
	if protocol != "2025-11-25" {
		t.Fatalf("negotiated %q, want 2025-11-25", protocol)
	}
	changed := h.Changed()
	if _, err := h.Call(t.Context(), roman, "fake_add", json.RawMessage(`{"text":"legacy"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(10 * time.Second):
		t.Fatal("Changed did not fire")
	}
	res, err := h.Call(t.Context(), roman, "fake_echo", json.RawMessage(`{"text":"old"}`))
	if err != nil || res.Text != "old" || !strings.Contains(string(res.Structured), `"caller":"Roman"`) {
		t.Fatalf("fake_echo = %+v, %v", res, err)
	}
	if got := h.Instructions(); got != "Fake instructions." {
		t.Errorf("instructions = %q", got)
	}
}

func TestStderrLog(t *testing.T) {
	h, dataDir := newHost(t, fastTiming, fake())
	waitUp(t, h, "fake")
	if _, err := h.Call(t.Context(), roman, "fake_log", json.RawMessage(`{"text":"hello from stderr"}`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, h, "the stderr line", func() bool { return slices.Contains(h.Logs("fake"), "hello from stderr") })
	if h.Logs("nope") != nil {
		t.Error("Logs of an unknown plugin is not nil")
	}

	h.Close()
	path := filepath.Join(dataDir, "logs", "fake.log")
	b, err := os.ReadFile(path) //nolint:gosec // a path inside t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"fake plugin starting\n", "hello from stderr\n", "homie: "} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s lacks %q:\n%s", path, want, b)
		}
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("%s mode = %v, want 0600", path, fi.Mode().Perm())
	}
}

func TestEnvAndDataDir(t *testing.T) {
	t.Setenv("HOMIE_TEST_LEAK", "secret")
	h, dataDir := newHost(t, fastTiming, fake())
	waitUp(t, h, "fake")
	res, err := h.Call(t.Context(), roman, "fake_env", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Env []string `json:"env"`
		Cwd string   `json:"cwd"`
	}
	if err := json.Unmarshal(res.Structured, &got); err != nil {
		t.Fatal(err)
	}
	pluginDir := filepath.Join(dataDir, "plugins", "fake", "data")
	env := map[string]string{}
	for _, kv := range got.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	want := map[string]string{
		"HOMIE_PLUGIN_ID":   "fake",
		"HOMIE_DATA_DIR":    pluginDir,
		"HOMIE_LOG_LEVEL":   "debug",
		"HOMIE_HUB_VERSION": "test-version",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env %s = %q, want %q", k, env[k], v)
		}
	}
	allowed := []string{"PATH", "HOME", "LANG", "TZ", "HOMIE_PLUGIN_ID", "HOMIE_DATA_DIR", "HOMIE_LOG_LEVEL",
		"HOMIE_HUB_VERSION"}
	if runtime.GOOS == "darwin" {
		allowed = append(allowed, "__CF_USER_TEXT_ENCODING") // added by macOS to every process
	}
	for k := range env {
		if !slices.Contains(allowed, k) {
			t.Errorf("unexpected env %s", k)
		}
	}
	resolved, _ := filepath.EvalSymlinks(pluginDir)
	if cwd, _ := filepath.EvalSymlinks(got.Cwd); cwd != resolved {
		t.Errorf("cwd = %q, want %q", got.Cwd, pluginDir)
	}
	if fi, err := os.Stat(pluginDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("plugin data dir: %v, %v", fi, err)
	}
}

func TestShutdown(t *testing.T) {
	h, _ := newHost(t, fastTiming, fake())
	waitUp(t, h, "fake")
	h.Close()
	if st := state(h, "fake"); st.State != hub.PluginStopped {
		t.Errorf("state after Close = %+v", st)
	}
	if _, err := h.Call(t.Context(), roman, "fake_echo", nil); !errors.Is(err, hub.ErrPluginDown) {
		t.Errorf("call after Close: err = %v, want ErrPluginDown", err)
	}
	h.Start(t.Context()) // no effect after Close
	if st := state(h, "fake"); st.State != hub.PluginStopped {
		t.Errorf("Start after Close changed the state: %+v", st)
	}
}

func TestShutdownKillsStubbornPlugin(t *testing.T) {
	tm := fastTiming
	tm.killGrace = 100 * time.Millisecond
	h, _ := newHost(t, tm, fake("--stubborn"))
	waitUp(t, h, "fake")
	start := time.Now()
	h.Close()
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Close took %v", d)
	}
	if !slices.ContainsFunc(h.Logs("fake"), func(l string) bool { return strings.Contains(l, "signal: killed") }) {
		t.Errorf("the stubborn plugin was not killed: %q", h.Logs("fake"))
	}
	if st := state(h, "fake"); st.State != hub.PluginStopped {
		t.Errorf("state after Close = %+v", st)
	}
}

func TestCloseDuringCall(t *testing.T) {
	h, _ := newHost(t, fastTiming, fake())
	waitUp(t, h, "fake")
	errc := make(chan error, 1)
	go func() {
		_, err := h.Call(context.Background(), roman, "fake_sleep", json.RawMessage(`{"ms":800}`))
		errc <- err
	}()
	waitFor(t, h, "the call to reach the plugin", func() bool { return slices.Contains(h.Logs("fake"), "sleeping 800") })
	h.Close()
	if err := <-errc; err != nil && !errors.Is(err, hub.ErrPluginDown) && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("in-flight call ended with %v", err)
	}
}

func TestExampleUnderHost(t *testing.T) {
	h, _ := newHost(t, fastTiming, Spec{ID: "echo", Command: []string{exampleBin}})
	waitUp(t, h, "echo")
	if got := names(h.Tools(hub.AudienceAdmin)); !slices.Equal(got, []string{"echo_list", "echo_reset", "echo_say"}) {
		t.Fatalf("admin tools = %v", got)
	}
	if got := names(h.Tools(hub.AudienceAssistant)); !slices.Equal(got, []string{"echo_list", "echo_say"}) {
		t.Fatalf("assistant tools = %v", got)
	}
	tools := h.Tools(hub.AudienceAdmin)
	if r := toolByName(tools, "echo_reset"); r.Audience != hub.AudienceAdmin || r.Timeout != 3*time.Minute {
		t.Errorf("echo_reset = %+v", r)
	}
	if !toolByName(tools, "echo_list").Inventory {
		t.Error("echo_list is not the inventory tool")
	}
	res, err := h.Call(t.Context(), hub.Caller{Person: "Tanya", Via: "site"}, "echo_say", json.RawMessage(`{"text":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	_ = json.Unmarshal(res.Structured, &out)
	if res.Text != "hello" || out["caller"] != "Tanya" {
		t.Errorf("echo_say = %+v", res)
	}
	res, err = h.Call(t.Context(), roman, "echo_list", nil)
	if err != nil || !strings.Contains(string(res.Structured), `"items"`) {
		t.Errorf("echo_list = %s, %v", res.Structured, err)
	}
	if st := state(h, "echo"); st.Title != "Echo" || st.Version != "0.1.0" {
		t.Errorf("status = %+v", st)
	}
	if !strings.Contains(h.Instructions(), "echo_say") {
		t.Errorf("instructions = %q", h.Instructions())
	}
}

func TestNewValidation(t *testing.T) {
	tests := []struct {
		name  string
		specs []Spec
	}{
		{"bad id", []Spec{{ID: "Bad", Command: []string{"x"}}}},
		{"short id", []Spec{{ID: "a", Command: []string{"x"}}}},
		{"duplicate id", []Spec{{ID: "tv", Command: []string{"x"}}, {ID: "tv", Command: []string{"y"}}}},
		{"empty command", []Spec{{ID: "tv"}}},
		{"empty binary", []Spec{{ID: "tv", Command: []string{""}}}},
	}
	for _, tt := range tests {
		if _, err := New(tt.specs, t.TempDir(), nil); err == nil {
			t.Errorf("%s: New succeeded", tt.name)
		}
	}
	h, err := New([]Spec{{ID: "tv", Command: []string{"bin/homie-tv"}}}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(h.plugins[0].command[0]) {
		t.Errorf("relative command not made absolute: %q", h.plugins[0].command[0])
	}
	if st := h.Status(); len(st) != 1 || st[0].State != hub.PluginStopped || st[0].Title != "tv" {
		t.Errorf("status before Start = %+v", st)
	}
}

func countPrefix(lines []string, prefix string) int {
	n := 0
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func TestToolI18n(t *testing.T) {
	h, _ := newHost(t, fastTiming, fake())
	waitUp(t, h, "fake")
	tools := h.Tools(hub.AudienceAdmin)
	want := map[string]hub.ToolText{"ru": {Title: "Эхо", Description: "Повторяет текст.", Properties: map[string]string{"text": "Текст"}}}
	if got := toolByName(tools, "fake_echo").I18n; !reflect.DeepEqual(got, want) {
		t.Errorf("fake_echo I18n = %#v, want %#v", got, want)
	}
	if got := toolByName(tools, "fake_log").I18n; got != nil {
		t.Errorf("fake_log I18n = %#v, want nil", got)
	}
	// Assistants get the same tool; the English texts stay as they are.
	if echo := toolByName(h.Tools(hub.AudienceAssistant), "fake_echo"); echo.Description != "Echoes text." || echo.I18n["ru"].Title != "Эхо" {
		t.Errorf("assistant fake_echo = %+v", echo)
	}
}

func TestCallProgress(t *testing.T) {
	for _, args := range [][]string{nil, {"--legacy"}} {
		t.Run(fmt.Sprint("args ", args), func(t *testing.T) {
			h, _ := newHost(t, fastTiming, fake(args...))
			waitUp(t, h, "fake")

			reports := make(chan Progress, 10)
			ctx := WithProgress(t.Context(), func(p Progress) { reports <- p })
			type outcome struct {
				res hub.Result
				err error
			}
			done := make(chan outcome, 1)
			go func() {
				res, err := h.Call(ctx, roman, "fake_progress", nil)
				done <- outcome{res, err}
			}()
			first, second := <-reports, <-reports
			if _, err := h.Call(t.Context(), roman, "fake_release", nil); err != nil {
				t.Fatal(err)
			}
			if o := <-done; o.err != nil || o.res.Text != "released" {
				t.Fatalf("fake_progress = %+v, %v", o.res, o.err)
			}
			wantFirst := Progress{Progress: 1, Total: 2, Message: "Press OK", I18n: map[string]string{"ru": "Нажмите OK"}}
			if !reflect.DeepEqual(first, wantFirst) {
				t.Errorf("first report = %+v, want %+v", first, wantFirst)
			}
			if want := (Progress{Progress: 2, Total: 2, Message: "Almost done"}); !reflect.DeepEqual(second, want) {
				t.Errorf("second report = %+v, want %+v", second, want)
			}

			// Without WithProgress the plugin gets no token and sends nothing.
			if res, err := h.Call(t.Context(), roman, "fake_progress", nil); err != nil || res.Text != "no token" {
				t.Fatalf("fake_progress without progress = %+v, %v", res, err)
			}
			h.mu.Lock()
			left := len(h.progress)
			h.mu.Unlock()
			if left != 0 {
				t.Errorf("%d progress sinks left after the calls", left)
			}
		})
	}
}
