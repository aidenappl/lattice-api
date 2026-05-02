package structs

import "time"

type DatabaseInstance struct {
	ID                  int        `json:"id"`
	Name                string     `json:"name"`
	Engine              string     `json:"engine"`
	EngineVersion       string     `json:"engine_version"`
	WorkerID            int        `json:"worker_id"`
	Status              string     `json:"status"`
	Port                int        `json:"port"`
	RootPassword        *string    `json:"-"`
	DatabaseName        string     `json:"database_name"`
	Username            string     `json:"username"`
	Password            *string    `json:"-"`
	CPULimit            *float64   `json:"cpu_limit"`
	MemoryLimit         *int       `json:"memory_limit"`
	HealthStatus        string     `json:"health_status"`
	SnapshotSchedule    *string    `json:"snapshot_schedule"`
	RetentionCount      *int       `json:"retention_count"`
	BackupDestinationID *int       `json:"backup_destination_id"`
	ContainerName       string     `json:"container_name"`
	VolumeName          string     `json:"volume_name"`
	Active              bool       `json:"active"`
	StartedAt           *time.Time `json:"started_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	InsertedAt          time.Time  `json:"inserted_at"`
}
