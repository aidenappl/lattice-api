package socket

import (
	"sort"
	"testing"
)

// The db_* payload contract.
//
// This file exists because ten separate defects shipped in the managed-database
// subsystem with the same shape: the orchestrator wrote one key name and the
// runner read another. Every one compiled, every one saved cleanly, and every one
// produced an empty value instead of an error:
//
//	API sent                     runner read              result
//	backup_destination           backup_dest             every scheduled snapshot silently no-op'd
//	backup_destination.type      dest_type               NewDestination("") — manual snapshots failed
//	backup_destination.config    dest_config             no credentials
//	filename                     remote_path             empty object key; remote delete refused
//	snapshot_id (number)         snapshot_id.(string)    "" → parsed to 0 → every status reply dropped
//	snapshot_id (restore)        restore_id              restore correlation lost
//
// The pre-existing contract tests could not catch any of them: db_contract_test.go
// pins reply *type* names and correlation keys, and db_dispatch_test.go greps
// source text for constant *identifiers*. Neither has ever looked at the key set
// of a payload.
//
// So this test pins the key names themselves. It is deliberately a dumb,
// hard-coded list: the point is that changing a wire key requires editing a
// literal here, which is the prompt to go change the runner too.

// dbPayloadKeys is the exact wire key set for each db_* command, as the runner
// reads it. Keep in step with lattice-runner's db_* handlers.
var dbPayloadKeys = map[string][]string{
	MsgDbUpdateSchedule: {
		PayloadDbInstanceID,
		PayloadContainerName,
		PayloadEngine,
		PayloadDatabaseName,
		PayloadUsername,
		PayloadPassword,
		PayloadRootPassword,
		PayloadCron,
		PayloadRetentionCount,
		PayloadBackupDestination,
	},
	MsgDbSnapshot: {
		PayloadDbInstanceID,
		PayloadRequestID,
		PayloadIdempotencyKey,
		PayloadSnapshotID,
		PayloadContainerName,
		PayloadEngine,
		PayloadDatabaseName,
		PayloadUsername,
		PayloadPassword,
		PayloadFilename,
		PayloadBackupDestination,
	},
	MsgDbRestore: {
		PayloadDbInstanceID,
		PayloadRequestID,
		PayloadIdempotencyKey,
		PayloadSnapshotID,
		PayloadContainerName,
		PayloadEngine,
		PayloadDatabaseName,
		PayloadUsername,
		PayloadPassword,
		PayloadFilename,
		PayloadBackupDestination,
	},
	MsgDbDeleteSnapshot: {
		PayloadDbInstanceID,
		PayloadRequestID,
		PayloadIdempotencyKey,
		PayloadSnapshotID,
		PayloadFilename,
		PayloadBackupDestination,
	},
	MsgDbMirrorSnapshot: {
		PayloadDbInstanceID,
		PayloadRequestID,
		PayloadIdempotencyKey,
		PayloadSnapshotID,
		PayloadFilename,
		PayloadSourceDestination,
		PayloadTargetDestination,
	},
	MsgDbRemove: {
		PayloadDbInstanceID,
		PayloadRequestID,
		PayloadIdempotencyKey,
		PayloadContainerName,
		PayloadVolumeName,
		PayloadRemoveVolume,
	},
}

