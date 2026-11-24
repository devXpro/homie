package pluginkit

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCheckToolName(t *testing.T) {
	tests := []struct {
		plugin, tool string
		ok           bool
	}{
		{"tv", "tv_power_on", true},
		{"tv", "tv_x", true},
		{"tv", "tv_set_input2", true},
		{"kettle", "kettle_boil", true},
		{"tv", "tv_", false},
		{"tv", "tv_Power", false},
		{"tv", "tv_2on", false},
		{"tv", "tv-power", false},
		{"tv", "tvpower", false},
		{"tv", "kettle_boil", false},
		{"tv", "tv_power-on", false},
		{"t", "t_on", false},                                 // id too short
		{"Tv", "Tv_on", false},                               // id upper case
		{"1tv", "1tv_on", false},                             // id starts with a digit
		{"abcdefghijklmnopq", "abcdefghijklmnopq_on", false}, // id longer than 16
		{"abcdefghijklmnop", "abcdefghijklmnop_on", true},    // id of 16
		{"tv_x", "tv_x_on", false},                           // underscore in id
		{"tv", "", false},
	}
	for _, tt := range tests {
		err := CheckToolName(tt.plugin, tt.tool)
		if (err == nil) != tt.ok {
			t.Errorf("CheckToolName(%q, %q) = %v, want ok=%v", tt.plugin, tt.tool, err, tt.ok)
		}
	}
}

func TestToolHelpersChain(t *testing.T) {
	tool := &mcp.Tool{Name: "tv_pair"}
	got := Admin(Timeout(Inventory(tool), 2500*time.Millisecond))
	if got != tool {
		t.Fatal("helpers must return the same tool")
	}
	want := map[string]any{MetaAudience: "admin", MetaTimeout: 3, MetaRole: "inventory"}
	for k, v := range want {
		if tool.Meta[k] != v {
			t.Errorf("Meta[%q] = %#v, want %#v", k, tool.Meta[k], v)
		}
	}
}

func TestNewPanicsOnBadID(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New with a bad id did not panic")
		}
	}()
	New(Plugin{ID: "Bad-ID", Name: "x", Version: "1"})
}

func TestDataDir(t *testing.T) {
	t.Run("from env", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "plugin", "data")
		t.Setenv(EnvDataDir, dir)
		got, err := DataDir()
		if err != nil || got != dir {
			t.Fatalf("DataDir() = %q, %v; want %q", got, err, dir)
		}
		checkDir(t, dir)
	})
	t.Run("development", func(t *testing.T) {
		t.Setenv(EnvDataDir, "")
		wd := t.TempDir()
		t.Chdir(wd)
		got, err := DataDir()
		if err != nil {
			t.Fatal(err)
		}
		// t.TempDir may sit behind a symlink (macOS /var → /private/var), so compare resolved paths.
		want, _ := filepath.EvalSymlinks(filepath.Join(wd, "data"))
		if resolved, _ := filepath.EvalSymlinks(got); resolved != want {
			t.Fatalf("DataDir() = %q, want %q", got, want)
		}
		checkDir(t, got)
	})
}

func checkDir(t *testing.T, dir string) {
	t.Helper()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("%s: mode %v, want a 0700 directory", dir, fi.Mode())
	}
}

func TestCallerNil(t *testing.T) {
	if p, v := Caller(nil); p != "" || v != "" {
		t.Fatalf("Caller(nil) = %q, %q", p, v)
	}
	if p, v := Caller(&mcp.CallToolRequest{}); p != "" || v != "" {
		t.Fatalf("Caller(empty) = %q, %q", p, v)
	}
}

