package admin

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/devXpro/homie/internal/wire"
)

// Jobs: long hub operations that are not tool calls (ADMIN-API §2, Job). The admin runs the installer's function
// detached from the request, turns its reports into Job objects and SSE "job" events, and audits the outcome.

const (
	keepJobs    = 50
	keepJobsFor = time.Hour
)

var jobSteps = map[string][]string{
	"plugin.install": {"resolve", "download", "verify", "unpack", "start"},
	"plugin.update":  {"resolve", "download", "verify", "unpack", "switch", "start"},
	"plugin.remove":  {"stop", "remove"},
	"hub.update":     {"resolve", "download", "verify", "replace", "restart"},
}

type jobProgressJSON struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total,omitempty"`
}

type jobJSON struct {
	ID        string           `json:"id"`
	Kind      string           `json:"kind"`
	Target    string           `json:"target,omitempty"`
	State     string           `json:"state"`
	Steps     []string         `json:"steps"`
	Step      string           `json:"step,omitempty"`
	Progress  *jobProgressJSON `json:"progress,omitempty"`
	Message   string           `json:"message,omitempty"`
	StartedAt string           `json:"startedAt"`
	EndedAt   *string          `json:"endedAt,omitempty"`
	Error     *callErrorJSON   `json:"error,omitempty"`
}

// jobEntry is one job; j is guarded by Server.mu. done is closed when it ended.
type jobEntry struct {
	j       jobJSON
	started time.Time
	ended   time.Time
	done    chan struct{}
}

// jobFunc does the work and returns the plugin id it acted on (install learns it from the release).
type jobFunc func(ctx context.Context, report Reporter) (target string, err error)

// startJob starts a job unless one of the same kind and target runs, or any plugin job (install, update, remove) of
// the same plugin (409 job_running). The installer also locks per plugin id, for an install whose id comes from the
// release. The job, its audit entry and its caller outlive q.
func (s *Server) startJob(q *request, kind, target string, fn jobFunc) (*jobEntry, error) {
	s.mu.Lock()
	for _, e := range s.jobs {
		samePlugin := strings.HasPrefix(e.j.Kind, "plugin.") && strings.HasPrefix(kind, "plugin.")
		if e.j.State == "running" && e.j.Target == target && (e.j.Kind == kind || samePlugin) {
			s.mu.Unlock()
			return nil, &wire.Error{Status: http.StatusConflict, Code: "job_running", Message: "this is already in progress",
				Extra: map[string]string{"jobId": e.j.ID}}
		}
	}
	start := s.now()
	steps := jobSteps[kind]
	e := &jobEntry{
		j: jobJSON{
			ID: "j_" + randomID(9), Kind: kind, Target: target, State: "running", Steps: steps,
			StartedAt: ts(start),
		},
		started: start,
		done:    make(chan struct{}),
	}
	if len(steps) > 0 {
		e.j.Step = steps[0]
	}
	s.jobs[e.j.ID] = e
	s.pruneJobsLocked()
	snapshot := e.j
	s.mu.Unlock()
	s.events.publish("job", snapshot)

	rec := &request{ctx: context.WithoutCancel(q.ctx), v: q.v, local: q.local, ip: q.ip}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		gotTarget, err := fn(s.ctx, func(r JobReport) { s.jobReport(e, r) })
		s.finishJob(e, gotTarget, err)
		s.mu.Lock()
		auditTarget := e.j.Target
		if kind == "hub.update" {
			auditTarget = e.j.Message
		}
		s.mu.Unlock()
		s.recordAdmin(rec, kind, auditTarget, nil, err)
		if err == nil && kind == "plugin.remove" {
			if err := s.dropPluginAccess(rec, target); err != nil {
				s.log.Error("cannot take the removed plugin's permission from people and invites", "plugin", target, "err", err)
			}
			s.events.publish("plugin.removed", map[string]string{"id": target})
		}
	}()
	return e, nil
}

// jobSnapshot returns a job as it is now.
func (s *Server) jobSnapshot(e *jobEntry) jobJSON {
	s.mu.Lock()
	defer s.mu.Unlock()
	return e.j
}

func (s *Server) jobReport(e *jobEntry, r JobReport) {
	s.mu.Lock()
	if e.j.State != "running" {
		s.mu.Unlock()
		return
	}
	if r.Step != "" {
		e.j.Step = r.Step
	}
	if r.Done > 0 || r.Total > 0 {
		e.j.Progress = &jobProgressJSON{Done: r.Done, Total: r.Total}
	} else if r.Step != "" {
		e.j.Progress = nil
	}
	if r.Message != "" {
		e.j.Message = r.Message
	}
	snapshot := e.j
	s.mu.Unlock()
	s.events.publish("job", snapshot)
}

func (s *Server) finishJob(e *jobEntry, target string, err error) {
	s.mu.Lock()
	end := s.now()
	e.ended = end
	e.j.EndedAt = optTS(end)
	e.j.Progress = nil
	if target != "" {
		e.j.Target = target
	}
	if err == nil {
		e.j.State = "ok"
	} else {
		e.j.State = "failed"
		if je, ok := errors.AsType[*JobError](err); ok {
			e.j.Error = &callErrorJSON{Code: je.Code, Message: je.Message}
		} else {
			e.j.Error = &callErrorJSON{Code: "internal", Message: err.Error()}
		}
	}
	snapshot := e.j
	s.mu.Unlock()
	close(e.done)
	s.events.publish("job", snapshot)
}

func (s *Server) pruneJobsLocked() {
	now := s.now()
	var finished []*jobEntry
	for id, e := range s.jobs {
		if e.ended.IsZero() {
			continue
		}
		if now.Sub(e.ended) > keepJobsFor {
			delete(s.jobs, id)
			continue
		}
		finished = append(finished, e)
	}
	if len(finished) <= keepJobs {
		return
	}
	slices.SortFunc(finished, func(a, b *jobEntry) int { return a.ended.Compare(b.ended) })
	for _, e := range finished[:len(finished)-keepJobs] {
		delete(s.jobs, e.j.ID)
	}
}

func (s *Server) listJobs(q *request) error {
	state := q.r.URL.Query().Get("state")
	s.mu.Lock()
	s.pruneJobsLocked()
	entries := make([]*jobEntry, 0, len(s.jobs))
	for _, e := range s.jobs {
		if state == "" || e.j.State == state {
			entries = append(entries, e)
		}
	}
	slices.SortFunc(entries, func(a, b *jobEntry) int { return b.started.Compare(a.started) })
	items := make([]jobJSON, 0, len(entries))
	for _, e := range entries {
		items = append(items, e.j)
	}
	s.mu.Unlock()
	return s.writeJSON(q, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getJob(q *request) error {
	s.mu.Lock()
	e, ok := s.jobs[q.r.PathValue("id")]
	var snapshot jobJSON
	if ok {
		snapshot = e.j
	}
	s.mu.Unlock()
	if !ok {
		return notFound("job")
	}
	return s.writeJSON(q, http.StatusOK, snapshot)
}
