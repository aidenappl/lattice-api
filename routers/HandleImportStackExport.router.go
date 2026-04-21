package routers

import (
	"encoding/json"
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/tools"
)

func HandleImportStackExport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version    string `json:"version"`
		Stack      struct {
			Name                 string  `json:"name"`
			Description          *string `json:"description"`
			DeploymentStrategy   string  `json:"deployment_strategy"`
			AutoDeploy           bool    `json:"auto_deploy"`
			EnvVars              *string `json:"env_vars"`
			ComposeYAML          *string `json:"compose_yaml"`
			PlacementConstraints *string `json:"placement_constraints"`
		} `json:"stack"`
		Containers []struct {
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
			HealthCheck   *string  `json:"health_check"`
			RegistryID    *int     `json:"registry_id"`
			DependsOn     *string  `json:"depends_on"`
		} `json:"containers"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	if body.Stack.Name == "" {
		responder.MissingBodyFields(w, "stack.name")
		return
	}

	if err := tools.ValidateName(body.Stack.Name); err != nil {
		responder.SendError(w, http.StatusBadRequest, err.Error())
		return
	}

	strategy := body.Stack.DeploymentStrategy
	if strategy == "" {
		strategy = "rolling"
	}

	// Create the stack
	stack, err := query.CreateStack(db.DB, query.CreateStackRequest{
		Name:                 body.Stack.Name,
		Description:          body.Stack.Description,
		DeploymentStrategy:   strategy,
		AutoDeploy:           body.Stack.AutoDeploy,
		EnvVars:              body.Stack.EnvVars,
		ComposeYAML:          body.Stack.ComposeYAML,
		PlacementConstraints: body.Stack.PlacementConstraints,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create stack from import")
		return
	}

	// Create containers
	for _, c := range body.Containers {
		if c.Name == "" || c.Image == "" {
			continue
		}
		tag := c.Tag
		if tag == "" {
			tag = "latest"
		}
		replicas := c.Replicas
		if replicas <= 0 {
			replicas = 1
		}
		_, err := query.CreateContainer(db.DB, query.CreateContainerRequest{
			StackID:       stack.ID,
			Name:          c.Name,
			Image:         c.Image,
			Tag:           tag,
			PortMappings:  c.PortMappings,
			EnvVars:       c.EnvVars,
			Volumes:       c.Volumes,
			CPULimit:      c.CPULimit,
			MemoryLimit:   c.MemoryLimit,
			Replicas:      replicas,
			RestartPolicy: c.RestartPolicy,
			Command:       c.Command,
			Entrypoint:    c.Entrypoint,
			HealthCheck:   c.HealthCheck,
			RegistryID:    c.RegistryID,
			DependsOn:     c.DependsOn,
		})
		if err != nil {
			responder.QueryError(w, err, "failed to create container from import")
			return
		}
	}

	logAudit(r, "import", "stack", intPtr(stack.ID), strPtr(body.Stack.Name))
	responder.NewCreated(w, stack, "stack imported")
}
