package socket

import (
	"encoding/json"
	"time"
)

// Worker -> Orchestrator message types
const (
	MsgHeartbeat             = "heartbeat"
	MsgContainerStatus       = "container_status"
	MsgContainerHealthStatus = "container_health_status"
	MsgContainerSync         = "container_sync"
	MsgDeploymentProgress    = "deployment_progress"
	MsgContainerLogs         = "container_logs"
	MsgRegistration          = "registration"
	MsgWorkerActionStatus    = "worker_action_status"
	MsgWorkerShutdown        = "worker_shutdown"
	MsgWorkerCrash           = "worker_crash"
	MsgLifecycleLog          = "lifecycle_log"
)

// Orchestrator -> Worker message types
const (
	MsgDeploy        = "deploy"
	MsgStart         = "start"
	MsgStop          = "stop"
	MsgKill          = "kill"
	MsgRestart       = "restart"
	MsgPause         = "pause"
	MsgUnpause       = "unpause"
	MsgRemove        = "remove"
	MsgRecreate      = "recreate"
	MsgPullImage     = "pull_image"
	MsgConnected     = "connected"
	MsgRebootOS      = "reboot_os"
	MsgUpgradeRunner = "upgrade_runner"
	MsgStopAll       = "stop_all"
	MsgStartAll      = "start_all"
)

// Envelope is the standard message sent orchestrator -> worker.
type Envelope struct {
	Type      string         `json:"type"`
	CommandID string         `json:"command_id,omitempty"`
	WorkerID  string         `json:"worker_id,omitempty"`
	IssuedAt  *time.Time     `json:"issued_at,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
}

// IncomingMessage is a message from worker -> orchestrator.
type IncomingMessage struct {
	Type      string          `json:"type"`
	CommandID string          `json:"command_id,omitempty"`
	WorkerID  string          `json:"worker_id,omitempty"`
	Status    string          `json:"status,omitempty"`
	Payload   map[string]any  `json:"payload,omitempty"`
	Raw       json.RawMessage `json:"-"`
}
