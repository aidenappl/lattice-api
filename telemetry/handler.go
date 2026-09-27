package telemetry

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/aidenappl/lattice-api/logger"
)

const (
	// DEFAULT_WARN_LIMIT is how many identical warnings pass per window.
	DEFAULT_WARN_LIMIT = 20
	// DEFAULT_WARN_WINDOW is the length of one rate-limit window.
	DEFAULT_WARN_WINDOW = time.Minute
	// maxLimiterKeys bounds the limiter's memory; see warnLimiter.sweep.
	maxLimiterKeys = 10000
)

// componentHandler fans every record out to two handlers.
//
// out (stdout) gets the record exactly as logged: no derived event, no
// suppressed count, no version, and LOG_LEVEL is its only gate.
//
// mon (go-monitor's slog handler, with no next of its own) gets a copy that
// keeps Monitor's event names what they were before slog:
//
//   - A record with no "event" attribute is filed as "<component>.log.<level>",
//     component coming from a "component" attribute (WithAttrs or the record's
//     own) and defaulting to "app".
//   - Repeated warnings — same level, component and message — reach Monitor at
//     most limit times per window. The first one through after a window with
//     suppressions carries suppressed=<count>. Errors and above are never
//     limited, and stdout is never limited.
//
// Each record's PC is passed on untouched, so the source of every event is the
// line that logged.
type componentHandler struct {
	out       slog.Handler
	mon       slog.Handler
	component string // from WithAttrs; "" when none
	hasEvent  bool   // an "event" attribute came from WithAttrs
	limiter   *warnLimiter
}

func newComponentHandler(out, mon slog.Handler, limit int, window time.Duration) *componentHandler {
	return &componentHandler{out: out, mon: mon, limiter: newWarnLimiter(limit, window, time.Now)}
}

func (h *componentHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return (h.out != nil && h.out.Enabled(ctx, l)) || h.mon.Enabled(ctx, l)
}

func (h *componentHandler) Handle(ctx context.Context, r slog.Record) error {
	var err error
	if h.out != nil && h.out.Enabled(ctx, r.Level) {
		err = h.out.Handle(ctx, r)
	}
	if !h.mon.Enabled(ctx, r.Level) {
		return err
	}

	component, hasEvent := h.component, h.hasEvent
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "component":
			if s, ok := a.Value.Resolve().Any().(string); ok {
				component = s
			}
		case "event":
			hasEvent = true
		}
		return true
	})

	var suppressed int
	if r.Level >= slog.LevelWarn && r.Level < slog.LevelError {
		var ok bool
		if ok, suppressed = h.limiter.allow(r.Level, component, r.Message); !ok {
			return err
		}
	}

	if !hasEvent || suppressed > 0 {
		r = r.Clone()
		if !hasEvent {
			r.AddAttrs(slog.String("event", logger.EventName(component, r.Level)))
		}
		if suppressed > 0 {
			r.AddAttrs(slog.Int("suppressed", suppressed))
		}
	}
	if merr := h.mon.Handle(ctx, r); err == nil {
		err = merr
	}
	return err
}

func (h *componentHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	c := *h
	for _, a := range attrs {
		switch a.Key {
		case "component":
			if s, ok := a.Value.Resolve().Any().(string); ok {
				c.component = s
			}
		case "event":
			c.hasEvent = true
		}
	}
	if h.out != nil {
		c.out = h.out.WithAttrs(attrs)
	}
	c.mon = h.mon.WithAttrs(attrs)
	return &c
}

func (h *componentHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	if h.out != nil {
		c.out = h.out.WithGroup(name)
	}
	c.mon = h.mon.WithGroup(name)
	return &c
}

// warnLimiter counts records per key in fixed windows. It is shared by every
// handler derived from one componentHandler.
type warnLimiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	buckets map[limiterKey]*bucket
}

type limiterKey struct {
	level     slog.Level
	component string
	message   string
}

type bucket struct {
	start      time.Time
	count      int
	suppressed int
}

func newWarnLimiter(limit int, window time.Duration, now func() time.Time) *warnLimiter {
	if limit <= 0 {
		limit = DEFAULT_WARN_LIMIT
	}
	if window <= 0 {
		window = DEFAULT_WARN_WINDOW
	}
	return &warnLimiter{limit: limit, window: window, now: now, buckets: map[limiterKey]*bucket{}}
}

// allow reports whether a record may pass and, when it opens a new window after
// suppressions, how many records the previous window dropped.
func (l *warnLimiter) allow(level slog.Level, component, message string) (bool, int) {
	now := l.now()
	k := limiterKey{level, component, message}

	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.buckets[k]
	if b == nil {
		if len(l.buckets) >= maxLimiterKeys {
			l.sweep(now)
		}
		b = &bucket{start: now}
		l.buckets[k] = b
	}

	var flushed int
	if now.Sub(b.start) >= l.window {
		flushed = b.suppressed
		*b = bucket{start: now}
	}
	if b.count >= l.limit {
		b.suppressed++
		return false, 0
	}
	b.count++
	return true, flushed
}

// sweep drops keys whose window has ended with nothing left to report and, if
// that frees nothing, every key: forgetting a count beats running this O(n)
// sweep on every new key once the map is full.
func (l *warnLimiter) sweep(now time.Time) {
	for k, b := range l.buckets {
		if b.suppressed == 0 && now.Sub(b.start) >= l.window {
			delete(l.buckets, k)
		}
	}
	if len(l.buckets) >= maxLimiterKeys {
		clear(l.buckets)
	}
}
