package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// captureHandler records every record it is handed, with the attributes
// accumulated through WithAttrs.
type captureHandler struct {
	mu      *sync.Mutex
	records *[]captured
	pre     []slog.Attr
}

type captured struct {
	level slog.Level
	msg   string
	pc    uintptr
	attrs map[string]any
}

func newCapture() *captureHandler {
	return &captureHandler{mu: &sync.Mutex{}, records: &[]captured{}}
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	c := captured{level: r.Level, msg: r.Message, pc: r.PC, attrs: map[string]any{}}
	for _, a := range h.pre {
		c.attrs[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool { c.attrs[a.Key] = a.Value.Any(); return true })
	h.mu.Lock()
	*h.records = append(*h.records, c)
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.pre = append(append([]slog.Attr(nil), h.pre...), attrs...)
	return &c
}

func (h *captureHandler) WithGroup(string) slog.Handler { return h }

func (h *captureHandler) all() []captured {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]captured(nil), *h.records...)
}

func TestComponentHandlerEventNames(t *testing.T) {
	tests := []struct {
		name      string
		with      []any // WithAttrs
		level     slog.Level
		args      []any // record attrs
		wantEvent string
	}{
		{name: "no component falls back to app", level: slog.LevelInfo, wantEvent: "app.log.info"},
		{name: "component from the record", level: slog.LevelError, args: []any{"component", "worker"}, wantEvent: "worker.log.error"},
		{name: "component from WithAttrs", with: []any{"component", "socket"}, level: slog.LevelWarn, wantEvent: "socket.log.warn"},
		{name: "record component overrides WithAttrs", with: []any{"component", "socket"}, level: slog.LevelDebug, args: []any{"component", "hub"}, wantEvent: "hub.log.debug"},
		{name: "explicit event on the record is kept", level: slog.LevelError, args: []any{"component", "deploy", "event", "deployment.failed"}, wantEvent: "deployment.failed"},
		{name: "explicit event from WithAttrs is kept", with: []any{"event", "stack.deploy.success"}, level: slog.LevelInfo, wantEvent: "stack.deploy.success"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := newCapture()
			log := slog.New(newComponentHandler(nil, capture, DEFAULT_WARN_LIMIT, time.Minute))
			if tt.with != nil {
				log = log.With(tt.with...)
			}
			log.Log(context.Background(), tt.level, "something happened", tt.args...)

			got := capture.all()
			if len(got) != 1 {
				t.Fatalf("got %d records, want 1", len(got))
			}
			if got[0].attrs["event"] != tt.wantEvent {
				t.Errorf("event = %v, want %q", got[0].attrs["event"], tt.wantEvent)
			}
			if got[0].pc == 0 {
				t.Error("the record's PC was lost")
			}
		})
	}
}

func TestComponentHandlerPreservesPC(t *testing.T) {
	capture := newCapture()
	h := newComponentHandler(nil, capture, DEFAULT_WARN_LIMIT, time.Minute)
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "m", 12345)
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if got := capture.all(); len(got) != 1 || got[0].pc != 12345 {
		t.Errorf("records = %+v, want PC 12345", got)
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestComponentHandlerLimitsRepeatedWarnings(t *testing.T) {
	tests := []struct {
		name  string
		level slog.Level
		sends int
		want  int
	}{
		{name: "warnings past the limit are dropped", level: slog.LevelWarn, sends: 10, want: 3},
		{name: "errors are never limited", level: slog.LevelError, sends: 10, want: 10},
		{name: "fatal is never limited", level: slog.LevelError + 4, sends: 10, want: 10},
		{name: "info is not limited", level: slog.LevelInfo, sends: 10, want: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := newCapture()
			h := newComponentHandler(nil, capture, 3, time.Minute)
			log := slog.New(h)
			for range tt.sends {
				log.Log(context.Background(), tt.level, "send queue full", "component", "socket")
			}
			if n := len(capture.all()); n != tt.want {
				t.Errorf("passed %d records, want %d", n, tt.want)
			}
		})
	}
}

