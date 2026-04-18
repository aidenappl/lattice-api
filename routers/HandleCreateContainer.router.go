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

func HandleCreateContainer(w http.ResponseWriter, r *http.Request) {
	stackID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid stack id")
		return
	}

	var body struct {
		Name          string   `json:"name"`
		Image         string   `json:"image"`
		Tag           string   `json:"tag"`
		PortMappings  *string  `json:"port_mappings"`
		EnvVars       *string  `json:"env_vars"`
		Volumes       *string  `json:"volumes"`
		CPULimit      *float64 `json:"cpu_limit"`
		MemoryLimit   *int     `json:"memory_limit"`
		Replicas      int      `json:"replicas"`
		RestartPolicy *string  `json:"restart_policy"`
		Command       *string  `json:"command"`
		Entrypoint    *string  `json:"entrypoint"`
		RegistryID    *int     `json:"registry_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.Name == "" {
		responder.MissingBodyFields(w, "name")
		return
	}
	if body.Image == "" {
		responder.MissingBodyFields(w, "image")
		return
	}
	if body.Tag == "" {
		body.Tag = "latest"
	}
	if body.Replicas <= 0 {
		body.Replicas = 1
	}

	container, err := query.CreateContainer(db.DB, query.CreateContainerRequest{
		StackID:       stackID,
		Name:          body.Name,
		Image:         body.Image,
		Tag:           body.Tag,
		PortMappings:  body.PortMappings,
		EnvVars:       body.EnvVars,
		Volumes:       body.Volumes,
		CPULimit:      body.CPULimit,
		MemoryLimit:   body.MemoryLimit,
		Replicas:      body.Replicas,
		RestartPolicy: body.RestartPolicy,
		Command:       body.Command,
		Entrypoint:    body.Entrypoint,
		RegistryID:    body.RegistryID,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create container")
		return
	}

	responder.NewCreated(w, container, "container created")
}
