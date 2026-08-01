package socket

import (
	"encoding/json"
	"testing"
)

// The database subsystem shipped with eight separate protocol defects, every one
// of which compiled cleanly and every one of which was invisible at runtime:
// replies omitting the ID that correlates them to a row, an ID sent as a number
// and read as a string, reply types with no handler at all, and a command the
// orchestrator defined but never sent. The tests here assert the contract
// itself, so the next mismatch fails the build instead of silently discarding
// data for months.

// TestDbReplyTypesRegistered asserts every reply type the runner can emit is
// present in DbReplyTypes. ws_dispatch.go's switch is checked against this list
// by TestDbReplyTypesHaveDispatchCase in the main package.
func TestDbReplyTypesRegistered(t *testing.T) {
	// Every db_* / backup_dest_* message the runner sends back. Keep in step
	// with the reply sites in lattice-runner/main.go.
	runnerReplies := []string{
		"db_status",
		"db_health_status",
		"db_snapshot_status",
		"db_restore_status",
		"db_delete_snapshot_result",
		"db_schedule_status",
		"db_mirror_status",
		"db_sync",
		"backup_dest_test_result",
	}

	registered := map[string]bool{}
	for _, r := range DbReplyTypes {
		registered[r] = true
	}

	for _, reply := range runnerReplies {
		if !registered[reply] {
			t.Errorf("runner emits %q but it is not in DbReplyTypes — it will be silently dropped", reply)
		}
	}

	if len(DbReplyTypes) != len(runnerReplies) {
		t.Errorf("DbReplyTypes has %d entries, runner emits %d — the lists have drifted",
			len(DbReplyTypes), len(runnerReplies))
	}
}

// TestDbCommandsAreDistinctFromReplies guards against a command and a reply
// sharing a name, which would make the dispatch switch ambiguous.
func TestDbCommandsAreDistinctFromReplies(t *testing.T) {
	commands := []string{
		MsgDbCreate, MsgDbStart, MsgDbStop, MsgDbRestart, MsgDbRemove,
		MsgDbSnapshot, MsgDbRestore, MsgDbUpdateSchedule, MsgDbDeleteSnapshot,
		MsgDbSyncRequest, MsgBackupDestTest,
	}

	replies := map[string]bool{}
	for _, r := range DbReplyTypes {
		replies[r] = true
	}

	for _, cmd := range commands {
		if replies[cmd] {
			t.Errorf("%q is both a command and a reply type", cmd)
		}
	}
}

// TestCorrelationKeysStable pins the payload key names. The orchestrator and the
// runner are separate binaries deployed independently, so renaming one of these
// silently breaks correlation exactly the way the original bug did.
func TestCorrelationKeysStable(t *testing.T) {
	cases := map[string]string{
		PayloadDbInstanceID:   "database_instance_id",
		PayloadRequestID:      "request_id",
		PayloadIdempotencyKey: "idempotency_key",
		PayloadPhase:          "phase",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("correlation key changed: got %q, want %q", got, want)
		}
	}
}

// TestDbInstanceIDSurvivesJSONRoundTrip documents the coercion the orchestrator
// must perform.
//
// A Go int marshals to a JSON number and unmarshals into float64, never int.
// The original snapshot handler asserted .(float64) on a value the runner sent
// as a string, so the assertion always failed and every snapshot status update
// was dropped. Both encodings must be accepted.
func TestDbInstanceIDSurvivesJSONRoundTrip(t *testing.T) {
	t.Run("number", func(t *testing.T) {
		raw, err := json.Marshal(map[string]any{PayloadDbInstanceID: 42})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, ok := decoded[PayloadDbInstanceID].(float64); !ok {
			t.Fatalf("expected float64 after round trip, got %T", decoded[PayloadDbInstanceID])
		}
	})

	t.Run("string", func(t *testing.T) {
		raw, err := json.Marshal(map[string]any{PayloadDbInstanceID: "42"})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, ok := decoded[PayloadDbInstanceID].(float64); ok {
			t.Fatal("a string ID must not assert as float64 — this is the bug that dropped every snapshot update")
		}
	})
}

// TestPhaseValuesStable pins the reply phases the orchestrator branches on.
func TestPhaseValuesStable(t *testing.T) {
	if PhaseAck != "ack" || PhaseCompleted != "completed" || PhaseFailed != "failed" {
		t.Errorf("reply phase values changed: %q %q %q", PhaseAck, PhaseCompleted, PhaseFailed)
	}
}
