package routers

import (
	"testing"
	"time"

	"github.com/aidenappl/lattice-api/logger"
)

// A stall or timeout force-fail is an error unless the runner's latest ping
// reply, still fresh, said it has the deploy in progress: then the runner
// reports the eventual failure itself and the API's event is a warning.
func TestFailDeploymentLevel(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name  string
		cause string
		reply *bool // nil: no reply recorded
		age   time.Duration
		want  logger.Level
	}{
		{"stalled, runner in progress", "stalled", boolp(true), 10 * time.Second, logger.LevelWarn},
		{"timeout, runner in progress", "timeout", boolp(true), time.Minute, logger.LevelWarn},
		{"stalled, runner idle", "stalled", boolp(false), 10 * time.Second, logger.LevelError},
		{"timeout, no reply (worker disconnected)", "timeout", nil, 0, logger.LevelError},
		{"stalled, reply is stale", "stalled", boolp(true), runnerStatusFreshness + time.Second, logger.LevelError},
		{"dispatch failed is always an error", "dispatch_failed", boolp(true), time.Second, logger.LevelError},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := 9000 + i
			t.Cleanup(func() { forgetRunnerDeploymentStatus(id) })
			if tt.reply != nil {
				recordRunnerDeploymentStatusAt(id, *tt.reply, now.Add(-tt.age))
			}
			if got := failDeploymentLevel(id, tt.cause, now); got != tt.want {
				t.Errorf("level = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunnerStatusLatestReplyWinsAndStaleEntriesArePruned(t *testing.T) {
	now := time.Now()
	t.Cleanup(func() { forgetRunnerDeploymentStatus(1); forgetRunnerDeploymentStatus(2) })

	recordRunnerDeploymentStatusAt(1, true, now.Add(-time.Minute))
	recordRunnerDeploymentStatusAt(1, false, now)
	if runnerHasDeploymentInProgress(1, now) {
		t.Error("an idle reply after an in-progress one must win")
	}

	recordRunnerDeploymentStatusAt(2, true, now.Add(-2*runnerStatusFreshness))
	recordRunnerDeploymentStatusAt(3, true, now)
	t.Cleanup(func() { forgetRunnerDeploymentStatus(3) })
	runnerStatuses.Lock()
	_, stale := runnerStatuses.m[2]
	runnerStatuses.Unlock()
	if stale {
		t.Error("a stale entry survived the next write")
	}
}

func boolp(b bool) *bool { return &b }
