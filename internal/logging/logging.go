// Package logging owns only where log output is written.
package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

const dateLayout = "20060102"

var filePattern = regexp.MustCompile(`^rinexclient_(\d{8})\.log$`)

func fileName(date string) string {
	return "rinexclient_" + date + ".log"
}

// RotatingWriter appends logs to one file per local calendar day.
// Its mutex serializes goroutines in this process; O_APPEND prevents separate
// scheduled runs from truncating an existing daily file.
type RotatingWriter struct {
	mu            sync.Mutex
	dir           string
	retentionDays int

	file       *os.File
	openedDate string

	writeFailed      bool
	rotateWarnedDate string

	// Test hooks. Production uses the defaults installed by the constructor.
	nowFn   func() time.Time
	openFn  func(name string) (*os.File, error)
	writeFn func(f *os.File, p []byte) (int, error)
	warnOut io.Writer
}

// NewRotatingWriter creates dir, opens today's file immediately, and performs
// a best-effort retention cleanup. Opening immediately lets the caller decide
// whether to fall back to stderr before normal work starts.
func NewRotatingWriter(dir string, retentionDays int) (*RotatingWriter, error) {
	return newRotatingWriter(dir, retentionDays, nil)
}

type hooks struct {
	nowFn   func() time.Time
	openFn  func(name string) (*os.File, error)
	writeFn func(f *os.File, p []byte) (int, error)
	warnOut io.Writer
}

func defaultOpen(name string) (*os.File, error) {
	return os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}

func newRotatingWriter(
	dir string,
	retentionDays int,
	h *hooks,
) (*RotatingWriter, error) {
	if retentionDays < 1 {
		return nil, fmt.Errorf(
			"logging: retentionDays = %d, must be at least 1",
			retentionDays,
		)
	}

	w := &RotatingWriter{
		dir:           dir,
		retentionDays: retentionDays,
		nowFn:         time.Now,
		openFn:        defaultOpen,
		writeFn:       (*os.File).Write,
		warnOut:       os.Stderr,
	}
	if h != nil {
		if h.nowFn != nil {
			w.nowFn = h.nowFn
		}
		if h.openFn != nil {
			w.openFn = h.openFn
		}
		if h.writeFn != nil {
			w.writeFn = h.writeFn
		}
		if h.warnOut != nil {
			w.warnOut = h.warnOut
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("logging: create log dir: %w", err)
	}

	now := w.nowFn()
	today := now.Format(dateLayout)
	f, err := w.openFn(filepath.Join(dir, fileName(today)))
	if err != nil {
		return nil, fmt.Errorf("logging: open log file: %w", err)
	}
	w.file = f
	w.openedDate = today
	w.cleanup(now)

	return w, nil
}

// Write rotates at the first write of a new local day. After initialization,
// file failures never stop the transfer: stderr has already received the line
// because main places it first in io.MultiWriter.
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := w.nowFn()
	today := now.Format(dateLayout)
	if today != w.openedDate {
		w.rotate(now, today)
	}

	if w.file == nil {
		// Close is for tests and tools. The main program intentionally leaves the
		// descriptor alive so main's final [FATAL] can still reach the file.
		return len(p), nil
	}

	n, err := w.writeFn(w.file, p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		if !w.writeFailed {
			w.writeFailed = true
			fmt.Fprintf(
				w.warnOut,
				"[LOG][WARN] file logging failed; continuing with stderr only: %v\n",
				err,
			)
		}

		return len(p), nil
	}

	if w.writeFailed {
		w.writeFailed = false
		fmt.Fprintln(w.warnOut, "[LOG][INFO] file logging resumed")
	}

	return len(p), nil
}

// rotate switches files only after the new file opens successfully. On
// failure the previous file remains active and the next Write retries.
// The caller must hold w.mu.
func (w *RotatingWriter) rotate(now time.Time, today string) {
	f, err := w.openFn(filepath.Join(w.dir, fileName(today)))
	if err != nil {
		if w.rotateWarnedDate != today {
			w.rotateWarnedDate = today
			fmt.Fprintf(
				w.warnOut,
				"[LOG][WARN] rotate to %s failed; keep writing previous file: %v\n",
				fileName(today),
				err,
			)
		}
		return
	}

	if w.file != nil {
		if err := w.file.Close(); err != nil {
			fmt.Fprintf(w.warnOut, "[LOG][WARN] close previous log file: %v\n", err)
		}
	}

	w.file = f
	w.openedDate = today
	w.rotateWarnedDate = ""
	w.cleanup(now)
}

// cleanup deletes only regular files produced by this logger whose filename
// date is older than the retention window. It intentionally ignores mtime.
// The caller must hold w.mu, except during construction before publication.
func (w *RotatingWriter) cleanup(now time.Time) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		fmt.Fprintf(w.warnOut, "[LOG][WARN] retention scan: %v\n", err)
		return
	}

	year, month, day := now.Date()
	today := time.Date(year, month, day, 0, 0, 0, 0, now.Location())
	oldest := today.AddDate(0, 0, -(w.retentionDays - 1))

	for _, entry := range entries {
		match := filePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			fmt.Fprintf(
				w.warnOut,
				"[LOG][WARN] retention inspect %s: %v\n",
				entry.Name(),
				err,
			)
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}

		fileDate, err := time.ParseInLocation(
			dateLayout,
			match[1],
			now.Location(),
		)
		if err != nil || !fileDate.Before(oldest) {
			continue
		}

		if err := os.Remove(filepath.Join(w.dir, entry.Name())); err != nil {
			fmt.Fprintf(
				w.warnOut,
				"[LOG][WARN] retention delete %s: %v\n",
				entry.Name(),
				err,
			)
		}
	}
}

// Close is intended for tests and tools. The main program relies on process
// exit so the final [FATAL], logged after run returns, is not lost.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}

	err := w.file.Close()
	w.file = nil
	return err
}
