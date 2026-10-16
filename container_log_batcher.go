package main

import (
	"context"
	"sync"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
)

// Container log lines arrive one WebSocket message each. Storing every line as
// its own INSERT meant one transaction and one disk sync per line — most of the
// database's ~10 commits/s, each 0.4–1.4 s on production's virtual disk, while
// holding a msgSem slot. The batcher collects lines and writes them as one
// multi-row INSERT.
//
// The live log view is fed by the admin-hub broadcast, not the database, so the
// short delay before a line is stored doesn't show up there.
const (
	logBatchMaxRows  = 200
	logBatchMaxBytes = 1 << 20 // well under max_allowed_packet (16 MiB)
	logBatchInterval = 250 * time.Millisecond
	logBatchQueue    = 10000
)

var containerLogs = newContainerLogBatcher(
	func(batch []query.CreateContainerLogRequest) error { return query.CreateContainerLogs(db.DB, batch) },
	func(r query.CreateContainerLogRequest) error { return query.CreateContainerLog(db.DB, r) },
	logBatchMaxRows, logBatchMaxBytes, logBatchInterval, logBatchQueue,
)

type containerLogBatcher struct {
	in       chan query.CreateContainerLogRequest
	done     chan struct{}
	flush    func([]query.CreateContainerLogRequest) error
	single   func(query.CreateContainerLogRequest) error
	maxRows  int
	maxBytes int
	interval time.Duration

	mu      sync.RWMutex // guards closed against Add racing Close
	closed  bool
	started sync.Once
}

func newContainerLogBatcher(
	flush func([]query.CreateContainerLogRequest) error,
	single func(query.CreateContainerLogRequest) error,
	maxRows, maxBytes int, interval time.Duration, queue int,
) *containerLogBatcher {
	return &containerLogBatcher{
		in:       make(chan query.CreateContainerLogRequest, queue),
		done:     make(chan struct{}),
		flush:    flush,
		single:   single,
		maxRows:  maxRows,
		maxBytes: maxBytes,
		interval: interval,
	}
}

// Start launches the writer goroutine. Safe to call more than once.
func (b *containerLogBatcher) Start() {
	b.started.Do(func() { go b.run() })
}

// Add queues a line without blocking. If the queue is full or the batcher is
// closed, the line is stored directly instead of being dropped.
func (b *containerLogBatcher) Add(r query.CreateContainerLogRequest) {
	b.mu.RLock()
	if !b.closed {
		select {
		case b.in <- r:
			b.mu.RUnlock()
			return
		default:
		}
	}
	b.mu.RUnlock()
	b.storeOne(r)
}

// Close stops accepting lines, writes everything queued, and waits for that
// (or ctx) to finish.
func (b *containerLogBatcher) Close(ctx context.Context) {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		close(b.in)
	}
	b.mu.Unlock()
	b.Start() // so a never-started batcher still drains
	select {
	case <-b.done:
	case <-ctx.Done():
		logger.WarnCtx(ctx, "container", "log batcher did not drain before shutdown", logger.F{"queued": len(b.in)})
	}
}

func (b *containerLogBatcher) run() {
	defer close(b.done)
	defer logger.Recover("container-log-batcher")

	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()

	batch := make([]query.CreateContainerLogRequest, 0, b.maxRows)
	size := 0
	write := func() {
		if len(batch) == 0 {
			return
		}
		b.write(batch)
		batch = make([]query.CreateContainerLogRequest, 0, b.maxRows)
		size = 0
	}

	for {
		select {
		case r, ok := <-b.in:
			if !ok {
				write()
				return
			}
			batch = append(batch, r)
			size += len(r.Message)
			if len(batch) >= b.maxRows || size >= b.maxBytes {
				write()
			}
		case <-ticker.C:
			write()
		}
	}
}

// write stores a batch. If the batch insert fails (one oversized or invalid
// row fails the whole statement), it falls back to row by row so the other
// lines still land and the bad one is reported on its own.
func (b *containerLogBatcher) write(batch []query.CreateContainerLogRequest) {
	err := b.flush(batch)
	if err == nil {
		return
	}
	logger.WarnCtx(context.Background(), "container", "batched log insert failed; retrying row by row", logger.F{"rows": len(batch), "error": err})
	for _, r := range batch {
		b.storeOne(r)
	}
}

func (b *containerLogBatcher) storeOne(r query.CreateContainerLogRequest) {
	if err := b.single(r); err != nil {
		logger.ErrorCtx(context.Background(), "container", "failed to store container log", logger.F{"worker_id": r.WorkerID, "error": err})
	}
}
