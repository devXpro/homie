package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devXpro/homie/internal/hub"
)

// The events: GET /api/home/events (server-sent events) and GET /api/home/events/poll (a long poll, for a quick
// tunnel, which carries no SSE), over one ring of the last 500 events.
//
// Event ids are "<run>-<seq>": run is random per Server, so an id from another hub run is recognized and answered with
// "resync", like an id that has fallen out of the ring.

type event struct {
	seq        uint64
	typ        string
	data       []byte
	memberData []byte // what members get instead of data; nil = members do not get it
	personID   string // with memberData: the one member who gets it ("" = every member)
}

// forMember reports whether a member (not an owner) with id personID gets ev.
func (ev event) forMember(personID string) bool {
	return ev.memberData != nil && (ev.personID == "" || ev.personID == personID)
}

type subscriber struct {
	ch       chan event
	owner    bool
	personID string
	gone     chan struct{} // closed when the broker drops the subscriber (closed or too slow)
	once     sync.Once
}

func (sub *subscriber) drop() { sub.once.Do(func() { close(sub.gone) }) }

func (sub *subscriber) wants(ev event) bool { return sub.owner || ev.forMember(sub.personID) }

type broker struct {
	mu     sync.Mutex
	run    string
	seq    uint64
	ring   []event // oldest first, at most size
	size   int
	subs   map[*subscriber]struct{}
	closed bool
}

func newBroker(size int) *broker {
	return &broker{run: randomID(4), size: size, subs: map[*subscriber]struct{}{}}
}

// publish sends v as an owner-only event and keeps it in the ring.
func (b *broker) publish(typ string, v any) {
	data, err := marshal(v)
	if err != nil {
		return
	}
	b.publishRaw(typ, data, nil, "")
}

// publishRaw sends an event; memberData is what members get instead of data (nil: members do not get it), and only
// the member personID when it is set.
func (b *broker) publishRaw(typ string, data, memberData []byte, personID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.seq++
	ev := event{seq: b.seq, typ: typ, data: data, memberData: memberData, personID: personID}
	b.ring = append(b.ring, ev)
	if len(b.ring) > b.size {
		b.ring = slices.Delete(b.ring, 0, len(b.ring)-b.size)
	}
	for sub := range b.subs {
		if !sub.wants(ev) {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			// Too slow: drop it; the client reconnects with its last id and resumes from the ring.
			delete(b.subs, sub)
			sub.drop()
		}
	}
}

// subscribe registers a subscriber and returns the events after lastID to replay, or resync = true when that is
// impossible. An empty lastID replays nothing.
func (b *broker) subscribe(owner bool, personID, lastID string) (sub *subscriber, replay []event, resync bool) {
	sub = &subscriber{ch: make(chan event, 256), owner: owner, personID: personID, gone: make(chan struct{})}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		sub.drop()
		return sub, nil, false
	}
	b.subs[sub] = struct{}{}
	if lastID == "" {
		return sub, nil, false
	}
	i := strings.LastIndexByte(lastID, '-') // the run id is base64url and may itself contain "-"
	ok := i > 0
	run, seqStr := lastID[:max(i, 0)], lastID[i+1:]
	seq, err := strconv.ParseUint(seqStr, 10, 64)
	if !ok || err != nil || run != b.run || seq > b.seq {
		return sub, nil, true
	}
	if seq == b.seq {
		return sub, nil, false
	}
	if len(b.ring) == 0 || b.ring[0].seq > seq+1 {
		return sub, nil, true // the next event the client needs is gone
	}
	for _, ev := range b.ring {
		if ev.seq > seq && sub.wants(ev) {
			replay = append(replay, ev)
		}
	}
	return sub, replay, false
}

func (b *broker) unsubscribe(sub *subscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subs, sub)
}

func (b *broker) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for sub := range b.subs {
		delete(b.subs, sub)
		sub.drop()
	}
}

func (b *broker) id(seq uint64) string { return b.run + "-" + strconv.FormatUint(seq, 10) }

// latest is the id of the newest event.
func (b *broker) latest() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.id(b.seq)
}

