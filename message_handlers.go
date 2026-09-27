package main

// This file contains WebSocket message handler functions extracted from main.go.
// They are called from the OnMessage dispatch in main().

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/mailer"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/aidenappl/lattice-api/webhooks"
)

// prevNetworkState stores the previous heartbeat's network bytes and timestamp
// for each worker, used to compute network rates (bytes/sec).
var (
	prevNetworkState   = make(map[int]networkSnapshot)
	prevNetworkStateMu sync.Mutex
)

type networkSnapshot struct {
	rxBytes int64
	txBytes int64
	at      time.Time
}

// stripDeploySuffix removes a 6-char alphanumeric deploy suffix from a container name.
// e.g., "hillview-auth-api-x7k2m9" -> "hillview-auth-api"
func stripDeploySuffix(name string) string {
	idx := strings.LastIndex(name, "-")
	if idx == -1 || idx == len(name)-1 {
		return name
	}
	suffix := name[idx+1:]
	if len(suffix) != 6 {
		return name
	}
	for _, c := range suffix {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return name
		}
	}
	return name[:idx]
}

// handleHeartbeatMetrics persists worker metrics and enriches the payload with
// computed network rates (bytes/sec). The rates are written back into the
// payload map so the admin WebSocket broadcast includes them.
func handleHeartbeatMetrics(workerID int, payload map[string]any) {
	req := query.CreateMetricsRequest{WorkerID: workerID}

	extractFloat := func(key string) *float64 {
		if v, ok := payload[key].(float64); ok {
			return &v
		}
		return nil
	}
	extractInt := func(key string) *int {
		if v, ok := payload[key].(float64); ok {
			i := int(v)
			return &i
		}
		return nil
	}
	extractInt64 := func(key string) *int64 {
		if v, ok := payload[key].(float64); ok {
			i := int64(v)
			return &i
		}
		return nil
	}

	req.CPUPercent = clampFloat(extractFloat("cpu_percent"), 0, 100)
	req.CPUCores = extractInt("cpu_cores")
	req.LoadAvg1 = clampFloatMin(extractFloat("load_avg_1"), 0)
	req.LoadAvg5 = clampFloatMin(extractFloat("load_avg_5"), 0)
	req.LoadAvg15 = clampFloatMin(extractFloat("load_avg_15"), 0)
	req.MemoryUsedMB = clampFloatMin(extractFloat("memory_used_mb"), 0)
	req.MemoryTotalMB = clampFloatMin(extractFloat("memory_total_mb"), 0)
	req.MemoryFreeMB = clampFloatMin(extractFloat("memory_free_mb"), 0)
	req.SwapUsedMB = clampFloatMin(extractFloat("swap_used_mb"), 0)
	req.SwapTotalMB = clampFloatMin(extractFloat("swap_total_mb"), 0)
	req.DiskUsedMB = clampFloatMin(extractFloat("disk_used_mb"), 0)
	req.DiskTotalMB = clampFloatMin(extractFloat("disk_total_mb"), 0)
	req.ContainerCount = extractInt("container_count")
	req.ContainerRunningCount = extractInt("container_running_count")
	req.NetworkRxBytes = extractInt64("network_rx_bytes")
	req.NetworkTxBytes = extractInt64("network_tx_bytes")
	req.UptimeSeconds = clampFloatMin(extractFloat("uptime_seconds"), 0)
	req.ProcessCount = extractInt("process_count")

	// Runner self-metrics
	req.RunnerGoroutines = extractInt("runner_goroutines")
	req.RunnerHeapMB = clampFloatMin(extractFloat("runner_heap_mb"), 0)
	req.RunnerSysMB = clampFloatMin(extractFloat("runner_sys_mb"), 0)

	// Compute network rates (bytes/sec) from consecutive heartbeats
	now := time.Now()
	if req.NetworkRxBytes != nil && req.NetworkTxBytes != nil {
		prevNetworkStateMu.Lock()
		prev, hasPrev := prevNetworkState[workerID]
		prevNetworkState[workerID] = networkSnapshot{
			rxBytes: *req.NetworkRxBytes,
			txBytes: *req.NetworkTxBytes,
			at:      now,
		}
		prevNetworkStateMu.Unlock()

		if hasPrev {
			elapsed := now.Sub(prev.at).Seconds()
			if elapsed > 0 {
				rxDelta := *req.NetworkRxBytes - prev.rxBytes
				txDelta := *req.NetworkTxBytes - prev.txBytes
				// Only compute rate if bytes increased (counter reset = skip)
				if rxDelta >= 0 && txDelta >= 0 {
					rxRate := math.Round(float64(rxDelta)/elapsed*10) / 10
					txRate := math.Round(float64(txDelta)/elapsed*10) / 10
					req.NetworkRxRate = &rxRate
					req.NetworkTxRate = &txRate
					// Write rates back into the payload so the admin WebSocket
					// broadcast includes them for real-time dashboard display.
					payload["network_rx_rate"] = rxRate
					payload["network_tx_rate"] = txRate
				}
			}
		}
	}

	if err := query.CreateMetrics(db.DB, req); err != nil {
		logger.ErrorCtx(context.Background(), "worker", "failed to store heartbeat metrics", logger.F{"worker_id": workerID, "error": err})
	}

	// Persist per-container resource stats if present
	handleContainerMetrics(workerID, payload)
}

