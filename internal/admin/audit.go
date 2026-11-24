package admin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devXpro/homie/internal/audit"
	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/store"
)

// AuditWriter is the phase 2 audit log (ADMIN-API §8): a non-blocking hub.Auditor for tool calls from every surface
// and RecordAdmin for admin and security events. A goroutine batches the records into store.Audit.Append; the admin
// API then publishes them as SSE "audit" events with the ids the store assigned. Give the same writer to the gateway
// (hub.Auditor), to the auth package (RecordAdmin) and to admin.Options.Audit.
type AuditWriter struct {
	repo    store.Audit
	people  store.People // maps a caller's display name to a person id; may be nil
	log     *slog.Logger
	ch      chan store.AuditRecord
	done    chan struct{}
	dropped atomic.Uint64

	mu       sync.Mutex
	closed   bool
	onStored func([]store.AuditRecord)
}

var _ hub.Auditor = (*AuditWriter)(nil)

const auditBatch = 100

// NewAuditWriter starts the writer. people may be nil; then tool records carry only the caller's name.
func NewAuditWriter(repo store.Audit, people store.People, log *slog.Logger) *AuditWriter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	w := &AuditWriter{repo: repo, people: people, log: log, ch: make(chan store.AuditRecord, 1024), done: make(chan struct{})}
	go w.run()
	return w
}

// Record queues a tool call; it never blocks. Arguments are redacted again here.
func (w *AuditWriter) Record(_ context.Context, e hub.AuditEntry) {
	pluginID, _, _ := strings.Cut(e.Tool, "_")
	w.enqueue(store.AuditRecord{
		At: e.At, Kind: store.AuditTool, Person: e.Caller.Person, Via: e.Caller.Via, Tool: e.Tool,
		PluginID: pluginID, Args: audit.Redact(e.Args), OK: e.OK, Error: e.Error, Duration: e.Duration,
	})
}

// RecordAdmin queues an admin or security event (Kind is set to admin); it never blocks.
func (w *AuditWriter) RecordAdmin(_ context.Context, rec store.AuditRecord) {
	rec.Kind = store.AuditAdmin
	rec.Args = audit.Redact(rec.Args)
	w.enqueue(rec)
}

// Dropped returns how many records were lost because the queue was full.
func (w *AuditWriter) Dropped() uint64 { return w.dropped.Load() }

func (w *AuditWriter) enqueue(rec store.AuditRecord) {
	if rec.At.IsZero() {
		rec.At = time.Now()
	}
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

func (w *AuditWriter) setOnStored(fn func([]store.AuditRecord)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onStored = fn
}

// Close stores what is queued and stops the writer.
func (w *AuditWriter) Close() {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.ch)
	}
	w.mu.Unlock()
	<-w.done
}

