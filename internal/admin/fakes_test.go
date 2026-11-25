package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/devXpro/mcp-gatekeeper/core"
	"github.com/devXpro/mcp-gatekeeper/httpsec"
	"github.com/devXpro/mcp-gatekeeper/tunnel/cloudflare"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/plugins"
	"github.com/devXpro/homie/internal/store"
	"github.com/devXpro/homie/internal/tunnel"
)

// fakeHost is a PluginHost. Calls to a tool block on its gate channel when one is set; the "mode" argument steers
// the outcome otherwise.
type fakeHost struct {
	mu      sync.Mutex
	tools   []hub.Tool
	status  []hub.PluginStatus
	logs    map[string][]string
	changed chan struct{}
	gates   map[string]chan hub.Result
	calls   []fakeCall
}

type fakeCall struct {
	Caller hub.Caller
	Name   string
	Args   string
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		tools:   testTools(),
		status:  []hub.PluginStatus{{ID: "tv", Title: "TV", Version: "0.2.0", State: hub.PluginUp, Since: time.Unix(1, 0)}},
		logs:    map[string][]string{"tv": {"one", "two"}},
		changed: make(chan struct{}),
		gates:   map[string]chan hub.Result{},
	}
}

func testTools() []hub.Tool {
	return []hub.Tool{
		{
			PluginID: "tv", Name: "tv_list", Title: "List TVs", Audience: hub.AudienceAssistant, Inventory: true,
			InputSchema: json.RawMessage(`{"type":"object"}`), Timeout: 30 * time.Second,
		},
		{
			PluginID: "tv", Name: "tv_pair", Title: "Pair a TV", Audience: hub.AudienceAdmin, Timeout: 180 * time.Second,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"tv":{"type":"string","x-homie-ref":"inventory"}},"required":["tv"]}`),
			I18n:        map[string]hub.ToolText{"ru": {Title: "Подключить телевизор", Properties: map[string]string{"tv": "Телевизор"}}},
		},
		{
			PluginID: "tv", Name: "tv_volume", Title: "Set volume", Audience: hub.AudienceAssistant, Timeout: 30 * time.Second,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"tv":{"type":"string"},` +
				`"percent":{"type":"integer","minimum":0,"maximum":100},"mode":{"type":"string","enum":["quiet","loud"]}},` +
				`"required":["tv","percent"],"additionalProperties":false}`),
		},
	}
}

func (f *fakeHost) Tools(audience hub.Audience) []hub.Tool {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []hub.Tool
	for _, t := range f.tools {
		if audience == hub.AudienceAdmin || t.Audience == audience {
			out = append(out, t)
		}
	}
	return out
}

func (f *fakeHost) Changed() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.changed
}

func (f *fakeHost) setTools(tools []hub.Tool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tools = tools
	close(f.changed)
	f.changed = make(chan struct{})
}

func (f *fakeHost) Status() []hub.PluginStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.status)
}

func (f *fakeHost) setState(id string, state hub.PluginState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.status {
		if f.status[i].ID == id {
			f.status[i].State = state
		}
	}
}

func (f *fakeHost) Logs(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.logs[id])
}

func (f *fakeHost) addLog(id string, lines ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs[id] = append(f.logs[id], lines...)
}

// gate makes the next calls of a tool block until a result is sent on the returned channel.
func (f *fakeHost) gate(name string) chan hub.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan hub.Result)
	f.gates[name] = ch
	return ch
}

func (f *fakeHost) Call(ctx context.Context, caller hub.Caller, name string, args json.RawMessage) (hub.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{Caller: caller, Name: name, Args: string(args)})
	gate := f.gates[name]
	f.mu.Unlock()
	if fn, ok := ctx.Value(progressKey{}).(func(plugins.Progress)); ok {
		fn(plugins.Progress{Progress: 1, Total: 2, Message: "Press OK on the TV remote",
			I18n: map[string]string{"ru": "Нажмите OK на пульте телевизора"}})
	}
	if gate != nil {
		select {
		case res := <-gate:
			return res, nil
		case <-ctx.Done():
			return hub.Result{}, ctx.Err()
		}
	}
	if name == "tv_list" {
		return hub.Result{Structured: json.RawMessage(
			`{"items":[{"id":"tv","name":"Living room TV","state":"on"},{"id":"x","name":"Odd","state":"weird"}]}`)}, nil
	}
	var a struct{ Mode string }
	_ = json.Unmarshal(args, &a)
	if a.Mode == "loud" {
		return hub.Result{Text: "Too loud for the neighbours.", IsError: true}, nil
	}
	return hub.Result{Text: "Done."}, nil
}

func (f *fakeHost) callCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Name == name {
			n++
		}
	}
	return n
}

func (f *fakeHost) lastCall() fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

type progressKey struct{}

func testWithProgress(ctx context.Context, fn func(plugins.Progress)) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

