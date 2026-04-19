package routers

import (
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

// Set from main at startup via ldflags.
var APIVersion string
var LatestRunnerVer string
var LatestWebVer string

func HandleGetVersions(w http.ResponseWriter, r *http.Request) {
	result, err := query.ListWorkers(db.DB, query.ListWorkersRequest{Limit: 500})
	if err != nil {
		responder.QueryError(w, err, "failed to list workers")
		return
	}

	type workerVersion struct {
		WorkerID      int     `json:"worker_id"`
		Name          string  `json:"name"`
		RunnerVersion *string `json:"runner_version"`
		Status        string  `json:"status"`
		Outdated      bool    `json:"outdated"`
	}

	// Fall back to API version when ldflags weren't set.
	latest := LatestRunnerVer
	if latest == "" {
		latest = APIVersion
	}

	webLatest := LatestWebVer
	if webLatest == "" {
		webLatest = APIVersion
	}

	workers := *result
	wv := make([]workerVersion, 0, len(workers))
	outdatedCount := 0
	for _, wk := range workers {
		outdated := false
		if wk.RunnerVersion != nil && *wk.RunnerVersion != latest {
			outdated = true
			outdatedCount++
		}
		wv = append(wv, workerVersion{
			WorkerID:      wk.ID,
			Name:          wk.Name,
			RunnerVersion: wk.RunnerVersion,
			Status:        wk.Status,
			Outdated:      outdated,
		})
	}

	responder.New(w, map[string]any{
		"api": map[string]any{
			"current": APIVersion,
		},
		"web": map[string]any{
			"latest": webLatest,
		},
		"runner": map[string]any{
			"latest":         latest,
			"workers":        wv,
			"outdated_count": outdatedCount,
		},
	})
}
