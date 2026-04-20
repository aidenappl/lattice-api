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
			since := time.Now().Add(-d)
			req.Since = &since
			req.Limit = 500 // allow more data points for time range queries
		}
	}

	metrics, err := query.ListMetrics(db.DB, req)
	if err != nil {
		responder.QueryError(w, err, "failed to list worker metrics")
		return
	}

	responder.New(w, metrics, "worker metrics retrieved")
}
