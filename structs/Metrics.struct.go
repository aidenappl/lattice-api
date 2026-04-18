package structs

import "time"

type WorkerMetrics struct {
	ID             int       `json:"id"`
	WorkerID       int       `json:"worker_id"`
	CPUPercent     *float64  `json:"cpu_percent"`
	MemoryUsedMB   *float64  `json:"memory_used_mb"`
	MemoryTotalMB  *float64  `json:"memory_total_mb"`
	DiskUsedMB     *float64  `json:"disk_used_mb"`
	DiskTotalMB    *float64  `json:"disk_total_mb"`
	ContainerCount *int      `json:"container_count"`
	NetworkRxBytes *int64    `json:"network_rx_bytes"`
	NetworkTxBytes *int64    `json:"network_tx_bytes"`
	RecordedAt     time.Time `json:"recorded_at"`
}

type ContainerEvent struct {
	ID          int       `json:"id"`
	ContainerID *int      `json:"container_id"`
	WorkerID    int       `json:"worker_id"`
	EventType   string    `json:"event_type"`
	Message     *string   `json:"message"`
	RecordedAt  time.Time `json:"recorded_at"`
}
