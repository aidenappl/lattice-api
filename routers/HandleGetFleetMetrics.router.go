package routers

import (
	"net/http"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleGetFleetMetrics(w http.ResponseWriter, r *http.Request) {
	rangeStr := r.URL.Query().Get("range")
	var since time.Time
	points := 24

	switch rangeStr {
	case "1h":
		since = time.Now().Add(-1 * time.Hour)
		points = 60
	case "6h":
		since = time.Now().Add(-6 * time.Hour)
		points = 72
	case "24h", "":
		since = time.Now().Add(-24 * time.Hour)
		points = 96
	case "7d":
		since = time.Now().Add(-7 * 24 * time.Hour)
		points = 168
	default:
		since = time.Now().Add(-24 * time.Hour)
		points = 96
	}

	history, err := query.GetFleetMetricsHistory(db.DB, since, points)
	if err != nil {
		responder.QueryError(w, err, "failed to fetch fleet metrics history")
		return
	}

	responder.New(w, history, "fleet metrics history retrieved")
}
