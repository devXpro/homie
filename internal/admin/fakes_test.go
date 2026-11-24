package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/keys"
	"github.com/devXpro/homie/internal/plugins"
	"github.com/devXpro/homie/internal/relay"
	"github.com/devXpro/homie/internal/store"
)

// memStore is an in-memory store.Store with the methods the admin uses. The embedded nil interfaces make any other
// method panic, which a test would notice at once.
type memStore struct {
	mu       sync.Mutex
	people   map[string]store.Person
	passkeys map[string]store.Passkey
	sessions map[string]store.Session
	keys     map[string]store.Key
	plugins  map[string]store.Plugin
	audit    []store.AuditRecord
	settings store.HomeSettings
	appended chan struct{} // receives after every Append
}

func newMemStore() *memStore {
	return &memStore{
		people: map[string]store.Person{}, passkeys: map[string]store.Passkey{}, sessions: map[string]store.Session{},
		keys: map[string]store.Key{}, plugins: map[string]store.Plugin{},
		settings: store.HomeSettings{HomeName: "Мой шалаш", Language: "ru"},
		appended: make(chan struct{}, 100),
	}
}

func (m *memStore) People() store.People           { return memPeople{m: m} }
func (m *memStore) Passkeys() store.Passkeys       { return memPasskeys{m: m} }
func (m *memStore) Sessions() store.Sessions       { return memSessions{m: m} }
func (m *memStore) Keys() store.Keys               { return memKeys{m: m} }
func (m *memStore) Invites() store.Invites         { return nil }
func (m *memStore) Connections() store.Connections { return nil }
func (m *memStore) Plugins() store.Plugins         { return memPlugins{m: m} }
func (m *memStore) Audit() store.Audit             { return memAudit{m: m} }
func (m *memStore) Settings() store.Settings       { return memSettings{m: m} }
func (m *memStore) Close() error                   { return nil }
func (m *memStore) InTx(_ context.Context, fn func(tx store.Store) error) error {
	return fn(m)
}

func sortedValues[V any](mp map[string]V, cmp func(a, b V) int) []V {
	out := make([]V, 0, len(mp))
	for _, v := range mp {
		out = append(out, v)
	}
	slices.SortFunc(out, cmp)
	return out
}

type memPeople struct {
	store.People
	m *memStore
}

func (r memPeople) Get(_ context.Context, id string) (store.Person, error) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	p, ok := r.m.people[id]
	if !ok {
		return store.Person{}, store.ErrNotFound
	}
	return p, nil
}

func (r memPeople) List(context.Context) ([]store.Person, error) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	return sortedValues(r.m.people, func(a, b store.Person) int { return strings.Compare(a.ID, b.ID) }), nil
}

func (r memPeople) Update(_ context.Context, p store.Person) error {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	if _, ok := r.m.people[p.ID]; !ok {
		return store.ErrNotFound
	}
	r.m.people[p.ID] = p
	return nil
}

type memPasskeys struct {
	store.Passkeys
	m *memStore
}

func (r memPasskeys) Get(_ context.Context, id string) (store.Passkey, error) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	pk, ok := r.m.passkeys[id]
	if !ok {
		return store.Passkey{}, store.ErrNotFound
	}
	return pk, nil
}

func (r memPasskeys) ListByPerson(_ context.Context, personID string) ([]store.Passkey, error) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	var out []store.Passkey
	for _, pk := range sortedValues(r.m.passkeys, func(a, b store.Passkey) int { return strings.Compare(a.ID, b.ID) }) {
		if pk.PersonID == personID {
			out = append(out, pk)
		}
	}
	return out, nil
}

func (r memPasskeys) Rename(_ context.Context, id, name string) error {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	pk, ok := r.m.passkeys[id]
	if !ok {
		return store.ErrNotFound
	}
	pk.Name = name
	r.m.passkeys[id] = pk
	return nil
}

func (r memPasskeys) Delete(_ context.Context, id string) error {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	pk, ok := r.m.passkeys[id]
	if !ok {
		return store.ErrNotFound
	}
	n := 0
	for _, other := range r.m.passkeys {
		if other.PersonID == pk.PersonID {
			n++
		}
	}
	if n == 1 {
		return store.ErrLastPasskey
	}
	delete(r.m.passkeys, id)
	return nil
}

