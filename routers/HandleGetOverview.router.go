package routers

import (
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleGetOverview(w http.ResponseWriter, r *http.Request) {
	activeTrue := true

	allWorkers, err := query.ListWorkers(db.DB, query.ListWorkersRequest{Limit: 10000})
	if err != nil {
		responder.QueryError(w, err, "failed to count workers")
		return
	}

	onlineStatus := "online"
	onlineWorkers, err := query.ListWorkers(db.DB, query.ListWorkersRequest{
		Status: &onlineStatus,
		Active: &activeTrue,
		Limit:  10000,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to count online workers")
		return
	}

	allStacks, err := query.ListStacks(db.DB, query.ListStacksRequest{Limit: 10000})
	if err != nil {
		responder.QueryError(w, err, "failed to count stacks")
		return
	}

	activeStacks, err := query.ListStacks(db.DB, query.ListStacksRequest{
		Active: &activeTrue,
		Limit:  10000,
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

	responder.New(w, map[string]any{
		"total_workers":          totalWorkers,
		"online_workers":         onlineWorkerCount,
		"total_stacks":           totalStacks,
		"active_stacks":          activeStackCount,
		"recent_deployments":     recentDeployments,
		"recent_deployment_count": recentDeploymentCount,
	}, "overview retrieved")
}