func (w *AuditWriter) run() {
	defer close(w.done)
	for rec := range w.ch {
		batch := []store.AuditRecord{rec}
	drain:
		for len(batch) < auditBatch {
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

func (w *AuditWriter) store(batch []store.AuditRecord) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w.fillPersonIDs(ctx, batch)
	stored, err := w.repo.Append(ctx, batch)
	if err != nil {
		w.dropped.Add(uint64(len(batch)))
		w.log.Error("audit: cannot store records", "count", len(batch), "err", err)
		return
	}
	w.mu.Lock()
	fn := w.onStored
	w.mu.Unlock()
	if fn != nil {
		fn(stored)
	}
}

// fillPersonIDs sets PersonID of tool records from the caller's display name, when it names exactly one person.
func (w *AuditWriter) fillPersonIDs(ctx context.Context, batch []store.AuditRecord) {
	if w.people == nil || !slices.ContainsFunc(batch, func(r store.AuditRecord) bool { return r.PersonID == "" && r.Person != "" }) {
		return
	}
	people, err := w.people.List(ctx)
	if err != nil {
		return
	}
	ids := map[string]string{}
	for _, p := range people {
		if _, dup := ids[p.Name]; dup {
			ids[p.Name] = ""
			continue
		}
		ids[p.Name] = p.ID
	}
	for i := range batch {
		if batch[i].PersonID == "" {
			batch[i].PersonID = ids[batch[i].Person]
		}
	}
}

// recordAdmin writes an admin audit record for the request's principal.
func (s *Server) recordAdmin(q *request, action, target, pluginID string, details map[string]any, failure error) {
	if s.opts.Audit == nil {
		return
	}
	rec := store.AuditRecord{
		At: s.now(), PersonID: q.p.Person.ID, Person: q.p.Person.Name, Via: q.via(),
		Action: action, Target: target, PluginID: pluginID, OK: failure == nil,
	}
	if failure != nil {
		rec.Error = failure.Error()
	}
	if len(details) > 0 {
		rec.Args, _ = marshal(details)
	}
	s.opts.Audit.RecordAdmin(q.r.Context(), rec)
}

// publishAudit sends stored records as SSE events.
func (s *Server) publishAudit(recs []store.AuditRecord) {
	titles := s.toolTitles()
	for _, rec := range recs {
		s.events.publish("audit", toAuditItem(rec, titles))
	}
}

func (s *Server) toolTitles() map[string]string {
	titles := map[string]string{}
	for _, t := range s.host.Tools(hub.AudienceAdmin) {
		if t.Title != "" {
			titles[t.Name] = t.Title
		}
	}
	return titles
}

// cursor is the opaque audit page cursor: the next BeforeID bound to a hash of the filter.
type cursor struct {
	Before int64  `json:"b"`
	Filter string `json:"f"`
}

func filterHash(f store.AuditFilter) string {
	f.BeforeID, f.Limit = 0, 0
	b, _ := json.Marshal(f)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func (s *Server) listAudit(q *request) error {
	v := q.r.URL.Query()
	f := store.AuditFilter{
		Kind: store.AuditKind(v.Get("kind")), PersonID: v.Get("person"), PluginID: v.Get("plugin"), Tool: v.Get("tool"),
		Status: store.AuditStatus(v.Get("status")), Limit: 50,
	}
	switch f.Kind {
	case "", store.AuditTool, store.AuditAdmin:
	default:
		return invalidField("/kind", "enum", `must be "tool" or "admin"`)
	}
	switch f.Status {
	case store.AuditAny, store.AuditOK, store.AuditFailed:
	default:
		return invalidField("/status", "enum", `must be "ok" or "failed"`)
	}
	if via := v.Get("via"); via != "" {
		for part := range strings.SplitSeq(via, ",") {
			if part = strings.TrimSpace(part); part != "" {
				f.Via = append(f.Via, part)
			}
		}
	}
	for _, tf := range []struct {
		key string
		dst *time.Time
	}{{"since", &f.Since}, {"until", &f.Until}} {
		if raw := v.Get(tf.key); raw != "" {
			t, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return invalidField("/"+tf.key, "format", "must be an RFC 3339 time")
			}
			*tf.dst = t
		}
	}
	if raw := v.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			return invalidField("/limit", "range", "must be 1 to 200")
		}
		f.Limit = n
	}
	hash := filterHash(f)
	if raw := v.Get("cursor"); raw != "" {
		var c cursor
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(b, &c) != nil || c.Filter != hash || c.Before <= 0 {
			return invalidField("/cursor", "cursor", "does not belong to this filter; start without a cursor")
		}
		f.BeforeID = c.Before
	}
	recs, err := s.st.Audit().Query(q.r.Context(), f)
	if err != nil {
		return err
	}
	titles := s.toolTitles()
	items := make([]auditItemJSON, 0, len(recs))
	for _, rec := range recs {
		items = append(items, toAuditItem(rec, titles))
	}
	body := map[string]any{"items": items}
	if len(recs) == f.Limit {
		b, _ := json.Marshal(cursor{Before: recs[len(recs)-1].ID, Filter: hash})
		body["nextCursor"] = base64.RawURLEncoding.EncodeToString(b)
	}
	return s.writeJSON(q, http.StatusOK, body)
}

func (s *Server) getAudit(q *request) error {
	id, err := strconv.ParseInt(q.r.PathValue("id"), 10, 64)
	if err != nil {
		return notFound("audit record")
	}
	rec, err := s.st.Audit().Get(q.r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		return notFound("audit record")
	}
	if err != nil {
		return err
	}
	return s.writeJSON(q, http.StatusOK, toAuditItem(rec, s.toolTitles()))
}
