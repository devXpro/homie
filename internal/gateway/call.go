package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/devXpro/homie/internal/audit"
	"github.com/devXpro/homie/internal/hub"
)

var errRateLimited = errors.New("rate limited")

// callTool serves POST /api/v1/tools/{name}. Checks run in this order: key (401, in authenticate), rate (429),
// tool visible to the person (404), body (400), then the call itself (200, 422, 503, 504).
func (g *gateway) callTool(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey{}).(principal)
	name := r.PathValue("name")
	caller := hub.Caller{Person: p.person.Name, Via: "site"}
	start := g.now()

	if !g.limiter.allow(p.bucket) {
		g.record(r.Context(), caller, name, nil, start, hub.Result{}, errRateLimited)
		writeError(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	tool, ok := g.current().view(g, p.person).byName[name]
	if !ok {
		g.record(r.Context(), caller, name, nil, start, hub.Result{}, hub.ErrUnknownTool)
		writeError(w, http.StatusNotFound, "unknown tool")
		return
	}
	args, err := readArgs(w, r)
	if err != nil {
		g.record(r.Context(), caller, name, nil, start, hub.Result{}, err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	res, err := g.invoke(r.Context(), caller, tool, args)
	switch {
	case err == nil && res.IsError:
		writeError(w, http.StatusUnprocessableEntity, failureText(res))
	case err == nil && present(res.Structured) != nil && json.Valid(res.Structured):
		writeRaw(w, http.StatusOK, res.Structured)
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]string{"text": res.Text})
	case errors.Is(err, hub.ErrUnknownTool):
		writeError(w, http.StatusNotFound, "unknown tool")
	case errors.Is(err, hub.ErrPluginDown):
		writeError(w, http.StatusServiceUnavailable, "plugin unavailable")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "timeout")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// readArgs reads the body as the arguments object. An empty body means no arguments.
func readArgs(w http.ResponseWriter, r *http.Request) (json.RawMessage, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		return nil, errors.New("request body is too large or unreadable")
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(body) {
		return nil, errors.New("invalid JSON")
	}
	if body[0] != '{' {
		return nil, errors.New("the body must be a JSON object of arguments")
	}
	return body, nil
}

func failureText(res hub.Result) string {
	if res.Text != "" {
		return res.Text
	}
	return "The tool failed without saying why."
}

// invoke calls the tool with its own timeout and records the outcome. Both surfaces end here.
func (g *gateway) invoke(ctx context.Context, caller hub.Caller, tool hub.Tool, args json.RawMessage) (hub.Result, error) {
	start := g.now()
	if tool.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, tool.Timeout)
		defer cancel()
	}
	res, err := g.host.Call(ctx, caller, tool.Name, args)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) && !errors.Is(err, context.DeadlineExceeded) {
		err = errors.Join(err, context.DeadlineExceeded)
	}
	g.record(context.WithoutCancel(ctx), caller, tool.Name, args, start, res, err)
	return res, err
}

func (g *gateway) record(ctx context.Context, caller hub.Caller, name string, args json.RawMessage, start time.Time,
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
		Error:    AuditError(res, err),
		Duration: g.now().Sub(start),
	}
	if e.Error == "" && res.IsError {
		e.Error = failureText(res)
	}
	g.audit.Record(ctx, e)
}

// errInvalidArgs is what the audit log keeps of a schema-validation failure.
const errInvalidArgs = "invalid arguments (rejected by the tool's input schema; details not logged)"

// AuditError is the error text for an audit entry: "" when the call worked. Schema-validation failures quote the
// rejected argument values, and those may be secrets (a PIN, a pairing code) that Redact hid in the arguments, so
// they become a fixed message. Both go-sdk shapes are caught: the isError result of a typed handler
// (`validating "arguments": …`) and a JSON-RPC invalid-params error.
func AuditError(res hub.Result, err error) string {
	var rpcErr *jsonrpc.Error
	switch {
	case err != nil && errors.As(err, &rpcErr) && rpcErr.Code == jsonrpc.CodeInvalidParams:
		return errInvalidArgs
	case err != nil:
		return err.Error()
	case res.IsError && strings.HasPrefix(strings.TrimSpace(res.Text), `validating "arguments"`):
		return errInvalidArgs
	case res.IsError:
		return res.Text
	}
	return ""
}

// mcpToolHandler runs one tool for /mcp. Every failure becomes an isError result with text the model can relay.
func (g *gateway) mcpToolHandler(tool hub.Tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var p principal
		if req.Extra != nil && req.Extra.TokenInfo != nil {
			p, _ = req.Extra.TokenInfo.Extra[principalExtra].(principal)
		}
		if p.bucket == "" {
			return failure("Not authorized."), nil
		}
		caller := hub.Caller{Person: p.person.Name, Via: "mcp"}
		if !g.limiter.allow(p.bucket) {
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