// handleContainerMetrics extracts and stores per-container CPU/memory stats
// sent by the runner every 3rd heartbeat.
func handleContainerMetrics(workerID int, payload map[string]any) {
	statsRaw, ok := payload["container_stats"]
	if !ok {
		return
	}
	statsList, ok := statsRaw.([]any)
	if !ok || len(statsList) == 0 {
		return
	}

	var reqs []query.CreateContainerMetricsRequest
	for _, item := range statsList {
		s, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := s["name"].(string)
		if name == "" {
			continue
		}

		cmReq := query.CreateContainerMetricsRequest{
			WorkerID:      workerID,
			ContainerName: name,
		}

		if v, ok := s["cpu_percent"].(float64); ok {
			clamped := math.Max(0, v)
			cmReq.CPUPercent = &clamped
		}
		if v, ok := s["mem_usage_mb"].(float64); ok {
			clamped := math.Max(0, v)
			cmReq.MemUsageMB = &clamped
		}
		if v, ok := s["mem_limit_mb"].(float64); ok {
			clamped := math.Max(0, v)
			cmReq.MemLimitMB = &clamped
		}
		if v, ok := s["mem_percent"].(float64); ok {
			clamped := math.Min(100, math.Max(0, v))
			cmReq.MemPercent = &clamped
		}

		// Resolve container name to DB ID
		lookupName := name
		if canonical := stripDeploySuffix(name); canonical != name {
			lookupName = canonical
		}
		if c, err := containerNameCache.GetContainerByName(lookupName); err == nil {
			cmReq.ContainerID = &c.ID
		}

		reqs = append(reqs, cmReq)
	}

	if len(reqs) > 0 {
		if err := query.CreateContainerMetricsBatch(db.DB, reqs); err != nil {
			logger.ErrorCtx(context.Background(), "worker", "failed to store container metrics", logger.F{"worker_id": workerID, "count": len(reqs), "error": err})
		}
	}
}

// clampFloat returns v clamped to [min, max], or nil if v is nil.
func clampFloat(v *float64, min, max float64) *float64 {
	if v == nil {
		return nil
	}
	clamped := math.Max(min, math.Min(max, *v))
	return &clamped
}

// clampFloatMin returns v clamped to [min, +inf), or nil if v is nil.
func clampFloatMin(v *float64, min float64) *float64 {
	if v == nil {
		return nil
	}
	clamped := math.Max(min, *v)
	return &clamped
}