// sseWriter writes frames and flushes them.
type sseWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (sw sseWriter) frame(id, typ string, data []byte) error {
	var buf bytes.Buffer
	if id != "" {
		fmt.Fprintf(&buf, "id: %s\n", id)
	}
	fmt.Fprintf(&buf, "event: %s\ndata: %s\n\n", typ, data)
	if _, err := sw.w.Write(buf.Bytes()); err != nil {
		return err
	}
	return sw.rc.Flush()
}

func (sw sseWriter) raw(s string) error {
	if _, err := sw.w.Write([]byte(s)); err != nil {
		return err
	}
	return sw.rc.Flush()
}

// dataFor is what a subscriber of q gets of ev.
func dataFor(q *request, ev event) []byte {
	if q.owner() {
		return ev.data
	}
	return ev.memberData
}

func (s *Server) hello() []byte {
	b, _ := marshal(map[string]string{"version": s.opts.Version, "serverTime": ts(s.now())})
	return b
}

func (s *Server) streamEvents(q *request) error {
	owner := q.owner()
	logsID := q.r.URL.Query().Get("logs")
	if logsID != "" && !owner {
		return errorf(http.StatusForbidden, "forbidden", "only an owner may follow plugin logs")
	}
	h := q.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	q.w.WriteHeader(http.StatusOK)
	sw := sseWriter{w: q.w, rc: http.NewResponseController(q.w)}

	sub, replay, resync := s.events.subscribe(owner, string(q.v.Person.ID), q.r.Header.Get("Last-Event-ID"))
	defer s.events.unsubscribe(sub)

	if sw.raw("retry: 3000\n\n") != nil || sw.frame("", "hello", s.hello()) != nil {
		return nil
	}
	if resync && sw.frame("", "resync", []byte("{}")) != nil {
		return nil
	}
	send := func(ev event) error { return sw.frame(s.events.id(ev.seq), ev.typ, dataFor(q, ev)) }
	for _, ev := range replay {
		if send(ev) != nil {
			return nil
		}
	}

	ping := time.NewTicker(s.pingEvery)
	defer ping.Stop()
	var logs <-chan time.Time
	var tail logTail
	if logsID != "" {
		t := time.NewTicker(s.logsEvery)
		defer t.Stop()
		logs = t.C
		tail.prev = s.host.Logs(logsID) // the page loads the history with GET /plugins/{id}/logs
	}
	for {
		select {
		case <-q.ctx.Done():
			return nil
		case <-s.ctx.Done():
			return nil
		case <-sub.gone:
			return nil
		case ev := <-sub.ch:
			if send(ev) != nil {
				return nil
			}
		case <-ping.C:
			// The session may have ended meanwhile (sign-out elsewhere, the person removed or their term over).
			if _, err := q.sess.Viewer(q.r); err != nil {
				_ = sw.frame("", "session.revoked", []byte("{}"))
				return nil
			}
			if sw.raw(": ping\n\n") != nil {
				return nil
			}
		case <-logs:
			if lines := tail.next(s.host.Logs(logsID)); len(lines) > 0 {
				data, _ := marshal(struct {
					PluginID string   `json:"pluginId"`
					Lines    []string `json:"lines"`
				}{logsID, lines})
				if sw.frame("", "plugin.log", data) != nil {
					return nil
				}
			}
		}
	}
}

