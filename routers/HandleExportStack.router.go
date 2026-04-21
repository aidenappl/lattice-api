package routers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/gorilla/mux"
)

type containerExportData struct {
	Name          string  `json:"name"`
	Image         string  `json:"image"`
	Tag           string  `json:"tag"`
	PortMappings  *string `json:"port_mappings"`
	EnvVars       *string `json:"env_vars"`
	Volumes       *string `json:"volumes"`
	CPULimit      *float64 `json:"cpu_limit"`
	MemoryLimit   *int    `json:"memory_limit"`
	Replicas      int     `json:"replicas"`
	RestartPolicy *string `json:"restart_policy"`
	Command       *string `json:"command"`
	Entrypoint    *string `json:"entrypoint"`
	HealthCheck   *string `json:"health_check"`
	RegistryID    *int    `json:"registry_id"`
	DependsOn     *string `json:"depends_on"`
}

func formatContainersForExport(containers []structs.Container) []containerExportData {
	out := make([]containerExportData, 0, len(containers))
	for _, c := range containers {
		out = append(out, containerExportData{
			Name:          c.Name,
			Image:         c.Image,
			Tag:           c.Tag,
			PortMappings:  c.PortMappings,
			EnvVars:       c.EnvVars,
			Volumes:       c.Volumes,
			CPULimit:      c.CPULimit,
			MemoryLimit:   c.MemoryLimit,
			Replicas:      c.Replicas,
			RestartPolicy: c.RestartPolicy,
			Command:       c.Command,
			Entrypoint:    c.Entrypoint,
			HealthCheck:   c.HealthCheck,
			RegistryID:    c.RegistryID,
			DependsOn:     c.DependsOn,
		})
	}
	return out
}

func HandleExportStack(w http.ResponseWriter, r *http.Request) {
	stackID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid stack id")
		return
	}

	stack, err := query.GetStackByID(db.DB, stackID)
	if err != nil {
		responder.NotFound(w)
		return
	}

	containers, err := query.ListContainersByStack(db.DB, stackID)
	if err != nil {
		responder.QueryError(w, err, "failed to list containers")
		return
	}

	export := map[string]any{
		"version": "1",
		"stack": map[string]any{
			"name":                  stack.Name,
			"description":           stack.Description,
			"deployment_strategy":    stack.DeploymentStrategy,
			"auto_deploy":           stack.AutoDeploy,
			"env_vars":              stack.EnvVars,
			"compose_yaml":          stack.ComposeYAML,
			"placement_constraints": stack.PlacementConstraints,
		},
		"containers": formatContainersForExport(*containers),
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-export.json"`, stack.Name))
	responder.New(w, export, "stack exported")
}
