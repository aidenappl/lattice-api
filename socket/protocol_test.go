package socket

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMessageConstantsUnique(t *testing.T) {
	// Every message constant defined in protocol.go must be non-empty and
	// unique.  If a future change accidentally duplicates a value or leaves
	// one blank the test will catch it.
	msgs := []string{
		// Worker -> Orchestrator
		MsgHeartbeat,
		MsgContainerStatus,
		MsgContainerHealthStatus,
		MsgContainerSync,
		MsgDeploymentProgress,
		MsgDeploymentStatus,
		MsgContainerLogs,
		MsgRegistration,
		MsgWorkerActionStatus,
		MsgWorkerShutdown,
		MsgWorkerCrash,
		MsgLifecycleLog,

		// Orchestrator -> Worker
		MsgDeploy,
		MsgStart,
		MsgStop,
		MsgKill,
		MsgRestart,
		MsgPause,
		MsgUnpause,
		MsgRemove,
		MsgRecreate,
		MsgPullImage,
		MsgConnected,
		MsgRebootOS,
		MsgUpgradeRunner,
		MsgStopAll,
		MsgStartAll,
		MsgListVolumes,
		MsgCreateVolume,
		MsgRemoveVolume,
		MsgListNetworks,
		MsgCreateNetwork,
		MsgRemoveNetwork,
		MsgForceRemove,
		MsgDeploymentPing,

		// Volume/Network responses
		MsgListVolumesResponse,
		MsgListNetworksResponse,

		// Exec
		MsgExecStart,
		MsgExecInput,
		MsgExecResize,
		MsgExecClose,
		MsgExecOutput,

		// Database management: Orchestrator -> Worker
		MsgDbCreate,
		MsgDbStart,
		MsgDbStop,
		MsgDbRestart,
		MsgDbRemove,
		MsgDbSnapshot,
		MsgDbRestore,
		MsgDbUpdateSchedule,
		MsgDbDeleteSnapshot,
		MsgBackupDestTest,

		// Database management: Worker -> Orchestrator
		MsgDbStatus,
		MsgDbHealthStatus,
		MsgDbSnapshotProgress,
		MsgDbRestoreStatus,
		MsgBackupDestTestResult,

		// Admin client -> API
		MsgSubscribe,
		MsgUnsubscribe,
	}

	seen := make(map[string]bool)
	for _, m := range msgs {
		if m == "" {
			t.Error("found empty message constant")
		}
		if seen[m] {
			t.Errorf("duplicate message constant: %q", m)
		}
		seen[m] = true
	}
}

func TestEnvelopeJSON(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	env := Envelope{
		Type:      MsgDeploy,
		CommandID: "cmd-123",
		WorkerID:  "worker-1",
		IssuedAt:  &now,
		Payload: map[string]any{
			"image": "nginx:latest",
		},
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal Envelope: %v", err)
	}

	var decoded Envelope
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal Envelope: %v", err)
	}

	if decoded.Type != env.Type {
		t.Errorf("Type = %q, want %q", decoded.Type, env.Type)
	}
	if decoded.CommandID != env.CommandID {
		t.Errorf("CommandID = %q, want %q", decoded.CommandID, env.CommandID)
	}
	if decoded.WorkerID != env.WorkerID {
		t.Errorf("WorkerID = %q, want %q", decoded.WorkerID, env.WorkerID)
	}
	if decoded.IssuedAt == nil {
		t.Fatal("IssuedAt should not be nil")
	}
	if !decoded.IssuedAt.Equal(now) {
		t.Errorf("IssuedAt = %v, want %v", decoded.IssuedAt, now)
	}
	if decoded.Payload["image"] != "nginx:latest" {
		t.Errorf("Payload[image] = %v, want %q", decoded.Payload["image"], "nginx:latest")
	}
}

func TestEnvelopeOmitEmpty(t *testing.T) {
	env := Envelope{Type: MsgHeartbeat}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}

	for _, field := range []string{"command_id", "worker_id", "issued_at", "payload"} {
		if _, ok := raw[field]; ok {
			t.Errorf("field %q should be omitted when zero-value", field)
		}
	}
	if _, ok := raw["type"]; !ok {
		t.Error("field \"type\" must always be present")
	}
}

func TestIncomingMessageJSON(t *testing.T) {
	jsonStr := `{
		"type": "container_status",
		"command_id": "cmd-456",
		"worker_id": "w-2",
		"status": "running",
		"payload": {"container_id": "abc123"}
	}`

	var msg IncomingMessage
	if err := json.Unmarshal([]byte(jsonStr), &msg); err != nil {
		t.Fatalf("Unmarshal IncomingMessage: %v", err)
	}

	if msg.Type != MsgContainerStatus {
		t.Errorf("Type = %q, want %q", msg.Type, MsgContainerStatus)
	}
	if msg.CommandID != "cmd-456" {
		t.Errorf("CommandID = %q, want %q", msg.CommandID, "cmd-456")
	}
	if msg.WorkerID != "w-2" {
		t.Errorf("WorkerID = %q, want %q", msg.WorkerID, "w-2")
	}
	if msg.Status != "running" {
		t.Errorf("Status = %q, want %q", msg.Status, "running")
	}
	if msg.Payload["container_id"] != "abc123" {
		t.Errorf("Payload[container_id] = %v, want %q", msg.Payload["container_id"], "abc123")
	}
}

func TestIncomingMessageOmitEmpty(t *testing.T) {
	msg := IncomingMessage{Type: MsgHeartbeat}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}

	for _, field := range []string{"command_id", "worker_id", "status", "payload"} {
		if _, ok := raw[field]; ok {
			t.Errorf("field %q should be omitted when zero-value", field)
		}
	}
}
