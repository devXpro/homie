package tunnel

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Log rotation of cloudflared.log, like the plugins' logs: at 5 MB, keeping cloudflared.log, .1 and .2.
const (
	childLogMax  = 5 << 20
	childLogKeep = 3
)

// ChildLog is cloudflared's output in a file of the data dir (0600), rotated like the plugins' logs. It is safe for
// concurrent use; a file error is reported once to onError and then ignored, so the tunnel never stops for its log.
type ChildLog struct {
	path    string
	maxSize int64
	onError func(error)

	mu     sync.Mutex
	file   *os.File
	size   int64
	failed bool
}

// NewChildLog returns the log at path; its directory is made 0700 when missing. onError may be nil.
func NewChildLog(path string, onError func(error)) *ChildLog {
	if onError == nil {
		onError = func(error) {}
	}
	return &ChildLog{path: path, maxSize: childLogMax, onError: onError}
}

// Write appends p, rotating first when the file is full.
func (l *ChildLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.writeLocked(p); err != nil && !l.failed {
		l.failed = true
		l.onError(err)
	}
	return len(p), nil
}

func (l *ChildLog) writeLocked(p []byte) error {
	if l.file != nil && l.size+int64(len(p)) > l.maxSize {
		_ = l.file.Close()
		l.file = nil
		for i := childLogKeep - 1; i > 0; i-- {
			from := l.path
			if i > 1 {
				from = fmt.Sprintf("%s.%d", l.path, i-1)
			}
			_ = os.Rename(from, fmt.Sprintf("%s.%d", l.path, i))
		}
	}
	if l.file == nil {
		if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			return err
		}
		fi, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return err
		}
		l.file, l.size = f, fi.Size()
	}
	n, err := l.file.Write(p)
	l.size += int64(n)
	return err
}

// Close closes the file.
func (l *ChildLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
