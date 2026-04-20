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

func HandleUpdateCompose(w http.ResponseWriter, r *http.Request) {
	stackID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid stack id")
		return
	}

	var body struct {
		ComposeYAML string `json:"compose_yaml"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.ComposeYAML == "" {
		responder.MissingBodyFields(w, "compose_yaml")
		return
	}

	var compose composeFile
	if err := yaml.Unmarshal([]byte(body.ComposeYAML), &compose); err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid docker-compose YAML: "+err.Error())
		return
	}
	if len(compose.Services) == 0 {
		responder.SendError(w, http.StatusBadRequest, "no services found in compose file")
		return
	}

	// Verify stack exists
	stack, err := query.GetStackByID(db.DB, stackID)
	if err != nil {
		responder.QueryError(w, err, "stack not found")
		return
	}

	// Soft-delete existing containers
	existing, err := query.ListContainersByStack(db.DB, stack.ID)
	if err != nil {
		responder.QueryError(w, err, "failed to list containers")
		return
	}
	if existing != nil {
		for _, c := range *existing {
			_ = query.DeleteContainer(db.DB, c.ID)
		}
	}

	// Create containers from compose services
	for name, svc := range compose.Services {
		image, tag := parseImageRef(svc.Image)

		containerName := name
		if svc.ContainerName != "" {
			containerName = svc.ContainerName
		}

		req := query.CreateContainerRequest{
			StackID:  stack.ID,
			Name:     containerName,
			Image:    image,
			Tag:      tag,
			Replicas: 1,
		}

		if svc.Restart != "" {
			req.RestartPolicy = &svc.Restart
		}

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

		envMap := parseComposeEnv(svc.Environment)
		if len(envMap) > 0 {
			b, _ := json.Marshal(envMap)
			s := string(b)
			req.EnvVars = &s
		}

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

		cmd := parseStringOrList(svc.Command)
		if len(cmd) > 0 {
			b, _ := json.Marshal(cmd)
			s := string(b)
			req.Command = &s
		}

		ep := parseStringOrList(svc.Entrypoint)
		if len(ep) > 0 {
			b, _ := json.Marshal(ep)
			s := string(b)
			req.Entrypoint = &s
		}

		if svc.Healthcheck != nil && !svc.Healthcheck.Disable {
			b, _ := json.Marshal(svc.Healthcheck)
			s := string(b)
			req.HealthCheck = &s
		}

		if svc.Deploy != nil {
			if svc.Deploy.Replicas > 0 {
				req.Replicas = svc.Deploy.Replicas
			}
			if svc.Deploy.Resources != nil && svc.Deploy.Resources.Limits != nil {
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
		}

		if _, err := query.CreateContainer(db.DB, req); err != nil {
			responder.QueryError(w, err, fmt.Sprintf("failed to create container %s", name))
			return
		}
	}

	// Replace networks — delete existing, create from compose
	_ = query.DeleteNetworksByStack(db.DB, stack.ID)
	if len(compose.Networks) > 0 {
		for key, net := range compose.Networks {
			driver := net.Driver
			if driver == "" {
				driver = "bridge"
			}
			name := net.Name
			if name == "" {
				name = key
			}
			_ = query.CreateNetwork(db.DB, query.CreateNetworkRequest{
				StackID: stack.ID,
				Name:    name,
				Driver:  driver,
			})
		}
	}

	// Store compose YAML on stack
	stack, err = query.UpdateStack(db.DB, stack.ID, query.UpdateStackRequest{
		ComposeYAML: &body.ComposeYAML,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to update stack compose")
		return
	}

	logAudit(r, "update_compose", "stack", intPtr(stackID), nil)
	responder.New(w, stack, "stack updated from compose file")
}
