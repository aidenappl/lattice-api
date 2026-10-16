package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aidenappl/lattice-api/query"
)

// recorder captures what a batcher writes, in place of the database.
type recorder struct {
	mu       sync.Mutex
	batches  [][]string
	singles  []string
	failNext bool
}

func (r *recorder) flush(b []query.CreateContainerLogRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNext {
		r.failNext = false
		return errors.New("batch rejected")
	}
	msgs := make([]string, len(b))
	for i, x := range b {
		msgs[i] = x.Message
	}
	r.batches = append(r.batches, msgs)
	return nil
}

func (r *recorder) single(x query.CreateContainerLogRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.singles = append(r.singles, x.Message)
	return nil
}

func (r *recorder) snapshot() ([][]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.batches...), append([]string(nil), r.singles...)
}

func line(msg string) query.CreateContainerLogRequest {
	return query.CreateContainerLogRequest{WorkerID: 1, Stream: "stdout", Message: msg}
}

func TestContainerLogBatcher(t *testing.T) {
	tests := []struct {
		name        string
		maxRows     int
		maxBytes    int
		interval    time.Duration
		lines       []string
		failFirst   bool
		wantBatches [][]string
		wantSingles []string
	}{
		{
			name:    "row limit flushes a full batch, close drains the rest",
			maxRows: 2, maxBytes: 1 << 20, interval: time.Hour,
			lines:       []string{"a", "b", "c"},
			wantBatches: [][]string{{"a", "b"}, {"c"}},
		},
		{
			name:    "byte limit flushes before the row limit",
			maxRows: 100, maxBytes: 5, interval: time.Hour,
			lines:       []string{"abc", "def", "g"},
			wantBatches: [][]string{{"abc", "def"}, {"g"}},
		},
		{
			name:    "a failed batch is retried row by row",
			maxRows: 100, maxBytes: 1 << 20, interval: time.Hour,
			lines:       []string{"x", "y"},
			failFirst:   true,
			wantSingles: []string{"x", "y"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{failNext: tt.failFirst}
			b := newContainerLogBatcher(rec.flush, rec.single, tt.maxRows, tt.maxBytes, tt.interval, 100)
			b.Start()
			for _, l := range tt.lines {
				b.Add(line(l))
			}
			b.Close(context.Background())

			batches, singles := rec.snapshot()
			if strings.Join(flatten(batches), ",") != strings.Join(flatten(tt.wantBatches), ",") || len(batches) != len(tt.wantBatches) {
				t.Fatalf("batches = %v, want %v", batches, tt.wantBatches)
			}
			if strings.Join(singles, ",") != strings.Join(tt.wantSingles, ",") {
				t.Fatalf("singles = %v, want %v", singles, tt.wantSingles)
			}
		})
	}
}

func TestContainerLogBatcherFlushesOnInterval(t *testing.T) {
	rec := &recorder{}
	b := newContainerLogBatcher(rec.flush, rec.single, 100, 1<<20, 20*time.Millisecond, 100)
	b.Start()
	defer b.Close(context.Background())

	b.Add(line("tick"))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if batches, _ := rec.snapshot(); len(batches) == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("line was not written by the interval flush")
}

func TestContainerLogBatcherStoresDirectlyWhenClosedOrFull(t *testing.T) {
	rec := &recorder{}
	// Not started and a queue of 1: the second line finds the queue full.
	b := newContainerLogBatcher(rec.flush, rec.single, 100, 1<<20, time.Hour, 1)
	b.Add(line("queued"))
	b.Add(line("overflow"))
	b.Close(context.Background())
	b.Add(line("after-close"))

	batches, singles := rec.snapshot()
	if got := strings.Join(flatten(batches), ","); got != "queued" {
		t.Fatalf("batched = %q, want %q", got, "queued")
	}
	if got := strings.Join(singles, ","); got != "overflow,after-close" {
		t.Fatalf("stored directly = %q, want %q", got, "overflow,after-close")
	}
}

func TestContainerLogBatcherConcurrentAddAndClose(t *testing.T) {
	rec := &recorder{}
	b := newContainerLogBatcher(rec.flush, rec.single, 10, 1<<20, time.Millisecond, 50)
	b.Start()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				b.Add(line("m"))
			}
		}()
	}
	time.Sleep(2 * time.Millisecond)
	b.Close(context.Background())
	wg.Wait()

	batches, singles := rec.snapshot()
	if got := len(flatten(batches)) + len(singles); got != 1000 {
		t.Fatalf("stored %d lines, want 1000 (none lost)", got)
	}
}

func flatten(b [][]string) []string {
	var out []string
	for _, x := range b {
		out = append(out, x...)
	}
	return out
}
