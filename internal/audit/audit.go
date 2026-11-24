// Package audit records tool calls as JSON lines in monthly files, <dir>/audit-YYYY-MM.jsonl (PLAN.md §5.4).
//
// Record never blocks: entries go through a buffered channel to one writer goroutine, and when the buffer is full the
// entry is dropped and counted. Arguments are redacted again on the way to disk, whatever the caller did.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devXpro/homie/internal/hub"
)

// DefaultBuffer is the number of entries Record can queue before it starts dropping them.
const DefaultBuffer = 1024

// Log is a hub.Auditor writing JSONL files. Open starts it; Close flushes and stops it.
type Log struct {
	dir     string
	log     *slog.Logger
	ch      chan hub.AuditEntry
	done    chan struct{}
	dropped atomic.Uint64

	mu     sync.RWMutex // guards closed against Record racing Close
	closed bool

	// Owned by the writer goroutine.
	file  *os.File
	month string
	err   error
}

var _ hub.Auditor = (*Log)(nil)

// line is one entry as written to disk. Times are UTC; the file month is the UTC month of At.
type line struct {
	At         time.Time       `json:"at"`
	Person     string          `json:"person"`
	Via        string          `json:"via"`
	Tool       string          `json:"tool"`
	Args       json.RawMessage `json:"args,omitempty"`
	OK         bool            `json:"ok"`
	Error      string          `json:"error,omitempty"`
	DurationMS int64           `json:"duration_ms"`
}

// Open creates dir (0700) if needed and starts the writer. log receives write failures; nil discards them.
func Open(dir string, log *slog.Logger) (*Log, error) {
	l, err := newLog(dir, DefaultBuffer, log)
	if err != nil {
		return nil, err
	}
	go l.run()
	return l, nil
}

func newLog(dir string, buffer int, log *slog.Logger) (*Log, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("audit dir: %w", err)
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Log{
		dir:  dir,
		log:  log,
		ch:   make(chan hub.AuditEntry, buffer),
		done: make(chan struct{}),
	}, nil
}

// Record queues e for writing. It drops e and counts it when the queue is full or the Log is closed.
func (l *Log) Record(_ context.Context, e hub.AuditEntry) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		l.dropped.Add(1)
		return
	}
	select {
	case l.ch <- e:
	default:
		l.dropped.Add(1)
	}
}

// Dropped returns how many entries were dropped so far.
func (l *Log) Dropped() uint64 { return l.dropped.Load() }

// Close writes what is queued, closes the file and returns the first write error, if any.
func (l *Log) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		<-l.done
		return l.err
	}
	l.closed = true
	close(l.ch)
	l.mu.Unlock()
	<-l.done
	return l.err
}

func (l *Log) run() {
	defer close(l.done)
	for e := range l.ch {
		if err := l.write(e); err != nil {
			if l.err == nil {
				l.err = err
			}
			l.log.Error("audit write failed", "tool", e.Tool, "err", err)
		}
	}
	if l.file != nil {
		if err := l.file.Close(); err != nil && l.err == nil {
			l.err = err
		}
	}
}

func (l *Log) write(e hub.AuditEntry) error {
	at := e.At.UTC()
	b, err := json.Marshal(line{
		At:         at,
		Person:     e.Caller.Person,
		Via:        e.Caller.Via,
		Tool:       e.Tool,
		Args:       Redact(e.Args),
		OK:         e.OK,
		Error:      e.Error,
		DurationMS: e.Duration.Milliseconds(),
	})
	if err != nil {
		return err
	}
	if month := at.Format("2006-01"); month != l.month || l.file == nil {
		if l.file != nil {
			_ = l.file.Close()
			l.file = nil
		}
		name := filepath.Join(l.dir, "audit-"+month+".jsonl")
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) //nolint:gosec // name is built from our own dir and a date
		if err != nil {
			return err
		}
		l.file, l.month = f, month
	}
	_, err = l.file.Write(append(b, '\n'))
	return err
}