type memSessions struct {
	store.Sessions
	m *memStore
}

func (r memSessions) ListByPerson(_ context.Context, personID string) ([]store.Session, error) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	var out []store.Session
	for _, s := range sortedValues(r.m.sessions, func(a, b store.Session) int { return strings.Compare(a.ID, b.ID) }) {
		if s.PersonID == personID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r memSessions) Delete(_ context.Context, id string) error {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	if _, ok := r.m.sessions[id]; !ok {
		return store.ErrNotFound
	}
	delete(r.m.sessions, id)
	return nil
}

func (r memSessions) DeleteByPerson(_ context.Context, personID, keep string) (int, error) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	n := 0
	for id, s := range r.m.sessions {
		if s.PersonID == personID && id != keep {
			delete(r.m.sessions, id)
			n++
		}
	}
	return n, nil
}

type memKeys struct {
	store.Keys
	m *memStore
}

func (r memKeys) Get(_ context.Context, id string) (store.Key, error) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	k, ok := r.m.keys[id]
	if !ok {
		return store.Key{}, store.ErrNotFound
	}
	return k, nil
}

func (r memKeys) List(context.Context) ([]store.Key, error) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	return sortedValues(r.m.keys, func(a, b store.Key) int { return strings.Compare(a.ID, b.ID) }), nil
}

func (r memKeys) ListByPerson(ctx context.Context, personID string) ([]store.Key, error) {
	all, _ := r.List(ctx)
	return slices.DeleteFunc(all, func(k store.Key) bool { return k.PersonID != personID }), nil
}

func (r memKeys) Create(_ context.Context, k store.Key) error {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	if _, ok := r.m.keys[k.ID]; ok {
		return store.ErrConflict
	}
	r.m.keys[k.ID] = k
	return nil
}

func (r memKeys) Delete(_ context.Context, id string) error {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	if _, ok := r.m.keys[id]; !ok {
		return store.ErrNotFound
	}
	delete(r.m.keys, id)
	return nil
}

type memPlugins struct {
	store.Plugins
	m *memStore
}

func (r memPlugins) Get(_ context.Context, id string) (store.Plugin, error) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	p, ok := r.m.plugins[id]
	if !ok {
		return store.Plugin{}, store.ErrNotFound
	}
	return p, nil
}

func (r memPlugins) List(context.Context) ([]store.Plugin, error) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	return sortedValues(r.m.plugins, func(a, b store.Plugin) int { return strings.Compare(a.ID, b.ID) }), nil
}

func (r memPlugins) Create(_ context.Context, p store.Plugin) error {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	if _, ok := r.m.plugins[p.ID]; ok {
		return store.ErrConflict
	}
	r.m.plugins[p.ID] = p
	return nil
}

func (r memPlugins) Update(_ context.Context, p store.Plugin) error {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	if _, ok := r.m.plugins[p.ID]; !ok {
		return store.ErrNotFound
	}
	r.m.plugins[p.ID] = p
	return nil
}

type memAudit struct {
	store.Audit
	m *memStore
}

func (r memAudit) Append(_ context.Context, recs []store.AuditRecord) ([]store.AuditRecord, error) {
	r.m.mu.Lock()
	out := make([]store.AuditRecord, 0, len(recs))
	for _, rec := range recs {
		rec.ID = int64(len(r.m.audit) + 1)
		r.m.audit = append(r.m.audit, rec)
		out = append(out, rec)
	}
	r.m.mu.Unlock()
	select {
	case r.m.appended <- struct{}{}:
	default:
	}
	return out, nil
}

