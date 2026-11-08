package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/structs"
)

// APL-120: one chatty container wrote ~6 GB of log lines a day into
// container_logs. Each container may store at most its stack's
// log_lines_per_minute lines per minute; the rest are dropped, and a marker row
// ("N log lines dropped") is stored in their place so the log view shows the
// gap. The live log view (the admin hub broadcast) still shows every line.

const logRateWindow = time.Minute

type logWindow struct {
	start   time.Time
	lines   int
	dropped int
	limit   int
	last    query.CreateContainerLogRequest // identity for the marker row
}

type logRateLimiter struct {
	mu      sync.Mutex
	windows map[string]*logWindow
}

func newLogRateLimiter() *logRateLimiter {
	return &logRateLimiter{windows: map[string]*logWindow{}}
}

// logRateKey identifies the container a line came from: its Lattice ID, else
// its name (unmanaged containers), else only the worker.
func logRateKey(r query.CreateContainerLogRequest) string {
	switch {
	case r.ContainerID != nil:
		return fmt.Sprintf("id:%d", *r.ContainerID)
	case r.ContainerName != nil:
		return fmt.Sprintf("name:%d:%s", r.WorkerID, *r.ContainerName)
	}
	return fmt.Sprintf("worker:%d", r.WorkerID)
}

// Allow reports whether r may be stored under limit lines per minute
// (structs.LOG_LINES_UNLIMITED: always). When r opens a new window after one
// that dropped lines, marker is that window's "dropped" row, to store before r.
func (l *logRateLimiter) Allow(r query.CreateContainerLogRequest, limit int, now time.Time) (ok bool, marker *query.CreateContainerLogRequest) {
	if limit == structs.LOG_LINES_UNLIMITED {
		return true, nil
	}
	key := logRateKey(r)
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.windows[key]
	if w == nil || now.Sub(w.start) >= logRateWindow {
		if w != nil && w.dropped > 0 {
			marker = droppedMarker(w)
		}
		w = &logWindow{start: now}
		l.windows[key] = w
	}
	w.limit = limit
	w.last = r
	if w.lines >= limit {
		w.dropped++
		return false, marker
	}
	w.lines++
	return true, marker
}

// Sweep closes every window older than a minute, returning the markers of
// those that dropped lines (a container that went quiet after a burst would
// otherwise never get one), and forgets them.
func (l *logRateLimiter) Sweep(now time.Time) []query.CreateContainerLogRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []query.CreateContainerLogRequest
	for key, w := range l.windows {
		if now.Sub(w.start) < logRateWindow {
			continue
		}
		if w.dropped > 0 {
			out = append(out, *droppedMarker(w))
		}
		delete(l.windows, key)
	}
	return out
}

// droppedMarker is the row stored in place of a window's dropped lines, stamped
// at the window's end so it sorts after the lines that were kept.
func droppedMarker(w *logWindow) *query.CreateContainerLogRequest {
	at := w.start.Add(logRateWindow).UTC()
	return &query.CreateContainerLogRequest{
		ContainerID:   w.last.ContainerID,
		ContainerName: w.last.ContainerName,
		WorkerID:      w.last.WorkerID,
		Stream:        "stderr",
		Message:       fmt.Sprintf("[lattice] %d log lines dropped: over this stack's limit of %d lines per minute (log_lines_per_minute)", w.dropped, w.limit),
		RecordedAt:    &at,
	}
}

// ── Per-stack limit lookup ───────────────────────────────────────────────

// stackLogLimitTTL bounds how stale a stack's limit may be on the log path,
// which must not query the stacks table per line.
const stackLogLimitTTL = time.Minute

type stackLimitEntry struct {
	limit   int
	fetched time.Time
}

type stackLogLimits struct {
	mu      sync.Mutex
	entries map[int]stackLimitEntry
	fetch   func(stackID int) (int, error)
}

func newStackLogLimits(fetch func(stackID int) (int, error)) *stackLogLimits {
	return &stackLogLimits{entries: map[int]stackLimitEntry{}, fetch: fetch}
}

// For is the lines-per-minute limit for a stack; the default for stackID 0
// (unmanaged containers) or when the stack can't be read.
func (s *stackLogLimits) For(stackID int, now time.Time) int {
	if stackID <= 0 {
		return structs.LOG_LINES_DEFAULT
	}
	s.mu.Lock()
	e, ok := s.entries[stackID]
	s.mu.Unlock()
	if ok && now.Sub(e.fetched) < stackLogLimitTTL {
		return e.limit
	}
	limit, err := s.fetch(stackID)
	if err != nil {
		if ok {
			return e.limit
		}
		return structs.LOG_LINES_DEFAULT
	}
	s.mu.Lock()
	s.entries[stackID] = stackLimitEntry{limit: limit, fetched: now}
	s.mu.Unlock()
	return limit
}

var (
	logLimiter       = newLogRateLimiter()
	stackLogLineCaps = newStackLogLimits(func(stackID int) (int, error) {
		st, err := query.GetStackByID(db.DB, stackID)
		if err != nil {
			return 0, err
		}
		if st == nil {
			return structs.LOG_LINES_DEFAULT, nil
		}
		return st.LogLinesPerMinute, nil
	})
)

// startLogRateSweeper stores the markers of windows that closed without a
// following line.
func startLogRateSweeper() {
	go func() {
		defer logger.Recover("log-rate-sweeper")
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for now := range ticker.C {
			for _, m := range logLimiter.Sweep(now) {
				containerLogs.Add(m)
			}
		}
	}()
}
