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
