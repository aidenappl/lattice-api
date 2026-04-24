package routers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		responder.New(w, &query.SearchResults{}, "search results")
		return
	}
	// Truncate long search queries to prevent excessive LIKE pattern matching
	if len(q) > 200 {
		q = q[:200]
	}

	limit := 8
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	results, err := query.Search(db.DB, q, limit)
	if err != nil {
		responder.QueryError(w, err, "failed to search")
		return
	}

	responder.New(w, results, "search results")
}
