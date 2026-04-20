package routers

import (
	"encoding/json"
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleCreateStack(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name               string  `json:"name"`
		Description        *string `json:"description"`
		WorkerID           *int    `json:"worker_id"`
		DeploymentStrategy string  `json:"deployment_strategy"`
		AutoDeploy         bool    `json:"auto_deploy"`
		EnvVars            *string `json:"env_vars"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.Name == "" {
		responder.MissingBodyFields(w, "name")
		return
	}
	if body.DeploymentStrategy == "" {
		body.DeploymentStrategy = "rolling"
	}

	stack, err := query.CreateStack(db.DB, query.CreateStackRequest{
		Name:               body.Name,
		Description:        body.Description,
		WorkerID:           body.WorkerID,
		DeploymentStrategy: body.DeploymentStrategy,
		AutoDeploy:         body.AutoDeploy,
		EnvVars:            body.EnvVars,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create stack")
		return
	}

	logAudit(r, "create", "stack", intPtr(stack.ID), strPtr(stack.Name))
	responder.NewCreated(w, stack, "stack created")
}
