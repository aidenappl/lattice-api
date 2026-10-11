package routers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/tools"
	"github.com/gorilla/mux"
)

func HandleUpdateContainer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid container id")
		return
	}

	var body struct {
		Name         *string `json:"name"`
		Image        *string `json:"image"`
		Tag          *string `json:"tag"`
		Status       *string `json:"status"`
		PortMappings *string `json:"port_mappings"`
		EnvVars      *string `json:"env_vars"`
		Volumes      *string `json:"volumes"`
		// Absent leaves the limit alone; null removes it (tools.Optional).
		CPULimit      tools.Optional[float64] `json:"cpu_limit"`
		MemoryLimit   tools.Optional[int]     `json:"memory_limit"`
		Replicas      *int                    `json:"replicas"`
		RestartPolicy *string                 `json:"restart_policy"`
		Command       *string                 `json:"command"`
		Entrypoint    *string                 `json:"entrypoint"`
		HealthCheck   *string                 `json:"health_check"`
		Ulimits       *string                 `json:"ulimits"`
		RegistryID    tools.Optional[int]     `json:"registry_id"`
		DependsOn     *string                 `json:"depends_on"`
		Active        *bool                   `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	command, err := commandFromBody("command", body.Command)
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, err.Error())
		return
	}
	entrypoint, err := commandFromBody("entrypoint", body.Entrypoint)
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, err.Error())
		return
	}
	ulimits, err := ulimitsFromBody(body.Ulimits)
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Clearing the healthcheck ("") also clears the stale health status, which
	// nothing else would reset: with no configured check the container would keep
	// showing its last healthy/unhealthy verdict. A healthcheck baked into the
	// image still applies at the next deploy; the worker's sync reports it then.
	var healthStatus *string
	if body.HealthCheck != nil && *body.HealthCheck == "" {
		none := "none"
		healthStatus = &none
	}

	container, err := query.UpdateContainer(db.DB, id, query.UpdateContainerRequest{
		Name:          body.Name,
		Image:         body.Image,
		Tag:           body.Tag,
		Status:        body.Status,
		PortMappings:  body.PortMappings,
		EnvVars:       body.EnvVars,
		Volumes:       body.Volumes,
		CPULimit:      body.CPULimit.Value,
		MemoryLimit:   body.MemoryLimit.Value,
		Replicas:      body.Replicas,
		RestartPolicy: body.RestartPolicy,
		Command:       command,
		Entrypoint:    entrypoint,
		HealthCheck:   body.HealthCheck,
		HealthStatus:  healthStatus,
		Ulimits:       ulimits,
		RegistryID:    body.RegistryID.Value,
		DependsOn:     body.DependsOn,
		Active:        body.Active,

		ClearCPULimit:    body.CPULimit.Clear(),
		ClearMemoryLimit: body.MemoryLimit.Clear(),
		ClearRegistryID:  body.RegistryID.Clear(),
	})
	if err != nil {
		responder.QueryError(w, err, "failed to update container")
		return
	}

	logAudit(r, "update", "container", intPtr(id), nil)
	responder.New(w, container, "container updated")
}