func handleDeploymentProgress(ctx context.Context, payload map[string]any) {
	deploymentID, ok := payload["deployment_id"].(float64)
	if !ok {
		return
	}
	status, _ := payload["status"].(string)
	message, _ := payload["message"].(string)
	step, _ := payload["step"].(string)
	containerName, _ := payload["container_name"].(string)

	// Determine log level from status
	level := "info"
	if status == "failed" {
		level = "error"
	}

	// Build a descriptive stage
	var stage *string
	if step != "" {
		s := step
		if containerName != "" {
			s = containerName + ":" + step
		}
		stage = &s
	} else if containerName != "" {
		stage = &containerName
	}

	// Build the log message
	logMsg := message
	if logMsg == "" {
		logMsg = fmt.Sprintf("status=%s", status)
	}

	progressFields := logger.F{"deployment_id": int(deploymentID), "progress_level": level, "stage": stage, "status": status, "detail": logMsg}
	logDeploymentProgress(ctx, status, progressFields)

	// Store deployment log
	if err := query.CreateDeploymentLog(db.DB, query.CreateDeploymentLogRequest{
		DeploymentID: int(deploymentID),
		Level:        level,
		Stage:        stage,
		Message:      logMsg,
	}); err != nil {
		logger.ErrorCtx(ctx, "deploy", "failed to store deployment log", logger.F{"deployment_id": int(deploymentID), "error": err})
	}

	// Fire webhooks and email notifications on deployment terminal states
	if status == "failed" {
		webhooks.Fire("deployment.failed", map[string]any{
			"deployment_id": int(deploymentID),
			"message":       message,
		})
		stackLabel := fmt.Sprintf("Deployment #%d", int(deploymentID))
		if dep, dErr := query.GetDeploymentByID(db.DB, int(deploymentID)); dErr == nil {
			if s, sErr := query.GetStackByID(db.DB, dep.StackID); sErr == nil {
				stackLabel = fmt.Sprintf("<strong>%s</strong> (Deployment #%d)", s.Name, int(deploymentID))
			}
		}
		mailer.Notify("deployment.failed", "Deployment Failed",
			fmt.Sprintf("%s has failed.\n\n<strong>Error:</strong> %s\n\nAny successfully updated containers have been rolled back to their previous state. Review the deployment logs in the Lattice dashboard for details.", stackLabel, message))
	}
	if status == "deployed" {
		webhooks.Fire("deployment.success", map[string]any{
			"deployment_id": int(deploymentID),
			"message":       message,
		})
		stackLabel := fmt.Sprintf("Deployment #%d", int(deploymentID))
		if dep, dErr := query.GetDeploymentByID(db.DB, int(deploymentID)); dErr == nil {
			if s, sErr := query.GetStackByID(db.DB, dep.StackID); sErr == nil {
				stackLabel = fmt.Sprintf("<strong>%s</strong> (Deployment #%d)", s.Name, int(deploymentID))
			}
		}
		mailer.Notify("deployment.success", "Deployment Successful",
			fmt.Sprintf("%s deployed successfully.\n\nAll containers have been updated and are running.", stackLabel))
	}

	// Update deployment status if it's a terminal/state-change status
	if status == "deploying" || status == "validating" || status == "deployed" || status == "failed" || status == "rolled_back" {
		// On terminal states, update deployment + stack + containers atomically
		if status == "deployed" || status == "failed" || status == "rolled_back" {
			dep, err := query.GetDeploymentByID(db.DB, int(deploymentID))
			if err != nil {
				logger.ErrorCtx(ctx, "deploy", "failed to get deployment for status update", logger.F{"deployment_id": int(deploymentID), "error": err})
				return
			}

			stackStatus := status
			if status == "rolled_back" {
				stackStatus = "failed"
			}

			tx, txErr := db.BeginTx()
			if txErr != nil {
				logger.ErrorCtx(ctx, "deploy", "failed to start transaction for deployment completion", logger.F{"deployment_id": int(deploymentID), "error": txErr})
				return
			}

			if err := query.UpdateDeploymentAndStackStatus(tx, int(deploymentID), status, dep.StackID, stackStatus); err != nil {
				tx.Rollback()
				logger.ErrorCtx(ctx, "deploy", "failed to update deployment/stack status", logger.F{"deployment_id": int(deploymentID), "error": err})
				return
			}

			// Update container statuses within the same transaction
			if status == "deployed" || status == "failed" {
				containerStatus := "running"
				if status == "failed" {
					containerStatus = "error"
				}
				dcs, err := query.ListDeploymentContainers(tx, int(deploymentID))
				if err != nil {
					logger.ErrorCtx(ctx, "deploy", "failed to list deployment containers for status update", logger.F{"deployment_id": int(deploymentID), "error": err})
				} else if dcs != nil {
					for _, dc := range *dcs {
						s := containerStatus
						// Logged, not fatal: the deployment's own outcome still has to be
						// recorded, and the next container_sync corrects the container.
						if _, err := query.UpdateContainer(tx, dc.ContainerID, query.UpdateContainerRequest{Status: &s}); err != nil {
							logger.ErrorCtx(ctx, "deploy", "failed to update container status for deployment completion", logger.F{
								"deployment_id": int(deploymentID), "container_id": dc.ContainerID, "status": s, "error": err,
							})
						}
					}
				}
			}

			if err := tx.Commit(); err != nil {
				logger.ErrorCtx(ctx, "deploy", "failed to commit deployment completion", logger.F{"deployment_id": int(deploymentID), "error": err})
				return
			}
			logger.InfoCtx(ctx, "deploy", "updated deployment/stack status", logger.F{"deployment_id": int(deploymentID), "status": status, "stack_id": dep.StackID})
			switch status {
			case "failed":
				fields := deploymentOutcomeFields(dep, time.Now())
				fields["stage"] = stage
				fields["detail"] = logMsg
				if step != "" {
					fields["failed_step"] = step
				}
				logDeploymentFailed(ctx, dep.Status, fields)
			case "deployed":
				logDeploymentSucceeded(ctx, dep.Status, deploymentOutcomeFields(dep, time.Now()))
			}
		} else {
			// Non-terminal state (deploying/validating). The runner repeats
			// "deploying" on every step; only a change is written, so started_at
			// stays the time the deploy began rather than its latest step.
			dep, err := query.GetDeploymentByID(db.DB, int(deploymentID))
			if err != nil {
				logger.ErrorCtx(ctx, "deploy", "failed to get deployment for status update", logger.F{"deployment_id": int(deploymentID), "error": err})
				return
			}
			if dep.Status == status {
				return
			}
			if lateProgress(dep.Status, status) {
				// A runner still reporting steps for a deployment already settled —
				// typically one the monitor force-failed. Writing it would resurrect
				// the row as in-progress.
				logger.DebugCtx(ctx, "deploy", "ignored late deployment progress", logger.F{"deployment_id": int(deploymentID), "status": status, "current_status": dep.Status})
				return
			}
			if err := query.UpdateDeploymentStatus(db.DB, int(deploymentID), status); err != nil {
				logger.ErrorCtx(ctx, "deploy", "failed to update deployment status", logger.F{"deployment_id": int(deploymentID), "error": err})
				return
			}
			if status == "deploying" {
				logDeploymentStarted(ctx, dep)
			}
		}
	}
}

