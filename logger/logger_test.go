package logger

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureHandler records every slog record handed to it.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r.Clone())
	h.mu.Unlock()
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) all() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]slog.Record(nil), h.records...)
}

// capture makes a recording handler the slog default and installs the panic
// sink, for one test.
func capture(t *testing.T) (records func() []slog.Record, panics func() []PanicRecord) {
	t.Helper()
	prev := slog.Default().Handler()
	h := &captureHandler{}
	SetHandler(h)
	var mu sync.Mutex
	var ps []PanicRecord
	SetPanicSink(func(_ context.Context, p PanicRecord) { mu.Lock(); ps = append(ps, p); mu.Unlock() })
	t.Cleanup(func() { SetHandler(prev); SetPanicSink(nil) })
	return h.all,
		func() []PanicRecord { mu.Lock(); defer mu.Unlock(); return append([]PanicRecord(nil), ps...) }
}

func attrs(r slog.Record) map[string]any {
	m := map[string]any{}
	r.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value.Any(); return true })
	return m
}

// Every public function hands the handler a record whose PC is the line that
// logged — checked for each, since each must call logAt directly.
func TestRecordsCarryTheCallersPC(t *testing.T) {
	records, _ := capture(t)
	ctx := context.Background()

	calls := []struct {
		name  string
		level slog.Level
		log   func()
	}{
		{"Debug", slog.LevelDebug, func() { Debug("c", "m") }},
		{"Info", slog.LevelInfo, func() { Info("c", "m") }},
		{"Warn", slog.LevelWarn, func() { Warn("c", "m") }},
		{"Error", slog.LevelError, func() { Error("c", "m") }},
		{"DebugCtx", slog.LevelDebug, func() { DebugCtx(ctx, "c", "m") }},
		{"InfoCtx", slog.LevelInfo, func() { InfoCtx(ctx, "c", "m") }},
		{"WarnCtx", slog.LevelWarn, func() { WarnCtx(ctx, "c", "m") }},
		{"ErrorCtx", slog.LevelError, func() { ErrorCtx(ctx, "c", "m") }},
		{"EventCtx", slog.LevelInfo, func() { EventCtx(ctx, LevelInfo, "c.happened", "c", "m") }},
	}
	for i, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			c.log()
			_, file, _, _ := runtime.Caller(0)
			rs := records()
			if len(rs) != i+1 {
				t.Fatalf("handler received %d records, want %d", len(rs), i+1)
			}
			r := rs[i]
			if r.Level != c.level {
				t.Errorf("level = %v, want %v", r.Level, c.level)
			}
			frame, _ := runtime.CallersFrames([]uintptr{r.PC}).Next()
			// The call sits in the table's closure on this file.
			if frame.File != file || !strings.Contains(frame.Function, "TestRecordsCarryTheCallersPC") {
				t.Errorf("PC resolves to %s (%s:%d), want a closure in TestRecordsCarryTheCallersPC in %s", frame.Function, frame.File, frame.Line, file)
			}
		})
	}
}

func TestRecordsCarryComponentAndFields(t *testing.T) {
	records, _ := capture(t)

	Warn("worker", "heartbeat update failed", F{"worker_id": 3, "error": errors.New("bad connection"), "event": "deploy", "component": "ignored"})
	_, _, line, _ := runtime.Caller(0)

	rs := records()
	if len(rs) != 1 {
		t.Fatalf("handler received %d records, want 1", len(rs))
	}
	r := rs[0]
	a := attrs(r)
	if r.Message != "heartbeat update failed" || a["component"] != "worker" || a["worker_id"] != int64(3) {
		t.Errorf("record = %v %v", r.Message, a)
	}
	if a["error"] != "bad connection" {
		t.Errorf("error = %#v, want its text", a["error"])
	}
	if _, ok := a["event"]; ok || a["event_type"] != "deploy" {
		t.Errorf("an event field must be renamed to event_type, got %v", a)
	}
	if frame, _ := runtime.CallersFrames([]uintptr{r.PC}).Next(); frame.Line != line-1 || !strings.HasSuffix(frame.File, "logger_test.go") {
		t.Errorf("PC resolves to %s:%d, want logger_test.go:%d", frame.File, frame.Line, line-1)
	}
}

func TestLogLevelGatesStdout(t *testing.T) {
	prev := slog.Default().Handler()
	t.Cleanup(func() { SetHandler(prev); SetOutput(os.Stdout); Init("info", "text") })
	var buf bytes.Buffer
	SetOutput(&buf)
	SetHandler(StdoutHandler())
	Init("warn", "text")

	Info("retention", "starting cleanup")
	Warn("retention", "slow")

	if out := buf.String(); strings.Contains(out, "starting cleanup") || !strings.Contains(out, "slow") {
		t.Errorf("LOG_LEVEL=warn stdout = %q", out)
	}
}

