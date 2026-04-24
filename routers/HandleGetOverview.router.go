package routers

import (
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleGetOverview(w http.ResponseWriter, r *http.Request) {
	activeTrue := true

	allWorkers, err := query.ListWorkers(db.DB, query.ListWorkersRequest{Limit: db.MAX_LIMIT})
	if err != nil {
		responder.QueryError(w, err, "failed to count workers")
		return
	}

	onlineStatus := "online"
	onlineWorkers, err := query.ListWorkers(db.DB, query.ListWorkersRequest{
		Status: &onlineStatus,
		Active: &activeTrue,
		Limit:  db.MAX_LIMIT,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to count online workers")
		return
	}

	allStacks, err := query.ListStacks(db.DB, query.ListStacksRequest{Limit: db.MAX_LIMIT})
	if err != nil {
		responder.QueryError(w, err, "failed to count stacks")
		return
	}

	activeStacks, err := query.ListStacks(db.DB, query.ListStacksRequest{
		Active: &activeTrue,
		Limit:  db.MAX_LIMIT,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to count active stacks")
		return
	}

	recentDeployments, err := query.ListDeployments(db.DB, query.ListDeploymentsRequest{Limit: 10})
	if err != nil {
		responder.QueryError(w, err, "failed to list recent deployments")
		return
	}

	// Get all containers for total/running counts
	allContainers, err := query.ListAllContainers(db.DB, query.ListAllContainersRequest{})
	if err != nil {
		responder.QueryError(w, err, "failed to count containers")
		return
	}

	// Get fleet metrics (latest per online worker)
	fleetMetrics, err := query.GetLatestMetricsForAllWorkers(db.DB)
	if err != nil {
		// Non-fatal: fleet metrics might be empty on fresh installs
		fleetMetrics = nil
	}

	totalWorkers := 0
	if allWorkers != nil {
		totalWorkers = len(*allWorkers)
	}
	onlineWorkerCount := 0
	if onlineWorkers != nil {
		onlineWorkerCount = len(*onlineWorkers)
	}
	totalStacks := 0
	if allStacks != nil {
		totalStacks = len(*allStacks)
	}
	activeStackCount := 0
	if activeStacks != nil {
		activeStackCount = len(*activeStacks)
	}
	recentDeploymentCount := 0
	if recentDeployments != nil {
		recentDeploymentCount = len(*recentDeployments)
	}

	totalContainers := 0
	runningContainers := 0
	if allContainers != nil {
		totalContainers = len(*allContainers)
		for _, c := range *allContainers {
			if c.Status == "running" {
				runningContainers++
			}
		}
	}

	// Aggregate fleet metrics
	var cpuAvg, memAvg float64
	var totalContainerCount, totalRunningCount int
	if len(fleetMetrics) > 0 {
		var cpuSum, memSum float64
		var cpuCount, memCount int
		for _, m := range fleetMetrics {
			if m.CPUPercent != nil {
				cpuSum += *m.CPUPercent
				cpuCount++
			}
			if m.MemoryUsedMB != nil && m.MemoryTotalMB != nil && *m.MemoryTotalMB > 0 {
				memSum += (*m.MemoryUsedMB / *m.MemoryTotalMB) * 100
				memCount++
			}
			if m.ContainerCount != nil {
				totalContainerCount += *m.ContainerCount
			}
			if m.ContainerRunningCount != nil {
				totalRunningCount += *m.ContainerRunningCount
			}
		}
		if cpuCount > 0 {
			cpuAvg = cpuSum / float64(cpuCount)
		}
		if memCount > 0 {
			memAvg = memSum / float64(memCount)
		}
	}

	// Build per-worker metrics summary for fleet resource panel
	type workerMetricsSummary struct {
		WorkerID   int      `json:"worker_id"`
		WorkerName string   `json:"worker_name"`
		CPU        *float64 `json:"cpu"`
		Memory     *float64 `json:"memory"`
		DiskUsed   *float64 `json:"disk_used"`
		DiskTotal  *float64 `json:"disk_total"`
		NetRxRate  *float64 `json:"net_rx_rate"`
		NetTxRate  *float64 `json:"net_tx_rate"`
		Containers *int     `json:"containers"`
		Running    *int     `json:"running"`
		Status     string   `json:"status"`
	}
	var workerSummaries []workerMetricsSummary
	if onlineWorkers != nil && len(fleetMetrics) > 0 {
		workerMap := make(map[int]string)
		for _, w := range *onlineWorkers {
			workerMap[w.ID] = w.Name
		}
		for _, m := range fleetMetrics {
			name := workerMap[m.WorkerID]
			if name == "" {
				continue
			}
			var memPct *float64
			if m.MemoryUsedMB != nil && m.MemoryTotalMB != nil && *m.MemoryTotalMB > 0 {
				pct := (*m.MemoryUsedMB / *m.MemoryTotalMB) * 100
				memPct = &pct
			}
			workerSummaries = append(workerSummaries, workerMetricsSummary{
				WorkerID:   m.WorkerID,
				WorkerName: name,
				CPU:        m.CPUPercent,
				Memory:     memPct,
				DiskUsed:   m.DiskUsedMB,
				DiskTotal:  m.DiskTotalMB,
				NetRxRate:  m.NetworkRxRate,
				NetTxRate:  m.NetworkTxRate,
				Containers: m.ContainerCount,
				Running:    m.ContainerRunningCount,
				Status:     "online",
			})
		}
	}

	// Count deploying/failed stacks
	deployingStacks := 0
	failedStacks := 0
	if allStacks != nil {
		for _, s := range *allStacks {
			switch s.Status {
			case "deploying":
				deployingStacks++
			case "failed", "error":
				failedStacks++
			}
		}
	}

	responder.New(w, map[string]any{
		"total_workers":           totalWorkers,
		"online_workers":          onlineWorkerCount,
		"total_stacks":            totalStacks,
		"active_stacks":           activeStackCount,
		"deploying_stacks":        deployingStacks,
		"failed_stacks":           failedStacks,
		"total_containers":        totalContainers,
		"running_containers":      runningContainers,
		"recent_deployments":      recentDeployments,
		"recent_deployment_count": recentDeploymentCount,
		"fleet_cpu_avg":           cpuAvg,
		"fleet_memory_avg":        memAvg,
		"fleet_container_count":   totalContainerCount,
		"fleet_running_count":     totalRunningCount,
		"worker_metrics":          workerSummaries,
	}, "overview retrieved")
}