// logDeploymentProgress logs one progress message from the runner. The
// runner's text is variable, so it goes in a field and the message stays fixed.
// A terminal result (deployed, rolled back) is info; everything else is debug —
// each deploy sends dozens of steps, and deployment_logs keeps them all
// regardless. That includes "failed": the runner sends it more than once per
// deploy (a rollback notice or a health-check failure, then the final failure),
// so the one error event comes from logDeploymentFailed instead.
func logDeploymentProgress(ctx context.Context, status string, fields logger.F) {
	switch status {
	case "deployed", "rolled_back":
		logger.InfoCtx(ctx, "deploy", "deployment progress", fields)
	default:
		logger.DebugCtx(ctx, "deploy", "deployment progress", fields)
	}
}

// deploymentOutcomeFields are the fields of deployment.succeeded and
// deployment.failed. dep is the row as it was before this message's update.
func deploymentOutcomeFields(dep *structs.Deployment, now time.Time) logger.F {
	fields := logger.F{
		"deployment_id": dep.ID,
		"stack_id":      dep.StackID,
		"strategy":      dep.Strategy,
	}
	if ms, ok := deploymentDurationMS(dep, now); ok {
		fields["duration_ms"] = ms
	}
	return fields
}

// deploymentDurationMS is how long a deployment has run: from started_at (its
// first "deploying"), or from its creation when it failed before it began. A
// negative span means the clocks disagree, and is left out rather than sent.
func deploymentDurationMS(dep *structs.Deployment, now time.Time) (int64, bool) {
	start := dep.InsertedAt
	if dep.StartedAt != nil {
		start = *dep.StartedAt
	}
	if start.IsZero() || now.Before(start) {
		return 0, false
	}
	return now.Sub(start).Milliseconds(), true
}

