package plugins

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	logRingLines = 1000
	logMaxLine   = 8 << 10 // longer stderr lines are truncated
	logFileMax   = 5 << 20 // rotate the log file at 5 MB
	logFileKeep  = 3       // <id>.log, <id>.log.1, <id>.log.2
)

// logSink keeps a plugin's stderr: the last lines in memory and everything in a rotated file.
type logSink struct {
	path    string
	log     *slog.Logger
	maxSize int64

	mu      sync.Mutex
	ring    []string
	next    int // ring write position once full
	file    *os.File
	size    int64
	failed  bool          // a file error was already reported
	updated chan struct{} // closed at the next line (tests wait on it)
}

func newLogSink(path string, log *slog.Logger) *logSink {
	return &logSink{path: path, log: log, maxSize: logFileMax, updated: make(chan struct{})}
}

// copy reads r line by line until EOF or a read error.
func (s *logSink) copy(r io.Reader) {
	br := bufio.NewReaderSize(r, logMaxLine)
	for {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			s.add(string(line) + " [truncated]")
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = br.ReadSlice('\n')
			}
			if err != nil {
				return
			}
			continue
		}
		if text := strings.TrimRight(string(line), "\r\n"); text != "" || err == nil {
			s.add(text)
		}
		if err != nil {
			return
		}
	}
}

// event records a line from the host itself.
func (s *logSink) event(format string, args ...any) {
	s.add(fmt.Sprintf("homie: %s %s", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...)))
}

func (s *logSink) add(text string) {
	s.log.Debug("plugin stderr", "line", text)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ring) < logRingLines {
		s.ring = append(s.ring, text)
	} else {
		s.ring[s.next] = text
		s.next = (s.next + 1) % logRingLines
	}
	if err := s.writeLocked(text + "\n"); err != nil && !s.failed {
		s.failed = true
		s.log.Warn("cannot write the plugin log file", "path", s.path, "err", err)
	}
	close(s.updated)
	s.updated = make(chan struct{})
}

func (s *logSink) writeLocked(text string) error {
	if s.file != nil && s.size+int64(len(text)) > s.maxSize {
		_ = s.file.Close()
		s.file = nil
		for i := logFileKeep - 1; i > 0; i-- {
			from := s.path
			if i > 1 {
				from = fmt.Sprintf("%s.%d", s.path, i-1)
			}
			if err := os.Rename(from, fmt.Sprintf("%s.%d", s.path, i)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	if s.file == nil {
		f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		fi, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return err
		}
		s.file, s.size = f, fi.Size()
	}
	n, err := s.file.WriteString(text)
	s.size += int64(n)
	return err
}

// lines returns the kept lines, oldest first.
func (s *logSink) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.ring))
	out = append(out, s.ring[s.next:]...)
	return append(out, s.ring[:s.next]...)
}

// changed returns a channel closed at the next line.
func (s *logSink) changed() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updated
}

func (s *logSink) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}
