package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/devXpro/homie/internal/audit"
	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/plugins"
	"github.com/devXpro/homie/internal/wire"
)

// Admin tool calls (ADMIN-API §3.5, "Tool call rules"): validated against the input schema, detached from the HTTP
// request, one running call per tool, progress over SSE, finished calls kept for 15 minutes (at most 100).

const (
	keepCalls    = 100
	keepCallsFor = 15 * time.Minute
)

type progressJSON struct {
	Progress float64 `json:"progress"`
	Total    float64 `json:"total,omitempty"`
	Message  string  `json:"message,omitempty"`
	// MessageI18n holds Message translated by language ({"ru": "…"}); an addition to ADMIN-API §2 (orchestrator,
	// 2026-11-15). The SPA picks its language and falls back to Message.
	MessageI18n map[string]string `json:"messageI18n,omitempty"`
}

type callResultJSON struct {
	Text       string          `json:"text"`
	Structured json.RawMessage `json:"structured,omitempty"`
}

type callErrorJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type callJSON struct {
	ID         string          `json:"id"`
	Tool       string          `json:"tool"`
	PluginID   string          `json:"pluginId"`
	Title      string          `json:"title"`
	Args       json.RawMessage `json:"args"`
	State      string          `json:"state"`
	StartedAt  string          `json:"startedAt"`
	EndedAt    *string         `json:"endedAt,omitempty"`
	DurationMs *int64          `json:"durationMs,omitempty"`
	TimeoutMs  int64           `json:"timeoutMs"`
	Progress   *progressJSON   `json:"progress,omitempty"`
	Result     *callResultJSON `json:"result,omitempty"`
	Error      *callErrorJSON  `json:"error,omitempty"`
}

// callEntry is one call; c is guarded by Server.mu.
type callEntry struct {
	c         callJSON
	started   time.Time
	ended     time.Time
	cancel    context.CancelFunc
	cancelled bool
	done      chan struct{}

	// SSE throttle for progress events
	lastSent time.Time
	pending  bool
}

func (s *Server) startCall(q *request) error {
	var body struct {
		Args json.RawMessage `json:"args"`
	}
	if err := decode(q, &body); err != nil {
		return err
	}
	e, err := s.beginCall(q, q.r.PathValue("name"), body.Args)
	if err != nil {
		return err
	}
	if q.r.URL.Query().Get("wait") != "1" {
		return s.writeJSON(q, http.StatusAccepted, s.callSnapshot(e))
	}
	select {
	case <-e.done:
		return s.writeJSON(q, http.StatusOK, s.callSnapshot(e))
	case <-q.ctx.Done():
		return nil
	}
}

// beginCall starts a call of tool name with args (nil is {}) for the caller of q, detached from q: the checks, then
// the call on its own goroutine. The admin socket's `tool call` waits for it like ?wait=1.
func (s *Server) beginCall(q *request, name string, args json.RawMessage) (*callEntry, error) {
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	if len(args) > maxArgsBytes {
		return nil, errorf(http.StatusRequestEntityTooLarge, "too_large", "the tool arguments are too large")
	}
	tool, ok := s.findTool(name)
	if !ok {
		return nil, errorf(http.StatusNotFound, "unknown_tool", "no such tool")
	}
	if fields := s.validateArgs(tool, args); len(fields) > 0 {
		return nil, &wire.Error{
			Status: http.StatusBadRequest, Code: "invalid_args",
			Message: strings.TrimPrefix(fields[0].Path, "/") + ": " + fields[0].Message, Fields: fields,
		}
	}
	if !s.pluginUp(tool.PluginID) {
		return nil, errorf(http.StatusServiceUnavailable, "plugin_unavailable", "the plugin is not running")
	}
	caller := s.caller(q)

	s.mu.Lock()
	if id, busy := s.running[name]; busy {
		s.mu.Unlock()
		return nil, &wire.Error{Status: http.StatusConflict, Code: "call_running", Message: "this tool is already running",
			Extra: map[string]string{"callId": id}}
	}
	ctx, cancel := context.WithCancel(s.ctx)
	start := s.now()
	e := &callEntry{
		c: callJSON{
			ID: "c_" + randomID(9), Tool: name, PluginID: tool.PluginID, Title: tool.Title, Args: audit.Redact(args),
			State: "running", StartedAt: ts(start), TimeoutMs: tool.Timeout.Milliseconds(),
		},
		started: start,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	if e.c.Title == "" {
		e.c.Title = name
	}
	s.calls[e.c.ID] = e
	s.running[name] = e.c.ID
	s.pruneCallsLocked()
	snapshot := e.c
	s.mu.Unlock()
	s.publishCall(snapshot)

	ctx = s.opts.WithProgress(ctx, func(p plugins.Progress) { s.callProgress(e, p) })
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		res, err := s.invoke(ctx, caller, tool, args)
		s.finishCall(e, res, err)
	}()
	return e, nil
}

