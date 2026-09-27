package socket

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"

	monitor "github.com/aidenappl/go-monitor"
)

var hex16 = regexp.MustCompile(`^[0-9a-f]{16}$`)

func TestNewCommand(t *testing.T) {
	const (
		reqID   = "11111111-2222-4333-8444-555555555555"
		traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	)
	tests := []struct {
		name      string
		ctx       context.Context
		wantReq   string
		wantTrace string
	}{
		{name: "background ctx carries no ids", ctx: context.Background()},
		{name: "nil ctx is tolerated", ctx: nil},
		{name: "request ctx ids are copied", ctx: monitor.WithTraceID(monitor.WithRequestID(context.Background(), reqID), traceID), wantReq: reqID, wantTrace: traceID},
		{name: "job ctx without request ids", ctx: monitor.WithJobID(context.Background(), monitor.NewJobID())},
		{name: "invalid ctx ids are dropped", ctx: monitor.WithTraceID(monitor.WithRequestID(context.Background(), "bad id"), "<x>")},
	}
	// Every command type the API sends to a runner.
	types := []string{
		MsgDeploy, MsgStart, MsgStop, MsgKill, MsgRestart, MsgPause, MsgUnpause, MsgRemove, MsgRecreate,
		MsgRebootOS, MsgUpgradeRunner, MsgStopAll, MsgStartAll, MsgListVolumes, MsgCreateVolume,
		MsgRemoveVolume, MsgListNetworks, MsgCreateNetwork, MsgRemoveNetwork, MsgForceRemove,
		MsgDeploymentPing, MsgExecStart, MsgExecInput, MsgExecResize, MsgExecClose, MsgDbCreate,
		MsgDbStart, MsgDbStop, MsgDbRestart, MsgDbRemove, MsgDbSnapshot, MsgDbRestore,
		MsgDbUpdateSchedule, MsgDbDeleteSnapshot, MsgDbMirrorSnapshot, MsgBackupDestTest, MsgDbSyncRequest,
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := map[string]bool{}
			for _, typ := range types {
				env := NewCommand(tt.ctx, typ, map[string]any{"k": "v"})
				if env.Type != typ || env.Payload["k"] != "v" {
					t.Errorf("%s: type/payload not carried: %+v", typ, env)
				}
				if !hex16.MatchString(env.CommandID) || !monitor.ValidCorrelationID(env.CommandID) {
					t.Errorf("%s: command_id %q is not a valid 16-hex id", typ, env.CommandID)
				}
				if seen[env.CommandID] {
					t.Errorf("%s: command_id %q reused", typ, env.CommandID)
				}
				seen[env.CommandID] = true
				if env.RequestID != tt.wantReq || env.TraceID != tt.wantTrace {
					t.Errorf("%s: ids = (%q, %q), want (%q, %q)", typ, env.RequestID, env.TraceID, tt.wantReq, tt.wantTrace)
				}
			}
		})
	}
}

func TestEnvelopeWireFields(t *testing.T) {
	ctx := monitor.WithTraceID(monitor.WithRequestID(context.Background(), "0123456789abcdef"), "4bf92f3577b34da6a3ce929d0e0e4736")
	b, err := json.Marshal(NewCommand(ctx, MsgDeploy, nil))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"type", "command_id", "request_id", "trace_id"} {
		if _, ok := m[k]; !ok {
			t.Errorf("wire envelope missing %q: %s", k, b)
		}
	}

	// Without ids the new fields are omitted, so the wire shape is unchanged
	// for anything that carries none.
	b, _ = json.Marshal(NewCommand(context.Background(), MsgDeploy, nil))
	m = nil
	_ = json.Unmarshal(b, &m)
	if _, ok := m["request_id"]; ok {
		t.Errorf("empty request_id should be omitted: %s", b)
	}
	if _, ok := m["trace_id"]; ok {
		t.Errorf("empty trace_id should be omitted: %s", b)
	}
}

func TestIncomingMessageContext(t *testing.T) {
	const (
		cmd   = "fedcba9876543210"
		req   = "0123456789abcdef"
		trace = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	)
	tests := []struct {
		name      string
		raw       string
		wantReq   string
		wantTrace string
	}{
		{name: "unsolicited message has no ids", raw: `{"type":"heartbeat"}`},
		{name: "reply echoes all ids", raw: `{"type":"db_status","command_id":"` + cmd + `","request_id":"` + req + `","trace_id":"` + trace + `"}`, wantReq: req, wantTrace: trace},
		{name: "command_id stands in for a missing request_id", raw: `{"type":"db_status","command_id":"` + cmd + `"}`, wantReq: cmd},
		{name: "invalid request_id falls back to command_id", raw: `{"type":"db_status","command_id":"` + cmd + `","request_id":"nope!"}`, wantReq: cmd},
		{name: "invalid ids are dropped", raw: `{"type":"db_status","command_id":"x y","request_id":"nope!","trace_id":"<t>"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg IncomingMessage
			if err := json.Unmarshal([]byte(tt.raw), &msg); err != nil {
				t.Fatal(err)
			}
			ctx := msg.Context(context.Background())
			if got := monitor.RequestID(ctx); got != tt.wantReq {
				t.Errorf("request_id = %q, want %q", got, tt.wantReq)
			}
			if got := monitor.TraceID(ctx); got != tt.wantTrace {
				t.Errorf("trace_id = %q, want %q", got, tt.wantTrace)
			}
		})
	}
}
