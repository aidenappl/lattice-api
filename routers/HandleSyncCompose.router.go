package routers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
	"gopkg.in/yaml.v3"
)

// HandleSyncCompose re-parses the stored compose YAML for a stack and
// patches existing containers with any fields defined in the compose
// (health_check, env_vars, ports, volumes, restart policy, etc.)
// without deleting or recreating containers and without touching status.
func HandleSyncCompose(w http.ResponseWriter, r *http.Request) {
	stackID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid stack id")
		return
	}

	stack, err := query.GetStackByID(db.DB, stackID)
	if err != nil {
		responder.QueryError(w, err, "stack not found")
		return
	}
	if stack.ComposeYAML == nil || *stack.ComposeYAML == "" {
		responder.SendError(w, http.StatusBadRequest, "stack has no compose YAML stored")
		return
	}

	var compose composeFile
	if err := yaml.Unmarshal([]byte(*stack.ComposeYAML), &compose); err != nil {
		responder.SendError(w, http.StatusBadRequest, "failed to parse stored compose YAML: "+err.Error())
		return
	}
	if len(compose.Services) == 0 {
		responder.SendError(w, http.StatusBadRequest, "no services found in compose YAML")
		return
	}

	existing, err := query.ListContainersByStack(db.DB, stackID)
	if err != nil {
		responder.QueryError(w, err, "failed to list containers")
		return
	}

	// Build a map of containerName → container for fast lookup
	containersByName := make(map[string]int) // name → id
	if existing != nil {
		for _, c := range *existing {
			containersByName[c.Name] = c.ID
		}
	}

	type syncResult struct {
		ContainerName string `json:"container_name"`
		Updated       bool   `json:"updated"`
		Reason        string `json:"reason,omitempty"`
	}
	results := make([]syncResult, 0, len(compose.Services))

	for svcKey, svc := range compose.Services {
		containerName := svcKey
		if svc.ContainerName != "" {
			containerName = svc.ContainerName
		}

		cID, found := containersByName[containerName]
		if !found {
			results = append(results, syncResult{
				ContainerName: containerName,
				Updated:       false,
				Reason:        "no matching container found in stack",
			})
			continue
		}

		req := query.UpdateContainerRequest{}

		// Health check — normalize Test to ["CMD-SHELL", "command"] format
		if svc.Healthcheck != nil && !svc.Healthcheck.Disable {
			svc.Healthcheck.Test = normalizeHealthTest(svc.Healthcheck.Test)
			b, _ := json.Marshal(svc.Healthcheck)
			s := string(b)
			req.HealthCheck = &s
		} else if svc.Healthcheck != nil && svc.Healthcheck.Disable {
			empty := ""
			req.HealthCheck = &empty
		}

		// Restart policy
		if svc.Restart != "" {
			req.RestartPolicy = &svc.Restart
		}

		// Port mappings
		if len(svc.Ports) > 0 {
			portMappings := make([]map[string]string, 0, len(svc.Ports))
			for _, p := range svc.Ports {
				pm := parsePortMapping(p)
				if pm != nil {
					portMappings = append(portMappings, pm)
				}
			}
			if len(portMappings) > 0 {
				b, _ := json.Marshal(portMappings)
				s := string(b)
				req.PortMappings = &s
			}
		}

		// Environment variables
		envMap := parseComposeEnv(svc.Environment)
		if len(envMap) > 0 {
			b, _ := json.Marshal(envMap)
			s := string(b)
			req.EnvVars = &s
		}

		// Volumes
		if len(svc.Volumes) > 0 {
			volMap := make(map[string]string, len(svc.Volumes))
			for _, v := range svc.Volumes {
				parts := strings.SplitN(v, ":", 2)
				if len(parts) == 2 {
					volMap[parts[0]] = parts[1]
				}
			}
			if len(volMap) > 0 {
				b, _ := json.Marshal(volMap)
				s := string(b)
				req.Volumes = &s
			}
		}

		// Command
		cmd := parseStringOrList(svc.Command)
		if len(cmd) > 0 {
			b, _ := json.Marshal(cmd)
			s := string(b)
			req.Command = &s
		}

		// Entrypoint
		ep := parseStringOrList(svc.Entrypoint)
		if len(ep) > 0 {
			b, _ := json.Marshal(ep)
			s := string(b)
			req.Entrypoint = &s
		}

		// Deploy resource limits
		if svc.Deploy != nil && svc.Deploy.Resources != nil && svc.Deploy.Resources.Limits != nil {
			if svc.Deploy.Resources.Limits.CPUs != "" {
				var cpus float64
				fmt.Sscanf(svc.Deploy.Resources.Limits.CPUs, "%f", &cpus)
				if cpus > 0 {
					req.CPULimit = &cpus
				}
			}
			if svc.Deploy.Resources.Limits.Memory != "" {
				mem := parseMemoryString(svc.Deploy.Resources.Limits.Memory)
				if mem > 0 {
					req.MemoryLimit = &mem
				}
			}
		}

		if _, err := query.UpdateContainer(db.DB, cID, req); err != nil {
			results = append(results, syncResult{
				ContainerName: containerName,
				Updated:       false,
				Reason:        "db update failed: " + err.Error(),
			})
			continue
		}

		results = append(results, syncResult{ContainerName: containerName, Updated: true})
	}

	logAudit(r, "sync_compose", "stack", intPtr(stackID), nil)
	responder.New(w, results, "compose sync complete")
}
