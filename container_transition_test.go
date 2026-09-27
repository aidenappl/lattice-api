package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/aidenappl/lattice-api/logger"
)

// recordingHandler keeps every slog record it is handed.
type recordingHandler struct{ records *[]slog.Record }

func (h recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordingHandler) Handle(_ context.Context, r slog.Record) error {
	*h.records = append(*h.records, r.Clone())
	return nil
}
func (h recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recordingHandler) WithGroup(string) slog.Handler      { return h }

// Runners re-report container state constantly; only a change is news.
func TestLogContainerTransition(t *testing.T) {
	var got []slog.Record
	prev := slog.Default().Handler()
	logger.SetHandler(recordingHandler{&got})
	t.Cleanup(func() { logger.SetHandler(prev) })

	tests := []struct {
		name, field, previous, current string
		level                          slog.Level
		msg                            string
	}{
		{"a repeated health report is debug", "health_status", "healthy", "healthy", slog.LevelDebug, "health status updated (unchanged)"},
		{"turning unhealthy is a warning", "health_status", "healthy", "unhealthy", slog.LevelWarn, "health status updated"},
		{"recovering is info", "health_status", "unhealthy", "healthy", slog.LevelInfo, "health status updated"},
		{"a status change is info", "status", "running", "stopped", slog.LevelInfo, "status updated"},
		{"a repeated status is debug", "status", "running", "running", slog.LevelDebug, "status updated (unchanged)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got = nil
			logContainerTransition(tt.field, "web", tt.previous, tt.current)
			if len(got) != 1 {
				t.Fatalf("logged %d records, want 1", len(got))
			}
			r := got[0]
			fields := map[string]any{}
			r.Attrs(func(a slog.Attr) bool { fields[a.Key] = a.Value.Any(); return true })
			if r.Level != tt.level || r.Message != tt.msg || fields[tt.field] != tt.current || fields["previous"] != tt.previous {
				t.Errorf("record = %v %q %v", r.Level, r.Message, fields)
			}
		})
	}
}
