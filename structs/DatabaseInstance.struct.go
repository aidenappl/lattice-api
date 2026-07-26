package structs

import "time"

// DatabaseStatus is the lifecycle state of a managed database instance.
//
// The vocabulary is deliberately small and stable — variable failure detail
// belongs in LastError, not in new status values. This mirrors how RDS and
// Cloud SQL model instance state.
type DatabaseStatus string

const (
	// DBStatusPending is the initial state: the row exists, but no worker has
	// begun provisioning it yet.
	DBStatusPending DatabaseStatus = "pending"
	// DBStatusProvisioning means the worker has accepted the create command and
	// is pulling the image / running first-time initialisation.
	DBStatusProvisioning DatabaseStatus = "provisioning"
	// DBStatusRunning means the container is up. Health is reported separately
	// via HealthStatus.
	DBStatusRunning DatabaseStatus = "running"
	// DBStatusStopped means the container exists but is not running.
	DBStatusStopped DatabaseStatus = "stopped"
	// DBStatusRestarting is a transient state during a restart action.
	DBStatusRestarting DatabaseStatus = "restarting"
	// DBStatusDegraded means the container is present but impaired — most often
	// a restart loop or a failed initialisation. Non-terminal: the reconciler
	// keeps watching and will move it to running or error.
	DBStatusDegraded DatabaseStatus = "degraded"
	// DBStatusDeleting is a transient state while the worker tears the
	// container and volume down.
	DBStatusDeleting DatabaseStatus = "deleting"
	// DBStatusError is terminal for the current desired state and always
	// carries a populated LastError.
	DBStatusError DatabaseStatus = "error"
)

// IsValid reports whether s is a status the platform knows how to render and
// reconcile. Used to reject arbitrary strings at the API and MCP boundary.
func (s DatabaseStatus) IsValid() bool {
	switch s {
	case DBStatusPending, DBStatusProvisioning, DBStatusRunning, DBStatusStopped,
		DBStatusRestarting, DBStatusDegraded, DBStatusDeleting, DBStatusError:
		return true
	}
	return false
}

// IsTransitional reports whether s is a state the instance should not remain in
// indefinitely. The provisioning watchdog fails these out past a deadline.
func (s DatabaseStatus) IsTransitional() bool {
	switch s {
	case DBStatusPending, DBStatusProvisioning, DBStatusRestarting, DBStatusDeleting:
		return true
	}
	return false
}

// DatabaseHealth is the observed container health of a managed database,
// tracked independently of DatabaseStatus. A running instance can be unhealthy.
type DatabaseHealth string

const (
	DBHealthNone      DatabaseHealth = "none"
	DBHealthStarting  DatabaseHealth = "starting"
	DBHealthHealthy   DatabaseHealth = "healthy"
	DBHealthUnhealthy DatabaseHealth = "unhealthy"
)

func (h DatabaseHealth) IsValid() bool {
	switch h {
	case DBHealthNone, DBHealthStarting, DBHealthHealthy, DBHealthUnhealthy:
		return true
	}
	return false
}

// Machine-readable codes for DatabaseError.Code. These are stable identifiers
// the UI and MCP can branch on; Message carries the human detail.
const (
	DBErrCodeProvisionTimeout = "provision_timeout"
	DBErrCodeCreateFailed     = "create_failed"
	DBErrCodeStartFailed      = "start_failed"
	DBErrCodeStopFailed       = "stop_failed"
	DBErrCodeRemoveFailed     = "remove_failed"
	DBErrCodeRestartLoop      = "restart_loop"
	DBErrCodeInitFailed       = "init_failed"
	DBErrCodePortConflict     = "port_conflict"
	DBErrCodeContainerMissing = "container_missing"
	DBErrCodeWorkerOffline    = "worker_offline"
	DBErrCodeSnapshotFailed   = "snapshot_failed"
	DBErrCodeRestoreFailed    = "restore_failed"
)

// DatabaseError is the structured failure detail attached to an instance. It is
// stored as JSON in database_instances.last_error and cleared on recovery.
type DatabaseError struct {
	Code       string    `json:"code"`
	Message    string    `json:"message"`
	OccurredAt time.Time `json:"occurred_at"`
	Retryable  bool      `json:"retryable"`
}

type DatabaseInstance struct {
	ID                  int            `json:"id"`
	Name                string         `json:"name"`
	Engine              string         `json:"engine"`
	EngineVersion       string         `json:"engine_version"`
	WorkerID            int            `json:"worker_id"`
	Status              string         `json:"status"`
	Port                int            `json:"port"`
	RootPassword        *string        `json:"-"`
	DatabaseName        string         `json:"database_name"`
	Username            string         `json:"username"`
	Password            *string        `json:"-"`
	CPULimit            *float64       `json:"cpu_limit"`
	MemoryLimit         *int           `json:"memory_limit"`
	HealthStatus        string         `json:"health_status"`
	LastError           *DatabaseError `json:"last_error"`
	SnapshotSchedule    *string        `json:"snapshot_schedule"`
	RetentionCount      *int           `json:"retention_count"`
	BackupDestinationID *int           `json:"backup_destination_id"`
	ContainerName       string         `json:"container_name"`
	VolumeName          string         `json:"volume_name"`
	Active              bool           `json:"active"`
	StartedAt           *time.Time     `json:"started_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	InsertedAt          time.Time      `json:"inserted_at"`
}