// polled is one event of GET /events/poll.
type polled struct {
	ID    string          `json:"id,omitempty"`
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// pollEvents is GET /events/poll: without after, hello and the cursor at once; with after, the events since then, or,
// when there are none yet, the first ones within longPoll (none: an empty list and the same cursor). A cursor that
// cannot resume answers resync. plugin.log is SSE only (poll GET /plugins/{id}/logs instead).
func (s *Server) pollEvents(q *request) error {
	after := q.r.URL.Query().Get("after")
	if after == "" {
		return s.writeJSON(q, http.StatusOK, map[string]any{
			"events": []polled{{Event: "hello", Data: s.hello()}}, "cursor": s.events.latest()})
	}
	sub, replay, resync := s.events.subscribe(q.owner(), string(q.v.Person.ID), after)
	defer s.events.unsubscribe(sub)
	if resync {
		return s.writeJSON(q, http.StatusOK, map[string]any{
			"events": []polled{{Event: "resync", Data: json.RawMessage("{}")}}, "cursor": s.events.latest()})
	}
	out := make([]polled, 0, len(replay))
	cursor := after
	add := func(ev event) {
		id := s.events.id(ev.seq)
		out = append(out, polled{ID: id, Event: ev.typ, Data: dataFor(q, ev)})
		cursor = id
	}
	for _, ev := range replay {
		add(ev)
	}
	if len(out) == 0 {
		wait := time.NewTimer(s.longPoll)
		defer wait.Stop()
		select {
		case ev := <-sub.ch:
			add(ev)
		drain:
			for {
				select {
				case ev := <-sub.ch:
					add(ev)
				default:
					break drain
				}
			}
		case <-wait.C:
		case <-sub.gone:
		case <-q.ctx.Done():
			return nil
		case <-s.ctx.Done():
		}
	}
	return s.writeJSON(q, http.StatusOK, map[string]any{"events": out, "cursor": cursor})
}

// logTail finds the lines added to a bounded log ring since the previous snapshot.
type logTail struct{ prev []string }

func (t *logTail) next(cur []string) []string {
	prev := t.prev
	t.prev = cur
	if len(prev) == 0 {
		return cur
	}
	// The new lines start after the last position where cur holds prev's tail (up to 8 lines compared, fewer near
	// the start of cur, where older lines may have been rotated out).
	for end := len(cur); end >= 1; end-- {
		k := min(8, len(prev), end)
		if slices.Equal(cur[end-k:end], prev[len(prev)-k:]) {
			return cur[end:]
		}
	}
	return cur
}

// watch publishes tunnel, plugin and tool changes while the server runs.
func (s *Server) watch() {
	defer s.wg.Done()
	tick := time.NewTicker(s.pollEvery)
	defer tick.Stop()
	lastTunnel, lastPlugins := s.tunnelSnapshot(), s.pluginSnapshots()
	lastTools := s.toolFingerprints()
	changed := s.host.Changed()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-changed:
			changed = s.host.Changed()
			cur := s.toolFingerprints()
			var ids []string
			for id, fp := range cur {
				if lastTools[id] != fp {
					ids = append(ids, id)
				}
			}
			for id := range lastTools {
				if _, ok := cur[id]; !ok {
					ids = append(ids, id)
				}
			}
			if len(ids) == 0 { // only instructions changed: every plugin may be concerned
				for id := range cur {
					ids = append(ids, id)
				}
			}
			slices.Sort(ids)
			for _, id := range ids {
				s.events.publish("tools", map[string]string{"pluginId": id})
			}
			lastTools = cur
		case <-tick.C:
		}
		if cur := s.tunnelSnapshot(); cur != nil && !bytes.Equal(cur, lastTunnel) {
			s.events.publishRaw("tunnel", cur, nil, "")
			lastTunnel = cur
		}
		cur := s.pluginSnapshots()
		for id, data := range cur {
			if !bytes.Equal(data, lastPlugins[id]) {
				s.events.publishRaw("plugin", data, nil, "")
			}
		}
		lastPlugins = cur
	}
}

func (s *Server) tunnelSnapshot() []byte {
	if s.opts.Tunnel == nil {
		return nil
	}
	b, _ := marshal(s.opts.Tunnel.Status().JSON())
	return b
}

func (s *Server) pluginSnapshots() map[string][]byte {
	out := map[string][]byte{}
	sums, err := s.pluginSummaries(s.ctx)
	if err != nil {
		return out
	}
	for _, p := range sums {
		out[p.ID], _ = marshal(p)
	}
	return out
}

func (s *Server) toolFingerprints() map[string]string {
	byPlugin := map[string][]hub.Tool{}
	for _, t := range s.host.Tools(hub.AudienceAdmin) {
		byPlugin[t.PluginID] = append(byPlugin[t.PluginID], t)
	}
	out := map[string]string{}
	for id, tools := range byPlugin {
		b, _ := json.Marshal(tools)
		out[id] = string(b)
	}
	return out
}
