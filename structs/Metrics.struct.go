package structs

import "time"

type WorkerMetrics struct {
	ID                    int       `json:"id"`
	WorkerID              int       `json:"worker_id"`
	CPUPercent            *float64  `json:"cpu_percent"`
	CPUCores              *int      `json:"cpu_cores"`
	LoadAvg1              *float64  `json:"load_avg_1"`
	LoadAvg5              *float64  `json:"load_avg_5"`
	LoadAvg15             *float64  `json:"load_avg_15"`
	MemoryUsedMB          *float64  `json:"memory_used_mb"`
	MemoryTotalMB         *float64  `json:"memory_total_mb"`
	MemoryFreeMB          *float64  `json:"memory_free_mb"`
	SwapUsedMB            *float64  `json:"swap_used_mb"`
	SwapTotalMB           *float64  `json:"swap_total_mb"`
	DiskUsedMB            *float64  `json:"disk_used_mb"`
	DiskTotalMB           *float64  `json:"disk_total_mb"`
	ContainerCount        *int      `json:"container_count"`
	ContainerRunningCount *int      `json:"container_running_count"`
	NetworkRxBytes        *int64    `json:"network_rx_bytes"`
	NetworkTxBytes        *int64    `json:"network_tx_bytes"`
	UptimeSeconds         *float64  `json:"uptime_seconds"`
	ProcessCount          *int      `json:"process_count"`
	RecordedAt            time.Time `json:"recorded_at"`
}

type ContainerEvent struct {
	ID          int       `json:"id"`
	ContainerID *int      `json:"container_id"`
	WorkerID    int       `json:"worker_id"`
	EventType   string    `json:"event_type"`
	Message     *string   `json:"message"`
	RecordedAt  time.Time `json:"recorded_at"`
}
