package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/structs"
)

const (
	// dbReconcileInterval is how often the control plane asks every connected
	// worker what database containers it can actually see.
	//
	// Kubernetes defaults its informer resync to 10 hours, but that suits a
	// system where watch events are reliable and the API server is the source
	// of truth. Here the worker is the only observer, drift matters within
	// minutes, and a fleet of tens of workers makes a 60s sweep nearly free.
	dbReconcileInterval = 60 * time.Second

	// dbProvisionTimeout bounds how long an instance may sit in a transitional
	// status before the watchdog fails it out. Generous enough to cover pulling
	// a several-hundred-megabyte database image on a slow link.
	dbProvisionTimeout = 10 * time.Minute

	// dbWatchdogInterval is how often stuck instances are swept for.
	dbWatchdogInterval = 30 * time.Second
)

// databaseReconciler keeps database_instances rows honest.
//
// It is level-triggered: it never reasons about which command produced the
// current state, only about whether what the worker observes matches what the
// control plane recorded. That is what makes a dropped or mishandled db_status
// self-correcting rather than permanently stranding an instance — the failure
// mode this subsystem shipped with.
type databaseReconciler struct {
	workerHub *socket.WorkerHub

	// mu guards inflight, which prevents two concurrent reconciles of the same
	// instance from racing each other into conflicting writes.
	mu       sync.Mutex
	inflight map[int]bool

	stop chan struct{}
	once sync.Once
}

var dbReconciler *databaseReconciler

func newDatabaseReconciler(workerHub *socket.WorkerHub) *databaseReconciler {
	return &databaseReconciler{
		workerHub: workerHub,
		inflight:  map[int]bool{},
		stop:      make(chan struct{}),
	}
}

// Start runs the reconcile and watchdog loops until Stop is called.
func (rc *databaseReconciler) Start() {
	go rc.runLoop("db-reconcile", dbReconcileInterval, rc.reconcileAll)
	go rc.runLoop("db-watchdog", dbWatchdogInterval, rc.failStuckInstances)
	logger.Info("database", "reconciler started", logger.F{
		"reconcile_interval": dbReconcileInterval.String(),
		"provision_timeout":  dbProvisionTimeout.String(),
	})
}

// Stop halts the loops. Safe to call more than once.
func (rc *databaseReconciler) Stop() {
	rc.once.Do(func() { close(rc.stop) })
}

func (rc *databaseReconciler) runLoop(name string, interval time.Duration, fn func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-rc.stop:
			logger.Info("database", "reconciler loop stopped", logger.F{"loop": name})
			return
		case <-ticker.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						logger.Error("panic", fmt.Sprintf("%v", r), logger.F{"goroutine": name})
					}
				}()
				fn()
			}()
		}
	}
}

// reconcileAll asks every connected worker for a full report of the database
// containers it can see. Workers answer with db_sync, which handleDbSync diffs
// against desired state.
func (rc *databaseReconciler) reconcileAll() {
	for _, workerID := range rc.workerHub.ListConnectedIDs() {
		rc.RequestSync(workerID)
	}
}

// RequestSync asks one worker to report its database containers. Called on the
// reconcile tick and immediately on worker reconnect, so a worker that was
// offline during a state change is corrected as soon as it returns rather than
// up to a full interval later.
func (rc *databaseReconciler) RequestSync(workerID int) {
	if rc == nil || rc.workerHub == nil {
		return
	}
	if !rc.workerHub.IsConnected(workerID) {
		return
	}
	if err := rc.workerHub.SendJSONToWorker(workerID, socket.Envelope{
		Type:    socket.MsgDbSyncRequest,
		Payload: map[string]any{},
	}); err != nil {
		logger.Warn("database", "failed to request database sync", logger.F{
			"worker_id": workerID, "error": err,
		})
	}
}

// failStuckInstances is the backstop that makes "hung in pending forever"
// impossible. Anything sitting in a transitional status past the deadline is
// moved to error with a reason attached, whatever the cause — lost message,
// crashed runner, worker that never came back.
func (rc *databaseReconciler) failStuckInstances() {
	cutoff := time.Now().UTC().Add(-dbProvisionTimeout)

	stuck, err := query.ListStuckDatabaseInstances(db.DB, cutoff)
	if err != nil {
		logger.Error("database", "watchdog failed to list stuck instances", logger.F{"error": err})
		return
	}

	for _, instance := range stuck {
		if !rc.claim(instance.ID) {
			continue
		}

		status := structs.DatabaseStatus(instance.Status)
		message := fmt.Sprintf("stuck in %s for more than %s with no response from worker %d",
			instance.Status, dbProvisionTimeout, instance.WorkerID)

		code := structs.DBErrCodeProvisionTimeout
		if !rc.workerHub.IsConnected(instance.WorkerID) {
			code = structs.DBErrCodeWorkerOffline
			message = fmt.Sprintf("stuck in %s for more than %s; worker %d is not connected",
				instance.Status, dbProvisionTimeout, instance.WorkerID)
		}

		// A delete holds the row open until the worker confirms the container
		// and data volume are gone, so a delete that never completed must fail
		// out like anything else — it is retryable, and the resources it was
		// meant to destroy are still on the worker. Recording an event and
		// leaving it in `deleting` (as this did while deletion was optimistic)
		// stranded it there permanently.
		if status == structs.DBStatusDeleting {
			message = fmt.Sprintf("delete did not complete: %s; container %s and data volume %s may still exist",
				message, instance.ContainerName, instance.VolumeName)
			code = structs.DBErrCodeRemoveFailed
		}

		dbLifecycle.Transition(instance.ID, structs.DBStatusError, transitionOpts{
			Message: message,
			Err:     newDatabaseError(code, message, true),
			Actor:   "watchdog",
		})
		rc.release(instance.ID)
	}
}

// claim marks an instance as being reconciled. Returns false if another
// reconcile is already in flight for it.
func (rc *databaseReconciler) claim(instanceID int) bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.inflight[instanceID] {
		return false
	}
	rc.inflight[instanceID] = true
	return true
}

func (rc *databaseReconciler) release(instanceID int) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	delete(rc.inflight, instanceID)
}
