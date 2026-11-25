package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/devXpro/homie/internal/audit"
	"github.com/devXpro/homie/internal/hub"
)

var errRateLimited = errors.New("rate limited")

func failureText(res hub.Result) string {
	if res.Text != "" {
		return res.Text
	}
	return "The tool failed without saying why."
}

// via is the surface of an assistant's call: "chatgpt" or "claude", from the OAuth client behind the token.
func via(p principal) string {
	if p.access.Assistant != "" {
		return string(p.access.Assistant)
	}
	return "mcp"
}

// invoke calls the tool with its own timeout, at most MaxToolTimeout, and records the outcome.
func (g *Gateway) invoke(ctx context.Context, caller hub.Caller, tool hub.Tool, args json.RawMessage) (hub.Result, error) {
	start := g.now()
	timeout := tool.Timeout
	if timeout <= 0 || timeout > g.opts.MaxToolTimeout {
		timeout = g.opts.MaxToolTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := g.host.Call(ctx, caller, tool.Name, args)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) && !errors.Is(err, context.DeadlineExceeded) {
		err = errors.Join(err, context.DeadlineExceeded)
	}
	g.record(context.WithoutCancel(ctx), caller, tool.Name, args, start, res, err)
	return res, err
}

func (g *Gateway) record(ctx context.Context, caller hub.Caller, name string, args json.RawMessage, start time.Time,
	res hub.Result, err error,
) {
	if g.audit == nil {
		return
	}
	e := hub.AuditEntry{
		At:       start,
		Caller:   caller,
		Tool:     name,
		Args:     audit.Redact(args),
		OK:       err == nil && !res.IsError,
		Error:    audit.ErrorText(res, err),
		Duration: g.now().Sub(start),
	}
	if e.Error == "" && res.IsError {
		e.Error = failureText(res)
	}
	g.audit.Record(ctx, e)
}

// mcpToolHandler runs one tool for /mcp. Every failure becomes an isError result with text the model can relay.
func (g *Gateway) mcpToolHandler(tool hub.Tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p principal
		ok := false
		if req.Extra != nil && req.Extra.TokenInfo != nil {
			p, ok = req.Extra.TokenInfo.Extra[principalExtra].(principal)
		}
		if !ok || p.access.GrantID == "" {
			return failure("Not authorized."), nil
		}
		caller := hub.Caller{Person: p.person.Name, PersonID: string(p.person.ID), Via: via(p)}
		if !g.limiter.allow(string(p.access.GrantID)) {
			g.record(ctx, caller, tool.Name, nil, g.now(), hub.Result{}, errRateLimited)
			return failure("Too many calls right now. Wait a minute and try again."), nil
		}
		args := req.Params.Arguments
		if present(args) == nil {
			args = json.RawMessage(`{}`)
		}

		res, err := g.invoke(ctx, caller, tool, args)
		switch {
		case err == nil && res.IsError:
			return failure(failureText(res)), nil
		case err == nil:
			out := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: res.Text}}}
			if s := present(res.Structured); s != nil && json.Valid(s) {
				out.StructuredContent = s
				if res.Text == "" {
					out.Content = []mcp.Content{&mcp.TextContent{Text: string(s)}}
				}
			}
			return out, nil
		case errors.Is(err, hub.ErrUnknownTool):
			return failure("This tool is gone from the home hub. Start a new chat so the tool list reloads."), nil
		case errors.Is(err, hub.ErrPluginDown):
			return failure("The home service for this device is not running right now. Try again later."), nil
		case errors.Is(err, context.DeadlineExceeded):
			return failure("The device did not answer in time. It is unknown whether it took effect."), nil
		default:
			return failure("The home hub could not run this tool."), nil
		}
	}
}

func failure(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: true}
}
