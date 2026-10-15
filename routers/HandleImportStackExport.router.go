package routers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/tools"
)

func HandleImportStackExport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version string `json:"version"`
		Stack   struct {
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
			Ulimits       *string  `json:"ulimits"`
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

	// Begin transaction — if container creation fails, the stack is rolled back too
	tx, err := db.BeginTx()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to start transaction", err)
		return
	}
	defer tx.Rollback() // no-op if committed

	// Create the stack
	stack, err := query.CreateStack(tx, query.CreateStackRequest{
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
		command, err := commandFromBody("command", c.Command)
		if err != nil {
			responder.SendError(w, http.StatusBadRequest, fmt.Sprintf("container %s: %v", c.Name, err))
			return
		}
		entrypoint, err := commandFromBody("entrypoint", c.Entrypoint)
		if err != nil {
			responder.SendError(w, http.StatusBadRequest, fmt.Sprintf("container %s: %v", c.Name, err))
			return
		}
		ulimits, err := ulimitsFromBody(c.Ulimits)
		if err != nil {
			responder.SendError(w, http.StatusBadRequest, fmt.Sprintf("container %s: %v", c.Name, err))
			return
		}
		_, err = query.CreateContainer(tx, query.CreateContainerRequest{
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
			Command:       command,
			Entrypoint:    entrypoint,
			HealthCheck:   c.HealthCheck,
			Ulimits:       ulimits,
			RegistryID:    c.RegistryID,
			DependsOn:     c.DependsOn,
		})
		if err != nil {
			responder.QueryError(w, err, "failed to create container from import")
			return
		}
	}

	// Networks: an export carries them only inside compose_yaml, so create the
	// records from it the way a compose import does. Without this, a stack
	// imported from an export (or created from a template) had no networks
	// until the next restart's backfill.
	if body.Stack.ComposeYAML != nil {
		nets, err := composeNetworkRequests(stack.ID, *body.Stack.ComposeYAML)
		if err != nil {
			responder.SendError(w, http.StatusBadRequest, err.Error())
			return
		}
		for _, n := range nets {
			if err := query.CreateNetwork(tx, n); err != nil {
				responder.SendError(w, http.StatusInternalServerError, "failed to create stack network", err)
				return
			}
		}
	}

	if err := tx.Commit(); err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to commit transaction", err)
		return
	}

	logAudit(r, "import", "stack", intPtr(stack.ID), strPtr(body.Stack.Name))
	responder.NewCreated(w, stack, "stack imported")
}
