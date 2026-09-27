package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/structs"
)

func recordAttr(r slog.Record, key string) any {
	var v any
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			v = a.Value.Any()
			return false
		}
		return true
	})
	return v
}

// eventsNamed counts the records filed under an explicit event name.
func eventsNamed(rs []slog.Record, event string) int {
	n := 0
	for _, r := range rs {
		if recordAttr(r, "event") == event {
			n++
		}
	}
	return n
}

// The runner sends status "failed" more than once per deploy (a rollback
// notice or a health-check failure, then the final failure); only the
// transition to failed is recorded, once, as deployment.failed at info. The
// runner's own deployment.failed is the error, so the API logs no error here —
// one failed deploy must open one Monitor issue, not two.
func TestRunnerReportedFailureIsOneInfoTransition(t *testing.T) {
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
			name: "success records no failure",
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
				logDeploymentProgress(context.Background(), s.status, f)
				if s.status == "failed" {
					logDeploymentFailed(context.Background(), s.previous, logger.F{"deployment_id": 7, "stack_id": 2, "strategy": "rolling", "duration_ms": int64(1500)})
				}
			}
			failed := 0
			for _, r := range got {
				if r.Level >= slog.LevelWarn {
					t.Errorf("record %q is %v; a runner-reported failure must not raise a warning or error", r.Message, r.Level)
				}
				if recordAttr(r, "event") == "deployment.failed" {
					failed++
					if r.Level != slog.LevelInfo || r.Message != "deployment failed" {
						t.Errorf("deployment.failed = %v %q, want info \"deployment failed\"", r.Level, r.Message)
					}
					for _, k := range []string{"deployment_id", "stack_id", "strategy", "duration_ms"} {
						if recordAttr(r, k) == nil {
							t.Errorf("deployment.failed is missing %s", k)
						}
					}
				}
				r.Attrs(func(a slog.Attr) bool {
					if a.Key == "level" {
						t.Errorf("record %q carries a level field; it collides with stdout's level", r.Message)
					}
					return true
				})
			}
			if failed != tt.want {
				t.Errorf("logged %d deployment.failed, want %d", failed, tt.want)
			}
		})
	}
}

// deployment.started comes from the first "deploying" only: once started_at is
// set, a repeat (or validating → deploying) is not a second start.
func TestDeploymentStartedOnce(t *testing.T) {
	var got []slog.Record
	prev := slog.Default().Handler()
	logger.SetHandler(recordingHandler{&got})
	t.Cleanup(func() { logger.SetHandler(prev) })

	started := time.Now().Add(-time.Minute)
	tests := []struct {
		name string
		dep  structs.Deployment
		want int
	}{
		{"first deploying", structs.Deployment{ID: 7, StackID: 2, Status: "approved", Strategy: "rolling"}, 1},
		{"validating back to deploying", structs.Deployment{ID: 7, StackID: 2, Status: "validating", StartedAt: &started}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got = nil
			logDeploymentStarted(context.Background(), &tt.dep)
			if n := eventsNamed(got, "deployment.started"); n != tt.want {
				t.Errorf("logged %d deployment.started, want %d", n, tt.want)
			}
			for _, r := range got {
				if r.Level != slog.LevelInfo || recordAttr(r, "strategy") != tt.dep.Strategy {
					t.Errorf("record = %v %q strategy=%v", r.Level, r.Message, recordAttr(r, "strategy"))
				}
			}
		})
	}
}

