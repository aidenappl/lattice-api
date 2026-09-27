package logger

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// Level represents log severity
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// slogLevel maps a Level onto the slog level every record is built with.
func (l Level) slogLevel() slog.Level {
	switch l {
	case LevelDebug:
		return slog.LevelDebug
	case LevelWarn:
		return slog.LevelWarn
	case LevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

var (
	// stdoutLevel is LOG_LEVEL: it gates what reaches stdout, and nothing else.
	stdoutLevel slog.LevelVar
	useJSON     atomic.Bool
	outLogger   = log.New(os.Stdout, "", 0)
)

func init() {
	// Until telemetry installs the Monitor chain — and for good in the one-off
	// subcommands, or when Monitor is misconfigured — log lines go to stdout only.
	SetHandler(StdoutHandler())
}

// Init configures the logger. Call at startup.
// level: "debug", "info", "warn", "error"
// format: "text" or "json"
func Init(level, format string) {
	switch strings.ToLower(level) {
	case "debug":
		stdoutLevel.Set(slog.LevelDebug)
	case "warn", "warning":
		stdoutLevel.Set(slog.LevelWarn)
	case "error":
		stdoutLevel.Set(slog.LevelError)
	default:
		stdoutLevel.Set(slog.LevelInfo)
	}
	useJSON.Store(strings.ToLower(format) == "json")
}

// SetOutput redirects stdout lines to w, for tests.
func SetOutput(w io.Writer) { outLogger.SetOutput(w) }

// SetHandler makes h the slog default, which the logger shims and direct
// slog.*Context calls both log through.
//
// slog.SetDefault also redirects the standard log package into h; that is undone
// here, so log.Printf output keeps its own format and does not become Monitor
// events until its call sites are converted deliberately.
func SetHandler(h slog.Handler) {
	w, flags := log.Writer(), log.Flags()
	slog.SetDefault(slog.New(h))
	log.SetOutput(w)
	log.SetFlags(flags)
}

// LevelName is the Monitor name of a slog level: debug, info, warn, error or
// fatal — the same mapping go-monitor's slog handler applies.
func LevelName(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "debug"
	case l < slog.LevelWarn:
		return "info"
	case l < slog.LevelError:
		return "warn"
	case l < slog.LevelError+4:
		return "error"
	default:
		return "fatal"
	}
}

// EventName is the Monitor event a log record without an explicit event is
// filed under: "<component>.log.<level>", with "app" for no component.
func EventName(component string, l slog.Level) string {
	if component == "" {
		component = "app"
	}
	return component + ".log." + LevelName(l)
}

func levelStr(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "DEBUG"
	case l < slog.LevelWarn:
		return "INFO"
	case l < slog.LevelError:
		return "WARN"
	default:
		return "ERROR"
	}
}

func levelColor(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "\033[36m" // cyan
	case l < slog.LevelWarn:
		return "\033[32m" // green
	case l < slog.LevelError:
		return "\033[33m" // yellow
	default:
		return "\033[31m" // red
	}
}

const resetColor = "\033[0m"

// field is one key/value of a stdout line, in output order.
type field struct {
	key string
	val any
}

// render writes one stdout line. It is the only writer of log lines, so the
// line format is the same whether the line came from slog or from Request/Panic.
func render(level slog.Level, ts time.Time, component, msg string, fields []field) {
	stamp := ts.UTC().Format("2006-01-02T15:04:05.000Z")

	if useJSON.Load() {
		// Structured JSON output
		parts := []string{
			fmt.Sprintf(`"ts":"%s"`, stamp),
			fmt.Sprintf(`"level":"%s"`, levelStr(level)),
		}
		if component != "" {
			parts = append(parts, fmt.Sprintf(`"component":"%s"`, component))
		}
		parts = append(parts, fmt.Sprintf(`"msg":"%s"`, escapeJSON(msg)))
		for _, f := range fields {
			parts = append(parts, fmt.Sprintf(`"%s":%s`, f.key, formatValue(f.val)))
		}
		outLogger.Printf("{%s}", strings.Join(parts, ","))
		return
	}

	// Human-readable colored output
	prefix := fmt.Sprintf("%s %s%-5s%s", stamp, levelColor(level), levelStr(level), resetColor)
	if component != "" {
		prefix += fmt.Sprintf(" [%s]", component)
	}
	if len(fields) > 0 {
		fieldParts := make([]string, 0, len(fields))
		for _, f := range fields {
			fieldParts = append(fieldParts, fmt.Sprintf("%s=%v", f.key, f.val))
		}
		outLogger.Printf("%s %s %s", prefix, msg, strings.Join(fieldParts, " "))
	} else {
		outLogger.Printf("%s %s", prefix, msg)
	}
}

// emit prints a line that does not go through slog (requests, panics), gated
// by LOG_LEVEL.
func emit(level Level, component, msg string, fields map[string]any) {
	l := level.slogLevel()
	if l < stdoutLevel.Level() {
		return
	}
	keys := sortedKeys(fields)
	out := make([]field, 0, len(keys))
	for _, k := range keys {
		out = append(out, field{k, fields[k]})
	}
	render(l, time.Now(), component, msg, out)
}