func (s *Server) findTool(name string) (hub.Tool, bool) {
	for _, t := range s.host.Tools(hub.AudienceAdmin) {
		if t.Name == name {
			return t, true
		}
	}
	return hub.Tool{}, false
}

// pluginUp reports whether the host says the plugin runs; unknown plugins count as up and fail in Call.
func (s *Server) pluginUp(id string) bool {
	for _, st := range s.host.Status() {
		if st.ID == id {
			return st.State == hub.PluginUp
		}
	}
	return true
}

// invoke calls a tool with its own timeout and audits it, like the gateway does for assistants.
func (s *Server) invoke(ctx context.Context, caller hub.Caller, tool hub.Tool, args json.RawMessage) (hub.Result, error) {
	start := s.now()
	if tool.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, tool.Timeout)
		defer cancel()
	}
	res, err := s.host.Call(ctx, caller, tool.Name, args)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) && !errors.Is(err, context.DeadlineExceeded) {
		err = errors.Join(err, context.DeadlineExceeded)
	}
	if s.opts.Activity != nil {
		e := hub.AuditEntry{
			At: start, Caller: caller, Tool: tool.Name, Args: audit.Redact(args),
			OK: err == nil && !res.IsError, Duration: s.now().Sub(start),
		}
		// The audit gets the gateway's text: a plugin's validation error quotes the rejected values, which may be
		// the secrets Redact hid in the arguments (the caller still sees the full message in the Call).
		switch {
		case errors.Is(err, context.Canceled):
			e.Error = "cancelled"
		case err == nil && res.IsError && res.Text == "":
			e.Error = failureText(res)
		default:
			e.Error = audit.ErrorText(res, err)
		}
		s.opts.Activity.Record(context.WithoutCancel(ctx), e)
	}
	return res, err
}

func (s *Server) finishCall(e *callEntry, res hub.Result, err error) {
	s.mu.Lock()
	end := s.now()
	e.ended = end
	e.c.EndedAt = optTS(end)
	d := end.Sub(e.started).Milliseconds()
	e.c.DurationMs = &d
	switch {
	case e.cancelled && err != nil:
		e.c.State = "cancelled"
	case err == nil && res.IsError:
		e.c.State = "failed"
		e.c.Result = &callResultJSON{Text: failureText(res)}
	case err == nil:
		e.c.State = "ok"
		e.c.Result = &callResultJSON{Text: res.Text}
		if len(res.Structured) > 0 && json.Valid(res.Structured) {
			e.c.Result.Structured = res.Structured
		}
	default:
		e.c.State = "error"
		e.c.Error = callError(err, e.c.TimeoutMs)
	}
	if s.running[e.c.Tool] == e.c.ID {
		delete(s.running, e.c.Tool)
	}
	e.pending = false
	snapshot := e.c
	s.mu.Unlock()
	close(e.done)
	s.publishCall(snapshot)
}

