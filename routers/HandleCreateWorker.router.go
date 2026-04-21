package routers

import (
	"encoding/json"
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/tools"
)

func HandleCreateWorker(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string  `json:"name"`
		Hostname  string  `json:"hostname"`
		IPAddress *string `json:"ip_address"`
		Labels    *string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.Name == "" {
		responder.MissingBodyFields(w, "name")
		return
	}
	if err := tools.ValidateName(body.Name); err != nil {
		responder.SendError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Hostname == "" {
		responder.MissingBodyFields(w, "hostname")
		return
	}

	worker, err := query.CreateWorker(db.DB, query.CreateWorkerRequest{
		Name:      body.Name,
		Hostname:  body.Hostname,
		IPAddress: body.IPAddress,
		Labels:    body.Labels,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create worker")
		return
	}

	logAudit(r, "create", "worker", intPtr(worker.ID), strPtr(worker.Name))
	responder.NewCreated(w, worker, "worker created")
}
