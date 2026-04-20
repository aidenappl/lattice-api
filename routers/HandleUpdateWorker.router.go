package routers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

func HandleUpdateWorker(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid worker id")
		return
	}

	var body struct {
		Name      *string `json:"name"`
		Hostname  *string `json:"hostname"`
		IPAddress *string `json:"ip_address"`
		Status    *string `json:"status"`
		Labels    *string `json:"labels"`
		Active    *bool   `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	worker, err := query.UpdateWorker(db.DB, id, query.UpdateWorkerRequest{
		Name:      body.Name,
		Hostname:  body.Hostname,
		IPAddress: body.IPAddress,
		Status:    body.Status,
		Labels:    body.Labels,
		Active:    body.Active,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to update worker")
		return
	}

	logAudit(r, "update", "worker", intPtr(id), nil)
	responder.New(w, worker, "worker updated")
}
