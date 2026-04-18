package socket

import (
	"encoding/json"
	"time"
)

// Worker -> Orchestrator message types
const (
	MsgHeartbeat          = "heartbeat"
	MsgContainerStatus    = "container_status"
	MsgDeploymentProgress = "deployment_progress"
	MsgContainerLogs      = "container_logs"
	MsgRegistration       = "registration"
)

// Orchestrator -> Worker message types
const (
	MsgDeploy       = "deploy"
	MsgStop         = "stop"
	MsgRestart      = "restart"
	MsgRemove       = "remove"
	MsgPullImage    = "pull_image"
	MsgExec         = "exec"
	MsgConfigUpdate = "config_update"
	MsgAck          = "ack"
	MsgConnected    = "connected"
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
