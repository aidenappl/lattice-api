package telemetry

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	monitor "github.com/aidenappl/go-monitor"
	"github.com/aidenappl/lattice-api/logger"
)

func record(t *testing.T) *monitor.Recorder {
	t.Helper()
	rec := monitor.StartRecording()
	InstallSinks()
	t.Cleanup(func() {
		rec.Stop()
		logger.SetSink(nil)
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
	if c, _ := d["caller"].(string); !strings.HasPrefix(c, "telemetry_test.go:") {
		t.Errorf("caller = %v, want the logging line", d["caller"])
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
