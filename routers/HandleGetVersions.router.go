package routers

import (
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/versions"
)

// APIVersion is set from main at startup (via ldflags).
var APIVersion string

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

	latestRunner := versions.LatestRunner()
	latestWeb := versions.LatestWeb()
	latestAPI := versions.LatestAPI()

	workers := *result
	wv := make([]workerVersion, 0, len(workers))
	outdatedCount := 0
	for _, wk := range workers {
		outdated := false
		if wk.RunnerVersion != nil && latestRunner != "" && *wk.RunnerVersion != latestRunner {
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
			"latest":  latestAPI,
		},
		"web": map[string]any{
			"latest": latestWeb,
		},
		"runner": map[string]any{
			"latest":         latestRunner,
			"workers":        wv,
			"outdated_count": outdatedCount,
		},
		"last_checked": versions.LastChecked(),
	})
}

func HandleRefreshVersions(w http.ResponseWriter, r *http.Request) {
	versions.Refresh()
	logAudit(r, "refresh", "versions", nil, nil)
	responder.New(w, map[string]any{
		"api":          versions.LatestAPI(),
		"web":          versions.LatestWeb(),
		"runner":       versions.LatestRunner(),
		"last_checked": versions.LastChecked(),
	}, "Version cache refreshed")
}
