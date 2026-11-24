package plugins

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/pluginkit"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLogRing(t *testing.T) {
	s := newLogSink(filepath.Join(t.TempDir(), "x.log"), slog.New(slog.DiscardHandler))
	defer s.close()
	for i := range logRingLines + 5 {
		s.add(fmt.Sprint("line ", i))
	}
	lines := s.lines()
	if len(lines) != logRingLines || lines[0] != "line 5" || lines[len(lines)-1] != fmt.Sprint("line ", logRingLines+4) {
		t.Fatalf("ring: %d lines, first %q, last %q", len(lines), lines[0], lines[len(lines)-1])
	}
}

func TestLogRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	s := newLogSink(path, slog.New(slog.DiscardHandler))
	s.maxSize = 100
	for i := range 40 {
		s.add(fmt.Sprintf("line %02d ......", i)) // 16 bytes with the newline
	}
	s.close()
	for _, p := range []string{path, path + ".1", path + ".2"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size() > 100 || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: size %d, mode %v", p, fi.Size(), fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("%s.3 exists: %v", path, err)
	}
	b, _ := os.ReadFile(path) //nolint:gosec // a path inside t.TempDir
	if !strings.HasSuffix(string(b), "line 39 ......\n") {
		t.Errorf("current file ends with %q", b)
	}
}

func TestLogCopyTruncatesLongLines(t *testing.T) {
	s := newLogSink(filepath.Join(t.TempDir(), "x.log"), slog.New(slog.DiscardHandler))
	defer s.close()
	long := strings.Repeat("a", 3*logMaxLine)
	s.copy(strings.NewReader("first\r\n" + long + "\nafter\nno newline at end"))
	lines := s.lines()
	if len(lines) != 4 || lines[0] != "first" || lines[2] != "after" || lines[3] != "no newline at end" {
		t.Fatalf("lines = %d: %.40q", len(lines), lines)
	}
	if !strings.HasSuffix(lines[1], "[truncated]") || len(lines[1]) > logMaxLine+20 {
		t.Errorf("long line kept %d bytes", len(lines[1]))
	}
}

func TestConvertTool(t *testing.T) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	tests := []struct {
		name      string
		tool      *mcp.Tool
		skip      bool
		audience  hub.Audience
		timeout   time.Duration
		inventory bool
		warnings  int
	}{
		{name: "plain", tool: &mcp.Tool{Name: "tv_on"}, audience: hub.AudienceAssistant, timeout: 30 * time.Second},
		{name: "bad name", tool: &mcp.Tool{Name: "kettle_on"}, skip: true},
		// Schemas the go-sdk's AddTool panics on: skipped here, so the gateway never sees them.
		{name: "input schema not an object", tool: &mcp.Tool{Name: "tv_on", InputSchema: map[string]any{"type": "string"}}, skip: true},
		{name: "x-mcp-header on an object", tool: &mcp.Tool{Name: "tv_on", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{"o": map[string]any{"type": "object", "x-mcp-header": "X-O"}},
		}}, skip: true},
		{name: "x-mcp-header not a header name", tool: &mcp.Tool{Name: "tv_on", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{"o": map[string]any{"type": "string", "x-mcp-header": "a b"}},
		}}, skip: true},
		{name: "output schema not marshalable", tool: &mcp.Tool{Name: "tv_on", OutputSchema: func() {}}, skip: true},
		{name: "admin", tool: pluginkit.Admin(&mcp.Tool{Name: "tv_pair"}), audience: hub.AudienceAdmin, timeout: 30 * time.Second},
		{name: "explicit assistant", tool: &mcp.Tool{Name: "tv_on", Meta: mcp.Meta{pluginkit.MetaAudience: "assistant"}},
			audience: hub.AudienceAssistant, timeout: 30 * time.Second},
		{name: "unknown audience", tool: &mcp.Tool{Name: "tv_on", Meta: mcp.Meta{pluginkit.MetaAudience: 1.0}},
			audience: hub.AudienceAdmin, timeout: 30 * time.Second, warnings: 1},
		{name: "timeout", tool: &mcp.Tool{Name: "tv_pair", Meta: mcp.Meta{pluginkit.MetaTimeout: 180.0}},
			audience: hub.AudienceAssistant, timeout: 180 * time.Second},
		{name: "fractional timeout", tool: &mcp.Tool{Name: "tv_x", Meta: mcp.Meta{pluginkit.MetaTimeout: 1.5}},
			audience: hub.AudienceAssistant, timeout: 1500 * time.Millisecond},
		{name: "huge timeout", tool: &mcp.Tool{Name: "tv_x", Meta: mcp.Meta{pluginkit.MetaTimeout: 1e300}},
			audience: hub.AudienceAssistant, timeout: 300 * time.Second, warnings: 1},
		{name: "zero timeout", tool: &mcp.Tool{Name: "tv_x", Meta: mcp.Meta{pluginkit.MetaTimeout: 0.0}},
			audience: hub.AudienceAssistant, timeout: 30 * time.Second, warnings: 1},
		{name: "string timeout", tool: &mcp.Tool{Name: "tv_x", Meta: mcp.Meta{pluginkit.MetaTimeout: "10"}},
			audience: hub.AudienceAssistant, timeout: 30 * time.Second, warnings: 1},
		{name: "inventory", tool: pluginkit.Inventory(&mcp.Tool{Name: "tv_list", Annotations: ro}),
			audience: hub.AudienceAssistant, timeout: 30 * time.Second, inventory: true},
		{name: "inventory not read-only", tool: pluginkit.Inventory(&mcp.Tool{Name: "tv_list"}),
			audience: hub.AudienceAssistant, timeout: 30 * time.Second, warnings: 1},
		{name: "unknown role", tool: &mcp.Tool{Name: "tv_list", Annotations: ro, Meta: mcp.Meta{pluginkit.MetaRole: "x"}},
			audience: hub.AudienceAssistant, timeout: 30 * time.Second, warnings: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warnings, err := convertTool("tv", tt.tool)
			if tt.skip {
				if err == nil {
					t.Fatal("not skipped")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Audience != tt.audience || got.Timeout != tt.timeout || got.Inventory != tt.inventory || len(warnings) != tt.warnings {
				t.Errorf("got audience %q timeout %v inventory %v warnings %q", got.Audience, got.Timeout, got.Inventory, warnings)
			}
			if string(got.InputSchema) != `{"type":"object"}` || got.PluginID != "tv" {
				t.Errorf("schema %s plugin %q", got.InputSchema, got.PluginID)
			}
		})
	}
}

