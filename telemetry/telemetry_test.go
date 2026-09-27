package telemetry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"

	monitor "github.com/aidenappl/go-monitor"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/logger"
)

func record(t *testing.T) *monitor.Recorder {
	t.Helper()
	rec := monitor.StartRecording()
	prev := slog.Default().Handler()
	InstallSinks("test-version")
	t.Cleanup(func() {
		rec.Stop()
		logger.SetHandler(prev)
		logger.SetPanicSink(nil)
	})
	return rec
}

func TestLogLinesBecomeEventsNamedByComponentAndLevel(t *testing.T) {
	rec := record(t)

	logger.Error("worker", "heartbeat update failed", logger.F{"worker_id": 3, "error": errors.New("driver: bad connection")})

	evs := rec.Named("worker.log.error")
	if len(evs) != 1 {
		t.Fatalf("recorded %d worker.log.error events, want 1 (all: %d)", len(evs), len(rec.Events()))
	}
	e := evs[0]
	d := e.Data.(map[string]any)
	if e.Level != monitor.LevelError {
		t.Errorf("level = %q, want error", e.Level)
	}
	// An error value is sent as its text: that is what Monitor groups issues by.
	if d["error"] != "driver: bad connection" || d["message"] != "heartbeat update failed" || fmt.Sprint(d["worker_id"]) != "3" {
		t.Errorf("data = %v", d)
	}
	if d["source_file"] != "telemetry_test.go" {
		t.Errorf("source_file = %v, want the logging line's file", d["source_file"])
	}
	if d["version"] != "test-version" {
		t.Errorf("version = %v, want test-version", d["version"])
	}
	if _, ok := d["caller"]; ok {
		t.Error("caller is redundant with source_file/source_line and should be gone")
	}
}

func TestEveryLevelMapsToItsMonitorLevel(t *testing.T) {
	rec := record(t)

	logger.Info("retention", "starting cleanup")
	logger.Warn("webhook", "delivery returned error status")

	if evs := rec.Named("retention.log.info"); len(evs) != 1 || evs[0].Level != monitor.LevelInfo {
		t.Errorf("info: %v", evs)
	}
	if evs := rec.Named("webhook.log.warn"); len(evs) != 1 || evs[0].Level != monitor.LevelWarn {
		t.Errorf("warn: %v", evs)
	}
}

func TestPanicsBecomePanicRecoveredWithTheirStack(t *testing.T) {
	rec := record(t)

	func() {
		defer logger.Recover("socket.worker.on_message", logger.F{"worker_id": 3})
		var m map[string]int
		m["x"] = 1
	}()

	evs := rec.Named("panic.recovered")
	if len(evs) != 1 {
		t.Fatalf("recorded %d panic.recovered events, want 1", len(evs))
	}
	d := evs[0].Data.(map[string]any)
	if d["goroutine"] != "socket.worker.on_message" || fmt.Sprint(d["worker_id"]) != "3" {
		t.Errorf("data = %v", d)
	}
	if !strings.Contains(fmt.Sprint(d["error"]), "nil map") || !strings.Contains(fmt.Sprint(d["stacktrace"]), "telemetry_test.go") {
		t.Errorf("error/stacktrace missing: %v", d)
	}
	if n := len(rec.Named("panic.log.error")); n != 0 {
		t.Errorf("a panic was reported twice (%d extra panic.log.error events)", n)
	}
}

func TestDebugLinesShipOnlyWithMonitorDebug(t *testing.T) {
	tests := []struct {
		name         string
		monitorDebug bool
		want         int
	}{
		{name: "MONITOR_DEBUG off drops debug", monitorDebug: false, want: 0},
		{name: "MONITOR_DEBUG on ships debug", monitorDebug: true, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev := env.MonitorDebug
			env.MonitorDebug = tt.monitorDebug
			t.Cleanup(func() { env.MonitorDebug = prev })
			rec := record(t)

			logger.Debug("container", "health status updated (unchanged)")
			// Other levels are never gated.
			logger.Info("container", "status updated")

			evs := rec.Named("container.log.debug")
			if len(evs) != tt.want {
				t.Fatalf("recorded %d container.log.debug events, want %d", len(evs), tt.want)
			}
			if tt.want == 1 && evs[0].Level != monitor.LevelDebug {
				t.Errorf("level = %q, want debug", evs[0].Level)
			}
			if n := len(rec.Named("container.log.info")); n != 1 {
				t.Errorf("recorded %d container.log.info events, want 1", n)
			}
		})
	}
}

// The event names the line that logged — file, function and line — not the
// logger or this package.
func TestLogEventsAreAttributedToTheirCallSite(t *testing.T) {
	rec := record(t)

	logger.Error("comp", "msg", nil)
	_, _, line, _ := runtime.Caller(0)
	wantLine := line - 1

	evs := rec.Named("comp.log.error")
	if len(evs) != 1 {
		t.Fatalf("recorded %d comp.log.error events, want 1", len(evs))
	}
	d := evs[0].Data.(map[string]any)
	t.Logf("source_file=%v source_func=%v source_line=%v", d["source_file"], d["source_func"], d["source_line"])
	if d["source_file"] != "telemetry_test.go" {
		t.Errorf("source_file = %v, want telemetry_test.go", d["source_file"])
	}
	if d["source_func"] != "TestLogEventsAreAttributedToTheirCallSite" {
		t.Errorf("source_func = %v, want TestLogEventsAreAttributedToTheirCallSite", d["source_func"])
	}
	if fmt.Sprint(d["source_line"]) != fmt.Sprint(wantLine) {
		t.Errorf("source_line = %v, want %d", d["source_line"], wantLine)
	}
}

