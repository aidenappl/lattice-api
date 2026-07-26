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
	MsgDeploymentStatus      = "deployment_status"
	MsgContainerLogs         = "container_logs"
	MsgRegistration          = "registration"
	MsgWorkerActionStatus    = "worker_action_status"
	MsgWorkerShutdown        = "worker_shutdown"
	MsgWorkerCrash           = "worker_crash"
	MsgLifecycleLog          = "lifecycle_log"
)

// Orchestrator -> Worker message types
const (
	MsgDeploy         = "deploy"
	MsgStart          = "start"
	MsgStop           = "stop"
	MsgKill           = "kill"
	MsgRestart        = "restart"
	MsgPause          = "pause"
	MsgUnpause        = "unpause"
	MsgRemove         = "remove"
	MsgRecreate       = "recreate"
	MsgPullImage      = "pull_image"
	MsgConnected      = "connected"
	MsgRebootOS       = "reboot_os"
	MsgUpgradeRunner  = "upgrade_runner"
	MsgStopAll        = "stop_all"
	MsgStartAll       = "start_all"
	MsgListVolumes    = "list_volumes"
	MsgCreateVolume   = "create_volume"
	MsgRemoveVolume   = "remove_volume"
	MsgListNetworks   = "list_networks"
	MsgCreateNetwork  = "create_network"
	MsgRemoveNetwork  = "remove_network"
	MsgForceRemove    = "force_remove"
	MsgDeploymentPing = "deployment_ping"
)

// Worker -> Orchestrator response types for volume/network queries
const (
	MsgListVolumesResponse  = "list_volumes_response"
	MsgListNetworksResponse = "list_networks_response"
)

// Exec message types
const (
	MsgExecStart  = "exec_start"
	MsgExecInput  = "exec_input"
	MsgExecResize = "exec_resize"
	MsgExecClose  = "exec_close"
	MsgExecOutput = "exec_output"
)

// Database management: Orchestrator -> Worker
const (
	MsgDbCreate         = "db_create"
	MsgDbStart          = "db_start"
	MsgDbStop           = "db_stop"
	MsgDbRestart        = "db_restart"
	MsgDbRemove         = "db_remove"
	MsgDbSnapshot       = "db_snapshot"
	MsgDbRestore        = "db_restore"
	MsgDbUpdateSchedule = "db_update_schedule"
	MsgDbDeleteSnapshot = "db_delete_snapshot_file"
	MsgBackupDestTest   = "backup_dest_test"
	// MsgDbSyncRequest asks a worker to report every database container it can
	// see. The reply (MsgDbSync) is the observed state the reconciler diffs
	// desired state against.
	MsgDbSyncRequest = "db_sync_request"
)

// Database management: Worker -> Orchestrator
const (
	MsgDbStatus               = "db_status"
	MsgDbHealthStatus         = "db_health_status"
	MsgDbSnapshotProgress     = "db_snapshot_status"
	MsgDbRestoreStatus        = "db_restore_status"
	MsgDbDeleteSnapshotResult = "db_delete_snapshot_result"
	MsgDbScheduleStatus       = "db_schedule_status"
	MsgDbSync                 = "db_sync"
	MsgBackupDestTestResult   = "backup_dest_test_result"
)

// DbReplyTypes is every worker -> orchestrator message the database subsystem
// can produce. TestDbReplyTypesHaveHandlers asserts each one is dispatched.
//
// This list exists because db_delete_snapshot_result shipped with no constant
// and no handler, so snapshot deletions were silently discarded for months.
// Adding a reply type without wiring it now fails the build's test run.
var DbReplyTypes = []string{
	MsgDbStatus,
	MsgDbHealthStatus,
	MsgDbSnapshotProgress,
	MsgDbRestoreStatus,
	MsgDbDeleteSnapshotResult,
	MsgDbScheduleStatus,
	MsgDbSync,
	MsgBackupDestTestResult,
}

// Payload keys that correlate a database command with its replies. Every
// db_* command carries all three and every reply echoes them back.
const (
	// PayloadDbInstanceID ties a reply to the row it should update. Its absence
	// from runner replies is why no managed database could ever leave "pending".
	PayloadDbInstanceID = "database_instance_id"
	// PayloadRequestID is unique per attempt.
	PayloadRequestID = "request_id"
	// PayloadIdempotencyKey is stable across retries of the same logical
	// operation, so a redelivered command after a reconnect is a no-op.
	PayloadIdempotencyKey = "idempotency_key"
	// PayloadPhase distinguishes a fast receipt acknowledgement from the
	// terminal outcome: "ack", "completed" or "failed".
	PayloadPhase = "phase"
)

// Reply phases for PayloadPhase.
const (
	PhaseAck       = "ack"
	PhaseCompleted = "completed"
	PhaseFailed    = "failed"
)

// Admin client -> API message types
const (
	MsgSubscribe   = "subscribe"
	MsgUnsubscribe = "unsubscribe"
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
