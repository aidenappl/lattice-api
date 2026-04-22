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

func HandleUpdateContainer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid container id")
		return
	}

	var body struct {
		Name          *string  `json:"name"`
		Image         *string  `json:"image"`
		Tag           *string  `json:"tag"`
		Status        *string  `json:"status"`
		PortMappings  *string  `json:"port_mappings"`
		EnvVars       *string  `json:"env_vars"`
		Volumes       *string  `json:"volumes"`
		CPULimit      *float64 `json:"cpu_limit"`
		MemoryLimit   *int     `json:"memory_limit"`
		Replicas      *int     `json:"replicas"`
		RestartPolicy *string  `json:"restart_policy"`
		Command       *string  `json:"command"`
		Entrypoint    *string  `json:"entrypoint"`
		HealthCheck   *string  `json:"health_check"`
		RegistryID    *int     `json:"registry_id"`
		Active        *bool    `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	container, err := query.UpdateContainer(db.DB, id, query.UpdateContainerRequest{
		Name:          body.Name,
		Image:         body.Image,
		Tag:           body.Tag,
		Status:        body.Status,
		PortMappings:  body.PortMappings,
		EnvVars:       body.EnvVars,
		Volumes:       body.Volumes,
		CPULimit:      body.CPULimit,
		MemoryLimit:   body.MemoryLimit,
		Replicas:      body.Replicas,
		RestartPolicy: body.RestartPolicy,
		Command:       body.Command,
		Entrypoint:    body.Entrypoint,
		HealthCheck:   body.HealthCheck,
		RegistryID:    body.RegistryID,
		Active:        body.Active,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to update container")
		return
	}

	logAudit(r, "update", "container", intPtr(id), nil)
	responder.New(w, container, "container updated")
}
