package structs

import "time"

type Container struct {
	ID             int        `json:"id"`
	StackID        int        `json:"stack_id"`
	Name           string     `json:"name"`
	Image          string     `json:"image"`
	Tag            string     `json:"tag"`
	Status         string     `json:"status"`
	PortMappings   *string    `json:"port_mappings"`
	EnvVars        *string    `json:"env_vars"`
	Volumes        *string    `json:"volumes"`
	CPULimit       *float64   `json:"cpu_limit"`
	MemoryLimit    *int       `json:"memory_limit"`
	Replicas       int        `json:"replicas"`
	RestartPolicy  *string    `json:"restart_policy"`
	Command        *string    `json:"command"`
	Entrypoint     *string    `json:"entrypoint"`
	HealthCheck    *string    `json:"health_check"`
	Ulimits        *string    `json:"ulimits"`
	HealthStatus   string     `json:"health_status"`
	RegistryID     *int       `json:"registry_id"`
	DependsOn      *string    `json:"depends_on"`
	NetworkAliases *string    `json:"network_aliases"`
	Active         bool       `json:"active"`
	StartedAt      *time.Time `json:"started_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	InsertedAt     time.Time  `json:"inserted_at"`
}

// HasHealthCheck reports whether the container has a healthcheck configured in
// Lattice. A cleared one is stored as "" rather than NULL, so a nil check alone
// is wrong: it reads a cleared healthcheck as configured.
func (c Container) HasHealthCheck() bool {
	return c.HealthCheck != nil && *c.HealthCheck != ""
}

// ContainerStatus is the set of values Lattice writes to containers.status:
// the runner's Docker-state mapping (container_sync), container actions and
// deployment completion. The column is VARCHAR (migration 024) because the
// original ENUM lacked restarting and paused, and every such write failed.
type ContainerStatus string

const (
	ContainerStatusRunning    ContainerStatus = "running"
	ContainerStatusStopped    ContainerStatus = "stopped"
	ContainerStatusError      ContainerStatus = "error"
	ContainerStatusPending    ContainerStatus = "pending"
	ContainerStatusPaused     ContainerStatus = "paused"
	ContainerStatusRestarting ContainerStatus = "restarting"
)

func (s ContainerStatus) IsValid() bool {
	switch s {
	case ContainerStatusRunning, ContainerStatusStopped, ContainerStatusError,
		ContainerStatusPending, ContainerStatusPaused, ContainerStatusRestarting:
		return true
	}
	return false
}
