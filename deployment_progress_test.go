package main

import (
	"log/slog"
	"testing"

	"github.com/aidenappl/lattice-api/logger"
)

// The runner sends status "failed" more than once per deploy (a rollback
// notice or a health-check failure, then the final failure); only the
// transition to failed is an error.
func TestFailedDeployLogsOneError(t *testing.T) {
	var got []slog.Record
	prev := slog.Default().Handler()
	logger.SetHandler(recordingHandler{&got})
	t.Cleanup(func() { logger.SetHandler(prev) })

	tests := []struct {
		name  string
		steps []struct{ status, previous string }
		want  int
	}{
		{
			name: "rolling: rollback notice then final failure",
			steps: []struct{ status, previous string }{
				{"deploying", ""},
				{"failed", "deploying"},
				{"failed", "failed"},
			},
			want: 1,
		},
		{
			name: "blue-green: health-check failure then final failure",
			steps: []struct{ status, previous string }{
				{"validating", ""},
				{"failed", "validating"},
				{"failed", "failed"},
			},
			want: 1,
		},
		{
			name: "already marked failed by the stall monitor",
			steps: []struct{ status, previous string }{
				{"failed", "failed"},
			},
			want: 0,
		},
		{
			name: "success logs no error",
			steps: []struct{ status, previous string }{
				{"deploying", ""},
				{"deployed", "deploying"},
			},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got = nil
			for _, s := range tt.steps {
				f := logger.F{"deployment_id": 7, "status": s.status, "progress_level": "error"}
				logDeploymentProgress(s.status, f)
				if s.status == "failed" {
					logDeploymentFailed(s.previous, logger.F{"deployment_id": 7})
				}
			}
			errs := 0
			for _, r := range got {
				if r.Level >= slog.LevelError {
					errs++
					if r.Message != "deployment failed" {
						t.Errorf("error record message = %q", r.Message)
					}
				}
				r.Attrs(func(a slog.Attr) bool {
					if a.Key == "level" {
						t.Errorf("record %q carries a level field; it collides with stdout's level", r.Message)
					}
					return true
				})
			}
			if errs != tt.want {
				t.Errorf("logged %d errors, want %d", errs, tt.want)
			}
		})
	}
}
