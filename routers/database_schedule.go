package routers

import (
	"encoding/json"
	"log"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/structs"
)

// BuildDbSchedulePayload renders the db_update_schedule payload for an instance.
//
// One builder, used by both the create/update handlers and the worker-connect
// sync, so a schedule can never be expressed two different ways. Returns nil
// when the instance is not schedulable — no cron, or no backup destination,
// which the runner cannot act on.
func BuildDbSchedulePayload(instance *structs.DatabaseInstance) map[string]any {
	if instance == nil || instance.SnapshotSchedule == nil || *instance.SnapshotSchedule == "" {
		return nil
	}
	if instance.BackupDestinationID == nil {
		return nil
	}

	dest, err := query.GetBackupDestinationByID(db.DB, *instance.BackupDestinationID)
	if err != nil || dest == nil {
		log.Printf("database instance %d: cannot resolve backup destination %d for schedule: %v",
			instance.ID, *instance.BackupDestinationID, err)
		return nil
	}

	var destConfig map[string]any
	if dest.Config != nil {
		if err := json.Unmarshal([]byte(*dest.Config), &destConfig); err != nil {
			log.Printf("database instance %d: backup destination %d has unparseable config: %v",
				instance.ID, dest.ID, err)
			return nil
		}
	}

	retentionCount := 0
	if instance.RetentionCount != nil {
		retentionCount = *instance.RetentionCount
	}

	rootPw := ""
	if instance.RootPassword != nil {
		rootPw = *instance.RootPassword
	}
	pw := ""
	if instance.Password != nil {
		pw = *instance.Password
	}

	return map[string]any{
		socket.PayloadDbInstanceID:   instance.ID,
		socket.PayloadContainerName:  instance.ContainerName,
		socket.PayloadEngine:         instance.Engine,
		socket.PayloadDatabaseName:   instance.DatabaseName,
		socket.PayloadUsername:       instance.Username,
		socket.PayloadPassword:       pw,
		socket.PayloadRootPassword:   rootPw,
		socket.PayloadCron:           *instance.SnapshotSchedule,
		socket.PayloadRetentionCount: retentionCount,
		socket.PayloadBackupDestination: map[string]any{
			socket.PayloadDestType:   dest.Type,
			socket.PayloadDestConfig: destConfig,
		},
	}
}

// PushDbSchedule clears any snapshot schedule the runner still holds.
//
// ⚠️ Scheduling now lives in the control plane (database_scheduler.go), so this
// deliberately sends an **empty cron** rather than the instance's expression.
// Two schedulers for one instance means two backups per slot: the runner's cron
// would fire on its own clock while the control plane claims and dispatches the
// same nominal slot, and neither would know about the other.
//
// It is still called on create, on update and on worker reconnect, because that
// is exactly when a runner might be holding a stale job from before the move —
// a runner that registered a cron under the old model keeps firing it forever
// otherwise, and the resulting snapshots would arrive with no run row to explain
// them.
//
// BuildDbSchedulePayload is retained: it is the one place that knows how to
// express a schedule, and it will be needed again if the runner ever regains a
// scheduling role.
func PushDbSchedule(hub *socket.WorkerHub, instance *structs.DatabaseInstance) {
	if hub == nil || instance == nil {
		return
	}
	if !hub.IsConnected(instance.WorkerID) {
		return
	}

	if err := hub.SendJSONToWorker(instance.WorkerID, socket.Envelope{
		Type: socket.MsgDbUpdateSchedule,
		Payload: map[string]any{
			socket.PayloadDbInstanceID:   instance.ID,
			socket.PayloadContainerName:  instance.ContainerName,
			socket.PayloadCron:           "",
			socket.PayloadRetentionCount: 0,
		},
	}); err != nil {
		log.Printf("database instance %d: failed to clear worker-side schedule on worker %d: %v",
			instance.ID, instance.WorkerID, err)
	}
}

// DistributeDbSchedules clears worker-side schedules for every instance on a
// worker when it connects.
//
// Under the old model this rehydrated the runner's cron. It now does the
// opposite: a runner that reconnects carrying jobs registered before scheduling
// moved to the control plane would double-fire every slot, so connection is
// exactly the moment to take them away.
func DistributeDbSchedules(workerID int, hub *socket.WorkerHub) {
	instances, _, err := query.ListDatabaseInstances(db.DB, query.ListDatabaseInstancesRequest{
		WorkerID: &workerID,
		Limit:    db.MAX_LIMIT,
	})
	if err != nil || instances == nil {
		return
	}

	for i := range *instances {
		inst := (*instances)[i]
		PushDbSchedule(hub, &inst)
	}
}