// logDeploymentStarted reports deployment.started for the first "deploying" a
// runner sends. dep is the row before this message's update: a deployment that
// already has started_at (validating → deploying, or a late repeat after a
// force-fail) has been reported once already.
func logDeploymentStarted(ctx context.Context, dep *structs.Deployment) {
	if dep.StartedAt != nil {
		return
	}
	logger.EventCtx(ctx, logger.LevelInfo, "deployment.started", "deploy", "deployment started", logger.F{
		"deployment_id": dep.ID,
		"stack_id":      dep.StackID,
		"strategy":      dep.Strategy,
	})
}

// isTerminalDeploymentStatus reports whether a deployment in status is settled.
func isTerminalDeploymentStatus(status string) bool {
	switch status {
	case "deployed", "failed", "rolled_back":
		return true
	}
	return false
}

// lateProgress reports whether a non-terminal status (deploying, validating)
// arrived for a deployment already in a terminal state, and must be ignored.
func lateProgress(current, incoming string) bool {
	return !isTerminalDeploymentStatus(incoming) && isTerminalDeploymentStatus(current)
}

// logDeploymentSucceeded reports deployment.succeeded once: a repeated
// "deployed" (previous already deployed) is debug.
//
// A "deployed" for a deployment already marked failed — the monitor force-failed
// it on a stall or timeout while the runner kept going — is the deploy genuinely
// succeeding late. The row still flips to deployed, but it is reported as
// deployment.recovered with the previous status, not as an ordinary success.
func logDeploymentSucceeded(ctx context.Context, previous string, fields logger.F) {
	if previous == "deployed" {
		logger.DebugCtx(ctx, "deploy", "deployment succeeded (already deployed)", fields)
		return
	}
	if previous == "failed" {
		fields["previous_status"] = previous
		logger.EventCtx(ctx, logger.LevelInfo, "deployment.recovered", "deploy", "deployment recovered after being marked failed", fields)
		return
	}
	logger.EventCtx(ctx, logger.LevelInfo, "deployment.succeeded", "deploy", "deployment succeeded", fields)
}

// logDeploymentFailed records a runner-reported transition to failed as
// deployment.failed at info, the first time — when previous (the status before
// this message's update) is not already failed — and debug for every repeat.
//
// Info, not error: the runner raises its own deployment.failed error for the
// same failure, with the error text, and that is the one Monitor issue. This is
// the control plane's record of the state change. Failures only the API sees
// (stall, dispatch, timeout) stay errors, with a cause field.
func logDeploymentFailed(ctx context.Context, previous string, fields logger.F) {
	if previous == "failed" {
		logger.DebugCtx(ctx, "deploy", "deployment failed (already failed)", fields)
		return
	}
	logger.EventCtx(ctx, logger.LevelInfo, "deployment.failed", "deploy", "deployment failed", fields)
}

