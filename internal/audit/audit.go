// Package audit keeps the hub's activity log: every tool call, from the assistants (the MCP endpoint), the admin and
// the CLI, with redacted arguments, in the activity table of homie.db (store.Activity). Admin actions (a plugin
// installed, the settings changed) go to MCP Gatekeeper's audit instead (core.AuditEntry).
//
// Writer is the hub.Auditor every surface records into. Record never blocks the call path: records go through a
// buffered channel to one goroutine that batches them into store.Activity.Append; when the buffer is full a record is
// dropped and counted. Arguments are redacted again on the way in, whatever the caller did (Redact).
package audit

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/store"
)

// DefaultBuffer is the number of records Record can queue before it starts dropping them.
const DefaultBuffer = 1024

const batchSize = 100

// Writer is the activity log's hub.Auditor. NewWriter starts it; Close stores what is queued and stops it.
type Writer struct {
	repo    store.Activity
	log     *slog.Logger
	ch      chan store.ActivityRecord
	done    chan struct{}
	dropped atomic.Uint64

	mu       sync.Mutex
	closed   bool
	onStored func([]store.ActivityRecord)
}

var _ hub.Auditor = (*Writer)(nil)

// NewWriter starts the writer over repo; log receives store failures (nil discards them).
func NewWriter(repo store.Activity, log *slog.Logger) *Writer {
	return newWriter(repo, DefaultBuffer, log)
}

func newWriter(repo store.Activity, buffer int, log *slog.Logger) *Writer {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	w := &Writer{repo: repo, log: log, ch: make(chan store.ActivityRecord, buffer), done: make(chan struct{})}
	go w.run()
	return w
}

// Record queues a tool call; it never blocks.
func (w *Writer) Record(_ context.Context, e hub.AuditEntry) {
	pluginID, _, _ := strings.Cut(e.Tool, "_")
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	w.enqueue(store.ActivityRecord{
		At: at, PersonID: e.Caller.PersonID, Person: e.Caller.Person, Via: e.Caller.Via, Tool: e.Tool,
		PluginID: pluginID, Args: Redact(e.Args), OK: e.OK, Error: e.Error, Duration: e.Duration,
	})
}

func (w *Writer) enqueue(rec store.ActivityRecord) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		w.dropped.Add(1)
		return
	}
	select {
	case w.ch <- rec:
	default:
		w.dropped.Add(1)
	}
}

// OnStored sets fn to receive every batch once it is stored, with the ids the store gave it (the admin publishes them
// as events). It runs on the writer's goroutine.
func (w *Writer) OnStored(fn func([]store.ActivityRecord)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onStored = fn
}

// Dropped returns how many records were lost (a full queue, a closed writer or a store failure).
func (w *Writer) Dropped() uint64 { return w.dropped.Load() }

// Close stores what is queued and stops the writer.
func (w *Writer) Close() {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.ch)
	}
	w.mu.Unlock()
	<-w.done
}

func (w *Writer) run() {
	defer close(w.done)
	for rec := range w.ch {
		batch := []store.ActivityRecord{rec}
	drain:
		for len(batch) < batchSize {
			select {
			case r, ok := <-w.ch:
				if !ok {
					break drain
				}
				batch = append(batch, r)
			default:
				break drain
			}
		}
		w.store(batch)
	}
}

func (w *Writer) store(batch []store.ActivityRecord) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stored, err := w.repo.Append(ctx, batch)
	if err != nil {
		w.dropped.Add(uint64(len(batch)))
		w.log.Error("activity: cannot store records", "count", len(batch), "err", err)
		return
	}
	w.mu.Lock()
	fn := w.onStored
	w.mu.Unlock()
	if fn != nil {
		fn(stored)
	}
}

// errInvalidArgs is what the activity log keeps of a schema-validation failure.
const errInvalidArgs = "invalid arguments (rejected by the tool's input schema; details not logged)"

// ErrorText is the error text of an activity record: "" when the call worked. Schema-validation failures quote the
// rejected argument values, and those may be secrets (a PIN, a pairing code) that Redact hid in the arguments, so
// they become a fixed message. Both go-sdk shapes are caught: the isError result of a typed handler
// (`validating "arguments": …`) and a JSON-RPC invalid-params error.
func ErrorText(res hub.Result, err error) string {
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
