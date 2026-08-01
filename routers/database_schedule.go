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

// PushDbSchedule sends an instance's snapshot schedule to its worker.
//
// Called on create and update, not only on worker reconnect. Previously the
// only sender was the reconnect sync, so setting a schedule on a running
// instance changed a database row and told the runner nothing — the schedule
// did not take effect until the worker happened to reconnect.
//
// Clearing a schedule sends an empty cron so the runner drops the job rather
// than continuing to snapshot on a cadence nobody can see anymore.
func PushDbSchedule(hub *socket.WorkerHub, instance *structs.DatabaseInstance) {
	if hub == nil || instance == nil {
		return
	}
	if !hub.IsConnected(instance.WorkerID) {
		return
	}

	payload := BuildDbSchedulePayload(instance)
	if payload == nil {
		// Not schedulable: tell the runner to forget any job it holds.
		payload = map[string]any{
			socket.PayloadDbInstanceID:   instance.ID,
			socket.PayloadContainerName:  instance.ContainerName,
			socket.PayloadCron:           "",
			socket.PayloadRetentionCount: 0,
		}
	}

	if err := hub.SendJSONToWorker(instance.WorkerID, socket.Envelope{
		Type:    socket.MsgDbUpdateSchedule,
		Payload: payload,
	}); err != nil {
		log.Printf("database instance %d: failed to push snapshot schedule to worker %d: %v",
			instance.ID, instance.WorkerID, err)
	}
}

// DistributeDbSchedules pushes every schedulable instance's schedule to a
// worker. Called when a worker connects, so anything that changed while it was
// offline takes effect immediately rather than at the next edit.
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
		if inst.SnapshotSchedule == nil || *inst.SnapshotSchedule == "" {
			continue
		}
		PushDbSchedule(hub, &inst)
	}
}
