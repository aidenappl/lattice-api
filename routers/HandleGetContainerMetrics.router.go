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

func HandleGetContainerMetrics(w http.ResponseWriter, r *http.Request) {
	containerID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid container id")
		return
	}

	limit := 100
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	req := query.ListContainerMetricsRequest{
		ContainerID: containerID,
		Limit:       limit,
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
			req.Limit = 500
		}
	}

	metrics, err := query.ListContainerMetrics(db.DB, req)
	if err != nil {
		responder.QueryError(w, err, "failed to list container metrics")
		return
	}

	responder.New(w, metrics, "container metrics retrieved")
}