// TestDbPayloadKeyValuesStable pins the wire spelling of every payload key.
//
// A rename here is a protocol change. If this test fails, the runner must change
// in the same commit — that is the entire purpose.
func TestDbPayloadKeyValuesStable(t *testing.T) {
	expected := map[string]string{
		"PayloadContainerName":     "container_name",
		"PayloadVolumeName":        "volume_name",
		"PayloadRemoveVolume":      "remove_volume",
		"PayloadEngine":            "engine",
		"PayloadEngineVersion":     "engine_version",
		"PayloadDatabaseName":      "database_name",
		"PayloadUsername":          "username",
		"PayloadPassword":          "password",
		"PayloadRootPassword":      "root_password",
		"PayloadPort":              "port",
		"PayloadCPULimit":          "cpu_limit",
		"PayloadMemoryLimit":       "memory_limit",
		"PayloadAdoptVolume":       "adopt_existing_volume",
		"PayloadBinlogRetention":   "binlog_retention_seconds",
		"PayloadCron":              "cron",
		"PayloadRetentionCount":    "retention_count",
		"PayloadSnapshotID":        "snapshot_id",
		"PayloadFilename":          "filename",
		"PayloadBackupDestination": "backup_destination",
		"PayloadSourceDestination": "source_destination",
		"PayloadTargetDestination": "target_destination",
		"PayloadDestType":          "type",
		"PayloadDestConfig":        "config",
		"PayloadScheduled":         "scheduled",
		"PayloadStatus":            "status",
		"PayloadSizeBytes":         "size_bytes",
		"PayloadErrorMessage":      "error_message",
	}
	actual := map[string]string{
		"PayloadContainerName":     PayloadContainerName,
		"PayloadVolumeName":        PayloadVolumeName,
		"PayloadRemoveVolume":      PayloadRemoveVolume,
		"PayloadEngine":            PayloadEngine,
		"PayloadEngineVersion":     PayloadEngineVersion,
		"PayloadDatabaseName":      PayloadDatabaseName,
		"PayloadUsername":          PayloadUsername,
		"PayloadPassword":          PayloadPassword,
		"PayloadRootPassword":      PayloadRootPassword,
		"PayloadPort":              PayloadPort,
		"PayloadCPULimit":          PayloadCPULimit,
		"PayloadMemoryLimit":       PayloadMemoryLimit,
		"PayloadAdoptVolume":       PayloadAdoptVolume,
		"PayloadBinlogRetention":   PayloadBinlogRetention,
		"PayloadCron":              PayloadCron,
		"PayloadRetentionCount":    PayloadRetentionCount,
		"PayloadSnapshotID":        PayloadSnapshotID,
		"PayloadFilename":          PayloadFilename,
		"PayloadBackupDestination": PayloadBackupDestination,
		"PayloadSourceDestination": PayloadSourceDestination,
		"PayloadTargetDestination": PayloadTargetDestination,
		"PayloadDestType":          PayloadDestType,
		"PayloadDestConfig":        PayloadDestConfig,
		"PayloadScheduled":         PayloadScheduled,
		"PayloadStatus":            PayloadStatus,
		"PayloadSizeBytes":         PayloadSizeBytes,
		"PayloadErrorMessage":      PayloadErrorMessage,
	}

	for name, want := range expected {
		if got := actual[name]; got != want {
			t.Errorf("%s = %q, want %q — this is a wire protocol change; update lattice-runner in the same commit", name, got, want)
		}
	}
}

// TestDbPayloadKeysDeclaredForEveryDataCommand asserts every command that
// carries data beyond bare correlation has its key set declared above, so a new
// command cannot be added without recording its contract.
func TestDbPayloadKeysDeclaredForEveryDataCommand(t *testing.T) {
	dataCommands := []string{
		MsgDbUpdateSchedule,
		MsgDbSnapshot,
		MsgDbRestore,
		MsgDbDeleteSnapshot,
		MsgDbMirrorSnapshot,
		MsgDbRemove,
	}
	for _, cmd := range dataCommands {
		if _, ok := dbPayloadKeys[cmd]; !ok {
			t.Errorf("command %q carries payload data but has no declared key set in dbPayloadKeys", cmd)
		}
	}
}

// TestDbPayloadKeySetsHaveNoDuplicates guards the declarations themselves — a
// duplicated key in a set hides a missing one.
func TestDbPayloadKeySetsHaveNoDuplicates(t *testing.T) {
	for cmd, keys := range dbPayloadKeys {
		sorted := make([]string, len(keys))
		copy(sorted, keys)
		sort.Strings(sorted)
		for i := 1; i < len(sorted); i++ {
			if sorted[i] == sorted[i-1] {
				t.Errorf("command %q declares key %q twice", cmd, sorted[i])
			}
		}
	}
}

// TestBackupDestinationIsNestedNotFlat is the specific regression test for the
// defect that killed scheduled snapshots for three months.
//
// The destination travels as a nested object under one key. It must never be
// flattened back into sibling `dest_type`/`dest_config` keys, and the key must
// not drift to `backup_dest`.
func TestBackupDestinationIsNestedNotFlat(t *testing.T) {
	if PayloadBackupDestination == "backup_dest" {
		t.Fatal("PayloadBackupDestination regressed to \"backup_dest\": the runner reads \"backup_destination\"")
	}
	for cmd, keys := range dbPayloadKeys {
		for _, k := range keys {
			if k == "dest_type" || k == "dest_config" {
				t.Errorf("command %q declares flat %q; the destination must be nested under %q",
					cmd, k, PayloadBackupDestination)
			}
		}
	}
}