func callError(err error, timeoutMs int64) *callErrorJSON {
	switch {
	case errors.Is(err, hub.ErrPluginDown):
		return &callErrorJSON{Code: "plugin_unavailable", Message: "the plugin stopped during the call"}
	case errors.Is(err, context.DeadlineExceeded):
		return &callErrorJSON{Code: "timeout", Message: fmt.Sprintf("the tool did not answer within %d s", timeoutMs/1000)}
	case errors.Is(err, hub.ErrUnknownTool):
		return &callErrorJSON{Code: "unknown_tool", Message: "the tool is gone"}
	default:
		return &callErrorJSON{Code: "internal", Message: err.Error()}
	}
}

// callProgress stores a progress notification. It runs on the plugin's notification goroutine, so it only copies the
// report; a timer publishes the latest one, at most one event per progressGap per call (ADMIN-API §4: 4 per second).
func (s *Server) callProgress(e *callEntry, p plugins.Progress) {
	pj := &progressJSON{Progress: p.Progress, Total: p.Total, Message: p.Message}
	if len(p.I18n) > 0 {
		pj.MessageI18n = maps.Clone(p.I18n)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.c.State != "running" {
		return
	}
	e.c.Progress = pj
	if !e.pending {
		e.pending = true
		wait := max(0, e.lastSent.Add(s.progressGap).Sub(s.now()))
		time.AfterFunc(wait, func() { s.flushProgress(e) })
	}
}

func (s *Server) flushProgress(e *callEntry) {
	s.mu.Lock()
	if !e.pending || e.c.State != "running" {
		s.mu.Unlock()
		return
	}
	e.pending = false
	e.lastSent = s.now()
	snapshot := e.c
	s.mu.Unlock()
	s.publishCall(snapshot)
}

func (s *Server) publishCall(c callJSON) {
	s.events.publish("call", c)
}

func (s *Server) callSnapshot(e *callEntry) callJSON {
	s.mu.Lock()
	defer s.mu.Unlock()
	return e.c
}

// pruneCallsLocked forgets finished calls older than 15 minutes, and the oldest finished ones beyond 100.
func (s *Server) pruneCallsLocked() {
	now := s.now()
	var finished []*callEntry
	for id, e := range s.calls {
		if e.ended.IsZero() {
			continue
		}
		if now.Sub(e.ended) > keepCallsFor {
			delete(s.calls, id)
			continue
		}
		finished = append(finished, e)
	}
	if len(finished) <= keepCalls {
		return
	}
	slices.SortFunc(finished, func(a, b *callEntry) int { return a.ended.Compare(b.ended) })
	for _, e := range finished[:len(finished)-keepCalls] {
		delete(s.calls, e.c.ID)
	}
}

func (s *Server) listCalls(q *request) error {
	state := q.r.URL.Query().Get("state")
	s.mu.Lock()
	s.pruneCallsLocked()
	entries := make([]*callEntry, 0, len(s.calls))
	for _, e := range s.calls {
		if state == "" || e.c.State == state {
			entries = append(entries, e)
		}
	}
	slices.SortFunc(entries, func(a, b *callEntry) int { return b.started.Compare(a.started) })
	items := make([]callJSON, 0, len(entries))
	for _, e := range entries {
		items = append(items, e.c)
	}
	s.mu.Unlock()
	return s.writeJSON(q, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) call(id string) (*callEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.calls[id]
	return e, ok
}

func (s *Server) getCall(q *request) error {
	e, ok := s.call(q.r.PathValue("id"))
	if !ok {
		return notFound("call")
	}
	return s.writeJSON(q, http.StatusOK, s.callSnapshot(e))
}

func (s *Server) cancelCall(q *request) error {
	e, ok := s.call(q.r.PathValue("id"))
	if !ok {
		return notFound("call")
	}
	s.mu.Lock()
	if e.c.State == "running" {
		e.cancelled = true
		e.cancel()
	}
	snapshot := e.c
	s.mu.Unlock()
	return s.writeJSON(q, http.StatusAccepted, snapshot)
}