func TestDeploymentSucceededOnceWithDuration(t *testing.T) {
	var got []slog.Record
	prev := slog.Default().Handler()
	logger.SetHandler(recordingHandler{&got})
	t.Cleanup(func() { logger.SetHandler(prev) })

	now := time.Now()
	started := now.Add(-90 * time.Second)
	dep := &structs.Deployment{ID: 7, StackID: 2, Status: "deploying", Strategy: "blue-green", StartedAt: &started, InsertedAt: now.Add(-time.Hour)}

	logDeploymentSucceeded(context.Background(), "deploying", deploymentOutcomeFields(dep, now))
	logDeploymentSucceeded(context.Background(), "deployed", deploymentOutcomeFields(dep, now))

	if n := eventsNamed(got, "deployment.succeeded"); n != 1 {
		t.Fatalf("logged %d deployment.succeeded, want 1", n)
	}
	r := got[0]
	if r.Level != slog.LevelInfo || recordAttr(r, "duration_ms") != int64(90000) || recordAttr(r, "strategy") != "blue-green" || recordAttr(r, "stack_id") != int64(2) {
		t.Errorf("record = %v %q duration_ms=%v strategy=%v stack_id=%v", r.Level, r.Message, recordAttr(r, "duration_ms"), recordAttr(r, "strategy"), recordAttr(r, "stack_id"))
	}
}

func TestDeploymentDurationMS(t *testing.T) {
	now := time.Now()
	started := now.Add(-2 * time.Second)
	future := now.Add(time.Hour)
	tests := []struct {
		name   string
		dep    structs.Deployment
		want   int64
		wantOK bool
	}{
		{"from started_at", structs.Deployment{StartedAt: &started, InsertedAt: now.Add(-time.Hour)}, 2000, true},
		{"never started: from creation", structs.Deployment{InsertedAt: now.Add(-3 * time.Second)}, 3000, true},
		{"clock skew is left out", structs.Deployment{StartedAt: &future}, 0, false},
		{"no times at all", structs.Deployment{}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := deploymentDurationMS(&tt.dep, now)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("deploymentDurationMS = %d, %v; want %d, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// Late non-terminal progress for a settled deployment is ignored, so it cannot
// resurrect a force-failed row as in progress.
func TestLateProgressIsIgnored(t *testing.T) {
	tests := []struct {
		current, incoming string
		want              bool
	}{
		{"failed", "deploying", true},
		{"failed", "validating", true},
		{"deployed", "deploying", true},
		{"rolled_back", "validating", true},
		{"pending", "deploying", false},
		{"validating", "deploying", false},
		{"deploying", "validating", false},
		{"failed", "deployed", false},
		{"deploying", "failed", false},
	}
	for _, tt := range tests {
		t.Run(tt.current+"->"+tt.incoming, func(t *testing.T) {
			if got := lateProgress(tt.current, tt.incoming); got != tt.want {
				t.Errorf("lateProgress(%q, %q) = %v, want %v", tt.current, tt.incoming, got, tt.want)
			}
		})
	}
}

// A late "deployed" for a force-failed deployment is recorded as
// deployment.recovered with the previous status, never deployment.succeeded.
func TestLateDeployedAfterForceFailIsRecovered(t *testing.T) {
	var got []slog.Record
	prev := slog.Default().Handler()
	logger.SetHandler(recordingHandler{&got})
	t.Cleanup(func() { logger.SetHandler(prev) })

	dep := &structs.Deployment{ID: 7, StackID: 2, Status: "failed", Strategy: "rolling", InsertedAt: time.Now().Add(-time.Hour)}
	logDeploymentSucceeded(context.Background(), dep.Status, deploymentOutcomeFields(dep, time.Now()))

	if n := eventsNamed(got, "deployment.succeeded"); n != 0 {
		t.Errorf("logged %d deployment.succeeded, want 0", n)
	}
	if n := eventsNamed(got, "deployment.recovered"); n != 1 {
		t.Fatalf("logged %d deployment.recovered, want 1", n)
	}
	r := got[0]
	if r.Level != slog.LevelInfo || recordAttr(r, "previous_status") != "failed" || recordAttr(r, "deployment_id") != int64(7) {
		t.Errorf("record = %v %q previous_status=%v deployment_id=%v", r.Level, r.Message, recordAttr(r, "previous_status"), recordAttr(r, "deployment_id"))
	}
}