func TestCtxVariantsCarryTheContextIDs(t *testing.T) {
	rec := record(t)
	ctx := monitor.WithRequestID(context.Background(), "abc123abc123abcd")
	ctx = monitor.WithUserID(ctx, "42")

	logger.ErrorCtx(ctx, "comp", "msg", logger.F{"error": errors.New("boom")})
	_, _, line, _ := runtime.Caller(0)

	evs := rec.Named("comp.log.error")
	if len(evs) != 1 {
		t.Fatalf("recorded %d comp.log.error events, want 1", len(evs))
	}
	e := evs[0]
	d := e.Data.(map[string]any)
	t.Logf("request_id=%q user_id=%q source=%v:%v", e.RequestID, e.UserID, d["source_file"], d["source_line"])
	if e.RequestID != "abc123abc123abcd" || e.UserID != "42" {
		t.Errorf("request_id = %q, user_id = %q", e.RequestID, e.UserID)
	}
	if d["error"] != "boom" || d["source_func"] != "TestCtxVariantsCarryTheContextIDs" || fmt.Sprint(d["source_line"]) != fmt.Sprint(line-1) {
		t.Errorf("data = %v", d)
	}
}

// LOG_LEVEL gates stdout only: an info line below it still reaches Monitor.
func TestLogLevelDoesNotGateMonitor(t *testing.T) {
	rec := record(t)
	logger.Init("error", "json")
	t.Cleanup(func() { logger.Init("info", "text") })

	logger.Info("retention", "starting cleanup")

	if n := len(rec.Named("retention.log.info")); n != 1 {
		t.Errorf("recorded %d retention.log.info events, want 1", n)
	}
}

// A logger field named "event" is data; it must not rename the Monitor event.
func TestEventFieldDoesNotRenameTheEvent(t *testing.T) {
	rec := record(t)

	logger.Error("webhook", "could not encode payload", logger.F{"event": "deployment.succeeded"})

	evs := rec.Named("webhook.log.error")
	if len(evs) != 1 {
		t.Fatalf("recorded %d webhook.log.error events, want 1 (all: %v)", len(evs), rec.Events())
	}
	if d := evs[0].Data.(map[string]any); d["event_type"] != "deployment.succeeded" {
		t.Errorf("data = %v", d)
	}
}

// EventCtx files the event under its own name, attributed to the caller, and
// stdout shows that name as a field.
func TestEventCtxIsFiledUnderItsOwnName(t *testing.T) {
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	t.Cleanup(func() { logger.SetOutput(os.Stdout) })
	logger.Init("info", "json")
	t.Cleanup(func() { logger.Init("info", "text") })
	rec := record(t)

	logger.EventCtx(context.Background(), logger.LevelInfo, "deployment.succeeded", "deploy", "deployment succeeded", logger.F{"deployment_id": 7})
	_, _, line, _ := runtime.Caller(0)

	evs := rec.Named("deployment.succeeded")
	if len(evs) != 1 {
		t.Fatalf("recorded %d deployment.succeeded events, want 1 (all: %v)", len(evs), rec.Events())
	}
	if n := len(rec.Named("deploy.log.info")); n != 0 {
		t.Errorf("recorded %d deploy.log.info events, want 0", n)
	}
	d := evs[0].Data.(map[string]any)
	if evs[0].Level != monitor.LevelInfo || d["message"] != "deployment succeeded" || fmt.Sprint(d["deployment_id"]) != "7" {
		t.Errorf("event = %v %v", evs[0].Level, d)
	}
	if d["source_func"] != "TestEventCtxIsFiledUnderItsOwnName" || fmt.Sprint(d["source_line"]) != fmt.Sprint(line-1) {
		t.Errorf("source = %v:%v, want line %d", d["source_func"], d["source_line"], line-1)
	}
	if !strings.Contains(buf.String(), `"event":"deployment.succeeded"`) {
		t.Errorf("stdout = %s, want the event name as a field", buf.String())
	}
}

// Through the whole chain each call prints exactly one stdout line, in the
// format logger has always printed: no derived event, no source fields.
func TestStdoutLinesAreUnchangedAndNotDuplicated(t *testing.T) {
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	t.Cleanup(func() { logger.SetOutput(os.Stdout) })
	logger.Init("info", "json")
	t.Cleanup(func() { logger.Init("info", "text") })
	record(t)

	logger.Error("worker", "heartbeat update failed", logger.F{"worker_id": 3, "error": errors.New(`driver: "bad" connection`), "ok": true, "dur": 1.5})
	logger.Warn("", "no component")
	logger.Debug("x", "hidden")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []*regexp.Regexp{
		regexp.MustCompile(`^\{"ts":"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z","level":"ERROR","component":"worker","msg":"heartbeat update failed","dur":1.5,"error":"driver: \\"bad\\" connection","ok":true,"worker_id":3\}$`),
		regexp.MustCompile(`^\{"ts":"[^"]+","level":"WARN","msg":"no component"\}$`),
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d stdout lines, want %d:\n%s", len(lines), len(want), buf.String())
	}
	for i, re := range want {
		t.Logf("stdout: %s", lines[i])
		if !re.MatchString(lines[i]) {
			t.Errorf("line %d = %s\nwant match %s", i, lines[i], re)
		}
	}
}