func TestComponentHandlerReportsSuppressedCount(t *testing.T) {
	capture := newCapture()
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	h := newComponentHandler(nil, capture, 2, time.Minute)
	h.limiter.now = clock.now
	log := slog.New(h).With("component", "socket")

	for range 5 {
		log.Warn("send queue full")
	}
	// A different message or component is its own key.
	log.Warn("write failed")
	slog.New(h).Warn("send queue full", "component", "hub")

	clock.advance(time.Minute)
	log.Warn("send queue full")
	log.Warn("send queue full")

	var sqf []captured
	for _, c := range capture.all() {
		if c.msg == "send queue full" && c.attrs["component"] == "socket" {
			sqf = append(sqf, c)
		}
	}
	if len(sqf) != 4 {
		t.Fatalf("passed %d socket 'send queue full' records, want 4 (2 per window)", len(sqf))
	}
	for i, c := range sqf {
		got, has := c.attrs["suppressed"]
		switch i {
		case 2:
			if got != int64(3) {
				t.Errorf("first record of the new window: suppressed = %v, want 3", got)
			}
		default:
			if has {
				t.Errorf("record %d carries suppressed=%v; only the first after a suppressing window should", i, got)
			}
		}
	}
	if n := len(capture.all()); n != 6 {
		t.Errorf("passed %d records in all, want 6", n)
	}
}

func TestComponentHandlerIsSafeForConcurrentUse(t *testing.T) {
	capture := newCapture()
	h := newComponentHandler(nil, capture, 5, time.Minute)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			log := slog.New(h).With("component", "c").WithGroup("g")
			for range 50 {
				log.Warn("same warning")
				log.Error("same error", "i", i)
			}
		}()
	}
	wg.Wait()
	warns, errs := 0, 0
	for _, c := range capture.all() {
		if c.level == slog.LevelWarn {
			warns++
		} else {
			errs++
		}
	}
	if warns != 5 || errs != 400 {
		t.Errorf("warns = %d (want 5), errors = %d (want 400)", warns, errs)
	}
}

func TestComponentHandlerEnabledIsEitherBranch(t *testing.T) {
	out := slog.NewTextHandler(nil, &slog.HandlerOptions{Level: slog.LevelWarn})
	mon := slog.NewTextHandler(nil, &slog.HandlerOptions{Level: slog.LevelError})
	h := newComponentHandler(out, mon, DEFAULT_WARN_LIMIT, time.Minute)
	if h.Enabled(context.Background(), slog.LevelInfo) || !h.Enabled(context.Background(), slog.LevelWarn) {
		t.Error("Enabled should be true when either branch is enabled, and only then")
	}
}

// The limiter and the derived attributes are Monitor's: stdout gets every
// record, unchanged.
func TestSuppressedWarningsStillReachStdout(t *testing.T) {
	out, mon := newCapture(), newCapture()
	log := slog.New(newComponentHandler(out, mon, 3, time.Minute)).With("component", "socket")
	for range 10 {
		log.Warn("send queue full")
	}

	if n := len(mon.all()); n != 3 {
		t.Errorf("Monitor got %d records, want 3", n)
	}
	got := out.all()
	if len(got) != 10 {
		t.Fatalf("stdout got %d records, want all 10", len(got))
	}
	for i, c := range got {
		for _, k := range []string{"event", "suppressed", "version"} {
			if v, ok := c.attrs[k]; ok {
				t.Errorf("stdout record %d carries %s=%v", i, k, v)
			}
		}
		if c.pc == 0 {
			t.Errorf("stdout record %d lost its PC", i)
		}
	}
}

func TestLimiterSweepClearsWhenNothingCanBeEvicted(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := newWarnLimiter(1, time.Minute, clock.now)
	// Fill the map with keys that all have suppressions pending, so the
	// ordinary sweep can evict none of them.
	for i := range maxLimiterKeys {
		msg := fmt.Sprint(i)
		l.allow(slog.LevelWarn, "c", msg)
		l.allow(slog.LevelWarn, "c", msg)
	}
	if len(l.buckets) != maxLimiterKeys {
		t.Fatalf("buckets = %d, want %d", len(l.buckets), maxLimiterKeys)
	}
	clock.advance(time.Minute)
	if ok, _ := l.allow(slog.LevelWarn, "c", "new"); !ok {
		t.Fatal("a new key was not allowed")
	}
	if n := len(l.buckets); n != 1 {
		t.Errorf("after the fallback sweep buckets = %d, want 1", n)
	}
}
