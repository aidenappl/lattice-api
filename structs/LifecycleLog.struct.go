package structs

import "time"

type LifecycleLog struct {
	ID            int       `json:"id"`
	ContainerID   *int      `json:"container_id"`
	ContainerName *string   `json:"container_name"`
	WorkerID      int       `json:"worker_id"`
	Event         string    `json:"event"`
	Message       string    `json:"message"`
	RecordedAt    time.Time `json:"recorded_at"`
}
