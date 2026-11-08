package routers

import (
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

// HandleGetContainerLogVolume reports bytes and lines stored per container per
// UTC day, largest first (APL-120): which containers fill container_logs.
// ?days=1..7 (default 1, today), ?limit= (default 50, max 500).
func HandleGetContainerLogVolume(w http.ResponseWriter, r *http.Request) {
	req := query.ContainerLogVolumeRequest{}
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > query.LOG_VOLUME_MAX_DAYS {
			responder.SendError(w, http.StatusBadRequest, "days must be between 1 and 7")
			return
		}
		req.Days = n
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			responder.BadBody(w, err)
			return
		}
		req.Limit = n
	}

	volume, err := query.ContainerLogVolume(db.DB, req)
	if err != nil {
		responder.QueryError(w, err, "failed to report container log volume")
		return
	}
	responder.New(w, volume)
}
