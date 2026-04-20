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

func HandleUpdateStack(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid stack id")
		return
	}

	var body struct {
		Name               *string `json:"name"`
		Description        *string `json:"description"`
		WorkerID           *int    `json:"worker_id"`
		Status             *string `json:"status"`
		DeploymentStrategy *string `json:"deployment_strategy"`
		AutoDeploy         *bool   `json:"auto_deploy"`
		EnvVars            *string `json:"env_vars"`
		Active             *bool   `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	stack, err := query.UpdateStack(db.DB, id, query.UpdateStackRequest{
		Name:               body.Name,
		Description:        body.Description,
		WorkerID:           body.WorkerID,
		Status:             body.Status,
		DeploymentStrategy: body.DeploymentStrategy,
		AutoDeploy:         body.AutoDeploy,
		EnvVars:            body.EnvVars,
		Active:             body.Active,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to update stack")
		return
	}

	logAudit(r, "update", "stack", intPtr(id), nil)
	responder.New(w, stack, "stack updated")
}