func TestStdoutFormat(t *testing.T) {
	prev := slog.Default().Handler()
	t.Cleanup(func() { SetHandler(prev); SetOutput(os.Stdout); Init("info", "text") })
	var buf bytes.Buffer
	SetOutput(&buf)
	SetHandler(StdoutHandler())

	tests := []struct {
		name   string
		format string
		log    func()
		want   string // after the timestamp
	}{
		{"json with fields", "json", func() { Error("worker", "failed", F{"b": "x\ny", "a": 1}) }, `","level":"ERROR","component":"worker","msg":"failed","a":1,"b":"x\ny"}`},
		{"json without component", "json", func() { Info("", "plain") }, `","level":"INFO","msg":"plain"}`},
		{"text with fields", "text", func() { Warn("hub", "slow", F{"ms": 5}) }, " \033[33mWARN \033[0m [hub] slow ms=5"},
		{"text plain", "text", func() { Info("server", "up") }, " \033[32mINFO \033[0m [server] up"},
		{"derived event is not printed", "json", func() {
			slog.Error("m", "component", "c", "event", "c.log.error", "k", "v")
		}, `","level":"ERROR","component":"c","msg":"m","k":"v"}`},
		{"explicit event is printed", "json", func() {
			slog.Error("m", "component", "c", "event", "deployment.failed")
		}, `","level":"ERROR","component":"c","msg":"m","event":"deployment.failed"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf.Reset()
			Init("info", tt.format)
			tt.log()
			line := strings.TrimSuffix(buf.String(), "\n")
			if strings.Contains(line, "\n") {
				t.Fatalf("more than one line: %q", buf.String())
			}
			prefix := ""
			if tt.format == "json" {
				prefix = `{"ts":"`
			}
			if !strings.HasPrefix(line, prefix) || len(line) < len(prefix)+24 || line[len(prefix)+24:] != tt.want {
				t.Errorf("line = %q\nwant %s<ts>%s", line, prefix, tt.want)
			}
		})
	}
}

func TestRequestLinesAreNotPassedOn(t *testing.T) {
	records, _ := capture(t)

	Request("abc", "GET", "/admin/stacks", 500, time.Millisecond)

	if n := len(records()); n != 0 {
		t.Errorf("handler received %d records; the request middleware reports requests itself", n)
	}
}

func TestRecoverReportsThePanicWithTheStackWhereItHappened(t *testing.T) {
	_, panics := capture(t)

	func() {
		defer Recover("retention", F{"table": "container_logs"})
		panicsHere()
	}()

	ps := panics()
	if len(ps) != 1 {
		t.Fatalf("panic sink received %d reports, want 1", len(ps))
	}
	p := ps[0]
	if p.Goroutine != "retention" || p.Recovered != "boom" || p.Fields["table"] != "container_logs" {
		t.Errorf("report = %+v", p)
	}
	if !strings.Contains(p.Stack, "panicsHere") {
		t.Errorf("stack should include the frame that panicked:\n%s", p.Stack)
	}
}

func TestRecoverIsSilentWithoutAPanic(t *testing.T) {
	_, panics := capture(t)

	func() { defer Recover("idle") }()

	if n := len(panics()); n != 0 {
		t.Errorf("reported %d panics with nothing panicking", n)
	}
}

func panicsHere() { panic("boom") }

// EventCtx names the event through the slog "event" attribute and is attributed
// to its caller like every other function; an "event" field is still renamed.
func TestEventCtxNamesTheEvent(t *testing.T) {
	records, _ := capture(t)

	tests := []struct {
		name      string
		level     Level
		event     string
		fields    F
		wantLevel slog.Level
		wantEvent any
	}{
		{"named info event", LevelInfo, "deployment.succeeded", F{"deployment_id": 7}, slog.LevelInfo, "deployment.succeeded"},
		{"named error event", LevelError, "deployment.failed", F{"deployment_id": 7}, slog.LevelError, "deployment.failed"},
		{"event field is still renamed", LevelWarn, "worker.disconnected", F{"event": "other"}, slog.LevelWarn, "worker.disconnected"},
		{"empty event adds no attribute", LevelInfo, "", nil, slog.LevelInfo, nil},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			EventCtx(context.Background(), tt.level, tt.event, "deploy", "static message", tt.fields)
			_, file, line, _ := runtime.Caller(0)

			rs := records()
			if len(rs) != i+1 {
				t.Fatalf("handler received %d records, want %d", len(rs), i+1)
			}
			r := rs[i]
			a := attrs(r)
			if r.Level != tt.wantLevel || r.Message != "static message" || a["component"] != "deploy" {
				t.Errorf("record = %v %q %v", r.Level, r.Message, a)
			}
			if got, ok := a["event"]; tt.wantEvent == nil && ok || tt.wantEvent != nil && got != tt.wantEvent {
				t.Errorf("event = %v, want %v", got, tt.wantEvent)
			}
			if tt.fields["event"] != nil && a["event_type"] != tt.fields["event"] {
				t.Errorf("event field = %v, want it renamed to event_type", a)
			}
			frame, _ := runtime.CallersFrames([]uintptr{r.PC}).Next()
			if frame.File != file || frame.Line != line-1 {
				t.Errorf("PC resolves to %s:%d, want %s:%d", frame.File, frame.Line, file, line-1)
			}
		})
	}
}