func handleContainerStatus(ctx context.Context, workerID int, payload map[string]any) map[string]any {
	containerName, _ := payload["container_name"].(string)
	action, _ := payload["action"].(string)
	status, _ := payload["status"].(string)

	// Always return the raw payload with enrichments for the admin broadcast
	enriched := map[string]any{
		"container_name": containerName,
		"action":         action,
		"status":         status,
	}

	if containerName == "" || status != "success" {
		return enriched
	}

	// Map action to container DB status
	var dbStatus string
	switch action {
	case "stop", "remove", "kill":
		dbStatus = "stopped"
	case "start", "restart", "recreate", "unpause":
		dbStatus = "running"
	case "pause":
		dbStatus = "paused"
	default:
		return enriched
	}

	lookupName := containerName
	if canonical := stripDeploySuffix(containerName); canonical != containerName {
		lookupName = canonical
	}
	c, err := containerNameCache.GetContainerByName(lookupName)
	if err != nil {
		logger.ErrorCtx(ctx, "container", "could not find container", logger.F{"container_name": containerName, "error": err})
		return enriched
	}

	// Enrich with container_id and the resolved container state for the frontend
	enriched["container_id"] = c.ID
	enriched["container_state"] = dbStatus

	req := query.UpdateContainerRequest{Status: &dbStatus}
	// Track when container started running
	if dbStatus == "running" {
		now := time.Now().UTC()
		req.StartedAt = &now
	}
	// On start/recreate/restart, reset health_status to "starting" if the container has a healthcheck.
	if (action == "start" || action == "recreate" || action == "restart") && c.HealthCheck != nil {
		hs := "starting"
		req.HealthStatus = &hs
		enriched["health_status"] = hs
	}

	if _, err := query.UpdateContainer(db.DB, c.ID, req); err != nil {
		logger.ErrorCtx(ctx, "container", "failed to update status", logger.F{"container_name": containerName, "status": dbStatus, "error": err})
	} else {
		// Invalidate the cache so the next sync/status read sees fresh Status/HealthCheck
		// rather than the stale copy this write just superseded (up to a 60s TTL).
		containerNameCache.Invalidate(c.Name)
		logContainerTransition(ctx, "status", containerName, c.Status, dbStatus)

		// Write a lifecycle entry to lifecycle_logs so it persists in the log viewer.
		lifecycleMessages := map[string]string{
			"start":    "container started",
			"restart":  "container restarted",
			"stop":     "container stopped",
			"kill":     "container force-killed",
			"recreate": "container recreated",
			"pause":    "container paused",
			"unpause":  "container unpaused",
		}
		if msg, ok := lifecycleMessages[action]; ok {
			cID := c.ID
			cName := c.Name

			logReq := query.CreateLifecycleLogRequest{
				WorkerID:      workerID,
				ContainerID:   &cID,
				ContainerName: &cName,
				Event:         action,
				Message:       msg,
			}
			if err := query.CreateLifecycleLog(db.DB, logReq); err != nil {
				logger.ErrorCtx(ctx, "container", "failed to write lifecycle log", logger.F{"container_name": containerName, "error": err})
			}
		}
	}

	return enriched
}

