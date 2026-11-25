package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/store"
)

func TestRedact(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"no secrets", `{"input":"hdmi1","level":5}`, `{"input":"hdmi1","level":5}`},
		{"flat", `{"pin":"1234","name":"tv"}`, `{"name":"tv","pin":"[redacted]"}`},
		{"case and substrings", `{"API_Key":"k","authToken":"t","Password":"p","client_secret":"s","pairingCode":42}`,
			`{"API_Key":"[redacted]","Password":"[redacted]","authToken":"[redacted]","client_secret":"[redacted]","pairingCode":"[redacted]"}`},
		{"nested object value", `{"token":{"a":1}}`, `{"token":"[redacted]"}`},
		{"deep", `{"tv":{"auth":{"key":"x"},"list":[{"secret":"y","ok":true}]}}`,
			`{"tv":{"auth":{"key":"[redacted]"},"list":[{"ok":true,"secret":"[redacted]"}]}}`},
		{"array top level", `[{"code":"1"},2]`, `[{"code":"[redacted]"},2]`},
		{"big numbers kept exact", `{"n":12345678901234567890}`, `{"n":12345678901234567890}`},
		{"html kept", `{"q":"<b>"}`, `{"q":"<b>"}`},
		{"not json", `{"pin":`, `"[unreadable]"`},
		{"trailing garbage", `{} {"pin":1}`, `"[unreadable]"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(Redact(json.RawMessage(tt.in))); got != tt.want {
				t.Fatalf("Redact(%s) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "homie.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// Records are stored with the caller's id and surface, the plugin from the tool's name and the arguments redacted
// again, and OnStored hears every batch with its ids.
func TestWriterStoresAndPublishes(t *testing.T) {
	st := openStore(t)
	w := NewWriter(st.Activity(), nil)
	var (
		mu     sync.Mutex
		stored []store.ActivityRecord
	)
	w.OnStored(func(recs []store.ActivityRecord) {
		mu.Lock()
		defer mu.Unlock()
		stored = append(stored, recs...)
	})
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	w.Record(context.Background(), hub.AuditEntry{
		At: at, Caller: hub.Caller{Person: "Alice", PersonID: "per_alice", Via: "claude"}, Tool: "tv_power_on",
		Args: json.RawMessage(`{"tv":"tv","pin":"1234"}`), OK: true, Duration: 2 * time.Millisecond,
	})
	w.Record(context.Background(), hub.AuditEntry{Caller: hub.Caller{Person: "local", Via: "cli"}, Tool: "tv_pair",
		Error: "timeout"})
	w.Close()
	if w.Dropped() != 0 {
		t.Fatalf("dropped %d", w.Dropped())
	}
	recs, err := st.Activity().Query(context.Background(), store.ActivityFilter{})
	if err != nil || len(recs) != 2 {
		t.Fatalf("records = %+v, %v", recs, err)
	}
	first := recs[1]
	if first.PersonID != "per_alice" || first.Person != "Alice" || first.Via != "claude" || first.PluginID != "tv" ||
		string(first.Args) != `{"pin":"[redacted]","tv":"tv"}` || !first.At.Equal(at) || !first.OK {
		t.Errorf("first = %+v", first)
	}
	if recs[0].At.IsZero() || recs[0].OK || recs[0].Error != "timeout" {
		t.Errorf("second = %+v", recs[0])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(stored) != 2 || stored[0].ID == 0 || stored[1].ID != stored[0].ID+1 {
		t.Errorf("OnStored = %+v", stored)
	}
}

// blocking is an Activity whose Append says it was entered and waits for a release, to fill the writer's queue.
type blocking struct {
	store.Activity
	entered chan struct{}
	release chan struct{}
}

func (b blocking) Append(ctx context.Context, recs []store.ActivityRecord) ([]store.ActivityRecord, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	return b.Activity.Append(ctx, recs)
}

// Record never blocks: with the queue full it drops and counts; a closed writer drops too.
func TestWriterDropsWhenFull(t *testing.T) {
	st := openStore(t)
	b := blocking{Activity: st.Activity(), entered: make(chan struct{}, 1), release: make(chan struct{})}
	w := newWriter(b, 2, nil)
	w.Record(context.Background(), hub.AuditEntry{Tool: "tv_power_on"})
	<-b.entered // the writer holds the first record and waits in Append
	for range 10 {
		w.Record(context.Background(), hub.AuditEntry{Tool: "tv_power_on"})
	}
	if w.Dropped() != 8 { // two fit the queue
		t.Errorf("dropped %d, want 8", w.Dropped())
	}
	close(b.release)
	w.Close()
	if recs, _ := st.Activity().Query(context.Background(), store.ActivityFilter{}); len(recs) != 3 {
		t.Errorf("stored %d, want 3", len(recs))
	}
	w.Record(context.Background(), hub.AuditEntry{Tool: "tv_power_on"})
	if w.Dropped() != 9 {
		t.Error("a record after Close was not counted as dropped")
	}
}

// ErrorText is what every surface writes to the activity log for a call.
func TestErrorText(t *testing.T) {
	rpc := &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: `pin "1234" is too short`}
	tests := []struct {
		name string
		res  hub.Result
		err  error
		want string
	}{
		{"ok", hub.Result{Text: "done"}, nil, ""},
		{"tool failure keeps its text", hub.Result{Text: "TV is off.", IsError: true}, nil, "TV is off."},
		{"typed handler validation", hub.Result{Text: `validating "arguments": "1234" too short`, IsError: true}, nil, errInvalidArgs},
		{"JSON-RPC invalid params", hub.Result{}, fmt.Errorf("tv_pair: %w", rpc), errInvalidArgs},
		{"other JSON-RPC error", hub.Result{}, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "boom"}, "boom"},
		{"plugin down", hub.Result{}, hub.ErrPluginDown, "plugin is not running"},
	}
	for _, tt := range tests {
		if got := ErrorText(tt.res, tt.err); got != tt.want {
			t.Errorf("%s: ErrorText = %q, want %q", tt.name, got, tt.want)
		}
	}
}
