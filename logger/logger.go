package logger

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
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

var (
	minLevel  = LevelInfo
	useJSON   = false
	outLogger = log.New(os.Stdout, "", 0)
)

// Init configures the logger. Call at startup.
// level: "debug", "info", "warn", "error"
// format: "text" or "json"
func Init(level, format string) {
	switch strings.ToLower(level) {
	case "debug":
		minLevel = LevelDebug
	case "warn", "warning":
		minLevel = LevelWarn
	case "error":
		minLevel = LevelError
	default:
		minLevel = LevelInfo
	}
	useJSON = strings.ToLower(format) == "json"
}

func levelStr(l Level) string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "INFO"
	}
}

func levelColor(l Level) string {
	if useJSON {
		return ""
	}
	switch l {
	case LevelDebug:
		return "\033[36m" // cyan
	case LevelInfo:
		return "\033[32m" // green
	case LevelWarn:
		return "\033[33m" // yellow
	case LevelError:
		return "\033[31m" // red
	default:
		return ""
	}
}

const resetColor = "\033[0m"

func emit(level Level, component, msg string, fields map[string]any) {
	if level < minLevel {
		return
	}

	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	if useJSON {
		// Structured JSON output
		parts := []string{
			fmt.Sprintf(`"ts":"%s"`, ts),
			fmt.Sprintf(`"level":"%s"`, levelStr(level)),
		}
		if component != "" {
			parts = append(parts, fmt.Sprintf(`"component":"%s"`, component))
		}
		parts = append(parts, fmt.Sprintf(`"msg":"%s"`, escapeJSON(msg)))
		for k, v := range fields {
			parts = append(parts, fmt.Sprintf(`"%s":%s`, k, formatValue(v)))
		}
		outLogger.Printf("{%s}", strings.Join(parts, ","))
	} else {
		// Human-readable colored output
		color := levelColor(level)
		prefix := fmt.Sprintf("%s %s%-5s%s", ts, color, levelStr(level), resetColor)
		if component != "" {
			prefix += fmt.Sprintf(" [%s]", component)
		}
		if len(fields) > 0 {
			fieldParts := make([]string, 0, len(fields))
			for k, v := range fields {
				fieldParts = append(fieldParts, fmt.Sprintf("%s=%v", k, v))
			}
			outLogger.Printf("%s %s %s", prefix, msg, strings.Join(fieldParts, " "))
		} else {
			outLogger.Printf("%s %s", prefix, msg)
		}
	}
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

// ─── Sinks ───────────────────────────────────────────────────────────────────

// Record is one log call, as handed to the sink.
type Record struct {
	Level     Level
	Component string
	Msg       string
	Fields    map[string]any
	// Caller is the file:line of the logging call.
	Caller string
}

// PanicRecord is one recovered panic, as handed to the panic sink.
type PanicRecord struct {
	Goroutine string
	Recovered any
	// Stack is captured while the panicking frames are still on the stack, so it
	// shows where the panic happened, not where it was reported.
	Stack  string
	Fields map[string]any
}

type (
	sinkFunc      func(Record)
	panicSinkFunc func(context.Context, PanicRecord)
)

var (
	sink      atomic.Pointer[sinkFunc]
	panicSink atomic.Pointer[panicSinkFunc]
)

// SetSink receives every Debug/Info/Warn/Error call, whatever LOG_LEVEL is —
// LOG_LEVEL decides what reaches stdout, the sink decides what it keeps.
// Request lines are not passed on: the request middleware reports each request
// itself, with more than a log line can carry. nil removes the sink.
func SetSink(fn func(Record)) {
	if fn == nil {
		sink.Store(nil)
		return
	}
	f := sinkFunc(fn)
	sink.Store(&f)
}

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

// record writes one public-API call and hands it to the sink. It must be called
// directly by Debug/Info/Warn/Error: the caller depth is fixed.
func record(level Level, component, msg string, fields map[string]any) {
	if p := sink.Load(); p != nil {
		caller := ""
		if _, file, line, ok := runtime.Caller(2); ok {
			caller = fmt.Sprintf("%s:%d", filepath.Base(file), line)
		}
		(*p)(Record{Level: level, Component: component, Msg: msg, Fields: fields, Caller: caller})
	}
	emit(level, component, msg, fields)
}

// ─── Public API ──────────────────────────────────────────────────────────────

func Debug(component, msg string, fields ...map[string]any) {
	record(LevelDebug, component, msg, mergeFields(fields))
}

func Info(component, msg string, fields ...map[string]any) {
	record(LevelInfo, component, msg, mergeFields(fields))
}

func Warn(component, msg string, fields ...map[string]any) {
	record(LevelWarn, component, msg, mergeFields(fields))
}

func Error(component, msg string, fields ...map[string]any) {
	record(LevelError, component, msg, mergeFields(fields))
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