// handleLifecycleLog processes lifecycle_log messages from workers and persists
// them to the lifecycle_logs table. These are verbose progress messages sent
// during container actions (e.g. "pulling image...", "stopping container...").
func handleLifecycleLog(ctx context.Context, workerID int, payload map[string]any) {
	containerName, _ := payload["container_name"].(string)
	event, _ := payload["event"].(string)
	message, _ := payload["message"].(string)

	if message == "" {
		return
	}

	logReq := query.CreateLifecycleLogRequest{
		WorkerID: workerID,
		Event:    event,
		Message:  message,
	}

	if containerName != "" {
		logReq.ContainerName = &containerName
		lookupName := containerName
		if canonical := stripDeploySuffix(containerName); canonical != containerName {
			lookupName = canonical
		}
		if c, err := containerNameCache.GetContainerByName(lookupName); err == nil {
			logReq.ContainerID = &c.ID
		}
	}

	if err := query.CreateLifecycleLog(db.DB, logReq); err != nil {
		logger.ErrorCtx(ctx, "container", "failed to write lifecycle log", logger.F{"container_name": containerName, "lifecycle_event": event, "error": err})
	}
}

// handleContainerHealthStatus processes health_status messages from workers.
// logContainerTransition records a container's status or health. Runners
// re-report both constantly — every health probe, every sync — so logging each
// report buried the few that mean something. A change is logged at info, or warn
// when a container turns unhealthy; a repeat goes to debug, which neither stdout
// at the default LOG_LEVEL nor Monitor (without MONITOR_DEBUG) receives.
func logContainerTransition(ctx context.Context, field, containerName, previous, current string) {
	fields := logger.F{"container_name": containerName, field: current, "previous": previous}
	msg := "status updated"
	if field == "health_status" {
		msg = "health status updated"
	}
	switch {
	case previous == current:
		logger.DebugCtx(ctx, "container", msg+" (unchanged)", fields)
	case field == "health_status" && current == "unhealthy":
		logger.WarnCtx(ctx, "container", msg, fields)
	default:
		logger.InfoCtx(ctx, "container", msg, fields)
	}
}

func handleContainerHealthStatus(ctx context.Context, payload map[string]any) {
	containerName, _ := payload["container_name"].(string)
	healthStatus, _ := payload["health_status"].(string)
	if containerName == "" || healthStatus == "" {
		return
	}

	lookupName := containerName
	if canonical := stripDeploySuffix(containerName); canonical != containerName {
		lookupName = canonical
	}
	c, err := containerNameCache.GetContainerByName(lookupName)
	if errors.Is(err, sql.ErrNoRows) {
		// Not a Lattice-managed container — e.g. a database instance's
		// lattice-db-* container, whose health arrives separately as
		// db_health_status. Same as handleContainerSync: ignore it.
		logger.DebugCtx(ctx, "container", "ignoring health update for unmanaged container", logger.F{"container_name": containerName})
		return
	}
	if err != nil {
		logger.ErrorCtx(ctx, "container", "could not find container for health update", logger.F{"container_name": containerName, "error": err})
		return
	}

	// Runners report health on every probe; only a change needs a write.
	if c.HealthStatus == healthStatus {
		logContainerTransition(ctx, "health_status", containerName, c.HealthStatus, healthStatus)
		return
	}

	if _, err := query.UpdateContainer(db.DB, c.ID, query.UpdateContainerRequest{HealthStatus: &healthStatus}); err != nil {
		logger.ErrorCtx(ctx, "container", "failed to update health status", logger.F{"container_name": containerName, "health_status": healthStatus, "error": err})
	} else {
		containerNameCache.Invalidate(c.Name)
		logContainerTransition(ctx, "health_status", containerName, c.HealthStatus, healthStatus)
	}
}

