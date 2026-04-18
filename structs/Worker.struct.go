package structs

import "time"

type Worker struct {
	ID              int        `json:"id"`
	Name            string     `json:"name"`
	Hostname        string     `json:"hostname"`
	IPAddress       *string    `json:"ip_address"`
	Status          string     `json:"status"`
	OS              *string    `json:"os"`
	Arch            *string    `json:"arch"`
	DockerVersion   *string    `json:"docker_version"`
	RunnerVersion   *string    `json:"runner_version"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	Labels          *string    `json:"labels"`
	Active          bool       `json:"active"`
	UpdatedAt       time.Time  `json:"updated_at"`
	InsertedAt      time.Time  `json:"inserted_at"`
}

type WorkerToken struct {
	ID         int        `json:"id"`
	WorkerID   int        `json:"worker_id"`
	Name       string     `json:"name"`
	TokenHash  string     `json:"-"`
	LastUsedAt *time.Time `json:"last_used_at"`
	Active     bool       `json:"active"`
	UpdatedAt  time.Time  `json:"updated_at"`
	InsertedAt time.Time  `json:"inserted_at"`
}
