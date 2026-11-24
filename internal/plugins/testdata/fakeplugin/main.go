// Command fakeplugin is the plugin the host tests run. Its plugin id is "fake".
//
//	fakeplugin serve [--crash-on-start] [--stubborn] [--legacy]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/devXpro/homie/pluginkit"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type empty struct{}

type textIn struct {
	Text string `json:"text"`
}

type sleepIn struct {
	Ms int `json:"ms"`
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func main() {
	fs := flag.NewFlagSet("fakeplugin", flag.ExitOnError)
	crashOnStart := fs.Bool("crash-on-start", false, "exit 3 before speaking MCP")
	stubborn := fs.Bool("stubborn", false, "ignore SIGTERM and stay alive after stdin closes, so only SIGKILL stops it")
	legacy := fs.Bool("legacy", false, "speak only protocol 2025-11-25, like a plugin built on an older SDK")
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	_ = fs.Parse(args)

	fmt.Fprintln(os.Stderr, "fake plugin starting")
	if *crashOnStart {
		fmt.Fprintln(os.Stderr, "fake plugin crashing on start")
		os.Exit(3)
	}

	s := pluginkit.New(pluginkit.Plugin{
		ID: "fake", Name: "homie-fake", Title: "Fake", Version: "1.0.0",
		Instructions: "Fake instructions.",
	})
	if *legacy {
		s = mcp.NewServer(&mcp.Implementation{Name: "homie-fake", Title: "Fake", Version: "1.0.0"}, &mcp.ServerOptions{
			Instructions:              "Fake instructions.",
			SupportedProtocolVersions: []string{"2025-11-25"},
		})
	}

	echo := pluginkit.I18n(&mcp.Tool{Name: "fake_echo", Description: "Echoes text."}, "ru", pluginkit.Text{
		Title: "Эхо", Description: "Повторяет текст.", Properties: map[string]string{"text": "Текст"},
	})
	echo.Meta[pluginkit.MetaI18n].(map[string]any)["Not a tag"] = map[string]any{"title": "x"} // skipped by the hub
	mcp.AddTool(s, echo,
		func(_ context.Context, req *mcp.CallToolRequest, in textIn) (*mcp.CallToolResult, map[string]string, error) {
			person, via := pluginkit.Caller(req)
			return text(in.Text), map[string]string{"text": in.Text, "caller": person, "via": via}, nil
		})
	// fake_progress reports twice; when the hub asked for reports, it then waits for fake_release, so the test can
	// see both reports arrive before the result.
	release := make(chan struct{}, 1)
	mcp.AddTool(s, &mcp.Tool{Name: "fake_progress", Description: "Reports progress twice."},
		func(ctx context.Context, req *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, any, error) {
			if err := pluginkit.Progress(ctx, req, 1, 2, "Press OK", map[string]string{"ru": "Нажмите OK", "bad tag": "x"}); err != nil {
				return nil, nil, err
			}
			if err := pluginkit.Progress(ctx, req, 2, 2, "Almost done", nil); err != nil {
				return nil, nil, err
			}
			if req.Params.GetProgressToken() == nil {
				return text("no token"), nil, nil
			}
			select {
			case <-release:
				return text("released"), nil, nil
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		})
	mcp.AddTool(s, &mcp.Tool{Name: "fake_release", Description: "Lets fake_progress finish."},
		func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, any, error) {
			release <- struct{}{}
			return text("ok"), nil, nil
		})
	mcp.AddTool(s, pluginkit.Timeout(&mcp.Tool{Name: "fake_sleep", Description: "Sleeps ms milliseconds."}, time.Second),
		func(ctx context.Context, _ *mcp.CallToolRequest, in sleepIn) (*mcp.CallToolResult, any, error) {
			fmt.Fprintln(os.Stderr, "sleeping", in.Ms)
			select {
			case <-time.After(time.Duration(in.Ms) * time.Millisecond):
				return text("slept"), nil, nil
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		})
	mcp.AddTool(s, &mcp.Tool{Name: "fake_crash", Description: "Exits with status 3."},
		func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, any, error) {
			fmt.Fprintln(os.Stderr, "fake plugin crashing on request")
			os.Exit(3)
			return nil, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "fake_log", Description: "Writes text to stderr."},
		func(_ context.Context, _ *mcp.CallToolRequest, in textIn) (*mcp.CallToolResult, any, error) {
			fmt.Fprintln(os.Stderr, in.Text)
			return text("logged"), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "fake_fail", Description: "Fails as a tool."},
		func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "The fake device is unreachable."}}}, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "fake_env", Description: "Reports its environment."},
		func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, map[string]any, error) {
			wd, _ := os.Getwd()
			return text("env"), map[string]any{"env": os.Environ(), "cwd": wd}, nil
		})
	mcp.AddTool(s, pluginkit.Admin(&mcp.Tool{Name: "fake_add", Description: "Adds the tool fake_<text>."}),
		func(_ context.Context, _ *mcp.CallToolRequest, in textIn) (*mcp.CallToolResult, any, error) {
			mcp.AddTool(s, &mcp.Tool{Name: "fake_" + in.Text, Description: "Added at run time."},
				func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, any, error) {
					return text("added tool ran"), nil, nil
				})
			return text("added"), nil, nil
		})
	mcp.AddTool(s, pluginkit.Inventory(&mcp.Tool{
		Name: "fake_list", Description: "Lists fake devices.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}), func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, any, error) {
		return text("one device"), nil, nil
	})
	// Bad _meta values: an unknown audience hides the tool, the timeout is capped, a second inventory and an
	// inventory without readOnlyHint are ignored.
	weird := &mcp.Tool{Name: "fake_weird", Description: "Has odd _meta.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}
	weird.Meta = mcp.Meta{pluginkit.MetaAudience: "bogus", pluginkit.MetaTimeout: 999, pluginkit.MetaRole: "inventory"}
	mcp.AddTool(s, weird, func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, any, error) {
		return text("weird"), nil, nil
	})
	mcp.AddTool(s, pluginkit.Inventory(&mcp.Tool{Name: "fake_writer", Description: "Not read-only."}),
		func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, any, error) {
			return text("writer"), nil, nil
		})
	// Invalid names: the hub skips them.
	for _, name := range []string{"other_tool", "fake-dash", "fake_Upper"} {
		mcp.AddTool(s, &mcp.Tool{Name: name, Description: "Invalid name."},
			func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, any, error) {
				return text("should not be reachable"), nil, nil
			})
	}

	signals := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if *stubborn {
		signal.Ignore(syscall.SIGTERM)
		signals = signals[:1]
	}
	ctx, stop := signal.NotifyContext(context.Background(), signals...)
	err := pluginkit.Serve(ctx, s)
	stop()
	if *stubborn {
		fmt.Fprintln(os.Stderr, "fake plugin refuses to exit")
		time.Sleep(time.Hour) // not select{}: with no other goroutine the runtime would call it a deadlock and exit
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}