// fakeInstaller is an Installer; Install, and Update with blockUpdates set, block until release receives.
type fakeInstaller struct {
	release chan struct{}
	st      *store.Store

	mu           sync.Mutex
	blockUpdates bool
	downgrades   []bool // allowDowngrade of every Update and UpdateHub, in order
	homepage     string // the manifest homepage Preview reports
}

func (i *fakeInstaller) allowed() []bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return slices.Clone(i.downgrades)
}

func (i *fakeInstaller) Preview(_ context.Context, repo, _ string) (PluginPreview, error) {
	if repo != "devXpro/homie-tv" {
		return PluginPreview{}, ErrNotFound
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return PluginPreview{Repo: repo, ID: "tv", Title: "TV", Version: "0.3.0", Homepage: i.homepage, Compatible: true, AssetFound: true}, nil
}

func (i *fakeInstaller) Install(ctx context.Context, repo, version, id string, report Reporter) (string, error) {
	report(JobReport{Step: "download", Done: 10, Total: 100, Message: "homie-tv " + version})
	select {
	case <-i.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if repo == "devXpro/broken" {
		return "", &JobError{Code: "checksum_mismatch", Message: "checksums.txt does not match"}
	}
	if id == "" {
		id = "kettle"
	}
	return id, i.st.Plugins().Create(ctx, store.Plugin{ID: id, Source: store.PluginGitHub, Repo: repo, Version: version, Enabled: true})
}

func (i *fakeInstaller) Update(ctx context.Context, _, _ string, allowDowngrade bool, _ Reporter) error {
	i.mu.Lock()
	i.downgrades = append(i.downgrades, allowDowngrade)
	block := i.blockUpdates
	i.mu.Unlock()
	if block {
		select {
		case <-i.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (i *fakeInstaller) Remove(context.Context, string, bool, Reporter) error { return nil }
func (i *fakeInstaller) LatestVersion(context.Context, string) (string, error) {
	return "0.3.0", nil
}
func (i *fakeInstaller) LatestHub(context.Context) (HubRelease, error) {
	return HubRelease{Version: "0.3.0", NotesURL: "https://example.invalid/notes"}, nil
}

func (i *fakeInstaller) UpdateHub(_ context.Context, _ string, allowDowngrade bool, report Reporter) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.downgrades = append(i.downgrades, allowDowngrade)
	report(JobReport{Step: "replace", Message: "0.3.0"})
	return nil
}

// fakeTunnel is homed's tunnel as the admin sees it.
type fakeTunnel struct {
	mu       sync.Mutex
	st       tunnel.Status
	checks   int
	restarts chan struct{}
}

func (f *fakeTunnel) Status() tunnel.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *fakeTunnel) Check(context.Context) tunnel.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return f.st
}

func (f *fakeTunnel) Restart() error {
	f.restarts <- struct{}{}
	return nil
}

func (f *fakeTunnel) set(st tunnel.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st = st
}

func connectedTunnel() tunnel.Status {
	return tunnel.Status{Mode: tunnel.ModeNamed, State: string(cloudflare.StateConnected), URL: "https://home.example.com",
		Hostname: "home.example.com", Since: time.Unix(1, 0)}
}

// fakeSessions is core.Sessions over a map of cookie tokens: the CSRF header on writes, then the session, then
// step-up, with the gatekeeper's answers.
type fakeSessions struct {
	mu      sync.Mutex
	viewers map[string]core.Viewer
}

const cookieName = "__Host-homie_session"

func (f *fakeSessions) add(token string, v core.Viewer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.viewers[token] = v
}

func (f *fakeSessions) drop(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.viewers, token)
}

func (f *fakeSessions) stepUp(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.viewers[token]
	v.Session.StepUpUntil = time.Now().Add(5 * time.Minute)
	f.viewers[token] = v
}

func (f *fakeSessions) Viewer(r *http.Request) (core.Viewer, error) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return core.Viewer{}, core.ErrNoSession
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.viewers[c.Value]
	if !ok {
		return core.Viewer{}, core.ErrNoSession
	}
	return v, nil
}

func (f *fakeSessions) RequireViewer(next http.Handler) http.Handler {
	return f.SameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, err := f.Viewer(r)
		if errors.Is(err, core.ErrNoSession) {
			httpsec.WriteError(w, http.StatusUnauthorized, httpsec.CodeNoSession, "sign in first")
			return
		}
		next.ServeHTTP(w, r.WithContext(core.WithViewer(r.Context(), v)))
	}))
}

func (f *fakeSessions) RequireStepUp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, _ := core.ViewerFrom(r.Context())
		if !time.Now().Before(v.Session.StepUpUntil) {
			httpsec.WriteError(w, http.StatusForbidden, httpsec.CodeStepUp, "confirm with your passkey first")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (f *fakeSessions) SameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Homie") != "1" {
			httpsec.WriteError(w, http.StatusForbidden, httpsec.CodeForbidden, "cross-site request refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// waitUntil polls cond until it holds or the test deadline nears.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
