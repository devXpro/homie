package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/devXpro/homie/internal/hub"
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

func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test file in t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	s := bufio.NewScanner(f)
	for s.Scan() {
		var m map[string]any
		if err := json.Unmarshal(s.Bytes(), &m); err != nil {
			t.Fatalf("bad line %q: %v", s.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func TestLogWritesMonthlyFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	l, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	oct := time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC)
	nov := time.Date(2026, 11, 15, 12, 0, 0, 0, time.FixedZone("CET", 3600))
	l.Record(context.Background(), hub.AuditEntry{
		At: oct, Caller: hub.Caller{Person: "Roman", Via: "site"}, Tool: "tv_power_on",
		Args: json.RawMessage(`{"tv":"living"}`), OK: true, Duration: 1500 * time.Millisecond,
	})
	l.Record(context.Background(), hub.AuditEntry{
		At: nov, Caller: hub.Caller{Person: "Tanya", Via: "mcp"}, Tool: "tv_pair",
		Args: json.RawMessage(`{"pin":"123456"}`), Error: "plugin is not running",
	})
	l.Record(context.Background(), hub.AuditEntry{At: nov, Caller: hub.Caller{Person: "Roman", Via: "cli"}, Tool: "tv_list", OK: true})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	l.Record(context.Background(), hub.AuditEntry{At: nov, Tool: "after_close"})
	if l.Dropped() != 1 {
		t.Fatalf("Dropped = %d after a Record on a closed log", l.Dropped())
	}

	info, err := os.Stat(filepath.Join(dir, "audit-2026-10.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", info.Mode().Perm())
	}
	octLines := readLines(t, filepath.Join(dir, "audit-2026-10.jsonl"))
	if len(octLines) != 1 {
		t.Fatalf("october: %d lines", len(octLines))
	}
	want := map[string]any{
		"at": "2026-10-31T23:59:00Z", "person": "Roman", "via": "site", "tool": "tv_power_on",
		"args": map[string]any{"tv": "living"}, "ok": true, "duration_ms": float64(1500),
	}
	if got, _ := json.Marshal(octLines[0]); string(got) != string(must(json.Marshal(want))) {
		t.Fatalf("october line = %s", got)
	}

	novLines := readLines(t, filepath.Join(dir, "audit-2026-11.jsonl"))
	if len(novLines) != 2 {
		t.Fatalf("november: %d lines", len(novLines))
	}
	if novLines[0]["at"] != "2026-11-15T11:00:00Z" {
		t.Fatalf("time not stored in UTC: %v", novLines[0]["at"])
	}
	if args := novLines[0]["args"].(map[string]any); args["pin"] != "[redacted]" {
		t.Fatalf("pin not redacted on disk: %v", args)
	}
	if novLines[0]["ok"] != false || novLines[0]["error"] != "plugin is not running" {
		t.Fatalf("failure fields: %v", novLines[0])
	}
	if _, ok := novLines[1]["args"]; ok {
		t.Fatalf("empty args written: %v", novLines[1])
	}
	if _, ok := novLines[1]["error"]; ok {
		t.Fatalf("empty error written: %v", novLines[1])
	}
}

func TestRecordDropsWhenFull(t *testing.T) {
	// The writer is not started, so the buffer fills up and stays full.
	l, err := newLog(t.TempDir(), 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	for range 5 {
		l.Record(context.Background(), hub.AuditEntry{At: at, Tool: "tv_power_on", OK: true})
	}
	if l.Dropped() != 3 {
		t.Fatalf("Dropped = %d, want 3", l.Dropped())
	}
	go l.run()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if n := len(readLines(t, filepath.Join(l.dir, "audit-2026-11.jsonl"))); n != 2 {
		t.Fatalf("%d lines written, want the 2 queued", n)
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}
