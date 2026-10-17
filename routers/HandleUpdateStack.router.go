package routers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/gorilla/mux"
)

func HandleUpdateStack(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid stack id")
		return
	}

	var body struct {
		Name                 *string `json:"name"`
		Description          *string `json:"description"`
		WorkerID             *int    `json:"worker_id"`
		Status               *string `json:"status"`
		DeploymentStrategy   *string `json:"deployment_strategy"`
		AutoDeploy           *bool   `json:"auto_deploy"`
		EnvVars              *string `json:"env_vars"`
		SecretEnvKeys        *string `json:"secret_env_keys"`
		PlacementConstraints *string `json:"placement_constraints"`
		Active               *bool   `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	if body.Status != nil {
		validStatuses := map[string]bool{"active": true, "stopped": true, "deploying": true, "deployed": true, "failed": true, "error": true}
		if !validStatuses[*body.Status] {
			responder.SendError(w, http.StatusBadRequest, "invalid status value")
			return
		}
	}
	if body.DeploymentStrategy != nil && !structs.DeploymentStrategy(*body.DeploymentStrategy).IsValid() {
		responder.SendError(w, http.StatusBadRequest, structs.INVALID_DEPLOYMENT_STRATEGY)
		return
	}

	// Secret values come back to non-admins (and to the web UI until revealed)
	// as "***"; saving that must keep the stored value, not overwrite it.
	envVars := body.EnvVars
	if envVars != nil {
		existing, err := query.GetStackByID(db.DB, id)
		if err != nil {
			responder.NotFound(w)
			return
		}
		merged, err := mergeMaskedEnvVars(existing.EnvVars, *envVars)
		if err != nil {
			responder.SendError(w, http.StatusBadRequest, err.Error())
			return
		}
		envVars = &merged
	}
	var secretKeys *string
	if body.SecretEnvKeys != nil {
		// Only admins can see secret values, so only admins may un-mark one:
		// otherwise an editor could reveal a secret by clearing its flag.
		norm, err := normalizeSecretKeys(*body.SecretEnvKeys)
		if err != nil {
			responder.SendError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !canReadSecrets(r) {
			existing, err := query.GetStackByID(db.DB, id)
			if err != nil {
				responder.NotFound(w)
				return
			}
			next := parseSecretKeys(&norm)
			for k := range parseSecretKeys(existing.SecretEnvKeys) {
				if !next[k] {
					responder.SendError(w, http.StatusForbidden, "only admins can unmark a secret env var")
					return
				}
			}
		}
		secretKeys = &norm
	}

	stack, err := query.UpdateStack(db.DB, id, query.UpdateStackRequest{
		Name:                 body.Name,
		Description:          body.Description,
		WorkerID:             body.WorkerID,
		Status:               body.Status,
		DeploymentStrategy:   body.DeploymentStrategy,
		AutoDeploy:           body.AutoDeploy,
		EnvVars:              envVars,
		SecretEnvKeys:        secretKeys,
		PlacementConstraints: body.PlacementConstraints,
		Active:               body.Active,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to update stack")
		return
	}

	logAudit(r, "update", "stack", intPtr(id), nil)
	responder.New(w, stackForUser(r, stack), "stack updated")
}
