package routers

import (
	"context"
	"fmt"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
)

// writeDeploymentLog appends a line to a deployment's log — what the person
// watching the deploy reads. A write that fails is not worth failing the deploy
// over, but it is never dropped silently either.
func writeDeploymentLog(ctx context.Context, req query.CreateDeploymentLogRequest) {
	if err := query.CreateDeploymentLog(db.DB, req); err != nil {
		logger.ErrorCtx(ctx, "deploy", "could not write deployment log", logger.F{
			"deployment_id": req.DeploymentID,
			"log_level":     req.Level,
			"error":         err,
		})
	}
}

// markDeploymentFailed sets a deployment and its stack to failed together. It
// reports whether the write landed; a failure is logged here, since a deploy
// left in "deploying" is exactly what the caller was trying to prevent.
func markDeploymentFailed(ctx context.Context, deploymentID, stackID int) bool {
	err := func() error {
		tx, err := db.BeginTx()
		if err != nil {
			return fmt.Errorf("begin: %w", err)
		}
		defer tx.Rollback()
		if err := query.UpdateDeploymentAndStackStatus(tx, deploymentID, "failed", stackID, "failed"); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit: %w", err)
		}
		return nil
	}()
	if err != nil {
		logger.ErrorCtx(ctx, "deploy", "could not mark deployment failed", logger.F{
			"deployment_id": deploymentID,
			"stack_id":      stackID,
			"error":         err,
		})
		return false
	}
	return true
}

// releaseStackClaim resets a stack claimed for a deploy that never reached the
// worker. If it fails the stack stays "deploying" until the claim expires, so
// the failure is logged.
func releaseStackClaim(ctx context.Context, stackID int) {
	active := "active"
	if _, err := query.UpdateStack(db.DB, stackID, query.UpdateStackRequest{Status: &active}); err != nil {
		logger.ErrorCtx(ctx, "deploy", "could not release stack deploy claim", logger.F{"stack_id": stackID, "error": err})
	}
}

// failDeployment force-fails a deployment the runner never finished — it was
// never dispatched, stalled, or ran out of time — and reports it as the one
// error-level deployment.failed for that failure. Failures the runner executes
// are reported by the runner; the API raises this only for the ones it alone
// sees, and cause says which: "dispatch_failed", "stalled" or "timeout".
//
// A stall or timeout while the runner's latest ping reply said it still has the
// deploy in progress is a warning (runner_in_progress=true): the runner is
// still executing it and raises its own deployment.failed error if it fails,
// so an error here would make one failed deploy two issues.
//
// err is the underlying error, if there is one; extra adds fields.
func failDeployment(ctx context.Context, deploymentID, stackID int, cause string, err error, extra ...logger.F) {
	markDeploymentFailed(ctx, deploymentID, stackID)
	level := failDeploymentLevel(deploymentID, cause, time.Now())
	forgetRunnerDeploymentStatus(deploymentID)

	fields := logger.F{}
	for _, e := range extra {
		for k, v := range e {
			fields[k] = v
		}
	}
	fields["deployment_id"] = deploymentID
	fields["stack_id"] = stackID
	fields["cause"] = cause
	if level == logger.LevelWarn {
		fields["runner_in_progress"] = true
	}
	if err != nil {
		fields["error"] = err
	}
	// Strategy and duration are read back rather than passed in, so every
	// caller reports them the same way. Without the row they are left out.
	if dep, derr := query.GetDeploymentByID(db.DB, deploymentID); derr == nil && dep != nil {
		fields["strategy"] = dep.Strategy
		start := dep.InsertedAt
		if dep.StartedAt != nil {
			start = *dep.StartedAt
		}
		end := time.Now()
		if dep.CompletedAt != nil {
			end = *dep.CompletedAt
		}
		if !start.IsZero() && !end.Before(start) {
			fields["duration_ms"] = end.Sub(start).Milliseconds()
		}
	}
	logger.EventCtx(ctx, level, "deployment.failed", "deploy", "deployment failed", fields)
}

// failDeploymentLevel is the level of the API's deployment.failed: warn for a
// stall or timeout the runner reported still in progress, error otherwise.
func failDeploymentLevel(deploymentID int, cause string, now time.Time) logger.Level {
	if (cause == "stalled" || cause == "timeout") && runnerHasDeploymentInProgress(deploymentID, now) {
		return logger.LevelWarn
	}
	return logger.LevelError
}
