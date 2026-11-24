// Command example is a minimal Homie plugin built on pluginkit. It shows an assistant tool, an admin tool and an
// inventory tool.
//
//	go build -o bin/homie-echo ./pluginkit/example
//	homed plugin add --id echo -- "$PWD/bin/homie-echo" serve
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/devXpro/homie/pluginkit"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type sayIn struct {
	Text string `json:"text" jsonschema:"the text to repeat, at most 200 characters"`
}

type sayOut struct {
	Text   string `json:"text"`
	Caller string `json:"caller,omitempty"`
}

type item struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state,omitempty"`
}

type listOut struct {
	Items []item `json:"items"`
}

func newServer() *mcp.Server {
	s := pluginkit.New(pluginkit.Plugin{
		ID:           "echo",
		Name:         "homie-echo",
		Title:        "Echo",
		Version:      "0.1.0",
		Instructions: "Use echo_say to repeat a phrase back to the user.",
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "echo_say",
		Description: "Repeats the given text back. Text is at most 200 characters.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in sayIn) (*mcp.CallToolResult, sayOut, error) {
		if len(in.Text) > 200 {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "The text is too long: keep it under 200 characters."}},
			}, sayOut{}, nil
		}
		person, via := pluginkit.Caller(req)
		slog.Info("say", "caller", person, "via", via)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, sayOut{Text: in.Text, Caller: person}, nil
	})

	mcp.AddTool(s, pluginkit.Inventory(&mcp.Tool{
		Name:        "echo_list",
		Description: "Lists the echo devices.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}), func(context.Context, *mcp.CallToolRequest, any) (*mcp.CallToolResult, listOut, error) {
		return nil, listOut{Items: []item{{ID: "echo1", Name: "Echo", State: "on"}}}, nil
	})

	mcp.AddTool(s, pluginkit.Admin(pluginkit.Timeout(&mcp.Tool{
		Name:        "echo_reset",
		Description: "Resets the echo device. Admin only.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true},
	}, 3*time.Minute)), func(context.Context, *mcp.CallToolRequest, any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Echo is reset."}}}, nil, nil
	})
	return s
}

func main() {
	if len(os.Args) > 1 && os.Args[1] != "serve" {
		fmt.Fprintf(os.Stderr, "usage: %s [serve]\n", os.Args[0])
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := pluginkit.Serve(ctx, newServer())
	stop()
	if err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