func escapeJSON(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return s
}

func formatValue(v any) string {
	switch val := v.(type) {
	case string:
		return fmt.Sprintf(`"%s"`, escapeJSON(val))
	case int, int64, float64, bool:
		return fmt.Sprintf("%v", val)
	default:
		return fmt.Sprintf(`"%v"`, escapeJSON(fmt.Sprint(val)))
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ─── stdout handler ──────────────────────────────────────────────────────────

// stdoutHandler is the slog.Handler that prints log lines in the format this
// service has always printed (see render), gated by LOG_LEVEL.
//
// The "component" attribute becomes the line's component. An "event" attribute
// equal to the name derived from component and level is omitted: telemetry adds
// it only so Monitor files the event under its usual name.
type stdoutHandler struct {
	pre    []field // WithAttrs, keys already group-qualified
	prefix string  // "group.subgroup." from WithGroup
}

// StdoutHandler returns the handler that prints log lines to stdout.
func StdoutHandler() slog.Handler { return &stdoutHandler{} }

func (h *stdoutHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= stdoutLevel.Level()
}

func (h *stdoutHandler) Handle(_ context.Context, r slog.Record) error {
	fields := make([]field, 0, len(h.pre)+r.NumAttrs())
	fields = append(fields, h.pre...)
	r.Attrs(func(a slog.Attr) bool {
		fields = appendAttr(fields, h.prefix, a)
		return true
	})

	component := ""
	for _, f := range fields {
		if f.key == "component" {
			component, _ = f.val.(string)
		}
	}
	derived := EventName(component, r.Level)
	out := fields[:0:0]
	for _, f := range fields {
		if f.key == "component" {
			continue
		}
		if f.key == h.prefix+"event" && f.val == derived {
			continue
		}
		out = append(out, f)
	}

	render(r.Level, r.Time, component, r.Message, out)
	return nil
}

func (h *stdoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	c := &stdoutHandler{prefix: h.prefix, pre: append([]field(nil), h.pre...)}
	for _, a := range attrs {
		c.pre = appendAttr(c.pre, h.prefix, a)
	}
	return c
}

func (h *stdoutHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &stdoutHandler{prefix: h.prefix + name + ".", pre: h.pre}
}

// appendAttr flattens a into fields under prefix; groups become dotted keys.
func appendAttr(fields []field, prefix string, a slog.Attr) []field {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return fields
	}
	if a.Value.Kind() == slog.KindGroup {
		p := prefix
		if a.Key != "" {
			p = prefix + a.Key + "."
		}
		for _, ga := range a.Value.Group() {
			fields = appendAttr(fields, p, ga)
		}
		return fields
	}
	return append(fields, field{prefix + a.Key, a.Value.Any()})
}

// ─── Panic sink ──────────────────────────────────────────────────────────────

// PanicRecord is one recovered panic, as handed to the panic sink.
type PanicRecord struct {
	Goroutine string
	Recovered any
	// Stack is captured while the panicking frames are still on the stack, so it
	// shows where the panic happened, not where it was reported.
	Stack  string
	Fields map[string]any
}

type panicSinkFunc func(context.Context, PanicRecord)

var panicSink atomic.Pointer[panicSinkFunc]

// SetPanicSink receives every panic reported through Panic, PanicContext and
// Recover. nil removes it.
func SetPanicSink(fn func(context.Context, PanicRecord)) {
	if fn == nil {
		panicSink.Store(nil)
		return
	}
	f := panicSinkFunc(fn)
	panicSink.Store(&f)
}

// ─── Public API ──────────────────────────────────────────────────────────────
//
// Each call becomes a slog record carrying the program counter of the line that
// logged — the "wrapping output methods" pattern from the log/slog docs — so
// Monitor's source_file/source_func/source_line name the caller, not this file.
// Every Debug/Info/Warn/Error/…Ctx function must call logAt directly: the
// caller depth is fixed.

func Debug(component, msg string, fields ...map[string]any) {
	logAt(context.Background(), LevelDebug, "", component, msg, fields)
}

func Info(component, msg string, fields ...map[string]any) {
	logAt(context.Background(), LevelInfo, "", component, msg, fields)
}

func Warn(component, msg string, fields ...map[string]any) {
	logAt(context.Background(), LevelWarn, "", component, msg, fields)
}

func Error(component, msg string, fields ...map[string]any) {
	logAt(context.Background(), LevelError, "", component, msg, fields)
}

// DebugCtx is Debug for code that has a context: its request_id, trace_id,
// user_id and job_id travel with the event.
func DebugCtx(ctx context.Context, component, msg string, fields ...map[string]any) {
	logAt(ctx, LevelDebug, "", component, msg, fields)
}

// InfoCtx is Info with the context's ids; see DebugCtx.
func InfoCtx(ctx context.Context, component, msg string, fields ...map[string]any) {
	logAt(ctx, LevelInfo, "", component, msg, fields)
}

// WarnCtx is Warn with the context's ids; see DebugCtx.
func WarnCtx(ctx context.Context, component, msg string, fields ...map[string]any) {
	logAt(ctx, LevelWarn, "", component, msg, fields)
}

// ErrorCtx is Error with the context's ids; see DebugCtx.
func ErrorCtx(ctx context.Context, component, msg string, fields ...map[string]any) {
	logAt(ctx, LevelError, "", component, msg, fields)
}

// EventCtx logs a lifecycle event under its own Monitor event name —
// "deployment.succeeded", "worker.connected" — instead of the
// "<component>.log.<level>" every other line is filed under. Use it for the few
// events worth alerting on or counting by name; everything else stays on
// DebugCtx/InfoCtx/WarnCtx/ErrorCtx.
//
// event is set as the record's slog "event" attribute deliberately. It is the
// only way to name the event: an "event" key in fields is still renamed to
// event_type, as for every other function here. An empty event falls back to
// the derived name.
func EventCtx(ctx context.Context, level Level, event, component, msg string, fields ...map[string]any) {
	logAt(ctx, level, event, component, msg, fields)
}

// logAt builds the record and hands it to the default slog handler. It must be
// called directly by the public functions above: runtime.Callers skips itself,
// logAt and the public function, leaving the line that logged.
//
// event, when not empty, names the Monitor event (see EventCtx).
func logAt(ctx context.Context, level Level, event, component, msg string, fields []map[string]any) {
	if ctx == nil {
		ctx = context.Background()
	}
	l := level.slogLevel()
	h := slog.Default().Handler()
	if !h.Enabled(ctx, l) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(3, pcs[:])
	r := slog.NewRecord(time.Now(), l, msg, pcs[0])

	merged := mergeFields(fields)
	r.AddAttrs(slog.String("component", component))
	if event != "" {
		r.AddAttrs(slog.String("event", event))
	}
	for _, k := range sortedKeys(merged) {
		v := merged[k]
		if e, ok := v.(error); ok && e != nil {
			// An error is sent as its text: that is what Monitor groups by.
			v = e.Error()
		}
		switch k {
		case "component":
			// The component argument names the line; a field cannot override it.
			continue
		case "event":
			// Defensive fallback only: "event" names the Monitor event for slog
			// records, so a logger field of that name must not rename it. Call
			// sites use explicit keys (webhook_event, lifecycle_event) instead.
			k = "event_type"
		}
		r.AddAttrs(slog.Any(k, v))
	}
	_ = h.Handle(ctx, r)
}

// ─── Panics ──────────────────────────────────────────────────────────────────

// Recover recovers a panic in the calling goroutine and reports it. It must be
// deferred directly —
//
//	defer logger.Recover("retention", logger.F{"table": t})
//
// — because recover only stops a panic when the deferred function itself calls
// it; wrapped in another closure this is a no-op. A panic on a goroutine nothing
// recovers kills the whole process, every worker connection with it.
func Recover(goroutine string, fields ...map[string]any) {
	if rec := recover(); rec != nil {
		PanicContext(context.Background(), goroutine, rec, fields...)
	}
}

// Panic reports a value the caller has already recovered, for code with its own
// recover that decides what happens next.
func Panic(goroutine string, recovered any, fields ...map[string]any) {
	PanicContext(context.Background(), goroutine, recovered, fields...)
}

// PanicContext is Panic for a panic that belongs to a request: the context's ids
// travel with the report.
func PanicContext(ctx context.Context, goroutine string, recovered any, fields ...map[string]any) {
	f := mergeFields(fields)
	if f == nil {
		f = F{}
	}
	stack := string(debug.Stack())

	if p := panicSink.Load(); p != nil {
		(*p)(ctx, PanicRecord{Goroutine: goroutine, Recovered: recovered, Stack: stack, Fields: f})
	}

	out := make(F, len(f)+2)
	for k, v := range f {
		out[k] = v
	}
	out["goroutine"] = goroutine
	out["stack"] = stack
	// Stdout only: the panic sink has already reported it to Monitor.
	emit(LevelError, "panic", fmt.Sprint(recovered), out)
}

// F is a convenience alias for map[string]any
type F = map[string]any

func mergeFields(fields []map[string]any) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	merged := make(map[string]any)
	for _, f := range fields {
		for k, v := range f {
			merged[k] = v
		}
	}
	return merged
}

// ─── HTTP request logging ────────────────────────────────────────────────────

// Request prints one request line to stdout. It is not sent to Monitor: the
// request middleware reports each request itself, with more than a log line
// can carry.
func Request(requestID, method, path string, status int, duration time.Duration) {
	level := LevelInfo
	if status >= 500 {
		level = LevelError
	} else if status >= 400 {
		level = LevelWarn
	}
	emit(level, "http", fmt.Sprintf("%s %s", method, path), F{
		"status":      status,
		"duration_ms": duration.Milliseconds(),
		"request_id":  requestID,
	})
}
