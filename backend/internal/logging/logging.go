// Package logging installs a dual-sink writer for the stdlib log
// package: every log.Printf line goes both to stderr (so the dev
// terminal still sees them, colorized via the caller's wrapper) and
// to a per-day file under <data dir>/logs/backend-YYYY-MM-DD.log.
//
// The file is the canonical artifact for bug reports — when a user
// reports a problem we ask them to zip <data dir>/logs/. Files older
// than RetainDays are pruned at Init() so the directory doesn't grow
// without bound.
//
// We deliberately keep using the stdlib `log` package rather than
// pulling in slog or zap: every existing call site is `log.Printf`
// with a `[facet] ...` prefix, and the styledLogWriter in
// backend/cmd/rl-toolkit already consumes that format. Wrapping
// log.Output with a teeing writer means zero call-site changes.
package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RetainDays is how many days of backend-*.log files to keep before
// pruning. Matches the launcher's logging module so all three streams
// (launcher / backend / sidecar) age out together.
const RetainDays = 14

const filePrefix = "backend-"

// Init opens (or creates) today's backend log under
// <dataDir>/logs/backend-YYYY-MM-DD.log and returns an io.Writer that
// fans writes out to both `extra` (typically the styled stderr writer)
// and the file. The returned closer should be called at shutdown to
// flush any buffered data.
//
// On failure to open the log file (read-only home, exotic FS), Init
// degrades gracefully: it returns `extra` unchanged and a no-op
// closer, with a single warning printed to extra. The app keeps
// working; we just lose the file half.
func Init(dataDir string, extra io.Writer) (io.Writer, func() error) {
	logsDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		fmt.Fprintf(extra, "[logging] cannot create %s: %v — file logging disabled\n", logsDir, err)
		return extra, noopClose
	}
	sweepOld(logsDir, extra)

	rl := &rotatingLog{
		dir:     logsDir,
		current: "",
	}
	if err := rl.openForToday(); err != nil {
		fmt.Fprintf(extra, "[logging] cannot open log file: %v — file logging disabled\n", err)
		return extra, noopClose
	}

	return io.MultiWriter(extra, rl), rl.Close
}

// rotatingLog is an io.WriteCloser that lazily rolls to a new file
// when the local-day stamp changes. Single-process, single-mutex —
// the backend writes from many goroutines but the volume is moderate
// (no per-frame logging) so contention isn't a concern.
type rotatingLog struct {
	mu      sync.Mutex
	dir     string
	current string
	file    *os.File
}

func (r *rotatingLog) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	stamp := todayStamp()
	if stamp != r.current {
		// Best-effort reopen. If it fails the previous file (if any)
		// keeps receiving writes until midnight — better than dropping.
		_ = r.openForTodayLocked()
	}
	if r.file == nil {
		// No file ever opened; pretend the write went through so the
		// MultiWriter doesn't error its other sinks.
		return len(p), nil
	}
	return r.file.Write(p)
}

func (r *rotatingLog) openForToday() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.openForTodayLocked()
}

// openForTodayLocked opens today's file, replacing any previously-open
// one. Caller must hold r.mu.
func (r *rotatingLog) openForTodayLocked() error {
	stamp := todayStamp()
	path := filepath.Join(r.dir, filePrefix+stamp+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if r.file != nil {
		_ = r.file.Close()
	}
	r.file = f
	r.current = stamp
	return nil
}

func (r *rotatingLog) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

func noopClose() error { return nil }

// sweepOld deletes backend-*.log files in `dir` whose mtime is older
// than RetainDays. Best-effort; failures are reported once to extra
// and don't block startup.
func sweepOld(dir string, extra io.Writer) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-time.Duration(RetainDays) * 24 * time.Hour)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, filePrefix) || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(dir, name)
			if err := os.Remove(path); err != nil {
				fmt.Fprintf(extra, "[logging] sweep: cannot remove %s: %v\n", path, err)
			}
		}
	}
}

// todayStamp returns YYYY-MM-DD in local time. Local (not UTC) to
// match the file timestamps users see in their file manager — when
// they're hunting for "the log from yesterday afternoon," the date in
// the filename should match their wall clock, not GMT.
func todayStamp() string {
	return time.Now().Format("2006-01-02")
}