// TestServerOverMCP runs a pluginkit server against a go-sdk client in memory: identity, instructions, tool _meta
// and the caller's request _meta must all survive the protocol.
func TestServerOverMCP(t *testing.T) {
	s := New(Plugin{ID: "demo", Name: "homie-demo", Title: "Demo", Version: "1.2.3", Instructions: "Be brief."})
	type in struct{}
	type out struct {
		Person string `json:"person"`
		Via    string `json:"via"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "demo_who"}, func(_ context.Context, req *mcp.CallToolRequest, _ in) (*mcp.CallToolResult, out, error) {
		p, v := Caller(req)
		return nil, out{Person: p, Via: v}, nil
	})
	mcp.AddTool(s, Admin(Timeout(&mcp.Tool{Name: "demo_pair"}, 3*time.Minute)), func(context.Context, *mcp.CallToolRequest, in) (*mcp.CallToolResult, any, error) {
		return nil, nil, nil
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	served := make(chan error, 1)
	go func() { served <- serve(ctx, s, st) }()

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()

	init := cs.InitializeResult()
	if si := init.ServerInfo; si == nil || si.Name != "homie-demo" || si.Title != "Demo" || si.Version != "1.2.3" {
		t.Fatalf("serverInfo = %+v", init.ServerInfo)
	}
	if init.Instructions != "Be brief." {
		t.Fatalf("instructions = %q", init.Instructions)
	}

	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	meta := map[string]mcp.Meta{}
	for _, tool := range list.Tools {
		meta[tool.Name] = tool.Meta
	}
	if m := meta["demo_pair"]; m[MetaAudience] != "admin" || m[MetaTimeout] != float64(180) {
		t.Fatalf("demo_pair _meta = %v", m)
	}
	if m := meta["demo_who"]; m[MetaAudience] != nil {
		t.Fatalf("demo_who _meta = %v", m)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "demo_who",
		Meta: mcp.Meta{MetaCaller: "Roman", MetaVia: "site"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := res.StructuredContent.(map[string]any)
	if got["person"] != "Roman" || got["via"] != "site" {
		t.Fatalf("structured = %v", res.StructuredContent)
	}

	cancel()
	if err := <-served; err != nil {
		t.Fatalf("serve after cancel = %v, want nil", err)
	}
}

func TestServeReturnsNilWhenInputCloses(t *testing.T) {
	s := New(Plugin{ID: "demo", Name: "homie-demo", Version: "1"})
	st, ct := mcp.NewInMemoryTransports()
	served := make(chan error, 1)
	go func() { served <- serve(t.Context(), s, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = cs.Close()
	if err := <-served; err != nil {
		t.Fatalf("serve after the client left = %v, want nil", err)
	}
}

func TestI18n(t *testing.T) {
	tool := &mcp.Tool{Name: "tv_set_volume"}
	got := I18n(Admin(tool), "ru", Text{
		Title:      "Громкость",
		Properties: map[string]string{"percent": "Громкость, %"},
	})
	if got != tool {
		t.Fatal("I18n must return the same tool")
	}
	I18n(tool, "uk", Text{Title: "Гучність", Description: "Задає гучність."})
	I18n(tool, "ru", Text{Title: "Звук", Properties: map[string]string{"tv": "Телевизор"}}) // replaces ru
	want := map[string]any{
		"ru": map[string]any{"title": "Звук", "properties": map[string]any{"tv": "Телевизор"}},
		"uk": map[string]any{"title": "Гучність", "description": "Задає гучність."},
	}
	if !reflect.DeepEqual(tool.Meta[MetaI18n], want) {
		t.Fatalf("_meta i18n = %#v\nwant %#v", tool.Meta[MetaI18n], want)
	}
	if tool.Meta[MetaAudience] != "admin" {
		t.Error("I18n dropped other _meta keys")
	}
}

func TestInventoryRef(t *testing.T) {
	prop := map[string]any{"type": "string"}
	if got := InventoryRef(prop); got["x-homie-ref"] != "inventory" || got["type"] != "string" {
		t.Fatalf("InventoryRef = %v", got)
	}
	if prop[SchemaRef] != RefInventory {
		t.Error("InventoryRef did not set the property in place")
	}
	if got := InventoryRef(nil); got[SchemaRef] != RefInventory {
		t.Fatalf("InventoryRef(nil) = %v", got)
	}
}

func TestProgressWithoutToken(t *testing.T) {
	if err := Progress(t.Context(), nil, 1, 0, "x", nil); err != nil {
		t.Fatal(err)
	}
	if err := Progress(t.Context(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}, 1, 0, "x", nil); err != nil {
		t.Fatal(err)
	}
}

// TestProgressOverMCP checks that progress reports reach a client that asked for them, with the translated message in
// the notification _meta, and that a call without a progress token sends none.
func TestProgressOverMCP(t *testing.T) {
	s := New(Plugin{ID: "demo", Name: "homie-demo", Version: "1"})
	release := make(chan struct{})
	type out struct {
		Token bool `json:"token"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "demo_pair"}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, out, error) {
		token := req.Params.GetProgressToken() != nil
		if err := Progress(ctx, req, 1, 2, "Press OK on the TV remote", map[string]string{"ru": "Нажмите OK на пульте"}); err != nil {
			return nil, out{}, err
		}
		if err := Progress(ctx, req, 2, 2, "Almost done", nil); err != nil {
			return nil, out{}, err
		}
		if token {
			// Wait until the client saw both reports: the client may handle notifications after the response.
			select {
			case <-release:
			case <-ctx.Done():
				return nil, out{}, ctx.Err()
			}
		}
		return nil, out{Token: token}, nil
	})

	ctx := t.Context()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	reports := make(chan *mcp.ProgressNotificationParams, 10)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			reports <- req.Params
		},
	})
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()

	// Without a token: no reports, no waiting.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "demo_pair"})
	if err != nil || res.StructuredContent.(map[string]any)["token"] != false {
		t.Fatalf("call without token = %+v, %v", res, err)
	}

	params := &mcp.CallToolParams{Name: "demo_pair"}
	params.SetProgressToken("tok-1")
	done := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(ctx, params)
		done <- err
	}()
	first, second := <-reports, <-reports
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if first.ProgressToken != "tok-1" || first.Progress != 1 || first.Total != 2 || first.Message != "Press OK on the TV remote" {
		t.Errorf("first report = %+v", first)
	}
	if i18n, _ := first.Meta[MetaI18n].(map[string]any); i18n["ru"] != "Нажмите OK на пульте" {
		t.Errorf("first report _meta = %v", first.Meta)
	}
	if second.Progress != 2 || second.Message != "Almost done" || second.Meta[MetaI18n] != nil {
		t.Errorf("second report = %+v", second)
	}
	select {
	case r := <-reports:
		t.Errorf("unexpected report %+v (the call without a token must send none)", r)
	default:
	}
}
