package routers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

// workerChartPoints is roughly how many points a ranged worker chart gets.
const workerChartPoints = 300

func HandleGetWorkerMetrics(w http.ResponseWriter, r *http.Request) {
	workerID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid worker id")
		return
	}

	limit := 100
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	req := query.ListMetricsRequest{
		WorkerID: workerID,
		Limit:    limit,
	}

	if rangeStr := r.URL.Query().Get("range"); rangeStr != "" {
		rangeDurations := map[string]time.Duration{
			"1h":  time.Hour,
			"6h":  6 * time.Hour,
			"24h": 24 * time.Hour,
			"7d":  7 * 24 * time.Hour,
		}
		if d, ok := rangeDurations[rangeStr]; ok {
			// Ranged charts get the whole window in ~300 buckets; the newest
			// 500 raw samples only reached back ~80 minutes.
			metrics, err := query.ListMetricsBucketed(db.DB, workerID, d, workerChartPoints)
			if err != nil {
				responder.QueryError(w, err, "failed to list worker metrics")
				return
			}
			responder.New(w, metrics, "worker metrics retrieved")
			return
		}
	}

	metrics, err := query.ListMetrics(db.DB, req)
	if err != nil {
		responder.QueryError(w, err, "failed to list worker metrics")
		return
	}

	responder.New(w, metrics, "worker metrics retrieved")
}
