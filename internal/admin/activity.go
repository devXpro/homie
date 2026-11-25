package admin

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/devXpro/mcp-gatekeeper/core"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/store"
)

// The activity log: every tool call (GET /activity, GET /activity/{id}, the "activity" events). An owner reads
// everyone's; a member only their own.

type activityPersonJSON struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

type activityItemJSON struct {
	ID         string              `json:"id"`
	At         string              `json:"at"`
	Person     *activityPersonJSON `json:"person,omitempty"`
	Via        string              `json:"via"`
	Tool       string              `json:"tool"`
	ToolTitle  string              `json:"toolTitle,omitempty"`
	PluginID   string              `json:"pluginId,omitempty"`
	Args       json.RawMessage     `json:"args,omitempty"`
	OK         bool                `json:"ok"`
	Error      string              `json:"error,omitempty"`
	DurationMs int64               `json:"durationMs"`
}

func toActivityItem(rec store.ActivityRecord, titles map[string]string) activityItemJSON {
	out := activityItemJSON{
		ID: itoa(rec.ID), At: ts(rec.At), Via: rec.Via, Tool: rec.Tool, ToolTitle: titles[rec.Tool], PluginID: rec.PluginID,
		Args: rec.Args, OK: rec.OK, Error: rec.Error, DurationMs: rec.Duration.Milliseconds(),
	}
	if rec.Person != "" || rec.PersonID != "" {
		out.Person = &activityPersonJSON{ID: rec.PersonID, Name: rec.Person}
	}
	return out
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

// publishActivity sends stored records as "activity" events: to the owners, and to the member who made the call.
func (s *Server) publishActivity(recs []store.ActivityRecord) {
	titles := s.toolTitles()
	for _, rec := range recs {
		data, err := marshal(toActivityItem(rec, titles))
		if err != nil {
			continue
		}
		var member []byte
		if rec.PersonID != "" {
			member = data
		}
		s.events.publishRaw("activity", data, member, rec.PersonID)
	}
}

// cursor is the opaque page cursor: the next BeforeID bound to a hash of the filter.
type cursor struct {
	Before int64  `json:"b"`
	Filter string `json:"f"`
}

func filterHash(f store.ActivityFilter) string {
	f.BeforeID, f.Limit = 0, 0
	b, _ := json.Marshal(f)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// listActivity is GET /activity: filters person, plugin, tool, via (a comma list), status, since, until; newest
// first, cursor-paged. A member's person filter is always themselves.
func (s *Server) listActivity(q *request) error {
	v := q.r.URL.Query()
	f := store.ActivityFilter{
		PersonID: v.Get("person"), PluginID: v.Get("plugin"), Tool: v.Get("tool"),
		Status: store.ActivityStatus(v.Get("status")), Limit: 50,
	}
	if !q.owner() {
		f.PersonID = string(q.v.Person.ID)
	}
	switch f.Status {
	case store.ActivityAny, store.ActivityOK, store.ActivityFailed:
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
	recs, err := s.st.Activity().Query(q.ctx, f)
	if err != nil {
		return err
	}
	titles := s.toolTitles()
	items := make([]activityItemJSON, 0, len(recs))
	for _, rec := range recs {
		items = append(items, toActivityItem(rec, titles))
	}
	body := map[string]any{"items": items}
	if len(recs) == f.Limit {
		b, _ := json.Marshal(cursor{Before: recs[len(recs)-1].ID, Filter: hash})
		body["nextCursor"] = base64.RawURLEncoding.EncodeToString(b)
	}
	return s.writeJSON(q, http.StatusOK, body)
}

// getActivity is GET /activity/{id}; a member gets 404 for anyone else's.
func (s *Server) getActivity(q *request) error {
	id, err := strconv.ParseInt(q.r.PathValue("id"), 10, 64)
	if err != nil {
		return notFound("activity record")
	}
	rec, err := s.st.Activity().Get(q.ctx, id)
	if errors.Is(err, core.ErrNotFound) || err == nil && !q.owner() && rec.PersonID != string(q.v.Person.ID) {
		return notFound("activity record")
	}
	if err != nil {
		return err
	}
	return s.writeJSON(q, http.StatusOK, toActivityItem(rec, s.toolTitles()))
}