func (r memAudit) Query(_ context.Context, f store.AuditFilter) ([]store.AuditRecord, error) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	limit := f.Limit
	if limit == 0 {
		limit = 50
	}
	var out []store.AuditRecord
	for i := len(r.m.audit) - 1; i >= 0 && len(out) < limit; i-- {
		rec := r.m.audit[i]
		switch {
		case f.BeforeID > 0 && rec.ID >= f.BeforeID,
			f.Kind != "" && rec.Kind != f.Kind,
			f.PersonID != "" && rec.PersonID != f.PersonID,
			f.PluginID != "" && rec.PluginID != f.PluginID,
			f.Tool != "" && rec.Tool != f.Tool,
			len(f.Via) > 0 && !slices.Contains(f.Via, rec.Via),
			f.Status == store.AuditOK && !rec.OK,
			f.Status == store.AuditFailed && rec.OK:
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

func (r memAudit) Get(_ context.Context, id int64) (store.AuditRecord, error) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	if id < 1 || id > int64(len(r.m.audit)) {
		return store.AuditRecord{}, store.ErrNotFound
	}
	return r.m.audit[id-1], nil
}

func (m *memStore) auditRecords() []store.AuditRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.audit)
}

type memSettings struct {
	store.Settings
	m *memStore
}

func (r memSettings) Get(context.Context) (store.HomeSettings, error) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	return r.m.settings, nil
}

func (r memSettings) Put(_ context.Context, s store.HomeSettings) error {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	r.m.settings = s
	return nil
}

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

type progressKey struct{}

func testWithProgress(ctx context.Context, fn func(plugins.Progress)) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

// fakeAuth maps cookie values to principals; a session deleted from the store stops authenticating.
type fakeAuth struct {
	mu    sync.Mutex
	st    *memStore
	byTok map[string]Principal
}

func (a *fakeAuth) Authenticate(r *http.Request) (Principal, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	a.mu.Lock()
	p, ok := a.byTok[c.Value]
	a.mu.Unlock()
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	a.st.mu.Lock()
	_, alive := a.st.sessions[p.Session.ID]
	a.st.mu.Unlock()
	if !alive {
		return Principal{}, ErrUnauthenticated
	}
	return p, nil
}

func (a *fakeAuth) setStepUp(token string, until time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.byTok[token]
	p.StepUpUntil = until
	a.byTok[token] = p
}

// fakeRelay is a Relay.
type fakeRelay struct {
	mu        sync.Mutex
	domain    string
	status    relay.Status
	check     SelfCheck
	connected []relay.Code
}

func (r *fakeRelay) Domain() string { r.mu.Lock(); defer r.mu.Unlock(); return r.domain }
func (r *fakeRelay) Status() relay.Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}
func (r *fakeRelay) LastCheck() SelfCheck { r.mu.Lock(); defer r.mu.Unlock(); return r.check }
func (r *fakeRelay) CheckNow(context.Context) SelfCheck {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.check = SelfCheck{OK: false, CheckedAt: time.Unix(100, 0), Error: "instance header missing"}
	return r.check
}
func (r *fakeRelay) Connect(_ context.Context, c relay.Code) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.connected = append(r.connected, c)
	r.domain = c.Domain
	return nil
}

// fakeInstaller is an Installer; Install, and Update with blockUpdates set, block until release is closed.
type fakeInstaller struct {
	release chan struct{}
	st      *memStore

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

func (i *fakeInstaller) UpdateHub(_ context.Context, _ string, allowDowngrade bool, _ Reporter) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.downgrades = append(i.downgrades, allowDowngrade)
	return nil
}

// fakeKeys records resolver rebuilds: like cmd/homed's resolver, it reads every key with its person from the store.
type fakeKeys struct {
	st      store.Store
	mu      sync.Mutex
	entries []keys.Entry
	updates int
}

func (k *fakeKeys) Rebuild(ctx context.Context) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	people, err := k.st.People().List(ctx)
	if err != nil {
		return err
	}
	byID := map[string]hub.Person{}
	for _, p := range people {
		byID[p.ID] = hub.Person{ID: p.ID, Name: p.Name, Plugins: p.Plugins}
	}
	list, err := k.st.Keys().List(ctx)
	if err != nil {
		return err
	}
	k.entries = nil
	for _, key := range list {
		if p, ok := byID[key.PersonID]; ok {
			k.entries = append(k.entries, keys.Entry{Person: p, SHA256: key.SHA256})
		}
	}
	k.updates++
	return nil
}

func (k *fakeKeys) snapshot() ([]keys.Entry, int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.entries), k.updates
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

func (r *fakeRelay) setStatus(s relay.Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = s
}
