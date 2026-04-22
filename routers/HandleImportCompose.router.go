package routers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/tools"
	"gopkg.in/yaml.v3"
)

type composeFile struct {
	Services map[string]composeService `yaml:"services"`
	Networks map[string]composeNetwork `yaml:"networks"`
}

type composeNetwork struct {
	Driver string `yaml:"driver"`
	Name   string `yaml:"name"`
}

type composeService struct {
	Image         string         `yaml:"image"`
	ContainerName string         `yaml:"container_name"`
	Ports         []string       `yaml:"ports"`
	Environment   any            `yaml:"environment"`
	Volumes       []string       `yaml:"volumes"`
	Command       any            `yaml:"command"`
	Entrypoint    any            `yaml:"entrypoint"`
	Restart       string         `yaml:"restart"`
	Deploy        *composeDeploy `yaml:"deploy"`
	Labels        any            `yaml:"labels"`
	Healthcheck   *composeHealth `yaml:"healthcheck"`
	Networks      any            `yaml:"networks"`
}

type composeHealth struct {
	Disable     bool   `yaml:"disable"     json:"disable,omitempty"`
	Test        any    `yaml:"test"        json:"test,omitempty"`
	Interval    string `yaml:"interval"    json:"interval,omitempty"`
	Timeout     string `yaml:"timeout"     json:"timeout,omitempty"`
	Retries     int    `yaml:"retries"     json:"retries,omitempty"`
	StartPeriod string `yaml:"start_period" json:"start_period,omitempty"`
}

type composeDeploy struct {
	Replicas  int              `yaml:"replicas"`
	Resources *composeResource `yaml:"resources"`
}

type composeResource struct {
	Limits *composeResourceLimit `yaml:"limits"`
}

type composeResourceLimit struct {
	CPUs   string `yaml:"cpus"`
	Memory string `yaml:"memory"`
}

func HandleImportCompose(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, int64(tools.MaxYAMLSize)+4096) // allow overhead for JSON wrapper

	var body struct {
		Name               string  `json:"name"`
		Description        *string `json:"description"`
		WorkerID           *int    `json:"worker_id"`
		DeploymentStrategy string  `json:"deployment_strategy"`
		ComposeYAML        string  `json:"compose_yaml"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.ComposeYAML == "" {
		responder.MissingBodyFields(w, "compose_yaml")
		return
	}
	if err := tools.ValidateYAMLSize(body.ComposeYAML); err != nil {
		responder.SendError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.DeploymentStrategy == "" {
		body.DeploymentStrategy = "rolling"
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

	// Auto-derive stack name from first service if not provided
	if body.Name == "" {
		for svcName := range compose.Services {
			body.Name = svcName
			break
		}
	}

	// Begin transaction — if container creation fails, the stack is rolled back too
	tx, err := db.BeginTx()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback() // no-op if committed

	// Create stack
	stack, err := query.CreateStack(tx, query.CreateStackRequest{
		Name:               body.Name,
		Description:        body.Description,
		WorkerID:           body.WorkerID,
		DeploymentStrategy: body.DeploymentStrategy,
		ComposeYAML:        &body.ComposeYAML,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create stack")
		return
	}

	// Create containers from services
	for name, svc := range compose.Services {
		image, tag := parseImageRef(svc.Image)

		// Use container_name if specified, otherwise use the service name
		containerName := name
		if svc.ContainerName != "" {
			containerName = svc.ContainerName
		}

		// Check for name conflicts with existing containers in other stacks
		if exists, err := query.ContainerNameExists(tx, containerName, nil); err != nil {
			responder.QueryError(w, err, "failed to check container name")
			return
		} else if exists {
			responder.SendError(w, http.StatusConflict, fmt.Sprintf("container name '%s' is already in use by another stack", containerName))
			return
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

		// Healthcheck — normalize Test to ["CMD-SHELL", "command"] format
		if svc.Healthcheck != nil && !svc.Healthcheck.Disable {
			svc.Healthcheck.Test = normalizeHealthTest(svc.Healthcheck.Test)
			b, _ := json.Marshal(svc.Healthcheck)
			s := string(b)
			req.HealthCheck = &s
		}

		// Deploy config
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

		if _, err := query.CreateContainer(tx, req); err != nil {
			responder.QueryError(w, err, fmt.Sprintf("failed to create container %s", name))
			return
		}
	}

	// Save top-level networks
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
			_ = query.CreateNetwork(tx, query.CreateNetworkRequest{
				StackID: stack.ID,
				Name:    name,
				Driver:  driver,
			})
		}
	}

	if err := tx.Commit(); err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to commit transaction")
		return
	}

	// Re-fetch stack to return (outside transaction, it's committed now)
	stack, err = query.GetStackByID(db.DB, stack.ID)
	if err != nil {
		responder.QueryError(w, err, "failed to fetch created stack")
		return
	}

	logAudit(r, "import", "stack", intPtr(stack.ID), strPtr(stack.Name))
	responder.NewCreated(w, stack, "stack imported from compose file")
}

func parseImageRef(imageRef string) (string, string) {
	// Handle images like registry.example.com/image:tag
	parts := strings.Split(imageRef, ":")
	if len(parts) >= 2 {
		// Check if the last part looks like a tag (not a port)
		last := parts[len(parts)-1]
		if !strings.Contains(last, "/") {
			return strings.Join(parts[:len(parts)-1], ":"), last
		}
	}
	return imageRef, "latest"
}

func parsePortMapping(port string) map[string]string {
	// Formats: "8080:80", "8080:80/tcp", "127.0.0.1:8080:80"
	parts := strings.Split(port, ":")
	if len(parts) < 2 {
		return nil
	}

	hostPort := parts[len(parts)-2]
	containerPortProto := parts[len(parts)-1]

	containerPort := containerPortProto
	protocol := "tcp"
	if idx := strings.Index(containerPortProto, "/"); idx != -1 {
		containerPort = containerPortProto[:idx]
		protocol = containerPortProto[idx+1:]
	}

	return map[string]string{
		"host_port":      hostPort,
		"container_port": containerPort,
		"protocol":       protocol,
	}
}

func parseComposeEnv(env any) map[string]string {
	result := make(map[string]string)
	switch v := env.(type) {
	case map[string]any:
		for key, val := range v {
			result[key] = fmt.Sprintf("%v", val)
		}
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				continue
			}
			parts := strings.SplitN(s, "=", 2)
			if len(parts) == 2 {
				result[parts[0]] = parts[1]
			}
		}
	}
	return result
}

func parseStringOrList(v any) []string {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case string:
		if val == "" {
			return nil
		}
		return strings.Fields(val)
	case []any:
		result := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}

// normalizeHealthTest converts a healthcheck test value to ["CMD-SHELL", "command"] format.
// Docker Compose allows test as a string or a list; this ensures consistent []string storage.
func normalizeHealthTest(v any) []string {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case string:
		if val == "" {
			return nil
		}
		return []string{"CMD-SHELL", val}
	case []any:
		result := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	case []string:
		return val
	}
	return nil
}

func parseMemoryString(mem string) int {
	mem = strings.TrimSpace(strings.ToLower(mem))
	var value float64
	var unit string
	fmt.Sscanf(mem, "%f%s", &value, &unit)
	switch {
	case strings.HasPrefix(unit, "g"):
		return int(value * 1024)
	case strings.HasPrefix(unit, "m"):
		return int(value)
	case strings.HasPrefix(unit, "k"):
		return int(value / 1024)
	}
	return int(value)
}
