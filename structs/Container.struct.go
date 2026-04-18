package structs

import "time"

type Container struct {
	ID            int       `json:"id"`
	StackID       int       `json:"stack_id"`
	Name          string    `json:"name"`
	Image         string    `json:"image"`
	Tag           string    `json:"tag"`
	Status        string    `json:"status"`
	PortMappings  *string   `json:"port_mappings"`
	EnvVars       *string   `json:"env_vars"`
	Volumes       *string   `json:"volumes"`
	CPULimit      *float64  `json:"cpu_limit"`
	MemoryLimit   *int      `json:"memory_limit"`
	Replicas      int       `json:"replicas"`
	RestartPolicy *string   `json:"restart_policy"`
	Command       *string   `json:"command"`
	Entrypoint    *string   `json:"entrypoint"`
	HealthCheck   *string   `json:"health_check"`
	HealthStatus  string    `json:"health_status"`
	RegistryID    *int      `json:"registry_id"`
	Active        bool      `json:"active"`
	UpdatedAt     time.Time `json:"updated_at"`
	InsertedAt    time.Time `json:"inserted_at"`
}