func TestParseI18n(t *testing.T) {
	tests := []struct {
		name     string
		in       any
		want     map[string]hub.ToolText
		warnings int
	}{
		{name: "full", in: map[string]any{
			"ru":    map[string]any{"title": "Громкость", "description": "Задаёт громкость.", "properties": map[string]any{"percent": "Громкость, %"}},
			"pt-BR": map[string]any{"title": "Volume"},
		}, want: map[string]hub.ToolText{
			"ru":    {Title: "Громкость", Description: "Задаёт громкость.", Properties: map[string]string{"percent": "Громкость, %"}},
			"pt-BR": {Title: "Volume"},
		}},
		{name: "not an object", in: "ru", warnings: 1},
		{name: "bad tags and entries", in: map[string]any{
			"Russian": map[string]any{"title": "x"},
			"":        map[string]any{"title": "x"},
			"ru":      "Громкость",
			"uk":      map[string]any{"title": "Гучність"},
		}, want: map[string]hub.ToolText{"uk": {Title: "Гучність"}}, warnings: 3},
		{name: "bad fields kept partly", in: map[string]any{
			"ru": map[string]any{"title": 5, "description": "Описание", "properties": map[string]any{"tv": "Телевизор", "n": 1}, "extra": "x"},
		}, want: map[string]hub.ToolText{"ru": {Description: "Описание", Properties: map[string]string{"tv": "Телевизор"}}}, warnings: 3},
		{name: "bad properties", in: map[string]any{"ru": map[string]any{"title": "Т", "properties": []any{"tv"}}},
			want: map[string]hub.ToolText{"ru": {Title: "Т"}}, warnings: 1},
		{name: "empty entry dropped", in: map[string]any{"ru": map[string]any{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warnings := parseI18n(tt.in)
			if !reflect.DeepEqual(got, tt.want) || len(warnings) != tt.warnings {
				t.Errorf("parseI18n = %#v, warnings %q\nwant %#v with %d warnings", got, warnings, tt.want, tt.warnings)
			}
		})
	}
	tool, warnings, err := convertTool("tv", pluginkit.I18n(&mcp.Tool{Name: "tv_on"}, "ru", pluginkit.Text{Title: "Включить"}))
	if err != nil || len(warnings) != 0 || tool.I18n["ru"].Title != "Включить" {
		t.Errorf("convertTool with i18n = %+v, %q, %v", tool.I18n, warnings, err)
	}
}

func TestDeliverProgress(t *testing.T) {
	h, err := New(nil, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []Progress
	token, stop := h.watchProgress("tv", func(p Progress) { got = append(got, p) })
	other, stopOther := h.watchProgress("tv", func(Progress) { t.Error("another call's callback ran") })
	defer stopOther()
	if token == other {
		t.Fatalf("two calls share the token %q", token)
	}

	h.deliverProgress("tv", &mcp.ProgressNotificationParams{ProgressToken: token, Progress: 1, Message: "Searching",
		Meta: mcp.Meta{pluginkit.MetaI18n: map[string]any{"ru": "Ищу", "uk": 5, "bad tag": "x", "de": ""}}})
	h.deliverProgress("kettle", &mcp.ProgressNotificationParams{ProgressToken: token, Progress: 9}) // another plugin
	h.deliverProgress("tv", &mcp.ProgressNotificationParams{ProgressToken: "nope", Progress: 9})
	h.deliverProgress("tv", &mcp.ProgressNotificationParams{ProgressToken: 1.0, Progress: 9}) // not our token type
	h.deliverProgress("tv", nil)
	h.deliverProgress("tv", &mcp.ProgressNotificationParams{ProgressToken: token, Progress: 2, Total: 4,
		Meta: mcp.Meta{pluginkit.MetaI18n: "not an object"}})
	stop()
	h.deliverProgress("tv", &mcp.ProgressNotificationParams{ProgressToken: token, Progress: 3}) // after the call ended

	want := []Progress{
		{Progress: 1, Message: "Searching", I18n: map[string]string{"ru": "Ищу"}},
		{Progress: 2, Total: 4},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reports = %+v\nwant %+v", got, want)
	}
	if fn := progressFunc(t.Context()); fn != nil {
		t.Error("a plain context carries a progress callback")
	}
}
