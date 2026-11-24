package plugins

import (
	"context"
	"strconv"
	"sync"

	"github.com/devXpro/homie/pluginkit"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Progress is one progress report of a running tool call (MCP notifications/progress).
type Progress struct {
	Progress float64           // grows with every report
	Total    float64           // 0 when unknown
	Message  string            // short English text for a person; optional
	I18n     map[string]string // Message translated, by language ("ru"); optional
}

type progressKey struct{}

// WithProgress returns a context that makes Host.Call ask the plugin for progress reports and hand each one to fn.
// fn runs on the plugin session's notification goroutine, one report at a time and in order: it must return quickly.
// It is never called after Call has returned, so reports that arrive with or after the result are dropped.
func WithProgress(ctx context.Context, fn func(Progress)) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

func progressFunc(ctx context.Context) func(Progress) {
	fn, _ := ctx.Value(progressKey{}).(func(Progress))
	return fn
}

// progressSink routes the reports of one call to its callback.
type progressSink struct {
	plugin string // only this plugin may report to the sink

	mu   sync.Mutex
	done bool
	fn   func(Progress)
}

// watchProgress registers fn for one call to plugin and returns the progress token to send and the function that
// unregisters it; fn is not called once that function has returned.
func (h *Host) watchProgress(plugin string, fn func(Progress)) (token string, stop func()) {
	h.mu.Lock()
	h.progressSeq++
	token = "homie-" + strconv.FormatUint(h.progressSeq, 10)
	sink := &progressSink{plugin: plugin, fn: fn}
	if h.progress == nil {
		h.progress = map[string]*progressSink{}
	}
	h.progress[token] = sink
	h.mu.Unlock()
	return token, func() {
		h.mu.Lock()
		delete(h.progress, token)
		h.mu.Unlock()
		sink.mu.Lock()
		sink.done = true
		sink.mu.Unlock()
	}
}

// deliverProgress hands a progress notification from plugin to the call it belongs to. Reports with an unknown token,
// for a call that ended, or for another plugin's call are dropped.
func (h *Host) deliverProgress(plugin string, p *mcp.ProgressNotificationParams) {
	if p == nil {
		return
	}
	token, _ := p.ProgressToken.(string)
	h.mu.Lock()
	sink := h.progress[token]
	h.mu.Unlock()
	if sink == nil || sink.plugin != plugin {
		return
	}
	report := Progress{Progress: p.Progress, Total: p.Total, Message: p.Message, I18n: messageI18n(p.Meta)}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if !sink.done {
		sink.fn(report)
	}
}

// messageI18n reads the notification _meta "homie/i18n": {"<lang>": "<message>"}, skipping anything malformed.
func messageI18n(meta mcp.Meta) map[string]string {
	langs, _ := meta[pluginkit.MetaI18n].(map[string]any)
	var out map[string]string
	for lang, v := range langs {
		s, ok := v.(string)
		if !ok || s == "" || !langRe.MatchString(lang) {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[lang] = s
	}
	return out
}
