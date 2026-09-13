package main

import (
	"testing"

	"github.com/aidenappl/lattice-api/logger"
)

// Runners re-report container state constantly; only a change is news.
func TestLogContainerTransition(t *testing.T) {
	var got []logger.Record
	logger.SetSink(func(r logger.Record) { got = append(got, r) })
	t.Cleanup(func() { logger.SetSink(nil) })

	tests := []struct {
		name, field, previous, current string
		level                          logger.Level
		msg                            string
	}{
		{"a repeated health report is debug", "health_status", "healthy", "healthy", logger.LevelDebug, "health status updated (unchanged)"},
		{"turning unhealthy is a warning", "health_status", "healthy", "unhealthy", logger.LevelWarn, "health status updated"},
		{"recovering is info", "health_status", "unhealthy", "healthy", logger.LevelInfo, "health status updated"},
		{"a status change is info", "status", "running", "stopped", logger.LevelInfo, "status updated"},
		{"a repeated status is debug", "status", "running", "running", logger.LevelDebug, "status updated (unchanged)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got = nil
			logContainerTransition(tt.field, "web", tt.previous, tt.current)
			if len(got) != 1 {
				t.Fatalf("logged %d records, want 1", len(got))
			}
			r := got[0]
			if r.Level != tt.level || r.Msg != tt.msg || r.Fields[tt.field] != tt.current || r.Fields["previous"] != tt.previous {
				t.Errorf("record = %+v", r)
			}
		})
	}
}