// handleContainerSync reconciles live Docker state sent every heartbeat.
// It only writes to DB if the state differs from what is stored.
func handleContainerSync(payload map[string]any) {
	containerName, _ := payload["container_name"].(string)
	latticeStatus, _ := payload["status"].(string)
	if containerName == "" || latticeStatus == "" {
		return
	}

	c, err := containerNameCache.GetContainerByName(containerName)
	if err != nil {
		// Try stripping a 6-char deploy suffix (e.g., "myapp-x7k2m9" -> "myapp")
		if canonical := stripDeploySuffix(containerName); canonical != containerName {
			c, err = containerNameCache.GetContainerByName(canonical)
		}
		if err != nil {
			// Container not managed by Lattice — ignore
			return
		}
	}

	req := query.UpdateContainerRequest{}
	changed := false

	if c.Status != latticeStatus {
		req.Status = &latticeStatus
		changed = true
		// Track when container started running
		if latticeStatus == "running" && c.Status != "running" {
			now := time.Now().UTC()
			req.StartedAt = &now
		}
	}

	// If the container is no longer running (and not just paused), clear any stale health status.
	if latticeStatus != "running" && latticeStatus != "paused" && c.HealthStatus != "none" {
		none := "none"
		req.HealthStatus = &none
		changed = true
	}

	// If the worker reports a health_check config and we don't have one stored, persist it.
	if hcRaw, ok := payload["health_check"]; ok && hcRaw != nil && c.HealthCheck == nil {
		if hcBytes, err := json.Marshal(hcRaw); err == nil {
			hcStr := string(hcBytes)
			req.HealthCheck = &hcStr
			changed = true
		}
	}

	if !changed {
		return
	}

	if _, err := query.UpdateContainer(db.DB, c.ID, req); err != nil {
		logger.ErrorCtx(context.Background(), "container", "sync failed to update", logger.F{"container_name": containerName, "status": latticeStatus, "error": err})
	} else {
		containerNameCache.Invalidate(c.Name)
		logger.DebugCtx(context.Background(), "container", "sync updated", logger.F{"container_name": containerName, "status": latticeStatus, "previous_status": c.Status})
	}
}

func handleContainerLog(workerID int, payload map[string]any) {
	message, ok := payload["message"].(string)
	if !ok || message == "" {
		return
	}

	req := query.CreateContainerLogRequest{
		WorkerID: workerID,
		Message:  message,
		Stream:   "stdout",
	}

	if v, ok := payload["stream"].(string); ok {
		req.Stream = v
	}

	// Resolve container_name to container_id and always store the name
	if name, ok := payload["container_name"].(string); ok && name != "" {
		req.ContainerName = &name
		if c, err := containerNameCache.GetContainerByName(name); err == nil {
			req.ContainerID = &c.ID
		} else {
			logger.WarnCtx(context.Background(), "container", "could not resolve container name to ID", logger.F{"container_name": name, "error": err})
		}
	}

	// Use the Docker-recorded timestamp when provided by the runner so that
	// reconnect replays of the same line land on the same recorded_at value
	// and are silently dropped by the unique index in the DB.
	if ts, ok := payload["recorded_at"].(string); ok && ts != "" {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			req.RecordedAt = &t
		}
	}

	if err := query.CreateContainerLog(db.DB, req); err != nil {
		logger.ErrorCtx(context.Background(), "container", "failed to store container log", logger.F{"worker_id": workerID, "error": err})
	}
}

// writeWorkerLifecycleLogs writes a system log entry to every container
// belonging to workerID. Used for shutdown and crash events so the log viewer
// shows what happened to the runner.
func writeWorkerLifecycleLogs(workerID int, event string, message string) {
	containers, err := query.ListAllContainers(db.DB, query.ListAllContainersRequest{WorkerID: &workerID})
	if err != nil || containers == nil {
		if err != nil {
			logger.ErrorCtx(context.Background(), "worker", "failed to list containers for lifecycle log", logger.F{"worker_id": workerID, "error": err})
		}
		return
	}
	for _, c := range *containers {
		cID := c.ID
		cName := c.Name
		logReq := query.CreateLifecycleLogRequest{
			WorkerID:      workerID,
			ContainerID:   &cID,
			ContainerName: &cName,
			Event:         event,
			Message:       message,
		}
		if err := query.CreateLifecycleLog(db.DB, logReq); err != nil {
			logger.ErrorCtx(context.Background(), "worker", "failed to write lifecycle log for container", logger.F{"container_name": cName, "worker_id": workerID, "error": err})
		}
	}
}
